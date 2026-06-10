package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	dbpkg "github.com/aedatum/runway/internal/db"
	"github.com/aedatum/runway/internal/queue"
	"github.com/aedatum/runway/internal/validate"
)

// pushTriggerRe detects an `on: push` trigger in workflow YAML (plain, list,
// or block form) without a YAML parser.
var pushTriggerRe = regexp.MustCompile(`(?m)^(on:\s*(\[?[^\n]*\bpush\b|push\s*$)|\s{1,4}push\s*:)`)

// githubWebhook handles POST /webhooks/github. Auth is the HMAC signature
// (X-Hub-Signature-256) computed with the webhook_secret setting — the path
// is exempt from session/Bearer auth. Only push events enqueue runs.
func (s *Server) githubWebhook(w http.ResponseWriter, r *http.Request) {
	secret, _ := dbpkg.GetSetting(s.db, "webhook_secret", "")
	if secret == "" {
		writeError(w, http.StatusServiceUnavailable, "set the webhook_secret setting first")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body")
		return
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(r.Header.Get("X-Hub-Signature-256"))) {
		writeError(w, http.StatusUnauthorized, "bad signature")
		return
	}

	if ev := r.Header.Get("X-GitHub-Event"); ev != "push" {
		writeJSON(w, map[string]string{"ignored": ev})
		return
	}
	var p struct {
		Ref        string `json:"ref"`
		Deleted    bool   `json:"deleted"`
		Repository struct {
			Name  string `json:"name"`
			Owner struct {
				Name  string `json:"name"`
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}
	branch := strings.TrimPrefix(p.Ref, "refs/heads/")
	owner := p.Repository.Owner.Name
	if owner == "" {
		owner = p.Repository.Owner.Login
	}
	if p.Deleted || branch == p.Ref || !validate.Ref(branch) {
		writeJSON(w, map[string]string{"ignored": "non-branch or deleted ref"})
		return
	}
	repo, err := dbpkg.GetRepoByOwnerName(s.db, owner, p.Repository.Name)
	if err != nil || repo == nil {
		writeError(w, http.StatusNotFound, "repository not registered with runway")
		return
	}

	// Enqueue every workflow in the checkout whose YAML declares a push trigger.
	enqueued := []string{}
	if repo.ClonePath != nil && *repo.ClonePath != "" {
		wfDir := filepath.Join(*repo.ClonePath, ".github", "workflows")
		entries, _ := os.ReadDir(wfDir)
		for _, e := range entries {
			name := e.Name()
			if !validate.WorkflowFile(name) {
				continue
			}
			b, err := os.ReadFile(filepath.Join(wfDir, name))
			if err != nil || !pushTriggerRe.Match(b) {
				continue
			}
			if _, err := dbpkg.Enqueue(s.db, dbpkg.QueueItem{
				RepoID: repo.ID, WorkflowFile: name, Branch: branch, Event: "push",
				CreatedAt: time.Now().Unix(),
			}); err == nil {
				enqueued = append(enqueued, name)
			}
		}
	}
	writeJSON(w, map[string]any{"enqueued": enqueued, "branch": branch})
}

// rerunRun handles POST /repos/{owner}/{repo}/actions/runs/{run_id}/rerun.
func (s *Server) rerunRun(w http.ResponseWriter, r *http.Request) {
	repo := s.repoFromPath(w, r)
	if repo == nil {
		return
	}
	runID, _ := strconv.ParseInt(r.PathValue("run_id"), 10, 64)
	var file, branch, event string
	var inputs *string
	err := s.db.QueryRow(`
		SELECT r.workflow_file, r.branch, r.trigger, q.inputs
		FROM runs r LEFT JOIN queue q ON q.run_id = r.id
		WHERE r.id=? AND r.repo_id=?`, runID, repo.ID).
		Scan(&file, &branch, &event, &inputs)
	if err != nil {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	if event != "push" {
		event = "workflow_dispatch"
	}
	qID, err := dbpkg.Enqueue(s.db, dbpkg.QueueItem{
		RepoID: repo.ID, WorkflowFile: file, Branch: branch, Inputs: inputs,
		Event: event, CreatedAt: time.Now().Unix(),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("X-Runway-Queue-URL", fmt.Sprintf("/api/queue/%d", qID))
	w.WriteHeader(http.StatusCreated) // GitHub returns 201
}

// cancelRun handles POST /repos/{owner}/{repo}/actions/runs/{run_id}/cancel.
func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	if s.repoFromPath(w, r) == nil {
		return
	}
	runID, _ := strconv.ParseInt(r.PathValue("run_id"), 10, 64)
	if !queue.CancelRun(runID) {
		writeError(w, http.StatusConflict, "run is not currently executing")
		return
	}
	w.WriteHeader(http.StatusAccepted) // GitHub returns 202
}

// badge handles GET .../workflows/{workflow_file}/badge.svg — unauthenticated
// shields-style status badge for the latest run of that workflow.
func (s *Server) badge(w http.ResponseWriter, r *http.Request) {
	repo, err := dbpkg.GetRepoByOwnerName(s.db, r.PathValue("owner"), r.PathValue("repo"))
	status := "unknown"
	if err == nil && repo != nil {
		var st string
		if err := s.db.QueryRow(`
			SELECT status FROM runs WHERE repo_id=? AND workflow_file=?
			ORDER BY id DESC LIMIT 1`, repo.ID, r.PathValue("workflow_file")).Scan(&st); err == nil {
			status = st
		}
	}
	color := map[string]string{
		"success": "#22c55e", "failure": "#ef4444", "running": "#f59e0b",
		"cancelled": "#64748b", "unknown": "#64748b", "queued": "#64748b",
	}[status]
	if color == "" {
		color = "#64748b"
	}
	label := workflowLabel(r.PathValue("workflow_file"))
	lw, sw := 6*len(label)+12, 6*len(status)+12
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "no-cache, max-age=60")
	fmt.Fprintf(w, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="20" role="img" aria-label="%s: %s">`+
		`<rect width="%d" height="20" fill="#555"/><rect x="%d" width="%d" height="20" fill="%s"/>`+
		`<g fill="#fff" font-family="Verdana,sans-serif" font-size="11">`+
		`<text x="%d" y="14" text-anchor="middle">%s</text>`+
		`<text x="%d" y="14" text-anchor="middle">%s</text></g></svg>`,
		lw+sw, label, status, lw, lw, sw, color, lw/2, label, lw+sw/2, status)
}

func workflowLabel(file string) string {
	return strings.TrimSuffix(strings.TrimSuffix(file, ".yml"), ".yaml")
}

// listArtifacts handles GET /api/runs/{id}/artifacts; downloadArtifact serves
// GET /api/runs/{id}/artifacts/{path...} from the per-run artifact dir.
func (s *Server) listArtifacts(w http.ResponseWriter, r *http.Request) {
	dir, ok := s.artifactRunDir(w, r)
	if !ok {
		return
	}
	type af struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	out := []af{}
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, af{Path: rel, Size: info.Size()})
		}
		return nil
	})
	writeJSON(w, out)
}

func (s *Server) downloadArtifact(w http.ResponseWriter, r *http.Request) {
	dir, ok := s.artifactRunDir(w, r)
	if !ok {
		return
	}
	rel := filepath.FromSlash(strings.Trim(r.PathValue("path"), "/"))
	target := filepath.Join(dir, rel)
	if !strings.HasPrefix(target, dir+string(filepath.Separator)) {
		writeError(w, http.StatusUnprocessableEntity, "invalid path")
		return
	}
	http.ServeFile(w, r, target)
}

func (s *Server) artifactRunDir(w http.ResponseWriter, r *http.Request) (string, bool) {
	base, _ := dbpkg.GetSetting(s.db, "artifacts_dir", "")
	if base == "" {
		writeError(w, http.StatusServiceUnavailable, "set the artifacts_dir setting to enable artifacts")
		return "", false
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return "", false
	}
	dir, err := filepath.Abs(filepath.Join(base, fmt.Sprintf("run-%d", id)))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return "", false
	}
	if _, err := os.Stat(dir); err != nil {
		writeError(w, http.StatusNotFound, "no artifacts for this run")
		return "", false
	}
	return dir, true
}
