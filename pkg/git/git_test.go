package git

import (
	"os"
	"os/exec"
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

func TestUpstreamBaseFallsBackToOrigin(t *testing.T) {
	repo := initRepo(t)
	// No remotes at all: fall back to the conventional origin/main.
	assert.Equal(t, "origin/main", UpstreamBase(repo))
}

func TestUpstreamBasePrefersUpstreamRemote(t *testing.T) {
	repo := initRepo(t)
	// origin is the user's fork; upstream is the canonical repo. UpstreamBase
	// must follow upstream, and read its recorded default branch.
	gitInDir(t, repo, "remote", "add", "origin", "https://example.com/fork.git")
	gitInDir(t, repo, "remote", "add", "upstream", "https://example.com/canonical.git")
	gitInDir(t, repo, "symbolic-ref", "refs/remotes/upstream/HEAD", "refs/remotes/upstream/master")

	assert.Equal(t, "upstream/master", UpstreamBase(repo))
}

func TestUpstreamBaseUsesOriginHead(t *testing.T) {
	repo := initRepo(t)
	// No upstream remote (the user's own convention): follow origin's HEAD.
	gitInDir(t, repo, "remote", "add", "origin", "https://example.com/canonical.git")
	gitInDir(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

	assert.Equal(t, "origin/main", UpstreamBase(repo))
}

func initRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitInDir(t, repo, "init")
	return repo
}

func gitInDir(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
}
