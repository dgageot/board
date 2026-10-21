package board

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dgageot/board/pkg/loops"
)

func deleteLoopRequest(t *testing.T, b *Board, id string) *httptest.ResponseRecorder {
	t.Helper()
	mux, err := buildMux(b)
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/loops/"+id, http.NoBody))
	return rec
}

func deletionService(t *testing.T, b *Board) {
	t.Helper()
	service, err := loops.NewService(t.Context(), b.loops, &loops.ProcessRunner{Directory: t.TempDir()}, "auto", func() {})
	require.NoError(t, err)
	t.Cleanup(service.Close)
	b.loopService = service
}

func TestDeleteLoopRemovesHistoryAndPreservesProjectWork(t *testing.T) {
	b, store, loop, project := taskFixture(t)
	deletionService(t, b)
	task, card := reservedTask(t, store, loop.ID)
	other, err := b.loops.Create(t.Context(), loops.Loop{Name: "Other", Description: "Other task", Criteria: "Good"})
	require.NoError(t, err)
	sessions := &closeTaskSessions{}
	b.sessions = sessions
	b.controller.sessions = sessions
	b.controller.Start(card)
	_, err = store.db.Exec(`INSERT INTO loop_jobs(id,loop_id,version_id,kind,status,input,created_at) VALUES ('job',?,'workflow','run','succeeded','','now')`, loop.ID)
	require.NoError(t, err)
	_, err = store.db.Exec(`INSERT INTO loop_feedback(id,loop_id,job_id,rating,note,created_at) VALUES ('feedback',?,'job','good','','now')`, loop.ID)
	require.NoError(t, err)
	_, err = store.db.Exec(`INSERT INTO loop_rounds(id,loop_id,status,data) VALUES ('round',?,'completed','{}')`, loop.ID)
	require.NoError(t, err)
	rec := deleteLoopRequest(t, b, loop.ID)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Equal(t, 2, sessions.kills)
	for _, table := range []string{"loops", "loop_versions", "loop_jobs", "loop_rounds", "loop_feedback"} {
		column := "loop_id"
		if table == "loops" {
			column = "id"
		}
		var n int
		require.NoError(t, store.db.Get(&n, `SELECT COUNT(*) FROM `+table+` WHERE `+column+`=?`, loop.ID))
		assert.Zero(t, n, table)
	}
	var n int
	require.NoError(t, store.db.Get(&n, `SELECT COUNT(*) FROM loop_run_reviews WHERE task_id=?`, task.ID))
	assert.Zero(t, n)
	saved, err := store.GetCard(card.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusClosed, saved.Status)
	retained, err := store.GetProjectTask(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "closed", retained.Status)
	assert.FileExists(t, filepath.Join(card.Worktree, "result.txt"))
	_, err = store.GetProject(project.ID)
	require.NoError(t, err)
	_, err = b.loops.Get(t.Context(), other.ID)
	require.NoError(t, err)
	require.NoError(t, b.controller.ReconcileAll())
	assert.Empty(t, b.controller.watchers)
	require.Error(t, b.controller.SendPrompt(saved, "do more"))
	_, err = b.startCoach(t.Context(), saved)
	require.Error(t, err)
	assert.Equal(t, http.StatusNotFound, deleteLoopRequest(t, b, loop.ID).Code)
}

func TestDeleteLoopStopFailureRetainsRecoveryRecordsForRetry(t *testing.T) {
	b, store, loop, _ := taskFixture(t)
	deletionService(t, b)
	task, card := reservedTask(t, store, loop.ID)
	sessions := &closeTaskSessions{fail: true}
	b.sessions = sessions
	b.controller.sessions = sessions
	rec := deleteLoopRequest(t, b, loop.ID)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	detail, err := b.loops.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	assert.True(t, detail.Loop.Deleting)
	assert.Len(t, detail.Versions, 1)
	saved, err := store.GetProjectTask(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "closing", saved.Status)
	_, err = store.RunReview(task.ID)
	require.NoError(t, err)
	require.NoError(t, b.controller.ReconcileAll())
	assert.Empty(t, b.controller.watchers)
	assert.FileExists(t, filepath.Join(card.Worktree, "result.txt"))
	_, err = b.loopService.Generate(t.Context(), loop.ID)
	require.Error(t, err)
	sessions.fail = false
	require.Equal(t, http.StatusNoContent, deleteLoopRequest(t, b, loop.ID).Code)
}

