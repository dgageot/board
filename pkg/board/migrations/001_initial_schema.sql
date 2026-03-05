CREATE TABLE IF NOT EXISTS cards (
    id        TEXT PRIMARY KEY,
    title     TEXT NOT NULL,
    col       TEXT NOT NULL,
    status    TEXT NOT NULL,
    agent     TEXT NOT NULL,
    repo_path TEXT NOT NULL,
    branch    TEXT NOT NULL,
    worktree  TEXT NOT NULL,
    session   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS projects (
    id        TEXT PRIMARY KEY,
    name      TEXT NOT NULL,
    repo_path TEXT NOT NULL,
    agent     TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS columns (
    id     TEXT PRIMARY KEY,
    name   TEXT NOT NULL,
    emoji  TEXT NOT NULL,
    prompt TEXT NOT NULL,
    pos    INTEGER NOT NULL
);
