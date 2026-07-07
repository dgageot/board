package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// A worktree that does not exist yet has no PR status either (and no URL to
// resolve one by).
func TestPRStatusMissingWorktree(t *testing.T) {
	info, err := PRStatus(t.Context(), filepath.Join(t.TempDir(), "does-not-exist"), "")
	require.NoError(t, err)
	assert.Equal(t, PRInfo{}, info)
}

// A repository with no associated pull request yields an unknown status
// without surfacing an error.
func TestPRStatusNoPullRequest(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	info, err := PRStatus(ctx, initRepoWithCommit(t), "")
	require.NoError(t, err)
	assert.Equal(t, PRInfo{}, info)
}

// rollupStatus reduces the PR lifecycle state, draft flag, review decision and
// check rollup to a single status.
func TestRollupStatus(t *testing.T) {
	// check builds a rollup entry from a {status, conclusion, state} triple.
	check := func(c [3]string) prCheck {
		return prCheck{Status: c[0], Conclusion: c[1], State: c[2]}
	}
	// mkPR builds a prView from state, draft flag, review decision, and check
	// outcome triples, keeping the cases below terse.
	mkPR := func(state string, draft bool, review string, checks ...[3]string) prView {
		pr := prView{State: state, IsDraft: draft, ReviewDecision: review}
		for _, c := range checks {
			pr.StatusCheckRollup = append(pr.StatusCheckRollup, check(c))
		}
		return pr
	}

	pass := [3]string{"COMPLETED", "SUCCESS", ""}
	fail := [3]string{"COMPLETED", "FAILURE", ""}
	running := [3]string{"IN_PROGRESS", "", ""}
	skipped := [3]string{"COMPLETED", "SKIPPED", ""}

	tests := []struct {
		name string
		pr   prView
		want string
	}{
		// Lifecycle state wins outright.
		{"merged wins over any checks", mkPR("MERGED", false, "", fail), PRStatusMerged},
		{"lowercase merged state is handled", mkPR("merged", false, ""), PRStatusMerged},
		{"closed not merged is closed", mkPR("CLOSED", false, "", pass), PRStatusClosed},
		{"draft open PR is draft", mkPR("OPEN", true, "", pass), PRStatusDraft},
		{"merged draft is still merged", mkPR("MERGED", true, ""), PRStatusMerged},

		// Open PR: CI signal.
		{"open with no checks is open (waiting for review)", mkPR("OPEN", false, ""), PRStatusOpen},
		{"only skipped checks is open", mkPR("OPEN", false, "", skipped), PRStatusOpen},
		{"passing checks is success", mkPR("OPEN", false, "", pass, skipped), PRStatusSuccess},
		{"any failed check is failure", mkPR("OPEN", false, "", pass, fail), PRStatusFailure},
		{"a running check with none failed is pending", mkPR("OPEN", false, "", pass, running), PRStatusPending},
		{"failure beats a still-running check", mkPR("OPEN", false, "", running, fail), PRStatusFailure},
		{"StatusContext state pending", mkPR("OPEN", false, "", [3]string{"", "", "PENDING"}), PRStatusPending},
		{"StatusContext state failure", mkPR("OPEN", false, "", [3]string{"", "", "FAILURE"}), PRStatusFailure},

		// Open PR: review signal.
		{"approved with no checks is success", mkPR("OPEN", false, "APPROVED"), PRStatusSuccess},
		{"changes requested is failure", mkPR("OPEN", false, "CHANGES_REQUESTED", pass), PRStatusFailure},
		{"changes requested beats approval-less green", mkPR("OPEN", false, "CHANGES_REQUESTED"), PRStatusFailure},
		{"failed CI beats approval", mkPR("OPEN", false, "APPROVED", fail), PRStatusFailure},
		{"pending CI beats approval", mkPR("OPEN", false, "APPROVED", running), PRStatusPending},
		{"review required with no checks is open", mkPR("OPEN", false, "REVIEW_REQUIRED"), PRStatusOpen},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, rollupStatus(tt.pr))
		})
	}
}

