package loops

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Feedback is attached to an exact result, never silently substituted for a test.
type Feedback struct {
	ID        string `json:"id"`
	LoopID    string `json:"loopId"`
	JobID     string `json:"jobId"`
	Rating    string `json:"rating"`
	Note      string `json:"note"`
	CreatedAt string `json:"createdAt"`
}

// Round records the exact suite and candidate set used in an experiment.
type Round struct {
	ID         string           `json:"id"`
	LoopID     string           `json:"loopId"`
	Baseline   string           `json:"baseline"`
	Status     string           `json:"status"`
	Inputs     []string         `json:"inputs"`
	Repeats    int              `json:"repeats"`
	Candidates []Candidate      `json:"candidates"`
	Error      string           `json:"error"`
	CreatedAt  string           `json:"createdAt"`
	Project    *ProjectSnapshot `json:"project,omitempty"`
	Pass       int              `json:"pass,omitempty"`
	MaxPasses  int              `json:"maxPasses,omitempty"`
	Winner     string           `json:"winner,omitempty"`
	Outcome    string           `json:"outcome,omitempty"`
	CheckJob   string           `json:"checkJob,omitempty"`
	Stage      string           `json:"stage,omitempty"`
	Task       string           `json:"task,omitempty"`
	Criteria   string           `json:"criteria,omitempty"`
	JudgeModel string           `json:"judgeModel,omitempty"`
}

// Candidate keeps every attempt, including failures, in the comparison.
type Candidate struct {
	VersionID    string  `json:"versionId"`
	Parent       string  `json:"parent,omitempty"`
	ProposalID   string  `json:"proposalId,omitempty"`
	Decision     string  `json:"decision,omitempty"`
	Pass         int     `json:"pass,omitempty"`
	Label        string  `json:"label"`
	Trials       []Trial `json:"trials"`
	Confirmation []Trial `json:"confirmation,omitempty"`
	Error        string  `json:"error"`
}

// Trial is an execution paired with an independent quality judgment.
type Trial struct {
	JobID   string `json:"jobId"`
	JudgeID string `json:"judgeId"`
	Case    int    `json:"case"`
	Repeat  int    `json:"repeat"`
	Passed  bool   `json:"passed"`
	Reason  string `json:"reason"`
	Outcome string `json:"outcome,omitempty"`
}

// AddFeedback validates ownership and preserves the original user's assessment.
func (s *Store) AddFeedback(ctx context.Context, loopID, jobID, rating, note string) (Feedback, error) {
	job, err := s.Job(ctx, loopID, jobID)
	if err != nil {
		return Feedback{}, err
	}
	if job.Kind != "run" && job.Kind != "test" {
		return Feedback{}, fmt.Errorf("%w: feedback requires an agent execution", ErrInvalid)
	}
	if job.Status == "queued" || job.Status == "running" {
		return Feedback{}, fmt.Errorf("%w: wait for the result before rating it", ErrConflict)
	}
	if rating != "good" && rating != "bad" {
		return Feedback{}, fmt.Errorf("%w: rating must be good or bad", ErrInvalid)
	}
	note = strings.TrimSpace(note)
	if len(note) > 16000 {
		return Feedback{}, fmt.Errorf("%w: feedback is too long", ErrInvalid)
	}
	feedback := Feedback{ID: newID(), LoopID: loopID, JobID: jobID, Rating: rating, Note: note, CreatedAt: now()}
	_, err = s.db.ExecContext(ctx, `INSERT INTO loop_feedback (id,loop_id,job_id,rating,note,created_at) VALUES (?,?,?,?,?,?)`, feedback.ID, loopID, jobID, rating, note, feedback.CreatedAt)
	return feedback, err
}

// Feedback returns the evidence available when designing the next round.
func (s *Store) Feedback(ctx context.Context, loopID string) ([]Feedback, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,loop_id,job_id,rating,note,created_at FROM loop_feedback WHERE loop_id=? ORDER BY created_at DESC LIMIT 100`, loopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Feedback{}
	for rows.Next() {
		var f Feedback
		if err := rows.Scan(&f.ID, &f.LoopID, &f.JobID, &f.Rating, &f.Note, &f.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, f)
	}
	return result, rows.Err()
}

func (s *Store) saveRound(ctx context.Context, round Round) error {
	data, err := json.Marshal(round)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO loop_rounds (id,loop_id,status,data) VALUES (?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,data=excluded.data`, round.ID, round.LoopID, round.Status, string(data))
	return err
}

