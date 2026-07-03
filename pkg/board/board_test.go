package board

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleListProjectsEmpty(t *testing.T) {
	b, _ := newTestBoard(t)

	rec := httptest.NewRecorder()
	b.handleListProjects(rec, httptest.NewRequest(http.MethodGet, "/api/projects", http.NoBody))

	assert.Equal(t, http.StatusOK, rec.Code)

	var projects []*Project
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&projects))
	assert.Empty(t, projects)
}

func TestHandleListProjectsWithData(t *testing.T) {
	b, store := newTestBoard(t)

	require.NoError(t, store.InsertProject(&Project{ID: "p1", Name: "Proj", RepoPath: "/r", Agent: "/a"}))

	rec := httptest.NewRecorder()
	b.handleListProjects(rec, httptest.NewRequest(http.MethodGet, "/api/projects", http.NoBody))

	assert.Equal(t, http.StatusOK, rec.Code)

	var projects []*Project
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&projects))
	require.Len(t, projects, 1)
	assert.Equal(t, "Proj", projects[0].Name)
}

func TestHandleCreateProjectValidatesGitRepo(t *testing.T) {
	b, store := newTestBoard(t)

	body := `{"name":"Proj","repoPath":"/definitely/not/a/repo"}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(body))
	rec := httptest.NewRecorder()
	b.handleCreateProject(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	projects, err := store.ListProjects()
	require.NoError(t, err)
	assert.Empty(t, projects)
}

func TestHandleCreateProjectAcceptsGitRepo(t *testing.T) {
	b, store := newTestBoard(t)

	repo := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = repo
	require.NoError(t, cmd.Run())

	body := `{"name":"Proj","repoPath":"` + repo + `","agent":"/a"}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(body))
	rec := httptest.NewRecorder()
	b.handleCreateProject(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	projects, err := store.ListProjects()
	require.NoError(t, err)
	require.Len(t, projects, 1)
	assert.Equal(t, "Proj", projects[0].Name)
}

func TestHandleDeleteProject(t *testing.T) {
	b, store := newTestBoard(t)

	require.NoError(t, store.InsertProject(&Project{ID: "p1", Name: "P", RepoPath: "/r", Agent: "/a"}))

	req := httptest.NewRequest(http.MethodDelete, "/api/projects/p1", http.NoBody)
	req.SetPathValue("id", "p1")
	rec := httptest.NewRecorder()
	b.handleDeleteProject(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)

	projects, err := store.ListProjects()
	require.NoError(t, err)
	assert.Empty(t, projects)
}

func TestHandleListColumns(t *testing.T) {
	b, store := newTestBoard(t)

	require.NoError(t, store.SeedColumns([]Column{
		{ID: "dev", Name: "Dev", Emoji: "🔨", Prompt: ""},
		{ID: "done", Name: "Done", Emoji: "✅", Prompt: ""},
	}))

	rec := httptest.NewRecorder()
	b.handleListColumns(rec, httptest.NewRequest(http.MethodGet, "/api/columns", http.NoBody))

	assert.Equal(t, http.StatusOK, rec.Code)

	var cols []Column
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&cols))
	require.Len(t, cols, 2)
	assert.Equal(t, "dev", cols[0].ID)
	assert.Equal(t, "done", cols[1].ID)
}

func TestHandleUpdateColumns(t *testing.T) {
	b, store := newTestBoard(t)

	require.NoError(t, store.SeedColumns([]Column{
		{ID: "dev", Name: "Dev", Emoji: "🔨", Prompt: "old"},
	}))

	body := `[{"id":"dev","prompt":"new prompt"}]`
	req := httptest.NewRequest(http.MethodPut, "/api/columns", strings.NewReader(body))
	rec := httptest.NewRecorder()
	b.handleUpdateColumns(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)

	cols, err := store.ListColumns()
	require.NoError(t, err)
	require.Len(t, cols, 1)
	assert.Equal(t, "new prompt", cols[0].Prompt)
}

func TestHandleUpdateColumnsInvalidJSON(t *testing.T) {
	b, _ := newTestBoard(t)

	req := httptest.NewRequest(http.MethodPut, "/api/columns", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	b.handleUpdateColumns(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleListCardsEmpty(t *testing.T) {
	b, _ := newTestBoard(t)

	rec := httptest.NewRecorder()
	b.handleListCards(rec, httptest.NewRequest(http.MethodGet, "/api/cards", http.NoBody))

	assert.Equal(t, http.StatusOK, rec.Code)

	var cards []*Card
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&cards))
	assert.Empty(t, cards)
}

func TestHandleJumpCard(t *testing.T) {
	b, store := newTestBoard(t)

	require.NoError(t, store.InsertCard(&Card{
		ID: "c1", Title: "T", Column: "dev", Status: StatusRunning,
		Agent: "ag", RepoPath: "rp", Branch: "br", Worktree: "wt", Session: "my-session",
	}))
	// A reachable control plane means the agent really started.
	b.controller.clientFor = func(string, string) sessionClient { return &fakeClient{} }

	req := httptest.NewRequest(http.MethodPost, "/api/cards/c1/jump", http.NoBody)
	req.SetPathValue("id", "c1")
	rec := httptest.NewRecorder()
	b.handleJumpCard(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]string
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "my-session", resp["session"])
}

