package board

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dgageot/board/pkg/agent"
)

// fakeSessionManager records tmux operations and reports configurable liveness.
type fakeSessionManager struct {
	mu      sync.Mutex
	created []newSessionCall
	killed  []string
	alive   bool
}

type newSessionCall struct {
	name, workDir, sessionID, listenSocket, worktreeName, worktreeBase, prompt string
}

func newFakeSessionManager() *fakeSessionManager {
	return &fakeSessionManager{alive: true}
}

func (f *fakeSessionManager) NewSession(name, workDir, _, sessionID, listenSocket, worktreeName, worktreeBase, prompt string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, newSessionCall{name, workDir, sessionID, listenSocket, worktreeName, worktreeBase, prompt})
	return nil
}

func (f *fakeSessionManager) KillSession(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = append(f.killed, name)
	return nil
}

func (f *fakeSessionManager) Alive(string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alive, nil
}

func (f *fakeSessionManager) calls() []newSessionCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]newSessionCall(nil), f.created...)
}

// fakeClient is a scripted control-plane client.
type fakeClient struct {
	mu           sync.Mutex
	snapErr      error
	snap         agent.Snapshot
	events       []agent.Event
	gotSince     uint64
	streamCalled bool
	followErr    error
	followKey    string
	followMsg    string
}

func (f *fakeClient) Snapshot(context.Context) (agent.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap, f.snapErr
}

func (f *fakeClient) StreamEvents(ctx context.Context, since uint64, onEvent func(agent.Event) bool) error {
	f.mu.Lock()
	f.gotSince = since
	f.streamCalled = true
	evs := append([]agent.Event(nil), f.events...)
	f.mu.Unlock()
	for _, ev := range evs {
		if !onEvent(ev) {
			return nil
		}
	}
	// Block until the watcher is canceled so we don't busy-loop reconnecting.
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeClient) Followup(_ context.Context, key, msg string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.followKey, f.followMsg = key, msg
	return false, f.followErr
}

// newTestController builds a controller wired to the given fake client.
func newTestController(t *testing.T, store Store, sessions SessionManager, client sessionClient) *Controller {
	t.Helper()
	c := newController(t.Context(), store, sessions, func() {})
	c.clientFor = func(string, string) sessionClient { return client }
	return c
}

func devCard() *Card {
	return &Card{
		ID: "c1", Title: "old", Column: "dev", Status: StatusWaiting,
		Agent: "ag", RepoPath: "rp", Branch: "br", Worktree: "wt",
		Session: "s1", AgentSession: "sess-1",
	}
}

func TestControllerSnapshotSetsTitle(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard()))

	client := &fakeClient{snap: agent.Snapshot{Title: "Real Title"}}
	c := newTestController(t, store, newFakeSessionManager(), client)
	c.Start(devCard())

	require.Eventually(t, func() bool {
		card, err := store.GetCard("c1")
		return err == nil && card.Title == "Real Title"
	}, time.Second, 5*time.Millisecond)
}

// The controller mirrors the session's cumulative cost from the snapshot onto
// the card so it can be shown on the board.
func TestControllerSnapshotSetsCost(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard()))

	client := &fakeClient{snap: agent.Snapshot{Cost: 1.25}}
	c := newTestController(t, store, newFakeSessionManager(), client)
	c.Start(devCard())

	require.Eventually(t, func() bool {
		card, err := store.GetCard("c1")
		return err == nil && card.Cost == 1.25
	}, time.Second, 5*time.Millisecond)
}

// The snapshot's streaming flag is unreliable for attached sessions, so the
// controller must replay the whole buffer (since 0) rather than tail from the
// snapshot's last seq — otherwise a stream_started emitted before it connected
// is missed and a working card never turns running.
func TestControllerTailsFromBufferStart(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard()))

	client := &fakeClient{snap: agent.Snapshot{LastEventSeq: 7, Streaming: false}}
	c := newTestController(t, store, newFakeSessionManager(), client)
	c.Start(devCard())

	require.Eventually(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return client.streamCalled
	}, time.Second, 5*time.Millisecond)

	client.mu.Lock()
	defer client.mu.Unlock()
	assert.Equal(t, uint64(0), client.gotSince)
}

