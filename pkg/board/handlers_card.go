package board

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"

	"github.com/dgageot/board/pkg/git"
)

// getCard retrieves a card by path parameter. It writes a 404 when the card
// is missing and a 500 for any other store error.
func (b *Board) getCard(w http.ResponseWriter, r *http.Request) (*Card, bool) {
	id := r.PathValue("id")
	card, err := b.store.GetCard(id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, fmt.Errorf("%w: card %s", errNotFound, id))
		return nil, false
	case err != nil:
		writeError(w, fmt.Errorf("get card %s: %w", id, err))
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

// resolveProject returns the name, agent and repo path configured for the
// given project. Cards are always created against a saved project, so a
// missing or unknown project is a caller error; any other store failure is
// internal and reported as such.
func (b *Board) resolveProject(projectID string) (name, agent, repoPath string, err error) {
	if projectID == "" {
		return "", "", "", fmt.Errorf("%w: project required", errBadInput)
	}
	project, err := b.store.GetProject(projectID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", "", "", fmt.Errorf("%w: unknown project %q", errBadInput, projectID)
	case err != nil:
		return "", "", "", fmt.Errorf("get project %q: %w", projectID, err)
	}
	return project.Name, project.Agent, project.RepoPath, nil
}

// createCard creates a new card and launches its agent session. docker agent
// creates the isolated git worktree (named after the card) on first launch and
// exposes its control plane on a per-card unix socket; the board records where
// the worktree lives and starts watching the session. The title is left as a
// placeholder derived from the prompt and replaced when the agent emits its
// session_title event, so card creation is instant.
func (b *Board) createCard(prompt, projectID string) (card *Card, err error) {
	name, agent, repoPath, err := b.resolveProject(projectID)
	if err != nil {
		return nil, err
	}

	// Fail fast when the board has no columns, before any session or worktree
	// exists. The card's actual column is assigned atomically at insert time
	// (InsertCardInFirstColumn), so this check is only an early exit.
	cols, err := b.store.ListColumns()
	if err != nil {
		return nil, fmt.Errorf("list columns: %w", err)
	}
	if len(cols) == 0 {
		return nil, errors.New("no columns configured")
	}

	worktreeName := newWorktreeName()
	branch := git.WorktreeBranch(worktreeName)
	wtPath := git.WorktreeDir(worktreeName)
	sessionName := newSessionName()
	agentSession := newAgentSessionID()

	defer func() {
		if err != nil {
			_ = b.sessions.KillSession(sessionName)
			git.RemoveWorktree(repoPath, wtPath, branch)
		}
	}()

	// Launch from the repository: --worktree branches the new worktree from the
	// repo's upstream base (detected, not assumed — see git.UpstreamBase).
	worktreeBase := git.UpstreamBase(repoPath)
	if err := b.sessions.NewSession(sessionName, repoPath, agent, agentSession, socketPath(agentSession), worktreeName, worktreeBase, prompt); err != nil {
		return nil, fmt.Errorf("tmux session: %w", err)
	}

	card = &Card{
		ID:           newID(),
		Title:        placeholderTitle(prompt),
		Prompt:       prompt,
		Status:       StatusStarting,
		Project:      name,
		Agent:        agent,
		RepoPath:     repoPath,
		Branch:       branch,
		Worktree:     wtPath,
		Session:      sessionName,
		AgentSession: agentSession,
	}

	// The first column is resolved inside the insert transaction: a
	// concurrent column replace cannot leave the card in a deleted column.
	if err := b.store.InsertCardInFirstColumn(card); err != nil {
		return nil, fmt.Errorf("insert card: %w", err)
	}

	// The launch carries the initial prompt: the first turn is imminent, so
	// the watcher must hold the card at "starting" until stream_started
	// instead of flashing green when the control plane first answers.
	b.controller.ExpectTurn(card.ID)
	b.controller.Start(card)
	b.broadcast()
	return card, nil
}

// readCreateCardRequest parses and validates a createCardRequest from an HTTP request.
func readCreateCardRequest(w http.ResponseWriter, r *http.Request) (*createCardRequest, bool) {
	var req createCardRequest
	if !decodeJSON(w, r, &req) {
		return nil, false
	}
	if req.Prompt == "" {
		writeError(w, fmt.Errorf("%w: prompt required", errBadInput))
		return nil, false
	}
	return &req, true
}

func (b *Board) handleCreateCard(w http.ResponseWriter, r *http.Request) {
	req, ok := readCreateCardRequest(w, r)
	if !ok {
		return
	}

	card, err := b.createCard(req.Prompt, req.ProjectID)
	if err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, card)
}

