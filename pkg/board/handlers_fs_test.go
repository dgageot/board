package board

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithinDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "home", "user")

	assert.True(t, withinDir(root, root))
	assert.True(t, withinDir(root, filepath.Join(root, "src")))
	assert.True(t, withinDir(root, filepath.Join(root, "a", "b")))
	assert.False(t, withinDir(root, filepath.Dir(root)))
	assert.False(t, withinDir(root, "/etc"))
	assert.False(t, withinDir(root, root+"2"), "sibling with a common prefix is outside")
}

func browse(t *testing.T, b *Board, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/api/browse?path="+path, http.NoBody)
	rec := httptest.NewRecorder()
	b.handleBrowse(rec, req)
	return rec
}

func TestHandleBrowseDefaultsToHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	b, _ := newTestBoard(t)

	rec := browse(t, b, "")
	require.Equal(t, http.StatusOK, rec.Code)

	var resp browseResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, home, resp.Path)
	assert.Empty(t, resp.Parent, "home is the browse root: no parent is offered")
}

func TestHandleBrowseRejectsPathsOutsideHome(t *testing.T) {
	b, _ := newTestBoard(t)

	assert.Equal(t, http.StatusBadRequest, browse(t, b, "/etc").Code)
	assert.Equal(t, http.StatusBadRequest, browse(t, b, "/").Code)
}

func TestHandleBrowseRejectsTraversalOutOfHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	b, _ := newTestBoard(t)

	rec := browse(t, b, filepath.Join(home, "..", ".."))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDirectoryListingsPreserveFilenameOrder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".agents"), 0o755))
	for _, name := range []string{"z.yml", "B.yaml", "a.YAML", "a b.yaml", "ignore.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(home, ".agents", name), nil, 0o600))
	}
	for _, name := range []string{"z", "B", "a", "a b", ".hidden"} {
		require.NoError(t, os.Mkdir(filepath.Join(home, name), 0o755))
	}
	b := &Board{}
	rec := httptest.NewRecorder()
	b.handleListAgents(rec, httptest.NewRequest(http.MethodGet, "/api/agents", http.NoBody))
	var agents []string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &agents))
	require.Equal(t, []string{
		filepath.Join(home, ".agents", "B.yaml"), filepath.Join(home, ".agents", "a b.yaml"),
		filepath.Join(home, ".agents", "a.YAML"), filepath.Join(home, ".agents", "z.yml"),
	}, agents)
	rec = browse(t, b, "")
	var folders browseResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &folders))
	require.Equal(t, []string{"B", "a", "a b", "z"}, folders.Dirs)
}