func TestDeleteLoopMissingUnclosedCardDoesNotDiscardRecovery(t *testing.T) {
	b, store, loop, _ := taskFixture(t)
	deletionService(t, b)
	task, card := reservedTask(t, store, loop.ID)
	require.NoError(t, store.DeleteCard(card.ID))
	require.Equal(t, http.StatusInternalServerError, deleteLoopRequest(t, b, loop.ID).Code)
	_, err := store.GetProjectTask(task.ID)
	require.NoError(t, err)
	require.NoError(t, store.UpdateProjectTask(task.ID, "closed", ""))
	require.Equal(t, http.StatusNoContent, deleteLoopRequest(t, b, loop.ID).Code)
}

func TestDeleteLoopRollsBackHistoryIfDeletionFails(t *testing.T) {
	b, store, loop, _ := taskFixture(t)
	deletionService(t, b)
	task, _ := reservedTask(t, store, loop.ID)
	_, err := store.db.Exec(`CREATE TRIGGER reject_delete BEFORE DELETE ON loops BEGIN SELECT RAISE(FAIL,'storage failure'); END`)
	require.NoError(t, err)
	require.Equal(t, http.StatusInternalServerError, deleteLoopRequest(t, b, loop.ID).Code)
	detail, err := b.loops.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	assert.Len(t, detail.Versions, 1)
	_, err = store.RunReview(task.ID)
	require.NoError(t, err)
	saved, err := store.GetProjectTask(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "closed", saved.Status)
	_, err = store.db.Exec(`DROP TRIGGER reject_delete`)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, deleteLoopRequest(t, b, loop.ID).Code)
}

func TestDeletionPreventsLatePreparationAndNewTaskReservation(t *testing.T) {
	worker, task := learningFixture(t)
	require.NoError(t, worker.store.beginLoopDelete(t.Context(), task.LoopID))
	require.NoError(t, worker.processNext(t.Context())) // No learner is needed: deletion excludes it.
	_, err := worker.store.db.Exec(`UPDATE loop_run_reviews SET state='processing' WHERE task_id=?`, task.ID)
	require.NoError(t, err)
	review, err := worker.store.RunReview(task.ID)
	require.NoError(t, err)
	require.Error(t, worker.publish(t.Context(), *task, review, learningResult()))
	next := *task
	next.ID = "new"
	next.CardID = "new-card"
	require.Error(t, worker.store.ReserveProjectTask(&next, &Card{ID: next.CardID}))
}

func TestStartupAfterDeleteAdmissionClosesTaskWithoutResuming(t *testing.T) {
	b, store, loop, _ := taskFixture(t)
	deletionService(t, b)
	task, card := reservedTask(t, store, loop.ID)
	require.NoError(t, store.beginLoopDelete(t.Context(), loop.ID))
	sessions := &closeTaskSessions{}
	restarted := newController(t.Context(), store, sessions, func() {})
	require.NoError(t, restarted.ReconcileAll())
	assert.Empty(t, restarted.watchers)
	assert.Equal(t, 2, sessions.kills)
	saved, err := store.GetProjectTask(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "closed", saved.Status)
	assert.FileExists(t, filepath.Join(card.Worktree, "result.txt"))
	detail, err := b.loops.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	assert.True(t, detail.Loop.Deleting)
	require.Equal(t, http.StatusNoContent, deleteLoopRequest(t, b, loop.ID).Code)
}

func TestDeleteLoopRejectsCrossOriginRequests(t *testing.T) {
	b, _, loop, _ := taskFixture(t)
	deletionService(t, b)
	mux, err := buildMux(b)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodDelete, "http://localhost/api/loops/"+loop.ID, http.NoBody)
	req.Header.Set("Origin", "https://untrusted.example")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	detail, err := b.loops.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	assert.False(t, detail.Loop.Deleting)
}
