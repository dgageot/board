// Package git provides git worktree and diff operations.
package git

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// CreateWorktree creates a new git worktree with a new branch.
func CreateWorktree(repoPath, branch, worktreePath string) error {
	cmd := exec.Command("git", "worktree", "add", "-b", branch, worktreePath)
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
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

// Diff returns the diff between the merge-base of HEAD and main, and the current worktree state.
func Diff(worktree string) (string, error) {
	baseCmd := exec.Command("git", "merge-base", "HEAD", "main")
	baseCmd.Dir = worktree
	baseOut, err := baseCmd.Output()
	if err != nil {
		return "", fmt.Errorf("git merge-base: %w", err)
	}

	base := strings.TrimSpace(string(baseOut))

	cmd := exec.Command("git", "diff", base)
	cmd.Dir = worktree
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff: %w", err)
	}
	return string(out), nil
}

// WorktreePath computes the worktree directory path.
func WorktreePath(repoPath, branch string) string {
	parentDir := filepath.Dir(repoPath)
	name := strings.TrimPrefix(branch, "board/")
	return filepath.Join(parentDir, name)
}
