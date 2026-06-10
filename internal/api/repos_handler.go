package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	dbpkg "github.com/aedatum/runway/internal/db"
)

// ── Repo CRUD ─────────────────────────────────────────────────────────────────

// listRepos handles GET /api/repos
func (s *Server) listRepos(w http.ResponseWriter, r *http.Request) {
	repos, err := dbpkg.ListRepos(s.db)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if repos == nil {
		repos = []dbpkg.Repo{}
	}
	writeJSON(w, repos)
}

// createRepoRequest is the JSON body for POST /api/repos.
type createRepoRequest struct {
	Name          string  `json:"name"`
	Owner         string  `json:"owner"`
	GitURL        string  `json:"git_url"`
	DefaultBranch string  `json:"default_branch"`
	DeployKey     *string `json:"deploy_key,omitempty"`
}

// createRepo handles POST /api/repos
func (s *Server) createRepo(w http.ResponseWriter, r *http.Request) {
	var req createRepoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Name == "" || req.Owner == "" || req.GitURL == "" {
		writeError(w, http.StatusUnprocessableEntity, "name, owner and git_url are required")
		return
	}
	if req.DefaultBranch == "" {
		req.DefaultBranch = "main"
	}

	repo := dbpkg.Repo{
		Name:          req.Name,
		Owner:         req.Owner,
		GitURL:        req.GitURL,
		DefaultBranch: req.DefaultBranch,
		DeployKey:     req.DeployKey,
		CreatedAt:     time.Now().Unix(),
	}
	id, err := dbpkg.InsertRepo(s.db, repo)
	if err != nil {
		// Treat unique-constraint violations as a 409.
		if strings.Contains(err.Error(), "UNIQUE") {
			writeError(w, http.StatusConflict, "repo already registered")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	repo.ID = id
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(repo) //nolint:errcheck
}

// getRepo handles GET /api/repos/{id}
func (s *Server) getRepo(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	repo, err := dbpkg.GetRepo(s.db, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if repo == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, repo)
}

// deleteRepo handles DELETE /api/repos/{id}
func (s *Server) deleteRepo(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := dbpkg.DeleteRepo(s.db, id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Workflow listing + sync ───────────────────────────────────────────────────

// listRepoWorkflows handles GET /api/repos/{id}/workflows
// Uses the DB workflows table (populated by syncRepo / the engine).
func (s *Server) listRepoWorkflows(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	repo, err := dbpkg.GetRepo(s.db, id)
	if err != nil || repo == nil {
		writeError(w, http.StatusNotFound, "repo not found")
		return
	}
	workflows, err := dbpkg.ScanWorkflows(s.db, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if workflows == nil {
		workflows = []dbpkg.WorkflowSummary{}
	}
	writeJSON(w, workflows)
}

// syncRepo handles POST /api/repos/{id}/sync
// Re-scans the on-disk .github/workflows/ directory and upserts records into
// the workflows table.  Requires the repo to have been cloned (clone_path set).
func (s *Server) syncRepo(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	repo, err := dbpkg.GetRepo(s.db, id)
	if err != nil || repo == nil {
		writeError(w, http.StatusNotFound, "repo not found")
		return
	}

	if repo.ClonePath == nil || *repo.ClonePath == "" {
		writeError(w, http.StatusConflict,
			"repo has not been cloned yet — enqueue a dispatch first")
		return
	}

	wfDir := filepath.Join(*repo.ClonePath, ".github", "workflows")
	entries, err := os.ReadDir(wfDir)
	if err != nil {
		// No workflows directory — that's fine; return empty list.
		writeJSON(w, []dbpkg.WorkflowSummary{})
		return
	}

	var upserted int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		wfName := yamlNameField(filepath.Join(wfDir, name))
		if wfName == "" {
			wfName = strings.TrimSuffix(strings.TrimSuffix(name, ".yaml"), ".yml")
		}
		if err := dbpkg.UpsertWorkflow(s.db, id, name, wfName); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		upserted++
	}

	workflows, err := dbpkg.ScanWorkflows(s.db, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if workflows == nil {
		workflows = []dbpkg.WorkflowSummary{}
	}
	writeJSON(w, map[string]any{
		"synced":    upserted,
		"workflows": workflows,
	})
}

// yamlNameField reads the top-level `name:` field from a workflow YAML file
// without a full YAML parse.
func yamlNameField(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "name:"))
			val = strings.Trim(val, `"'`)
			return val
		}
	}
	return ""
}
