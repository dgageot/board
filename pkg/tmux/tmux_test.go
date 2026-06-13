package tmux

import (
	"testing"

	"al.essio.dev/pkg/shellescape"
	"github.com/stretchr/testify/assert"
)

func TestShellQuote(t *testing.T) {
	assert.Equal(t, `'hello world'`, shellescape.Quote("hello world"))
	assert.Equal(t, `'$HOME'`, shellescape.Quote("$HOME"))
	assert.Equal(t, "'`whoami`'", shellescape.Quote("`whoami`"))
	assert.Equal(t, `'it'"'"'s'`, shellescape.Quote("it's"))
	assert.Equal(t, `'a $var and `+"`cmd`"+`'`, shellescape.Quote("a $var and `cmd`"))
}

func TestAgentCommand(t *testing.T) {
	// No prompt: resume the session and wait for input.
	assert.Equal(t, "docker agent run my-agent --yolo --session abc123",
		agentCommand("my-agent", "abc123", ""))

	// With a prompt: deliver it as the session's first/next message, quoted.
	assert.Equal(t, "docker agent run my-agent --yolo --session abc123 'do this'",
		agentCommand("my-agent", "abc123", "do this"))
}
