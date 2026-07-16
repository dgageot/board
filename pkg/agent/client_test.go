package agent

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testClient wires a Client to a test HTTP server.
func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return newClient(srv.Client(), srv.URL, "sess-1")
}

func TestSnapshot(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/sessions/sess-1/snapshot", r.URL.Path)
		_, _ = io.WriteString(w, `{"title":"Hello","streaming":true,"last_event_seq":42,"messages":[{"message":{"cost":0.25}},{"message":{"role":"user"}},{"message":{"cost":0.75}}]}`)
	})

	snap, err := c.Snapshot(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "Hello", snap.Title)
	assert.True(t, snap.Streaming)
	assert.Equal(t, uint64(42), snap.LastEventSeq)
	// Without an aggregate cost field (older agent), Cost is summed from the
	// per-message costs, ignoring messages with none.
	assert.InDelta(t, 1.0, snap.Cost, 1e-9)
}

// A current agent reports the session's total cost directly; it wins over the
// per-message sum because it also covers sub-session and item-level costs.
func TestSnapshotPrefersAggregateCost(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"title":"Hello","cost":2.5,"messages":[{"message":{"cost":0.25}}]}`)
	})

	snap, err := c.Snapshot(t.Context())
	require.NoError(t, err)
	assert.InDelta(t, 2.5, snap.Cost, 1e-9)
}

// A 404 on /snapshot means the route is missing (docker-agent < v1.80.0): it
// must be flagged as ErrUnsupported so the watcher relaunches the session
// instead of retrying forever.
func TestSnapshotNotFoundIsUnsupported(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	_, err := c.Snapshot(t.Context())
	require.ErrorIs(t, err, ErrUnsupported)
}

// A 404 whose body carries the "unknown_session" code comes from a current
// agent that simply has not created the session yet: it must NOT be flagged
// as ErrUnsupported, or the watcher would needlessly kill and relaunch an
// agent that is still starting up.
func TestSnapshotUnknownSessionIsNotUnsupported(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"code":"unknown_session","message":"session not found: nope"}`)
	})

	_, err := c.Snapshot(t.Context())
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrUnsupported)
}

func TestFollowupSendsMessageAndIdempotencyKey(t *testing.T) {
	var gotKey, gotBody string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/sessions/sess-1/followup", r.URL.Path)
		gotKey = r.Header.Get("Idempotency-Key")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"status":"queued_streaming","duplicate":false}`)
	})

	dup, err := c.Followup(t.Context(), "key-1", "do this")
	require.NoError(t, err)
	assert.False(t, dup)
	assert.Equal(t, "key-1", gotKey)
	assert.JSONEq(t, `{"messages":[{"content":"do this"}]}`, gotBody)
}

func TestFollowupReportsDuplicate(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"status":"duplicate","duplicate":true}`)
	})

	dup, err := c.Followup(t.Context(), "key-1", "do this")
	require.NoError(t, err)
	assert.True(t, dup)
}

// An accepted follow-up with no body is still a success: the prompt was
// delivered, the server just had nothing more to say.
func TestFollowupToleratesEmptyBody(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})

	dup, err := c.Followup(t.Context(), "key-1", "do this")
	require.NoError(t, err)
	assert.False(t, dup)
}

func TestStreamEventsParsesDataLinesAndStops(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "10", r.URL.Query().Get("since"))
		fmt.Fprint(w,
			"id: 11\ndata: {\"type\":\"stream_started\",\"session_id\":\"sess-1\"}\n\n"+
				"id: 12\ndata: {\"type\":\"session_title\",\"title\":\"My Task\"}\n\n"+
				"id: 13\ndata: {\"type\":\"stream_stopped\"}\n\n")
	})

	var got []Event
	err := c.StreamEvents(t.Context(), 10, func(ev Event) bool {
		got = append(got, ev)
		return ev.Type != EventSessionTitle // stop right after the title
	})
	require.NoError(t, err)

	require.Len(t, got, 2)
	assert.Equal(t, EventStreamStarted, got[0].Type)
	assert.Equal(t, "sess-1", got[0].SessionID, "the event's session_id is decoded")
	assert.Equal(t, uint64(11), got[0].Seq, "the SSE id line is the event's seq")
	assert.Equal(t, EventSessionTitle, got[1].Type)
	assert.Equal(t, "My Task", got[1].Title)
	assert.Equal(t, uint64(12), got[1].Seq)
}

func TestStreamEventsWithoutIDsHasZeroSeq(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "data: {\"type\":\"stream_started\"}\n\n")
	})

	var got []Event
	err := c.StreamEvents(t.Context(), 0, func(ev Event) bool {
		got = append(got, ev)
		return true
	})
	require.NoError(t, err)

	require.Len(t, got, 1)
	assert.Equal(t, uint64(0), got[0].Seq)
}

func TestStreamEventsErrorsOnBadStatus(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	err := c.StreamEvents(t.Context(), 0, func(Event) bool { return true })
	require.Error(t, err)
}

// Heartbeat comments are invisible to the event callback, and once one has
// been seen, a stream that goes silent is aborted with an error (instead of
// blocking forever on a hung transport) so the watcher reconnects.
func TestStreamEventsIdleWatchdogAbortsSilentStream(t *testing.T) {
	old := streamIdleTimeout
	streamIdleTimeout = 50 * time.Millisecond
	t.Cleanup(func() { streamIdleTimeout = old })

	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			t.Error("response writer is not a flusher")
			return
		}
		fmt.Fprint(w, ": ping\n\n")
		fmt.Fprint(w, "data: {\"type\":\"stream_started\"}\n\n")
		f.Flush()
		// Then hang without closing, like a wedged transport.
		<-r.Context().Done()
	})

	var got []Event
	err := c.StreamEvents(t.Context(), 0, func(ev Event) bool {
		got = append(got, ev)
		return true
	})
	require.ErrorIs(t, err, errStreamIdle)
	require.Len(t, got, 1, "heartbeat comments must not reach the callback")
	assert.Equal(t, EventStreamStarted, got[0].Type)
}

// Without any heartbeat from the server (an older docker-agent), the watchdog
// stays unarmed: a quiet stream is left alone and a clean close still ends
// the stream without an idle error.
func TestStreamEventsNoHeartbeatNoWatchdog(t *testing.T) {
	old := streamIdleTimeout
	streamIdleTimeout = 50 * time.Millisecond
	t.Cleanup(func() { streamIdleTimeout = old })

	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			t.Error("response writer is not a flusher")
			return
		}
		fmt.Fprint(w, "data: {\"type\":\"stream_started\"}\n\n")
		f.Flush()
		time.Sleep(120 * time.Millisecond) // longer than the idle timeout
		fmt.Fprint(w, "data: {\"type\":\"stream_stopped\"}\n\n")
	})

	var got []Event
	err := c.StreamEvents(t.Context(), 0, func(ev Event) bool {
		got = append(got, ev)
		return true
	})
	require.NoError(t, err)
	require.Len(t, got, 2)
}
