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
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Event types the board reacts to on the session event stream. Every other
// runtime event is ignored.
const (
	EventStreamStarted = "stream_started"
	EventStreamStopped = "stream_stopped"
	EventSessionTitle  = "session_title"
	EventSessionExited = "session_exited"
	EventGap           = "gap"
)

// Event is the subset of a runtime event the board cares about.
type Event struct {
	Type  string `json:"type"`
	Title string `json:"title"`
}

// Snapshot is the part of GET /snapshot the board uses to (re)build a card's
// state and find the stream position to resume from.
type Snapshot struct {
	Title        string `json:"title"`
	Streaming    bool   `json:"streaming"`
	LastEventSeq uint64 `json:"last_event_seq"`
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
	if resp.StatusCode != http.StatusOK {
		return Snapshot{}, fmt.Errorf("snapshot: %s", resp.Status)
	}
	var snap Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return Snapshot{}, fmt.Errorf("decode snapshot: %w", err)
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
	if err := json.NewDecoder(resp.Body).Decode(&fr); err != nil {
		return false, fmt.Errorf("decode followup: %w", err)
	}
	return fr.Duplicate, nil
}

// StreamEvents tails the session event stream starting after `since` (0 from
// the beginning of the buffer). onEvent is called for every event; returning
// false stops the stream cleanly. It returns nil on a clean stop and an error
// when the connection fails.
func (c *Client) StreamEvents(ctx context.Context, since uint64, onEvent func(Event) bool) error {
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
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data:")
		if !ok {
			continue
		}
		var ev Event
		if json.Unmarshal([]byte(strings.TrimSpace(data)), &ev) != nil {
			continue
		}
		if !onEvent(ev) {
			return nil
		}
	}
	return scanner.Err()
}
