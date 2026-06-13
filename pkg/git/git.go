// Package git provides git worktree and diff operations.
package git

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// BranchPrefix is the prefix the board uses for all worktree branches.
const BranchPrefix = "board/"

// CreateWorktree creates a new git worktree with a new branch based on origin/main.
// It fetches origin first to ensure the branch starts from the latest remote state.
func CreateWorktree(repoPath, branch, worktreePath string) error {
	cmd := exec.Command("git", "fetch", "origin", "main")
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch: %s: %w", out, err)
	}

	cmd = exec.Command("git", "worktree", "add", "-b", branch, worktreePath, "origin/main")
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree add: %s: %w", out, err)
	}

	return nil
}

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
// merge-base with main. This includes committed, staged, unstaged, and
// untracked files.
func Diff(worktree string) (string, error) {
	// Mark untracked files as intent-to-add so they appear in the diff.
	addCmd := exec.Command("git", "add", "--intent-to-add", ".")
	addCmd.Dir = worktree
	_ = addCmd.Run()

	base, err := runGit(worktree, "merge-base", "HEAD", "origin/main")
	if err != nil {
		return "", err
	}

	return runGit(worktree, "diff", strings.TrimSpace(base))
}

// runGit runs `git <args...>` in dir and returns stdout, including stderr in
// any returned error.
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
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

// WorktreePath computes the worktree directory path, a sibling of the repo.
func WorktreePath(repoPath, branch string) string {
	// Resolve relative paths (e.g. ".") so the worktree lands next to the
	// repo instead of inside it.
	if abs, err := filepath.Abs(repoPath); err == nil {
		repoPath = abs
	}
	name := strings.TrimPrefix(branch, BranchPrefix)
	return filepath.Join(filepath.Dir(repoPath), name)
}
