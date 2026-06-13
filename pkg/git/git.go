// Package git provides git worktree and diff operations.
package git

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// WorktreeDir returns the directory docker-agent creates for a worktree of the
// given name: ~/.cagent/worktrees/<name>. This mirrors docker-agent's
// --worktree convention so the board can locate the worktree for diffs,
// editing, and cleanup.
func WorktreeDir(name string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cagent", "worktrees", name)
}

// WorktreeBranch returns the branch docker-agent checks out for a worktree of
// the given name: "worktree-<name>".
func WorktreeBranch(name string) string {
	return "worktree-" + name
}
