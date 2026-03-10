package board

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"
)

// Poller monitors tmux panes for activity and auto-advances cards.
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

// Run periodically checks tmux panes for activity changes.
// It polls every ~1s with a small random jitter to avoid
// aliasing with spinners that cycle at exact intervals.
func (p *Poller) Run(ctx context.Context) {
	for {
		jitter := time.Duration(rand.IntN(400)) * time.Millisecond
		select {
		case <-ctx.Done():
			return
		case <-time.After(800*time.Millisecond + jitter):
		}

		if p.poll() {
			p.onChanged()
		}
	}
}

// cardTransition describes a status change detected during polling.
type cardTransition struct {
	card        *Card
	newStatus   CardStatus
	autoAdvance bool
}

func (p *Poller) poll() bool {
	cards, err := p.store.ListCards()
	if err != nil {
		return false
	}

	// Phase 1: Read pane content and determine transitions under lock.
	var transitions []cardTransition
	p.mu.Lock()
	for _, card := range cards {
		if card.Status != StatusRunning && card.Status != StatusWaiting {
			continue
		}

		content, err := p.sessions.PaneContent(card.Session)
		if err != nil {
			continue
		}

		prev := p.lastContent[card.ID]
		p.lastContent[card.ID] = content

		if prev != "" && prev == content {
			p.stableCount[card.ID]++

			if card.Status == StatusRunning && p.stableCount[card.ID] >= stableThreshold {
				transitions = append(transitions, cardTransition{
					card:        card,
					newStatus:   StatusWaiting,
					autoAdvance: card.Auto,
				})
			}
		} else {
			p.stableCount[card.ID] = 0

			if card.Status == StatusWaiting {
				transitions = append(transitions, cardTransition{
					card:      card,
					newStatus: StatusRunning,
				})
			}
		}
	}
	p.mu.Unlock()

	// Phase 2: Apply transitions without holding the lock.
	changed := false
	for _, t := range transitions {
		t.card.Status = t.newStatus
		if err := p.store.UpdateCard(t.card); err != nil {
			continue
		}
		changed = true

		if t.autoAdvance {
			if p.autoAdvance(t.card) {
				changed = true
			}
		}
	}

	return changed
}

// autoAdvance moves a card to the next column and sends the column prompt.
func (p *Poller) autoAdvance(card *Card) bool {
	cols, _ := p.store.ListColumns()
	nextCol := nextColumn(cols, card.Column)
	if nextCol == "" {
		return false
	}

	prompt := columnPrompt(cols, nextCol)

	card.Column = nextCol
	if prompt != "" {
		card.Status = StatusRunning
	} else {
		card.Status = StatusWaiting
	}

	p.ResetCard(card.ID)

	if err := p.store.ReinsertCard(card); err != nil {
		return false
	}

	if err := sendPromptToCard(p.store, p.sessions, card, prompt); err != nil {
		return false
	}

	return true
}

// ResetCard clears the cached pane content for a card.
func (p *Poller) ResetCard(cardID string) {
	p.mu.Lock()
	delete(p.lastContent, cardID)
	delete(p.stableCount, cardID)
	p.mu.Unlock()
}

// sendPromptToCard sends a prompt to the card's tmux session.
// If the session is dead, it creates a new one.
func sendPromptToCard(store Store, sessions SessionManager, card *Card, prompt string) error {
	if prompt == "" {
		return nil
	}

	if err := sessions.SendKeys(card.Session, prompt); err != nil {
		sessionName := "board-" + newID()[:8]
		if err := sessions.NewSession(sessionName, card.Worktree, card.Agent, prompt); err != nil {
			return fmt.Errorf("tmux: %w", err)
		}
		card.Session = sessionName
		_ = store.UpdateCard(card)
	}

	return nil
}
