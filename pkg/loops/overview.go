package loops

import (
	"context"
	"encoding/json"
)

// Overview omits repeated output and file snapshots from polling responses.
type Overview struct {
	Detail

	Jobs     []Job      `json:"jobs"`
	Rounds   []Round    `json:"rounds"`
	Feedback []Feedback `json:"feedback"`
}

func (s *Store) Overview(ctx context.Context, id string) (Overview, error) {
	detail, err := s.Get(ctx, id)
	if err != nil {
		return Overview{}, err
	}
	jobs, err := s.jobSummaries(ctx, id)
	if err != nil {
		return Overview{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT status,json_remove(data,'$.project.files') FROM loop_rounds WHERE loop_id=? ORDER BY rowid DESC`, id)
	if err != nil {
		return Overview{}, err
	}
	defer rows.Close()
	rounds := []Round{}
	for rows.Next() {
		var status, data string
		if err := rows.Scan(&status, &data); err != nil {
			return Overview{}, err
		}
		var round Round
		if err := json.Unmarshal([]byte(data), &round); err != nil {
			return Overview{}, err
		}
		round.Status = status
		rounds = append(rounds, round)
	}
	if err := rows.Err(); err != nil {
		return Overview{}, err
	}
	if err := rows.Close(); err != nil {
		return Overview{}, err
	}
	feedback, err := s.Feedback(ctx, id)
	return Overview{Detail: detail, Jobs: jobs, Rounds: rounds, Feedback: feedback}, err
}

func (s *Store) jobSummaries(ctx context.Context, id string) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,loop_id,version_id,kind,status,'','',error,metrics,created_at,finished_at,json_remove(project,'$.files'),closed_at,length(input),length(output) FROM loop_jobs WHERE loop_id=? ORDER BY created_at DESC,id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []Job{}
	for rows.Next() {
		var job Job
		var metrics, project string
		if err := rows.Scan(&job.ID, &job.LoopID, &job.VersionID, &job.Kind, &job.Status, &job.Input, &job.Output, &job.Error, &metrics, &job.CreatedAt, &job.FinishedAt, &project, &job.ClosedAt, &job.InputSize, &job.OutputSize); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(metrics), &job.Metrics); err != nil {
			return nil, err
		}
		if project != "{}" && project != "null" {
			if err := json.Unmarshal([]byte(project), &job.Project); err != nil {
				return nil, err
			}
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// DisplayJob loads the selected output without sending its project snapshot again.
func (s *Store) DisplayJob(ctx context.Context, loopID, id string) (Job, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,loop_id,version_id,kind,status,input,output,error,metrics,created_at,finished_at,json_remove(project,'$.files'),closed_at FROM loop_jobs WHERE loop_id=? AND id=?`, loopID, id)
	job, err := scanJob(row)
	if err != nil {
		return Job{}, err
	}
	return job, nil
}

// HasActiveRound is a bounded query used while holding the admission lock.
func (s *Store) HasActiveRound(ctx context.Context, loopID string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM loop_rounds WHERE loop_id=? AND status IN ('designing','testing'))`, loopID).Scan(&exists)
	return exists, err
}

func (s *Store) LatestRunInputs(ctx context.Context, loopID, versionID string) ([]Job, error) {
	// Filter and deduplicate in SQL before limiting; repeated retries must not hide older test cases.
	rows, err := s.db.QueryContext(ctx, `SELECT j.id,j.loop_id,j.version_id,j.kind,j.status,j.input,'',j.error,j.metrics,j.created_at,j.finished_at,json_remove(j.project,'$.files'),j.closed_at
 FROM loop_jobs j WHERE j.loop_id=? AND j.version_id=? AND j.kind='run'
 AND json_extract(j.project,'$.id')=(SELECT json_extract(project,'$.id') FROM loop_jobs WHERE loop_id=? AND version_id=? AND kind='run' AND json_extract(project,'$.id') IS NOT NULL ORDER BY created_at DESC,id DESC LIMIT 1)
 AND trim(j.input)!='' AND length(CAST(j.input AS BLOB))<=65536
 AND j.id=(SELECT j2.id FROM loop_jobs j2 WHERE j2.loop_id=j.loop_id AND j2.version_id=j.version_id AND j2.kind='run' AND j2.input=j.input AND json_extract(j2.project,'$.id')=json_extract(j.project,'$.id') ORDER BY j2.created_at DESC,j2.id DESC LIMIT 1)
 ORDER BY j.created_at DESC,j.id DESC`, loopID, versionID, loopID, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []Job{}
	total := 0
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		if total+len(job.Input) > 65536 {
			continue
		}
		jobs = append(jobs, job)
		total += len(job.Input)
		if len(jobs) == 3 {
			break
		}
	}
	return jobs, rows.Err()
}
