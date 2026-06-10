package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	dbpkg "github.com/LogicalSapien/runway/internal/db"
)

// ── GitHub-compatible response shapes ────────────────────────────────────────

// ghRun matches GitHub's WorkflowRun object shape (subset used by tooling).
type ghRun struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	Conclusion *string `json:"conclusion"`
	WorkflowID int64   `json:"workflow_id"`
	HeadBranch string  `json:"head_branch"`
	HeadSHA    string  `json:"head_sha"`
	RunNumber  int64   `json:"run_number"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
	HTMLURL    string  `json:"html_url"`
}

func toGHRun(r dbpkg.Run, baseURL string) ghRun {
	// Map Runway status → GitHub status/conclusion
	status, conclusion := runwayStatusToGH(r.Status)

	var headSHA string
	if r.CommitSHA != nil {
		headSHA = *r.CommitSHA
	}

	createdAt := time.Unix(r.CreatedAt, 0).UTC().Format(time.RFC3339)
	updatedAt := createdAt
	if r.FinishedAt != nil {
		updatedAt = time.Unix(*r.FinishedAt, 0).UTC().Format(time.RFC3339)
	} else if r.StartedAt != nil {
		updatedAt = time.Unix(*r.StartedAt, 0).UTC().Format(time.RFC3339)
	}

	return ghRun{
		ID:         r.ID,
		Name:       r.Workflow,
		Status:     status,
		Conclusion: conclusion,
		WorkflowID: r.ID, // Runway doesn't have separate workflow IDs; use run ID
		HeadBranch: r.Branch,
		HeadSHA:    headSHA,
		RunNumber:  r.ID,
		CreatedAt:  createdAt,
		UpdatedAt:  updatedAt,
		HTMLURL:    fmt.Sprintf("%s/runs/%d", strings.TrimRight(baseURL, "/"), r.ID),
	}
}

// runwayStatusToGH converts a Runway status string to the GitHub
// (status, conclusion) pair.
func runwayStatusToGH(s string) (status string, conclusion *string) {
	switch s {
	case "queued":
		return "queued", nil
	case "running":
		return "in_progress", nil
	case "success":
		c := "success"
		return "completed", &c
	case "failure":
		c := "failure"
		return "completed", &c
	case "cancelled":
		c := "cancelled"
		return "completed", &c
	default:
		c := "neutral"
		return "completed", &c
	}
}

func baseURL(r *http.Request) string {
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") == "" {
		scheme = "http"
	}
	return scheme + "://" + r.Host
}

// ── GitHub-compatible handlers ────────────────────────────────────────────────

// ghListRuns handles GET /repos/{owner}/{repo}/actions/runs
func (s *Server) ghListRuns(w http.ResponseWriter, r *http.Request) {
	owner := r.PathValue("owner")
	repoName := r.PathValue("repo")
	repo := owner + "/" + repoName

	limit, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if limit <= 0 {
		limit = 30
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	filter := dbpkg.ListRunsFilter{Repo: repo, Limit: limit, Offset: (page - 1) * limit}
	if wf := r.URL.Query().Get("workflow_id"); wf != "" {
		filter.Workflow = wf
	}
	runs, err := dbpkg.ListRuns(s.db, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	base := baseURL(r)
	out := make([]ghRun, len(runs))
	for i, run := range runs {
		out[i] = toGHRun(run, base)
	}
	writeJSON(w, map[string]any{
		"total_count":   len(out),
		"workflow_runs": out,
	})
}

// ghGetRun handles GET /repos/{owner}/{repo}/actions/runs/{run_id}
func (s *Server) ghGetRun(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("run_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid run_id")
		return
	}
	run, err := dbpkg.GetRun(s.db, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if run == nil {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	writeJSON(w, toGHRun(*run, baseURL(r)))
}

// ── Runway-native run handlers ────────────────────────────────────────────────

// listRuns handles GET /api/runs
func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	filter := dbpkg.ListRunsFilter{
		Repo:     r.URL.Query().Get("repo"),
		Workflow: r.URL.Query().Get("workflow"),
		Status:   r.URL.Query().Get("status"),
		Since:    since,
		Limit:    limit,
		Offset:   offset,
	}
	runs, err := dbpkg.ListRuns(s.db, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if runs == nil {
		runs = []dbpkg.Run{}
	}
	if total, err := dbpkg.CountRuns(s.db, filter); err == nil {
		w.Header().Set("X-Total-Count", strconv.Itoa(total))
	}
	writeJSON(w, runs)
}

// getRun handles GET /api/runs/{id}
func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	run, err := dbpkg.GetRun(s.db, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if run == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, run)
}

// listRunJobs handles GET /api/runs/{id}/jobs
func (s *Server) listRunJobs(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	jobs, err := dbpkg.ListJobsByRun(s.db, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if jobs == nil {
		jobs = []dbpkg.Job{}
	}
	writeJSON(w, jobs)
}

// getRunLogs handles GET /api/runs/{id}/logs — returns plain-text log for whole run.
func (s *Server) getRunLogs(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	lines, err := dbpkg.GetRunLogs(s.db, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for _, l := range lines {
		fmt.Fprintln(w, l.Text)
	}
}
