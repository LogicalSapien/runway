package db

import (
	"database/sql"
)

// LogLine is a single line of output captured from a step.
type LogLine struct {
	ID     int64  `json:"id"`
	StepID int64  `json:"step_id"`
	RunID  int64  `json:"run_id"` // denorm
	Ts     int64  `json:"ts"`
	LineNo int    `json:"line_no"`
	Text   string `json:"text"`
}

// InsertLog appends a log line.
func InsertLog(db *sql.DB, l LogLine) error {
	_, err := db.Exec(
		`INSERT INTO logs(step_id,run_id,ts,line_no,text)
		 VALUES(?,?,?,?,?)`,
		l.StepID, l.RunID, l.Ts, l.LineNo, l.Text,
	)
	return err
}

// GetLogs returns log lines for a step with line_no greater than afterLineNo,
// ordered ascending. Pass afterLineNo=0 to retrieve all lines.
func GetLogs(db *sql.DB, stepID int64, afterLineNo int) ([]LogLine, error) {
	rows, err := db.Query(
		`SELECT id,step_id,run_id,ts,line_no,text
		 FROM logs
		 WHERE step_id=? AND line_no>?
		 ORDER BY line_no`,
		stepID, afterLineNo,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogLine
	for rows.Next() {
		var l LogLine
		if err := rows.Scan(&l.ID, &l.StepID, &l.RunID, &l.Ts, &l.LineNo, &l.Text); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// GetRunLogs returns all log lines for every step belonging to a run,
// ordered by step_id then line_no.  The denormalised run_id column makes this
// a single-table scan without a join.
func GetRunLogs(db *sql.DB, runID int64) ([]LogLine, error) {
	rows, err := db.Query(
		`SELECT id,step_id,run_id,ts,line_no,text
		 FROM logs
		 WHERE run_id=?
		 ORDER BY step_id, line_no`,
		runID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogLine
	for rows.Next() {
		var l LogLine
		if err := rows.Scan(&l.ID, &l.StepID, &l.RunID, &l.Ts, &l.LineNo, &l.Text); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
