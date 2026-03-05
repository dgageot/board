package board

import (
	"fmt"
	"log"
	"net/http"

	"github.com/dgageot/board/pkg/git"
)

func (b *Board) handleListCards(w http.ResponseWriter, _ *http.Request) {
	cards, err := b.store.ListCards()
	if err != nil {
		writeError(w, fmt.Errorf("list cards: %w", err))
		return
	}
	writeJSON(w, cards)
}

type createCardRequest struct {
	Title     string `json:"title"`
	Prompt    string `json:"prompt"`
	ProjectID string `json:"projectId"`
}

func (b *Board) handleCreateCard(w http.ResponseWriter, r *http.Request) {
	var req createCardRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, fmt.Errorf("%w: invalid json", errBadInput))
		return
	}
	if req.Title == "" || req.Prompt == "" {
		writeError(w, fmt.Errorf("%w: title and prompt required", errBadInput))
		return
	}

	project, _ := b.store.GetProject(req.ProjectID)

	agent := b.config.DefaultAgent
	repoPath := b.config.DefaultRepoPath
	if project != nil {
		agent = project.Agent
		repoPath = project.RepoPath
	}

	branch := sanitizeBranch(req.Title)
	wtPath := git.WorktreePath(repoPath, branch)
	sessionName := "board-" + newID()[:8]

	if err := git.CreateWorktree(repoPath, branch, wtPath); err != nil {
		writeError(w, fmt.Errorf("git worktree: %w", err))
		return
	}

	card := &Card{
		ID:       newID(),
		Title:    req.Title,
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
	writeJSON(w, card)
}

func (b *Board) handleNextCard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	card, err := b.store.GetCard(id)
	if err != nil {
		writeError(w, fmt.Errorf("%w: card %s", errNotFound, id))
		return
	}

	cols, _ := b.store.ListColumns()
	nextCol := nextColumn(cols, card.Column)
	if nextCol == "" {
		writeError(w, fmt.Errorf("%w: already in last column", errBadInput))
		return
	}

	card.Column = nextCol
	card.Status = StatusRunning
	b.poller.ResetCard(card.ID)

	if err := b.store.ReinsertCard(card); err != nil {
		writeError(w, fmt.Errorf("reinsert card: %w", err))
		return
	}

	if err := sendPromptToCard(b.store, b.sessions, card, columnPrompt(cols, nextCol)); err != nil {
		writeError(w, err)
		return
	}

	b.broadcast()
	writeJSON(w, card)
}

func (b *Board) handleMoveCard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req struct {
		Column string `json:"column"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, fmt.Errorf("%w: invalid json", errBadInput))
		return
	}

	cols, _ := b.store.ListColumns()
	dstIdx := columnIndex(cols, req.Column)
	if dstIdx < 0 {
		writeError(w, fmt.Errorf("%w: invalid column %s", errBadInput, req.Column))
		return
	}

	card, err := b.store.GetCard(id)
	if err != nil {
		writeError(w, fmt.Errorf("%w: card %s", errNotFound, id))
		return
	}

	srcIdx := columnIndex(cols, card.Column)
	movedForward := card.Column != req.Column && dstIdx > srcIdx

	card.Column = req.Column
	card.Status = StatusRunning
	b.poller.ResetCard(card.ID)

	if err := b.store.ReinsertCard(card); err != nil {
		writeError(w, fmt.Errorf("reinsert card: %w", err))
		return
	}

	if movedForward {
		if err := sendPromptToCard(b.store, b.sessions, card, columnPrompt(cols, req.Column)); err != nil {
			writeError(w, err)
			return
		}
	}

	b.broadcast()
	writeJSON(w, card)
}

func (b *Board) handleJumpCard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	card, err := b.store.GetCard(id)
	if err != nil {
		writeError(w, fmt.Errorf("%w: card %s", errNotFound, id))
		return
	}

	writeJSON(w, map[string]string{
		"session": card.Session,
		"command": fmt.Sprintf("tmux attach -t %s", card.Session),
	})
}

func (b *Board) handleDiffCard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	card, err := b.store.GetCard(id)
	if err != nil {
		log.Printf("[diff] card %s: not found: %v", id, err)
		writeError(w, fmt.Errorf("%w: card %s", errNotFound, id))
		return
	}

	log.Printf("[diff] card %s (%s): worktree=%s", card.ID, card.Title, card.Worktree)

	diff, err := git.Diff(card.Worktree)
	if err != nil {
		log.Printf("[diff] card %s: git diff error: %v", card.ID, err)
		writeError(w, fmt.Errorf("git diff: %w", err))
		return
	}

	log.Printf("[diff] card %s: diff length=%d", card.ID, len(diff))

	writeJSON(w, map[string]string{"diff": diff})
}

func (b *Board) handleToggleAutoCard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	card, err := b.store.GetCard(id)
	if err != nil {
		writeError(w, fmt.Errorf("%w: card %s", errNotFound, id))
		return
	}

	card.Auto = !card.Auto
	if err := b.store.UpdateCard(card); err != nil {
		writeError(w, fmt.Errorf("update card: %w", err))
		return
	}

	b.broadcast()
	writeJSON(w, card)
}

func (b *Board) handleDeleteCard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	card, err := b.store.GetCard(id)
	if err != nil {
		writeError(w, fmt.Errorf("%w: card %s", errNotFound, id))
		return
	}

	if err := b.store.DeleteCard(id); err != nil {
		writeError(w, fmt.Errorf("delete card: %w", err))
		return
	}

	b.poller.ResetCard(id)

	_ = b.sessions.KillSession(card.Session)
	git.RemoveWorktree(card.RepoPath, card.Worktree, card.Branch)

	b.broadcast()
	w.WriteHeader(http.StatusNoContent)
}

func (b *Board) handleClearColumn(w http.ResponseWriter, r *http.Request) {
	column := r.PathValue("column")

	cards, err := b.store.ListCardsByColumn(column)
	if err != nil {
		writeError(w, fmt.Errorf("list cards: %w", err))
		return
	}

	for _, card := range cards {
		if err := b.store.DeleteCard(card.ID); err != nil {
			writeError(w, fmt.Errorf("delete card %s: %w", card.ID, err))
			return
		}

		b.poller.ResetCard(card.ID)

		_ = b.sessions.KillSession(card.Session)
		git.RemoveWorktree(card.RepoPath, card.Worktree, card.Branch)
	}

	b.broadcast()
	w.WriteHeader(http.StatusNoContent)
}