// A turn already in progress when the watcher connects: its stream_started is
// replayed from the buffer with no matching stop, so the card is running even
// though the snapshot reports streaming=false.
func TestControllerOpenStreamIsRunning(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard())) // starts waiting

	client := &fakeClient{
		snap:   agent.Snapshot{Streaming: false},
		events: []agent.Event{{Type: agent.EventStreamStarted}},
	}
	c := newTestController(t, store, newFakeSessionManager(), client)
	c.Start(devCard())

	require.Eventually(t, func() bool {
		card, err := store.GetCard("c1")
		return err == nil && card.Status == StatusRunning
	}, time.Second, 5*time.Millisecond)
}

func TestControllerEventsDriveStatusAndTitle(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard()))

	client := &fakeClient{
		snap: agent.Snapshot{Streaming: false},
		events: []agent.Event{
			{Type: agent.EventStreamStarted},
			{Type: agent.EventSessionTitle, Title: "Generated"},
			{Type: agent.EventStreamStopped},
		},
	}
	c := newTestController(t, store, newFakeSessionManager(), client)
	c.Start(devCard())

	require.Eventually(t, func() bool {
		card, err := store.GetCard("c1")
		return err == nil && card.Title == "Generated" && card.Status == StatusWaiting
	}, time.Second, 5*time.Millisecond)
}

// A failed turn: cagent emits the error event, then still fires
// stream_stopped. The card must end up red, not waiting.
func TestControllerErrorEventMarksCardError(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard()))

	client := &fakeClient{
		snap: agent.Snapshot{Streaming: false},
		events: []agent.Event{
			{Type: agent.EventStreamStarted},
			{Type: agent.EventError},
			{Type: agent.EventStreamStopped},
		},
	}
	c := newTestController(t, store, newFakeSessionManager(), client)
	c.Start(devCard())

	require.Eventually(t, func() bool {
		card, err := store.GetCard("c1")
		return err == nil && card.Status == StatusError
	}, time.Second, 5*time.Millisecond)
}

// cagent delivers the error event reliably but the trailing stream_stopped is
// best-effort and can be dropped. The card must still go red on the error
// event alone, without waiting for a stop that may never arrive.
func TestControllerErrorWithoutStopMarksCardError(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard()))

	client := &fakeClient{
		snap: agent.Snapshot{Streaming: false},
		events: []agent.Event{
			{Type: agent.EventStreamStarted},
			{Type: agent.EventError},
		},
	}
	c := newTestController(t, store, newFakeSessionManager(), client)
	c.Start(devCard())

	require.Eventually(t, func() bool {
		card, err := store.GetCard("c1")
		return err == nil && card.Status == StatusError
	}, time.Second, 5*time.Millisecond)
}

// A turn blocked on /pause: runtime_paused turns the card paused (whitish).
func TestControllerPausedEventMarksCardPaused(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard()))

	client := &fakeClient{
		snap: agent.Snapshot{Streaming: false},
		events: []agent.Event{
			{Type: agent.EventStreamStarted},
			{Type: agent.EventRuntimePaused},
		},
	}
	c := newTestController(t, store, newFakeSessionManager(), client)
	c.Start(devCard())

	require.Eventually(t, func() bool {
		card, err := store.GetCard("c1")
		return err == nil && card.Status == StatusPaused
	}, time.Second, 5*time.Millisecond)
}

