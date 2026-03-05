package git

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWorktreePath(t *testing.T) {
	got := WorktreePath("/home/user/src/myrepo", "board/fix-bug-abc12345")
	assert.Equal(t, "/home/user/src/fix-bug-abc12345", got)
}

func TestWorktreePathStripsPrefix(t *testing.T) {
	got := WorktreePath("/repo", "board/my-branch")
	assert.Equal(t, "/my-branch", got)
}
