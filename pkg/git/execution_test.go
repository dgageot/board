package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalExecutionBaseAlsoSupportsDiffAndCommits(t *testing.T) {
	repo := t.TempDir()
	command := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	command("init")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "file.txt"), []byte("before\n"), 0o600))
	command("add", ".")
	command("-c", "commit.gpgsign=false", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	base, err := ExecutionBase(t.Context(), repo)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "file.txt"), []byte("after\n"), 0o600))
	command("add", ".")
	command("-c", "commit.gpgsign=false", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "task")
	commits, err := Commits(repo, base)
	require.NoError(t, err)
	require.Len(t, commits, 1)
	diff, err := Diff(repo, base)
	require.NoError(t, err)
	assert.Contains(t, diff, "+after")
	diff, err = CommitDiff(repo, commits[0].Hash, base)
	require.NoError(t, err)
	assert.Contains(t, diff, "+after")
	command("update-ref", "refs/remotes/origin/main", "HEAD")
	commits, err = Commits(repo, base)
	require.NoError(t, err)
	assert.Len(t, commits, 1)
}

func TestExecutionPreflightHonorsCancellation(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := ExecutionBase(ctx, dir)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.False(t, IsRepoContext(ctx, dir))
	assert.Less(t, time.Since(start), 2*time.Second)
}