// There is no resume event: the run loop simply starts emitting events again.
// Any event after runtime_paused must flip the card back to running.
func TestControllerActivityAfterPauseResumesRunning(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard()))

	client := &fakeClient{
		snap: agent.Snapshot{Streaming: false},
		events: []agent.Event{
			{Type: agent.EventStreamStarted},
			{Type: agent.EventRuntimePaused},
			{Type: "tool_call"}, // resumed: the loop emits again
		},
	}
	c := newTestController(t, store, newFakeSessionManager(), client)
	c.Start(devCard())

	require.Eventually(t, func() bool {
		card, err := store.GetCard("c1")
		return err == nil && card.Status == StatusRunning
	}, time.Second, 5*time.Millisecond)
}

// A turn that ends while paused (e.g. canceled) must not stay whitish.
func TestControllerStopAfterPauseIsWaiting(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard()))

	client := &fakeClient{
		snap: agent.Snapshot{Streaming: false},
		events: []agent.Event{
			{Type: agent.EventStreamStarted},
			{Type: agent.EventRuntimePaused},
			{Type: agent.EventStreamStopped},
		},
	}
	c := newTestController(t, store, newFakeSessionManager(), client)
	c.Start(devCard())

	require.Eventually(t, func() bool {
		card, err := store.GetCard("c1")
		return err == nil && card.Status == StatusWaiting
	}, time.Second, 5*time.Millisecond)
}

// A new turn after a failure clears the sticky error: stream_started flips the
// card back to running.
func TestControllerStreamStartedClearsError(t *testing.T) {
	store := openTestStore(t)
	errored := devCard()
	errored.Status = StatusError
	require.NoError(t, store.InsertCard(errored))

	client := &fakeClient{
		snap:   agent.Snapshot{Streaming: false},
		events: []agent.Event{{Type: agent.EventStreamStarted}},
	}
	c := newTestController(t, store, newFakeSessionManager(), client)
	c.Start(devCard())

	require.Eventually(t, func() bool {
		card, err := store.GetCard("c1")
		return err == nil && card.Status == StatusRunning
	}, time.Second, 5*time.Millisecond)
}

// A reconnect replays the whole event buffer. Historical events at or below
// the snapshot's seq must not be re-broadcast one by one: a long-resolved
// error would flash the card red on every reconnect. Only the state derived
// at the end of the replay is applied — here it matches the stored one, so
// nothing changes at all.
func TestControllerReplayDoesNotBroadcastHistory(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard())) // waiting

	// A failed turn followed by a successful one, all in the past.
	client := &fakeClient{
		snap: agent.Snapshot{LastEventSeq: 5},
		events: []agent.Event{
			{Type: agent.EventStreamStarted, Seq: 1},
			{Type: agent.EventError, Seq: 2},
			{Type: agent.EventStreamStopped, Seq: 3},
			{Type: agent.EventStreamStarted, Seq: 4},
			{Type: agent.EventStreamStopped, Seq: 5},
		},
	}

	var changes atomic.Int32
	c := newController(t.Context(), store, newFakeSessionManager(), func() { changes.Add(1) })
	c.clientFor = func(string, string) sessionClient { return client }
	c.Start(devCard())

	require.Eventually(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return client.streamCalled
	}, time.Second, 5*time.Millisecond)
	time.Sleep(100 * time.Millisecond) // let the replay finish

	card, err := store.GetCard("c1")
	require.NoError(t, err)
	assert.Equal(t, StatusWaiting, card.Status)
	assert.Zero(t, changes.Load(), "replayed history must not be re-broadcast")
}

// A turn still open at the end of the replay is real state, not history: the
// derived running status is applied once the replay catches up.
func TestControllerReplayedOpenTurnIsApplied(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard())) // waiting

	client := &fakeClient{
		snap:   agent.Snapshot{LastEventSeq: 1},
		events: []agent.Event{{Type: agent.EventStreamStarted, Seq: 1}},
	}
	c := newTestController(t, store, newFakeSessionManager(), client)
	c.Start(devCard())

	require.Eventually(t, func() bool {
		card, err := store.GetCard("c1")
		return err == nil && card.Status == StatusRunning
	}, time.Second, 5*time.Millisecond)
}

