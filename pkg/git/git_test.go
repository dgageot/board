package git

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorktreeDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	got := WorktreeDir("board-abc12345")
	assert.Equal(t, filepath.Join(home, ".cagent", "worktrees", "board-abc12345"), got)
}

func TestWorktreeBranch(t *testing.T) {
	assert.Equal(t, "worktree-board-abc12345", WorktreeBranch("board-abc12345"))
}

func TestDiffMissingWorktree(t *testing.T) {
	diff, err := Diff(filepath.Join(t.TempDir(), "does-not-exist"))
	require.NoError(t, err)
	assert.Empty(t, diff)
}
