package db

import (
	"database/sql"
	"time"
)

// QueueItem is a pending or in-flight workflow dispatch request.
type QueueItem struct {
	ID           int64   `json:"id"`
	RepoID       int64   `json:"repo_id"`
	WorkflowFile string  `json:"workflow_file"`
	Branch       string  `json:"branch"`
	Inputs       *string `json:"inputs,omitempty"` // JSON blob or nil
	Status       string  `json:"status"`           // queued/running/done/cancelled
	Priority     int     `json:"priority"`         // higher = dequeued sooner
	CreatedAt    int64   `json:"created_at"`
	StartedAt    *int64  `json:"started_at,omitempty"`
	FinishedAt   *int64  `json:"finished_at,omitempty"`
}

// Enqueue adds a new item to the dispatch queue and returns its ID.
func Enqueue(db *sql.DB, q QueueItem) (int64, error) {
	if q.CreatedAt == 0 {
		q.CreatedAt = time.Now().Unix()
	}
	res, err := db.Exec(
		`INSERT INTO queue(repo_id,workflow_file,branch,inputs,status,priority,created_at)
		 VALUES(?,?,?,?,?,?,?)`,
		q.RepoID, q.WorkflowFile, q.Branch, q.Inputs,
		"queued", q.Priority, q.CreatedAt,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DequeueNext claims the next eligible queued item, respecting the concurrency
// cap.  It returns nil if no item is available (queue empty or concurrency
// limit reached).
//
// Concurrency is enforced by checking how many items are currently in the
// "running" state against the provided concurrency limit.
func DequeueNext(db *sql.DB, concurrency int) (*QueueItem, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	// Count in-flight items.
	var running int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM queue WHERE status='running'`,
	).Scan(&running); err != nil {
		return nil, err
	}
	if running >= concurrency {
		return nil, nil // at capacity
	}

	// Pick the highest-priority oldest queued item.
	var item QueueItem
	err = tx.QueryRow(
		`SELECT id,repo_id,workflow_file,branch,inputs,status,priority,created_at,started_at,finished_at
		 FROM queue
		 WHERE status='queued'
		 ORDER BY priority DESC, created_at ASC
		 LIMIT 1`,
	).Scan(&item.ID, &item.RepoID, &item.WorkflowFile, &item.Branch, &item.Inputs,
		&item.Status, &item.Priority, &item.CreatedAt, &item.StartedAt, &item.FinishedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	if _, err := tx.Exec(
		`UPDATE queue SET status='running', started_at=? WHERE id=?`,
		now, item.ID,
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	item.Status = "running"
	item.StartedAt = &now
	return &item, nil
}

// FinishQueueItem marks a running queue item as done.
func FinishQueueItem(db *sql.DB, id int64) error {
	_, err := db.Exec(
		`UPDATE queue SET status='done', finished_at=? WHERE id=?`,
		time.Now().Unix(), id,
	)
	return err
}

// CancelQueueItem marks a queued (or running) item as cancelled.
func CancelQueueItem(db *sql.DB, id int64) error {
	_, err := db.Exec(
		`UPDATE queue SET status='cancelled', finished_at=? WHERE id=?`,
		time.Now().Unix(), id,
	)
	return err
}

// ListQueue returns all queue items ordered by priority descending then age.
// Pass status="" to return items of all statuses; otherwise filter to that status.
func ListQueue(db *sql.DB, status string) ([]QueueItem, error) {
	q := `SELECT id,repo_id,workflow_file,branch,inputs,status,priority,
	             created_at,started_at,finished_at
	      FROM queue`
	args := []any{}
	if status != "" {
		q += " WHERE status=?"
		args = append(args, status)
	}
	q += " ORDER BY priority DESC, created_at ASC"

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueueItem
	for rows.Next() {
		var item QueueItem
		if err := rows.Scan(&item.ID, &item.RepoID, &item.WorkflowFile, &item.Branch,
			&item.Inputs, &item.Status, &item.Priority,
			&item.CreatedAt, &item.StartedAt, &item.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
