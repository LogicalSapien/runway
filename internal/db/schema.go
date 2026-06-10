package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

// Open opens (or creates) the SQLite database at path, applies WAL mode and
// the v2 schema migrations, and returns the connection pool.
func Open(path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir %s: %w", dir, err)
		}
	}
	dsn := fmt.Sprintf(
		"file:%s?_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000",
		path,
	)
	d, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite performs best with a single writer.
	d.SetMaxOpenConns(1)
	if err := migrate(d); err != nil {
		d.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return d, nil
}

func migrate(d *sql.DB) error {
	if _, err := d.Exec(schema); err != nil {
		return err
	}
	// Additive migrations for tables created by older versions.
	// "duplicate column name" means the column already exists — not an error.
	for _, stmt := range []string{
		`ALTER TABLE queue ADD COLUMN run_id INTEGER`,
	} {
		if _, err := d.Exec(stmt); err != nil &&
			!strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("migration %q: %w", stmt, err)
		}
	}
	return nil
}

const schema = `
-- ─── repos ────────────────────────────────────────────────────────────────────
-- Registered repositories that Runway manages / monitors.
CREATE TABLE IF NOT EXISTS repos (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    name           TEXT    NOT NULL,                       -- e.g. "my-app"
    owner          TEXT    NOT NULL,                       -- e.g. "my-org"
    git_url        TEXT    NOT NULL,                       -- SSH or HTTPS clone URL
    default_branch TEXT    NOT NULL DEFAULT 'main',
    deploy_key     TEXT,                                   -- PEM private key; NULL = use system SSH
    clone_path     TEXT,                                   -- absolute path after first clone
    created_at     INTEGER NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_repos_owner_name ON repos(owner, name);

-- ─── workflows ────────────────────────────────────────────────────────────────
-- Workflow files discovered inside a repo's .github/workflows/ directory.
CREATE TABLE IF NOT EXISTS workflows (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id    INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    file       TEXT    NOT NULL,                           -- filename, e.g. "deploy.yml"
    name       TEXT    NOT NULL,                           -- from YAML name: field
    updated_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_workflows_repo_file ON workflows(repo_id, file);

-- ─── runs ─────────────────────────────────────────────────────────────────────
-- A single execution of a workflow (triggered by API, push, or manual dispatch).
-- repo_id may be NULL for runs captured from legacy Docker-event watcher that
-- have not yet been matched to a registered repo.
CREATE TABLE IF NOT EXISTS runs (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id       INTEGER REFERENCES repos(id) ON DELETE SET NULL,
    repo          TEXT    NOT NULL,                        -- human name, denorm for display
    workflow      TEXT    NOT NULL,                        -- workflow name
    workflow_file TEXT,                                    -- filename, e.g. "deploy.yml"
    trigger       TEXT    NOT NULL DEFAULT 'api',          -- api | push | schedule | manual
    branch        TEXT    NOT NULL DEFAULT 'main',
    commit_sha    TEXT,
    status        TEXT    NOT NULL DEFAULT 'queued',       -- queued/running/success/failure/cancelled/unknown
    started_at    INTEGER,                                 -- NULL until work begins
    finished_at   INTEGER,
    created_at    INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_runs_created      ON runs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_runs_repo_status  ON runs(repo_id, status);
CREATE INDEX IF NOT EXISTS idx_runs_status       ON runs(status);

-- ─── jobs ─────────────────────────────────────────────────────────────────────
-- A GitHub Actions job within a run (act runs one or more jobs per workflow).
CREATE TABLE IF NOT EXISTS jobs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id      INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    name        TEXT    NOT NULL,                          -- job id from YAML, e.g. "build"
    status      TEXT    NOT NULL DEFAULT 'pending',        -- pending/running/success/failure/skipped
    started_at  INTEGER,
    finished_at INTEGER
);

CREATE INDEX IF NOT EXISTS idx_jobs_run ON jobs(run_id);

-- ─── steps ────────────────────────────────────────────────────────────────────
-- A step within a job.  run_id is denormalised so callers can join logs → run
-- without traversing jobs.
CREATE TABLE IF NOT EXISTS steps (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id      INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    run_id      INTEGER NOT NULL,                          -- denorm; mirrors jobs.run_id
    name        TEXT    NOT NULL,
    status      TEXT    NOT NULL DEFAULT 'pending',        -- pending/running/success/failure/skipped
    started_at  INTEGER,
    finished_at INTEGER
);

CREATE INDEX IF NOT EXISTS idx_steps_job ON steps(job_id);
CREATE INDEX IF NOT EXISTS idx_steps_run ON steps(run_id);

-- ─── logs ─────────────────────────────────────────────────────────────────────
-- Individual log lines emitted by a step.  run_id is denormalised so callers
-- can fetch all logs for a run in one query without joining through steps.
CREATE TABLE IF NOT EXISTS logs (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    step_id INTEGER NOT NULL REFERENCES steps(id) ON DELETE CASCADE,
    run_id  INTEGER NOT NULL,                              -- denorm
    ts      INTEGER NOT NULL,                              -- Unix seconds
    line_no INTEGER NOT NULL,
    text    TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_logs_step_lineno ON logs(step_id, line_no);
CREATE INDEX IF NOT EXISTS idx_logs_run         ON logs(run_id, line_no);

-- ─── queue ────────────────────────────────────────────────────────────────────
-- Dispatch queue for workflow runs.  The runner dequeues items respecting the
-- concurrency setting and the priority column (higher = sooner).
CREATE TABLE IF NOT EXISTS queue (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id       INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    workflow_file TEXT    NOT NULL,
    branch        TEXT    NOT NULL,
    inputs        TEXT,                                    -- JSON blob or NULL
    status        TEXT    NOT NULL DEFAULT 'queued',       -- queued/running/done/cancelled
    priority      INTEGER NOT NULL DEFAULT 0,              -- higher wins
    run_id        INTEGER,                                 -- runs.id once the engine starts it
    created_at    INTEGER NOT NULL,
    started_at    INTEGER,
    finished_at   INTEGER
);

CREATE INDEX IF NOT EXISTS idx_queue_status_priority ON queue(status, priority DESC, created_at ASC);

-- ─── settings ─────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

INSERT OR IGNORE INTO settings(key, value) VALUES ('concurrency',      '1');
INSERT OR IGNORE INTO settings(key, value) VALUES ('api_token_hash',   '');
INSERT OR IGNORE INTO settings(key, value) VALUES ('api_token_set_at', '');
INSERT OR IGNORE INTO settings(key, value) VALUES ('retention_days',   '30');

-- ─── rate_limit_counts ────────────────────────────────────────────────────────
-- Sliding-window request counter per API token (keyed by hashed token value).
-- window_start is the Unix timestamp of the start of the current window.
CREATE TABLE IF NOT EXISTS rate_limit_counts (
    token_hash   TEXT    NOT NULL,
    window_start INTEGER NOT NULL,
    count        INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (token_hash, window_start)
);

-- ─── users ────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS users (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    username        TEXT    NOT NULL UNIQUE,
    role            TEXT    NOT NULL DEFAULT 'viewer',  -- admin | viewer
    password_hash   TEXT    NOT NULL,
    must_change_pw  INTEGER NOT NULL DEFAULT 0,         -- 1 = prompt on next login
    created_at      INTEGER NOT NULL
);

-- ─── sessions ─────────────────────────────────────────────────────────────────
-- Browser login sessions (HttpOnly cookie).
CREATE TABLE IF NOT EXISTS sessions (
    token_hash TEXT    NOT NULL PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);

-- ─── api_keys ─────────────────────────────────────────────────────────────────
-- Long-lived programmatic tokens (generated by admin, used in Bearer header).
CREATE TABLE IF NOT EXISTS api_keys (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    key_hash     TEXT    NOT NULL UNIQUE,
    name         TEXT    NOT NULL,
    created_by   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER
);
`
