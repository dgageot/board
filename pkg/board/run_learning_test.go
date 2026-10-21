package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dgageot/board/pkg/loops"
)

type fakeRunLearner struct {
	learn func(context.Context, string, loops.RunLearningRequest) (loops.RunLearningResult, error)
}

func (f fakeRunLearner) LearnRun(ctx context.Context, id string, r loops.RunLearningRequest) (loops.RunLearningResult, error) {
	return f.learn(ctx, id, r)
}

func learningFixture(t *testing.T) (*runLearningWorker, *ProjectTask) {
	t.Helper()
	b, store, loop, _ := taskFixture(t)
	task, _ := reservedTask(t, store, loop.ID)
	task.VersionID = "workflow"
	_, err := store.db.Exec(`UPDATE loop_project_tasks SET version_id='workflow' WHERE id=?`, task.ID)
	require.NoError(t, err)
	review := RunReview{State: "pending", StartedAt: "2026-10-21T10:00:00Z", FinishedAt: "2026-10-21T10:00:05Z", Complete: true, Outcome: "normal"}
	data, err := json.Marshal(review)
	require.NoError(t, err)
	_, err = store.db.Exec(`UPDATE loop_run_reviews SET state='pending',data=? WHERE task_id=?`, string(data), task.ID)
	require.NoError(t, err)
	return &runLearningWorker{store: store, loops: b.loops, changed: func() {}}, task
}

func learningResult() loops.RunLearningResult {
	return loops.RunLearningResult{
		Assessment: json.RawMessage(`{"quality":"failed","efficiency":"inefficient","changeImpact":"hurt","summary":"Tests were not run.","improvements":["Run tests before committing"]}`),
		YAML:       "agents:\n  root:\n    model: auto\n    instruction: Execute and verify the dependency update. Run tests before committing.\n", Rationale: "Enforce verification",
	}
}

func TestLearningPublishesUntestedNextVersionExactlyOnce(t *testing.T) {
	worker, task := learningFixture(t)
	calls := 0
	worker.learner = fakeRunLearner{learn: func(_ context.Context, id string, r loops.RunLearningRequest) (loops.RunLearningResult, error) {
		calls++
		assert.Equal(t, task.LoopID, id)
		assert.Equal(t, "workflow", r.Parent.ID)
		return learningResult(), nil
	}}
	require.NoError(t, worker.processNext(t.Context()))
	review, err := worker.store.RunReview(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "ready", review.State)
	assert.Equal(t, "hurt", review.Assessment.ChangeImpact)
	require.NotEmpty(t, review.NextVersion)
	detail, err := worker.loops.Get(t.Context(), task.LoopID)
	require.NoError(t, err)
	assert.Equal(t, review.NextVersion, detail.Loop.ActiveVersion)
	assert.Len(t, detail.Versions, 2)
	require.NoError(t, worker.processNext(t.Context()))
	assert.Equal(t, 1, calls)
}

func TestNewerRunPreventsStaleNextVersionPublication(t *testing.T) {
	worker, task := learningFixture(t)
	worker.learner = fakeRunLearner{learn: func(_ context.Context, _ string, _ loops.RunLearningRequest) (loops.RunLearningResult, error) {
		newer := *task
		newer.ID = "newer"
		newer.CardID = "newer-card"
		newer.Status = "reserved"
		require.NoError(t, worker.store.ReserveProjectTask(&newer, &Card{ID: newer.CardID, Title: "New run", Status: StatusStarting}))
		return learningResult(), nil
	}}
	require.NoError(t, worker.processNext(t.Context()))
	review, err := worker.store.RunReview(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "superseded", review.State)
	assert.Empty(t, review.NextVersion)
	assert.NotNil(t, review.Assessment)
}

