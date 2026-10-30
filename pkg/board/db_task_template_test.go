package board

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskTemplateCRUD(t *testing.T) {
	store := openTestStore(t)
	templates, err := store.ListTaskTemplates()
	require.NoError(t, err)
	assert.Equal(t, []*TaskTemplate{}, templates, "empty lists must encode as [] rather than null")

	first := &TaskTemplate{ID: "z", Name: "Same name", Prompt: " \nFix {{issue}}\t\n"}
	second := &TaskTemplate{ID: "a", Name: "Same name", Prompt: "Review {{issue}}"}
	require.NoError(t, store.InsertTaskTemplate(first))
	require.NoError(t, store.InsertTaskTemplate(second))
	templates, err = store.ListTaskTemplates()
	require.NoError(t, err)
	assert.Equal(t, []*TaskTemplate{first, second}, templates, "order is insertion order, not ID or name")

	require.Error(t, store.InsertTaskTemplate(&TaskTemplate{ID: first.ID, Name: "Duplicate ID", Prompt: "Wrong"}))
	updated := &TaskTemplate{ID: first.ID, Name: "Updated", Prompt: "\n  Keep {{placeholder}} verbatim.\n"}
	stored, err := store.UpdateTaskTemplate(updated)
	require.NoError(t, err)
	assert.Equal(t, updated, stored)
	assert.NotSame(t, updated, stored)
	stored, err = store.UpdateTaskTemplate(updated)
	require.NoError(t, err, "unchanged updates still return the stored row")
	assert.Equal(t, updated, stored)
	templates, err = store.ListTaskTemplates()
	require.NoError(t, err)
	assert.Equal(t, []*TaskTemplate{updated, second}, templates, "updates must not change row order")

	require.NoError(t, store.DeleteTaskTemplate(first.ID))
	third := &TaskTemplate{ID: "m", Name: "Third", Prompt: "Third prompt"}
	require.NoError(t, store.InsertTaskTemplate(third))
	templates, err = store.ListTaskTemplates()
	require.NoError(t, err)
	assert.Equal(t, []*TaskTemplate{second, third}, templates)
	require.NoError(t, store.DeleteTaskTemplate(second.ID))
	require.NoError(t, store.DeleteTaskTemplate(third.ID))
	templates, err = store.ListTaskTemplates()
	require.NoError(t, err)
	assert.Equal(t, []*TaskTemplate{}, templates)
}

func TestTaskTemplateMissing(t *testing.T) {
	store := openTestStore(t)
	stored, err := store.UpdateTaskTemplate(&TaskTemplate{ID: "missing", Name: "Missing", Prompt: "Do not insert"})
	require.ErrorIs(t, err, sql.ErrNoRows)
	assert.Nil(t, stored)
	require.ErrorIs(t, store.DeleteTaskTemplate("missing"), sql.ErrNoRows)
	templates, err := store.ListTaskTemplates()
	require.NoError(t, err)
	assert.Empty(t, templates)

	template := &TaskTemplate{ID: "deleted", Name: "Deleted", Prompt: "Do not recreate"}
	require.NoError(t, store.InsertTaskTemplate(template))
	require.NoError(t, store.DeleteTaskTemplate(template.ID))
	stored, err = store.UpdateTaskTemplate(template)
	require.ErrorIs(t, err, sql.ErrNoRows)
	assert.Nil(t, stored)
	require.ErrorIs(t, store.DeleteTaskTemplate(template.ID), sql.ErrNoRows)
}

func TestTaskTemplateStoreErrors(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.Close())
	templates, err := store.ListTaskTemplates()
	require.Error(t, err)
	assert.Nil(t, templates)
	template := &TaskTemplate{ID: "id", Name: "Name", Prompt: "Prompt"}
	require.Error(t, store.InsertTaskTemplate(template))
	stored, err := store.UpdateTaskTemplate(template)
	require.Error(t, err)
	assert.Nil(t, stored)
	require.Error(t, store.DeleteTaskTemplate(template.ID))
}

func TestTaskTemplateMigrationAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.db")
	db, err := sqlx.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// Build the previous release's database before applying migration 014.
	migrations, err := loadMigrations()
	require.NoError(t, err)
	for _, migration := range migrations {
		if migration.version >= 14 {
			break
		}
		data, err := migrationFiles.ReadFile("migrations/" + migration.name)
		require.NoError(t, err)
		_, err = db.Exec(string(data))
		require.NoError(t, err)
	}
	_, err = db.Exec("CREATE TABLE schema_version (version INTEGER NOT NULL); INSERT INTO schema_version VALUES (13)")
	require.NoError(t, err)
	assert.False(t, tableExists(db, "task_templates"))
	store := &SQLiteStore{db: db}
	card := &Card{
		ID: "existing", Title: "Existing card", Prompt: "Existing prompt", TitleGenerated: true,
		Column: "dev", Status: StatusWaiting, Project: "project", Agent: "/agent", RepoPath: "/repo",
		Branch: "branch", Worktree: "worktree", Session: "session", AgentSession: "agent-session",
		Cost: 1.5, PRURL: "https://example.com/pull/1", CoachRan: true,
	}
	require.NoError(t, store.InsertCard(card))
	project := &Project{ID: "project", Name: "Project", RepoPath: "/repo", Agent: "/agent"}
	require.NoError(t, store.InsertProject(project))
	require.NoError(t, store.SeedColumns(defaultColumns))

	require.NoError(t, migrate(db))
	version, err := currentVersion(db)
	require.NoError(t, err)
	assert.Equal(t, 14, version)
	templates, err := store.ListTaskTemplates()
	require.NoError(t, err)
	assert.Equal(t, []*TaskTemplate{}, templates)
	first := &TaskTemplate{ID: "z", Name: "First", Prompt: "\n{{issue}}\n"}
	second := &TaskTemplate{ID: "a", Name: "Second", Prompt: "Review"}
	require.NoError(t, store.InsertTaskTemplate(first))
	require.NoError(t, store.InsertTaskTemplate(second))
	first.Name = "Renamed"
	_, err = store.UpdateTaskTemplate(first)
	require.NoError(t, err)
	deleted := &TaskTemplate{ID: "deleted", Name: "Deleted", Prompt: "Deleted"}
	require.NoError(t, store.InsertTaskTemplate(deleted))
	require.NoError(t, store.DeleteTaskTemplate(deleted.ID))
	require.NoError(t, store.Close())

	reopened, err := sqlx.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	require.NoError(t, migrate(reopened), "restart must not reapply migration 014")
	store = &SQLiteStore{db: reopened}
	templates, err = store.ListTaskTemplates()
	require.NoError(t, err)
	assert.Equal(t, []*TaskTemplate{first, second}, templates)
	cards, err := store.ListCards()
	require.NoError(t, err)
	assert.Equal(t, []*Card{card}, cards)
	projects, err := store.ListProjects()
	require.NoError(t, err)
	assert.Equal(t, []*Project{project}, projects)
	columns, err := store.ListColumns()
	require.NoError(t, err)
	assert.Equal(t, defaultColumns, columns)
}
