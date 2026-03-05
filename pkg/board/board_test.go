package board

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestHandleToggleAutoCard(t *testing.T) {
	b, store := newTestBoard(t)

	require.NoError(t, store.InsertCard(&Card{
		ID: "c1", Title: "T", Column: "dev", Status: StatusRunning,
		Agent: "ag", RepoPath: "rp", Branch: "br", Worktree: "wt", Session: "s1",
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/cards/c1/auto", http.NoBody)
	req.SetPathValue("id", "c1")
	rec := httptest.NewRecorder()
	b.handleToggleAutoCard(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var card Card
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&card))
	assert.True(t, card.Auto)

	// Toggle back
	req = httptest.NewRequest(http.MethodPost, "/api/cards/c1/auto", http.NoBody)
	req.SetPathValue("id", "c1")
	rec = httptest.NewRecorder()
	b.handleToggleAutoCard(rec, req)

	require.NoError(t, json.NewDecoder(rec.Body).Decode(&card))
	assert.False(t, card.Auto)
}

func TestHandleToggleAutoCardNotFound(t *testing.T) {
	b, _ := newTestBoard(t)

	req := httptest.NewRequest(http.MethodPost, "/api/cards/nonexistent/auto", http.NoBody)
	req.SetPathValue("id", "nonexistent")
	rec := httptest.NewRecorder()
	b.handleToggleAutoCard(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandleJumpCard(t *testing.T) {
	b, store := newTestBoard(t)

	require.NoError(t, store.InsertCard(&Card{
		ID: "c1", Title: "T", Column: "dev", Status: StatusRunning,
		Agent: "ag", RepoPath: "rp", Branch: "br", Worktree: "wt", Session: "my-session",
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/cards/c1/jump", http.NoBody)
	req.SetPathValue("id", "c1")
	rec := httptest.NewRecorder()
	b.handleJumpCard(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]string
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "my-session", resp["session"])
	assert.Contains(t, resp["command"], "my-session")
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

	body := `{"title":"","prompt":""}`
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

func TestBroadcastToClients(t *testing.T) {
	b, _ := newTestBoard(t)

	ch := make(chan string, 4)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()

	b.broadcast()

	select {
	case msg := <-ch:
		assert.Equal(t, "refresh", msg)
	default:
		t.Fatal("expected broadcast message")
	}
}

func TestBroadcastSkipsFullChannels(t *testing.T) {
	b, _ := newTestBoard(t)

	ch := make(chan string) // unbuffered, will be full
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()

	// Should not block
	b.broadcast()
}
