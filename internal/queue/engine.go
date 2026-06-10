package queue

import (
	"context"
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"time"

	dbpkg "github.com/LogicalSapien/runway/internal/db"
	"github.com/LogicalSapien/runway/internal/secrets"
)

// Engine is a background goroutine that drains the queue table, respecting a
// configurable concurrency limit.  Each dequeued item performs a git pull then
// spawns act; progress is streamed into the DB in real time.
// Concurrency is enforced at the DB level (counting running rows) so restarts
// cannot cause over-scheduling.
type Engine struct {
	db       *sql.DB
	reposDir string          // base directory for cloned repos (REPOS_ROOT)
	cipher   *secrets.Cipher // decrypts repo/environment secrets; nil = disabled
}

// NewEngine creates a queue engine.  reposDir is the root under which repos are
// cloned (one sub-directory per repo, named after repo.name).
func NewEngine(db *sql.DB, reposDir string, cipher *secrets.Cipher) *Engine {
	return &Engine{db: db, reposDir: reposDir, cipher: cipher}
}

// Run starts the engine and blocks until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) {
	e.healStale()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.tick(ctx)
		}
	}
}

// tick dequeues as many items as the concurrency limit allows and launches each
// in a separate goroutine.
func (e *Engine) tick(ctx context.Context) {
	limit := e.concurrencyLimit()
	items, err := e.dequeue(limit)
	if err != nil {
		log.Printf("queue engine: dequeue: %v", err)
		return
	}

	for _, item := range items {
		go e.process(ctx, item)
	}
}

// QueueItem is a row from the queue table joined with its repo.
type QueueItem struct {
	ID           int64
	RepoID       int64
	RepoName     string
	RepoOwner    string
	GitURL       string
	Branch       string
	DeployKey    string // PEM text or path; empty = use system SSH
	ClonePath    string // absolute path on disk; may be empty (engine will set it)
	WorkflowFile string
	Inputs       string // JSON blob or ""
	Event        string // act trigger event (workflow_dispatch | push)
}

