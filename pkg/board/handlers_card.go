package board

import (
	"cmp"
	"fmt"
	"net/http"
	"os/exec"
	"strings"

	"github.com/dgageot/board/pkg/git"
)

// getCard retrieves a card by path parameter or writes a 404 error.
func (b *Board) getCard(w http.ResponseWriter, r *http.Request) (*Card, bool) {
	id := r.PathValue("id")
	card, err := b.store.GetCard(id)
	if err != nil {
		writeError(w, fmt.Errorf("%w: card %s", errNotFound, id))
		return nil, false
	}
	return card, true
}

func (b *Board) handleListCards(w http.ResponseWriter, _ *http.Request) {
	cards, err := b.store.ListCards()
	if err != nil {
		writeError(w, fmt.Errorf("list cards: %w", err))
		return
	}
	writeJSON(w, cards)
}

type createCardRequest struct {
	Prompt    string `json:"prompt"`
	ProjectID string `json:"projectId,omitempty"`
}

// createCard creates a new card with a worktree and tmux session.
func (b *Board) createCard(prompt, projectID string) (*Card, error) {
	project, _ := b.store.GetProject(projectID)
	if project == nil {
		project = &Project{}
	}

	agent := cmp.Or(project.Agent, b.config.DefaultAgent)
	repoPath := cmp.Or(project.RepoPath, b.config.DefaultRepoPath)

	title, err := generateTitle(agent, prompt)
	if err != nil {
		return nil, fmt.Errorf("generate title: %w", err)
	}

	branch := sanitizeBranch(title)
	wtPath := git.WorktreePath(repoPath, branch)
	sessionName := "board-" + newID()[:8]

	if err := git.CreateWorktree(repoPath, branch, wtPath); err != nil {
		return nil, fmt.Errorf("git worktree: %w", err)
	}

	var retErr error
	defer func() {
		if retErr != nil {
			_ = b.sessions.KillSession(sessionName)
			git.RemoveWorktree(repoPath, wtPath, branch)
		}
	}()

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

	if retErr = b.sessions.NewSession(sessionName, wtPath, agent, prompt); retErr != nil {
		return nil, fmt.Errorf("tmux session: %w", retErr)
	}

	if retErr = b.store.InsertCard(card); retErr != nil {
		return nil, fmt.Errorf("insert card: %w", retErr)
	}

	return card, nil
}

func (b *Board) handleCreateCard(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, card)
}

func (b *Board) handleMoveCard(w http.ResponseWriter, r *http.Request) {
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

	card, ok := b.getCard(w, r)
	if !ok {
		return
	}

	srcIdx := columnIndex(cols, card.Column)
	movedForward := dstIdx > srcIdx && card.Column != req.Column

	if movedForward && card.Status == StatusRunning {
		writeError(w, fmt.Errorf("%w: cannot move a running card forward", errBadInput))
		return
	}

	card.Column = req.Column
	card.Status = StatusRunning

	if movedForward {
		if err := b.poller.MoveCardToColumn(card, req.Column, columnPrompt(cols, req.Column)); err != nil {
			writeError(w, err)
			return
		}
	} else {
		b.poller.ResetCard(card.ID)

		if err := b.store.ReinsertCard(card); err != nil {
			writeError(w, fmt.Errorf("reinsert card: %w", err))
			return
		}
	}

	b.broadcast()
	writeJSON(w, card)
}

func (b *Board) handleJumpCard(w http.ResponseWriter, r *http.Request) {
	card, ok := b.getCard(w, r)
	if !ok {
		return
	}

	writeJSON(w, map[string]string{
		"session": card.Session,
	})
}

func (b *Board) handleDiffCard(w http.ResponseWriter, r *http.Request) {
	card, ok := b.getCard(w, r)
	if !ok {
		return
	}

	diff, err := git.Diff(card.Worktree)
	if err != nil {
		writeError(w, fmt.Errorf("git diff: %w", err))
		return
	}

	writeJSON(w, map[string]string{"diff": diff})
}

func (b *Board) handleToggleAutoCard(w http.ResponseWriter, r *http.Request) {
	card, ok := b.getCard(w, r)
	if !ok {
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

// deleteCardResources cleans up session and worktree for a card.
func (b *Board) deleteCardResources(card *Card) {
	b.poller.ResetCard(card.ID)
	_ = b.sessions.KillSession(card.Session)
	git.RemoveWorktree(card.RepoPath, card.Worktree, card.Branch)
}

func (b *Board) handleDeleteCard(w http.ResponseWriter, r *http.Request) {
	card, ok := b.getCard(w, r)
	if !ok {
		return
	}

	if err := b.store.DeleteCard(card.ID); err != nil {
		writeError(w, fmt.Errorf("delete card: %w", err))
		return
	}

	b.deleteCardResources(card)

	b.broadcast()
	w.WriteHeader(http.StatusNoContent)
}

// generateTitle uses docker agent to generate a short title from a prompt.
func generateTitle(agent, prompt string) (string, error) {
	cmd := exec.Command("docker", "agent", "debug", "title", agent, prompt)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("docker agent debug title: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (b *Board) handleOpenVSCode(w http.ResponseWriter, r *http.Request) {
	card, ok := b.getCard(w, r)
	if !ok {
		return
	}

	if err := exec.Command(b.config.EditorCommand, card.Worktree).Start(); err != nil {
		writeError(w, fmt.Errorf("open editor: %w", err))
		return
	}

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

		b.deleteCardResources(card)
	}

	b.broadcast()
	w.WriteHeader(http.StatusNoContent)
}
