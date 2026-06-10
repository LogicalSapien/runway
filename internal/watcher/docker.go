package watcher

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"

	dbpkg "github.com/LogicalSapien/runway/internal/db"
)

type DockerWatcher struct {
	db      *sql.DB
	cli     *client.Client
	logsDir string // host path mounted at /runner/act-logs, or ""
}

func NewDockerWatcher(db *sql.DB) (*DockerWatcher, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	logsDir := "/runner/act-logs"
	if _, err := os.Stat(logsDir); os.IsNotExist(err) {
		logsDir = "" // not mounted or not yet created
	}
	w := &DockerWatcher{db: db, cli: cli, logsDir: logsDir}
	w.healStale()
	return w, nil
}

// healStale marks any runs/steps left in "running" state from a previous watcher
// session (e.g. runway restarted while a container was running or had already died).
func (w *DockerWatcher) healStale() {
	_, err := w.db.Exec(
		`UPDATE steps SET status='unknown', finished_at=strftime('%s','now')
		 WHERE status='running'`)
	if err != nil {
		log.Printf("healStale steps: %v", err)
	}
	_, err = w.db.Exec(
		`UPDATE runs SET status='unknown', finished_at=strftime('%s','now')
		 WHERE status='running'`)
	if err != nil {
		log.Printf("healStale runs: %v", err)
	}
}

func (w *DockerWatcher) Watch(ctx context.Context) {
	for {
		if err := w.watch(ctx); err != nil && ctx.Err() == nil {
			log.Printf("docker watcher error: %v — retrying in 5s", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		} else if ctx.Err() != nil {
			return
		}
	}
}

func (w *DockerWatcher) watch(ctx context.Context) error {
	f := filters.NewArgs()
	f.Add("type", "container")
	f.Add("event", "start")
	f.Add("event", "die")

	eventCh, errCh := w.cli.Events(ctx, types.EventsOptions{Filters: f})

	active := map[string]int64{} // containerID -> runID

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errCh:
			return err
		case ev := <-eventCh:
			name := ev.Actor.Attributes["name"]
			if !isActContainer(name) {
				continue
			}
			// Engine-dispatched runs are tracked by the queue engine itself —
			// capturing them here would duplicate every run. The watcher only
			// records manual act invocations (break-glass usage).
			if ev.Actor.Attributes["act.dispatcher"] == "runway" {
				continue
			}
			switch ev.Action {
			case "start":
				runID, err := w.startRun(ev, name)
				if err != nil {
					log.Printf("watcher: start run: %v", err)
					continue
				}
				active[ev.Actor.ID] = runID

			case "die":
				runID, ok := active[ev.Actor.ID]
				if !ok {
					continue
				}
				exitCode := 0
				if ec := ev.Actor.Attributes["exitCode"]; ec != "" {
					fmt.Sscanf(ec, "%d", &exitCode)
				}
				// act kills step containers with SIGKILL (exit 137) after every job,
				// regardless of success. Treat 0 and 137 as "check logs"; only treat
				// other non-zero exit codes as failure.
				status := "success"
				if exitCode != 0 && exitCode != 137 {
					status = "failure"
				}
				// Read logs after container dies — the act wrapper writes the log file
				// only after act exits, so we must wait until die before reading.
				containerID := ev.Actor.ID
				go func(cid string, rid int64, ec int, exitStatus string) {
					parsed := w.tailLogs(ctx, cid, rid)
					if parsed != "" {
						exitStatus = parsed
					}
					if err := dbpkg.FinishRun(w.db, rid, exitStatus); err != nil {
						log.Printf("watcher: finish run %d: %v", rid, err)
					}
				}(containerID, runID, exitCode, status)
				delete(active, ev.Actor.ID)
			}
		}
	}
}

func isActContainer(name string) bool {
	return strings.HasPrefix(name, "act-") || strings.Contains(name, "-act-")
}

func (w *DockerWatcher) startRun(ev events.Message, name string) (int64, error) {
	attrs := ev.Actor.Attributes
	repo, workflow := parseActContainerName(name)
	if attrs["act.workflow"] != "" {
		workflow = attrs["act.workflow"]
	}
	if attrs["act.repo"] != "" {
		repo = attrs["act.repo"]
	}
	sha := attrs["act.sha"]
	var shaPtr *string
	if sha != "" {
		shaPtr = &sha
	}
	ts := ev.TimeNano / 1e9
	run := dbpkg.Run{
		Repo:      repo,
		Workflow:  workflow,
		Trigger:   "act",
		Branch:    attrs["act.branch"],
		CommitSHA: shaPtr,
		StartedAt: &ts,
		Status:    "running",
	}
	return dbpkg.InsertRun(w.db, run)
}

