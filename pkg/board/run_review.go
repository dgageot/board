package board

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"time"
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
	Usage          map[string]float64 `json:"usage,omitempty"`
	Events         []json.RawMessage  `json:"events,omitempty"`
	EventBytes     int                `json:"eventBytes,omitempty"`
	Truncated      bool               `json:"truncated,omitempty"`
}

// RunAssessment is a judge opinion, not a substitute for observed tests or CI.
type RunAssessment struct {
	Quality      string   `json:"quality"`
	Efficiency   string   `json:"efficiency"`
	ChangeImpact string   `json:"changeImpact"`
	Summary      string   `json:"summary"`
	Improvements []string `json:"improvements"`
}

func (r *RunReview) observe(root string, seq uint64, raw []byte) error {
	if seq == 0 || seq <= r.LastSeq || r.FinishedAt != "" {
		return nil
	}
	var event struct {
		Type      string    `json:"type"`
		SessionID string    `json:"session_id"`
		Timestamp time.Time `json:"timestamp"`
		Reason    string    `json:"reason"`
		Usage     *struct {
			Cost float64 `json:"cost"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return err
	}
	r.LastSeq = seq
	if event.Type == "gap" {
		r.Complete = false
		r.Truncated = true
		return nil
	}
	if r.StartedAt == "" {
		if event.Type != "stream_started" || event.SessionID != root || event.Timestamp.IsZero() {
			return nil
		}
		r.StartedAt = event.Timestamp.UTC().Format(time.RFC3339Nano)
		r.Complete = !r.Truncated
		r.Outcome = "running"
		r.OpenStreams = map[string]int{}
		r.Usage = map[string]float64{}
	}
	if event.Type == "stream_started" && event.SessionID != "" {
		r.OpenStreams[event.SessionID]++
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
	if len(r.OpenStreams) > 0 {
		r.Complete = false
	}
	var cost float64
	priced := len(r.Usage) > 0
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
	case "stream_started", "stream_stopped", "agent_choice", "tool_call", "tool_call_response", "token_usage", "error", "budget_exceeded", "warning":
		return true
	default:
		return false
	}
}

func validAssessment(a RunAssessment) error {
	if !containsValue(a.Quality, "satisfied", "failed", "unknown") || !containsValue(a.Efficiency, "efficient", "mixed", "inefficient", "unknown") || !containsValue(a.ChangeImpact, "helped", "hurt", "inconclusive") || strings.TrimSpace(a.Summary) == "" || len(a.Summary) > 8000 || len(a.Improvements) > 8 {
		return errors.New("invalid run assessment")
	}
	for _, item := range a.Improvements {
		if len(item) > 2000 {
			return errors.New("assessment suggestion too long")
		}
	}
	return nil
}

func containsValue(value string, values ...string) bool { return slices.Contains(values, value) }
