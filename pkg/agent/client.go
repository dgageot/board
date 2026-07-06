// Package agent is a thin client for a docker-agent run's control plane,
// the HTTP API a run exposes with `--listen`. The board talks to one such
// API per card over a private unix socket to observe the session (events,
// title, streaming state) and to drive it (follow-up prompts), instead of
// scraping the terminal.
package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Event types the board reacts to on the session event stream. Every other
// runtime event is ignored.
const (
	EventStreamStarted = "stream_started"
	EventStreamStopped = "stream_stopped"
	EventSessionTitle  = "session_title"
	EventSessionExited = "session_exited"
	// EventUserMessage marks a real user prompt entering the session. The
	// runtime emits it only for human-authored turns (sub-agent and skill
	// sub-sessions suppress it via SendUserMessage=false), right before the
	// turn's outermost stream_started, which makes it a turn-boundary marker.
	EventUserMessage = "user_message"
	// EventError is emitted when a turn fails (model error, tool failure,
	// hook block…). Unlike stream_stopped it is delivered on the blocking
	// sink and buffered for replay, so it is the reliable failure signal.
	EventError = "error"
	// EventRuntimePaused is emitted when the run loop blocks at an iteration
	// boundary because /pause was toggled on. There is no matching resume
	// event: the loop simply starts emitting events again once resumed.
	EventRuntimePaused = "runtime_paused"
	EventGap           = "gap"
)

// ReasonNormal is the stream_stopped reason for a turn that completed
// cleanly, as opposed to "error", "canceled", "hook_blocked"...
const ReasonNormal = "normal"

// streamIdleTimeout is how long StreamEvents tolerates a silent connection
// once the server has proven it sends heartbeats (": ping" SSE comments,
// emitted every 15s by docker-agent's control plane). Three missed
// heartbeats means the transport is hung — e.g. the agent's VM was paused —
// not that the session is quiet, so the stream is aborted and the watcher
// reconnects. Servers that predate heartbeats never arm the watchdog, so
// long-lived idle streams to older agents keep working.
var streamIdleTimeout = 45 * time.Second

// errStreamIdle reports a stream aborted by the idle watchdog.
var errStreamIdle = errors.New("event stream idle: heartbeats stopped")

// ErrUnsupported reports a control plane that answers but lacks GET /snapshot:
// the agent runs docker-agent < v1.80.0 (or serves an unknown session). The
// board cannot watch such a session; relaunching it picks up the binary
// currently installed.
var ErrUnsupported = errors.New("no GET /snapshot (docker-agent < v1.80.0, or unknown session)")

// errCodeUnknownSession is the machine-readable code a current docker-agent
// puts in a 404 snapshot body when the session does not exist (yet). It
// distinguishes "the server is current but the session is not there" — the
// agent is still creating it, keep waiting — from a route-less 404 produced
// by an older binary, which really is [ErrUnsupported].
const errCodeUnknownSession = "unknown_session"

// Event is the subset of a runtime event the board cares about.
type Event struct {
	Type  string `json:"type"`
	Title string `json:"title"`
	// Reason classifies how a stream ended (stream_stopped only): "normal",
	// "error", "canceled", "hook_blocked"... It is authoritative for the
	// turn's outcome, unlike mid-turn error events which a parent agent may
	// have recovered from.
	Reason string `json:"reason"`
	// Seq is the event's position in the session's buffer, parsed from the
	// SSE "id:" line. It is 0 when the server sent no id. Compared with
	// [Snapshot.LastEventSeq] it tells replayed history from live events.
	Seq uint64 `json:"-"`
}

// Snapshot is the part of GET /snapshot the board uses to (re)build a card's
// state and find the stream position to resume from.
type Snapshot struct {
	Title string `json:"title"`
	// Streaming reports whether the server holds a turn's streaming lock. It is
	// always false for attached (--listen) sessions — turns run in the TUI, not
	// through the server's RunSession — so the controller ignores it and derives
	// the running state from stream_started/stream_stopped events instead.
	Streaming    bool   `json:"streaming"`
	LastEventSeq uint64 `json:"last_event_seq"`
	// Cost is the session's cumulative cost in US dollars. Current agents
	// report an aggregate (which includes sub-session and item-level costs);
	// for older ones it is summed from the per-message costs the runtime
	// records on each assistant message (see decodeSnapshot).
	Cost float64 `json:"-"`
}

// snapshotWire mirrors the fields the board reads off GET /snapshot. Current
// agents report the session's total cost directly; older ones only record a
// cost on every assistant message, so the per-message costs are kept as a
// fallback and summed into the aggregate [Snapshot.Cost].
type snapshotWire struct {
	Title        string  `json:"title"`
	Streaming    bool    `json:"streaming"`
	LastEventSeq uint64  `json:"last_event_seq"`
	Cost         float64 `json:"cost"`
	Messages     []struct {
		Message struct {
			Cost float64 `json:"cost"`
		} `json:"message"`
	} `json:"messages"`
}

// Client drives one session's control plane.
type Client struct {
	http    *http.Client
	base    string
	session string
}

