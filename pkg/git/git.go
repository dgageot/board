// Package git provides git worktree and diff operations.
package git

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
