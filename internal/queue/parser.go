package queue

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode"
)

// Parser reads act stdout lines and writes jobs, steps, and log lines into the
// DB in real time.
//
// Act output format (representative examples):
//
//	[JobName] ⭐ Run StepName
//	[JobName]   ✅  Success - StepName [0s]
//	[JobName]   ❌  Failure - StepName [1.234s]
//	[JobName] 🏁  Job succeeded
//	[JobName] 🏁  Job failed
//	Error: Job 'JobName' failed
//	[JobName]   | <arbitrary log line>
//
// Lines not matching a lifecycle event are stored as log lines for the current
// step (or a synthetic "output" step if no step is active yet).
type Parser struct {
	db    *sql.DB
	runID int64

	// per-job state (keyed by job name)
	jobs map[string]int64 // job name → job DB id

	// per-step state within the current job
	currentJobName  string
	currentJobID    int64
	currentStepName string
	currentStepID   int64
	currentLineNo   int
}

// NewParser constructs a parser for a given run.
func NewParser(db *sql.DB, runID int64) *Parser {
	return &Parser{
		db:    db,
		runID: runID,
		jobs:  make(map[string]int64),
	}
}

// Feed processes one line of act output.
func (p *Parser) Feed(line string) {
	jobName, rest, hasJob := parseJobPrefix(line)

	if hasJob {
		p.ensureJob(jobName)
		p.handleJobLine(jobName, rest, line)
	} else {
		p.handleBare(line)
	}
}

// Flush finalises any open step/job left running at process exit.
func (p *Parser) Flush() {
	if p.currentStepID != 0 {
		p.finishStep(p.currentStepID, "unknown")
	}
	for jobName, jobID := range p.jobs {
		var status string
		err := p.db.QueryRow(
			`SELECT status FROM jobs WHERE id=?`, jobID,
		).Scan(&status)
		if err == nil && (status == "running" || status == "pending") {
			_ = p.finishJobByID(jobID, jobName, "unknown")
		}
	}
}

// ─── line parsing ─────────────────────────────────────────────────────────────

// parseJobPrefix extracts "[JobName]" from the start of a line.
// Returns (jobName, remainder, true) on success.
func parseJobPrefix(line string) (jobName, rest string, ok bool) {
	line = stripAnsi(line)
	if !strings.HasPrefix(line, "[") {
		return "", line, false
	}
	end := strings.Index(line, "]")
	if end < 0 {
		return "", line, false
	}
	jobName = line[1:end]
	rest = strings.TrimSpace(line[end+1:])
	return jobName, rest, true
}

// handleJobLine dispatches a line that has a [JobName] prefix.
func (p *Parser) handleJobLine(jobName, rest, rawLine string) {
	// Strip leading icon characters (emoji are multi-byte; trim by rune class)
	content := strings.TrimLeftFunc(rest, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '\''
	})
	content = strings.TrimSpace(content)

	switch {
	case isRunLine(rest):
		// "⭐ Run StepName" — new step starting
		stepName := extractAfter(content, "Run ")
		p.startStep(jobName, stepName)

	case isSuccessLine(rest):
		// "✅  Success - StepName [Xs]"
		stepName := extractBetween(content, "Success - ", " [")
		if stepName == "" {
			stepName = extractAfter(content, "Success - ")
		}
		p.completeStep(jobName, stepName, "success")

	case isFailureLine(rest):
		// "❌  Failure - StepName [Xs]"
		stepName := extractBetween(content, "Failure - ", " [")
		if stepName == "" {
			stepName = extractAfter(content, "Failure - ")
		}
		p.completeStep(jobName, stepName, "failure")

	case isJobSucceeded(rest):
		p.finishJob(jobName, "success")

	case isJobFailed(rest):
		p.finishJob(jobName, "failure")

	default:
		// Ordinary log line
		p.appendLog(rawLine)
	}
}