// Replayed title events are stale by definition: the snapshot's title already
// reflects them and must win.
func TestControllerReplayedTitleIsIgnored(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard()))

	client := &fakeClient{
		snap: agent.Snapshot{Title: "Fresh", LastEventSeq: 2},
		events: []agent.Event{
			{Type: agent.EventSessionTitle, Title: "Stale", Seq: 1},
			{Type: agent.EventStreamStarted, Seq: 2},
		},
	}
	c := newTestController(t, store, newFakeSessionManager(), client)
	c.Start(devCard())

	require.Eventually(t, func() bool {
		card, err := store.GetCard("c1")
		return err == nil && card.Status == StatusRunning
	}, time.Second, 5*time.Millisecond)

	card, err := store.GetCard("c1")
	require.NoError(t, err)
	assert.Equal(t, "Fresh", card.Title, "the snapshot title wins over replayed title events")
}

func TestControllerNestedStreamsStayRunning(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard()))

	// A turn with a sub-agent: parent starts, sub-agent starts then stops.
	// The parent is still working, so the card must stay running.
	client := &fakeClient{
		snap: agent.Snapshot{Streaming: false},
		events: []agent.Event{
			{Type: agent.EventStreamStarted}, // parent
			{Type: agent.EventStreamStarted}, // sub-agent
			{Type: agent.EventStreamStopped}, // sub-agent done, parent still running
		},
	}
	c := newTestController(t, store, newFakeSessionManager(), client)
	c.Start(devCard())

	require.Eventually(t, func() bool {
		card, err := store.GetCard("c1")
		return err == nil && card.Status == StatusRunning
	}, time.Second, 5*time.Millisecond)

	// The inner stop must not have flipped the card to waiting.
	card, err := store.GetCard("c1")
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, card.Status)
}

// A sub-agent's failure is not turn-fatal: the parent captures the error and
// finishes the turn normally. The mid-turn error event turns the card red, but
// the outermost stop's "normal" reason is authoritative and must clear it —
// otherwise the card stays red until the user's next prompt.
func TestControllerSubAgentErrorRecoveredByParent(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard()))

	client := &fakeClient{
		snap: agent.Snapshot{Streaming: false},
		events: []agent.Event{
			{Type: agent.EventStreamStarted},                   // parent
			{Type: agent.EventStreamStarted},                   // sub-agent
			{Type: agent.EventError},                           // sub-agent fails
			{Type: agent.EventStreamStopped, Reason: "error"},  // sub-agent ends
			{Type: agent.EventStreamStopped, Reason: "normal"}, // parent completes fine
		},
	}
	c := newTestController(t, store, newFakeSessionManager(), client)
	c.Start(devCard())

	require.Eventually(t, func() bool {
		card, err := store.GetCard("c1")
		return err == nil && card.Status == StatusWaiting
	}, time.Second, 5*time.Millisecond)
}

// A turn that genuinely fails ends with a stop whose reason is "error": the
// card must stay red, the normal-stop recovery must not kick in.
func TestControllerFailedTurnStaysError(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard()))

	client := &fakeClient{
		snap: agent.Snapshot{Streaming: false},
		events: []agent.Event{
			{Type: agent.EventStreamStarted},
			{Type: agent.EventError},
			{Type: agent.EventStreamStopped, Reason: "error"},
		},
	}
	c := newTestController(t, store, newFakeSessionManager(), client)
	c.Start(devCard())

	require.Eventually(t, func() bool {
		card, err := store.GetCard("c1")
		return err == nil && card.Status == StatusError
	}, time.Second, 5*time.Millisecond)
}

