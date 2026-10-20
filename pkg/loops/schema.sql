CREATE TABLE IF NOT EXISTS loops (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL,
    criteria TEXT NOT NULL,
    example TEXT NOT NULL,
    active_version TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS loop_versions (
    id TEXT PRIMARY KEY,
    loop_id TEXT NOT NULL REFERENCES loops(id),
    number INTEGER NOT NULL,
    yaml TEXT NOT NULL,
    rationale TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(loop_id, number)
);
CREATE TABLE IF NOT EXISTS loop_jobs (
    id TEXT PRIMARY KEY,
    loop_id TEXT NOT NULL REFERENCES loops(id),
    version_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    status TEXT NOT NULL,
    input TEXT NOT NULL,
    output TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    metrics TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    finished_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS loop_jobs_loop ON loop_jobs(loop_id, created_at);
