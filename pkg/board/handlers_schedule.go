package board

import (
	"fmt"
	"net/http"

	"github.com/dgageot/board/pkg/git"
)

type scheduleRequest struct {
	Prompt    string `json:"prompt"`
	ProjectID string `json:"projectId,omitempty"`
}

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
	var req scheduleRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, fmt.Errorf("%w: invalid json", errBadInput))
		return
	}
	if req.Prompt == "" {
		writeError(w, fmt.Errorf("%w: prompt required", errBadInput))
		return
	}

	project, _ := b.store.GetProject(req.ProjectID)

	agent := b.config.DefaultAgent
	repoPath := b.config.DefaultRepoPath
	if project != nil {
		agent = project.Agent
		repoPath = project.RepoPath
	}

	title, err := generateTitle(agent, req.Prompt)
	if err != nil {
		writeError(w, fmt.Errorf("generate title: %w", err))
		return
	}

	branch := sanitizeBranch(title)
	wtPath := git.WorktreePath(repoPath, branch)
	sessionName := "board-" + newID()[:8]

	if err := git.CreateWorktree(repoPath, branch, wtPath); err != nil {
		writeError(w, fmt.Errorf("git worktree: %w", err))
		return
	}

	card := &Card{
		ID:       newID(),
		Title:    title,
		Column:   "dev",
		Status:   StatusRunning,
		Agent:    agent,
		RepoPath: repoPath,
		Branch:   branch,
		Worktree: wtPath,
		Session:  sessionName,
	}

	if err := b.sessions.NewSession(sessionName, wtPath, agent, req.Prompt); err != nil {
		git.RemoveWorktree(repoPath, wtPath, branch)
		writeError(w, fmt.Errorf("tmux session: %w", err))
		return
	}

	if err := b.store.InsertCard(card); err != nil {
		_ = b.sessions.KillSession(sessionName)
		git.RemoveWorktree(repoPath, wtPath, branch)
		writeError(w, fmt.Errorf("insert card: %w", err))
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

	result := make([]scheduleCardResponse, len(cards))
	for i, c := range cards {
		result[i] = toScheduleCard(c)
	}
	writeJSON(w, result)
}
