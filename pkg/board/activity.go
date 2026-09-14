package board

import (
	"context"
	"errors"
	"log"
	"sync"

	"github.com/dgageot/board/pkg/agent"
)

// cardActivity is the sole status writer for a watcher. Live activity owns
// running/paused/idle; the event stream supplies turn outcomes, not counters
// that can permanently drift when a start or stop is dropped.
type cardActivity struct {
	mu          sync.Mutex
	controller  *Controller
	cardID      string
	rootStatus  CardStatus
	known       bool
	unavailable bool
	sessions    []agent.SessionActivity
	generation  uint64
}

func (s *cardActivity) setRootStatus(status CardStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rootStatus = status
	s.apply()
}

func (s *cardActivity) relaunched() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generation++
	s.rootStatus = StatusStarting
	s.known = false
	s.sessions = nil
	s.apply()
	s.controller.setActivityWarning(s.cardID, "")
}

func (s *cardActivity) currentGeneration() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.generation
}

func (s *cardActivity) update(sessions []agent.SessionActivity, err error, generation uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation != s.generation {
		return // The sample belongs to the process that was just replaced.
	}
	s.unavailable = err != nil
	if err == nil {
		s.known = true
		s.sessions = sessions
	}
	status := s.apply()
	warning := ""
	if err != nil && (status != StatusStarting || errors.Is(err, agent.ErrActivityUnsupported)) {
		warning = err.Error()
	}
	s.controller.setActivityWarning(s.cardID, warning)
}

func (s *cardActivity) apply() CardStatus {
	status := s.rootStatus
	if s.known {
		status = activityStatus(s.sessions)
		switch status { //nolint:exhaustive // Only idle and running need turn bookkeeping.
		case StatusWaiting:
			if s.rootStatus == StatusError {
				status = StatusError
			} else if expected, _ := s.controller.turnExpected(s.cardID); expected {
				status = StatusStarting
			}
		case StatusRunning:
			s.controller.setExpectTurn(s.cardID, false)
		}
	}
	// A failed probe is not evidence that every other tab is idle. Preserve
	// known work, but never show a green, movable card on incomplete data.
	if s.unavailable && status != StatusRunning && status != StatusStarting {
		status = StatusUnknown
	}
	s.controller.setStatus(s.cardID, status)
	return status
}

func activityStatus(sessions []agent.SessionActivity) CardStatus {
	status := StatusWaiting
	for _, session := range sessions {
		if !session.Streaming {
			continue
		}
		if !session.Paused {
			return StatusRunning
		}
		status = StatusPaused
	}
	return status
}

func (c *Controller) watchTabActivity(ctx context.Context, client sessionClient, state *cardActivity) {
	for ctx.Err() == nil {
		generation := state.currentGeneration()
		probeCtx, cancel := context.WithTimeout(ctx, readyProbeTimeout)
		sessions, err := client.Activity(probeCtx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		state.update(sessions, err, generation)
		if sleep(ctx) {
			return
		}
	}
}

func (c *Controller) setActivityWarning(cardID, warning string) {
	c.mu.Lock()
	changed := c.activityWarnings[cardID] != warning
	if warning == "" {
		delete(c.activityWarnings, cardID)
	} else {
		c.activityWarnings[cardID] = warning
	}
	c.mu.Unlock()
	if changed {
		if warning != "" {
			log.Printf("card %s: activity unavailable: %s", cardID, warning)
		}
		c.onChanged()
	}
}

func (c *Controller) activityWarning(cardID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.activityWarnings[cardID]
}
