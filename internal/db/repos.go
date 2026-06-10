package db

import (
	"database/sql"
	"time"
)

// Repo is a registered repository managed by Runway.
type Repo struct {
	ID            int64   `json:"id"`
	Name          string  `json:"name"`
	Owner         string  `json:"owner"`
	GitURL        string  `json:"git_url"`
	DefaultBranch string  `json:"default_branch"`
	DeployKey     *string `json:"deploy_key,omitempty"` // PEM private key; nil = system SSH
	ClonePath     *string `json:"clone_path,omitempty"` // set after first clone
	CreatedAt     int64   `json:"created_at"`
}

// InsertRepo inserts a new repo record and returns the assigned ID.
func InsertRepo(db *sql.DB, r Repo) (int64, error) {
	res, err := db.Exec(
		`INSERT INTO repos(name,owner,git_url,default_branch,deploy_key,clone_path,created_at)
		 VALUES(?,?,?,?,?,?,?)`,
		r.Name, r.Owner, r.GitURL, r.DefaultBranch, r.DeployKey, r.ClonePath,
		r.CreatedAt,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetRepo returns the repo with the given ID, or nil if not found.
func GetRepo(db *sql.DB, id int64) (*Repo, error) {
	var r Repo
	err := db.QueryRow(
		`SELECT id,name,owner,git_url,default_branch,deploy_key,clone_path,created_at
		 FROM repos WHERE id=?`, id,
	).Scan(&r.ID, &r.Name, &r.Owner, &r.GitURL, &r.DefaultBranch,
		&r.DeployKey, &r.ClonePath, &r.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ListRepos returns all registered repos ordered by owner then name.
func ListRepos(db *sql.DB) ([]Repo, error) {
	rows, err := db.Query(
		`SELECT id,name,owner,git_url,default_branch,deploy_key,clone_path,created_at
		 FROM repos ORDER BY owner, name`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Repo
	for rows.Next() {
		var r Repo
		if err := rows.Scan(&r.ID, &r.Name, &r.Owner, &r.GitURL, &r.DefaultBranch,
			&r.DeployKey, &r.ClonePath, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRepoByOwnerName returns the repo matching the given owner and name, or nil
// if no such repo is registered.
func GetRepoByOwnerName(db *sql.DB, owner, name string) (*Repo, error) {
	var r Repo
	err := db.QueryRow(
		`SELECT id,name,owner,git_url,default_branch,deploy_key,clone_path,created_at
		 FROM repos WHERE owner=? AND name=?`, owner, name,
	).Scan(&r.ID, &r.Name, &r.Owner, &r.GitURL, &r.DefaultBranch,
		&r.DeployKey, &r.ClonePath, &r.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// UpdateRepo updates the mutable fields of an existing repo record.
func UpdateRepo(db *sql.DB, r Repo) error {
	_, err := db.Exec(
		`UPDATE repos
		 SET name=?, owner=?, git_url=?, default_branch=?,
		     deploy_key=?, clone_path=?
		 WHERE id=?`,
		r.Name, r.Owner, r.GitURL, r.DefaultBranch,
		r.DeployKey, r.ClonePath,
		r.ID,
	)
	return err
}

// DeleteRepo removes a repo and cascades to workflows, runs, jobs, steps,
// logs, and queue items via ON DELETE CASCADE foreign keys.
func DeleteRepo(db *sql.DB, id int64) error {
	_, err := db.Exec(`DELETE FROM repos WHERE id=?`, id)
	return err
}

// WorkflowSummary is a workflow record enriched with run statistics.
type WorkflowSummary struct {
	ID         int64  `json:"id"`
	RepoID     int64  `json:"repo_id"`
	File       string `json:"file"`
	Name       string `json:"name"`
	UpdatedAt  int64  `json:"updated_at"`
	RunCount   int    `json:"run_count"`
	LastStatus string `json:"last_status"` // "" when no runs exist yet
}

// ScanWorkflows returns every workflow registered for repoID enriched with the
// total run count and the status of the most recent run.
func ScanWorkflows(db *sql.DB, repoID int64) ([]WorkflowSummary, error) {
	rows, err := db.Query(
		`SELECT
		     w.id, w.repo_id, w.file, w.name, w.updated_at,
		     COUNT(r.id) AS run_count,
		     COALESCE(
		         (SELECT r2.status FROM runs r2
		          WHERE r2.repo_id = w.repo_id AND r2.workflow_file = w.file
		          ORDER BY r2.created_at DESC LIMIT 1),
		         ''
		     ) AS last_status
		 FROM workflows w
		 LEFT JOIN runs r ON r.repo_id = w.repo_id AND r.workflow_file = w.file
		 WHERE w.repo_id = ?
		 GROUP BY w.id
		 ORDER BY w.name`,
		repoID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorkflowSummary
	for rows.Next() {
		var s WorkflowSummary
		if err := rows.Scan(&s.ID, &s.RepoID, &s.File, &s.Name, &s.UpdatedAt,
			&s.RunCount, &s.LastStatus); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// UpsertWorkflow inserts or updates a workflow record (keyed by repo_id + file).
func UpsertWorkflow(db *sql.DB, repoID int64, file, name string) error {
	_, err := db.Exec(
		`INSERT INTO workflows(repo_id,file,name,updated_at)
		 VALUES(?,?,?,?)
		 ON CONFLICT(repo_id,file) DO UPDATE SET
		     name=excluded.name, updated_at=excluded.updated_at`,
		repoID, file, name, time.Now().Unix(),
	)
	return err
}
