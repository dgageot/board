CREATE TABLE IF NOT EXISTS loop_run_reviews (
    task_id TEXT PRIMARY KEY,
    state TEXT NOT NULL DEFAULT 'observing',
    data TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS loop_run_reviews_state ON loop_run_reviews(state);
