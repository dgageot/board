package loops

import "context"

// StopLoop prevents admission and joins this process's active jobs and optimizers.
// The deleting flag persists across restarts until Board finishes cleanup.
func (s *Service) StopLoop(ctx context.Context, loopID string) error {
	s.mu.Lock()
	waiting, err := s.stopLoopLocked(ctx, loopID)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	for _, done := range waiting {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (s *Service) stopLoopLocked(ctx context.Context, loopID string) ([]chan struct{}, error) {
	result, err := s.store.db.ExecContext(ctx, `UPDATE loops SET deleting=1 WHERE id=?`, loopID)
	if err != nil {
		return nil, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ErrNotFound
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT id FROM loop_jobs WHERE loop_id=? UNION SELECT id FROM loop_rounds WHERE loop_id=?`, loopID, loopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var waiting []chan struct{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if cancel := s.cancel[id]; cancel != nil {
			cancel()
		}
		if done := s.running[id]; done != nil {
			waiting = append(waiting, done)
		}
	}
	return waiting, rows.Err()
}