// Rounds returns the experiment history without reconstructing evidence from chat.
func (s *Store) Rounds(ctx context.Context, loopID string) ([]Round, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status,data FROM loop_rounds WHERE loop_id=? ORDER BY rowid DESC`, loopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Round{}
	for rows.Next() {
		var status, data string
		if err := rows.Scan(&status, &data); err != nil {
			return nil, err
		}
		var round Round
		if err := json.Unmarshal([]byte(data), &round); err != nil {
			return nil, err
		}
		round.Status = status
		result = append(result, round)
	}
	return result, rows.Err()
}

// Promote changes only the active pointer after verifying a complete evidence set.
func (s *Store) Promote(ctx context.Context, loopID, versionID, expectedActive string) error {
	detail, err := s.Get(ctx, loopID)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(detail.Versions, func(v Version) bool { return v.ID == versionID }) {
		return ErrNotFound
	}
	rounds, err := s.Rounds(ctx, loopID)
	if err != nil {
		return err
	}
	qualified := false
	for _, round := range rounds {
		if round.Status != "completed" {
			continue
		}
		for _, candidate := range round.Candidates {
			if candidate.VersionID == versionID && (round.MaxPasses == 0 || round.Winner == versionID) && candidatePassed(round, candidate) && s.persistedEvidence(ctx, round, candidate) && s.confirmedEvidence(ctx, round, candidate) {
				qualified = true
			}
		}
	}
	// The initial baseline may be selected after its first successful inspected run.
	if !qualified && expectedActive == "" && len(detail.Versions) > 0 && detail.Versions[len(detail.Versions)-1].ID == versionID {
		jobs, err := s.Jobs(ctx, loopID)
		if err != nil {
			return err
		}
		qualified = slices.ContainsFunc(jobs, func(j Job) bool { return j.Kind == "run" && j.Status == "succeeded" && j.VersionID == versionID })
	}
	if !qualified {
		return fmt.Errorf("%w: run and inspect the initial version, or complete a passing experiment before promotion", ErrConflict)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE loops SET active_version=? WHERE id=? AND active_version=?`, versionID, loopID, expectedActive)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return ErrConflict
	}
	return err
}

func candidatePassed(round Round, candidate Candidate) bool {
	if candidate.Error != "" || len(candidate.Trials) != len(round.Inputs)*round.Repeats {
		return false
	}
	seen := map[[2]int]bool{}
	for _, trial := range candidate.Trials {
		key := [2]int{trial.Case, trial.Repeat}
		if !trial.Passed || trial.JobID == "" || trial.JudgeID == "" || trial.Case < 0 || trial.Case >= len(round.Inputs) || trial.Repeat < 0 || trial.Repeat >= round.Repeats || seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}

type qualityVerdict struct {
	Passed *bool  `json:"passed"`
	Reason string `json:"reason"`
}

func (s *Store) persistedEvidence(ctx context.Context, round Round, candidate Candidate) bool {
	seenJobs := map[string]bool{}
	for _, trial := range candidate.Trials {
		if seenJobs[trial.JobID] || seenJobs[trial.JudgeID] {
			return false
		}
		seenJobs[trial.JobID], seenJobs[trial.JudgeID] = true, true
		run, err := s.Job(ctx, round.LoopID, trial.JobID)
		if err != nil || !sameProject(run.Project, round.Project) || run.Kind != "test" || run.Status != "succeeded" || run.VersionID != candidate.VersionID || run.Input != round.Inputs[trial.Case] {
			return false
		}
		judge, err := s.Job(ctx, round.LoopID, trial.JudgeID)
		if err != nil || !sameProject(judge.Project, round.Project) || judge.Kind != "judge" || judge.Status != "succeeded" || judge.VersionID != candidate.VersionID {
			return false
		}
		var evidence map[string]string
		if json.Unmarshal([]byte(judge.Input), &evidence) != nil || evidence["input"] != run.Input || evidence["answer"] != run.Output {
			return false
		}
		if round.MaxPasses > 0 && (evidence["task"] != round.Task || evidence["criteria"] != round.Criteria) {
			return false
		}
		var verdict qualityVerdict
		if json.Unmarshal([]byte(judge.Output), &verdict) != nil || verdict.Passed == nil || !*verdict.Passed || strings.TrimSpace(verdict.Reason) == "" {
			return false
		}
	}
	return true
}

func sameProject(a, b *ProjectSnapshot) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.ID == b.ID && a.Path == b.Path && a.Revision == b.Revision && a.Digest == b.Digest
}

func (s *Store) confirmedEvidence(ctx context.Context, round Round, candidate Candidate) bool {
	if round.MaxPasses == 0 || candidate.Pass == 0 {
		return true
	}
	confirmation := Candidate{VersionID: candidate.VersionID, Trials: candidate.Confirmation}
	return candidatePassed(round, confirmation) && s.persistedEvidence(ctx, round, confirmation)
}