// Stream events are delivered best-effort: a dropped stream_stopped leaves the
// depth counter skewed, and the drop never reaches the replay buffer either.
// user_message — emitted only when a real user turn starts — is the resync
// point: it must zero the drifted depth so the next turn's stop flips the card
// back to waiting instead of leaving it stuck running forever.
func TestControllerUserMessageHealsDroppedStop(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard()))

	client := &fakeClient{
		snap: agent.Snapshot{Streaming: false},
		events: []agent.Event{
			{Type: agent.EventStreamStarted}, // turn 1; its stop was dropped
			{Type: agent.EventUserMessage},   // turn 2 begins: resync
			{Type: agent.EventStreamStarted},
			{Type: agent.EventStreamStopped, Reason: "normal"},
		},
	}
	c := newTestController(t, store, newFakeSessionManager(), client)
	c.Start(devCard())

	require.Eventually(t, func() bool {
		card, err := store.GetCard("c1")
		return err == nil && card.Status == StatusWaiting
	}, time.Second, 5*time.Millisecond)
}

func TestControllerRelaunchesWhenSessionDead(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard()))

	sessions := newFakeSessionManager()
	sessions.alive = false // pane is dead: the watcher must relaunch
	client := &fakeClient{snapErr: errors.New("connection refused")}
	c := newTestController(t, store, sessions, client)
	c.Start(devCard())

	require.Eventually(t, func() bool { return len(sessions.calls()) > 0 }, time.Second, 5*time.Millisecond)

	call := sessions.calls()[0]
	assert.Equal(t, "s1", call.name, "relaunch reuses the tmux session name")
	assert.Equal(t, "sess-1", call.sessionID, "relaunch resumes the same docker-agent session")
	assert.Empty(t, call.worktreeName, "relaunch omits --worktree so the session reattaches")
	assert.Equal(t, "wt", call.workDir, "relaunch resumes from the worktree")
	assert.Equal(t, socketPath("sess-1"), call.listenSocket, "relaunch reuses the same socket")
	assert.Empty(t, call.prompt)
}

// A killed agent leaves its control-plane socket file behind; a unix listener
// cannot bind a path that already exists, so relaunch must remove the stale
// socket or the resumed run never exposes its control plane.
func TestRelaunchRemovesStaleSocket(t *testing.T) {
	store := openTestStore(t)
	c := newTestController(t, store, newFakeSessionManager(), &fakeClient{})

	card := devCard()
	socket := socketPath(card.AgentSession)
	require.NoError(t, os.MkdirAll(filepath.Dir(socket), 0o755))
	require.NoError(t, os.WriteFile(socket, nil, 0o600))
	t.Cleanup(func() { _ = os.Remove(socket) })

	require.NoError(t, c.relaunch(card, ""))

	_, err := os.Stat(socket)
	assert.True(t, os.IsNotExist(err), "relaunch must remove the stale control-plane socket")
}

// A relaunched agent is starting again: the card must turn blue until its
// control plane answers.
func TestRelaunchMarksCardStarting(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard())) // starts waiting
	c := newTestController(t, store, newFakeSessionManager(), &fakeClient{})

	require.NoError(t, c.relaunch(devCard(), ""))

	card, err := store.GetCard("c1")
	require.NoError(t, err)
	assert.Equal(t, StatusStarting, card.Status)
}

// Once the control plane answers, the agent has started: a card still marked
// starting defaults to waiting (the event replay corrects it if a turn is
// already underway).
func TestControllerSnapshotClearsStarting(t *testing.T) {
	store := openTestStore(t)
	card := devCard()
	card.Status = StatusStarting
	require.NoError(t, store.InsertCard(card))

	client := &fakeClient{snap: agent.Snapshot{}}
	c := newTestController(t, store, newFakeSessionManager(), client)
	c.Start(card)

	require.Eventually(t, func() bool {
		card, err := store.GetCard("c1")
		return err == nil && card.Status == StatusWaiting
	}, time.Second, 5*time.Millisecond)
}

