package board

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
)

// finishTaskClose is also used on startup to finish interrupted closure.
func (c *Controller) finishTaskClose(store projectTaskStore, task *ProjectTask) error {
	card, err := c.store.GetCard(task.CardID)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("cannot verify project task termination: its card is missing")
	}
	if err != nil {
		return err
	}
	for _, name := range []string{card.Session, coachSessionName(card.ID)} {
		if err := c.sessions.KillSession(name); err != nil {
			return fmt.Errorf("stop project task session: %w", err)
		}
	}
	if err := c.store.UpdateCardStatus(card.ID, StatusClosed); err != nil {
		return err
	}
	return store.UpdateProjectTask(task.ID, "closed", "")
}

func (b *Board) handleCloseLoopProjectTask(w http.ResponseWriter, r *http.Request) {
	store, ok := b.store.(projectTaskStore)
	if !ok {
		http.Error(w, "project tasks unavailable", http.StatusServiceUnavailable)
		return
	}
	b.taskLaunch.Lock()
	defer b.taskLaunch.Unlock()
	task, err := store.GetProjectTask(r.PathValue("task"))
	if errors.Is(err, sql.ErrNoRows) || err == nil && task.LoopID != r.PathValue("id") {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	if task.Status != "closed" {
		if err := store.UpdateProjectTask(task.ID, "closing", task.Error); err != nil {
			writeError(w, err)
			return
		}
		b.controller.Stop(task.CardID)
		// Keep the card, worktree, commits and conversation; only stop execution.
		if err := b.controller.finishTaskClose(store, task); err != nil {
			b.broadcast()
			writeError(w, err)
			return
		}
	}
	b.broadcast()
	w.WriteHeader(http.StatusNoContent)
}

func (c *Controller) reconcileTaskClose(card *Card) (bool, error) {
	store, ok := c.store.(projectTaskStore)
	if !ok {
		return false, nil
	}
	task, err := store.ProjectTaskForCard(card.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if task.Status == "closing" {
		return true, c.finishTaskClose(store, task)
	}
	return task.Status == "closed", nil
}
