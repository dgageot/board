package board

import (
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) *sqlx.DB {
	t.Helper()

	db, err := sqlx.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	require.NoError(t, migrate(db))

	return db
}

func openTestStore(t *testing.T) *SQLiteStore {
	t.Helper()

	db := openTestDB(t)
	return &SQLiteStore{db: db}
}

// noopSessionManager is a no-op SessionManager for tests.
type noopSessionManager struct{}

func (noopSessionManager) NewSession(string, string, string, string) error { return nil }
func (noopSessionManager) KillSession(string) error                        { return nil }
func (noopSessionManager) SendKeys(string, string) error                   { return nil }
func (noopSessionManager) PaneContent(string) (string, error)              { return "", nil }

func newTestBoard(t *testing.T) (*Board, *SQLiteStore) {
	t.Helper()

	store := openTestStore(t)
	sessions := noopSessionManager{}
	b := &Board{
		config:   Config{DefaultAgent: "test-agent", DefaultRepoPath: "/test/repo", ListenAddr: ":0"},
		store:    store,
		sessions: sessions,
		poller:   newPoller(store, sessions, func() {}),
		clients:  make(map[chan string]struct{}),
	}

	return b, store
}
