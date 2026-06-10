package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	dbpkg "github.com/aedatum/runway/internal/db"
)

// getLogs handles GET /api/steps/{id}/logs
// Returns the full log as a JSON array of LogLine objects.
func (s *Server) getLogs(w http.ResponseWriter, r *http.Request) {
	stepID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	lines, err := dbpkg.GetLogs(s.db, stepID, after)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if lines == nil {
		lines = []dbpkg.LogLine{}
	}
	writeJSON(w, lines)
}

// streamLogs handles GET /api/steps/{id}/stream
// Streams new log lines as Server-Sent Events (SSE).
//
// Each event carries a JSON-encoded LogLine as its data payload, allowing
// the client to track line_no and reconnect cleanly.
//
// The stream closes when the client disconnects or when the step has reached a
// terminal state and all buffered lines have been delivered (signalled by an
// SSE "event: done" frame).
func (s *Server) streamLogs(w http.ResponseWriter, r *http.Request) {
	stepID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable Nginx buffering

	lastLine := 0
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			lines, err := dbpkg.GetLogs(s.db, stepID, lastLine)
			if err != nil {
				fmt.Fprintf(w, "event: error\ndata: %s\n\n", err.Error())
				flusher.Flush()
				return
			}
			for _, l := range lines {
				b, _ := json.Marshal(l)
				fmt.Fprintf(w, "data: %s\n\n", b)
				lastLine = l.LineNo
			}
			if len(lines) > 0 {
				flusher.Flush()
			}

			// Check whether the step has reached a terminal state; if so,
			// flush remaining lines (already delivered above) and close.
			step, err := dbpkg.GetStep(s.db, stepID)
			if err == nil && step != nil && isTerminal(step.Status) && len(lines) == 0 {
				fmt.Fprintf(w, "event: done\ndata: {\"step_id\":%d,\"status\":%q}\n\n",
					stepID, step.Status)
				flusher.Flush()
				return
			}
		}
	}
}

// isTerminal reports whether a Runway step/job/run status is a terminal one
// (i.e. the engine will not append more log lines).
func isTerminal(status string) bool {
	switch status {
	case "success", "failure", "cancelled", "skipped", "unknown":
		return true
	}
	return false
}
