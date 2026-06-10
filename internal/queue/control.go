package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// running maps run ID → cancel func for in-flight act processes, so the API
// can cancel a running run. In-memory only: after a restart nothing is
// running anyway (healStale marks leftovers failed).
var running sync.Map

func registerRun(runID int64, cancel context.CancelFunc) { running.Store(runID, cancel) }
func unregisterRun(runID int64)                          { running.Delete(runID) }

// CancelRun cancels a running run's act process. Returns false when the run
// is not currently executing on this instance.
func CancelRun(runID int64) bool {
	v, ok := running.Load(runID)
	if !ok {
		return false
	}
	v.(context.CancelFunc)()
	return true
}

// notify POSTs a JSON payload to the notify_webhook_url setting when a run
// finishes. notify_on controls which statuses fire (default: failure only;
// "all" = every run). The "text" field makes Slack incoming webhooks render
// without any mapping.
func (e *Engine) notify(qi QueueItem, runID int64, status string) {
	url := e.settingOrDefault("notify_webhook_url", "")
	if url == "" {
		return
	}
	mode := e.settingOrDefault("notify_on", "failure")
	if mode != "all" && status != "failure" {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"text": fmt.Sprintf("Runway: %s/%s %s — run #%d (%s@%s) %s",
			qi.RepoOwner, qi.RepoName, qi.WorkflowFile, runID, qi.Branch, qi.Event,
			strings.ToUpper(status)),
		"repo":     qi.RepoOwner + "/" + qi.RepoName,
		"workflow": qi.WorkflowFile,
		"run_id":   runID,
		"branch":   qi.Branch,
		"event":    qi.Event,
		"status":   status,
	})
	go func() {
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Post(url, "application/json", bytes.NewReader(payload))
		if err != nil {
			log.Printf("notify: %v", err)
			return
		}
		resp.Body.Close()
	}()
}