func TestJudgeFailureKeepsRunEvidenceAndCurrentAgent(t *testing.T) {
	worker, task := learningFixture(t)
	worker.learner = fakeRunLearner{learn: func(context.Context, string, loops.RunLearningRequest) (loops.RunLearningResult, error) {
		return loops.RunLearningResult{}, errors.New("provider unavailable")
	}}
	require.NoError(t, worker.processNext(t.Context()))
	review, err := worker.store.RunReview(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "failed", review.State)
	assert.Contains(t, review.Error, "provider unavailable")
	assert.NotEmpty(t, review.FinishedAt)
	detail, err := worker.loops.Get(t.Context(), task.LoopID)
	require.NoError(t, err)
	assert.Len(t, detail.Versions, 1)
}

func TestPublicationFailureRetainsAssessmentAndRollsBackVersion(t *testing.T) {
	worker, task := learningFixture(t)
	worker.learner = fakeRunLearner{learn: func(context.Context, string, loops.RunLearningRequest) (loops.RunLearningResult, error) {
		result := learningResult()
		result.JudgeJob = "judge"
		result.RevisionJobs = []string{"patch"}
		return result, nil
	}}
	_, err := worker.store.db.Exec(`CREATE TRIGGER reject_next BEFORE INSERT ON loop_versions BEGIN SELECT RAISE(FAIL,'disk error'); END`)
	require.NoError(t, err)
	require.NoError(t, worker.processNext(t.Context()))
	review, err := worker.store.RunReview(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "failed", review.State)
	assert.NotNil(t, review.Assessment)
	assert.Equal(t, "judge", review.JudgeJob)
	assert.Equal(t, []string{"patch"}, review.RevisionJobs)
	detail, err := worker.loops.Get(t.Context(), task.LoopID)
	require.NoError(t, err)
	assert.Len(t, detail.Versions, 1)
}

func TestCanceledRevisionRetainsCompletedJudgment(t *testing.T) {
	worker, task := learningFixture(t)
	worker.learner = fakeRunLearner{learn: func(context.Context, string, loops.RunLearningRequest) (loops.RunLearningResult, error) {
		result := learningResult()
		result.JudgeJob = "judge"
		result.RevisionJobs = []string{"patch"}
		return result, context.Canceled
	}}
	require.NoError(t, worker.processNext(t.Context()))
	review, err := worker.store.RunReview(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "interrupted", review.State)
	assert.NotNil(t, review.Assessment)
	assert.Equal(t, "judge", review.JudgeJob)
}

func TestInvalidAssessmentRetainsJobLinkage(t *testing.T) {
	worker, task := learningFixture(t)
	worker.learner = fakeRunLearner{learn: func(context.Context, string, loops.RunLearningRequest) (loops.RunLearningResult, error) {
		result := learningResult()
		result.Assessment = json.RawMessage(`{"quality":"invalid"}`)
		result.JudgeJob = "judge"
		result.RevisionJobs = []string{"revision"}
		return result, nil
	}}
	require.NoError(t, worker.processNext(t.Context()))
	review, err := worker.store.RunReview(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "failed", review.State)
	assert.Equal(t, []string{"judge"}, review.JudgeJobs)
	assert.Equal(t, []string{"revision"}, review.RevisionJobs)
}

func TestManualAgentChangeSupersedesPreparation(t *testing.T) {
	worker, task := learningFixture(t)
	worker.learner = fakeRunLearner{learn: func(context.Context, string, loops.RunLearningRequest) (loops.RunLearningResult, error) {
		_, err := worker.store.db.Exec(`UPDATE loops SET active_version='workflow' WHERE id=?`, task.LoopID)
		require.NoError(t, err)
		return learningResult(), nil
	}}
	require.NoError(t, worker.processNext(t.Context()))
	review, err := worker.store.RunReview(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "superseded", review.State)
	assert.Empty(t, review.NextVersion)
}

