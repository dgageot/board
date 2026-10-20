package loops

import (
	"context"
	"fmt"
)

const (
	ModeAnalysis = "analysis"
	ModeProject  = "project"
)

// SetMode is an explicit workflow choice, never inferred from untrusted output.
func (s *Store) SetMode(ctx context.Context, id, mode string) error {
	if mode != ModeAnalysis && mode != ModeProject {
		return fmt.Errorf("%w: choose analysis or project execution", ErrInvalid)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE loops SET active_version=CASE WHEN mode=? THEN active_version ELSE '' END,mode=? WHERE id=? AND NOT EXISTS(SELECT 1 FROM loop_jobs WHERE loop_id=? AND status IN ('queued','running')) AND NOT EXISTS(SELECT 1 FROM loop_rounds WHERE loop_id=? AND status IN ('designing','testing'))`, mode, mode, id, id, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		if _, lookup := s.Get(ctx, id); lookup != nil {
			return lookup
		}
		return fmt.Errorf("%w: wait for active work before switching execution type", ErrConflict)
	}
	return err
}

// ProjectInstruction returns a validated prompt artifact, not executable generated YAML.
func ProjectInstruction(version Version) (string, error) {
	config, err := parseAgent(version.YAML)
	if err != nil {
		return "", err
	}
	return config.Agents["root"].Instruction, nil
}

// ActivateProjectVersion selects a reviewed workflow, not a claim of benchmark quality.
func (s *Store) ActivateProjectVersion(ctx context.Context, id, versionID, expected string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE loops SET active_version=? WHERE id=? AND mode='project' AND active_version=? AND EXISTS(SELECT 1 FROM loop_versions WHERE id=? AND loop_id=? AND mode='project')`, versionID, id, expected, versionID, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return ErrConflict
	}
	return err
}
