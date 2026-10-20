CREATE TABLE IF NOT EXISTS loop_project_tasks (
    id TEXT PRIMARY KEY,
    loop_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    project_name TEXT NOT NULL,
    repo_path TEXT NOT NULL,
    agent TEXT NOT NULL,
    task TEXT NOT NULL,
    criteria TEXT NOT NULL,
    prompt TEXT NOT NULL,
    card_id TEXT NOT NULL UNIQUE,
    base TEXT NOT NULL,
    status TEXT NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS loop_project_tasks_loop ON loop_project_tasks(loop_id, created_at);
