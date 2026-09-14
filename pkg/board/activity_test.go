package board

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dgageot/board/pkg/agent"
)

func newTestActivity(t *testing.T) (*cardActivity, *SQLiteStore) {
	t.Helper()
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard()))
	c := newTestController(t, store, newFakeSessionManager(), &fakeClient{})
	return &cardActivity{controller: c, cardID: "c1", rootStatus: StatusWaiting}, store
}

func assertCardStatus(t *testing.T, store Store, want CardStatus) {
	t.Helper()
	card, err := store.GetCard("c1")
	require.NoError(t, err)
	assert.Equal(t, want, card.Status)
}

func TestActivityWorkingTabWinsOverRootEvents(t *testing.T) {
	for _, rootStatus := range []CardStatus{StatusWaiting, StatusError, StatusPaused, StatusStarting} {
		t.Run(string(rootStatus), func(t *testing.T) {
			state, store := newTestActivity(t)
			state.update([]agent.SessionActivity{{ID: "other-tab", Streaming: true}}, nil, 0)
			state.setRootStatus(rootStatus)
			assertCardStatus(t, store, StatusRunning)
			state.update([]agent.SessionActivity{{ID: "other-tab"}}, nil, 0)
			want := StatusWaiting
			if rootStatus == StatusError {
				want = StatusError
			}
			assertCardStatus(t, store, want)
		})
	}
}

func TestActivityHealsDroppedStreamEvents(t *testing.T) {
	state, store := newTestActivity(t)
	// No start event, including on reconnect with an evicted replay buffer.
	state.update([]agent.SessionActivity{{ID: "sess-1", Streaming: true}}, nil, 0)
	assertCardStatus(t, store, StatusRunning)
	state.setRootStatus(StatusRunning)
	// No stop event: live idle state must clear even a stale replayed start.
	state.update([]agent.SessionActivity{{ID: "sess-1"}}, nil, 0)
	assertCardStatus(t, store, StatusWaiting)
	state.setRootStatus(StatusRunning)
	assertCardStatus(t, store, StatusWaiting)
}

func TestActivityPausedTabDoesNotHideWorkingTab(t *testing.T) {
	state, store := newTestActivity(t)
	state.update([]agent.SessionActivity{
		{ID: "sess-1", Streaming: true, Paused: true},
		{ID: "tab-2", Streaming: true},
	}, nil, 0)
	assertCardStatus(t, store, StatusRunning)
	state.update([]agent.SessionActivity{{ID: "sess-1", Streaming: true, Paused: true}}, nil, 0)
	assertCardStatus(t, store, StatusPaused)
	state.update([]agent.SessionActivity{{ID: "sess-1", Streaming: true}}, nil, 0)
	assertCardStatus(t, store, StatusRunning)
	state.update([]agent.SessionActivity{}, nil, 0)
	assertCardStatus(t, store, StatusWaiting)
}

func TestActivityFailureIsNotIdle(t *testing.T) {
	state, store := newTestActivity(t)
	state.update(nil, agent.ErrActivityUnsupported, 0)
	assertCardStatus(t, store, StatusUnknown)
	state.setRootStatus(StatusWaiting)
	assertCardStatus(t, store, StatusUnknown)
	assert.True(t, StatusUnknown.Busy())
	state.update([]agent.SessionActivity{{ID: "tab-2", Streaming: true}}, nil, 0)
	assertCardStatus(t, store, StatusRunning)
	state.update(nil, errors.New("timeout"), 0)
	state.setRootStatus(StatusWaiting)
	assertCardStatus(t, store, StatusRunning)
	state.update([]agent.SessionActivity{{ID: "tab-2"}}, nil, 0)
	assertCardStatus(t, store, StatusWaiting)
}

func TestActivityExpectedTurnStartsWithoutEvent(t *testing.T) {
	state, store := newTestActivity(t)
	state.controller.ExpectTurn("c1")
	state.update([]agent.SessionActivity{}, nil, 0)
	assertCardStatus(t, store, StatusStarting)
	state.update([]agent.SessionActivity{{ID: "sess-1", Streaming: true}}, nil, 0)
	assertCardStatus(t, store, StatusRunning)
	expected, _ := state.controller.turnExpected("c1")
	assert.False(t, expected)
	state.update([]agent.SessionActivity{}, nil, 0)
	assertCardStatus(t, store, StatusWaiting)
}

