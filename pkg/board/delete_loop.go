package board

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/dgageot/board/pkg/loops"
)

func (b *Board) handleDeleteLoop(w http.ResponseWriter, r *http.Request) {
	store, ok := b.store.(*SQLiteStore)
	if !ok || b.loopService == nil {
		http.Error(w, "loop deletion unavailable", http.StatusServiceUnavailable)
		return
	}
	b.taskLaunch.Lock()
	defer b.taskLaunch.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	id := r.PathValue("id")
	if err := store.beginLoopDelete(ctx, id); err != nil {
		if errors.Is(err, loops.ErrNotFound) {
			http.NotFound(w, r)
		} else {
			writeError(w, err)
		}
		return
	}
	b.broadcast()
	defer b.broadcast()
	if err := b.loopService.StopLoop(ctx, id); err != nil {
		writeError(w, fmt.Errorf("stop loop; retry deletion: %w", err))
		return
	}
	tasks, err := store.ListProjectTasks(id)
	if err != nil {
		writeError(w, err)
		return
	}
	for _, task := range tasks {
		if task.Status == "closed" {
			continue
		}
		b.controller.Stop(task.CardID)
		if err := b.controller.finishTaskClose(store, &task); err != nil {
			writeError(w, fmt.Errorf("stop task; retry deletion: %w", err))
			return
		}
	}
	if err := store.finishLoopDelete(ctx, id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *SQLiteStore) beginLoopDelete(ctx context.Context, id string) error {
	return runInTx(s.db, func(tx *sqlx.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE loops SET deleting=1 WHERE id=?`, id)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return loops.ErrNotFound
		}
		_, err = tx.ExecContext(ctx, `UPDATE loop_project_tasks SET status='closing' WHERE loop_id=? AND status!='closed'`, id)
		return err
	})
}

func (s *SQLiteStore) finishLoopDelete(ctx context.Context, id string) error {
	return runInTx(s.db, func(tx *sqlx.Tx) error {
		var safe bool
		if err := tx.GetContext(ctx, &safe, `SELECT EXISTS(SELECT 1 FROM loops WHERE id=? AND deleting=1) AND NOT EXISTS(SELECT 1 FROM loop_project_tasks WHERE loop_id=? AND status!='closed')`, id, id); err != nil {
			return err
		}
		if !safe {
			return errors.New("loop cleanup is incomplete; retry deletion")
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM loop_run_reviews WHERE task_id IN (SELECT id FROM loop_project_tasks WHERE loop_id=?)`, id); err != nil {
			return err
		}
		for _, table := range []string{"loop_initial_runs", "loop_feedback", "loop_rounds", "loop_jobs", "loop_versions"} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE loop_id=?`, id); err != nil {
				return err
			}
		}
		// Retain closed task provenance for Board's cards, pinned diff base and no-replay guard.
		_, err := tx.ExecContext(ctx, `DELETE FROM loops WHERE id=? AND deleting=1`, id)
		return err
	})
}
