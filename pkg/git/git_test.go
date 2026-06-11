package git

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorktreePath(t *testing.T) {
	got := WorktreePath("/home/user/src/myrepo", "board/fix-bug-abc12345")
	assert.Equal(t, "/home/user/src/fix-bug-abc12345", got)
}

func TestWorktreePathStripsPrefix(t *testing.T) {
	got := WorktreePath("/repo", "board/my-branch")
	assert.Equal(t, "/my-branch", got)
}

func TestWorktreePathRelativeRepo(t *testing.T) {
	t.Chdir(t.TempDir())

	got := WorktreePath(".", "board/my-branch")

	// The worktree must be a sibling of the repo, not inside it.
	wd, err := os.Getwd()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(filepath.Dir(wd), "my-branch"), got)
}
