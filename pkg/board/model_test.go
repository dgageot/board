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

func TestNewBranchName(t *testing.T) {
	branch := newBranchName()

	assert.True(t, strings.HasPrefix(branch, "board/"))
	assert.Greater(t, len(branch), len("board/"))
}

func TestNewBranchNameUniqueness(t *testing.T) {
	b1 := newBranchName()
	b2 := newBranchName()

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
