// Package git provides git worktree and diff operations.
package git

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// RemoveWorktree removes a git worktree and its branch.
func RemoveWorktree(repoPath, worktreePath, branch string) {
	cmd := exec.Command("git", "worktree", "remove", "--force", worktreePath)
	cmd.Dir = repoPath
	_ = cmd.Run()

	cmd = exec.Command("git", "branch", "-D", branch)
	cmd.Dir = repoPath
	_ = cmd.Run()
}

// Diff returns the full diff of all changes in the worktree relative to the
// merge-base with the upstream default branch. This includes committed,
// staged, unstaged, and untracked files. It never mutates the worktree.
func Diff(worktree string) (string, error) {
	// The worktree may not exist yet while the agent is still starting up and
	// docker-agent has not created it. Report no changes rather than an error.
	if _, err := os.Stat(worktree); err != nil {
		return "", nil
	}

	base, err := runGit(worktree, "merge-base", "HEAD", UpstreamBase(worktree))
	if err != nil {
		return "", err
	}

	// Mark untracked files as intent-to-add so they appear in the diff — in a
	// throwaway copy of the index, so this read never mutates the worktree's
	// real index (which would surprise git status, stash, …).
	indexCopy, cleanup, err := copyIndex(worktree)
	if err != nil {
		return "", err
	}
	defer cleanup()

	env := append(os.Environ(), "GIT_INDEX_FILE="+indexCopy)
	if _, err := runGitEnv(worktree, env, "add", "--intent-to-add", "."); err != nil {
		return "", err
	}

	return runGitEnv(worktree, env, "diff", strings.TrimSpace(base))
}

// copyIndex copies the worktree's git index to a temporary file and returns
// its path and a cleanup func. A missing index (fresh repository) yields an
// empty temporary index.
func copyIndex(worktree string) (string, func(), error) {
	out, err := runGit(worktree, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return "", nil, err
	}
	indexPath := strings.TrimSpace(out)

	tmp, err := os.CreateTemp("", "board-index-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.Remove(tmp.Name()) }

	err = func() error {
		defer func() { _ = tmp.Close() }()
		src, err := os.Open(indexPath)
		if os.IsNotExist(err) {
			return nil // no index yet: start from an empty one
		}
		if err != nil {
			return err
		}
		defer func() { _ = src.Close() }()
		_, err = io.Copy(tmp, src)
		return err
	}()
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("copy index: %w", err)
	}

	return tmp.Name(), cleanup, nil
}

// runGit runs `git <args...>` in dir and returns stdout, including stderr in
// any returned error.
func runGit(dir string, args ...string) (string, error) {
	return runGitEnv(dir, nil, args...)
}

// runGitEnv is [runGit] with an explicit environment (nil inherits the
// process environment).
func runGitEnv(dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %s: %w", args[0], strings.TrimSpace(stderr.String()), err)
	}
	return string(out), nil
}

// IsRepo reports whether path is inside a git working tree.
func IsRepo(path string) bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = path
	return cmd.Run() == nil
}

// UpstreamBase returns the ref worktrees branch from and diffs compare against:
// the default branch of the repository's upstream remote, as "<remote>/<branch>".
//
// Remote names are not universal: some users keep the canonical repo as
// "origin" and never add a fork remote, while others name the canonical repo
// "upstream" and point "origin" at their own fork. So the remote is detected
// rather than assumed: a remote named "upstream" wins when present, otherwise
// "origin". The branch is read from the remote's recorded HEAD; when that is
// not recorded, the conventional default branches are probed before assuming
// "main".
func UpstreamBase(dir string) string {
	remote := upstreamRemote(dir)
	if out, err := runGit(dir, "symbolic-ref", "--short", "refs/remotes/"+remote+"/HEAD"); err == nil {
		if ref := strings.TrimSpace(out); ref != "" {
			return ref
		}
	}
	for _, branch := range []string{"main", "master"} {
		ref := remote + "/" + branch
		if _, err := runGit(dir, "rev-parse", "--verify", "--quiet", "refs/remotes/"+ref); err == nil {
			return ref
		}
	}
	return remote + "/main"
}

// upstreamRemote returns "upstream" when the repository has a remote by that
// name, otherwise "origin".
func upstreamRemote(dir string) string {
	out, err := runGit(dir, "remote")
	if err != nil {
		return "origin"
	}
	if slices.Contains(strings.Fields(out), "upstream") {
		return "upstream"
	}
	return "origin"
}

