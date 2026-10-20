package board

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type closeTaskSessions struct {
	noopSessionManager

	kills int
	fail  bool
}

func (s *closeTaskSessions) KillSession(string) error {
	s.kills++
	if s.fail {
		return errors.New("tmux unavailable")
	}
	return nil
}

func reservedTask(t *testing.T, store *SQLiteStore, loopID string) (*ProjectTask, *Card) {
	t.Helper()
	card := &Card{ID: "close-card", Title: "Task", Session: "close-session", AgentSession: "session", Status: StatusRunning, Worktree: t.TempDir(), Branch: "retained-branch"}
	task := &ProjectTask{ID: "close-task", LoopID: loopID, CardID: card.ID, Status: "launched"}
	require.NoError(t, store.ReserveProjectTask(task, card))
	require.NoError(t, os.WriteFile(filepath.Join(card.Worktree, "result.txt"), []byte("keep"), 0o600))
	return task, card
}

func closeTaskRequest(b *Board, loopID, taskID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/loops/"+loopID+"/tasks/"+taskID+"/close", http.NoBody)
	req.SetPathValue("id", loopID)
	req.SetPathValue("task", taskID)
	rec := httptest.NewRecorder()
	b.handleCloseLoopProjectTask(rec, req)
	return rec
}

func TestCloseTaskStopsSessionButPreservesWorktree(t *testing.T) {
	b, store, loop, _ := taskFixture(t)
	task, card := reservedTask(t, store, loop.ID)
	sessions := &closeTaskSessions{}
	b.sessions = sessions
	b.controller.sessions = sessions
	b.controller.Start(card)
	rec := closeTaskRequest(b, loop.ID, task.ID)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Equal(t, 2, sessions.kills)
	saved, err := store.GetCard(card.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusClosed, saved.Status)
	stored, err := store.GetProjectTask(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "closed", stored.Status)
	assert.FileExists(t, filepath.Join(card.Worktree, "result.txt"))
	require.NoError(t, b.controller.ReconcileAll())
	b.controller.mu.Lock()
	_, watching := b.controller.watchers[card.ID]
	b.controller.mu.Unlock()
	assert.False(t, watching)
	require.Equal(t, http.StatusNoContent, closeTaskRequest(b, loop.ID, task.ID).Code)
	assert.Equal(t, 2, sessions.kills)
}

func TestCloseTaskFailureRemainsRetryableAndChecksOwnership(t *testing.T) {
	b, store, loop, _ := taskFixture(t)
	task, card := reservedTask(t, store, loop.ID)
	sessions := &closeTaskSessions{fail: true}
	b.sessions = sessions
	b.controller.sessions = sessions
	assert.Equal(t, http.StatusNotFound, closeTaskRequest(b, "another-loop", task.ID).Code)
	assert.Zero(t, sessions.kills)
	assert.Equal(t, http.StatusInternalServerError, closeTaskRequest(b, loop.ID, task.ID).Code)
	stored, err := store.GetProjectTask(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "closing", stored.Status)
	assert.FileExists(t, filepath.Join(card.Worktree, "result.txt"))
	require.NoError(t, b.controller.ReconcileAll())
	stored, err = store.GetProjectTask(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "closing", stored.Status)
	sessions.fail = false
	require.NoError(t, b.controller.ReconcileAll())
	stored, err = store.GetProjectTask(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "closed", stored.Status)
}

func TestClosedTaskCannotStartCoach(t *testing.T) {
	b, store, loop, _ := taskFixture(t)
	task, card := reservedTask(t, store, loop.ID)
	require.NoError(t, store.UpdateProjectTask(task.ID, "closed", ""))
	_, err := b.startCoach(t.Context(), card)
	require.ErrorIs(t, err, errBadInput)
}
