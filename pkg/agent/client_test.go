package agent

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

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
		_, _ = io.WriteString(w, `{"title":"Hello","streaming":true,"last_event_seq":42}`)
	})

	snap, err := c.Snapshot(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "Hello", snap.Title)
	assert.True(t, snap.Streaming)
	assert.Equal(t, uint64(42), snap.LastEventSeq)
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

func TestStreamEventsParsesDataLinesAndStops(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "10", r.URL.Query().Get("since"))
		fmt.Fprint(w,
			"id: 11\ndata: {\"type\":\"stream_started\"}\n\n"+
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
