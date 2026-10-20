package loops

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

//go:embed schema.sql
var schema string

// Store persists loop contracts and immutable versions in Board's database.
type Store struct {
	db *sql.DB
}

// NewStore initializes the loop tables without changing Board's schema version.
func NewStore(ctx context.Context, db *sql.DB) (*Store, error) {
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return nil, fmt.Errorf("initialize loops: %w", err)
	}
	return &Store{db: db}, nil
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// Create records the task before any generation or model calls take place.
func (s *Store) Create(ctx context.Context, loop Loop) (Loop, error) {
	loop.Name = strings.TrimSpace(loop.Name)
	loop.Description = strings.TrimSpace(loop.Description)
	loop.Criteria = strings.TrimSpace(loop.Criteria)
	loop.Example = strings.TrimSpace(loop.Example)
	if loop.Name == "" || loop.Description == "" || loop.Criteria == "" || loop.Example == "" {
		return Loop{}, fmt.Errorf("%w: name, task, quality criteria and example input are required", ErrInvalid)
	}
	if len(loop.Name) > 160 || len(loop.Description)+len(loop.Criteria)+len(loop.Example) > 64*1024 {
		return Loop{}, fmt.Errorf("%w: task contract is too large", ErrInvalid)
	}
	loop.ID, loop.CreatedAt, loop.ActiveVersion = newID(), now(), ""
	_, err := s.db.ExecContext(ctx, `INSERT INTO loops (id,name,description,criteria,example,created_at) VALUES (?,?,?,?,?,?)`,
		loop.ID, loop.Name, loop.Description, loop.Criteria, loop.Example, loop.CreatedAt)
	return loop, err
}

// List returns the newest loops first.
func (s *Store) List(ctx context.Context) ([]Loop, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,description,criteria,example,active_version,created_at FROM loops ORDER BY created_at DESC,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	loops := []Loop{}
	for rows.Next() {
		var loop Loop
		if err := rows.Scan(&loop.ID, &loop.Name, &loop.Description, &loop.Criteria, &loop.Example, &loop.ActiveVersion, &loop.CreatedAt); err != nil {
			return nil, err
		}
		loops = append(loops, loop)
	}
	return loops, rows.Err()
}

// Get retrieves one loop and its complete version history.
func (s *Store) Get(ctx context.Context, id string) (Detail, error) {
	detail := Detail{Versions: []Version{}}
	loop := &detail.Loop
	err := s.db.QueryRowContext(ctx, `SELECT id,name,description,criteria,example,active_version,created_at FROM loops WHERE id=?`, id).
		Scan(&loop.ID, &loop.Name, &loop.Description, &loop.Criteria, &loop.Example, &loop.ActiveVersion, &loop.CreatedAt)
	if err != nil {
		return Detail{}, notFound(err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,loop_id,number,yaml,rationale,created_at FROM loop_versions WHERE loop_id=? ORDER BY number DESC`, id)
	if err != nil {
		return Detail{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var version Version
		if err := rows.Scan(&version.ID, &version.LoopID, &version.Number, &version.YAML, &version.Rationale, &version.CreatedAt); err != nil {
			return Detail{}, err
		}
		detail.Versions = append(detail.Versions, version)
	}
	return detail, rows.Err()
}

// AddVersion preserves the previous configuration rather than editing it.
func (s *Store) AddVersion(ctx context.Context, loopID, config, rationale string) (Version, error) {
	if len(config) > 256*1024 || len(rationale) > 16*1024 {
		return Version{}, fmt.Errorf("%w: version is too large", ErrInvalid)
	}
	var document map[string]any
	if err := yaml.Unmarshal([]byte(config), &document); err != nil {
		return Version{}, fmt.Errorf("%w: invalid YAML: %w", ErrInvalid, err)
	}
	agents, ok := document["agents"].(map[string]any)
	if !ok || agents["root"] == nil {
		return Version{}, fmt.Errorf("%w: configuration must define agents.root", ErrInvalid)
	}
	version := Version{ID: newID(), LoopID: loopID, YAML: config, Rationale: strings.TrimSpace(rationale), CreatedAt: now()}
	// A single statement allocates the number and checks ownership atomically.
	err := s.db.QueryRowContext(ctx, `INSERT INTO loop_versions (id,loop_id,number,yaml,rationale,created_at)
		SELECT ?,id,(SELECT COALESCE(MAX(number),0)+1 FROM loop_versions WHERE loop_id=?),?,?,? FROM loops WHERE id=? RETURNING number`,
		version.ID, loopID, config, version.Rationale, version.CreatedAt, loopID).Scan(&version.Number)
	return version, notFound(err)
}