// ghRepoPattern extracts "owner/repo" from a GitHub remote URL, matching both
// SSH (git@github.com:owner/repo.git) and HTTPS
// (https://github.com/owner/repo.git) forms, with an optional ".git" suffix.
var ghRepoPattern = regexp.MustCompile(`github\.com[:/]([^/\s]+)/([^/\s]+?)(?:\.git)?$`)

// RemoteRepos returns the set of GitHub "owner/repo" slugs the worktree's git
// remotes point at (origin, upstream, ...), deduplicated. It is used to scope
// pull request URL detection to the card's own repository: a PR opened for the
// card is on one of these repos, so URLs on any other repo can be ignored. A
// missing worktree or a repo with no GitHub remotes yields an empty slice and
// no error — best-effort, so callers simply skip repo scoping then.
func RemoteRepos(worktree string) []string {
	if _, err := os.Stat(worktree); err != nil {
		return nil
	}
	out, err := runGit(worktree, "remote", "-v")
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var repos []string
	for line := range strings.SplitSeq(out, "\n") {
		// Each line is "<name>\t<url> (fetch|push)"; pull the URL out.
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		m := ghRepoPattern.FindStringSubmatch(fields[1])
		if m == nil {
			continue
		}
		repo := m[1] + "/" + m[2]
		if !seen[repo] {
			seen[repo] = true
			repos = append(repos, repo)
		}
	}
	return repos
}

// PRURLForHead returns the URL of the pull request whose head commit is the
// worktree's current HEAD, or "" when none matches. It identifies the PR by
// commit SHA rather than branch name — the agent may push under a different
// branch name (e.g. to a fork), which breaks name-based lookups, but the head
// commit is unique to the pushed branch, so the match is exact.
//
// It searches each of the worktree's GitHub remotes (RemoteRepos) with
// `gh pr list --search <sha>` and keeps only a PR whose headRefOid equals HEAD,
// so a match on a shared ancestor commit (which GitHub's search also returns)
// is rejected. A missing worktree, a missing `gh`, no remotes, unpushed local
// commits, or simply no PR all yield "" and no error — best-effort, so the
// common "not opened yet / diverged" cases are not treated as failures.
//
// The context bounds the `gh` invocations: they reach out to GitHub, so a
// caller must be able to cap how long it waits (and cancel it).
func PRURLForHead(ctx context.Context, worktree string) (string, error) {
	if _, err := os.Stat(worktree); err != nil {
		return "", nil
	}
	head, err := runGit(worktree, "rev-parse", "HEAD")
	if err != nil {
		return "", nil
	}
	head = strings.TrimSpace(head)

	for _, repo := range RemoteRepos(worktree) {
		cmd := exec.CommandContext(ctx, "gh", "pr", "list",
			"--repo", repo, "--state", "all", "--search", head,
			"--json", "url,headRefOid",
			"--jq", ".[] | select(.headRefOid==\""+head+"\") | .url")
		out, err := cmd.Output()
		if err != nil {
			continue // no `gh`, not authenticated, or no match on this repo
		}
		// --jq may emit several matching URLs (rare); take the first.
		if url := strings.TrimSpace(string(out)); url != "" {
			if i := strings.IndexByte(url, '\n'); i >= 0 {
				url = url[:i]
			}
			return url, nil
		}
	}
	return "", nil
}

// WorktreeDir returns the directory docker-agent creates for a worktree of the
// given name: ~/.cagent/worktrees/<name>. This mirrors docker-agent's
// --worktree convention so the board can locate the worktree for diffs,
// editing, and cleanup. The home dir lookup error is ignored: the board
// validates the home directory at startup.
func WorktreeDir(name string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cagent", "worktrees", name)
}

// WorktreeBranch returns the branch docker-agent checks out for a worktree of
// the given name: "worktree-<name>".
func WorktreeBranch(name string) string {
	return "worktree-" + name
}

