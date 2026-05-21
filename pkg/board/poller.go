package board

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"
)

// Poller monitors tmux panes for activity changes.
type Poller struct {
	store       Store
	sessions    SessionManager
	onChanged   func()
	mu          sync.Mutex
	lastContent map[string]string // card ID -> last captured pane content
	stableCount map[string]int    // card ID -> consecutive unchanged polls
}

func newPoller(store Store, sessions SessionManager, onChanged func()) *Poller {
	return &Poller{
		store:       store,
		sessions:    sessions,
		onChanged:   onChanged,
		lastContent: make(map[string]string),
		stableCount: make(map[string]int),
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
	p.mu.Lock()
	for _, card := range cards {
		if card.Status == StatusDone {
			continue
		}

		content, err := p.sessions.PaneContent(card.Session)
		if err != nil {
			continue
		}

		prev := p.lastContent[card.ID]
		p.lastContent[card.ID] = content

		switch {
		case prev != "" && prev == content:
			p.stableCount[card.ID]++
			if card.Status == StatusRunning && p.stableCount[card.ID] >= stableThreshold {
				transitions[card.ID] = StatusWaiting
			}
		default:
			p.stableCount[card.ID] = 0
			if card.Status == StatusWaiting {
				transitions[card.ID] = StatusRunning
			}
		}
	}
	p.mu.Unlock()

	// Phase 2: apply transitions without the lock. We update only the status
	// column so a concurrent move-card handler that just changed the row's
	// column is not silently reverted by our stale snapshot.
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

// ResetCard clears the cached pane content for a card.
func (p *Poller) ResetCard(cardID string) {
	p.mu.Lock()
	delete(p.lastContent, cardID)
	delete(p.stableCount, cardID)
	p.mu.Unlock()
}

// SendPromptToCard sends a prompt to the card's tmux session.
// If the session is dead, it creates a new one.
func (p *Poller) SendPromptToCard(card *Card, prompt string) error {
	if prompt == "" {
		return nil
	}

	if err := p.sessions.SendKeys(card.Session, prompt); err != nil {
		sessionName := newSessionName()
		if err := p.sessions.NewSession(sessionName, card.Worktree, card.Agent, prompt); err != nil {
			return fmt.Errorf("tmux: %w", err)
		}
		card.Session = sessionName
		_ = p.store.UpdateCard(card)
	}

	return nil
}
