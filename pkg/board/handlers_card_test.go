package board

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPlaceholderTitle(t *testing.T) {
	// Short prompts are used as-is.
	assert.Equal(t, "Fix the login bug", placeholderTitle("  Fix the login bug  "))

	// Only the first line is kept.
	assert.Equal(t, "Add tests", placeholderTitle("Add tests\nand make them pass"))

	// Long prompts are cut on a word boundary with an ellipsis.
	long := "Refactor the authentication module to support single sign-on across all services"
	got := placeholderTitle(long)
	assert.True(t, strings.HasSuffix(got, "…"), "long title is truncated")
	assert.LessOrEqual(t, len([]rune(got)), 41, "stays within the cap (40 + ellipsis)")
	assert.False(t, strings.HasSuffix(strings.TrimSuffix(got, "…"), " "), "no trailing space before ellipsis")
	assert.True(t, strings.HasPrefix(long, strings.TrimSuffix(got, "…")), "prefix of the prompt")
}

func TestPlaceholderTitleNoMidWordCut(t *testing.T) {
	// A single very long word (no space to break on) is still bounded.
	got := placeholderTitle(strings.Repeat("x", 100))
	assert.True(t, strings.HasSuffix(got, "…"))
	assert.Len(t, []rune(got), 41)
}