// PR status values returned by [PRStatus]. They are mutually exclusive and
// each maps to a distinct icon + color the board shows next to a pull request
// link.
const (
	// PRStatusUnknown means there is no PR, or its status could not be read
	// (no `gh`, not authenticated, network error). The board shows no icon.
	PRStatusUnknown = ""
	// PRStatusMerged means the PR has been merged.
	PRStatusMerged = "merged"
	// PRStatusClosed means the PR was closed without being merged.
	PRStatusClosed = "closed"
	// PRStatusDraft means the PR is open but still a draft.
	PRStatusDraft = "draft"
	// PRStatusFailure means the PR is open and blocked: a check failed or a
	// reviewer requested changes.
	PRStatusFailure = "failure"
	// PRStatusPending means the PR is open and checks are still running (and
	// none has failed yet).
	PRStatusPending = "pending"
	// PRStatusSuccess means the PR is open and green: checks passed and/or a
	// review approved it.
	PRStatusSuccess = "success"
	// PRStatusOpen means the PR is open with no CI or review signal yet, i.e.
	// simply waiting for review.
	PRStatusOpen = "open"
)

// prView is the subset of `gh pr view --json ...` output PRStatus parses. Each
// check in statusCheckRollup is either a CheckRun (with a `conclusion` once
// COMPLETED, else a running `status`) or a StatusContext (with a `state`), so
// both fields are read.
type prView struct {
	State             string    `json:"state"`          // OPEN, MERGED, CLOSED
	IsDraft           bool      `json:"isDraft"`        // open draft PR
	ReviewDecision    string    `json:"reviewDecision"` // APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED, ""
	StatusCheckRollup []prCheck `json:"statusCheckRollup"`
}

// prCheck is one entry of a PR's statusCheckRollup.
type prCheck struct {
	Name         string    `json:"name"`         // CheckRun: check run name
	WorkflowName string    `json:"workflowName"` // CheckRun: owning workflow
	Context      string    `json:"context"`      // StatusContext: context name
	Status       string    `json:"status"`       // CheckRun: QUEUED, IN_PROGRESS, COMPLETED
	Conclusion   string    `json:"conclusion"`   // CheckRun: SUCCESS, FAILURE, ...
	State        string    `json:"state"`        // StatusContext: SUCCESS, FAILURE, PENDING, ...
	StartedAt    time.Time `json:"startedAt"`    // start of this run (both kinds)
}

// key groups the runs of one logical check: the status context name, or the
// workflow plus check run name (the same job name can exist in two
// workflows). An empty key means the entry cannot be grouped and is kept
// as-is.
func (c prCheck) key() string {
	if c.Context != "" {
		return c.Context
	}
	if c.Name == "" {
		return ""
	}
	return c.WorkflowName + "\x00" + c.Name
}

// PRInfo is the pull request state the board shows on a card: a single
// mutually-exclusive Status (one of the PRStatus* constants, driving the
// icon) plus Approved, an independent flag for a green check mark shown when
// the PR has an approving review and no outstanding change requests.
type PRInfo struct {
	Status   string `json:"status"`
	Approved bool   `json:"approved"`
}

// PRStatus returns the merge/CI status of a pull request. It identifies the PR
// by prURL when one is given (`gh pr view <url>`, which works from any
// directory and keeps reporting a merged/closed PR even after the branch or
// worktree is gone); otherwise it falls back to the PR of the worktree's
// current branch. A missing worktree, a missing `gh`, no PR, or any read error
// all yield a zero [PRInfo] (unknown status, not approved) and no error, so
// callers can display it as "no icon" without treating the common cases as
// failures.
//
// The context bounds the `gh` invocation: it reaches out to GitHub, so a
// caller must be able to cap how long it waits (and cancel it).
func PRStatus(ctx context.Context, worktree, prURL string) (PRInfo, error) {
	args := []string{"pr", "view", "--json", "state,isDraft,reviewDecision,statusCheckRollup"}
	cmd := exec.CommandContext(ctx, "gh", args...)
	if prURL != "" {
		// Resolve the PR by URL: independent of the local branch/worktree, and
		// still valid once a merged PR's worktree has been cleaned up.
		cmd.Args = append(cmd.Args, prURL)
	} else {
		// No recorded URL: fall back to the worktree's branch, if it exists.
		if _, err := os.Stat(worktree); err != nil {
			return PRInfo{}, nil
		}
		cmd.Dir = worktree
	}

	out, err := cmd.Output()
	if err != nil {
		return PRInfo{}, nil
	}

	var pr prView
	if err := json.Unmarshal(out, &pr); err != nil {
		return PRInfo{}, nil
	}
	return PRInfo{
		Status:   rollupStatus(pr),
		Approved: strings.EqualFold(pr.ReviewDecision, "APPROVED"),
	}, nil
}

