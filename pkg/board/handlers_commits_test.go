package board

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dgageot/board/pkg/git"
)

func TestCommitRoutes(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	run("init")
	run("-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "Base")
	run("update-ref", "refs/remotes/origin/main", "HEAD")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "file.txt"), []byte("committed\n"), 0o644))
	run("add", ".")
	run("-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "One change")
	hash := run("rev-parse", "HEAD")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "file.txt"), []byte("dirty\n"), 0o644))

	b, store := newTestBoard(t)
	require.NoError(t, store.SeedColumns(defaultColumns))
	require.NoError(t, store.InsertCard(&Card{
		ID: "c1", Title: "T", Column: "dev", Status: StatusWaiting,
		Worktree: repo, Session: "s1",
	}))
	mux, err := buildMux(b)
	require.NoError(t, err)
	request := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, http.NoBody))
		return rec
	}

	rec := request("/api/cards/c1/commits")
	require.Equal(t, http.StatusOK, rec.Code)
	var commits []git.Commit
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &commits))
	assert.Equal(t, []git.Commit{{Hash: hash, Subject: "One change"}}, commits)

	rec = request("/api/cards/c1/diff?commit=" + hash)
	require.Equal(t, http.StatusOK, rec.Code)
	var diff map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &diff))
	assert.Contains(t, diff["diff"], "+committed")
	assert.NotContains(t, diff["diff"], "dirty")

	rec = request("/api/cards/c1/diff")
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &diff))
	assert.Contains(t, diff["diff"], "+dirty")

	assert.Equal(t, http.StatusNotFound, request("/api/cards/missing/commits").Code)
	assert.Equal(t, http.StatusNotFound, request("/api/cards/missing/diff?commit="+hash).Code)
	assert.Equal(t, http.StatusNotFound, request("/api/cards/c1/diff?commit=HEAD").Code)
	assert.Equal(t, http.StatusNotFound, request("/api/cards/c1/diff?commit="+strings.Repeat("0", 40)).Code)
}

func TestCommitRoutesMissingWorktree(t *testing.T) {
	b, store := newTestBoard(t)
	require.NoError(t, store.SeedColumns(defaultColumns))
	require.NoError(t, store.InsertCard(&Card{
		ID: "c1", Title: "T", Column: "dev", Status: StatusWaiting,
		Worktree: filepath.Join(t.TempDir(), "missing"), Session: "s1",
	}))
	mux, err := buildMux(b)
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/cards/c1/commits", http.NoBody))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, "[]", rec.Body.String())
}
