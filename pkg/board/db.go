package board

import (
	"embed"
	"fmt"
	"log"
	"os"
	"path/filepath"

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
	db, err := sqlx.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
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

// migrate applies pending migrations.
func migrate(db *sqlx.DB) error {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	// Ensure the schema_version table exists.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}

	current := currentVersion(db)

	// Bootstrap: a pre-migration database already has the full schema
	// of migration 1; record that without re-applying it.
	if current == 0 && tableExists(db, "cards") {
		if _, err := db.Exec(`INSERT INTO schema_version (version) VALUES (?)`, 1); err != nil {
			return fmt.Errorf("bootstrap version: %w", err)
		}
		current = 1
	}

	for i := current; i < len(entries); i++ {
		data, err := migrationFiles.ReadFile("migrations/" + entries[i].Name())
		if err != nil {
			return fmt.Errorf("read %s: %w", entries[i].Name(), err)
		}

		err = runInTx(db, func(tx *sqlx.Tx) error {
			if _, err := tx.Exec(string(data)); err != nil {
				return err
			}
			return setVersion(tx, i+1)
		})
		if err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}

		log.Printf("applied migration %d", i+1)
	}

	return nil
}

// runInTx runs fn inside a transaction, committing on success and rolling
// back on error.
func runInTx(db *sqlx.DB, fn func(*sqlx.Tx) error) error {
	tx, err := db.Beginx()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func currentVersion(db *sqlx.DB) int {
	var version int
	if err := db.Get(&version, `SELECT COALESCE(MAX(version), 0) FROM schema_version`); err != nil {
		return 0
	}
	return version
}

func setVersion(tx *sqlx.Tx, version int) error {
	if _, err := tx.Exec(`DELETE FROM schema_version`); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO schema_version (version) VALUES (?)`, version)
	return err
}

func tableExists(db *sqlx.DB, name string) bool {
	var count int
	err := db.Get(&count, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name)
	return err == nil && count > 0
}

// --- Cards ---

const (
	cardColumns     = "id, title, col, status, agent, repo_path, branch, worktree, session, agent_session"
	cardNamedValues = ":id, :title, :col, :status, :agent, :repo_path, :branch, :worktree, :session, :agent_session"
	insertCardSQL   = "INSERT INTO cards (" + cardColumns + ") VALUES (" + cardNamedValues + ")"
	updateCardSQL   = `UPDATE cards SET
		title = :title, col = :col, status = :status, agent = :agent,
		repo_path = :repo_path, branch = :branch, worktree = :worktree, session = :session,
		agent_session = :agent_session
		WHERE id = :id`
)

func (s *SQLiteStore) ListCards() ([]*Card, error) {
	cards := []*Card{}
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
	_, err := s.db.NamedExec(insertCardSQL, c)
	return err
}

func (s *SQLiteStore) UpdateCard(c *Card) error {
	_, err := s.db.NamedExec(updateCardSQL, c)
	return err
}

// UpdateCardStatus updates only the status column of a card. It is meant for
// background goroutines that hold a stale snapshot of the row (the poller
// and the remote-session watcher); a full [UpdateCard] would silently revert
// concurrent edits made by, e.g., the move-card handler.
func (s *SQLiteStore) UpdateCardStatus(id string, status CardStatus) error {
	_, err := s.db.Exec("UPDATE cards SET status = ? WHERE id = ?", status, id)
	return err
}

// UpdateCardSession updates only the session column of a card. Same rationale
// as [SQLiteStore.UpdateCardStatus].
func (s *SQLiteStore) UpdateCardSession(id, session string) error {
	_, err := s.db.Exec("UPDATE cards SET session = ? WHERE id = ?", session, id)
	return err
}

func (s *SQLiteStore) DeleteCard(id string) error {
	_, err := s.db.Exec("DELETE FROM cards WHERE id = ?", id)
	return err
}

func (s *SQLiteStore) ListCardsByColumn(column string) ([]*Card, error) {
	cards := []*Card{}
	if err := s.db.Select(&cards, "SELECT "+cardColumns+" FROM cards WHERE col = ?", column); err != nil {
		return nil, err
	}
	return cards, nil
}

// ReinsertCard deletes and re-inserts a card so it gets the highest rowid.
func (s *SQLiteStore) ReinsertCard(c *Card) error {
	return runInTx(s.db, func(tx *sqlx.Tx) error {
		if _, err := tx.Exec("DELETE FROM cards WHERE id = ?", c.ID); err != nil {
			return err
		}
		_, err := tx.NamedExec(insertCardSQL, c)
		return err
	})
}

// --- Projects ---

const (
	projectColumns   = "id, name, repo_path, agent, pos"
	insertProjectSQL = "INSERT INTO projects (" + projectColumns + ") VALUES (:id, :name, :repo_path, :agent, :pos)"
)

func (s *SQLiteStore) ListProjects() ([]*Project, error) {
	projects := []*Project{}
	if err := s.db.Select(&projects, "SELECT "+projectColumns+" FROM projects ORDER BY pos"); err != nil {
		return nil, err
	}
	return projects, nil
}

func (s *SQLiteStore) GetProject(id string) (*Project, error) {
	var project Project
	if err := s.db.Get(&project, "SELECT "+projectColumns+" FROM projects WHERE id = ?", id); err != nil {
		return nil, err
	}
	return &project, nil
}

// InsertProject appends a project at the end of the ordered list.
func (s *SQLiteStore) InsertProject(p *Project) error {
	if err := s.db.Get(&p.Pos, "SELECT COALESCE(MAX(pos), -1) + 1 FROM projects"); err != nil {
		return err
	}
	_, err := s.db.NamedExec(insertProjectSQL, p)
	return err
}

func (s *SQLiteStore) DeleteProject(id string) error {
	_, err := s.db.Exec("DELETE FROM projects WHERE id = ?", id)
	return err
}

// ReorderProjects persists the given project order by assigning each id its
// index as the position. Unknown ids are ignored.
func (s *SQLiteStore) ReorderProjects(ids []string) error {
	return runInTx(s.db, func(tx *sqlx.Tx) error {
		for i, id := range ids {
			if _, err := tx.Exec("UPDATE projects SET pos = ? WHERE id = ?", i, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// --- Columns ---

const columnColumns = "id, name, emoji, prompt"

func (s *SQLiteStore) ListColumns() ([]Column, error) {
	cols := []Column{}
	if err := s.db.Select(&cols, "SELECT "+columnColumns+" FROM columns ORDER BY pos"); err != nil {
		return nil, err
	}
	return cols, nil
}

func (s *SQLiteStore) SeedColumns(cols []Column) error {
	return runInTx(s.db, func(tx *sqlx.Tx) error {
		for i, c := range cols {
			if _, err := tx.Exec(
				"INSERT OR IGNORE INTO columns (id, name, emoji, prompt, pos) VALUES (?, ?, ?, ?, ?)",
				c.ID, c.Name, c.Emoji, c.Prompt, i,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *SQLiteStore) UpdateColumnPrompt(id, prompt string) error {
	_, err := s.db.Exec("UPDATE columns SET prompt = ? WHERE id = ?", prompt, id)
	return err
}