func TestActivityAndRootEventsHaveOneStatusWriter(t *testing.T) {
	state, store := newTestActivity(t)
	active := []agent.SessionActivity{{ID: "tab-2", Streaming: true}}
	state.update(active, nil, 0)
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 100 {
			state.setRootStatus(StatusWaiting)
			state.setRootStatus(StatusError)
			state.setRootStatus(StatusPaused)
		}
	})
	wg.Go(func() {
		for range 100 {
			state.update(active, nil, 0)
		}
	})
	wg.Wait()
	assertCardStatus(t, store, StatusRunning)
}

func TestControllerProbesTabsWhileOriginalSnapshotUnavailable(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard()))
	client := &fakeClient{
		snapErr:  errors.New("original tab closed"),
		activity: []agent.SessionActivity{{ID: "other-tab", Streaming: true}},
	}
	sessions := newFakeSessionManager()
	c := newTestController(t, store, sessions, client)
	c.Start(devCard())
	defer c.Stop("c1")
	require.Eventually(t, func() bool {
		card, err := store.GetCard("c1")
		return err == nil && card.Status == StatusRunning
	}, time.Second, time.Millisecond)
	assert.Empty(t, sessions.calls())
}

func TestControllerOriginalTabExitDoesNotKillOtherTabs(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard()))
	client := &fakeClient{
		events:   []agent.Event{{Type: agent.EventSessionExited}},
		activity: []agent.SessionActivity{{ID: "other-tab", Streaming: true}},
	}
	sessions := newFakeSessionManager()
	c := newTestController(t, store, sessions, client)
	c.Start(devCard())
	defer c.Stop("c1")
	require.Eventually(t, func() bool {
		card, err := store.GetCard("c1")
		return err == nil && card.Status == StatusRunning
	}, time.Second, time.Millisecond)
	assert.Never(t, func() bool { return len(sessions.calls()) > 0 }, 600*time.Millisecond, 10*time.Millisecond)
}

func TestControllerUnsupportedActivityIsVisibleWithoutKillingAgent(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard()))
	client := &fakeClient{activityErr: agent.ErrActivityUnsupported}
	sessions := newFakeSessionManager()
	c := newTestController(t, store, sessions, client)
	c.Start(devCard())
	defer c.Stop("c1")
	require.Eventually(t, func() bool {
		card, err := store.GetCard("c1")
		return err == nil && card.Status == StatusUnknown
	}, time.Second, time.Millisecond)
	assert.Contains(t, c.activityWarning("c1"), "upgrade docker-agent")
	assert.Empty(t, sessions.calls())
}

type cancelingActivityClient struct {
	fakeClient

	entered chan struct{}
	stopped chan struct{}
}

func (c *cancelingActivityClient) Activity(ctx context.Context) ([]agent.SessionActivity, error) {
	close(c.entered)
	<-ctx.Done()
	close(c.stopped)
	return nil, ctx.Err()
}

func TestControllerStopJoinsActivityProbe(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.InsertCard(devCard()))
	client := &cancelingActivityClient{entered: make(chan struct{}), stopped: make(chan struct{})}
	c := newTestController(t, store, newFakeSessionManager(), client)
	c.Start(devCard())
	select {
	case <-client.entered:
	case <-time.After(time.Second):
		t.Fatal("activity probe did not start")
	}
	c.Stop("c1")
	select {
	case <-client.stopped:
	default:
		t.Fatal("Stop returned before the activity probe stopped")
	}
	assert.Empty(t, c.activityWarning("c1"))
}

func TestActivityRelaunchDiscardsOldProcessState(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(strconv.FormatBool(streaming), func(t *testing.T) {
			state, store := newTestActivity(t)
			state.update([]agent.SessionActivity{{ID: "sess-1", Streaming: streaming}}, nil, 0)
			state.relaunched()
			assertCardStatus(t, store, StatusStarting)
			state.update(nil, errors.New("socket not ready"), 1)
			assertCardStatus(t, store, StatusStarting)
			assert.Empty(t, state.controller.activityWarning("c1"))
			state.update([]agent.SessionActivity{}, nil, 0)
			assertCardStatus(t, store, StatusStarting)
			state.update([]agent.SessionActivity{{ID: "sess-1", Streaming: true}}, nil, 1)
			assertCardStatus(t, store, StatusRunning)
		})
	}
}

func TestRelaunchResetsWatcherActivity(t *testing.T) {
	state, store := newTestActivity(t)
	state.controller.watchers["c1"] = &watcher{activity: state}
	state.update([]agent.SessionActivity{{ID: "sess-1", Streaming: true}}, nil, 0)
	require.NoError(t, state.controller.relaunch(devCard(), ""))
	state.update(nil, errors.New("starting"), 1)
	assertCardStatus(t, store, StatusStarting)
}