// dequeue marks up to n queued items as running and returns them.
// The available slot count is computed inside the transaction against the DB's
// own running count — not an in-memory counter — so restarts cannot cause
// over-scheduling regardless of concurrency.
func (e *Engine) dequeue(limit int) ([]QueueItem, error) {
	tx, err := e.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	// Count how many items are already running according to the DB.
	var inFlight int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM queue WHERE status='running'`).Scan(&inFlight); err != nil {
		return nil, err
	}
	n := limit - inFlight
	if n <= 0 {
		return nil, nil
	}

	rows, err := tx.Query(`
		SELECT q.id, q.repo_id, q.workflow_file, q.branch, COALESCE(q.inputs,''), q.event,
		       r.name, r.owner, r.git_url,
		       COALESCE(r.deploy_key,''), COALESCE(r.clone_path,'')
		FROM queue q
		JOIN repos r ON r.id = q.repo_id
		WHERE q.status = 'queued'
		ORDER BY q.priority DESC, q.created_at ASC
		LIMIT ?`, n)
	if err != nil {
		return nil, err
	}

	var items []QueueItem
	for rows.Next() {
		var qi QueueItem
		if err := rows.Scan(
			&qi.ID, &qi.RepoID, &qi.WorkflowFile, &qi.Branch, &qi.Inputs, &qi.Event,
			&qi.RepoName, &qi.RepoOwner, &qi.GitURL,
			&qi.DeployKey, &qi.ClonePath,
		); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, qi)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	for _, qi := range items {
		if _, err := tx.Exec(
			`UPDATE queue SET status='running', started_at=? WHERE id=?`,
			now, qi.ID,
		); err != nil {
			return nil, err
		}
	}

	return items, tx.Commit()
}

// process performs the full lifecycle for one queue item:
//  1. git clone/pull → get HEAD sha
//  2. insert a run record (status=running)
//  3. spawn act, stream stdout through the parser
//  4. finish run and queue item on exit
func (e *Engine) process(ctx context.Context, qi QueueItem) {
	log.Printf("queue: starting %s/%s workflow=%s branch=%s",
		qi.RepoOwner, qi.RepoName, qi.WorkflowFile, qi.Branch)

	// 1. git
	clonePath, sha, err := CloneOrPull(qi, e.reposDir)
	if err != nil {
		log.Printf("queue: git error repo=%s: %v", qi.RepoName, err)
		e.failQueueItem(qi.ID, "git error: "+err.Error())
		return
	}

	// Persist clone_path so future runs know where the repo lives.
	if qi.ClonePath == "" {
		e.exec(`UPDATE repos SET clone_path=? WHERE id=?`, clonePath, qi.RepoID)
	}

	// 2. create run
	now := time.Now().Unix()
	res, err := e.db.Exec(`
		INSERT INTO runs(repo_id, repo, workflow, workflow_file, trigger, branch, commit_sha, status, started_at, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		qi.RepoID, qi.RepoName,
		workflowName(qi.WorkflowFile), qi.WorkflowFile,
		qi.Event, qi.Branch, sha,
		"running", now, now,
	)
	if err != nil {
		log.Printf("queue: insert run repo=%s: %v", qi.RepoName, err)
		e.failQueueItem(qi.ID, "db error: "+err.Error())
		return
	}
	runID, _ := res.LastInsertId()

	// Link queue item to run
	e.exec(`UPDATE queue SET run_id=? WHERE id=?`, runID, qi.ID)

	// 3. run act — merge the operator's global secrets file with this repo's
	// stored secrets/variables into per-run temp files (deleted afterwards).
	secretsFile, varsFile, cleanupFiles := e.buildRunFiles(qi, clonePath)
	defer cleanupFiles()
	runCtx, cancelRun := context.WithCancel(ctx)
	registerRun(runID, cancelRun)
	runner := NewRunner(e.db, qi, runID, clonePath, sha, secretsFile, varsFile)
	exitCode, runErr := runner.Run(runCtx)
	cancelled := runCtx.Err() != nil && ctx.Err() == nil
	unregisterRun(runID)
	cancelRun()

	// 4. finish
	status := "success"
	if exitCode != 0 || runErr != nil {
		status = "failure"
	}
	if cancelled {
		status = "cancelled"
	}
	finishedAt := time.Now().Unix()
	e.exec(
		`UPDATE runs SET status=?, finished_at=? WHERE id=?`,
		status, finishedAt, runID,
	)
	e.exec(
		`UPDATE queue SET status='done', finished_at=? WHERE id=?`,
		finishedAt, qi.ID,
	)

	log.Printf("queue: finished run=%d repo=%s status=%s exit=%d",
		runID, qi.RepoName, status, exitCode)
	e.notify(qi, runID, status)
}

// envRefRe matches "environment: name" and the block form's "name: x" line in
// workflow YAML — a heuristic to decide which environment scopes apply to a run
// (act itself does not resolve GitHub environments).
var envRefRe = regexp.MustCompile(`(?m)^\s*(?:environment|name):\s*['"]?([A-Za-z0-9._-]+)['"]?\s*$`)

// buildRunFiles merges the operator's global secrets file with this repo's
// stored secrets and variables into per-run 0600 temp files. Environment-scoped
// values apply when the workflow file references that environment, overriding
// repo-level values of the same name. Returns ("", "", noop) on full failure —
// the run proceeds with whatever could be built.
func (e *Engine) buildRunFiles(qi QueueItem, clonePath string) (secretsFile, varsFile string, cleanup func()) {
	cleanup = func() {}

	// Environments referenced by this workflow ∩ environments defined for the repo.
	var envs []string
	if wf, err := os.ReadFile(filepath.Join(clonePath, ".github", "workflows", qi.WorkflowFile)); err == nil {
		defined, _ := dbpkg.ListEnvironments(e.db, qi.RepoID)
		referenced := map[string]bool{}
		for _, m := range envRefRe.FindAllStringSubmatch(string(wf), -1) {
			referenced[m[1]] = true
		}
		for _, d := range defined {
			if referenced[d] {
				envs = append(envs, d)
			}
		}
	}

	// Secrets: global file first, repo/env values override.
	merged := map[string]string{}
	if global := e.settingOrDefault("secrets_file", ""); global != "" {
		if b, err := os.ReadFile(global); err == nil {
			merged = secrets.ParseDotenv(string(b))
		}
	}
	if e.cipher != nil {
		encVals, err := dbpkg.SecretValuesForRun(e.db, qi.RepoID, envs)
		if err != nil {
			log.Printf("queue: load secrets repo=%d: %v", qi.RepoID, err)
		}
		for name, enc := range encVals {
			val, err := e.cipher.Decrypt(enc)
			if err != nil {
				log.Printf("queue: decrypt secret %s: %v (skipped)", name, err)
				continue
			}
			merged[name] = val
		}
	}

	var paths []string
	if len(merged) > 0 {
		p, err := secrets.WriteDotenvFile("runway-secrets-*", merged)
		if err != nil {
			log.Printf("queue: write secrets file: %v", err)
		} else {
			secretsFile = p
			paths = append(paths, p)
		}
	}

	// Variables (plaintext, exposed as ${{ vars.NAME }}).
	vars, err := dbpkg.VariableValuesForRun(e.db, qi.RepoID, envs)
	if err != nil {
		log.Printf("queue: load variables repo=%d: %v", qi.RepoID, err)
	}
	if len(vars) > 0 {
		p, err := secrets.WriteDotenvFile("runway-vars-*", vars)
		if err != nil {
			log.Printf("queue: write vars file: %v", err)
		} else {
			varsFile = p
			paths = append(paths, p)
		}
	}

	cleanup = func() {
		for _, p := range paths {
			_ = os.Remove(p)
		}
	}
	return secretsFile, varsFile, cleanup
}

