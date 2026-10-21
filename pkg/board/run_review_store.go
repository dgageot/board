package board

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/jmoiron/sqlx"
)

type reviewStore interface {
	ObserveRun(cardID, root string, seq uint64, raw []byte) error
	RunReview(taskID string) (RunReview, error)
}

func (s *SQLiteStore) ObserveRun(cardID, root string, seq uint64, raw []byte) error {
	return runInTx(s.db, func(tx *sqlx.Tx) error {
		var row struct {
			ID    string `db:"task_id"`
			State string `db:"state"`
			Data  string `db:"data"`
		}
		err := tx.Get(&row, `SELECT r.task_id,r.state,r.data FROM loop_run_reviews r JOIN loop_project_tasks t ON t.id=r.task_id WHERE t.card_id=?`, cardID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if row.State != "observing" {
			return nil
		}
		var review RunReview
		if err := json.Unmarshal([]byte(row.Data), &review); err != nil {
			return err
		}
		review.State = row.State
		if seq != 0 && seq <= review.LastSeq {
			return nil
		}
		if err := review.observe(root, seq, raw); err != nil {
			return err
		}
		data, err := json.Marshal(review)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE loop_run_reviews SET state=?,data=? WHERE task_id=?`, review.State, string(data), row.ID)
		return err
	})
}

func (s *SQLiteStore) RunReview(taskID string) (RunReview, error) {
	var state, data string
	if err := s.db.QueryRow(`SELECT state,data FROM loop_run_reviews WHERE task_id=?`, taskID).Scan(&state, &data); err != nil {
		return RunReview{}, err
	}
	var review RunReview
	if err := json.Unmarshal([]byte(data), &review); err != nil {
		return RunReview{}, err
	}
	review.State = state
	return review, nil
}

func reserveReview(tx *sqlx.Tx, task *ProjectTask) error {
	var active string
	if err := tx.QueryRow(`SELECT active_version FROM loops WHERE id=?`, task.LoopID).Scan(&active); err != nil {
		return err
	}
	data, err := json.Marshal(RunReview{State: "observing", ExpectedActive: active})
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO loop_run_reviews(task_id,data) VALUES (?,?)`, task.ID, string(data))
	return err
}

func (c *Controller) observeTaskRun(card *Card, seq uint64, raw []byte) {
	store, ok := c.store.(reviewStore)
	if !ok || len(raw) == 0 {
		return
	}
	if err := store.ObserveRun(card.ID, card.AgentSession, seq, raw); err != nil {
		log.Printf("record run %s: %v", card.ID, err)
	}
}

func (s *SQLiteStore) interruptRunReview(cardID, reason string) error {
	task, err := s.ProjectTaskForCard(cardID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	review, err := s.RunReview(task.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if review.State != "observing" {
		return nil
	}
	review.State = "pending"
	review.Complete = false
	review.Outcome = reason
	review.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(review)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE loop_run_reviews SET state='pending',data=? WHERE task_id=? AND state='observing'`, string(data), task.ID)
	return err
}