// A launch that carried an initial prompt is about to run its first turn: the
// control plane answers while the agent is still initializing (MCP servers,
// RAG indexing…), before the prompt is submitted. The card must stay
// "starting" — not flash green — until the turn's stream_started arrives.
func TestControllerExpectedTurnKeepsStarting(t *testing.T) {
	store := openTestStore(t)
	card := devCard()
	card.Status = StatusStarting
	require.NoError(t, store.InsertCard(card))

	client := &fakeClient{snap: agent.Snapshot{}} // control plane answers, no events yet
	c := newTestController(t, store, newFakeSessionManager(), client)
	c.ExpectTurn(card.ID)
	c.Start(card)

	require.Eventually(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return client.streamCalled
	}, time.Second, 5*time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	got, err := store.GetCard("c1")
	require.NoError(t, err)
	assert.Equal(t, StatusStarting, got.Status, "a card expecting its first turn must not turn green before the turn starts")
}

// The expected turn arriving (stream_started) flips the card to running and
// clears the expectation, so the turn's end flips it to waiting as usual.
func TestControllerExpectedTurnRunsThenWaits(t *testing.T) {
	store := openTestStore(t)
	card := devCard()
	card.Status = StatusStarting
	require.NoError(t, store.InsertCard(card))

	client := &fakeClient{
		snap: agent.Snapshot{},
		events: []agent.Event{
			{Type: agent.EventUserMessage},
			{Type: agent.EventStreamStarted},
			{Type: agent.EventStreamStopped, Reason: agent.ReasonNormal},
		},
	}
	c := newTestController(t, store, newFakeSessionManager(), client)
	c.ExpectTurn(card.ID)
	c.Start(card)

	require.Eventually(t, func() bool {
		card, err := store.GetCard("c1")
		return err == nil && card.Status == StatusWaiting
	}, time.Second, 5*time.Millisecond)
	expected, _ := c.turnExpected(card.ID)
	assert.False(t, expected, "stream_started clears the expected turn")
}

// An expected turn that never starts must not pin the card at "starting"
// forever — a starting card counts as busy and cannot even be moved. Past
// expectTurnTimeout the hold is dropped: the stream is cut when the
// expectation expires, the watcher re-evaluates it and falls back to waiting.
func TestControllerExpectedTurnExpires(t *testing.T) {
	store := openTestStore(t)
	card := devCard()
	card.Status = StatusStarting
	require.NoError(t, store.InsertCard(card))

	client := &fakeClient{snap: agent.Snapshot{}} // control plane answers, the turn never starts
	c := newTestController(t, store, newFakeSessionManager(), client)
	// An expectation about to expire: the hold lasts a few more milliseconds.
	c.mu.Lock()
	c.expectTurn[card.ID] = time.Now().Add(50 * time.Millisecond).Add(-expectTurnTimeout)
	c.mu.Unlock()
	c.Start(card)

	require.Eventually(t, func() bool {
		card, err := store.GetCard("c1")
		return err == nil && card.Status == StatusWaiting
	}, time.Second, 5*time.Millisecond)
	expected, _ := c.turnExpected(card.ID)
	assert.False(t, expected, "an expired expectation is dropped")
}

// A prompt-bearing relaunch (SendPrompt to a dead agent) runs the prompt as
// its first turn: the relaunched card must stay "starting" until that turn
// begins, exactly like a fresh launch with a prompt.
func TestRelaunchWithPromptExpectsTurn(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard()))
	c := newTestController(t, store, newFakeSessionManager(), &fakeClient{})

	require.NoError(t, c.relaunch(devCard(), "do it"))
	expected, _ := c.turnExpected("c1")
	assert.True(t, expected, "a prompt-bearing relaunch expects a first turn")

	// A plain resume (no prompt) must clear a stale expectation: no turn is
	// coming, so the card may turn green once the control plane answers.
	require.NoError(t, c.relaunch(devCard(), ""))
	expected, _ = c.turnExpected("c1")
	assert.False(t, expected, "a promptless relaunch expects no turn")
}