// handleBare handles lines without a [JobName] prefix.
func (p *Parser) handleBare(line string) {
	// "Error: Job 'JobName' failed" — global failure signal
	if strings.HasPrefix(line, "Error:") && strings.Contains(line, "failed") {
		// Extract job name from "Error: Job 'JobName' failed"
		inner := strings.TrimPrefix(line, "Error: Job '")
		if idx := strings.Index(inner, "'"); idx >= 0 {
			jobName := inner[:idx]
			p.finishJob(jobName, "failure")
			return
		}
		// No recognisable job name — mark run as failed at DB level.
		_, _ = p.db.Exec(
			`UPDATE runs SET status='failure', finished_at=? WHERE id=?`,
			now(), p.runID,
		)
		return
	}
	p.appendLog(line)
}

// ─── step lifecycle ────────────────────────────────────────────────────────────

func (p *Parser) startStep(jobName, stepName string) {
	p.ensureJob(jobName)

	// Close previous step if open.
	if p.currentStepID != 0 && p.currentStepName != stepName {
		p.finishStep(p.currentStepID, "unknown")
		p.currentStepID = 0
	}

	jobID := p.jobs[jobName]
	t := now()
	res, err := p.db.Exec(
		`INSERT INTO steps(job_id, run_id, name, status, started_at) VALUES(?,?,?,?,?)`,
		jobID, p.runID, stepName, "running", t,
	)
	if err != nil {
		log.Printf("parser: insert step run=%d job=%s step=%s: %v", p.runID, jobName, stepName, err)
		return
	}
	stepID, _ := res.LastInsertId()

	p.currentJobName = jobName
	p.currentJobID = jobID
	p.currentStepName = stepName
	p.currentStepID = stepID
	p.currentLineNo = 0

	// Ensure the parent job is marked running.
	_, _ = p.db.Exec(
		`UPDATE jobs SET status='running', started_at=COALESCE(started_at,?) WHERE id=?`,
		t, jobID,
	)
}

func (p *Parser) completeStep(jobName, stepName, status string) {
	// Try to find the step by name in case the parser got out of sync.
	stepID := p.currentStepID
	if p.currentStepName != stepName {
		_ = p.db.QueryRow(
			`SELECT id FROM steps WHERE run_id=? AND name=? ORDER BY id DESC LIMIT 1`,
			p.runID, stepName,
		).Scan(&stepID)
	}
	if stepID != 0 {
		p.finishStep(stepID, status)
	}
	if p.currentStepID == stepID {
		p.currentStepID = 0
		p.currentStepName = ""
		p.currentLineNo = 0
	}
}

func (p *Parser) finishStep(stepID int64, status string) {
	_, _ = p.db.Exec(
		`UPDATE steps SET status=?, finished_at=? WHERE id=?`,
		status, now(), stepID,
	)
}

// ─── job lifecycle ─────────────────────────────────────────────────────────────

// ensureJob creates a job row if one does not already exist for jobName.
func (p *Parser) ensureJob(jobName string) {
	if _, exists := p.jobs[jobName]; exists {
		return
	}
	res, err := p.db.Exec(
		`INSERT INTO jobs(run_id, name, status) VALUES(?,?,?)`,
		p.runID, jobName, "pending",
	)
	if err != nil {
		log.Printf("parser: insert job run=%d name=%s: %v", p.runID, jobName, err)
		return
	}
	id, _ := res.LastInsertId()
	p.jobs[jobName] = id
}

func (p *Parser) finishJob(jobName, status string) {
	jobID, ok := p.jobs[jobName]
	if !ok {
		return
	}
	_ = p.finishJobByID(jobID, jobName, status)
}

func (p *Parser) finishJobByID(jobID int64, jobName, status string) error {
	// Close any open step for this job.
	if p.currentJobName == jobName && p.currentStepID != 0 {
		stepStatus := "success"
		if status == "failure" {
			stepStatus = "failure"
		}
		p.finishStep(p.currentStepID, stepStatus)
		p.currentStepID = 0
		p.currentStepName = ""
	}

	_, err := p.db.Exec(
		`UPDATE jobs SET status=?, finished_at=? WHERE id=?`,
		status, now(), jobID,
	)
	return err
}

// ─── log lines ────────────────────────────────────────────────────────────────

