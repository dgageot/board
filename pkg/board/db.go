package board

import (
	"embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// SQLiteStore implements Store using a SQLite database.
type SQLiteStore struct {
	db *sqlx.DB
}

func openStore() (*SQLiteStore, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("config dir: %w", err)
	}

	dbDir := filepath.Join(dir, "board")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}

	dbPath := filepath.Join(dbDir, "board.db")
	db, err := sqlx.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return &SQLiteStore{db: db}, nil
}

// Close closes the underlying database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// loadMigrations reads all .sql files from the embedded migrations directory, sorted by filename.
func loadMigrations() ([]string, error) {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}

	slices.SortFunc(entries, func(a, b os.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})

	var migrations []string
	for _, e := range entries {
		data, err := migrationFiles.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		migrations = append(migrations, string(data))
	}

	return migrations, nil
}

// migrate applies pending migrations.
func migrate(db *sqlx.DB) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}

	// Ensure the schema_version table exists.
	db.MustExec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`)

	current := currentVersion(db)

	// Bootstrap: detect pre-migration databases that already have
	// the full schema but no version tracking yet.
	if current == 0 && tableExists(db, "cards") {
		current = detectVersion(db)
		if current > 0 {
			db.MustExec(`INSERT INTO schema_version (version) VALUES (?)`, current)
		}
	}

	for i := current; i < len(migrations); i++ {
		tx, err := db.Beginx()
		if err != nil {
			return fmt.Errorf("migration %d: begin: %w", i+1, err)
		}

		if _, err := tx.Exec(migrations[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}

		if err := setVersion(tx, i+1); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: set version: %w", i+1, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %d: commit: %w", i+1, err)
		}

		log.Printf("applied migration %d", i+1)
	}

	return nil
}

func currentVersion(db *sqlx.DB) int {
	var version int
	if err := db.Get(&version, `SELECT COALESCE(MAX(version), 0) FROM schema_version`); err != nil {
		return 0
	}
	return version
}

func setVersion(tx *sqlx.Tx, version int) error {
	tx.MustExec(`DELETE FROM schema_version`)
	_, err := tx.Exec(`INSERT INTO schema_version (version) VALUES (?)`, version)
	return err
}

func tableExists(db *sqlx.DB, name string) bool {
	var count int
	err := db.Get(&count, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name)
	return err == nil && count > 0
}

func columnExists(db *sqlx.DB, table, column string) bool {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dfltValue *string
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dfltValue, &pk); err != nil {
			return false
		}
		if name == column {
			return true
		}
	}
	return false
}

// detectVersion figures out which migration an existing pre-migration
// database corresponds to by inspecting the schema.
func detectVersion(db *sqlx.DB) int {
	version := 1 // tables exist → at least migration 1
	if columnExists(db, "cards", "auto") {
		version = 2
	}
	return version
}

// --- Cards ---

const cardColumns = "id, title, col, status, auto, agent, repo_path, branch, worktree, session"

func (s *SQLiteStore) ListCards() ([]*Card, error) {
	var cards []*Card
	if err := s.db.Select(&cards, "SELECT "+cardColumns+" FROM cards ORDER BY rowid"); err != nil {
		return nil, err
	}
	return cards, nil
}

func (s *SQLiteStore) GetCard(id string) (*Card, error) {
	var card Card
	if err := s.db.Get(&card, "SELECT "+cardColumns+" FROM cards WHERE id = ?", id); err != nil {
		return nil, err
	}
	return &card, nil
}

func (s *SQLiteStore) InsertCard(c *Card) error {
	_, err := s.db.Exec(
		"INSERT INTO cards (id, title, col, status, auto, agent, repo_path, branch, worktree, session) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		c.ID, c.Title, c.Column, string(c.Status), c.Auto, c.Agent, c.RepoPath, c.Branch, c.Worktree, c.Session,
	)
	return err
}

func (s *SQLiteStore) UpdateCard(c *Card) error {
	_, err := s.db.Exec(
		"UPDATE cards SET title = ?, col = ?, status = ?, auto = ?, agent = ?, repo_path = ?, branch = ?, worktree = ?, session = ? WHERE id = ?",
		c.Title, c.Column, string(c.Status), c.Auto, c.Agent, c.RepoPath, c.Branch, c.Worktree, c.Session, c.ID,
	)
	return err
}

func (s *SQLiteStore) DeleteCard(id string) error {
	_, err := s.db.Exec("DELETE FROM cards WHERE id = ?", id)
	return err
}

func (s *SQLiteStore) ListCardsByColumn(column string) ([]*Card, error) {
	var cards []*Card
	if err := s.db.Select(&cards, "SELECT "+cardColumns+" FROM cards WHERE col = ?", column); err != nil {
		return nil, err
	}
	return cards, nil
}

// ReinsertCard deletes and re-inserts a card so it gets the highest rowid.
func (s *SQLiteStore) ReinsertCard(c *Card) error {
	tx, err := s.db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // best-effort rollback on deferred path

	if _, err := tx.Exec("DELETE FROM cards WHERE id = ?", c.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(
		"INSERT INTO cards (id, title, col, status, auto, agent, repo_path, branch, worktree, session) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		c.ID, c.Title, c.Column, string(c.Status), c.Auto, c.Agent, c.RepoPath, c.Branch, c.Worktree, c.Session,
	); err != nil {
		return err
	}

	return tx.Commit()
}

// --- Projects ---

func (s *SQLiteStore) ListProjects() ([]*Project, error) {
	var projects []*Project
	if err := s.db.Select(&projects, "SELECT id, name, repo_path, agent FROM projects"); err != nil {
		return nil, err
	}
	return projects, nil
}

func (s *SQLiteStore) GetProject(id string) (*Project, error) {
	var project Project
	if err := s.db.Get(&project, "SELECT id, name, repo_path, agent FROM projects WHERE id = ?", id); err != nil {
		return nil, err
	}
	return &project, nil
}

func (s *SQLiteStore) InsertProject(p *Project) error {
	_, err := s.db.Exec(
		"INSERT INTO projects (id, name, repo_path, agent) VALUES (?, ?, ?, ?)",
		p.ID, p.Name, p.RepoPath, p.Agent,
	)
	return err
}

func (s *SQLiteStore) DeleteProject(id string) error {
	_, err := s.db.Exec("DELETE FROM projects WHERE id = ?", id)
	return err
}

// --- Columns ---

func (s *SQLiteStore) ListColumns() ([]Column, error) {
	var cols []Column
	if err := s.db.Select(&cols, "SELECT id, name, emoji, prompt FROM columns ORDER BY pos"); err != nil {
		return nil, err
	}
	return cols, nil
}

func (s *SQLiteStore) SeedColumns(cols []Column) error {
	for i, c := range cols {
		_, err := s.db.Exec(
			"INSERT OR IGNORE INTO columns (id, name, emoji, prompt, pos) VALUES (?, ?, ?, ?, ?)",
			c.ID, c.Name, c.Emoji, c.Prompt, i,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) UpdateColumnPrompt(id, prompt string) error {
	_, err := s.db.Exec("UPDATE columns SET prompt = ? WHERE id = ?", prompt, id)
	return err
}
