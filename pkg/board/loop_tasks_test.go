package board

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dgageot/board/pkg/loops"
)

type taskSessions struct {
	noopSessionManager

	launch func(agent, prompt string) error
	calls  atomic.Int32
}

func (s *taskSessions) NewSession(_, _, agent, _, _, _, _, prompt string) error {
	s.calls.Add(1)
	return s.launch(agent, prompt)
}

func taskFixture(t *testing.T) (*Board, *SQLiteStore, loops.Loop, *Project) {
	t.Helper()
	b, store := newTestBoard(t)
	repo := t.TempDir()
	cmd := exec.CommandContext(t.Context(), "git", "init", repo)
	require.NoError(t, cmd.Run())
	cmd = exec.CommandContext(t.Context(), "git", "-C", repo, "-c", "commit.gpgsign=false", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial")
	require.NoError(t, cmd.Run())
	project := &Project{ID: "project", Name: "Repo", RepoPath: repo, Agent: "trusted-project-agent"}
	require.NoError(t, store.InsertProject(project))
	require.NoError(t, store.SeedColumns(defaultColumns))
	loop, err := b.loops.Create(t.Context(), loops.Loop{Name: "Update dependencies", Description: "Bump Go dependencies", Criteria: "Signed commits; CI and license checks green", ProjectID: project.ID, Mode: loops.ModeProject})
	require.NoError(t, err)
	_, err = store.db.Exec(`INSERT INTO loop_versions(id,loop_id,number,yaml,rationale,created_at,mode) VALUES('workflow',?,1,?,'','now','project')`, loop.ID, "agents:\n  root:\n    model: auto\n    instruction: Execute and verify the dependency update.\n")
	require.NoError(t, err)
	return b, store, loop, project
}

func taskRequest(t *testing.T, b *Board, loopID string, project *Project, id string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"requestId": id, "versionId": "workflow", "projectId": project.ID, "repoPath": project.RepoPath, "agent": project.Agent})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/loops/"+loopID+"/tasks", strings.NewReader(string(body)))
	req.SetPathValue("id", loopID)
	rec := httptest.NewRecorder()
	b.handleRunLoopProjectTask(rec, req)
	return rec
}

func TestLoopTaskReservesBeforeLaunchAndDeduplicates(t *testing.T) {
	b, store, loop, project := taskFixture(t)
	sessions := &taskSessions{launch: func(agent, prompt string) error {
		task, err := store.GetProjectTask("request-one")
		require.NoError(t, err)
		card, err := store.GetCard(task.CardID)
		require.NoError(t, err)
		assert.Equal(t, "reserved", task.Status)
		assert.Equal(t, project.Agent, agent)
		assert.Equal(t, agent, card.Agent)
		assert.Contains(t, prompt, loop.Description)
		assert.Contains(t, prompt, loop.Criteria)
		assert.Contains(t, prompt, "All commits must be signed")
		assert.Contains(t, prompt, "Execute and verify the dependency update.")
		assert.Equal(t, "workflow", task.VersionID)
		return nil
	}}
	b.sessions = sessions
	rec := taskRequest(t, b, loop.ID, project, "request-one")
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var task ProjectTask
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &task))
	t.Cleanup(func() { b.controller.Stop(task.CardID) })
	assert.Equal(t, "launched", task.Status)
	rec = taskRequest(t, b, loop.ID, project, "request-one")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, int32(1), sessions.calls.Load())
	require.NoError(t, store.DeleteCard(task.CardID))
	tasks, err := store.ListProjectTasks(loop.ID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, loop.Description, tasks[0].Task)
}

func TestLoopTaskRejectsMissingRequestIDAndChangedProject(t *testing.T) {
	b, store, loop, project := taskFixture(t)
	rec := taskRequest(t, b, loop.ID, project, "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	stale := *project
	stale.Agent = "changed"
	rec = taskRequest(t, b, loop.ID, &stale, "request-one")
	assert.Equal(t, http.StatusConflict, rec.Code)
	cards, err := store.ListCards()
	require.NoError(t, err)
	assert.Empty(t, cards)
}

func TestLoopTaskStillRequiresProjectSelection(t *testing.T) {
	b, store, loop, project := taskFixture(t)
	sessions := &taskSessions{launch: func(string, string) error { return nil }}
	b.sessions = sessions
	missing := *project
	missing.ID = ""
	rec := taskRequest(t, b, loop.ID, &missing, "request-one")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Zero(t, sessions.calls.Load())
	cards, err := store.ListCards()
	require.NoError(t, err)
	assert.Empty(t, cards)
}

func TestLoopTaskFailedLaunchIsNeverReplayed(t *testing.T) {
	b, store, loop, project := taskFixture(t)
	sessions := &taskSessions{launch: func(string, string) error { return errors.New("launch failed") }}
	b.sessions = sessions
	rec := taskRequest(t, b, loop.ID, project, "request-one")
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	task, err := store.GetProjectTask("request-one")
	require.NoError(t, err)
	assert.Equal(t, "launch_uncertain", task.Status)
	card, err := store.GetCard(task.CardID)
	require.NoError(t, err)
	t.Cleanup(func() { b.controller.Stop(task.CardID) })
	assert.Equal(t, StatusError, card.Status)
	require.ErrorContains(t, b.controller.relaunch(card, ""), "not automatically relaunched")
	rec = taskRequest(t, b, loop.ID, project, "request-one")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, int32(1), sessions.calls.Load())
}