func TestMissingStopOnCloseKeepsUnknownEvidence(t *testing.T) {
	worker, task := learningFixture(t)
	_, err := worker.store.db.Exec(`UPDATE loop_run_reviews SET state='observing',data='{}' WHERE task_id=?`, task.ID)
	require.NoError(t, err)
	require.NoError(t, worker.store.interruptRunReview(task.CardID, "canceled"))
	review, err := worker.store.RunReview(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "pending", review.State)
	assert.False(t, review.Complete)
	assert.Nil(t, review.Cost)
	assert.Nil(t, review.DurationMS)
}

func TestBoundedHistoryKeepsLearningFromAllRuns(t *testing.T) {
	worker, task := learningFixture(t)
	for i := range 55 {
		id := fmt.Sprintf("history-%d", i)
		_, err := worker.store.db.Exec(`INSERT INTO loop_project_tasks(id,loop_id,project_id,project_name,repo_path,agent,task,criteria,prompt,card_id,base,status,error,created_at,version_id) SELECT ?,loop_id,project_id,project_name,repo_path,agent,task,criteria,prompt,?,base,status,error,created_at,version_id FROM loop_project_tasks WHERE id=?`, id, id, task.ID)
		require.NoError(t, err)
		data := `{"complete":true,"assessment":{"quality":"failed","efficiency":"mixed","changeImpact":"hurt","summary":"Missing tests","improvements":["Run tests"]}}`
		_, err = worker.store.db.Exec(`INSERT INTO loop_run_reviews(task_id,state,data) VALUES (?,'ready',?)`, id, data)
		require.NoError(t, err)
	}
	last := *task
	last.ID = "history-54"
	data, err := worker.history(t.Context(), last)
	require.NoError(t, err)
	var history struct {
		RecentRuns []any `json:"recentRuns"`
		Omitted    int   `json:"omittedOlderRuns"`
		AllRuns    struct {
			Count   int            `json:"count"`
			Quality map[string]int `json:"quality"`
		} `json:"allRuns"`
	}
	require.NoError(t, json.Unmarshal(data, &history))
	assert.Len(t, history.RecentRuns, 50)
	assert.Equal(t, 5, history.Omitted)
	assert.Equal(t, 55, history.AllRuns.Count)
	assert.Equal(t, 54, history.AllRuns.Quality["failed"])
}

func TestStartupInterruptsPreparationWithoutReplayingTask(t *testing.T) {
	worker, task := learningFixture(t)
	_, err := worker.store.db.Exec(`UPDATE loop_run_reviews SET state='processing' WHERE task_id=?`, task.ID)
	require.NoError(t, err)
	service, err := loops.NewService(t.Context(), worker.loops, &loops.ProcessRunner{Directory: t.TempDir()}, "auto", func() {})
	require.NoError(t, err)
	defer service.Close()
	b := &Board{store: worker.store, loops: worker.loops, loopService: service}
	stop := b.startRunLearning(t.Context())
	stop()
	review, err := worker.store.RunReview(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "interrupted", review.State)
	jobs, err := worker.loops.Jobs(t.Context(), task.LoopID)
	require.NoError(t, err)
	assert.Empty(t, jobs)
}

func TestRetryPreparationOnlyQueuesRecordedRun(t *testing.T) {
	worker, task := learningFixture(t)
	_, err := worker.store.db.Exec(`UPDATE loop_run_reviews SET state='interrupted' WHERE task_id=?`, task.ID)
	require.NoError(t, err)
	b := &Board{store: worker.store, loops: worker.loops}
	req := httptest.NewRequest(http.MethodPost, "/prepare", http.NoBody)
	req.SetPathValue("id", task.LoopID)
	req.SetPathValue("task", task.ID)
	rec := httptest.NewRecorder()
	b.handleRetryRunLearning(rec, req)
	assert.Equal(t, http.StatusAccepted, rec.Code)
	review, err := worker.store.RunReview(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "pending", review.State)
	rec = httptest.NewRecorder()
	b.handleRetryRunLearning(rec, req)
	assert.Equal(t, http.StatusConflict, rec.Code)
}