// A freshly created card whose agent has not come up yet must not attach a
// terminal to the bare launch command.
func TestHandleJumpCardAgentStillStarting(t *testing.T) {
	b, store := newTestBoard(t)

	require.NoError(t, store.InsertCard(&Card{
		ID: "c1", Title: "T", Column: "dev", Status: StatusStarting,
		Agent: "ag", RepoPath: "rp", Branch: "br", Worktree: "wt", Session: "my-session",
	}))
	// The default test client never reaches a control plane.
	req := httptest.NewRequest(http.MethodPost, "/api/cards/c1/jump", http.NoBody)
	req.SetPathValue("id", "c1")
	rec := httptest.NewRecorder()
	b.handleJumpCard(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// A card that already left "starting" opens even when the control plane is
// unreachable (e.g. its socket vanished): the tmux session showing the
// agent's UI is still attachable, so the probe must not lock the user out.
func TestHandleJumpCardControlPlaneUnreachable(t *testing.T) {
	b, store := newTestBoard(t)

	require.NoError(t, store.InsertCard(&Card{
		ID: "c1", Title: "T", Column: "dev", Status: StatusWaiting,
		Agent: "ag", RepoPath: "rp", Branch: "br", Worktree: "wt", Session: "my-session",
	}))
	// The default test client never reaches a control plane.
	req := httptest.NewRequest(http.MethodPost, "/api/cards/c1/jump", http.NoBody)
	req.SetPathValue("id", "c1")
	rec := httptest.NewRecorder()
	b.handleJumpCard(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]string
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "my-session", resp["session"])
}

func TestHandleJumpCardNotFound(t *testing.T) {
	b, _ := newTestBoard(t)

	req := httptest.NewRequest(http.MethodPost, "/api/cards/nonexistent/jump", http.NoBody)
	req.SetPathValue("id", "nonexistent")
	rec := httptest.NewRecorder()
	b.handleJumpCard(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandleCreateCardMissingFields(t *testing.T) {
	b, _ := newTestBoard(t)

	body := `{"prompt":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/cards", strings.NewReader(body))
	rec := httptest.NewRecorder()
	b.handleCreateCard(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleCreateCardInvalidJSON(t *testing.T) {
	b, _ := newTestBoard(t)

	req := httptest.NewRequest(http.MethodPost, "/api/cards", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	b.handleCreateCard(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// A forward move is persisted before the column prompt is delivered: a failed
// delivery must surface an error but never hide the move.
func TestHandleMoveCardPersistsMoveWhenPromptFails(t *testing.T) {
	b, store := newTestBoard(t)

	require.NoError(t, store.SeedColumns(defaultColumns))
	require.NoError(t, store.InsertCard(&Card{
		ID: "c1", Title: "T", Column: "dev", Status: StatusWaiting,
		Agent: "ag", RepoPath: "rp", Branch: "br", Worktree: "wt", Session: "s1",
	}))
	// The agent is alive but rejects the follow-up: prompt delivery fails.
	b.controller.clientFor = func(string, string) sessionClient {
		return &fakeClient{followErr: errors.New("queue full")}
	}

	req := httptest.NewRequest(http.MethodPost, "/api/cards/c1/move", strings.NewReader(`{"column":"review"}`))
	req.SetPathValue("id", "c1")
	rec := httptest.NewRecorder()
	b.handleMoveCard(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	card, err := store.GetCard("c1")
	require.NoError(t, err)
	assert.Equal(t, "review", card.Column, "the move is persisted even when the prompt fails")
}

func TestBroadcastToClients(t *testing.T) {
	b, _ := newTestBoard(t)

	ch := make(chan struct{}, 4)
	b.addClient(ch)
	defer b.removeClient(ch)

	b.broadcast()

	select {
	case <-ch:
	default:
		t.Fatal("expected broadcast message")
	}
}

func TestBroadcastSkipsFullChannels(t *testing.T) {
	b, _ := newTestBoard(t)

	ch := make(chan struct{}) // unbuffered, will be full
	b.addClient(ch)
	defer b.removeClient(ch)

	// Should not block
	b.broadcast()
}
