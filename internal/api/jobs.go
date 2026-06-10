package api

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	dbpkg "github.com/LogicalSapien/runway/internal/db"
)

// ── GitHub-compatible response shapes ────────────────────────────────────────

// ghStep matches GitHub's WorkflowStep object shape.
type ghStep struct {
	Name        string  `json:"name"`
	Status      string  `json:"status"`
	Conclusion  *string `json:"conclusion"`
	Number      int64   `json:"number"`
	StartedAt   *string `json:"started_at"`
	CompletedAt *string `json:"completed_at"`
}

// ghJob matches GitHub's WorkflowJob object shape.
type ghJob struct {
	ID          int64    `json:"id"`
	RunID       int64    `json:"run_id"`
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	Conclusion  *string  `json:"conclusion"`
	StartedAt   *string  `json:"started_at"`
	CompletedAt *string  `json:"completed_at"`
	Steps       []ghStep `json:"steps"`
}

func toGHJob(j dbpkg.Job, steps []dbpkg.Step) ghJob {
	status, conclusion := runwayStatusToGH(j.Status)

	var startedAt, completedAt *string
	if j.StartedAt != nil {
		s := time.Unix(*j.StartedAt, 0).UTC().Format(time.RFC3339)
		startedAt = &s
	}
	if j.FinishedAt != nil {
		s := time.Unix(*j.FinishedAt, 0).UTC().Format(time.RFC3339)
		completedAt = &s
	}

	ghSteps := make([]ghStep, len(steps))
	for i, st := range steps {
		sts, stc := runwayStatusToGH(st.Status)
		var stStart, stDone *string
		if st.StartedAt != nil {
			s := time.Unix(*st.StartedAt, 0).UTC().Format(time.RFC3339)
			stStart = &s
		}
		if st.FinishedAt != nil {
			s := time.Unix(*st.FinishedAt, 0).UTC().Format(time.RFC3339)
			stDone = &s
		}
		ghSteps[i] = ghStep{
			Name:        st.Name,
			Status:      sts,
			Conclusion:  stc,
			Number:      int64(i + 1),
			StartedAt:   stStart,
			CompletedAt: stDone,
		}
	}

	return ghJob{
		ID:          j.ID,
		RunID:       j.RunID,
		Name:        j.Name,
		Status:      status,
		Conclusion:  conclusion,
		StartedAt:   startedAt,
		CompletedAt: completedAt,
		Steps:       ghSteps,
	}
}

// ── GitHub-compatible handlers ────────────────────────────────────────────────

// ghListRunJobs handles GET /repos/{owner}/{repo}/actions/runs/{run_id}/jobs
func (s *Server) ghListRunJobs(w http.ResponseWriter, r *http.Request) {
	runID, err := strconv.ParseInt(r.PathValue("run_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid run_id")
		return
	}
	jobs, err := dbpkg.ListJobsByRun(s.db, runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ghJobs, err := enrichJobs(s, jobs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"total_count": len(ghJobs),
		"jobs":        ghJobs,
	})
}

// ghGetJob handles GET /repos/{owner}/{repo}/actions/jobs/{job_id}
func (s *Server) ghGetJob(w http.ResponseWriter, r *http.Request) {
	jobID, err := strconv.ParseInt(r.PathValue("job_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid job_id")
		return
	}
	job, err := dbpkg.GetJob(s.db, jobID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if job == nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	steps, err := dbpkg.ListStepsByJob(s.db, jobID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, toGHJob(*job, steps))
}

// ghGetJobLogs handles GET /repos/{owner}/{repo}/actions/jobs/{job_id}/logs
// Returns plain text (all step logs for the job concatenated).
func (s *Server) ghGetJobLogs(w http.ResponseWriter, r *http.Request) {
	jobID, err := strconv.ParseInt(r.PathValue("job_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid job_id")
		return
	}
	steps, err := dbpkg.ListStepsByJob(s.db, jobID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for _, step := range steps {
		lines, err := dbpkg.GetLogs(s.db, step.ID, 0)
		if err != nil {
			continue
		}
		for _, l := range lines {
			fmt.Fprintln(w, l.Text)
		}
	}
}

// ghGetRunLogs handles GET /repos/{owner}/{repo}/actions/runs/{run_id}/logs
// Returns plain text of all logs for the run.
func (s *Server) ghGetRunLogs(w http.ResponseWriter, r *http.Request) {
	runID, err := strconv.ParseInt(r.PathValue("run_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid run_id")
		return
	}
	lines, err := dbpkg.GetRunLogs(s.db, runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for _, l := range lines {
		fmt.Fprintln(w, l.Text)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func enrichJobs(s *Server, jobs []dbpkg.Job) ([]ghJob, error) {
	out := make([]ghJob, 0, len(jobs))
	for _, j := range jobs {
		steps, err := dbpkg.ListStepsByJob(s.db, j.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, toGHJob(j, steps))
	}
	return out, nil
}
