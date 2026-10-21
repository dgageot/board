package board

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
)

// Initial runs are scheduled from durable creation intents, never by browser polling.
func (b *Board) startInitialRuns(ctx context.Context) func() {
	if _, ok := b.store.(*SQLiteStore); !ok {
		return func() {}
	}
	workerCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Go(func() {
		timer := time.NewTicker(time.Second)
		defer timer.Stop()
		for {
			if err := b.processInitialRun(workerCtx); err != nil && workerCtx.Err() == nil {
				log.Printf("initial loop run: %v", err)
			}
			select {
			case <-workerCtx.Done():
				return
			case <-timer.C:
			}
		}
	})
	return func() { cancel(); wg.Wait() }
}

func (b *Board) processInitialRun(ctx context.Context) error {
	store, ok := b.store.(*SQLiteStore)
	if !ok {
		return nil
	}
	// Share the manual launch and deletion lock, including the final eligibility check.
	for !b.taskLaunch.TryLock() {
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer b.taskLaunch.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	var next struct {
		LoopID        string `db:"loop_id"`
		TaskID        string `db:"task_id"`
		JobID         string `db:"job_id"`
		ProjectID     string `db:"project_id"`
		RepoPath      string `db:"repo_path"`
		ExecutionPath string `db:"execution_path"`
		Agent         string `db:"agent"`
		Status        string `db:"status"`
		VersionID     string `db:"version_id"`
		Error         string `db:"error"`
	}
	err := store.db.GetContext(ctx, &next, `SELECT i.loop_id,i.task_id,i.job_id,i.project_id,i.repo_path,i.execution_path,i.agent,COALESCE(j.status,'missing') AS status,COALESCE(j.version_id,'') AS version_id,COALESCE(j.error,'') AS error FROM loop_initial_runs i JOIN loops l ON l.id=i.loop_id LEFT JOIN loop_jobs j ON j.id=i.job_id AND j.loop_id=i.loop_id WHERE i.state='waiting' AND l.deleting=0 AND (j.status NOT IN ('queued','running') OR j.id IS NULL) ORDER BY i.rowid LIMIT 1`)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	finish := func(cause error) error {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		// No external work has been admitted, so preserve the failure for a manual retry.
		result, err := store.db.ExecContext(ctx, `UPDATE loop_initial_runs SET state='failed',error=? WHERE loop_id=? AND job_id=? AND state='waiting'`, cause.Error(), next.LoopID, next.JobID)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			_, saveErr := store.db.ExecContext(ctx, `UPDATE loop_initial_runs SET error=? WHERE loop_id=? AND state='admitted'`, cause.Error(), next.LoopID)
			b.broadcast()
			return errors.Join(cause, saveErr)
		}
		b.broadcast()
		return nil
	}
	if next.Status != "succeeded" {
		return finish(fmt.Errorf("initial agent generation %s: %s", next.Status, next.Error))
	}
	if next.VersionID == "" {
		return finish(errors.New("initial agent generation returned no version"))
	}
	_, _, err = b.runLoopProjectTaskLocked(ctx, next.LoopID, loopTaskRequest{RequestID: next.TaskID, VersionID: next.VersionID, ProjectID: next.ProjectID, RepoPath: next.RepoPath, Agent: next.Agent, executionPath: next.ExecutionPath})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return finish(err)
	}
	return nil
}

func reserveInitialRun(ctx context.Context, tx *sqlx.Tx, task *ProjectTask) error {
	var initialID string
	err := tx.GetContext(ctx, &initialID, `SELECT task_id FROM loop_initial_runs WHERE loop_id=?`, task.LoopID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if initialID != task.ID {
		_, err := tx.ExecContext(ctx, `UPDATE loop_initial_runs SET state='superseded',error='' WHERE loop_id=? AND state IN ('waiting','failed')`, task.LoopID)
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE loop_initial_runs SET state='admitted',error='' WHERE loop_id=? AND task_id=? AND state='waiting' AND EXISTS(SELECT 1 FROM loop_jobs j JOIN loops l ON l.id=j.loop_id JOIN projects p ON p.id=loop_initial_runs.project_id WHERE j.id=loop_initial_runs.job_id AND j.status='succeeded' AND j.version_id=? AND l.mode='project' AND l.deleting=0 AND (l.active_version='' OR l.active_version=?) AND p.id=? AND loop_initial_runs.execution_path=? AND p.repo_path=loop_initial_runs.repo_path AND p.agent=loop_initial_runs.agent)`, task.LoopID, task.ID, task.VersionID, task.VersionID, task.ProjectID, task.RepoPath)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%w: initial run changed or was already admitted", errBadInput)
	}
	_, err = tx.ExecContext(ctx, `UPDATE loops SET active_version=? WHERE id=? AND active_version=''`, task.VersionID, task.LoopID)
	return err
}