// parseActContainerName extracts a human-readable repo and workflow name from
// act container names like "act-Build--Push-deploy-<40hexchars>" or
// "act-MyWorkflow-jobname-<hash>".
// act names containers as: act-<WorkflowName>-<JobName>-<containerHash>
// where WorkflowName has spaces replaced by hyphens and "--" between words.
func parseActContainerName(name string) (repo, workflow string) {
	// Strip leading "act-"
	s := strings.TrimPrefix(name, "act-")

	// Strip trailing 40-char hex hash (container ID suffix act appends)
	parts := strings.Split(s, "-")
	if len(parts) > 0 {
		last := parts[len(parts)-1]
		if len(last) >= 12 && isHex(last) {
			parts = parts[:len(parts)-1]
		}
	}
	s = strings.Join(parts, "-")

	// act uses "--" as word separator within a name segment; single "-" separates segments.
	// The pattern is: <WorkflowName>-<JobName> where each name uses "--" internally.
	// Split on single "-" (not "--") to find the boundary.
	segments := splitOnSingleDash(s)
	if len(segments) >= 2 {
		workflow = strings.ReplaceAll(segments[0], "--", " ")
		// Use job name as suffix if it adds info
		job := strings.ReplaceAll(segments[1], "--", " ")
		if job != "" && !strings.EqualFold(job, workflow) {
			workflow = workflow + " / " + job
		}
	} else {
		workflow = strings.ReplaceAll(s, "--", " ")
	}

	// Repo is unknown from container name alone — leave empty so the caller
	// can fall back to the act.repo label or the workflow name.
	return "", strings.TrimSpace(workflow)
}

// splitOnSingleDash splits on "-" that is not part of "--".
func splitOnSingleDash(s string) []string {
	// Replace "--" with a placeholder, split on "-", restore.
	const placeholder = "\x00"
	s = strings.ReplaceAll(s, "--", placeholder)
	parts := strings.SplitN(s, "-", 2)
	for i, p := range parts {
		parts[i] = strings.ReplaceAll(p, placeholder, "--")
	}
	return parts
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// tailLogs reads act output into the DB after the container has died and returns
// the workflow outcome. act drives step containers via docker exec — ContainerLogs
// returns nothing useful. Instead we read from a log file written by the act wrapper
// script at /runner/act-logs/<containerID>.log.
//
// This function is called AFTER the die event so the wrapper has had time to flush
// the log file. It waits up to 10s for the file to appear (in case the wrapper is
// still copying). Returns "success", "failure", or "".
func (w *DockerWatcher) tailLogs(ctx context.Context, containerID string, runID int64) string {
	stepID, err := dbpkg.InsertStep(w.db, dbpkg.Step{
		RunID:  runID,
		Name:   "output",
		Status: "running",
	})
	if err != nil {
		log.Printf("tailLogs: insert step: %v", err)
		return ""
	}

	var rc io.ReadCloser

	if w.logsDir != "" {
		logFile := filepath.Join(w.logsDir, containerID+".log")
		// Wait up to 10s for the wrapper to finish writing the log file
		for i := 0; i < 20; i++ {
			if ctx.Err() != nil {
				dbpkg.FinishStep(w.db, stepID, "unknown")
				return ""
			}
			if _, err := os.Stat(logFile); err == nil {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		f, err := os.Open(logFile)
		if err == nil {
			rc = f
		} else {
			log.Printf("tailLogs: log file not found for %s after 10s", containerID)
		}
	}

	if rc == nil {
		dbpkg.FinishStep(w.db, stepID, "unknown")
		return ""
	}
	defer rc.Close()

	lineNo := 0
	outcome := ""
	scanner := bufio.NewScanner(rc)
	scanner.Buffer(make([]byte, 512*1024), 512*1024)
	for scanner.Scan() {
		if ctx.Err() != nil {
			break
		}
		lineNo++
		line := strings.TrimRight(scanner.Text(), "\r\n")
		if err := dbpkg.InsertLog(w.db, dbpkg.LogLine{
			StepID: stepID,
			Ts:     time.Now().Unix(),
			LineNo: lineNo,
			Text:   line,
		}); err != nil {
			log.Printf("tailLogs: insert log: %v", err)
		}
		if strings.Contains(line, "Job succeeded") {
			outcome = "success"
		} else if strings.Contains(line, "Job failed") || strings.Contains(line, "job failed") {
			outcome = "failure"
		}
	}

	stepStatus := outcome
	if stepStatus == "" {
		stepStatus = "success"
	}
	if err := scanner.Err(); err != nil {
		log.Printf("tailLogs: scanner: %v", err)
		stepStatus = "failure"
	}
	dbpkg.FinishStep(w.db, stepID, stepStatus)
	return outcome
}
