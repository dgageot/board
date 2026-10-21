package board

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jmoiron/sqlx"
)

const projectTaskColumns = "id,loop_id,project_id,project_name,repo_path,agent,task,criteria,prompt,card_id,base,status,error,created_at,version_id"

// GetProjectTask retrieves the durable idempotency record.
func (s *SQLiteStore) GetProjectTask(id string) (*ProjectTask, error) {
	var task ProjectTask
	if err := s.db.Get(&task, "SELECT "+projectTaskColumns+" FROM loop_project_tasks WHERE id=?", id); err != nil {
		return nil, err
	}
	return &task, nil
}

// ProjectTaskForCard identifies reservations that must never be silently relaunched.
func (s *SQLiteStore) ProjectTaskForCard(cardID string) (*ProjectTask, error) {
	var task ProjectTask
	if err := s.db.Get(&task, "SELECT "+projectTaskColumns+" FROM loop_project_tasks WHERE card_id=?", cardID); err != nil {
		return nil, err
	}
	return &task, nil
}

// ReserveProjectTask commits the card and its authorization before launching any agent.
func (s *SQLiteStore) ReserveProjectTask(task *ProjectTask, card *Card) error {
	return s.reserveProjectTask(context.Background(), task, card)
}

func (s *SQLiteStore) reserveProjectTask(ctx context.Context, task *ProjectTask, card *Card) error {
	return runInTxContext(ctx, s.db, func(tx *sqlx.Tx) error {
		var available bool
		if err := tx.GetContext(ctx, &available, `SELECT EXISTS(SELECT 1 FROM loops WHERE id=? AND deleting=0)`, task.LoopID); err != nil {
			return err
		}
		if !available {
			return fmt.Errorf("%w: loop is deleting or missing", errBadInput)
		}
		if task.VersionID != "" {
			var valid bool
			if err := tx.GetContext(ctx, &valid, `SELECT EXISTS(SELECT 1 FROM loops JOIN loop_versions ON loop_versions.loop_id=loops.id WHERE loops.id=? AND loops.mode='project' AND loop_versions.id=? AND loop_versions.mode='project')`, task.LoopID, task.VersionID); err != nil {
				return err
			}
			if !valid {
				return fmt.Errorf("%w: workflow mode or version changed; reopen the run dialog", errBadInput)
			}
		}
		if err := reserveInitialRun(ctx, tx, task); err != nil {
			return err
		}
		if err := tx.GetContext(ctx, &card.Column, "SELECT id FROM columns ORDER BY pos LIMIT 1"); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: no columns configured", errBadInput)
			}
			return err
		}
		if _, err := tx.NamedExecContext(ctx, insertCardSQL, card); err != nil {
			return err
		}
		_, err := tx.NamedExecContext(ctx, `INSERT INTO loop_project_tasks (`+projectTaskColumns+`) VALUES (:id,:loop_id,:project_id,:project_name,:repo_path,:agent,:task,:criteria,:prompt,:card_id,:base,:status,:error,:created_at,:version_id)`, task)
		if err != nil {
			return err
		}
		return reserveReview(ctx, tx, task)
	})
}

// UpdateProjectTask records the launch result without modifying its original contract.
func (s *SQLiteStore) UpdateProjectTask(id, status, message string) error {
	_, err := s.db.Exec("UPDATE loop_project_tasks SET status=?,error=? WHERE id=?", status, message, id)
	return err
}

// ListProjectTasks preserves provenance even after the corresponding card is deleted.
func (s *SQLiteStore) ListProjectTasks(loopID string) ([]ProjectTask, error) {
	tasks := []ProjectTask{}
	err := s.db.Select(&tasks, "SELECT "+projectTaskColumns+" FROM loop_project_tasks WHERE loop_id=? ORDER BY created_at DESC", loopID)
	return tasks, err
}
