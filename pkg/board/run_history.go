package board

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
)

// A user may retry preparation, but the recorded task execution is never rerun.
func (b *Board) handleRetryRunLearning(w http.ResponseWriter, r *http.Request) {
	store, ok := b.store.(*SQLiteStore)
	if !ok {
		http.Error(w, "run learning unavailable", http.StatusServiceUnavailable)
		return
	}
	task, err := store.GetProjectTask(r.PathValue("task"))
	if errors.Is(err, sql.ErrNoRows) || err == nil && task.LoopID != r.PathValue("id") {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	review, err := store.RunReview(task.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	if review.FinishedAt == "" {
		http.Error(w, "run has no recorded completion", http.StatusConflict)
		return
	}
	if review.State != "failed" && review.State != "interrupted" && review.State != "revision_failed" {
		http.Error(w, "preparation is not retryable", http.StatusConflict)
		return
	}
	result, err := store.db.Exec(`UPDATE loop_run_reviews SET state='pending' WHERE task_id=? AND state IN ('failed','interrupted','revision_failed')`, task.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	n, err := result.RowsAffected()
	if err != nil {
		writeError(w, err)
		return
	}
	if n == 0 {
		http.Error(w, "preparation changed", http.StatusConflict)
		return
	}
	b.broadcast()
	writeJSONStatus(w, http.StatusAccepted, map[string]string{"state": "pending"})
}

func (b *Board) handleRunEvidence(w http.ResponseWriter, r *http.Request) {
	store, ok := b.store.(*SQLiteStore)
	if !ok {
		http.NotFound(w, r)
		return
	}
	task, err := store.GetProjectTask(r.PathValue("task"))
	if errors.Is(err, sql.ErrNoRows) || err == nil && task.LoopID != r.PathValue("id") {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	review, err := store.RunReview(task.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="run-evidence.json"`)
	if err := json.NewEncoder(w).Encode(review); err != nil {
		return
	}
}
