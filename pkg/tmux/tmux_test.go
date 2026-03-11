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
