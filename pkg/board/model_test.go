package board

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewID(t *testing.T) {
	id1 := newID()
	id2 := newID()

	assert.Len(t, id1, 16)
	assert.Len(t, id2, 16)
	assert.NotEqual(t, id1, id2)
}

func TestSanitizeBranch(t *testing.T) {
	tests := []struct {
		name  string
		title string
		check func(t *testing.T, branch string)
	}{
		{
			name:  "simple title",
			title: "Fix the bug",
			check: func(t *testing.T, branch string) {
				t.Helper()
				assert.True(t, strings.HasPrefix(branch, "board/fix-the-bug-"))
			},
		},
		{
			name:  "special characters",
			title: "Add feature: foo/bar!",
			check: func(t *testing.T, branch string) {
				t.Helper()
				assert.True(t, strings.HasPrefix(branch, "board/add-feature-foo-bar-"))
				assert.NotContains(t, branch, ":")
				assert.NotContains(t, branch, "/bar!")
			},
		},
		{
			name:  "uppercase normalized",
			title: "UPPERCASE TITLE",
			check: func(t *testing.T, branch string) {
				t.Helper()
				assert.True(t, strings.HasPrefix(branch, "board/uppercase-title-"))
			},
		},
		{
			name:  "long title is truncated",
			title: strings.Repeat("abcdefghij", 10),
			check: func(t *testing.T, branch string) {
				t.Helper()
				// "board/" prefix + truncated slug + "-" + 8-char ID
				assert.LessOrEqual(t, len(branch), 6+40+1+8, "branch too long: %s", branch)
			},
		},
		{
			name:  "consecutive dashes collapsed",
			title: "hello---world",
			check: func(t *testing.T, branch string) {
				t.Helper()
				assert.True(t, strings.HasPrefix(branch, "board/hello-world-"))
				assert.NotContains(t, branch, "--")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			branch := sanitizeBranch(tt.title)
			tt.check(t, branch)
		})
	}
}

func TestSanitizeBranchUniqueness(t *testing.T) {
	b1 := sanitizeBranch("same title")
	b2 := sanitizeBranch("same title")

	assert.NotEqual(t, b1, b2)
}

func TestNextColumn(t *testing.T) {
	cols := []Column{
		{ID: "dev"}, {ID: "review"}, {ID: "done"},
	}

	assert.Equal(t, "review", nextColumn(cols, "dev"))
	assert.Equal(t, "done", nextColumn(cols, "review"))
	assert.Empty(t, nextColumn(cols, "done"))
	assert.Empty(t, nextColumn(cols, "nonexistent"))
}

func TestNextColumnEmpty(t *testing.T) {
	assert.Empty(t, nextColumn(nil, "dev"))
}

func TestColumnPrompt(t *testing.T) {
	cols := []Column{
		{ID: "dev", Prompt: ""},
		{ID: "review", Prompt: "Review changes"},
	}

	assert.Empty(t, columnPrompt(cols, "dev"))
	assert.Equal(t, "Review changes", columnPrompt(cols, "review"))
	assert.Empty(t, columnPrompt(cols, "nonexistent"))
}

func TestColumnIndex(t *testing.T) {
	cols := []Column{
		{ID: "dev"}, {ID: "review"}, {ID: "done"},
	}

	assert.Equal(t, 0, columnIndex(cols, "dev"))
	assert.Equal(t, 1, columnIndex(cols, "review"))
	assert.Equal(t, 2, columnIndex(cols, "done"))
	assert.Equal(t, -1, columnIndex(cols, "nonexistent"))
	assert.Equal(t, -1, columnIndex(nil, "dev"))
}