// Stop drops the card's turn expectation along with its watcher, so a deleted
// card does not leak an entry.
func TestControllerStopClearsExpectTurn(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard()))
	c := newTestController(t, store, newFakeSessionManager(), &fakeClient{})

	c.ExpectTurn("c1")
	c.Start(devCard())
	c.Stop("c1")

	expected, _ := c.turnExpected("c1")
	assert.False(t, expected)
}

func TestControllerDoesNotRelaunchWhileStarting(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard()))

	sessions := newFakeSessionManager()
	sessions.alive = true // still starting: connection fails but pane is alive
	client := &fakeClient{snapErr: errors.New("connection refused")}
	c := newTestController(t, store, sessions, client)
	c.Start(devCard())

	time.Sleep(200 * time.Millisecond)
	assert.Empty(t, sessions.calls(), "a starting session must not be relaunched")
}

// An alive agent whose control plane lacks GET /snapshot runs an old
// docker-agent binary: the watcher must relaunch it (once) so the session
// resumes on the currently installed binary, instead of retrying the 404
// forever and leaving the card stuck at "starting".
func TestControllerRelaunchesUnsupportedAgentOnce(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard()))

	sessions := newFakeSessionManager()
	sessions.alive = true // the old agent is alive, just unusable
	client := &fakeClient{snapErr: fmt.Errorf("snapshot: 404: %w", agent.ErrUnsupported)}
	c := newTestController(t, store, sessions, client)
	c.Start(devCard())

	require.Eventually(t, func() bool { return len(sessions.calls()) == 1 }, time.Second, 5*time.Millisecond)

	// If the relaunched binary is still too old, do not thrash kill/restart.
	time.Sleep(200 * time.Millisecond)
	assert.Len(t, sessions.calls(), 1, "an unsupported agent is relaunched at most once per watcher")
}

func TestSendPromptUsesFollowup(t *testing.T) {
	store := openTestStore(t)
	sessions := newFakeSessionManager()
	client := &fakeClient{}
	c := newTestController(t, store, sessions, client)

	require.NoError(t, c.SendPrompt(devCard(), "hello"))

	client.mu.Lock()
	defer client.mu.Unlock()
	assert.Equal(t, "hello", client.followMsg)
	assert.NotEmpty(t, client.followKey, "a follow-up carries an idempotency key")
	assert.Empty(t, sessions.calls(), "a reachable session is not relaunched")
}

func TestSendPromptRelaunchesWhenAgentIsGone(t *testing.T) {
	store := openTestStore(t)
	sessions := newFakeSessionManager()
	sessions.alive = false // the agent's tmux pane is gone
	client := &fakeClient{followErr: errors.New("connection refused")}
	c := newTestController(t, store, sessions, client)

	require.NoError(t, c.SendPrompt(devCard(), "hello"))

	require.Len(t, sessions.calls(), 1)
	call := sessions.calls()[0]
	assert.Equal(t, "s1", call.name)
	assert.Equal(t, "hello", call.prompt, "the prompt is delivered as the resumed session's next message")
	assert.Empty(t, call.worktreeName)
}

func TestSendPromptSurfacesErrorWhenAgentAlive(t *testing.T) {
	store := openTestStore(t)
	sessions := newFakeSessionManager()
	sessions.alive = true // agent is up; the follow-up failed for another reason
	client := &fakeClient{followErr: errors.New("429 queue full")}
	c := newTestController(t, store, sessions, client)

	err := c.SendPrompt(devCard(), "hello")
	require.Error(t, err)
	assert.Empty(t, sessions.calls(), "a live agent must never be relaunched on a transient follow-up error")
}

func TestSendPromptEmptyIsNoop(t *testing.T) {
	store := openTestStore(t)
	sessions := newFakeSessionManager()
	c := newTestController(t, store, sessions, &fakeClient{})

	require.NoError(t, c.SendPrompt(devCard(), ""))
	assert.Empty(t, sessions.calls())
}