// failQueueItem marks a queue item as done with an error (no run created / run
// already marked failed upstream).
func (e *Engine) failQueueItem(id int64, reason string) {
	now := time.Now().Unix()
	e.exec(
		`UPDATE queue SET status='done', finished_at=? WHERE id=?`,
		now, id,
	)
	log.Printf("queue: item %d failed: %s", id, reason)
}

// exec runs a fire-and-forget statement, logging instead of returning errors —
// callers are state updates where the run must proceed regardless.
func (e *Engine) exec(query string, args ...any) {
	if _, err := e.db.Exec(query, args...); err != nil {
		log.Printf("queue: db exec failed: %v (query: %.60s)", err, query)
	}
}

// healStale marks any queue items or runs that were left in 'running' state
// from a previous engine session (e.g. runway restarted mid-run) as failed.
// We do NOT re-queue them: re-queuing causes duplicate act spawns when the
// process restarts repeatedly (e.g. under high load). The operator must
// re-dispatch manually if needed.
func (e *Engine) healStale() {
	now := time.Now().Unix()
	if _, err := e.db.Exec(
		`UPDATE queue SET status='failed', finished_at=? WHERE status='running'`,
		now,
	); err != nil {
		log.Printf("queue: healStale queue: %v", err)
	}
	if _, err := e.db.Exec(
		`UPDATE runs SET status='unknown', finished_at=? WHERE status='running'`,
		now,
	); err != nil {
		log.Printf("queue: healStale runs: %v", err)
	}
	if _, err := e.db.Exec(
		`UPDATE jobs SET status='unknown', finished_at=? WHERE status='running'`,
		now,
	); err != nil {
		log.Printf("queue: healStale jobs: %v", err)
	}
	if _, err := e.db.Exec(
		`UPDATE steps SET status='unknown', finished_at=? WHERE status='running'`,
		now,
	); err != nil {
		log.Printf("queue: healStale steps: %v", err)
	}
}

// concurrencyLimit reads the 'concurrency' setting from the DB (default 1).
func (e *Engine) concurrencyLimit() int {
	var v int
	_ = e.db.QueryRow(
		`SELECT CAST(value AS INTEGER) FROM settings WHERE key='concurrency'`,
	).Scan(&v)
	if v <= 0 {
		v = 1
	}
	return v
}

// settingOrDefault reads a setting key, returning def if absent or empty.
func (e *Engine) settingOrDefault(key, def string) string {
	var v string
	_ = e.db.QueryRow(
		`SELECT value FROM settings WHERE key=?`, key,
	).Scan(&v)
	if v == "" {
		return def
	}
	return v
}

// workflowName derives a human-readable workflow name from its filename.
// e.g. "deploy-gallery.yml" → "deploy-gallery"
func workflowName(file string) string {
	n := file
	for _, suffix := range []string{".yml", ".yaml"} {
		if len(n) > len(suffix) && n[len(n)-len(suffix):] == suffix {
			n = n[:len(n)-len(suffix)]
			break
		}
	}
	return n
}
