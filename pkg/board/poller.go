package board

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"
)

// pollState tracks the per-card sampling state used to detect activity.
type pollState struct {
	lastContent string // last captured pane content
	stableCount int    // consecutive unchanged polls
}

// Poller monitors tmux panes for activity changes.
type Poller struct {
	store     Store
	sessions  SessionManager
	onChanged func()
	mu        sync.Mutex
	states    map[string]*pollState // card ID -> sampling state
}

func newPoller(store Store, sessions SessionManager, onChanged func()) *Poller {
	return &Poller{
		store:     store,
		sessions:  sessions,
		onChanged: onChanged,
		states:    make(map[string]*pollState),
	}
}

// stableThreshold is the number of consecutive unchanged polls
// required before a card transitions from running to waiting.
const stableThreshold = 3

// pollInterval is the base delay between two polls. Each poll adds a
// random jitter up to pollJitter to avoid aliasing with spinners that
// cycle at exact intervals.
const (
	pollInterval = 800 * time.Millisecond
	pollJitter   = 400 * time.Millisecond
)

// Run periodically checks tmux panes for activity changes.
func (p *Poller) Run(ctx context.Context) {
	for {
		jitter := time.Duration(rand.Int64N(int64(pollJitter)))
		select {
		case <-ctx.Done():
			return
		case <-time.After(pollInterval + jitter):
		}

		if p.poll() {
			p.onChanged()
		}
	}
}

func (p *Poller) poll() bool {
	cards, err := p.store.ListCards()
	if err != nil {
		return false
	}

	// Phase 1: read pane content under lock and collect status transitions.
	transitions := map[string]CardStatus{}
	active := map[string]bool{}
	var deadCards []*Card
	p.mu.Lock()
	for _, card := range cards {
		if card.Status == StatusDone {
			continue
		}
		active[card.ID] = true

		content, dead, err := p.sessions.PaneContent(card.Session)
		if err != nil || dead {
			// The session is gone (host rebooted) or the agent pane has died
			// (agent exited). Revive it after releasing the lock, so spawning a
			// subprocess does not block concurrent ResetCard calls.
			deadCards = append(deadCards, card)
			continue
		}

		state, ok := p.states[card.ID]
		if !ok {
			state = &pollState{}
			p.states[card.ID] = state
		}
		prev := state.lastContent
		state.lastContent = content

		if prev != "" && prev == content {
			state.stableCount++
			if card.Status == StatusRunning && state.stableCount >= stableThreshold {
				transitions[card.ID] = StatusWaiting
			}
			continue
		}

		state.stableCount = 0
		if card.Status == StatusWaiting {
			transitions[card.ID] = StatusRunning
		}
	}

	// Prune state for cards that are gone or done, so the map does not
	// grow forever.
	for id := range p.states {
		if !active[id] {
			delete(p.states, id)
		}
	}
	p.mu.Unlock()

	// Phase 2: revive dead sessions and apply transitions without the lock. We
	// update only the status column so a concurrent move-card handler that just
	// changed the row's column is not silently reverted by our stale snapshot.
	for _, card := range deadCards {
		p.reconnect(card)
	}

	changed := false
	for id, status := range transitions {
		if err := p.store.UpdateCardStatus(id, status); err != nil {
			continue
		}
		changed = true
	}

	return changed
}

// MoveCardToColumn moves a card to the given column, resets its poll state,
// reinserts it (for ordering), and sends the column prompt.
func (p *Poller) MoveCardToColumn(card *Card, column, prompt string) error {
	card.Column = column
	card.Status = StatusWaiting
	if prompt != "" {
		card.Status = StatusRunning
	}

	p.ResetCard(card.ID)

	if err := p.store.ReinsertCard(card); err != nil {
		return fmt.Errorf("reinsert card: %w", err)
	}

	return p.SendPromptToCard(card, prompt)
}

// ResetCard clears the cached sampling state for a card.
func (p *Poller) ResetCard(cardID string) {
	p.mu.Lock()
	delete(p.states, cardID)
	p.mu.Unlock()
}

// SendPromptToCard delivers a prompt to the card's agent. If the agent is
// alive the prompt is typed into its TUI. If the session is gone or its pane
// has died, the session is recreated under the same name, resuming the
// docker-agent session with the prompt as the next message (a dead pane
// silently swallows send-keys, so we must not type into one).
func (p *Poller) SendPromptToCard(card *Card, prompt string) error {
	if prompt == "" {
		return nil
	}

	if _, dead, err := p.sessions.PaneContent(card.Session); err == nil && !dead {
		if err := p.sessions.SendKeys(card.Session, prompt); err == nil {
			return nil
		}
	}

	_ = p.sessions.KillSession(card.Session)
	if err := p.sessions.NewSession(card.Session, card.Worktree, card.Agent, card.AgentSession, "", prompt); err != nil {
		return fmt.Errorf("tmux: %w", err)
	}

	return nil
}

// reconnect recreates a card's tmux session under the same name, resuming its
// docker-agent session. Used when the session has died while the card is still
// active, so the conversation continues instead of starting over.
//
// The session resumes from the worktree directory, not the repository: docker
// agent normally reattaches a resumed session to its worktree on its own, but
// launching from the worktree keeps the agent isolated even if that
// reattachment does not happen (e.g. the docker-agent session record is
// missing), instead of letting it run in the user's checkout.
func (p *Poller) reconnect(card *Card) {
	_ = p.sessions.KillSession(card.Session)
	if err := p.sessions.NewSession(card.Session, card.Worktree, card.Agent, card.AgentSession, "", ""); err != nil {
		return
	}
	p.ResetCard(card.ID)
}