func (b *Board) handleMoveCard(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Column string `json:"column"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	cols, err := b.store.ListColumns()
	if err != nil {
		writeError(w, fmt.Errorf("list columns: %w", err))
		return
	}

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
	// A card whose column is unknown (only possible via a hand-edited DB;
	// store-level checks keep cards in existing columns) has srcIdx -1, so any
	// move counts as forward: idle is required and the prompt is delivered — a
	// safe recovery path back onto the board.
	movedForward := dstIdx > srcIdx

	// A move never changes the card's status: the color tracks the agent's
	// activity, not the move. A running card cannot move forward; the check is
	// enforced atomically by the store so a watcher flipping the status
	// concurrently cannot slip past it.
	if err := b.controller.MoveCardToColumn(card, req.Column, movedForward); err != nil {
		writeError(w, err)
		return
	}

	// The move is persisted: let every client see it before attempting prompt
	// delivery, whose failure must not hide the move.
	b.broadcast()

	if movedForward {
		if err := b.controller.SendPrompt(card, columnPrompt(cols, req.Column)); err != nil {
			writeError(w, err)
			return
		}
	}

	writeJSON(w, card)
}

func (b *Board) handleJumpCard(w http.ResponseWriter, r *http.Request) {
	card, ok := b.getCard(w, r)
	if !ok {
		return
	}

	// While the card is still starting, only attach a terminal once the
	// agent's control plane answers; before that the session shows the bare
	// docker-agent launch command. The probe gates nothing else: once the
	// card has left "starting" its UI has been up, and the tmux session stays
	// attachable (remain-on-exit) even if the control plane later stops
	// answering — e.g. its socket vanished — so probing then would only lock
	// the user out of a live session.
	if card.Status == StatusStarting && !b.controller.Ready(card) {
		writeError(w, errAgentStarting)
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
		writeError(w, err)
		return
	}

	writeJSON(w, map[string]string{"diff": diff})
}

// handlePRStatus reports the merge/CI status of a card's pull request, fetched
// live from GitHub via `gh`. The board loads it per card on demand (on every
// reload, no auto-refresh), so it is a plain read that never mutates state.
// A card with no PR, or any lookup failure, yields an empty status, which the
// frontend renders as no icon.
func (b *Board) handlePRStatus(w http.ResponseWriter, r *http.Request) {
	card, ok := b.getCard(w, r)
	if !ok {
		return
	}

	info, err := git.PRStatus(r.Context(), card.Worktree, card.PRURL)
	if err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, info)
}

// deleteCardResources stops watching the card and cleans up its session and
// worktree. The harness coach started for the card, if any, is killed too: it
// only exists to review that card.
func (b *Board) deleteCardResources(card *Card) {
	b.controller.Stop(card.ID)
	_ = b.sessions.KillSession(card.Session)
	_ = b.sessions.KillSession(coachSessionName(card.ID))
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

// placeholderTitle is the short temporary title shown until the agent emits
// its session_title event. It is the prompt's first line, trimmed and cut to a
// few words so a long prompt never becomes an unwieldy card title.
func placeholderTitle(prompt string) string {
	title := prompt
	if i := strings.IndexByte(title, '\n'); i >= 0 {
		title = title[:i]
	}
	title = strings.TrimSpace(title)

	const maxLen = 40
	runes := []rune(title)
	if len(runes) <= maxLen {
		return title
	}

	cut := string(runes[:maxLen])
	// Prefer a word boundary so the title does not end mid-word.
	if i := strings.LastIndexByte(cut, ' '); i > 0 {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut) + "…"
}

func (b *Board) handleOpenVSCode(w http.ResponseWriter, r *http.Request) {
	card, ok := b.getCard(w, r)
	if !ok {
		return
	}

	cmd := exec.Command(b.config.EditorCommand, card.Worktree)
	if err := cmd.Start(); err != nil {
		writeError(w, fmt.Errorf("open editor: %w", err))
		return
	}
	// Reap the editor when it exits so it does not linger as a zombie.
	go func() { _ = cmd.Wait() }()

	w.WriteHeader(http.StatusNoContent)
}

func (b *Board) handleClearColumn(w http.ResponseWriter, r *http.Request) {
	column := r.PathValue("column")

	cards, err := b.store.ListCardsByColumn(column)
	if err != nil {
		writeError(w, fmt.Errorf("list cards: %w", err))
		return
	}

	var errs []error
	for _, card := range cards {
		if err := b.store.DeleteCard(card.ID); err != nil {
			errs = append(errs, fmt.Errorf("delete card %s: %w", card.ID, err))
			continue
		}
		b.deleteCardResources(card)
	}

	b.broadcast()

	if err := errors.Join(errs...); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
