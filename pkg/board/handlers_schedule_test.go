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

func TestHandleListScheduleEmpty(t *testing.T) {
	b, _ := newTestBoard(t)

	rec := httptest.NewRecorder()
	b.handleListSchedule(rec, httptest.NewRequest(http.MethodGet, "/api/schedule", http.NoBody))

	assert.Equal(t, http.StatusOK, rec.Code)

	var cards []scheduleCardResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&cards))
	assert.Empty(t, cards)
}

func TestHandleListScheduleWithCards(t *testing.T) {
	b, store := newTestBoard(t)

	require.NoError(t, store.InsertCard(&Card{
		ID: "c1", Title: "Running task", Column: "dev", Status: StatusRunning,
		Agent: "ag", RepoPath: "/repo", Branch: "br1", Worktree: "/wt1", Session: "s1",
	}))
	require.NoError(t, store.InsertCard(&Card{
		ID: "c2", Title: "Waiting task", Column: "review", Status: StatusWaiting,
		Agent: "ag", RepoPath: "/repo", Branch: "br2", Worktree: "/wt2", Session: "s2",
	}))
	require.NoError(t, store.InsertCard(&Card{
		ID: "c3", Title: "Done task", Column: "done", Status: StatusWaiting,
		Agent: "ag", RepoPath: "/repo", Branch: "br3", Worktree: "/wt3", Session: "s3",
	}))

	rec := httptest.NewRecorder()
	b.handleListSchedule(rec, httptest.NewRequest(http.MethodGet, "/api/schedule", http.NoBody))

	assert.Equal(t, http.StatusOK, rec.Code)

	var cards []scheduleCardResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&cards))
	require.Len(t, cards, 3)

	assert.Equal(t, "Running task", cards[0].Title)
	assert.Equal(t, "dev", cards[0].Column)
	assert.True(t, cards[0].Running)

	assert.Equal(t, "Waiting task", cards[1].Title)
	assert.Equal(t, "review", cards[1].Column)
	assert.False(t, cards[1].Running)

	assert.Equal(t, "Done task", cards[2].Title)
	assert.Equal(t, "done", cards[2].Column)
	assert.False(t, cards[2].Running)
}

func TestHandleScheduleCardMissingPrompt(t *testing.T) {
	b, _ := newTestBoard(t)

	req := httptest.NewRequest(http.MethodPost, "/api/schedule", strings.NewReader(`{"prompt":""}`))
	rec := httptest.NewRecorder()
	b.handleScheduleCard(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleScheduleCardInvalidJSON(t *testing.T) {
	b, _ := newTestBoard(t)

	req := httptest.NewRequest(http.MethodPost, "/api/schedule", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	b.handleScheduleCard(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
