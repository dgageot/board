package board

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"text/template"
)

// coachAgentConfig is the agent the board runs to review a card's session: a
// docker-agent expert on a capable model, carrying the docker-agent feature
// set in its instruction. It is embedded so the board ships its own coach;
// BOARD_COACH_AGENT points at a different config.
//
//go:embed coach.yaml
var coachAgentConfig []byte

//go:embed coach.md
var coachPromptSource string

// coachPromptTmpl renders the first message sent to the coach: what the card
// was, the pipeline it went through, and where to find the transcript.
var coachPromptTmpl = template.Must(template.New("coach").Parse(coachPromptSource))

// coachSessionName is the tmux session a card's coach runs in. Derived from
// the card id so a second click reattaches to the running coach instead of
// starting a second one.
func coachSessionName(cardID string) string {
	return sessionPrefix + "coach-" + cardID
}

// coachAgentSessionID is the docker-agent session id of a card's coach.
// Derived from the card's own session id, so a coach relaunched after its tmux
// session died resumes the review it had already started.
func coachAgentSessionID(agentSession string) string {
	return "coach-" + agentSession
}

// coachTranscriptPath is where a card's session transcript is staged for its
// coach to read. Derived from the (unique) docker-agent session id, so a new
// review overwrites its own file instead of piling up new ones.
func coachTranscriptPath(agentSession string) string {
	return filepath.Join(os.TempDir(), "board-transcript-"+agentSession+".json")
}

// handleCoachCard starts (or reattaches to) the harness coach for a card and
// answers with the tmux session to attach a terminal to.
func (b *Board) handleCoachCard(w http.ResponseWriter, r *http.Request) {
	card, ok := b.getCard(w, r)
	if !ok {
		return
	}

	session, err := b.startCoach(r.Context(), card)
	if err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, map[string]string{"session": session})
}

// startCoach launches the coach for a card and returns its tmux session name.
// The coach is a second agent, run from the card's worktree so it can read the
// repo and the agent config the card used, and handed the card's session
// transcript (exported from the control plane) to review.
//
// A coach already running for the card is reused: clicking the button again
// reattaches to the ongoing review — and to its conversation, so follow-up
// questions work — instead of starting over.
func (b *Board) startCoach(ctx context.Context, card *Card) (string, error) {
	name := coachSessionName(card.ID)
	if alive, err := b.sessions.Alive(name); err == nil && alive {
		return name, nil
	}

	transcript, err := b.controller.Transcript(ctx, card)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errAgentUnreachable, err)
	}
	transcriptPath := coachTranscriptPath(card.AgentSession)
	if err := os.WriteFile(transcriptPath, transcript, 0o600); err != nil {
		return "", fmt.Errorf("write transcript: %w", err)
	}

	// The column prompts are part of the harness the coach reviews.
	cols, err := b.store.ListColumns()
	if err != nil {
		return "", fmt.Errorf("list columns: %w", err)
	}

	prompt, err := coachPrompt(card, cols, transcriptPath)
	if err != nil {
		return "", err
	}

	agentConfig, err := b.coachAgentConfigPath()
	if err != nil {
		return "", err
	}

	// A dead session under the same name, or the socket a previous coach left
	// behind, would block the new run: clear both before launching.
	agentSession := coachAgentSessionID(card.AgentSession)
	socket := socketPath(agentSession)
	_ = b.sessions.KillSession(name)
	_ = os.Remove(socket)

	// No worktree arguments: the coach reviews the card's existing worktree,
	// it does not branch one of its own.
	if err := b.sessions.NewSession(name, card.Worktree, agentConfig, agentSession, socket, "", "", prompt); err != nil {
		return "", fmt.Errorf("coach session: %w", err)
	}

	return name, nil
}

// coachAgentConfigPath returns the path of the agent config the coach runs.
// BOARD_COACH_AGENT wins; otherwise the embedded default is written to the
// temp dir on every launch, so an upgraded board always runs its own coach.
func (b *Board) coachAgentConfigPath() (string, error) {
	if b.config.CoachAgent != "" {
		return b.config.CoachAgent, nil
	}
	path := filepath.Join(os.TempDir(), "board-coach.yaml")
	if err := os.WriteFile(path, coachAgentConfig, 0o600); err != nil {
		return "", fmt.Errorf("write coach agent config: %w", err)
	}
	return path, nil
}

// coachPromptData is what [coachPromptTmpl] renders.
type coachPromptData struct {
	Card       *Card
	Columns    []Column
	Transcript string
}

// coachPrompt renders the coach's first message.
func coachPrompt(card *Card, cols []Column, transcriptPath string) (string, error) {
	var buf bytes.Buffer
	data := coachPromptData{Card: card, Columns: cols, Transcript: transcriptPath}
	if err := coachPromptTmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render coach prompt: %w", err)
	}
	return buf.String(), nil
}
