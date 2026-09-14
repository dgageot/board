package board

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dgageot/board/pkg/agent"
)

// Run with DOCKER_AGENT_TEST_BINARY pointing to a binary with GET /api/activity.
// No credentials or external model calls: the local model holds each tab's turn
// until the test releases it.
func TestActivityRealDockerAgentTabs(t *testing.T) {
	binary := os.Getenv("DOCKER_AGENT_TEST_BINARY")
	if binary == "" {
		t.Skip("set DOCKER_AGENT_TEST_BINARY to run the real TUI integration test")
	}
	binary, err := filepath.Abs(binary)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()

	rootRelease, tabRelease := make(chan struct{}), make(chan struct{})
	rootStarted, tabStarted := make(chan struct{}, 1), make(chan struct{}, 1)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/title/") {
			var release <-chan struct{}
			switch {
			case bytes.Contains(body, []byte("root-work")):
				rootStarted <- struct{}{}
				release = rootRelease
			case bytes.Contains(body, []byte("tab-work")):
				tabStarted <- struct{}{}
				release = tabRelease
			}
			if release != nil {
				select {
				case <-release:
				case <-r.Context().Done():
					return
				case <-ctx.Done():
					return
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"model\":\"fake-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer model.Close()

	dir := t.TempDir()
	config := filepath.Join(dir, "agent.yaml")
	require.NoError(t, os.WriteFile(config, []byte(fmt.Sprintf(`models:
  fake:
    provider: openai
    model: fake-model
    base_url: %s/v1
    token_key: FAKE_API_KEY
    title_model: title
    provider_opts:
      api_type: openai_chatcompletions
  title:
    provider: openai
    model: fake-title
    base_url: %s/title/v1
    token_key: FAKE_API_KEY
    provider_opts:
      api_type: openai_chatcompletions
agents:
  root:
    model: fake
    instruction: Reply briefly.
`, model.URL, model.URL)), 0o600))
	socket := filepath.Join(os.TempDir(), "board-test-"+newID()+".sock")
	t.Cleanup(func() { _ = os.Remove(socket) })
	const rootID = "board-activity-integration"
	cmd := exec.CommandContext(ctx, binary, "run", "--listen", "unix://"+socket,
		"--data-dir", filepath.Join(dir, "data"), "--config-dir", filepath.Join(dir, "config"),
		"--working-dir", dir, "--session", rootID, config)
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "DOCKER_CLI_PLUGIN_") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "FAKE_API_KEY=test", "DOCKER_AGENT_NO_TOUR=1", "DOCKER_AGENT_HIDE_TELEMETRY_BANNER=1", "TELEMETRY_ENABLED=false", "TERM=xterm-256color")
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	require.NoError(t, err)
	var output lockedBuffer
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		_, _ = io.Copy(&output, terminal)
	}()
	defer func() {
		cancel()
		_ = cmd.Wait()
		_ = terminal.Close()
		<-drained
		if t.Failed() {
			t.Log(output.String())
		}
	}()

	client := agent.NewClient(socket, rootID)
	waitActivity := func(count, streaming int) []agent.SessionActivity {
		t.Helper()
		var sessions []agent.SessionActivity
		require.Eventually(t, func() bool {
			probeCtx, probeCancel := context.WithTimeout(ctx, time.Second)
			defer probeCancel()
			sessions, err = client.Activity(probeCtx)
			if err != nil || len(sessions) != count {
				return false
			}
			n := 0
			for _, s := range sessions {
				if s.Streaming {
					n++
				}
			}
			return n == streaming
		}, 12*time.Second, 50*time.Millisecond)
		return sessions
	}
	waitActivity(1, 0)
	require.Eventually(t, func() bool { return strings.Contains(output.String(), "Ctrl+t") }, 12*time.Second, 50*time.Millisecond)
	_, err = terminal.Write([]byte{0x14}) // Ctrl+t: a real independent TUI tab.
	require.NoError(t, err)
	sessions := waitActivity(2, 0)
	var tabID string
	for _, s := range sessions {
		if s.ID != rootID {
			tabID = s.ID
		}
	}
	require.NotEmpty(t, tabID)

	store := openTestStore(t)
	card := devCard()
	card.AgentSession = rootID
	card.Worktree = dir
	require.NoError(t, store.InsertCard(card))
	controller := newTestController(t, store, newFakeSessionManager(), client)
	board := &Board{store: store, controller: controller, clients: make(map[chan struct{}]struct{})}
	refresh := make(chan struct{}, 16)
	board.addClient(refresh)
	controller.onChanged = board.broadcast
	controller.Start(card)
	defer controller.Stop(card.ID)
	waitStatus := func(want CardStatus) {
		t.Helper()
		require.Eventually(t, func() bool {
			rec := httptest.NewRecorder()
			board.handleListCards(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/cards", http.NoBody))
			var cards []cardResponse
			return json.Unmarshal(rec.Body.Bytes(), &cards) == nil && len(cards) == 1 && cards[0].Status == want
		}, 3*time.Second, 10*time.Millisecond)
	}
	waitStatus(StatusWaiting)
	_, err = client.Followup(ctx, "root", "root-work")
	require.NoError(t, err)
	_, err = agent.NewClient(socket, tabID).Followup(ctx, "tab", "tab-work")
	require.NoError(t, err)
	waitActivity(2, 2)
	waitStatus(StatusRunning)

	close(rootRelease)
	waitActivity(2, 1)
	assert.Never(t, func() bool {
		got, getErr := store.GetCard(card.ID)
		return getErr != nil || got.Status != StatusRunning
	}, 1200*time.Millisecond, 10*time.Millisecond, "root finishing must not turn a working sibling green")

	close(tabRelease)
	waitActivity(2, 0)
	waitStatus(StatusWaiting)
	assert.NotEmpty(t, refresh, "status changes must wake the browser SSE clients")
	assert.Empty(t, controller.activityWarning(card.ID))
	assert.Len(t, rootStarted, 1)
	assert.Len(t, tabStarted, 1)
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
