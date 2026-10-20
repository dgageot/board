package loops

import (
	"context"
	"encoding/json"
	"unicode/utf8"
)

const maxDesignEvidence = 192 * 1024

type runEvidence struct {
	JobID     string  `json:"jobId"`
	VersionID string  `json:"versionId"`
	Input     string  `json:"input"`
	Output    string  `json:"output"`
	Error     string  `json:"error,omitempty"`
	Metrics   Metrics `json:"metrics"`
}

type feedbackEvidence struct {
	Rating string      `json:"rating"`
	Note   string      `json:"note"`
	Run    runEvidence `json:"run"`
}

func clipText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	for !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit] + "… [truncated]"
}

func projectRun(job Job) runEvidence {
	return runEvidence{JobID: job.ID, VersionID: job.VersionID, Input: clipText(job.Input, 2048), Output: clipText(job.Output, 4096), Error: clipText(job.Error, 512), Metrics: job.Metrics}
}

func (s *Store) designEvidence(ctx context.Context, loop Loop, baseline Version) ([]byte, error) {
	feedback, err := s.Feedback(ctx, loop.ID)
	if err != nil {
		return nil, err
	}
	evidence := struct {
		Task     Loop               `json:"task"`
		Baseline Version            `json:"baseline"`
		Feedback []feedbackEvidence `json:"feedback"`
		Runs     []runEvidence      `json:"recentRuns"`
	}{Task: loop, Baseline: baseline, Feedback: []feedbackEvidence{}, Runs: []runEvidence{}}
	seen := map[string]bool{}
	for _, f := range feedback {
		if len(evidence.Feedback) == 4 {
			break
		}
		job, err := s.Job(ctx, loop.ID, f.JobID)
		if err != nil {
			return nil, err
		}
		evidence.Feedback = append(evidence.Feedback, feedbackEvidence{Rating: f.Rating, Note: clipText(f.Note, 2048), Run: projectRun(job)})
		seen[job.ID] = true
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+jobColumns+" FROM loop_jobs WHERE loop_id=? AND kind IN ('run','test') ORDER BY created_at DESC LIMIT 8", loop.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		if !seen[job.ID] && len(evidence.Runs) < 4 {
			evidence.Runs = append(evidence.Runs, projectRun(job))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Baseline YAML is bounded independently, so imported documents cannot bloat context.
	evidence.Baseline.YAML = clipText(baseline.YAML, 48000)
	evidence.Baseline.Rationale = clipText(baseline.Rationale, 2048)
	data, err := json.Marshal(evidence)
	if err != nil {
		return nil, err
	}
	if len(data) > maxDesignEvidence {
		// Preserve the task contract and baseline; optional evidence is discarded first.
		evidence.Runs = nil
		evidence.Feedback = nil
		return json.Marshal(evidence)
	}
	return data, nil
}
