package tmux

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/creack/pty"
	"github.com/stretchr/testify/require"
)

func TestApplicationEscape(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	// Keep the private socket below Darwin's Unix socket path limit.
	t.Setenv("TMPDIR", "/tmp")
	t.Setenv("TMPDIR", t.TempDir())
	require.NoError(t, tmuxRun("-f", os.DevNull, "new-session", "-d", "-s", "keys", "sleep 300"))
	t.Cleanup(func() { _ = tmuxRun("kill-server") })
	applyServerDefaults()
	require.NoError(t, tmuxRun("set", "-g", "extended-keys-format", "csi-u"))

	for _, tt := range []struct {
		name, input, want string
	}{
		{"single", "\x1bO[", "\x1b"},
		{"repeated", "\x1bO[\x1bO[", "\x1b\x1b"},
		{"before text", "\x1bO[x", "\x1bx"},
		{"before arrow", "\x1bO[\x1b[A", "\x1b\x1b[A"},
		{"before ctrl digit", "\x1bO[\x1b[49;5u", "\x1b\x1b[49;5u"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			received := filepath.Join(t.TempDir(), "received")
			ready := filepath.Join(t.TempDir(), "ready")
			// Request extended key mode 2, as a TUI can. A pair of bare
			// Escapes in one read would become Alt-Escape in this mode.
			command := fmt.Sprintf("stty raw -echo; printf '\\033[>4;2m'; touch %s; dd bs=1 count=%d of=%s 2>/dev/null; sleep 300",
				shellescape.Quote(ready), len(tt.want), shellescape.Quote(received))
			require.NoError(t, tmuxRun("respawn-pane", "-k", "-t", "keys", command))
			require.Eventually(t, func() bool {
				_, err := os.Stat(ready)
				return err == nil
			}, 5*time.Second, 10*time.Millisecond)

			cmd := exec.CommandContext(t.Context(), "tmux", "-S", SocketPath(), "attach", "-t", "keys")
			cmd.Env = append(cmd.Environ(), "TERM=xterm-256color")
			terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 80, Rows: 24})
			require.NoError(t, err)
			outputDone := make(chan struct{})
			go func() {
				_, _ = io.Copy(io.Discard, terminal)
				close(outputDone)
			}()
			t.Cleanup(func() {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				_ = terminal.Close()
				<-outputDone
			})

			// A single write models adjacent WebSocket messages coalescing
			// in the PTY, independently of typing speed or escape-time.
			_, err = terminal.WriteString(tt.input)
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				data, err := os.ReadFile(received)
				return err == nil && len(data) >= len(tt.want)
			}, 5*time.Second, 10*time.Millisecond)
			data, err := os.ReadFile(received)
			require.NoError(t, err)
			require.Equal(t, tt.want, string(data))
		})
	}
}
