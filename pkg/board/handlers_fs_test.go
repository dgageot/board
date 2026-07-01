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
