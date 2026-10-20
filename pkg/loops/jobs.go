package loops

import (
	"context"
	"encoding/json"
	"fmt"
)

const jobColumns = "id,loop_id,version_id,kind,status,input,output,error,metrics,created_at,finished_at"

// Recover marks abandoned jobs rather than replaying uncertain work.
func (s *Store) Recover(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE loop_jobs SET status='interrupted',error='Board stopped before this attempt completed. Start a new attempt to retry.',finished_at=? WHERE status IN ('queued','running')`, now())
	return err
}

func (s *Store) newJob(ctx context.Context, loopID, versionID, kind, input string) (Job, error) {
	job := Job{ID: newID(), LoopID: loopID, VersionID: versionID, Kind: kind, Status: "queued", Input: input, CreatedAt: now()}
	_, err := s.db.ExecContext(ctx, `INSERT INTO loop_jobs (id,loop_id,version_id,kind,status,input,created_at) VALUES (?,?,?,?,?,?,?)`,
		job.ID, loopID, versionID, kind, job.Status, input, job.CreatedAt)
	return job, err
}

func (s *Store) saveJob(ctx context.Context, job Job) error {
	metrics, err := json.Marshal(job.Metrics)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE loop_jobs SET status=?,version_id=?,output=?,error=?,metrics=?,finished_at=? WHERE id=?`,
		job.Status, job.VersionID, job.Output, job.Error, string(metrics), job.FinishedAt, job.ID)
	return err
}

type scanner interface{ Scan(dest ...any) error }

func scanJob(row scanner) (Job, error) {
	var job Job
	var metrics string
	if err := row.Scan(&job.ID, &job.LoopID, &job.VersionID, &job.Kind, &job.Status, &job.Input, &job.Output, &job.Error, &metrics, &job.CreatedAt, &job.FinishedAt); err != nil {
		return Job{}, notFound(err)
	}
	if err := json.Unmarshal([]byte(metrics), &job.Metrics); err != nil {
		return Job{}, fmt.Errorf("read job metrics: %w", err)
	}
	return job, nil
}

// Jobs returns completed and in-flight attempts for one loop.
func (s *Store) Jobs(ctx context.Context, loopID string) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+jobColumns+" FROM loop_jobs WHERE loop_id=? ORDER BY created_at DESC,id", loopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []Job{}
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// Job retrieves an attempt only within its owning loop.
func (s *Store) Job(ctx context.Context, loopID, id string) (Job, error) {
	return scanJob(s.db.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM loop_jobs WHERE loop_id=? AND id=?", loopID, id))
}
