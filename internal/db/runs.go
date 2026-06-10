package db

import (
	"database/sql"
	"time"
)

// Run represents a single execution of a workflow.
type Run struct {
	ID           int64   `json:"id"`
	RepoID       *int64  `json:"repo_id,omitempty"` // nil for legacy watcher-captured runs
	Repo         string  `json:"repo"`              // human display name
	Workflow     string  `json:"workflow"`          // workflow name from YAML
	WorkflowFile *string `json:"workflow_file,omitempty"`
	Trigger      string  `json:"trigger"` // api | push | schedule | manual
	Branch       string  `json:"branch"`
	CommitSHA    *string `json:"commit_sha,omitempty"`
	Status       string  `json:"status"`               // queued/running/success/failure/cancelled/unknown
	StartedAt    *int64  `json:"started_at,omitempty"` // nil until work begins
	FinishedAt   *int64  `json:"finished_at,omitempty"`
	CreatedAt    int64   `json:"created_at"`
}

// InsertRun inserts a new run record and returns the assigned ID.
func InsertRun(db *sql.DB, r Run) (int64, error) {
	if r.CreatedAt == 0 {
		r.CreatedAt = time.Now().Unix()
	}
	res, err := db.Exec(
		`INSERT INTO runs(repo_id,repo,workflow,workflow_file,trigger,branch,commit_sha,
		                  status,started_at,finished_at,created_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		r.RepoID, r.Repo, r.Workflow, r.WorkflowFile, r.Trigger, r.Branch,
		r.CommitSHA, r.Status, r.StartedAt, r.FinishedAt, r.CreatedAt,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetRun returns the run with the given ID, or nil if not found.
func GetRun(db *sql.DB, id int64) (*Run, error) {
	var r Run
	err := db.QueryRow(
		`SELECT id,repo_id,repo,workflow,workflow_file,trigger,branch,commit_sha,
		        status,started_at,finished_at,created_at
		 FROM runs WHERE id=?`, id,
	).Scan(&r.ID, &r.RepoID, &r.Repo, &r.Workflow, &r.WorkflowFile, &r.Trigger,
		&r.Branch, &r.CommitSHA, &r.Status, &r.StartedAt, &r.FinishedAt, &r.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ListRunsFilter holds optional filter parameters for ListRuns.
type ListRunsFilter struct {
	Repo     string // filter by repo name
	Workflow string // filter by workflow name
	Status   string // filter by status
	Since    int64  // only runs created at/after this unix timestamp
	Limit    int
	Offset   int // 0 → default of 50
}

// ListRuns returns runs in reverse chronological order, applying any filters
// from f and capping results at f.Limit (default 50).
func ListRuns(db *sql.DB, f ListRunsFilter) ([]Run, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}

	q := `SELECT id,repo_id,repo,workflow,workflow_file,trigger,branch,commit_sha,
	             status,started_at,finished_at,created_at
	      FROM runs WHERE 1=1`
	args := []any{}

	if f.Repo != "" {
		q += " AND repo=?"
		args = append(args, f.Repo)
	}
	if f.Workflow != "" {
		q += " AND workflow=?"
		args = append(args, f.Workflow)
	}
	if f.Status != "" {
		q += " AND status=?"
		args = append(args, f.Status)
	}
	if f.Since > 0 {
		q += " AND created_at >= ?"
		args = append(args, f.Since)
	}
	q += " ORDER BY created_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, f.Offset)

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Run
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.RepoID, &r.Repo, &r.Workflow, &r.WorkflowFile,
			&r.Trigger, &r.Branch, &r.CommitSHA, &r.Status,
			&r.StartedAt, &r.FinishedAt, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FinishRun sets the terminal status and finished_at timestamp on a run.
func FinishRun(db *sql.DB, id int64, status string) error {
	_, err := db.Exec(
		`UPDATE runs SET finished_at=?, status=? WHERE id=?`,
		time.Now().Unix(), status, id,
	)
	return err
}

// UpdateRunStatus updates only the status column (e.g. queued → running).
func UpdateRunStatus(db *sql.DB, id int64, status string) error {
	now := time.Now().Unix()
	// Also stamp started_at the first time the run transitions into running.
	_, err := db.Exec(
		`UPDATE runs
		 SET status=?,
		     started_at = CASE WHEN status='queued' AND ?='running'
		                       THEN ? ELSE started_at END
		 WHERE id=?`,
		status, status, now, id,
	)
	return err
}

// CountRuns returns the total number of runs matching f (ignoring limit/offset).
func CountRuns(db *sql.DB, f ListRunsFilter) (int, error) {
	q := `SELECT COUNT(*) FROM runs WHERE 1=1`
	args := []any{}
	if f.Repo != "" {
		q += " AND repo=?"
		args = append(args, f.Repo)
	}
	if f.Workflow != "" {
		q += " AND workflow=?"
		args = append(args, f.Workflow)
	}
	if f.Status != "" {
		q += " AND status=?"
		args = append(args, f.Status)
	}
	if f.Since > 0 {
		q += " AND created_at >= ?"
		args = append(args, f.Since)
	}
	var n int
	err := db.QueryRow(q, args...).Scan(&n)
	return n, err
}
