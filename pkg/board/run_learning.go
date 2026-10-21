package board

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/dgageot/board/pkg/loops"
)

type runLearner interface {
	LearnRun(ctx context.Context, loopID string, request loops.RunLearningRequest) (loops.RunLearningResult, error)
}

// runLearningWorker processes one task at a time; task execution is never replayed.
type runLearningWorker struct {
	store   *SQLiteStore
	loops   *loops.Store
	learner runLearner
	changed func()
}

func (b *Board) startRunLearning(ctx context.Context) func() {
	store, ok := b.store.(*SQLiteStore)
	if !ok || b.loopService == nil {
		return func() {}
	}
	worker := runLearningWorker{store: store, loops: b.loops, learner: b.loopService, changed: b.broadcast}
	var wg sync.WaitGroup
	workerCtx, cancel := context.WithCancel(ctx)
	// A model request interrupted by a previous process is not automatically replayed.
	if _, err := store.db.Exec(`UPDATE loop_run_reviews SET state='interrupted' WHERE state='processing'`); err != nil {
		log.Printf("recover run learning: %v", err)
	}
	wg.Go(func() {
		timer := time.NewTicker(time.Second)
		defer timer.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-timer.C:
			}
			if err := worker.processNext(workerCtx); err != nil && workerCtx.Err() == nil {
				log.Printf("run learning: %v", err)
			}
		}
	})
	return func() { cancel(); wg.Wait() }
}

func (w *runLearningWorker) processNext(ctx context.Context) error {
	var taskID string
	err := w.store.db.GetContext(ctx, &taskID, `SELECT task_id FROM loop_run_reviews WHERE state='pending' ORDER BY rowid LIMIT 1`)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	result, err := w.store.db.ExecContext(ctx, `UPDATE loop_run_reviews SET state='processing' WHERE task_id=? AND state='pending'`, taskID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	w.changed()
	task, err := w.store.GetProjectTask(taskID)
	if err != nil {
		return w.finishError(taskID, err)
	}
	review, err := w.store.RunReview(taskID)
	if err != nil {
		return w.finishError(taskID, err)
	}
	detail, err := w.loops.Get(ctx, task.LoopID)
	if err != nil {
		return w.finishError(taskID, err)
	}
	var parent loops.Version
	for _, version := range detail.Versions {
		if version.ID == task.VersionID {
			parent = version
			break
		}
	}
	if parent.ID == "" {
		return w.finishError(taskID, errors.New("executed agent version unavailable"))
	}
	history, err := w.history(ctx, *task)
	if err != nil {
		return w.finishError(taskID, err)
	}
	evidence, err := json.Marshal(review)
	if err != nil {
		return w.finishError(taskID, err)
	}
	learningCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	learned, err := w.learner.LearnRun(learningCtx, task.LoopID, loops.RunLearningRequest{RunID: task.ID, Project: task.ProjectID, Base: task.Base, Complete: review.Complete && !review.Truncated, Task: task.Task, Criteria: task.Criteria, Parent: parent, Evidence: evidence, History: history})
	if learned.JudgeJob != "" {
		review.JudgeJob = learned.JudgeJob
		review.JudgeJobs = append(review.JudgeJobs, learned.JudgeJob)
	}
	review.RevisionJobs = append(review.RevisionJobs, learned.RevisionJobs...)
	if len(learned.Assessment) > 0 {
		var partial RunAssessment
		if json.Unmarshal(learned.Assessment, &partial) == nil && validAssessment(partial) == nil {
			review.Assessment = &partial
		}
	}
	if err != nil {
		return w.finishReviewError(taskID, review, err)
	}
	var assessment RunAssessment
	if err := json.Unmarshal(learned.Assessment, &assessment); err != nil {
		return w.finishReviewError(taskID, review, err)
	}
	if err := validAssessment(assessment); err != nil {
		return w.finishReviewError(taskID, review, err)
	}
	if !review.Complete || review.Truncated {
		assessment.Quality = "unknown"
		assessment.Efficiency = "unknown"
		assessment.ChangeImpact = "inconclusive"
	}
	review.Assessment = &assessment
	review.Error = learned.Error
	review.State = "ready"
	if learned.YAML == "" {
		review.State = "revision_failed"
	}
	if err := w.publish(ctx, *task, review, learned); err != nil {
		return w.finishReviewError(taskID, review, err)
	}
	return nil
}

func (w *runLearningWorker) finishError(taskID string, cause error) error {
	review, err := w.store.RunReview(taskID)
	if err != nil {
		return err
	}
	return w.finishReviewError(taskID, review, cause)
}

func (w *runLearningWorker) finishReviewError(taskID string, review RunReview, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	review.Error = cause.Error()
	review.State = "failed"
	review.NextVersion = ""
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		review.State = "interrupted"
	}
	data, err := json.Marshal(review)
	if err != nil {
		return err
	}
	_, err = w.store.db.ExecContext(ctx, `UPDATE loop_run_reviews SET state=?,data=? WHERE task_id=? AND state='processing'`, review.State, string(data), taskID)
	w.changed()
	return err
}

