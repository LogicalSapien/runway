package db

import (
	"database/sql"
	"time"
)

// Step represents a single step within a job.
type Step struct {
	ID         int64  `json:"id"`
	JobID      int64  `json:"job_id"`
	RunID      int64  `json:"run_id"` // denorm — mirrors jobs.run_id
	Name       string `json:"name"`
	Status     string `json:"status"` // pending/running/success/failure/skipped
	StartedAt  *int64 `json:"started_at,omitempty"`
	FinishedAt *int64 `json:"finished_at,omitempty"`
}

// InsertStep inserts a new step and returns the assigned ID.
func InsertStep(db *sql.DB, s Step) (int64, error) {
	res, err := db.Exec(
		`INSERT INTO steps(job_id,run_id,name,status,started_at,finished_at)
		 VALUES(?,?,?,?,?,?)`,
		s.JobID, s.RunID, s.Name, s.Status, s.StartedAt, s.FinishedAt,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetStep returns the step with the given ID, or nil if not found.
func GetStep(db *sql.DB, id int64) (*Step, error) {
	var s Step
	err := db.QueryRow(
		`SELECT id,job_id,run_id,name,status,started_at,finished_at
		 FROM steps WHERE id=?`, id,
	).Scan(&s.ID, &s.JobID, &s.RunID, &s.Name, &s.Status,
		&s.StartedAt, &s.FinishedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// ListStepsByJob returns all steps for a job ordered by insertion order.
func ListStepsByJob(db *sql.DB, jobID int64) ([]Step, error) {
	rows, err := db.Query(
		`SELECT id,job_id,run_id,name,status,started_at,finished_at
		 FROM steps WHERE job_id=? ORDER BY id`,
		jobID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Step
	for rows.Next() {
		var s Step
		if err := rows.Scan(&s.ID, &s.JobID, &s.RunID, &s.Name, &s.Status,
			&s.StartedAt, &s.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// FinishStep sets the terminal status and finished_at timestamp on a step.
func FinishStep(db *sql.DB, id int64, status string) error {
	_, err := db.Exec(
		`UPDATE steps SET finished_at=?, status=? WHERE id=?`,
		time.Now().Unix(), status, id,
	)
	return err
}
