package loops

import (
	"cmp"
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
	if err := migrateProjects(ctx, db); err != nil {
		return nil, fmt.Errorf("migrate project runs: %w", err)
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
	loop, err := s.prepareLoop(ctx, loop)
	if err != nil {
		return Loop{}, err
	}
	_, err = s.db.ExecContext(ctx, insertLoopSQL, loop.ID, loop.Name, loop.Description, loop.Criteria, loop.Example, loop.CreatedAt, loop.ProjectID, loop.Mode)
	return loop, err
}

const insertLoopSQL = `INSERT INTO loops (id,name,description,criteria,example,created_at,project_id,mode) VALUES (?,?,?,?,?,?,?,?)`

func (s *Store) prepareLoop(ctx context.Context, loop Loop) (Loop, error) {
	loop.Mode = cmp.Or(loop.Mode, ModeAnalysis)
	if loop.Mode != ModeAnalysis && loop.Mode != ModeProject {
		return Loop{}, fmt.Errorf("%w: invalid execution mode", ErrInvalid)
	}
	loop.Name = strings.TrimSpace(loop.Name)
	loop.Description = strings.TrimSpace(loop.Description)
	loop.Criteria = strings.TrimSpace(loop.Criteria)
	loop.Example = strings.TrimSpace(loop.Example)
	if loop.Name == "" || loop.Description == "" || loop.Criteria == "" {
		return Loop{}, fmt.Errorf("%w: name, task and quality criteria are required", ErrInvalid)
	}
	if len(loop.Name) > 160 || len(loop.Description)+len(loop.Criteria)+len(loop.Example) > 64*1024 {
		return Loop{}, fmt.Errorf("%w: task contract is too large", ErrInvalid)
	}
	if loop.ProjectID != "" {
		if _, err := s.Project(ctx, loop.ProjectID); err != nil {
			return Loop{}, err
		}
	}
	loop.ID, loop.CreatedAt, loop.ActiveVersion = newID(), now(), ""
	return loop, nil
}

// createGenerating saves the loop and its initial job together, or neither.
func (s *Store) createGenerating(ctx context.Context, loop Loop) (Loop, Job, error) {
	loop, err := s.prepareLoop(ctx, loop)
	if err != nil {
		return Loop{}, Job{}, err
	}
	input, err := generationInput(loop)
	if err != nil {
		return Loop{}, Job{}, err
	}
	job := Job{ID: newID(), LoopID: loop.ID, Kind: "generate", Status: "queued", Input: input, CreatedAt: now()}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Loop{}, Job{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, insertLoopSQL, loop.ID, loop.Name, loop.Description, loop.Criteria, loop.Example, loop.CreatedAt, loop.ProjectID, loop.Mode); err != nil {
		return Loop{}, Job{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO loop_jobs (id,loop_id,version_id,kind,status,input,created_at) VALUES (?,?,?,?,?,?,?)`, job.ID, job.LoopID, "", job.Kind, job.Status, job.Input, job.CreatedAt); err != nil {
		return Loop{}, Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Loop{}, Job{}, err
	}
	return loop, job, nil
}

// List returns the newest loops first.
func (s *Store) List(ctx context.Context) ([]Loop, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,description,criteria,example,active_version,created_at,project_id,mode,deleting FROM loops ORDER BY created_at DESC,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	loops := []Loop{}
	for rows.Next() {
		var loop Loop
		if err := rows.Scan(&loop.ID, &loop.Name, &loop.Description, &loop.Criteria, &loop.Example, &loop.ActiveVersion, &loop.CreatedAt, &loop.ProjectID, &loop.Mode, &loop.Deleting); err != nil {
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
	err := s.db.QueryRowContext(ctx, `SELECT id,name,description,criteria,example,active_version,created_at,project_id,mode,deleting FROM loops WHERE id=?`, id).
		Scan(&loop.ID, &loop.Name, &loop.Description, &loop.Criteria, &loop.Example, &loop.ActiveVersion, &loop.CreatedAt, &loop.ProjectID, &loop.Mode, &loop.Deleting)
	if err != nil {
		return Detail{}, notFound(err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,loop_id,number,yaml,rationale,created_at,mode FROM loop_versions WHERE loop_id=? ORDER BY number DESC`, id)
	if err != nil {
		return Detail{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var version Version
		if err := rows.Scan(&version.ID, &version.LoopID, &version.Number, &version.YAML, &version.Rationale, &version.CreatedAt, &version.Mode); err != nil {
			return Detail{}, err
		}
		detail.Versions = append(detail.Versions, version)
	}
	return detail, rows.Err()
}

// AddVersion preserves the previous configuration rather than editing it.
func (s *Store) AddVersion(ctx context.Context, loopID, config, rationale string) (Version, error) {
	return s.addVersion(ctx, loopID, config, rationale, ModeAnalysis)
}

func (s *Store) addVersion(ctx context.Context, loopID, config, rationale, mode string) (Version, error) {
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
	version := Version{ID: newID(), LoopID: loopID, YAML: config, Rationale: strings.TrimSpace(rationale), CreatedAt: now(), Mode: mode}
	// A single statement allocates the number and checks ownership atomically.
	err := s.db.QueryRowContext(ctx, `INSERT INTO loop_versions (id,loop_id,number,yaml,rationale,created_at,mode)
		SELECT ?,id,(SELECT COALESCE(MAX(number),0)+1 FROM loop_versions WHERE loop_id=?),?,?,?,? FROM loops WHERE id=? AND deleting=0 RETURNING number`,
		version.ID, loopID, config, version.Rationale, version.CreatedAt, mode, loopID).Scan(&version.Number)
	return version, notFound(err)
}