func (w *runLearningWorker) history(ctx context.Context, task ProjectTask) (json.RawMessage, error) {
	rows, err := w.store.db.QueryContext(ctx, `SELECT t.id,t.project_id,t.version_id,t.base,json_extract(r.data,'$.outcome'),json_extract(r.data,'$.cost'),json_extract(r.data,'$.durationMs'),json_extract(r.data,'$.assessment'),json_extract(r.data,'$.complete'),json_extract(r.data,'$.truncated') FROM loop_project_tasks t JOIN loop_run_reviews r ON r.task_id=t.id WHERE t.loop_id=? AND t.rowid < (SELECT rowid FROM loop_project_tasks WHERE id=?) ORDER BY t.rowid DESC`, task.LoopID, task.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	quality := map[string]int{}
	efficiency := map[string]int{}
	impact := map[string]int{}
	suggestions := map[string]int{}
	total := 0
	omitted := 0
	used := 0
	for rows.Next() {
		var id, project, version, base string
		var outcome, cost, duration, assessment, complete, truncated any
		if err := rows.Scan(&id, &project, &version, &base, &outcome, &cost, &duration, &assessment, &complete, &truncated); err != nil {
			return nil, err
		}
		total++
		var verdict RunAssessment
		if text, ok := assessment.(string); ok && json.Unmarshal([]byte(text), &verdict) == nil && verdict.Validate() == nil {
			quality[verdict.Quality]++
			efficiency[verdict.Efficiency]++
			impact[verdict.ChangeImpact]++
			for _, hint := range verdict.Improvements {
				suggestions[hint]++
			}
		}
		item := map[string]any{"run": id, "project": project, "version": version, "base": base, "outcome": outcome, "cost": cost, "durationMs": duration, "assessment": assessment, "complete": complete, "truncated": truncated}
		data, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		if used+len(data) > 192*1024 || len(items) >= 50 {
			omitted++
			continue
		}
		used += len(data)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	type recurring struct {
		Suggestion string `json:"suggestion"`
		Count      int    `json:"count"`
	}
	themes := []recurring{}
	for hint, count := range suggestions {
		themes = append(themes, recurring{hint, count})
	}
	slices.SortFunc(themes, func(a, b recurring) int {
		if c := cmp.Compare(b.Count, a.Count); c != 0 {
			return c
		}
		return cmp.Compare(a.Suggestion, b.Suggestion)
	})
	if len(themes) > 12 {
		themes = themes[:12]
	}
	return json.Marshal(map[string]any{"allRuns": map[string]any{"count": total, "quality": quality, "efficiency": efficiency, "changeImpact": impact, "recurringSuggestions": themes, "note": "Aggregate includes all preceding runs, across projects and versions; not a controlled comparison."}, "recentRuns": items, "omittedOlderRuns": omitted, "contract": "Same immutable loop task and criteria as the current run"})
}

func (w *runLearningWorker) publish(ctx context.Context, task ProjectTask, review RunReview, result loops.RunLearningResult) error {
	if result.YAML != "" {
		if _, err := loops.ProjectInstruction(loops.Version{YAML: result.YAML, Mode: loops.ModeProject}); err != nil {
			return err
		}
	}
	err := runInTx(w.store.db, func(tx *sqlx.Tx) error {
		var state, active, mode string
		if err := tx.GetContext(ctx, &state, `SELECT state FROM loop_run_reviews WHERE task_id=?`, task.ID); err != nil {
			return err
		}
		if state != "processing" {
			return errors.New("run learning was superseded")
		}
		if err := tx.GetContext(ctx, &active, `SELECT active_version FROM loops WHERE id=?`, task.LoopID); err != nil {
			return err
		}
		if err := tx.GetContext(ctx, &mode, `SELECT mode FROM loops WHERE id=?`, task.LoopID); err != nil {
			return err
		}
		var newer bool
		if err := tx.GetContext(ctx, &newer, `SELECT EXISTS(SELECT 1 FROM loop_project_tasks WHERE loop_id=? AND rowid > (SELECT rowid FROM loop_project_tasks WHERE id=?))`, task.LoopID, task.ID); err != nil {
			return err
		}
		if result.YAML != "" && !newer && active == review.ExpectedActive && mode == loops.ModeProject {
			id := newID()
			_, err := tx.ExecContext(ctx, `INSERT INTO loop_versions(id,loop_id,number,yaml,rationale,created_at,mode) SELECT ?,?,COALESCE(MAX(number),0)+1,?,?,?,'project' FROM loop_versions WHERE loop_id=?`, id, task.LoopID, result.YAML, result.Rationale, time.Now().UTC().Format(time.RFC3339Nano), task.LoopID)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE loops SET active_version=? WHERE id=?`, id, task.LoopID); err != nil {
				return err
			}
			review.NextVersion = id
		} else if result.YAML != "" {
			review.State = "superseded"
		}
		data, err := json.Marshal(review)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE loop_run_reviews SET state=?,data=? WHERE task_id=?`, review.State, string(data), task.ID)
		return err
	})
	w.changed()
	if err != nil {
		return fmt.Errorf("publish run learning: %w", err)
	}
	return nil
}
