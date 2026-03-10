package tmux

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestShellQuote(t *testing.T) {
	assert.Equal(t, `'hello world'`, shellQuote("hello world"))
	assert.Equal(t, `'$HOME'`, shellQuote("$HOME"))
	assert.Equal(t, "'`whoami`'", shellQuote("`whoami`"))
	assert.Equal(t, `'it'"'"'s'`, shellQuote("it's"))
	assert.Equal(t, `'a $var and `+"`cmd`"+`'`, shellQuote("a $var and `cmd`"))
}
