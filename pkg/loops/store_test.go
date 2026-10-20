package loops

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "loops.db")+"?_pragma=busy_timeout(5000)&_txlock=immediate")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	store, err := NewStore(t.Context(), db)
	require.NoError(t, err)
	return store
}

func testLoop(t *testing.T, store *Store) Loop {
	t.Helper()
	loop, err := store.Create(t.Context(), Loop{Name: "Release notes", Description: "Summarize changes", Criteria: "Do not invent", Example: "fix: login"})
	require.NoError(t, err)
	return loop
}

func TestLoopVersionsAreImmutable(t *testing.T) {
	store := testStore(t)
	loop := testLoop(t, store)
	first, err := store.AddVersion(t.Context(), loop.ID, "agents:\n  root:\n    model: auto\n", "baseline")
	require.NoError(t, err)
	second, err := store.AddVersion(t.Context(), loop.ID, "agents:\n  root:\n    model: openai/small\n", "cheaper")
	require.NoError(t, err)
	detail, err := store.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	assert.Equal(t, loop, detail.Loop)
	assert.Equal(t, []Version{second, first}, detail.Versions)
	assert.Equal(t, 1, first.Number)
	assert.Equal(t, 2, second.Number)
	assert.Empty(t, detail.Loop.ActiveVersion)
}

func TestLoopValidation(t *testing.T) {
	store := testStore(t)
	_, err := store.Create(t.Context(), Loop{Name: "Missing contract"})
	require.ErrorIs(t, err, ErrInvalid)
	loop := testLoop(t, store)
	for _, config := range []string{"[", "agents: {}", "agents: null"} {
		_, err := store.AddVersion(t.Context(), loop.ID, config, "")
		require.ErrorIs(t, err, ErrInvalid)
	}
	_, err = store.AddVersion(t.Context(), "missing", "agents:\n  root: {}", "")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestStoreReopenPreservesLoops(t *testing.T) {
	store := testStore(t)
	loop := testLoop(t, store)
	reopened, err := NewStore(t.Context(), store.db)
	require.NoError(t, err)
	list, err := reopened.List(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []Loop{loop}, list)
}