func TestControllerStopEndsWatcher(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard()))

	c := newTestController(t, store, newFakeSessionManager(), &fakeClient{snapErr: errors.New("x")})
	c.Start(devCard())
	c.Stop("c1")

	c.mu.Lock()
	_, ok := c.watchers["c1"]
	c.mu.Unlock()
	assert.False(t, ok, "watcher is removed on stop")
}

func TestMoveCardToColumnReinserts(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.SeedColumns(defaultColumns))
	require.NoError(t, store.InsertCard(devCard()))

	sessions := newFakeSessionManager()
	client := &fakeClient{}
	c := newTestController(t, store, sessions, client)

	card := devCard()
	require.NoError(t, c.MoveCardToColumn(card, "review", true))
	require.NoError(t, c.SendPrompt(card, "Review the changes"))

	got, _ := store.GetCard("c1")
	assert.Equal(t, "review", got.Column)
	assert.Equal(t, StatusWaiting, got.Status, "the move leaves the status untouched; the watcher drives running")

	client.mu.Lock()
	assert.Equal(t, "Review the changes", client.followMsg)
	client.mu.Unlock()
}

// A move never changes the card's status: the color tracks the agent's
// activity, not the move itself.
func TestMoveCardToColumnPreservesStatus(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.SeedColumns(defaultColumns))
	running := devCard()
	running.Status = StatusRunning
	require.NoError(t, store.InsertCard(running))

	c := newTestController(t, store, newFakeSessionManager(), &fakeClient{})

	card := devCard()
	card.Status = StatusRunning
	require.NoError(t, c.MoveCardToColumn(card, "done", false))

	got, _ := store.GetCard("c1")
	assert.Equal(t, "done", got.Column)
	assert.Equal(t, StatusRunning, got.Status)
}

// When a turn finishes, the controller looks up the PR whose head commit is
// the card's worktree HEAD and records its URL.
func TestControllerRecordsPRURLOnTurnEnd(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard()))

	client := &fakeClient{
		snap: agent.Snapshot{Streaming: false},
		events: []agent.Event{
			{Type: agent.EventStreamStarted},
			{Type: agent.EventStreamStopped, Reason: agent.ReasonNormal},
		},
	}
	c := newTestController(t, store, newFakeSessionManager(), client)
	c.prURLForHead = func(context.Context, string) (string, error) {
		return "https://github.com/o/r/pull/7", nil
	}
	c.Start(devCard())

	require.Eventually(t, func() bool {
		card, err := store.GetCard("c1")
		return err == nil && card.PRURL == "https://github.com/o/r/pull/7"
	}, time.Second, 5*time.Millisecond)
}

// No match (e.g. unpushed local commits, or a transient `gh` failure) never
// clears a URL the card already shows.
func TestControllerEmptyPRURLDoesNotClear(t *testing.T) {
	store := openTestStore(t)
	card := devCard()
	card.PRURL = "https://github.com/o/r/pull/7"
	require.NoError(t, store.InsertCard(card))

	client := &fakeClient{
		snap: agent.Snapshot{Streaming: false},
		events: []agent.Event{
			{Type: agent.EventStreamStarted},
			{Type: agent.EventStreamStopped, Reason: agent.ReasonNormal},
		},
	}
	c := newTestController(t, store, newFakeSessionManager(), client)
	c.prURLForHead = func(context.Context, string) (string, error) { return "", nil } // no match
	c.Start(devCard())

	require.Eventually(t, func() bool {
		card, err := store.GetCard("c1")
		return err == nil && card.Status == StatusWaiting
	}, time.Second, 5*time.Millisecond)

	card, err := store.GetCard("c1")
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/o/r/pull/7", card.PRURL, "no match must not clear the stored PR")
}
