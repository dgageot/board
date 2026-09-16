package board

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInsertProjectConcurrentPositionsAreDistinct guards the MAX(pos)+1
// computation against interleaving inserts: two projects must never land on
// the same position, otherwise ListProjects ORDER BY pos has ties and the
// sidebar order becomes arbitrary.
func TestInsertProjectConcurrentPositionsAreDistinct(t *testing.T) {
	for _, file := range []bool{false, true} {
		name := "memory"
		if file {
			name = "wal"
		}
		t.Run(name, func(t *testing.T) {
			store := openTestStore(t)
			if file {
				db, err := sqlx.Open("sqlite", filepath.Join(t.TempDir(), "board.db")+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate")
				require.NoError(t, err)
				t.Cleanup(func() { _ = db.Close() })
				require.NoError(t, migrate(db))
				store = &SQLiteStore{db: db}
			}
			checkConcurrentProjectPositions(t, store)
		})
	}
}

func checkConcurrentProjectPositions(t *testing.T, store *SQLiteStore) {
	t.Helper()
	const n = 32
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() {
			errs[i] = store.InsertProject(&Project{ID: fmt.Sprintf("p%d", i), Name: "p", RepoPath: "/r", Agent: "a"})
		})
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}

	projects, err := store.ListProjects()
	require.NoError(t, err)
	require.Len(t, projects, n)
	seen := make(map[int]string, n)
	for _, p := range projects {
		if prev, dup := seen[p.Pos]; dup {
			t.Fatalf("projects %s and %s share pos %d", prev, p.ID, p.Pos)
		}
		seen[p.Pos] = p.ID
	}
	assert.Equal(t, 0, projects[0].Pos)
	assert.Equal(t, n-1, projects[n-1].Pos)
}
