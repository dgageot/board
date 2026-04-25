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
