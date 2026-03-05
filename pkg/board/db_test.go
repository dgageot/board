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
	assert.True(t, columnExists(db, "cards", "auto"))
}

func TestMigrateIsIdempotent(t *testing.T) {
	db := openTestDB(t)

	require.NoError(t, migrate(db))
	require.NoError(t, migrate(db))

	assert.Equal(t, 2, currentVersion(db))
}

func TestDetectVersionWithAutoColumn(t *testing.T) {
	db := openTestDB(t)

	assert.Equal(t, 2, detectVersion(db))
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

func TestUpdateCard(t *testing.T) {
	store := openTestStore(t)

	card := &Card{
		ID: "card-1", Title: "Old", Column: "dev", Status: StatusRunning,
		Agent: "ag", RepoPath: "rp", Branch: "br", Worktree: "wt", Session: "s1",
	}
	require.NoError(t, store.InsertCard(card))

	card.Title = "New"
	card.Status = StatusWaiting
	card.Auto = true
	require.NoError(t, store.UpdateCard(card))

	got, err := store.GetCard("card-1")
	require.NoError(t, err)
	assert.Equal(t, "New", got.Title)
	assert.Equal(t, StatusWaiting, got.Status)
	assert.True(t, got.Auto)
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

func TestReinsertCardMovesToEnd(t *testing.T) {
	store := openTestStore(t)

	for _, id := range []string{"a", "b", "c"} {
		require.NoError(t, store.InsertCard(&Card{
			ID: id, Title: id, Column: "dev", Status: StatusRunning,
			Agent: "ag", RepoPath: "rp", Branch: "br", Worktree: "wt", Session: "s-" + id,
		}))
	}

	card, err := store.GetCard("a")
	require.NoError(t, err)
	card.Column = "review"
	require.NoError(t, store.ReinsertCard(card))

	cards, err := store.ListCards()
	require.NoError(t, err)
	require.Len(t, cards, 3)
	assert.Equal(t, "b", cards[0].ID)
	assert.Equal(t, "c", cards[1].ID)
	assert.Equal(t, "a", cards[2].ID)
	assert.Equal(t, "review", cards[2].Column)
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

func TestDeleteProject(t *testing.T) {
	store := openTestStore(t)

	require.NoError(t, store.InsertProject(&Project{ID: "p1", Name: "P", RepoPath: "/r", Agent: "/a"}))
	require.NoError(t, store.DeleteProject("p1"))

	_, err := store.GetProject("p1")
	assert.Error(t, err)
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

func TestUpdateColumnPrompt(t *testing.T) {
	store := openTestStore(t)

	require.NoError(t, store.SeedColumns([]Column{
		{ID: "a", Name: "A", Emoji: "🅰️", Prompt: "old"},
	}))

	require.NoError(t, store.UpdateColumnPrompt("a", "new prompt"))

	cols, err := store.ListColumns()
	require.NoError(t, err)
	require.Len(t, cols, 1)
	assert.Equal(t, "new prompt", cols[0].Prompt)
}