// appendLog stores a log line for the currently active step.  If no step is
// open yet (e.g. preamble output before any "Run" line) it creates a synthetic
// "output" step so logs are never lost.
func (p *Parser) appendLog(line string) {
	if p.currentStepID == 0 {
		p.ensureSyntheticStep()
	}
	if p.currentStepID == 0 {
		return // still couldn't create one — give up
	}
	p.currentLineNo++
	_, err := p.db.Exec(
		`INSERT INTO logs(step_id, run_id, ts, line_no, text) VALUES(?,?,?,?,?)`,
		p.currentStepID, p.runID, time.Now().Unix(), p.currentLineNo, line,
	)
	if err != nil {
		log.Printf("parser: insert log run=%d step=%d: %v", p.runID, p.currentStepID, err)
	}
}

// ensureSyntheticStep creates or re-uses a catchall "output" step (job "act")
// for lines that arrive before any proper step has started.
func (p *Parser) ensureSyntheticStep() {
	const syntheticJob = "act"
	const syntheticStep = "output"

	p.ensureJob(syntheticJob)
	jobID := p.jobs[syntheticJob]

	// Try to reuse an existing synthetic step.
	var existingID int64
	_ = p.db.QueryRow(
		`SELECT id FROM steps WHERE run_id=? AND job_id=? AND name=? LIMIT 1`,
		p.runID, jobID, syntheticStep,
	).Scan(&existingID)

	if existingID != 0 {
		p.currentJobName = syntheticJob
		p.currentJobID = jobID
		p.currentStepName = syntheticStep
		p.currentStepID = existingID
		return
	}

	// Create it fresh.
	t := now()
	res, err := p.db.Exec(
		`INSERT INTO steps(job_id, run_id, name, status, started_at) VALUES(?,?,?,?,?)`,
		jobID, p.runID, syntheticStep, "running", t,
	)
	if err != nil {
		log.Printf("parser: insert synthetic step run=%d: %v", p.runID, err)
		return
	}
	id, _ := res.LastInsertId()
	p.currentJobName = syntheticJob
	p.currentJobID = jobID
	p.currentStepName = syntheticStep
	p.currentStepID = id
	_, _ = p.db.Exec(
		`UPDATE jobs SET status='running', started_at=COALESCE(started_at,?) WHERE id=?`,
		t, jobID,
	)
}

// ─── pattern helpers ──────────────────────────────────────────────────────────

func isRunLine(s string) bool {
	return containsRune(s, '⭐') && strings.Contains(s, "Run ")
}

func isSuccessLine(s string) bool {
	return containsRune(s, '✅') && strings.Contains(s, "Success")
}

func isFailureLine(s string) bool {
	return containsRune(s, '❌') && strings.Contains(s, "Failure")
}

func isJobSucceeded(s string) bool {
	return containsRune(s, '🏁') && strings.Contains(s, "Job succeeded")
}

func isJobFailed(s string) bool {
	return containsRune(s, '🏁') && strings.Contains(s, "Job failed")
}

func containsRune(s string, r rune) bool {
	return strings.ContainsRune(s, r)
}

// extractAfter returns the portion of s after the first occurrence of prefix,
// trimmed of whitespace.
func extractAfter(s, prefix string) string {
	idx := strings.Index(s, prefix)
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(s[idx+len(prefix):])
}

// extractBetween returns the substring between open and close markers.
func extractBetween(s, open, close string) string {
	start := strings.Index(s, open)
	if start < 0 {
		return ""
	}
	start += len(open)
	end := strings.Index(s[start:], close)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(s[start : start+end])
}

// stripAnsi removes ANSI escape sequences from s.
func stripAnsi(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			// Consume until the final byte of the escape sequence (a letter).
			i += 2
			for i < len(s) && (s[i] < 'A' || s[i] > 'z') {
				i++
			}
			i++ // skip the terminating letter
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// Ensure fmt is used (it is used in engine.go via fmt.Sprintf; import here for
// correctness if the file is compiled standalone).
var _ = fmt.Sprintf