// rollupStatus reduces a PR's lifecycle state, draft flag, review decision and
// check rollup to a single mutually-exclusive status, in priority order:
//
//   - merged / closed: the PR's terminal lifecycle state wins outright.
//   - draft: an open draft is not ready for signals yet.
//   - failure: a failed check or a "changes requested" review blocks it.
//   - pending: checks are still running (none failed).
//   - success: checks passed and/or a review approved it.
//   - open: open with no CI or review signal, i.e. waiting for review.
func rollupStatus(pr prView) string {
	switch strings.ToUpper(pr.State) {
	case "MERGED":
		return PRStatusMerged
	case "CLOSED":
		return PRStatusClosed
	}

	if pr.IsDraft {
		return PRStatusDraft
	}

	ci := ciStatus(pr.StatusCheckRollup)
	review := strings.ToUpper(pr.ReviewDecision)

	// A blocking signal (failed CI or requested changes) wins over anything
	// green, so a card never looks ready while something needs attention.
	if ci == PRStatusFailure || review == "CHANGES_REQUESTED" {
		return PRStatusFailure
	}
	if ci == PRStatusPending {
		return PRStatusPending
	}
	// Green: an explicit approval, or passing checks. ciStatus reports success
	// only when at least one check ran, so a PR with no checks does not.
	if review == "APPROVED" || ci == PRStatusSuccess {
		return PRStatusSuccess
	}
	// Open with nothing to report yet: waiting for review.
	return PRStatusOpen
}

// latestChecks reduces a check rollup to the most recent run of each check.
// The rollup lists every run recorded on the PR's head commit — including
// stale ones superseded by a re-run, or cancelled by a concurrency group — so
// judging all of them would keep a PR red after a failed check was re-run
// green. GitHub's UI (and `gh pr checks`) only shows the latest run per
// check; this mirrors that.
func latestChecks(checks []prCheck) []prCheck {
	sorted := slices.Clone(checks)
	slices.SortStableFunc(sorted, func(a, b prCheck) int {
		return b.StartedAt.Compare(a.StartedAt) // newest first
	})
	seen := map[string]bool{}
	latest := make([]prCheck, 0, len(sorted))
	for _, c := range sorted {
		key := c.key()
		if key != "" && seen[key] {
			continue // an older run of a check already kept
		}
		seen[key] = true
		latest = append(latest, c)
	}
	return latest
}

// ciStatus reduces a PR's check rollup to failure, pending, success, or
// unknown (no checks / none conclusive). Only the latest run of each check
// counts (see latestChecks). A single failed check makes the whole rollup a
// failure; otherwise any still-running check makes it pending; a success
// requires at least one passing check.
func ciStatus(checks []prCheck) string {
	pending := false
	passed := false
	for _, c := range latestChecks(checks) {
		// A CheckRun reports its outcome in `conclusion` once COMPLETED and,
		// while still running, leaves it empty with a `status` of QUEUED or
		// IN_PROGRESS. A StatusContext reports its outcome in `state`. Prefer
		// the completed outcome, then the StatusContext state, then the
		// still-running CheckRun status.
		outcome := c.Conclusion
		if outcome == "" {
			outcome = c.State
		}
		if outcome == "" {
			outcome = c.Status
		}
		switch strings.ToUpper(outcome) {
		case "FAILURE", "ERROR", "CANCELLED", "TIMED_OUT", "STARTUP_FAILURE", "ACTION_REQUIRED":
			return PRStatusFailure
		case "SUCCESS":
			passed = true
		case "NEUTRAL", "SKIPPED", "COMPLETED", "":
			// Not a gating result. An empty outcome, or a bare COMPLETED with
			// no recorded conclusion, neither passes nor blocks.
		default:
			// QUEUED, IN_PROGRESS, PENDING, EXPECTED, WAITING, REQUESTED...
			pending = true
		}
	}
	switch {
	case pending:
		return PRStatusPending
	case passed:
		return PRStatusSuccess
	default:
		return PRStatusUnknown
	}
}
