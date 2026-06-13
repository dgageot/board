package board

import (
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSessionManager records calls and returns configurable pane content.
type fakeSessionManager struct {
	mu          sync.Mutex
	paneContent map[string]string
	paneDead    map[string]bool
	paneErr     map[string]error
	created     []newSessionCall
	killed      []string
	sentKeys    []string
	sendErr     error
}

// newSessionCall records the arguments of a NewSession call.
type newSessionCall struct {
	name         string
	workDir      string
	sessionID    string
	worktreeName string
	prompt       string
}

func newFakeSessionManager() *fakeSessionManager {
	return &fakeSessionManager{
		paneContent: make(map[string]string),
		paneDead:    make(map[string]bool),
		paneErr:     make(map[string]error),
	}
}

func (f *fakeSessionManager) NewSession(name, workDir, _, sessionID, worktreeName, prompt string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, newSessionCall{
		name:         name,
		workDir:      workDir,
		sessionID:    sessionID,
		worktreeName: worktreeName,
		prompt:       prompt,
	})
	return nil
}

func (f *fakeSessionManager) KillSession(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = append(f.killed, name)
	return nil
}

func (f *fakeSessionManager) SendKeys(name, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sentKeys = append(f.sentKeys, name+":"+msg)
	return nil
}

func (f *fakeSessionManager) PaneContent(name string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.paneErr[name]; err != nil {
		return "", false, err
	}
	return f.paneContent[name], f.paneDead[name], nil
}

func (f *fakeSessionManager) setPaneContent(content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paneContent["s1"] = content
}

func TestPollerDetectsWaitingAfterStableContent(t *testing.T) {
	store := openTestStore(t)
	sessions := newFakeSessionManager()
	poller := newPoller(store, sessions, func() {})

	require.NoError(t, store.InsertCard(&Card{
		ID: "c1", Title: "Task", Column: "dev", Status: StatusRunning,
		Agent: "ag", RepoPath: "rp", Branch: "br", Worktree: "wt", Session: "s1",
	}))

	// First poll: establishes baseline content.
	sessions.setPaneContent("some output")
	assert.False(t, poller.poll())
	card, _ := store.GetCard("c1")
	assert.Equal(t, StatusRunning, card.Status)

	// Subsequent polls with same content increment the stable count.
	for range stableThreshold {
		poller.poll()
	}

	card, _ = store.GetCard("c1")
	assert.Equal(t, StatusWaiting, card.Status)
}

func TestPollerDetectsRunningOnContentChange(t *testing.T) {
	store := openTestStore(t)
	sessions := newFakeSessionManager()
	poller := newPoller(store, sessions, func() {})

	require.NoError(t, store.InsertCard(&Card{
		ID: "c1", Title: "Task", Column: "dev", Status: StatusRunning,
		Agent: "ag", RepoPath: "rp", Branch: "br", Worktree: "wt", Session: "s1",
	}))

	// Drive to waiting state.
	sessions.setPaneContent("stable output")
	for range stableThreshold + 1 {
		poller.poll()
	}

	card, _ := store.GetCard("c1")
	require.Equal(t, StatusWaiting, card.Status)

	// Content changes → card should go back to running.
	sessions.setPaneContent("new output")
	poller.poll()

	card, _ = store.GetCard("c1")
	assert.Equal(t, StatusRunning, card.Status)
}

func TestPollerResetCardClearsState(t *testing.T) {
	store := openTestStore(t)
	sessions := newFakeSessionManager()
	poller := newPoller(store, sessions, func() {})

	require.NoError(t, store.InsertCard(&Card{
		ID: "c1", Title: "Task", Column: "dev", Status: StatusRunning,
		Agent: "ag", RepoPath: "rp", Branch: "br", Worktree: "wt", Session: "s1",
	}))

	// Build up stable count just under threshold.
	sessions.setPaneContent("output")
	for range stableThreshold {
		poller.poll()
	}

	// Reset clears stable count.
	poller.ResetCard("c1")

	// One more poll should not transition since count was reset.
	poller.poll()

	card, _ := store.GetCard("c1")
	assert.Equal(t, StatusRunning, card.Status)
}

