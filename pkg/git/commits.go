package git

import (
	"errors"
	"os"
	"regexp"
	"slices"
	"strings"
)

// Commit describes a commit introduced by the worktree's branch.
type Commit struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
}

// ErrCommitNotFound means the requested commit is not part of the worktree's changes.
var ErrCommitNotFound = errors.New("commit not found in worktree")

// Commits lists commits reachable from HEAD but not the upstream default branch,
// oldest first. Upstream commits brought in by a merge are excluded.
func Commits(worktree string) ([]Commit, error) {
	commits := make([]Commit, 0)
	if _, err := os.Stat(worktree); err != nil {
		if os.IsNotExist(err) {
			return commits, nil
		}
		return nil, err
	}

	out, err := runGit(worktree, "log", "--topo-order", "--reverse", "--no-decorate", "--no-notes", "--no-show-signature",
		"-z", "--format=%H%x00%s", "HEAD", "--not", UpstreamBase(worktree), "--")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return commits, nil
	}
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	if len(fields)%2 != 0 {
		return nil, errors.New("invalid git log output")
	}
	for i := 0; i < len(fields); i += 2 {
		commits = append(commits, Commit{Hash: fields[i], Subject: fields[i+1]})
	}
	return commits, nil
}

var commitHashPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// CommitDiff returns only the selected commit's patch, relative to its first
// parent for merges. It never includes index or working-tree changes.
func CommitDiff(worktree, hash string) (string, error) {
	if !commitHashPattern.MatchString(hash) {
		return "", ErrCommitNotFound
	}
	commits, err := Commits(worktree)
	if err != nil {
		return "", err
	}
	if !slices.ContainsFunc(commits, func(commit Commit) bool { return commit.Hash == hash }) {
		return "", ErrCommitNotFound
	}
	return runGit(worktree, "show", "--format=", "--patch", "--root", "--no-ext-diff",
		"--no-textconv", "--no-color", "--no-show-signature", "--diff-merges=first-parent", hash, "--")
}
