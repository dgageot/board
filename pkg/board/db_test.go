package board

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrate(t *testing.T) {
	db := openTestDB(t)

	assert.True(t, tableExists(db, "cards"))
	assert.True(t, tableExists(db, "projects"))
	assert.True(t, tableExists(db, "columns"))
	assert.True(t, tableExists(db, "schema_version"))
}

func TestMigrateIsIdempotent(t *testing.T) {
	db := openTestDB(t)

	require.NoError(t, migrate(db))
	require.NoError(t, migrate(db))

	version, err := currentVersion(db)
	require.NoError(t, err)
	assert.Equal(t, 6, version)
}

func TestMigrationVersion(t *testing.T) {
	v, err := migrationVersion("002_project_position.sql")
	require.NoError(t, err)
	assert.Equal(t, 2, v)

	_, err = migrationVersion("no_version.sql")
	require.Error(t, err)

	_, err = migrationVersion("000_zero.sql")
	require.Error(t, err)
}

// The embedded migrations themselves must be sequential from 1: a renamed or
// dropped file would otherwise silently skip or re-run migrations.
func TestLoadMigrationsAreSequential(t *testing.T) {
	migrations, err := loadMigrations()
	require.NoError(t, err)
	require.NotEmpty(t, migrations)
	for i, m := range migrations {
		assert.Equal(t, i+1, m.version, m.name)
	}
}

// --- Card CRUD ---

func TestInsertAndGetCard(t *testing.T) {
	store := openTestStore(t)

	card := &Card{
		ID:       "card-1",
		Title:    "Fix bug",
		Column:   "dev",
		Status:   StatusRunning,
		Agent:    "/path/to/agent",
		RepoPath: "/path/to/repo",
		Branch:   "board/fix-bug-abc123",
		Worktree: "/path/to/worktree",
		Session:  "board-abc123",
	}
	require.NoError(t, store.InsertCard(card))

	got, err := store.GetCard("card-1")
	require.NoError(t, err)
	assert.Equal(t, card, got)
}

func TestGetCardNotFound(t *testing.T) {
	store := openTestStore(t)

	_, err := store.GetCard("nonexistent")
	assert.Error(t, err)
}

func TestListCardsEmpty(t *testing.T) {
	store := openTestStore(t)

	cards, err := store.ListCards()
	require.NoError(t, err)
	assert.Empty(t, cards)
}

func TestListCardsPreservesInsertionOrder(t *testing.T) {
	store := openTestStore(t)

	for _, id := range []string{"a", "b", "c"} {
		require.NoError(t, store.InsertCard(&Card{
			ID: id, Title: id, Column: "dev", Status: StatusRunning,
			Agent: "ag", RepoPath: "rp", Branch: "br", Worktree: "wt", Session: "s-" + id,
		}))
	}

	cards, err := store.ListCards()
	require.NoError(t, err)
	require.Len(t, cards, 3)
	assert.Equal(t, "a", cards[0].ID)
	assert.Equal(t, "b", cards[1].ID)
	assert.Equal(t, "c", cards[2].ID)
}

func TestUpdateCardStatusPreservesOtherFields(t *testing.T) {
	store := openTestStore(t)

	require.NoError(t, store.InsertCard(&Card{
		ID: "card-1", Title: "Old", Column: "dev", Status: StatusRunning,
		Agent: "ag", RepoPath: "rp", Branch: "br", Worktree: "wt", Session: "s1",
	}))

	require.NoError(t, store.UpdateCardStatus("card-1", StatusWaiting))

	got, err := store.GetCard("card-1")
	require.NoError(t, err)
	assert.Equal(t, StatusWaiting, got.Status)
	assert.Equal(t, "Old", got.Title)
	assert.Equal(t, "dev", got.Column)
	assert.Equal(t, "s1", got.Session)
}