func TestPollerPrunesStateForRemovedCards(t *testing.T) {
	store := openTestStore(t)
	sessions := newFakeSessionManager()
	poller := newPoller(store, sessions, func() {})

	require.NoError(t, store.InsertCard(&Card{
		ID: "c1", Title: "Task", Column: "dev", Status: StatusRunning,
		Agent: "ag", RepoPath: "rp", Branch: "br", Worktree: "wt", Session: "s1",
	}))

	sessions.setPaneContent("output")
	poller.poll()
	assert.Contains(t, poller.states, "c1")

	require.NoError(t, store.DeleteCard("c1"))
	poller.poll()
	assert.Empty(t, poller.states)
}

func TestPollerIgnoresNonActiveCards(t *testing.T) {
	store := openTestStore(t)
	sessions := newFakeSessionManager()
	poller := newPoller(store, sessions, func() {})

	require.NoError(t, store.InsertCard(&Card{
		ID: "c1", Title: "Task", Column: "dev", Status: StatusDone,
		Agent: "ag", RepoPath: "rp", Branch: "br", Worktree: "wt", Session: "s1",
	}))

	sessions.setPaneContent("output")
	for range stableThreshold + 2 {
		poller.poll()
	}

	card, _ := store.GetCard("c1")
	assert.Equal(t, StatusDone, card.Status)
}

// raceStore wraps a Store and rewrites c1's column on the first ListCards
// call, mimicking a move-card handler that lands between the poller's read
// (phase 1) and write (phase 2) phases.
type raceStore struct {
	Store
	newColumn string
	done      bool
}

func (r *raceStore) ListCards() ([]*Card, error) {
	cards, err := r.Store.ListCards()
	if err != nil || r.done {
		return cards, err
	}
	for _, c := range cards {
		if c.ID != "c1" {
			continue
		}
		moved := *c
		moved.Column = r.newColumn
		if err := r.UpdateCard(&moved); err != nil {
			return nil, err
		}
		r.done = true
		break
	}
	return cards, nil
}

// TestPollerTransitionDoesNotRevertConcurrentColumnChange is a regression
// test for a race between [Poller.poll] and the move-card handler. The
// poller used to write its phase-1 snapshot back via [Store.UpdateCard],
// which rewrote every column of the row and silently reverted concurrent
// edits — most visibly the card's destination column.
func TestPollerTransitionDoesNotRevertConcurrentColumnChange(t *testing.T) {
	inner := openTestStore(t)
	sessions := newFakeSessionManager()

	require.NoError(t, inner.InsertCard(&Card{
		ID: "c1", Title: "Task", Column: "dev", Status: StatusRunning,
		Agent: "ag", RepoPath: "rp", Branch: "br", Worktree: "wt", Session: "s1",
	}))

	racy := &raceStore{Store: inner, newColumn: "review"}
	poller := newPoller(racy, sessions, func() {})

	// Drive the stable-count to threshold-1 so the next poll triggers the
	// Running->Waiting transition.
	sessions.setPaneContent("idle")
	for range stableThreshold {
		poller.poll()
	}

	// The next poll fires the transition. The wrapper has moved the card's
	// column on the way in; the transition write must not revert it.
	poller.poll()

	got, err := inner.GetCard("c1")
	require.NoError(t, err)
	assert.Equal(t, "review", got.Column, "poller must not revert a concurrent column change")
	assert.Equal(t, StatusWaiting, got.Status)
}

func TestPollerReconnectsDeadSession(t *testing.T) {
	store := openTestStore(t)
	sessions := newFakeSessionManager()
	poller := newPoller(store, sessions, func() {})

	require.NoError(t, store.InsertCard(&Card{
		ID: "c1", Title: "Task", Column: "dev", Status: StatusRunning,
		Agent: "ag", RepoPath: "rp", Branch: "br", Worktree: "wt",
		Session: "s1", AgentSession: "sess-1",
	}))

	// The tmux session is gone (agent exited or host rebooted).
	sessions.paneErr["s1"] = errors.New("session not found")
	poller.poll()

	require.Equal(t, []string{"s1"}, sessions.killed)
	require.Len(t, sessions.created, 1)
	assert.Equal(t, "s1", sessions.created[0].name, "reconnect reuses the tmux session name")
	assert.Equal(t, "sess-1", sessions.created[0].sessionID, "reconnect resumes the same docker-agent session")
	assert.Empty(t, sessions.created[0].worktreeName, "reconnect omits --worktree so the session reattaches to its worktree")
	assert.Equal(t, "wt", sessions.created[0].workDir, "reconnect resumes from the worktree so the agent stays isolated")
	assert.Empty(t, sessions.created[0].prompt, "reconnect resumes without a new prompt")
}

