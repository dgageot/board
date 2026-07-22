package board

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
	// _txlock=immediate makes read-then-write transactions (MoveCard,
	// ReplaceColumns) take the write lock up front, so a concurrent writer
	// surfaces as a retryable SQLITE_BUSY (covered by busy_timeout) instead of
	// a non-retryable SQLITE_BUSY_SNAPSHOT on lock upgrade.
	db, err := sqlx.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate")
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

// migration is one embedded migration file and the schema version it brings
// the database to.
type migration struct {
	version int
	name    string
}

// loadMigrations reads the embedded migration files and orders them by the
// version parsed from their filename prefix. Versions are validated to be
// sequential from 1 so a renamed, dropped or duplicated file fails loudly
// instead of silently skipping or re-running migrations.
func loadMigrations() ([]migration, error) {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}

	migrations := make([]migration, 0, len(entries))
	for _, e := range entries {
		version, err := migrationVersion(e.Name())
		if err != nil {
			return nil, err
		}
		migrations = append(migrations, migration{version: version, name: e.Name()})
	}
	slices.SortFunc(migrations, func(a, b migration) int { return a.version - b.version })

	for i, m := range migrations {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration versions must be sequential from 1: unexpected %s", m.name)
		}
	}
	return migrations, nil
}

// migrationVersion parses the numeric version prefix of a migration filename,
// e.g. "002_project_position.sql" → 2.
func migrationVersion(name string) (int, error) {
	prefix, _, _ := strings.Cut(name, "_")
	version, err := strconv.Atoi(prefix)
	if err != nil || version <= 0 {
		return 0, fmt.Errorf("migration %s: name must start with a positive version", name)
	}
	return version, nil
}

// migrate applies pending migrations.
func migrate(db *sqlx.DB) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}

	// Ensure the schema_version table exists.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}

	current, err := currentVersion(db)
	if err != nil {
		return fmt.Errorf("current version: %w", err)
	}

	// Bootstrap: a pre-migration database already has the full schema
	// of migration 1; record that without re-applying it.
	if current == 0 && tableExists(db, "cards") {
		if _, err := db.Exec(`INSERT INTO schema_version (version) VALUES (?)`, 1); err != nil {
			return fmt.Errorf("bootstrap version: %w", err)
		}
		current = 1
	}

	for _, m := range migrations {
		if m.version <= current {
			continue
		}

		data, err := migrationFiles.ReadFile("migrations/" + m.name)
		if err != nil {
			return fmt.Errorf("read %s: %w", m.name, err)
		}

		err = runInTx(db, func(tx *sqlx.Tx) error {
			if _, err := tx.Exec(string(data)); err != nil {
				return err
			}
			return setVersion(tx, m.version)
		})
		if err != nil {
			return fmt.Errorf("migration %d: %w", m.version, err)
		}

		log.Printf("applied migration %d", m.version)
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

func currentVersion(db *sqlx.DB) (int, error) {
	var version int
	if err := db.Get(&version, `SELECT COALESCE(MAX(version), 0) FROM schema_version`); err != nil {
		return 0, err
	}
	return version, nil
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
	cardColumns     = "id, title, prompt, title_generated, col, status, project, agent, repo_path, branch, worktree, session, agent_session, cost, pr_url"
	cardNamedValues = ":id, :title, :prompt, :title_generated, :col, :status, :project, :agent, :repo_path, :branch, :worktree, :session, :agent_session, :cost, :pr_url"
	insertCardSQL   = "INSERT INTO cards (" + cardColumns + ") VALUES (" + cardNamedValues + ")"
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

// InsertCardInFirstColumn inserts the card into the board's first column,
// resolved inside the transaction. Deciding the column at insert time (rather
// than from a snapshot the caller read earlier) closes the race with a
// concurrent ReplaceColumns that removes the column the caller saw: the card
// can never land in a column that no longer exists.
func (s *SQLiteStore) InsertCardInFirstColumn(c *Card) error {
	return runInTx(s.db, func(tx *sqlx.Tx) error {
		if err := tx.Get(&c.Column, "SELECT id FROM columns ORDER BY pos LIMIT 1"); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: no columns configured", errBadInput)
			}
			return err
		}
		_, err := tx.NamedExec(insertCardSQL, c)
		return err
	})
}