func TestDeleteCard(t *testing.T) {
	store := openTestStore(t)

	require.NoError(t, store.InsertCard(&Card{
		ID: "card-1", Title: "t", Column: "dev", Status: StatusRunning,
		Agent: "ag", RepoPath: "rp", Branch: "br", Worktree: "wt", Session: "s1",
	}))

	require.NoError(t, store.DeleteCard("card-1"))

	_, err := store.GetCard("card-1")
	assert.Error(t, err)
}

func TestListCardsByColumn(t *testing.T) {
	store := openTestStore(t)

	for _, c := range []struct{ id, col string }{
		{"1", "dev"},
		{"2", "review"},
		{"3", "dev"},
	} {
		require.NoError(t, store.InsertCard(&Card{
			ID: c.id, Title: c.id, Column: c.col, Status: StatusRunning,
			Agent: "ag", RepoPath: "rp", Branch: "br", Worktree: "wt", Session: "s-" + c.id,
		}))
	}

	cards, err := store.ListCardsByColumn("dev")
	require.NoError(t, err)
	require.Len(t, cards, 2)
	assert.Equal(t, "1", cards[0].ID)
	assert.Equal(t, "3", cards[1].ID)
}

func TestMoveCardMovesToEnd(t *testing.T) {
	store := openTestStore(t)

	for _, id := range []string{"a", "b", "c"} {
		require.NoError(t, store.InsertCard(&Card{
			ID: id, Title: id, Column: "dev", Status: StatusRunning,
			Agent: "ag", RepoPath: "rp", Branch: "br", Worktree: "wt", Session: "s-" + id,
		}))
	}

	moved, err := store.MoveCard("a", "review", false)
	require.NoError(t, err)
	assert.Equal(t, "review", moved.Column)

	cards, err := store.ListCards()
	require.NoError(t, err)
	require.Len(t, cards, 3)
	assert.Equal(t, "b", cards[0].ID)
	assert.Equal(t, "c", cards[1].ID)
	assert.Equal(t, "a", cards[2].ID)
	assert.Equal(t, "review", cards[2].Column)
}

// The running check is part of the move transaction: a running card must be
// rejected based on the stored status, not a caller's stale snapshot.
func TestMoveCardRequireIdleRejectsRunning(t *testing.T) {
	store := openTestStore(t)

	require.NoError(t, store.InsertCard(&Card{
		ID: "a", Title: "a", Column: "dev", Status: StatusRunning,
		Agent: "ag", RepoPath: "rp", Branch: "br", Worktree: "wt", Session: "s",
	}))

	_, err := store.MoveCard("a", "review", true)
	require.ErrorIs(t, err, errCardRunning)

	card, err := store.GetCard("a")
	require.NoError(t, err)
	assert.Equal(t, "dev", card.Column, "a rejected move must not be persisted")
}

// --- Project CRUD ---

func TestInsertAndGetProject(t *testing.T) {
	store := openTestStore(t)

	p := &Project{ID: "p1", Name: "My Project", RepoPath: "/repo", Agent: "/agent"}
	require.NoError(t, store.InsertProject(p))

	got, err := store.GetProject("p1")
	require.NoError(t, err)
	assert.Equal(t, p, got)
}

func TestGetProjectNotFound(t *testing.T) {
	store := openTestStore(t)

	_, err := store.GetProject("nonexistent")
	assert.Error(t, err)
}

func TestListProjectsEmpty(t *testing.T) {
	store := openTestStore(t)

	projects, err := store.ListProjects()
	require.NoError(t, err)
	assert.Empty(t, projects)
}

func TestListProjectsPreservesInsertionOrder(t *testing.T) {
	store := openTestStore(t)

	for _, id := range []string{"a", "b", "c"} {
		require.NoError(t, store.InsertProject(&Project{ID: id, Name: id, RepoPath: "/r", Agent: "/a"}))
	}

	projects, err := store.ListProjects()
	require.NoError(t, err)
	require.Len(t, projects, 3)
	assert.Equal(t, "a", projects[0].ID)
	assert.Equal(t, "b", projects[1].ID)
	assert.Equal(t, "c", projects[2].ID)
}

