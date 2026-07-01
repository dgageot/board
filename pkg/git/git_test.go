package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestUpstreamBaseProbesDefaultBranches(t *testing.T) {
	repo := initRepoWithCommit(t)
	// origin exists but its HEAD was never recorded (e.g. a plain fetch):
	// probe the conventional default branches instead of assuming main.
	gitInDir(t, repo, "remote", "add", "origin", "https://example.com/canonical.git")
	gitInDir(t, repo, "update-ref", "refs/remotes/origin/master", "HEAD")

	assert.Equal(t, "origin/master", UpstreamBase(repo))
}

// GET /diff is a read: untracked files must show up in the diff without the
// worktree's real index being touched.
func TestDiffIncludesUntrackedWithoutTouchingIndex(t *testing.T) {
	repo := initRepoWithCommit(t)
	gitInDir(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "new.txt"), []byte("hello\n"), 0o644))

	diff, err := Diff(repo)
	require.NoError(t, err)
	assert.Contains(t, diff, "new.txt", "untracked files appear in the diff")
	assert.Contains(t, diff, "+hello")

	out, err := runGit(repo, "ls-files", "--", "new.txt")
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(out), "the real index must not gain an intent-to-add entry")
}

func initRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitInDir(t, repo, "init")
	return repo
}

func initRepoWithCommit(t *testing.T) string {
	t.Helper()
	repo := initRepo(t)
	gitInDir(t, repo, "-c", "user.email=board@test", "-c", "user.name=board", "commit", "--allow-empty", "-m", "init")
	return repo
}

func gitInDir(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
}
