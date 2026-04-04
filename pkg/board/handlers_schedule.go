package board

import (
	"fmt"
	"net/http"
)

type scheduleCardResponse struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Column   string `json:"column"`
	Running  bool   `json:"running"`
	Branch   string `json:"branch"`
	Worktree string `json:"worktree"`
	RepoPath string `json:"repoPath"`
	Auto     bool   `json:"auto"`
}

func toScheduleCard(c *Card) scheduleCardResponse {
	return scheduleCardResponse{
		ID:       c.ID,
		Title:    c.Title,
		Column:   c.Column,
		Running:  c.Status == StatusRunning,
		Branch:   c.Branch,
		Worktree: c.Worktree,
		RepoPath: c.RepoPath,
		Auto:     c.Auto,
	}
}

func (b *Board) handleScheduleCard(w http.ResponseWriter, r *http.Request) {
	var req createCardRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, fmt.Errorf("%w: invalid json", errBadInput))
		return
	}
	if req.Prompt == "" {
		writeError(w, fmt.Errorf("%w: prompt required", errBadInput))
		return
	}

	card, err := b.createCard(req.Prompt, req.ProjectID)
	if err != nil {
		writeError(w, err)
		return
	}

	b.broadcast()

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, toScheduleCard(card))
}

func (b *Board) handleListSchedule(w http.ResponseWriter, _ *http.Request) {
	cards, err := b.store.ListCards()
	if err != nil {
		writeError(w, fmt.Errorf("list cards: %w", err))
		return
	}

	var result []scheduleCardResponse
	for _, c := range cards {
		result = append(result, toScheduleCard(c))
	}
	writeJSON(w, result)
}