func TestPollerReconnectsDeadPane(t *testing.T) {
	store := openTestStore(t)
	sessions := newFakeSessionManager()
	poller := newPoller(store, sessions, func() {})

	require.NoError(t, store.InsertCard(&Card{
		ID: "c1", Title: "Task", Column: "dev", Status: StatusRunning,
		Agent: "ag", RepoPath: "rp", Branch: "br", Worktree: "wt",
		Session: "s1", AgentSession: "sess-1",
	}))

	// The session is still around but the agent pane has died (agent exited).
	sessions.paneDead["s1"] = true
	poller.poll()

	require.Equal(t, []string{"s1"}, sessions.killed)
	require.Len(t, sessions.created, 1)
	assert.Equal(t, "s1", sessions.created[0].name)
	assert.Equal(t, "sess-1", sessions.created[0].sessionID)
	assert.Empty(t, sessions.created[0].worktreeName)
	assert.Empty(t, sessions.created[0].prompt)
}

func TestSendPromptToCardSendsKeysWhenAlive(t *testing.T) {
	store := openTestStore(t)
	sessions := newFakeSessionManager()
	poller := newPoller(store, sessions, func() {})

	card := &Card{ID: "c1", Session: "s1", Agent: "ag", Worktree: "wt", AgentSession: "sess-1"}
	require.NoError(t, poller.SendPromptToCard(card, "hello"))

	assert.Equal(t, []string{"s1:hello"}, sessions.sentKeys)
	assert.Empty(t, sessions.created, "a live session is not recreated")
}

func TestSendPromptToCardRecreatesWhenDead(t *testing.T) {
	store := openTestStore(t)
	sessions := newFakeSessionManager()
	poller := newPoller(store, sessions, func() {})

	card := &Card{ID: "c1", Session: "s1", Agent: "ag", RepoPath: "rp", Worktree: "wt", AgentSession: "sess-1"}
	// The agent pane has died: the prompt must not be typed into it (a dead
	// pane swallows send-keys); the session is recreated with the prompt.
	sessions.paneDead["s1"] = true
	require.NoError(t, poller.SendPromptToCard(card, "hello"))

	assert.Empty(t, sessions.sentKeys, "a dead pane must not receive send-keys")
	require.Equal(t, []string{"s1"}, sessions.killed)
	require.Len(t, sessions.created, 1)
	assert.Equal(t, "s1", sessions.created[0].name)
	assert.Equal(t, "sess-1", sessions.created[0].sessionID)
	assert.Empty(t, sessions.created[0].worktreeName, "a resumed session omits --worktree")
	assert.Equal(t, "wt", sessions.created[0].workDir, "the session resumes from the worktree so the agent stays isolated")
	assert.Equal(t, "hello", sessions.created[0].prompt, "the prompt is delivered as the resumed session's next message")
}

func TestSendPromptToCardRecreatesWhenSendKeysFails(t *testing.T) {
	store := openTestStore(t)
	sessions := newFakeSessionManager()
	// The pane looks alive but send-keys fails (it died in between): fall back
	// to recreating the session with the prompt.
	sessions.sendErr = errors.New("lost pane")
	poller := newPoller(store, sessions, func() {})

	card := &Card{ID: "c1", Session: "s1", Agent: "ag", Worktree: "wt", AgentSession: "sess-1"}
	require.NoError(t, poller.SendPromptToCard(card, "hello"))

	require.Len(t, sessions.created, 1)
	assert.Equal(t, "hello", sessions.created[0].prompt)
}