func TestDeleteProject(t *testing.T) {
	store := openTestStore(t)

	require.NoError(t, store.InsertProject(&Project{ID: "p1", Name: "P", RepoPath: "/r", Agent: "/a"}))
	require.NoError(t, store.DeleteProject("p1"))

	_, err := store.GetProject("p1")
	assert.Error(t, err)
}

func TestReorderProjects(t *testing.T) {
	store := openTestStore(t)

	for _, id := range []string{"a", "b", "c"} {
		require.NoError(t, store.InsertProject(&Project{ID: id, Name: id, RepoPath: "/r", Agent: "/a"}))
	}

	require.NoError(t, store.ReorderProjects([]string{"c", "a", "b"}))

	projects, err := store.ListProjects()
	require.NoError(t, err)
	require.Len(t, projects, 3)
	assert.Equal(t, "c", projects[0].ID)
	assert.Equal(t, "a", projects[1].ID)
	assert.Equal(t, "b", projects[2].ID)
}

// --- Column CRUD ---

func TestSeedAndListColumns(t *testing.T) {
	store := openTestStore(t)

	cols := []Column{
		{ID: "a", Name: "A", Emoji: "🅰️", Prompt: "do A"},
		{ID: "b", Name: "B", Emoji: "🅱️", Prompt: "do B"},
	}
	require.NoError(t, store.SeedColumns(cols))

	got, err := store.ListColumns()
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "a", got[0].ID)
	assert.Equal(t, "do A", got[0].Prompt)
	assert.Equal(t, "b", got[1].ID)
}

func TestSeedColumnsIsIdempotent(t *testing.T) {
	store := openTestStore(t)

	cols := []Column{{ID: "a", Name: "A", Emoji: "🅰️", Prompt: "p"}}
	require.NoError(t, store.SeedColumns(cols))
	require.NoError(t, store.SeedColumns(cols))

	got, err := store.ListColumns()
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestReplaceColumns(t *testing.T) {
	store := openTestStore(t)

	require.NoError(t, store.SeedColumns([]Column{
		{ID: "a", Name: "A", Emoji: "🅰️", Prompt: "old"},
		{ID: "b", Name: "B", Emoji: "🅱️", Prompt: "p"},
	}))

	// Rename a, drop b, add c, and reorder: c before a.
	require.NoError(t, store.ReplaceColumns([]Column{
		{ID: "c", Name: "C", Emoji: "©️", Prompt: "do C"},
		{ID: "a", Name: "Renamed", Emoji: "🔨", Prompt: "new prompt"},
	}))

	cols, err := store.ListColumns()
	require.NoError(t, err)
	require.Len(t, cols, 2)
	assert.Equal(t, "c", cols[0].ID)
	assert.Equal(t, "do C", cols[0].Prompt)
	assert.Equal(t, "a", cols[1].ID)
	assert.Equal(t, "Renamed", cols[1].Name)
	assert.Equal(t, "new prompt", cols[1].Prompt)
}

func TestReplaceColumnsRejectsEmpty(t *testing.T) {
	store := openTestStore(t)

	require.ErrorIs(t, store.ReplaceColumns(nil), errBadInput)
}

func TestReplaceColumnsRejectsDeletingColumnWithCards(t *testing.T) {
	store := openTestStore(t)

	require.NoError(t, store.SeedColumns([]Column{
		{ID: "a", Name: "A", Emoji: "🅰️", Prompt: ""},
		{ID: "b", Name: "B", Emoji: "🅱️", Prompt: ""},
	}))
	require.NoError(t, store.InsertCard(&Card{
		ID: "c1", Title: "t", Column: "b", Status: StatusWaiting,
		Agent: "ag", RepoPath: "rp", Branch: "br", Worktree: "wt", Session: "s",
	}))

	err := store.ReplaceColumns([]Column{{ID: "a", Name: "A", Emoji: "🅰️", Prompt: ""}})
	require.ErrorIs(t, err, errColumnHasCards)

	// The rejected replace must not be persisted.
	cols, err := store.ListColumns()
	require.NoError(t, err)
	assert.Len(t, cols, 2)
}
