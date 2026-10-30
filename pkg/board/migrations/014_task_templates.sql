CREATE TABLE IF NOT EXISTS task_templates (
    id     TEXT PRIMARY KEY,
    name   TEXT NOT NULL,
    prompt TEXT NOT NULL
);
