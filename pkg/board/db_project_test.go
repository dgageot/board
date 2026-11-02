package board

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateProjectPreservesOrderAndCards(t *testing.T) {
	store := openTestStore(t)
	for _, id := range []string{"a", "b", "c"} {
		require.NoError(t, store.InsertProject(&Project{ID: id, Name: id, RepoPath: "/old/repo", Agent: "/old/agent", Instructions: "old rules"}))
	}
	require.NoError(t, store.ReorderProjects([]string{"c", "a", "b"}))
	before, err := store.ListProjects()
	require.NoError(t, err)

	card := &Card{
		ID: "card-1", Title: "Existing card", Prompt: "Keep this prompt", TitleGenerated: true,
		Column: "dev", Status: StatusRunning, Project: "a", Agent: "/old/agent", RepoPath: "/old/repo",
		Branch: "branch", Worktree: "/worktree", Session: "session", AgentSession: "agent-session",
		Instructions: "old rules", Cost: 1.5, PRURL: "https://example.com/pull/1", CoachRan: true,
	}
	require.NoError(t, store.InsertCard(card))

	updated, err := store.UpdateProject(&Project{ID: "a", Name: "Renamed", RepoPath: "/new/repo", Agent: "/new/agent", Instructions: "new rules", Pos: 99})
	require.NoError(t, err)
	want := &Project{ID: "a", Name: "Renamed", RepoPath: "/new/repo", Agent: "/new/agent", Instructions: "new rules", Pos: 1}
	assert.Equal(t, want, updated)

	stored, err := store.GetProject("a")
	require.NoError(t, err)
	assert.Equal(t, want, stored)
	after, err := store.ListProjects()
	require.NoError(t, err)
	assert.Equal(t, []*Project{before[0], want, before[2]}, after)
	cards, err := store.ListCards()
	require.NoError(t, err)
	assert.Equal(t, []*Card{card}, cards)

	unchanged, err := store.UpdateProject(want)
	require.NoError(t, err, "saving unchanged values still returns the project")
	assert.Equal(t, want, unchanged)
}

func TestUpdateProjectNotFound(t *testing.T) {
	store := openTestStore(t)

	project, err := store.UpdateProject(&Project{ID: "missing", Name: "Missing", RepoPath: "/repo", Agent: "/agent"})
	require.ErrorIs(t, err, sql.ErrNoRows)
	assert.Nil(t, project)
	projects, err := store.ListProjects()
	require.NoError(t, err)
	assert.Empty(t, projects)
}

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

func TestProjectInstructionsMigrationAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.db")
	db, err := sqlx.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	migrations, err := loadMigrations()
	require.NoError(t, err)
	for _, migration := range migrations {
		if migration.version >= 15 {
			break
		}
		data, err := migrationFiles.ReadFile("migrations/" + migration.name)
		require.NoError(t, err)
		_, err = db.Exec(string(data))
		require.NoError(t, err)
	}
	_, err = db.Exec(`
		CREATE TABLE schema_version (version INTEGER NOT NULL);
		INSERT INTO schema_version VALUES (14);
		INSERT INTO projects (id, name, repo_path, agent, pos) VALUES ('old-project', 'Old', '/repo', '/agent', 0);
		INSERT INTO cards (id, title, col, status, agent, repo_path, branch, worktree, session)
		VALUES ('old-card', 'Old task', 'dev', 'waiting', '/agent', '/repo', 'branch', '/worktree', 'session');
	`)
	require.NoError(t, err)
	require.NoError(t, migrate(db))
	require.NoError(t, migrate(db), "migration is idempotent")
	store := &SQLiteStore{db: db}
	oldProject, err := store.GetProject("old-project")
	require.NoError(t, err)
	assert.Empty(t, oldProject.Instructions)
	oldCard, err := store.GetCard("old-card")
	require.NoError(t, err)
	assert.Empty(t, oldCard.Instructions)

	instructions := " \nRun tests; preserve $variables, `backticks` and {{placeholders}}.\n"
	project := &Project{ID: "project", Name: "Project", RepoPath: "/repo", Agent: "/agent", Instructions: instructions}
	require.NoError(t, store.InsertProject(project))
	card := &Card{ID: "card", Title: "Task", Instructions: instructions}
	require.NoError(t, store.InsertCard(card))
	require.NoError(t, store.Close())
	reopened, err := sqlx.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	require.NoError(t, migrate(reopened))
	store = &SQLiteStore{db: reopened}
	projects, err := store.ListProjects()
	require.NoError(t, err)
	assert.Equal(t, []*Project{oldProject, project}, projects)
	cards, err := store.ListCards()
	require.NoError(t, err)
	assert.Equal(t, []*Card{oldCard, card}, cards)
}
