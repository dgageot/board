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

func commitFile(t *testing.T, repo, name, contents, subject string) string {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(repo, name), []byte(contents), 0o644))
	gitInDir(t, repo, "add", name)
	gitInDir(t, repo, "-c", "user.email=board@test", "-c", "user.name=board", "-c", "commit.gpgsign=false", "commit", "-m", subject)
	hash, err := runGit(repo, "rev-parse", "HEAD")
	require.NoError(t, err)
	return strings.TrimSpace(hash)
}

func TestCommitsAndIndividualDiffs(t *testing.T) {
	repo := initRepoWithCommit(t)
	gitInDir(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	first := commitFile(t, repo, "one.txt", "one\n", "First <change>")
	second := commitFile(t, repo, "two.txt", "two\n", "Second change")
	gitInDir(t, repo, "-c", "user.email=board@test", "-c", "user.name=board", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "Empty change")

	// Neither dirty files nor the real index may enter a commit patch.
	require.NoError(t, os.WriteFile(filepath.Join(repo, "one.txt"), []byte("dirty\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("untracked\n"), 0o644))
	gitInDir(t, repo, "add", "one.txt")
	before, err := runGit(repo, "status", "--porcelain=v1")
	require.NoError(t, err)

	commits, err := Commits(repo)
	require.NoError(t, err)
	require.Len(t, commits, 3)
	assert.Equal(t, Commit{Hash: first, Subject: "First <change>"}, commits[0])
	assert.Equal(t, Commit{Hash: second, Subject: "Second change"}, commits[1])

	diff, err := CommitDiff(repo, first)
	require.NoError(t, err)
	assert.Contains(t, diff, "+one")
	assert.NotContains(t, diff, "two.txt")
	assert.NotContains(t, diff, "dirty")
	assert.NotContains(t, diff, "untracked")
	diff, err = CommitDiff(repo, second)
	require.NoError(t, err)
	assert.Contains(t, diff, "+two")
	assert.NotContains(t, diff, "one.txt")
	diff, err = CommitDiff(repo, commits[2].Hash)
	require.NoError(t, err)
	assert.Empty(t, diff)
	after, err := runGit(repo, "status", "--porcelain=v1")
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestCommitsExcludeUpstreamHistoryAndShowMergeAgainstFirstParent(t *testing.T) {
	repo := initRepoWithCommit(t)
	gitInDir(t, repo, "branch", "upstream")
	gitInDir(t, repo, "checkout", "-b", "feature")
	feature := commitFile(t, repo, "feature.txt", "feature\n", "Feature")
	gitInDir(t, repo, "checkout", "upstream")
	upstream := commitFile(t, repo, "upstream.txt", "upstream\n", "Upstream")
	gitInDir(t, repo, "update-ref", "refs/remotes/origin/main", upstream)
	gitInDir(t, repo, "checkout", "feature")
	gitInDir(t, repo, "-c", "user.email=board@test", "-c", "user.name=board", "-c", "commit.gpgsign=false", "merge", "--no-ff", "upstream", "-m", "Merge upstream")

	commits, err := Commits(repo)
	require.NoError(t, err)
	require.Len(t, commits, 2)
	assert.Equal(t, feature, commits[0].Hash)
	assert.Equal(t, "Merge upstream", commits[1].Subject)
	diff, err := CommitDiff(repo, commits[1].Hash)
	require.NoError(t, err)
	assert.Contains(t, diff, "+upstream")
	assert.NotContains(t, diff, "feature.txt")
	_, err = CommitDiff(repo, upstream)
	require.ErrorIs(t, err, ErrCommitNotFound)
}

func TestCommitsEmptyMissingAndInvalidSelection(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	commits, err := Commits(missing)
	require.NoError(t, err)
	assert.NotNil(t, commits)
	assert.Empty(t, commits)

	repo := initRepoWithCommit(t)
	base, err := runGit(repo, "rev-parse", "HEAD")
	require.NoError(t, err)
	gitInDir(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	commits, err = Commits(repo)
	require.NoError(t, err)
	assert.Empty(t, commits)
	for _, hash := range []string{"HEAD", "--output=/tmp/invalid", "HEAD~1", strings.Repeat("0", 40), strings.TrimSpace(base)} {
		_, err := CommitDiff(repo, hash)
		require.ErrorIs(t, err, ErrCommitNotFound)
	}
	_, err = CommitDiff(missing, strings.Repeat("a", 40))
	require.ErrorIs(t, err, ErrCommitNotFound)
	_, err = Commits(t.TempDir())
	require.Error(t, err)
}

func TestCommitsIgnoreConfiguredSignatureOutput(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen unavailable")
	}
	repo := initRepoWithCommit(t)
	gitInDir(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	key := filepath.Join(t.TempDir(), "signing_key")
	out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput()
	require.NoError(t, err, string(out))
	gitInDir(t, repo, "config", "gpg.format", "ssh")
	gitInDir(t, repo, "config", "gpg.ssh.program", "ssh-keygen")
	gitInDir(t, repo, "config", "user.signingkey", key)
	gitInDir(t, repo, "config", "log.showSignature", "true")
	gitInDir(t, repo, "-c", "user.email=board@test", "-c", "user.name=board", "commit", "--allow-empty", "-S", "-m", "Signed change")
	head, err := runGit(repo, "rev-parse", "HEAD")
	require.NoError(t, err)
	commits, err := Commits(repo)
	require.NoError(t, err)
	assert.Equal(t, []Commit{{Hash: strings.TrimSpace(head), Subject: "Signed change"}}, commits)
	diff, err := CommitDiff(repo, strings.TrimSpace(head))
	require.NoError(t, err)
	assert.Empty(t, diff)
}
