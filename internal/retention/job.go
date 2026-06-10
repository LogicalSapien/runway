package retention

import (
	"context"
	"database/sql"
	"log"
	"time"
)

type Job struct {
	db *sql.DB
}

func NewJob(db *sql.DB) *Job {
	return &Job{db: db}
}

func (j *Job) Run(ctx context.Context) {
	j.prune()
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			j.prune()
		}
	}
}

func (j *Job) prune() {
	var days int
	_ = j.db.QueryRow(`SELECT CAST(value AS INTEGER) FROM settings WHERE key='retention_days'`).Scan(&days)
	if days <= 0 {
		days = 30
	}
	cutoff := time.Now().AddDate(0, 0, -days).Unix()
	res, err := j.db.Exec(`DELETE FROM runs WHERE finished_at IS NOT NULL AND finished_at < ?`, cutoff)
	if err != nil {
		log.Printf("retention: prune: %v", err)
		return
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		log.Printf("retention: pruned %d runs older than %d days", n, days)
	}
}
