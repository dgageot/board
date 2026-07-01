package board

import (
	"context"
	"errors"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/dgageot/board/pkg/agent"
)

func openTestDB(t *testing.T) *sqlx.DB {
	t.Helper()

	db, err := sqlx.Open("sqlite", ":memory:")
	require.NoError(t, err)
	// A :memory: database is per-connection; pin the pool to one connection so
	// the watcher goroutines and the test share the same database.
	db.SetMaxOpenConns(1)
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

func (noopSessionManager) NewSession(string, string, string, string, string, string, string, string) error {
	return nil
}
func (noopSessionManager) KillSession(string) error   { return nil }
func (noopSessionManager) Alive(string) (bool, error) { return true, nil }

// noopSessionClient is a control-plane client that never connects, used so a
// test Board's watchers stay idle instead of dialing real sockets.
type noopSessionClient struct{}

func (noopSessionClient) Snapshot(context.Context) (agent.Snapshot, error) {
	return agent.Snapshot{}, errors.New("no control plane in tests")
}

func (noopSessionClient) StreamEvents(context.Context, uint64, func(agent.Event) bool) error {
	return errors.New("no control plane in tests")
}

func (noopSessionClient) Followup(context.Context, string, string) (bool, error) {
	return false, errors.New("no control plane in tests")
}

func newTestBoard(t *testing.T) (*Board, *SQLiteStore) {
	t.Helper()

	store := openTestStore(t)
	cfg := Config{ListenAddr: ":0"}
	b, err := newBoard(t.Context(), cfg, store, noopSessionManager{})
	require.NoError(t, err)
	// Keep watchers from dialing real sockets.
	b.controller.clientFor = func(string, string) sessionClient { return noopSessionClient{} }

	return b, store
}