// The check rollup lists every run recorded on the head commit, including
// stale ones superseded by a re-run or cancelled by a concurrency group. Only
// the most recent run of each check may count, otherwise a PR shows red after
// a failed check was re-run green.
func TestRollupStatusIgnoresSupersededRuns(t *testing.T) {
	at := func(minute int) time.Time { return time.Date(2026, 1, 1, 12, minute, 0, 0, time.UTC) }
	run := func(name, conclusion string, minute int) prCheck {
		return prCheck{Name: name, WorkflowName: "CI", Status: "COMPLETED", Conclusion: conclusion, StartedAt: at(minute)}
	}
	ctx := func(name, state string, minute int) prCheck {
		return prCheck{Context: name, State: state, StartedAt: at(minute)}
	}

	tests := []struct {
		name   string
		checks []prCheck
		want   string
	}{
		{"failed run re-run green is success", []prCheck{run("test", "FAILURE", 0), run("test", "SUCCESS", 5)}, PRStatusSuccess},
		{"cancelled run superseded by green is success", []prCheck{run("test", "CANCELLED", 0), run("test", "SUCCESS", 5)}, PRStatusSuccess},
		{"failed run re-running again is pending", []prCheck{run("test", "FAILURE", 0), {Name: "test", WorkflowName: "CI", Status: "IN_PROGRESS", StartedAt: at(5)}}, PRStatusPending},
		{"latest run failing is still failure", []prCheck{run("test", "SUCCESS", 0), run("test", "FAILURE", 5)}, PRStatusFailure},
		{"same job name in another workflow is distinct", []prCheck{run("test", "SUCCESS", 5), {Name: "test", WorkflowName: "Nightly", Status: "COMPLETED", Conclusion: "FAILURE", StartedAt: at(0)}}, PRStatusFailure},
		{"status context superseded by green is success", []prCheck{ctx("ci/build", "FAILURE", 0), ctx("ci/build", "SUCCESS", 5)}, PRStatusSuccess},
		{"unnamed entries are all kept", []prCheck{{Status: "COMPLETED", Conclusion: "SUCCESS"}, {Status: "COMPLETED", Conclusion: "FAILURE"}}, PRStatusFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := prView{State: "OPEN", StatusCheckRollup: tt.checks}
			assert.Equal(t, tt.want, rollupStatus(pr))
		})
	}
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

func TestRemoteReposMissingWorktree(t *testing.T) {
	assert.Nil(t, RemoteRepos(filepath.Join(t.TempDir(), "does-not-exist")))
}

func TestRemoteReposNoRemotes(t *testing.T) {
	assert.Empty(t, RemoteRepos(initRepo(t)))
}

// RemoteRepos parses owner/repo from both SSH and HTTPS GitHub remotes and
// deduplicates (a remote's fetch and push URLs both appear in `git remote -v`).
func TestRemoteReposParsesAndDedupes(t *testing.T) {
	repo := initRepo(t)
	gitInDir(t, repo, "remote", "add", "origin", "git@github.com:gtardif/board.git")
	gitInDir(t, repo, "remote", "add", "upstream", "https://github.com/dgageot/board.git")
	// A non-GitHub remote must be ignored.
	gitInDir(t, repo, "remote", "add", "gitlab", "https://gitlab.com/x/y.git")

	got := RemoteRepos(repo)
	assert.ElementsMatch(t, []string{"gtardif/board", "dgageot/board"}, got)
}

func TestPRURLForHeadMissingWorktree(t *testing.T) {
	url, err := PRURLForHead(t.Context(), filepath.Join(t.TempDir(), "does-not-exist"))
	require.NoError(t, err)
	assert.Empty(t, url)
}

// A repo with no GitHub remotes has nothing to search, so no PR is found and
// `gh` is never invoked. Best-effort: empty, no error.
func TestPRURLForHeadNoRemotes(t *testing.T) {
	url, err := PRURLForHead(t.Context(), initRepoWithCommit(t))
	require.NoError(t, err)
	assert.Empty(t, url)
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
