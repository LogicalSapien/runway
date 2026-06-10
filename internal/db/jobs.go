package db

import (
	"database/sql"
	"time"
)

// Job represents a GitHub Actions job within a run.
type Job struct {
	ID         int64  `json:"id"`
	RunID      int64  `json:"run_id"`
	Name       string `json:"name"`   // job id from YAML, e.g. "build"
	Status     string `json:"status"` // pending/running/success/failure/skipped
	StartedAt  *int64 `json:"started_at,omitempty"`
	FinishedAt *int64 `json:"finished_at,omitempty"`
}

// InsertJob inserts a new job and returns the assigned ID.
func InsertJob(db *sql.DB, j Job) (int64, error) {
	res, err := db.Exec(
		`INSERT INTO jobs(run_id,name,status,started_at,finished_at)
		 VALUES(?,?,?,?,?)`,
		j.RunID, j.Name, j.Status, j.StartedAt, j.FinishedAt,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetJob returns the job with the given ID, or nil if not found.
func GetJob(db *sql.DB, id int64) (*Job, error) {
	var j Job
	err := db.QueryRow(
		`SELECT id,run_id,name,status,started_at,finished_at
		 FROM jobs WHERE id=?`, id,
	).Scan(&j.ID, &j.RunID, &j.Name, &j.Status, &j.StartedAt, &j.FinishedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// ListJobsByRun returns all jobs for a run ordered by insertion order.
func ListJobsByRun(db *sql.DB, runID int64) ([]Job, error) {
	rows, err := db.Query(
		`SELECT id,run_id,name,status,started_at,finished_at
		 FROM jobs WHERE run_id=? ORDER BY id`,
		runID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.RunID, &j.Name, &j.Status,
			&j.StartedAt, &j.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// FinishJob sets the terminal status and finished_at timestamp on a job.
func FinishJob(db *sql.DB, id int64, status string) error {
	_, err := db.Exec(
		`UPDATE jobs SET finished_at=?, status=? WHERE id=?`,
		time.Now().Unix(), status, id,
	)
	return err
}
