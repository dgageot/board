package board

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSessionManager records calls and returns configurable pane content.
type fakeSessionManager struct {
	mu          sync.Mutex
	paneContent map[string]string
	created     []string
	killed      []string
	sentKeys    []string
}

func newFakeSessionManager() *fakeSessionManager {
	return &fakeSessionManager{
		paneContent: make(map[string]string),
	}
}

func (f *fakeSessionManager) NewSession(name, _, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, name)
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
	f.sentKeys = append(f.sentKeys, name+":"+msg)
	return nil
}

func (f *fakeSessionManager) PaneContent(name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.paneContent[name], nil
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
