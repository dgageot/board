package board

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dialBridge serves a single WebSocket that bridges cmd and returns the
// client side of the connection plus a channel closed when the handler
// returns.
func dialBridge(t *testing.T, cmd *exec.Cmd) (*websocket.Conn, <-chan struct{}) {
	t.Helper()

	handlerDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = bridgeTerminal(conn, cmd, &pty.Winsize{Cols: 80, Rows: 24})
	}))
	t.Cleanup(srv.Close)

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, resp, err := websocket.DefaultDialer.DialContext(t.Context(), url, nil)
	require.NoError(t, err)
	_ = resp.Body.Close()
	t.Cleanup(func() { _ = conn.Close() })

	return conn, handlerDone
}

// TestBridgeTerminalClosesWhenCommandExits reproduces a client that never
// sends anything: once the command exits, the server must still deliver a
// normal Close frame and the handler must return without waiting for the
// client to hang up first.
func TestBridgeTerminalClosesWhenCommandExits(t *testing.T) {
	conn, handlerDone := dialBridge(t, exec.CommandContext(t.Context(), "sh", "-c", "echo hello"))

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))

	var output strings.Builder
	for {
		_, data, err := conn.ReadMessage()
		if err == nil {
			output.Write(data)
			continue
		}
		var ce *websocket.CloseError
		require.ErrorAs(t, err, &ce, "expected a Close frame, got %v (output so far %q)", err, output.String())
		assert.Equal(t, websocket.CloseNormalClosure, ce.Code)
		assert.Equal(t, "session ended", ce.Text)
		break
	}
	assert.Contains(t, output.String(), "hello")

	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not return after the command exited")
	}
}

// TestBridgeTerminalStopsWhenClientDisconnects covers the other direction:
// a long-running command must be torn down when the client goes away.
func TestBridgeTerminalStopsWhenClientDisconnects(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "sleep", "30")
	conn, handlerDone := dialBridge(t, cmd)

	require.NoError(t, conn.Close())

	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not return after the client disconnected")
	}
	require.NotNil(t, cmd.ProcessState, "command should have been reaped")
}

// startTmuxSession starts a detached session on a private tmux server and
// returns the attach command for it. The test is skipped when tmux is absent.
func startTmuxSession(t *testing.T) *exec.Cmd {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	// Unix socket paths are length-limited (104 bytes on Darwin), so avoid the
	// long t.TempDir() path.
	t.Setenv("TMPDIR", "/tmp")
	sock := filepath.Join(t.TempDir(), "tmux.sock")
	require.NoError(t, exec.Command("tmux", "-S", sock, "-f", os.DevNull, "new-session", "-d", "-s", "s1", "sleep 300").Run())
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", sock, "kill-server").Run() })

	cmd := exec.CommandContext(t.Context(), "tmux", "-S", sock, "-2", "attach", "-t", "s1")
	cmd.Env = append(cmd.Environ(), "TERM=xterm-256color")
	return cmd
}

// TestBridgeTerminalTmuxSessionKilled is the production scenario behind
// TestBridgeTerminalClosesWhenCommandExits: the agent's tmux session goes
// away while the browser terminal is attached and idle.
func TestBridgeTerminalTmuxSessionKilled(t *testing.T) {
	cmd := startTmuxSession(t)
	conn, handlerDone := dialBridge(t, cmd)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, _, err := conn.ReadMessage()
	require.NoError(t, err)

	require.NoError(t, exec.Command("tmux", "-S", cmd.Args[2], "kill-session", "-t", "s1").Run())

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	for {
		_, _, err := conn.ReadMessage()
		if err == nil {
			continue
		}
		var ce *websocket.CloseError
		require.ErrorAs(t, err, &ce, "expected a Close frame, got %v", err)
		assert.Equal(t, websocket.CloseNormalClosure, ce.Code)
		break
	}

	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not return after the tmux session was killed")
	}
}

// TestBridgeTerminalTmuxClientDisconnect checks the attach client is torn
// down promptly when the browser goes away, so a stale client does not keep
// driving the pane size (window-size latest).
func TestBridgeTerminalTmuxClientDisconnect(t *testing.T) {
	cmd := startTmuxSession(t)
	conn, handlerDone := dialBridge(t, cmd)
	// Wait for an idle PTY read: closing a busy PTY can mask the Darwin hang.
	for {
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(100*time.Millisecond)))
		if _, _, err := conn.ReadMessage(); err != nil {
			var ne net.Error
			require.ErrorAs(t, err, &ne)
			require.True(t, ne.Timeout())
			break
		}
	}

	require.NoError(t, conn.Close())

	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after the client disconnected")
	}
	// Only the attach client is hung up; the agent's session must survive.
	require.NoError(t, exec.Command("tmux", "-S", cmd.Args[2], "has-session", "-t", "s1").Run())
}

func TestBridgeTerminalRelaysAllOutputBeforeClose(t *testing.T) {
	conn, _ := dialBridge(t, exec.CommandContext(t.Context(), "sh", "-c", "stty -opost; seq 1 20000"))
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
	var output strings.Builder
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			var ce *websocket.CloseError
			require.ErrorAs(t, err, &ce)
			require.Equal(t, websocket.CloseNormalClosure, ce.Code)
			break
		}
		output.Write(data)
	}
	var want strings.Builder
	for i := 1; i <= 20000; i++ {
		want.WriteString(strconv.Itoa(i) + "\n")
	}
	require.Equal(t, want.Len(), output.Len())
	require.Equal(t, want.String(), output.String())
}
