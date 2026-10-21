package board

import (
	"bytes"
	"encoding/json"
	"math"
	"time"

	"github.com/dgageot/board/pkg/loops"
)

// RunReview separates observed execution from the untested next-version proposal.
type RunReview struct {
	State          string             `json:"state"`
	StartedAt      string             `json:"startedAt,omitempty"`
	FinishedAt     string             `json:"finishedAt,omitempty"`
	DurationMS     *int64             `json:"durationMs"`
	Cost           *float64           `json:"cost"`
	Outcome        string             `json:"outcome,omitempty"`
	Complete       bool               `json:"complete"`
	Assessment     *RunAssessment     `json:"assessment,omitempty"`
	NextVersion    string             `json:"nextVersion,omitempty"`
	Error          string             `json:"error,omitempty"`
	ExpectedActive string             `json:"expectedActive,omitempty"`
	LastSeq        uint64             `json:"lastSeq,omitempty"`
	OpenStreams    map[string]int     `json:"openStreams,omitempty"`
	SeenSessions   map[string]bool    `json:"seenSessions,omitempty"`
	Background     map[string]bool    `json:"background,omitempty"`
	Usage          map[string]float64 `json:"usage,omitempty"`
	Events         []json.RawMessage  `json:"events,omitempty"`
	EventBytes     int                `json:"eventBytes,omitempty"`
	Unpriced       bool               `json:"unpriced,omitempty"`
	JudgeJobs      []string           `json:"judgeJobs,omitempty"`
	JudgeJob       string             `json:"judgeJob,omitempty"`
	RevisionJobs   []string           `json:"revisionJobs,omitempty"`
	Truncated      bool               `json:"truncated,omitempty"`
}

type RunAssessment = loops.RunAssessment

func (r *RunReview) observe(root string, seq uint64, raw []byte) error {
	if r.FinishedAt != "" {
		return nil
	}
	var event struct {
		Type      string    `json:"type"`
		SessionID string    `json:"session_id"`
		Timestamp time.Time `json:"timestamp"`
		Reason    string    `json:"reason"`
		Running   bool      `json:"running"`
		Budgets   []struct {
			Unpriced bool `json:"unpriced"`
		} `json:"budgets"`
		Usage *struct {
			Cost float64 `json:"cost"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return err
	}
	if event.Type == "gap" {
		r.Complete = false
		r.Truncated = true
		return nil
	}
	if seq == 0 || seq <= r.LastSeq {
		return nil
	}
	if seq > r.LastSeq+1 {
		r.Complete = false
		r.Truncated = true
	}
	r.LastSeq = seq
	if r.StartedAt == "" {
		if event.Type != "stream_started" || event.SessionID != root || event.Timestamp.IsZero() {
			return nil
		}
		r.StartedAt = event.Timestamp.UTC().Format(time.RFC3339Nano)
		r.Complete = !r.Truncated
		r.Outcome = "running"
		r.OpenStreams = map[string]int{}
		r.Usage = map[string]float64{}
		r.SeenSessions = map[string]bool{}
		r.Background = map[string]bool{}
	}
	if event.Type == "stream_started" && event.SessionID != "" {
		r.OpenStreams[event.SessionID]++
		r.SeenSessions[event.SessionID] = true
	}
	if event.Type == "background_agent_status" && event.SessionID != "" {
		r.SeenSessions[event.SessionID] = true
		if event.Running {
			r.Background[event.SessionID] = true
		} else {
			delete(r.Background, event.SessionID)
		}
	}
	if event.Type == "budget_usage" {
		for _, budget := range event.Budgets {
			r.Unpriced = r.Unpriced || budget.Unpriced
		}
	}
	if event.Type == "token_usage" && event.Usage != nil && event.SessionID != "" {
		cost := event.Usage.Cost
		if math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 {
			r.Complete = false
		} else {
			r.Usage[event.SessionID] = cost
		}
	}
	if event.Type == "stream_stopped" && event.SessionID != "" {
		r.OpenStreams[event.SessionID]--
		if r.OpenStreams[event.SessionID] <= 0 {
			delete(r.OpenStreams, event.SessionID)
		}
		if event.SessionID == root && r.OpenStreams[root] == 0 {
			r.finish(event.Reason, event.Timestamp)
		}
	}
	if relevantRunEvent(event.Type) {
		const maxEvent = 8192
		const maxEvidence = 256 * 1024
		if len(raw) > maxEvent || r.EventBytes+len(raw) > maxEvidence {
			r.Truncated = true
		} else {
			r.Events = append(r.Events, bytes.Clone(raw))
			r.EventBytes += len(raw)
		}
	}
	return nil
}

func (r *RunReview) finish(reason string, at time.Time) {
	r.Outcome = reason
	if reason == "" {
		r.Outcome = "unknown"
		r.Complete = false
	}
	start, err := time.Parse(time.RFC3339Nano, r.StartedAt)
	if err == nil && !at.IsZero() && !at.Before(start) {
		r.FinishedAt = at.UTC().Format(time.RFC3339Nano)
		duration := at.Sub(start).Milliseconds()
		r.DurationMS = &duration
	} else {
		r.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		r.Complete = false
	}
	if len(r.OpenStreams) > 0 || len(r.Background) > 0 {
		r.Complete = false
	}
	var cost float64
	priced := len(r.Usage) > 0 && r.Complete && !r.Unpriced
	for session := range r.SeenSessions {
		if _, ok := r.Usage[session]; !ok {
			priced = false
		}
	}
	for _, value := range r.Usage {
		cost += value
		priced = priced && value > 0
	}
	if priced {
		r.Cost = &cost
	}
	r.State = "pending"
}

func relevantRunEvent(kind string) bool {
	switch kind {
	case "stream_started", "stream_stopped", "background_agent_status", "agent_choice", "tool_call", "tool_call_response", "token_usage", "budget_usage", "error", "budget_exceeded", "warning":
		return true
	default:
		return false
	}
}

func validAssessment(a RunAssessment) error { return a.Validate() }