func TestLoopTaskReservationFailureNeverLaunches(t *testing.T) {
	b, store, loop, project := taskFixture(t)
	_, err := store.db.Exec(`CREATE TRIGGER reject_task BEFORE INSERT ON loop_project_tasks BEGIN SELECT RAISE(FAIL,'storage failure'); END`)
	require.NoError(t, err)
	sessions := &taskSessions{launch: func(string, string) error { return nil }}
	b.sessions = sessions
	rec := taskRequest(t, b, loop.ID, project, "request-one")
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Zero(t, sessions.calls.Load())
	cards, err := store.ListCards()
	require.NoError(t, err)
	assert.Empty(t, cards)
}

func TestLoopTaskPersistenceFailureStillMonitorsLaunch(t *testing.T) {
	b, store, loop, project := taskFixture(t)
	_, err := store.db.Exec(`CREATE TRIGGER fail_status BEFORE UPDATE ON loop_project_tasks BEGIN SELECT RAISE(FAIL,'storage failure'); END`)
	require.NoError(t, err)
	sessions := &taskSessions{launch: func(string, string) error { return nil }}
	b.sessions = sessions
	rec := taskRequest(t, b, loop.ID, project, "request-one")
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	task, err := store.GetProjectTask("request-one")
	require.NoError(t, err)
	t.Cleanup(func() { b.controller.Stop(task.CardID) })
	b.controller.mu.Lock()
	_, watching := b.controller.watchers[task.CardID]
	b.controller.mu.Unlock()
	assert.True(t, watching)
}

type blockingTaskSessions struct {
	noopSessionManager

	entered chan struct{}
	release chan struct{}
	alive   atomic.Bool
}

func (s *blockingTaskSessions) NewSession(string, string, string, string, string, string, string, string) error {
	close(s.entered)
	<-s.release
	s.alive.Store(true)
	return nil
}
func (s *blockingTaskSessions) KillSession(string) error { s.alive.Store(false); return nil }

func TestDeletingReservedLoopTaskWaitsForLaunchThenCleansUp(t *testing.T) {
	b, store, loop, project := taskFixture(t)
	sessions := &blockingTaskSessions{entered: make(chan struct{}), release: make(chan struct{})}
	b.sessions = sessions
	launched := make(chan struct{})
	body, err := json.Marshal(map[string]any{"requestId": "request-one", "versionId": "workflow", "projectId": project.ID, "repoPath": project.RepoPath, "agent": project.Agent})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/api/loops/"+loop.ID+"/tasks", strings.NewReader(string(body)))
	request.SetPathValue("id", loop.ID)
	go func() { defer close(launched); b.handleRunLoopProjectTask(httptest.NewRecorder(), request) }()
	<-sessions.entered
	task, err := store.GetProjectTask("request-one")
	require.NoError(t, err)
	locked := !b.taskLaunch.TryLock()
	if !locked {
		b.taskLaunch.Unlock()
	}
	require.True(t, locked)
	deleted := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodDelete, "/api/cards/"+task.CardID, http.NoBody)
		req.SetPathValue("id", task.CardID)
		rec := httptest.NewRecorder()
		b.handleDeleteCard(rec, req)
		deleted <- rec.Code
	}()
	close(sessions.release)
	<-launched
	require.Equal(t, http.StatusNoContent, <-deleted)
	assert.False(t, sessions.alive.Load())
	_, err = store.GetCard(task.CardID)
	require.Error(t, err)
}

func TestProjectTaskUsesExactGeneratedWorkflow(t *testing.T) {
	detail := loops.Detail{Loop: loops.Loop{Mode: loops.ModeProject}, Versions: []loops.Version{{ID: "v2", Mode: loops.ModeProject, YAML: "agents:\n  root:\n    model: auto\n    instruction: Run tests and commit signed changes.\n"}}}
	instruction, err := projectWorkflowInstruction(detail, "v2")
	require.NoError(t, err)
	assert.Equal(t, "Run tests and commit signed changes.", instruction)
	_, err = projectWorkflowInstruction(detail, "")
	require.ErrorIs(t, err, errBadInput)
	detail.Versions[0].Mode = loops.ModeAnalysis
	_, err = projectWorkflowInstruction(detail, "v2")
	require.ErrorIs(t, err, errBadInput)
}

func TestProjectExecutionRejectsAnalysisLoop(t *testing.T) {
	b, store, loop, project := taskFixture(t)
	require.NoError(t, b.loops.SetMode(t.Context(), loop.ID, loops.ModeAnalysis))
	rec := taskRequest(t, b, loop.ID, project, "wrong-mode")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	cards, err := store.ListCards()
	require.NoError(t, err)
	assert.Empty(t, cards)
}

func TestReservationChecksExecutionModeAtCommit(t *testing.T) {
	_, store, loop, _ := taskFixture(t)
	_, err := store.db.Exec(`UPDATE loops SET mode='analysis' WHERE id=?`, loop.ID)
	require.NoError(t, err)
	card := &Card{ID: "stale", Title: "Stale", Status: StatusStarting}
	task := &ProjectTask{ID: "stale", LoopID: loop.ID, CardID: card.ID, VersionID: "workflow", Status: "reserved"}
	require.ErrorIs(t, store.ReserveProjectTask(task, card), errBadInput)
	_, err = store.GetCard(card.ID)
	require.Error(t, err)
}
