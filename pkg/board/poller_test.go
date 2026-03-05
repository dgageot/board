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

func (f *fakeSessionManager) setPaneContent(name, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paneContent[name] = content
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
	sessions.setPaneContent("s1", "some output")
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
	sessions.setPaneContent("s1", "stable output")
	for range stableThreshold + 1 {
		poller.poll()
	}

	card, _ := store.GetCard("c1")
	require.Equal(t, StatusWaiting, card.Status)

	// Content changes → card should go back to running.
	sessions.setPaneContent("s1", "new output")
	poller.poll()

	card, _ = store.GetCard("c1")
	assert.Equal(t, StatusRunning, card.Status)
}

func TestPollerAutoAdvancesToNextColumn(t *testing.T) {
	store := openTestStore(t)
	sessions := newFakeSessionManager()
	poller := newPoller(store, sessions, func() {})

	require.NoError(t, store.SeedColumns([]Column{
		{ID: "dev", Name: "Dev", Emoji: "🔨", Prompt: ""},
		{ID: "review", Name: "Review", Emoji: "🔍", Prompt: "Review changes"},
		{ID: "done", Name: "Done", Emoji: "✅", Prompt: ""},
	}))

	require.NoError(t, store.InsertCard(&Card{
		ID: "c1", Title: "Task", Column: "dev", Status: StatusRunning, Auto: true,
		Agent: "ag", RepoPath: "rp", Branch: "br", Worktree: "wt", Session: "s1",
	}))

	// Drive to waiting state → should auto-advance to review.
	sessions.setPaneContent("s1", "done output")
	for range stableThreshold + 1 {
		poller.poll()
	}

	card, _ := store.GetCard("c1")
	assert.Equal(t, "review", card.Column)
	assert.Equal(t, StatusRunning, card.Status)

	// Verify that the review prompt was sent.
	sessions.mu.Lock()
	assert.Contains(t, sessions.sentKeys, card.Session+":Review changes")
	sessions.mu.Unlock()
}

func TestPollerAutoAdvanceStopsAtLastColumn(t *testing.T) {
	store := openTestStore(t)
	sessions := newFakeSessionManager()
	poller := newPoller(store, sessions, func() {})

	require.NoError(t, store.SeedColumns([]Column{
		{ID: "dev", Name: "Dev", Emoji: "🔨", Prompt: ""},
		{ID: "done", Name: "Done", Emoji: "✅", Prompt: ""},
	}))

	require.NoError(t, store.InsertCard(&Card{
		ID: "c1", Title: "Task", Column: "done", Status: StatusRunning, Auto: true,
		Agent: "ag", RepoPath: "rp", Branch: "br", Worktree: "wt", Session: "s1",
	}))

	// Drive to waiting state — no next column so it stays in "done".
	sessions.setPaneContent("s1", "final output")
	for range stableThreshold + 1 {
		poller.poll()
	}

	card, _ := store.GetCard("c1")
	assert.Equal(t, "done", card.Column)
	assert.Equal(t, StatusWaiting, card.Status)
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
	sessions.setPaneContent("s1", "output")
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

	sessions.setPaneContent("s1", "output")
	for range stableThreshold + 2 {
		poller.poll()
	}

	card, _ := store.GetCard("c1")
	assert.Equal(t, StatusDone, card.Status)
}

func TestFullCardLifecycleAcrossMultipleColumns(t *testing.T) {
	store := openTestStore(t)
	sessions := newFakeSessionManager()
	poller := newPoller(store, sessions, func() {})

	require.NoError(t, store.SeedColumns([]Column{
		{ID: "dev", Name: "Dev", Emoji: "🔨", Prompt: ""},
		{ID: "review", Name: "Review", Emoji: "🔍", Prompt: "Review the code"},
		{ID: "fix", Name: "Fix", Emoji: "🔧", Prompt: "Fix issues"},
		{ID: "done", Name: "Done", Emoji: "✅", Prompt: ""},
	}))

	require.NoError(t, store.InsertCard(&Card{
		ID: "c1", Title: "Full lifecycle", Column: "dev", Status: StatusRunning, Auto: true,
		Agent: "ag", RepoPath: "rp", Branch: "br", Worktree: "wt", Session: "s1",
	}))

	advanceCard := func(content string) {
		sessions.setPaneContent("s1", content)
		// +1 for baseline poll, + stableThreshold for stability detection
		for range stableThreshold + 1 {
			poller.poll()
		}
		// After auto-advance, the session may be reused, so update pane content
		// for the card's potentially new session.
		card, _ := store.GetCard("c1")
		sessions.setPaneContent(card.Session, content)
	}

	// dev → review
	advanceCard("dev output")
	card, _ := store.GetCard("c1")
	assert.Equal(t, "review", card.Column)

	// review → fix
	advanceCard("review output")
	card, _ = store.GetCard("c1")
	assert.Equal(t, "fix", card.Column)

	// fix → done (no prompt, so status should be waiting)
	advanceCard("fix output")
	card, _ = store.GetCard("c1")
	assert.Equal(t, "done", card.Column)
	assert.Equal(t, StatusWaiting, card.Status)
}
