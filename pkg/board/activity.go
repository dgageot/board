package board

import (
	"context"
	"errors"
	"sync"

	"github.com/dgageot/board/pkg/agent"
)

// cardActivity is the sole status writer for a watcher. Live activity owns
// running/paused/idle; the event stream supplies turn outcomes, not counters
// that can permanently drift when a start or stop is dropped.
type cardActivity struct {
	mu          sync.Mutex
	probeMu     sync.Mutex
	controller  *Controller
	cardID      string
	rootStatus  CardStatus
	rootSession string
	known       bool
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
	if err != nil {
		// Failed probes never change the displayed status. New root events
		// still apply, while known work in other tabs remains visible.
		s.known = false
		if errors.Is(err, agent.ErrActivityUnsupported) {
			s.sessions = nil
			s.apply()
		}
		return
	}
	s.known = true
	s.sessions = sessions
	s.apply()
}

func (s *cardActivity) apply() {
	s.controller.setStatus(s.cardID, s.status())
}

func (s *cardActivity) status() CardStatus {
	// Empty legacy listings are unsupported, never authoritative idle.
	if !s.known || legacyActivity(s.sessions) {
		return s.legacyStatus()
	}
	status := activityStatus(s.sessions)
	switch status { //nolint:exhaustive // Only idle and running need turn bookkeeping.
	case StatusWaiting:
		if s.rootStatus == StatusError {
			return StatusError
		}
		if expected, _ := s.controller.turnExpected(s.cardID); expected {
			return StatusStarting
		}
	case StatusRunning:
		s.controller.setExpectTurn(s.cardID, false)
	}
	return status
}

func (s *cardActivity) legacyStatus() CardStatus {
	// Legacy listings miss fork skills and pause state. They can add
	// evidence of work, but cannot declare an event-driven turn idle.
	for _, session := range s.sessions {
		if session.ID == s.rootSession && (!s.known || s.rootStatus == StatusPaused || s.rootStatus == StatusError) {
			continue
		}
		if session.Streaming && !session.Paused {
			s.controller.setExpectTurn(s.cardID, false)
			return StatusRunning
		}
	}
	return s.rootStatus
}

func legacyActivity(sessions []agent.SessionActivity) bool {
	for _, session := range sessions {
		if session.Legacy {
			return true
		}
	}
	return false
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

// setEventStatus refreshes legacy tabs before publishing a root stop, error,
// or pause. A sibling may have started since the last periodic sample.
func (s *cardActivity) setEventStatus(ctx context.Context, client sessionClient, status CardStatus) {
	s.mu.Lock()
	legacy := !s.known || legacyActivity(s.sessions)
	s.mu.Unlock()
	if status != StatusRunning && legacy {
		s.refresh(ctx, client)
	}
	s.setRootStatus(status)
}

func (s *cardActivity) refresh(ctx context.Context, client sessionClient) {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	generation := s.currentGeneration()
	probeCtx, cancel := context.WithTimeout(ctx, readyProbeTimeout)
	sessions, err := client.Activity(probeCtx)
	cancel()
	if ctx.Err() == nil {
		s.update(sessions, err, generation)
	}
}

func (c *Controller) watchTabActivity(ctx context.Context, client sessionClient, state *cardActivity) {
	for ctx.Err() == nil {
		state.refresh(ctx, client)
		if sleep(ctx) {
			return
		}
	}
}
