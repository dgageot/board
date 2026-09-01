package board

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// coachTest is a board whose tmux and control-plane calls are fakes, with one
// card ready to be coached.
type coachTest struct {
	board    *Board
	sessions *fakeSessionManager
	client   *fakeClient
	card     *Card
}

// newCoachTest builds a coachTest. TMPDIR is redirected so the staged
// transcript and coach config land in the test's own directory.
func newCoachTest(t *testing.T) coachTest {
	t.Helper()

	t.Setenv("TMPDIR", t.TempDir())

	store := openTestStore(t)
	require.NoError(t, store.SeedColumns([]Column{
		{ID: "dev", Name: "Dev", Emoji: "🔨", Prompt: ""},
		{ID: "review", Name: "Review", Emoji: "🔍", Prompt: "Review the local changes."},
	}))

	card := devCard()
	card.Prompt = "make the thing work"
	card.Agent = "/agents/gopher.yaml"
	require.NoError(t, store.InsertCard(card))

	sessions := newFakeSessionManager()
	sessions.alive = false // no coach running yet
	client := &fakeClient{transcript: []byte(`{"messages":[{"message":{"role":"user"}}]}`)}

	b, err := newBoard(t.Context(), Config{ListenAddr: ":0"}, store, sessions)
	require.NoError(t, err)
	b.controller.clientFor = func(string, string) sessionClient { return client }

	return coachTest{board: b, sessions: sessions, client: client, card: card}
}

func TestStartCoachLaunchesSessionInTheWorktree(t *testing.T) {
	ct := newCoachTest(t)

	session, err := ct.board.startCoach(t.Context(), ct.card)
	require.NoError(t, err)
	assert.Equal(t, coachSessionName(ct.card.ID), session)

	require.Len(t, ct.sessions.calls(), 1)
	call := ct.sessions.calls()[0]
	assert.Equal(t, coachSessionName(ct.card.ID), call.name)
	assert.Equal(t, ct.card.Worktree, call.workDir, "the coach reviews the card's worktree")
	assert.Equal(t, coachAgentSessionID(ct.card.AgentSession), call.sessionID)
	assert.Equal(t, socketPath(call.sessionID), call.listenSocket)
	assert.Empty(t, call.worktreeName, "the coach reuses the card's worktree, it does not branch one")
	assert.NotEqual(t, ct.card.Agent, call.agent, "the coach runs its own agent config, not the card's")

	// The launch prompt points the coach at the staged transcript and carries
	// the harness context it reviews.
	transcript := coachTranscriptPath(ct.card.AgentSession)
	assert.Contains(t, call.prompt, transcript)
	assert.Contains(t, call.prompt, ct.card.Agent, "the coach is told which agent config ran")
	assert.Contains(t, call.prompt, ct.card.Prompt, "the coach sees the original request")
	assert.Contains(t, call.prompt, "Review the local changes.", "the coach sees the column prompts")

	// The transcript itself is exported next to it.
	staged, err := os.ReadFile(transcript)
	require.NoError(t, err)
	assert.JSONEq(t, `{"messages":[{"message":{"role":"user"}}]}`, string(staged))
}

// The embedded coach config is staged on disk so `docker agent run` can load
// it: a board that ships its own coach must not depend on a user file.
func TestStartCoachStagesEmbeddedConfig(t *testing.T) {
	ct := newCoachTest(t)

	_, err := ct.board.startCoach(t.Context(), ct.card)
	require.NoError(t, err)

	staged, err := os.ReadFile(ct.sessions.calls()[0].agent)
	require.NoError(t, err)
	assert.Equal(t, coachAgentConfig, staged)
}

// BOARD_COACH_AGENT replaces the embedded coach with the user's own config.
func TestStartCoachHonorsConfiguredAgent(t *testing.T) {
	ct := newCoachTest(t)
	ct.board.config.CoachAgent = "/my/coach.yaml"

	_, err := ct.board.startCoach(t.Context(), ct.card)
	require.NoError(t, err)
	assert.Equal(t, "/my/coach.yaml", ct.sessions.calls()[0].agent)
}

// A coach already running for the card is reattached: relaunching would throw
// away the review — and the conversation — it has already produced.
func TestStartCoachReattachesToRunningCoach(t *testing.T) {
	ct := newCoachTest(t)
	ct.sessions.alive = true

	session, err := ct.board.startCoach(t.Context(), ct.card)
	require.NoError(t, err)
	assert.Equal(t, coachSessionName(ct.card.ID), session)
	assert.Empty(t, ct.sessions.calls(), "no second coach is started")

	_, err = os.Stat(coachTranscriptPath(ct.card.AgentSession))
	assert.ErrorIs(t, err, os.ErrNotExist, "no transcript is re-exported")
}

// Without a transcript there is nothing to review, so an unreachable control
// plane is reported as such (503) instead of launching a blind coach.
func TestStartCoachFailsWhenSessionUnreachable(t *testing.T) {
	ct := newCoachTest(t)
	ct.client.transcriptErr = errors.New("connection refused")

	_, err := ct.board.startCoach(t.Context(), ct.card)
	require.ErrorIs(t, err, errAgentUnreachable)
	assert.Empty(t, ct.sessions.calls())
}

func TestHandleCoachCardUnknownCard(t *testing.T) {
	ct := newCoachTest(t)

	req := httptest.NewRequest(http.MethodPost, "/api/cards/nope/coach", http.NoBody)
	req.SetPathValue("id", "nope")
	rec := httptest.NewRecorder()
	ct.board.handleCoachCard(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandleCoachCardReturnsSession(t *testing.T) {
	ct := newCoachTest(t)

	req := httptest.NewRequest(http.MethodPost, "/api/cards/"+ct.card.ID+"/coach", http.NoBody)
	req.SetPathValue("id", ct.card.ID)
	rec := httptest.NewRecorder()
	ct.board.handleCoachCard(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"session":"`+coachSessionName(ct.card.ID)+`"}`, rec.Body.String())
}

// Deleting a card must take its coach down with it: the coach only exists to
// review that card, and its tmux session would otherwise linger forever.
func TestDeleteCardKillsCoachSession(t *testing.T) {
	ct := newCoachTest(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/cards/"+ct.card.ID, http.NoBody)
	req.SetPathValue("id", ct.card.ID)
	rec := httptest.NewRecorder()
	ct.board.handleDeleteCard(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	ct.sessions.mu.Lock()
	defer ct.sessions.mu.Unlock()
	assert.Contains(t, ct.sessions.killed, coachSessionName(ct.card.ID))
}