// UpdateCardStatus updates only the status column of a card. It is meant for
// background goroutines that hold a stale snapshot of the row (the
// controller's watchers); a full-row update would silently revert
// concurrent edits made by, e.g., the move-card handler.
func (s *SQLiteStore) UpdateCardStatus(id string, status CardStatus) error {
	_, err := s.db.Exec("UPDATE cards SET status = ? WHERE id = ?", status, id)
	return err
}

// UpdateCardTitle updates only the title column of a card. Same rationale as
// [SQLiteStore.UpdateCardStatus]: the controller sets the title from a
// session_title event without reverting concurrent edits. It also marks the
// title as agent-generated, i.e. no longer the placeholder derived from the
// prompt.
func (s *SQLiteStore) UpdateCardTitle(id, title string) error {
	_, err := s.db.Exec("UPDATE cards SET title = ?, title_generated = 1 WHERE id = ?", title, id)
	return err
}

// UpdateCardCost updates only the cost column of a card. Same rationale as
// [SQLiteStore.UpdateCardStatus]: the controller mirrors the session's
// cumulative cost from its snapshot without reverting concurrent edits.
func (s *SQLiteStore) UpdateCardCost(id string, cost float64) error {
	_, err := s.db.Exec("UPDATE cards SET cost = ? WHERE id = ?", cost, id)
	return err
}

// UpdateCardPRURL updates only the pr_url column of a card. Same rationale as
// [SQLiteStore.UpdateCardStatus]: the controller records the discovered pull
// request without reverting concurrent edits.
func (s *SQLiteStore) UpdateCardPRURL(id, prURL string) error {
	_, err := s.db.Exec("UPDATE cards SET pr_url = ? WHERE id = ?", prURL, id)
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

// MoveCard atomically moves a card to the given column, re-inserting it so it
// gets the highest rowid. The row is re-read inside the transaction: the move
// preserves the current status (not a caller's stale snapshot) and, when
// requireIdle is set, a card whose watcher concurrently flipped it to busy
// (starting or running) is rejected with [errCardRunning]. The destination
// column's existence is also checked inside the transaction, so a concurrent
// ReplaceColumns that deletes it cannot leave the card orphaned. The updated
// card is returned.
func (s *SQLiteStore) MoveCard(id, column string, requireIdle bool) (*Card, error) {
	var card Card
	err := runInTx(s.db, func(tx *sqlx.Tx) error {
		var exists bool
		if err := tx.Get(&exists, "SELECT COUNT(*) > 0 FROM columns WHERE id = ?", column); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("%w: unknown column %q", errBadInput, column)
		}
		if err := tx.Get(&card, "SELECT "+cardColumns+" FROM cards WHERE id = ?", id); err != nil {
			return err
		}
		if requireIdle && card.Status.Busy() {
			return errCardRunning
		}
		card.Column = column
		if _, err := tx.Exec("DELETE FROM cards WHERE id = ?", id); err != nil {
			return err
		}
		_, err := tx.NamedExec(insertCardSQL, &card)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &card, nil
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

// ReplaceColumns atomically replaces the whole column set with cols, in
// order. It is rejected with errColumnHasCards when a card still sits in a
// column absent from cols; the check runs inside the transaction so a
// concurrent card move cannot slip past it.
func (s *SQLiteStore) ReplaceColumns(cols []Column) error {
	if len(cols) == 0 {
		return fmt.Errorf("%w: at least one column required", errBadInput)
	}
	ids := make([]string, len(cols))
	for i, c := range cols {
		ids[i] = c.ID
	}
	query, args, err := sqlx.In("SELECT DISTINCT col FROM cards WHERE col NOT IN (?) ORDER BY col", ids)
	if err != nil {
		return err
	}

	return runInTx(s.db, func(tx *sqlx.Tx) error {
		var orphans []string
		if err := tx.Select(&orphans, query, args...); err != nil {
			return err
		}
		if len(orphans) > 0 {
			return fmt.Errorf("%w: %s", errColumnHasCards, strings.Join(orphans, ", "))
		}
		if _, err := tx.Exec("DELETE FROM columns"); err != nil {
			return err
		}
		for i, c := range cols {
			if _, err := tx.Exec(
				"INSERT INTO columns (id, name, emoji, prompt, pos) VALUES (?, ?, ?, ?, ?)",
				c.ID, c.Name, c.Emoji, c.Prompt, i,
			); err != nil {
				return err
			}
		}
		return nil
	})
}
