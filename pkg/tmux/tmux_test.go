package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"al.essio.dev/pkg/shellescape"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// With no tmux server (e.g. after a reboot), Alive must report not alive
// rather than an error, so the controller relaunches the agent instead of
// waiting forever for a server that will never answer.
func TestAliveNoServer(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir()) // point SocketPath at a socket-less dir

	alive, err := Sessions{}.Alive("any")

	require.NoError(t, err)
	assert.False(t, alive)
}

// A stale socket file left behind by a killed server must also read as no
// server: nothing is listening on it.
func TestAliveStaleSocket(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	require.NoError(t, os.WriteFile(SocketPath(), nil, 0o600))

	alive, err := Sessions{}.Alive("any")

	require.NoError(t, err)
	assert.False(t, alive)
}

func TestShellQuote(t *testing.T) {
	assert.Equal(t, `'hello world'`, shellescape.Quote("hello world"))
	assert.Equal(t, `'$HOME'`, shellescape.Quote("$HOME"))
	assert.Equal(t, "'`whoami`'", shellescape.Quote("`whoami`"))
	assert.Equal(t, `'it'"'"'s'`, shellescape.Quote("it's"))
	assert.Equal(t, `'a $var and `+"`cmd`"+`'`, shellescape.Quote("a $var and `cmd`"))
}

func TestAgentCommand(t *testing.T) {
	const sock = "/run/board-abc.sock"

	// Resume (no worktree name): omit --worktree so the session reattaches to
	// its existing worktree; no prompt means wait for input.
	assert.Equal(t, "docker agent run my-agent --yolo --session abc123 --listen unix:///run/board-abc.sock",
		agentCommand("my-agent", "abc123", sock, "", "", ""))

	// Resume with a prompt: deliver it via stdin from the prompt file. The
	// prompt text itself never appears on the command line.
	assert.Equal(t, "docker agent run my-agent --yolo --session abc123 --listen unix:///run/board-abc.sock - < /tmp/board-prompt-abc123",
		agentCommand("my-agent", "abc123", sock, "", "", "/tmp/board-prompt-abc123"))

	// First launch (worktree name set): create the isolated worktree branched
	// from the given base and deliver the first prompt via stdin.
	assert.Equal(t, "docker agent run my-agent --yolo --session abc123 --listen unix:///run/board-abc.sock --worktree=board-xyz --worktree-base origin/main - < /tmp/board-prompt-abc123",
		agentCommand("my-agent", "abc123", sock, "board-xyz", "origin/main", "/tmp/board-prompt-abc123"))

	// The worktree base is not assumed: a non-origin upstream flows through.
	assert.Equal(t, "docker agent run my-agent --yolo --session abc123 --listen unix:///run/board-abc.sock --worktree=board-xyz --worktree-base upstream/master - < /tmp/board-prompt-abc123",
		agentCommand("my-agent", "abc123", sock, "board-xyz", "upstream/master", "/tmp/board-prompt-abc123"))
}

// The prompt is staged in a file read verbatim by the agent: quotes,
// backticks, dollars and newlines survive untouched because the shell that
// launches the agent never sees the prompt text.
func TestPromptFilePath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)

	assert.Equal(t, filepath.Join(os.TempDir(), "board-prompt-abc123"), promptFilePath("abc123"))
}

func TestAgentCommandPreservesAgentArgument(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, agent := range []string{
		"/tmp/My Agent.yaml", "/tmp/a'quoted.yaml", "/tmp/$(printf INJECTED).yaml",
		"/tmp/$HOME.yaml", "/tmp/agent; printf INJECTED", "docker.io/org/agent:latest",
		"~/My Agent.yaml", "$HOME/My Agent.yaml", "${HOME}/My Agent.yaml",
	} {
		t.Run(agent, func(t *testing.T) {
			want := agent
			for _, prefix := range []string{"~/", "$HOME/", "${HOME}/"} {
				if rest, ok := strings.CutPrefix(agent, prefix); ok {
					want = home + "/" + rest
				}
			}
			// Capture argv without starting Docker. Metacharacters must stay literal.
			script := `docker() { printf '%s\n' "$@"; }; ` + agentCommand(agent, "session", "/tmp/sock", "", "", "")
			out, err := exec.Command("sh", "-c", script).CombinedOutput()
			require.NoError(t, err, "%s", out)
			require.Equal(t, []string{"agent", "run", want, "--yolo", "--session", "session", "--listen", "unix:///tmp/sock"}, strings.Split(strings.TrimSuffix(string(out), "\n"), "\n"))
		})
	}
}
