package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	dbpkg "github.com/LogicalSapien/runway/internal/db"
	"github.com/LogicalSapien/runway/internal/validate"
)

// dispatchBody is the JSON body accepted by the workflow dispatch endpoint.
// It mirrors the GitHub Actions API schema.
type dispatchBody struct {
	Ref    string            `json:"ref"`
	Inputs map[string]string `json:"inputs"`
}

// dispatch handles:
//
//	POST /repos/{owner}/{repo}/actions/workflows/{workflow_file}/dispatches
//
// It locates the registered repo by owner+name, validates the workflow file,
// enqueues a run, and returns 204 with a Location header pointing at the
// Runway-native run URL (populated once the engine picks it up).
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) {
	owner := r.PathValue("owner")
	repoName := r.PathValue("repo")
	workflowFile := r.PathValue("workflow_file")

	if !validate.WorkflowFile(workflowFile) {
		writeError(w, http.StatusUnprocessableEntity,
			"workflow file must be a plain .yml/.yaml filename (no path separators)")
		return
	}

	// Parse body (ref is required; inputs is optional).
	var body dispatchBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Ref == "" {
		writeError(w, http.StatusUnprocessableEntity, "ref is required")
		return
	}
	if !validate.Ref(body.Ref) {
		writeError(w, http.StatusUnprocessableEntity, "ref contains invalid characters")
		return
	}

	// Look up the registered repo.
	repo, err := dbpkg.GetRepoByOwnerName(s.db, owner, repoName)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if repo == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf(
			"repository %s/%s not found — register it via POST /api/repos first",
			owner, repoName,
		))
		return
	}

	// Encode inputs as a JSON blob (nil-safe).
	var inputsJSON *string
	if len(body.Inputs) > 0 {
		b, err := json.Marshal(body.Inputs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to encode inputs")
			return
		}
		s := string(b)
		inputsJSON = &s
	}

	qItem := dbpkg.QueueItem{
		RepoID:       repo.ID,
		WorkflowFile: workflowFile,
		Branch:       body.Ref,
		Inputs:       inputsJSON,
		CreatedAt:    time.Now().Unix(),
	}
	qID, err := dbpkg.Enqueue(s.db, qItem)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Return 204 with a header pointing at the queue item.
	// GitHub returns 204 for this endpoint; we add a non-standard header
	// so callers can poll the queue item.
	w.Header().Set(
		"X-Runway-Queue-URL",
		fmt.Sprintf("/api/queue/%d", qID),
	)
	w.WriteHeader(http.StatusNoContent)
}