// NewClient returns a client that reaches the control plane over the given
// unix socket and targets the given session id.
func NewClient(socket, session string) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}
	return newClient(&http.Client{Transport: transport}, "http://agent", session)
}

// newClient builds a client around an explicit http.Client and base URL. Used
// by tests to target a plain TCP test server.
func newClient(httpc *http.Client, base, session string) *Client {
	return &Client{http: httpc, base: base, session: session}
}

func (c *Client) sessionURL() string {
	return c.base + "/api/sessions/" + url.PathEscape(c.session)
}

func (c *Client) endpoint(name string) string {
	return c.sessionURL() + "/" + name
}

// Snapshot reads the session's full state and the stream position it
// corresponds to.
func (c *Client) Snapshot(ctx context.Context) (Snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint("snapshot"), http.NoBody)
	if err != nil {
		return Snapshot{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		// The board always targets a session id it owns, so a 404 needs the
		// body to disambiguate: a current agent stamps "unknown_session" on it
		// (the session is not created/attached yet — keep waiting), while a
		// bare 404 means the route is missing: /snapshot needs docker-agent
		// v1.80.0+. Flag the latter so the watcher can relaunch instead of
		// retrying forever and leaving the card stuck at "starting".
		var apiErr struct {
			Code string `json:"code"`
		}
		if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&apiErr) == nil && apiErr.Code == errCodeUnknownSession {
			return Snapshot{}, fmt.Errorf("snapshot: %s: session not known yet", resp.Status)
		}
		return Snapshot{}, fmt.Errorf("snapshot: %s: %w", resp.Status, ErrUnsupported)
	}
	if resp.StatusCode != http.StatusOK {
		return Snapshot{}, fmt.Errorf("snapshot: %s", resp.Status)
	}
	var wire snapshotWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return Snapshot{}, fmt.Errorf("decode snapshot: %w", err)
	}
	snap := Snapshot{
		Title:        wire.Title,
		Streaming:    wire.Streaming,
		LastEventSeq: wire.LastEventSeq,
		Cost:         wire.Cost,
	}
	if snap.Cost == 0 {
		// Older agents have no aggregate cost field: fall back to summing the
		// per-message costs (which misses sub-session and item-level costs).
		for _, m := range wire.Messages {
			snap.Cost += m.Message.Cost
		}
	}
	return snap, nil
}

// Followup enqueues a message to run after the current turn. A non-empty
// idempotencyKey makes the call safe to retry. It reports whether the server
// treated the call as a duplicate.
func (c *Client) Followup(ctx context.Context, idempotencyKey, message string) (duplicate bool, err error) {
	body, err := json.Marshal(map[string]any{
		"messages": []map[string]string{{"content": message}},
	})
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("followup"), bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		return false, fmt.Errorf("followup: %s", resp.Status)
	}
	var fr struct {
		Duplicate bool `json:"duplicate"`
	}
	// The prompt was accepted; an empty body is not an error, just a server
	// that has nothing more to say.
	if err := json.NewDecoder(resp.Body).Decode(&fr); err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("decode followup: %w", err)
	}
	return fr.Duplicate, nil
}

// StreamEvents tails the session event stream starting after `since` (0 from
// the beginning of the buffer). onEvent is called for every event; returning
// false stops the stream cleanly. It returns nil on a clean stop and an error
// when the connection fails.
//
// Once the server has sent a heartbeat (": ping" SSE comment), a prolonged
// silence is treated as a hung transport and the stream is aborted with an
// error so the caller reconnects; servers that send no heartbeats are given
// unlimited quiet time, as before.
func (c *Client) StreamEvents(ctx context.Context, since uint64, onEvent func(Event) bool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	u := c.endpoint("events")
	if since > 0 {
		u += "?since=" + strconv.FormatUint(since, 10)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("events: %s", resp.Status)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	// The idle watchdog is armed by the first heartbeat and re-armed by any
	// subsequent line; when it fires it cancels the request, failing the read.
	var idle atomic.Bool
	var watchdog *time.Timer
	defer func() {
		if watchdog != nil {
			watchdog.Stop()
		}
	}()

	var seq uint64
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, ":") {
			// Heartbeat comment: the server sends them, so silence now means
			// a hung transport. Arm the watchdog on the first one.
			if watchdog == nil {
				watchdog = time.AfterFunc(streamIdleTimeout, func() {
					idle.Store(true)
					cancel()
				})
			}
			continue
		}
		if watchdog != nil {
			watchdog.Reset(streamIdleTimeout)
		}
		if id, ok := strings.CutPrefix(line, "id:"); ok {
			seq, _ = strconv.ParseUint(strings.TrimSpace(id), 10, 64)
			continue
		}
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		var ev Event
		if json.Unmarshal([]byte(strings.TrimSpace(data)), &ev) != nil {
			continue
		}
		ev.Seq = seq
		seq = 0
		if !onEvent(ev) {
			return nil
		}
	}
	if idle.Load() {
		return errStreamIdle
	}
	return scanner.Err()
}
