package tmux

import (
	"os"
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

	// Resume with a prompt: deliver it as the session's next message, quoted.
	assert.Equal(t, "docker agent run my-agent --yolo --session abc123 --listen unix:///run/board-abc.sock 'do this'",
		agentCommand("my-agent", "abc123", sock, "", "", "do this"))

	// First launch (worktree name set): create the isolated worktree branched
	// from the given base and deliver the first prompt.
	assert.Equal(t, "docker agent run my-agent --yolo --session abc123 --listen unix:///run/board-abc.sock --worktree=board-xyz --worktree-base origin/main 'do this'",
		agentCommand("my-agent", "abc123", sock, "board-xyz", "origin/main", "do this"))

	// The worktree base is not assumed: a non-origin upstream flows through.
	assert.Equal(t, "docker agent run my-agent --yolo --session abc123 --listen unix:///run/board-abc.sock --worktree=board-xyz --worktree-base upstream/master 'do this'",
		agentCommand("my-agent", "abc123", sock, "board-xyz", "upstream/master", "do this"))
}
