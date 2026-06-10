package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"regexp"

	dbpkg "github.com/aedatum/runway/internal/db"
)

// Secret/variable names follow GitHub's rules: alphanumeric + underscore, no
// leading digit, not GITHUB_-prefixed.
var secretNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validSecretName(name string) bool {
	return len(name) <= 200 && secretNameRe.MatchString(name)
}

// repoFromPath resolves {owner}/{repo} or 404s.
func (s *Server) repoFromPath(w http.ResponseWriter, r *http.Request) *dbpkg.Repo {
	repo, err := dbpkg.GetRepoByOwnerName(s.db, r.PathValue("owner"), r.PathValue("repo"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil
	}
	if repo == nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return nil
	}
	return repo
}

// envFromPath returns the environment scope ("" for repo-level routes) and
// validates it exists when non-empty.
func (s *Server) envFromPath(w http.ResponseWriter, r *http.Request, repoID int64) (string, bool) {
	env := r.PathValue("environment")
	if env == "" {
		return "", true
	}
	envs, err := dbpkg.ListEnvironments(s.db, repoID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return "", false
	}
	for _, e := range envs {
		if e == env {
			return env, true
		}
	}
	writeError(w, http.StatusNotFound, "environment not found — create it with PUT /repos/{owner}/{repo}/environments/{name}")
	return "", false
}

// ── secrets ──────────────────────────────────────────────────────────────────

// getPublicKey handles GET /repos/{owner}/{repo}/actions/secrets/public-key.
// Clients seal secret values against this key (libsodium crypto_box_seal),
// exactly like the GitHub API. Runway also accepts plaintext "value" as an
// extension for simple curl/UI use.
func (s *Server) getPublicKey(w http.ResponseWriter, r *http.Request) {
	if s.cipher == nil {
		writeError(w, http.StatusServiceUnavailable, "secrets are not configured")
		return
	}
	pub := s.cipher.PublicKey()
	writeJSON(w, map[string]string{
		"key_id": s.cipher.KeyID(),
		"key":    base64.StdEncoding.EncodeToString(pub[:]),
	})
}

func (s *Server) listSecrets(w http.ResponseWriter, r *http.Request) {
	repo := s.repoFromPath(w, r)
	if repo == nil {
		return
	}
	env, ok := s.envFromPath(w, r, repo.ID)
	if !ok {
		return
	}
	secrets, err := dbpkg.ListSecrets(s.db, repo.ID, env)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"total_count": len(secrets), "secrets": secrets})
}

func (s *Server) getSecret(w http.ResponseWriter, r *http.Request) {
	repo := s.repoFromPath(w, r)
	if repo == nil {
		return
	}
	env, ok := s.envFromPath(w, r, repo.ID)
	if !ok {
		return
	}
	sec, err := dbpkg.GetSecret(s.db, repo.ID, env, r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sec == nil {
		writeError(w, http.StatusNotFound, "secret not found")
		return
	}
	writeJSON(w, sec)
}

// putSecret handles PUT .../secrets/{name} with either GitHub's sealed-box
// body {"encrypted_value": "<base64>", "key_id": "..."} or the Runway
// extension {"value": "<plaintext>"} (HTTPS protects it in transit).
func (s *Server) putSecret(w http.ResponseWriter, r *http.Request) {
	repo := s.repoFromPath(w, r)
	if repo == nil {
		return
	}
	env, ok := s.envFromPath(w, r, repo.ID)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if !validSecretName(name) {
		writeError(w, http.StatusUnprocessableEntity, "invalid secret name (letters, digits, underscore; no leading digit)")
		return
	}
	if s.cipher == nil {
		writeError(w, http.StatusServiceUnavailable, "secrets are not configured")
		return
	}

	var body struct {
		Value          string `json:"value"`
		EncryptedValue string `json:"encrypted_value"`
		KeyID          string `json:"key_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	value := body.Value
	if value == "" && body.EncryptedValue != "" {
		sealed, err := base64.StdEncoding.DecodeString(body.EncryptedValue)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "encrypted_value is not valid base64")
			return
		}
		value, err = s.cipher.OpenSealedBox(sealed)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	}
	if value == "" {
		writeError(w, http.StatusUnprocessableEntity, "provide value (plaintext) or encrypted_value (sealed box)")
		return
	}

	existing, err := dbpkg.GetSecret(s.db, repo.ID, env, name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	enc, err := s.cipher.Encrypt(value)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encryption failed")
		return
	}
	if err := dbpkg.UpsertSecret(s.db, repo.ID, env, name, enc); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing == nil {
		w.WriteHeader(http.StatusCreated) // GitHub: 201 created, 204 updated
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteSecret(w http.ResponseWriter, r *http.Request) {
	repo := s.repoFromPath(w, r)
	if repo == nil {
		return
	}
	env, ok := s.envFromPath(w, r, repo.ID)
	if !ok {
		return
	}
	deleted, err := dbpkg.DeleteSecret(s.db, repo.ID, env, r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "secret not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── variables ────────────────────────────────────────────────────────────────

func (s *Server) listVariables(w http.ResponseWriter, r *http.Request) {
	repo := s.repoFromPath(w, r)
	if repo == nil {
		return
	}
	env, ok := s.envFromPath(w, r, repo.ID)
	if !ok {
		return
	}
	vars, err := dbpkg.ListVariables(s.db, repo.ID, env)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"total_count": len(vars), "variables": vars})
}

func (s *Server) getVariable(w http.ResponseWriter, r *http.Request) {
	repo := s.repoFromPath(w, r)
	if repo == nil {
		return
	}
	env, ok := s.envFromPath(w, r, repo.ID)
	if !ok {
		return
	}
	v, err := dbpkg.GetVariable(s.db, repo.ID, env, r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if v == nil {
		writeError(w, http.StatusNotFound, "variable not found")
		return
	}
	writeJSON(w, v)
}

// createVariable handles POST .../variables {"name": ..., "value": ...}.
func (s *Server) createVariable(w http.ResponseWriter, r *http.Request) {
	s.upsertVariable(w, r, "", http.StatusCreated)
}

// patchVariable handles PATCH .../variables/{name} {"value": ...}.
func (s *Server) patchVariable(w http.ResponseWriter, r *http.Request) {
	s.upsertVariable(w, r, r.PathValue("name"), http.StatusNoContent)
}

func (s *Server) upsertVariable(w http.ResponseWriter, r *http.Request, name string, okStatus int) {
	repo := s.repoFromPath(w, r)
	if repo == nil {
		return
	}
	env, ok := s.envFromPath(w, r, repo.ID)
	if !ok {
		return
	}
	var body struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if name == "" {
		name = body.Name
	}
	if !validSecretName(name) {
		writeError(w, http.StatusUnprocessableEntity, "invalid variable name")
		return
	}
	if err := dbpkg.UpsertVariable(s.db, repo.ID, env, name, body.Value); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(okStatus)
}

func (s *Server) deleteVariable(w http.ResponseWriter, r *http.Request) {
	repo := s.repoFromPath(w, r)
	if repo == nil {
		return
	}
	env, ok := s.envFromPath(w, r, repo.ID)
	if !ok {
		return
	}
	deleted, err := dbpkg.DeleteVariable(s.db, repo.ID, env, r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "variable not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── environments ─────────────────────────────────────────────────────────────

func (s *Server) listEnvs(w http.ResponseWriter, r *http.Request) {
	repo := s.repoFromPath(w, r)
	if repo == nil {
		return
	}
	envs, err := dbpkg.ListEnvironments(s.db, repo.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type envOut struct {
		Name string `json:"name"`
	}
	out := make([]envOut, 0, len(envs))
	for _, e := range envs {
		out = append(out, envOut{Name: e})
	}
	writeJSON(w, map[string]any{"total_count": len(out), "environments": out})
}

func (s *Server) putEnv(w http.ResponseWriter, r *http.Request) {
	repo := s.repoFromPath(w, r)
	if repo == nil {
		return
	}
	name := r.PathValue("environment")
	if !validSecretName(name) && !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`).MatchString(name) {
		writeError(w, http.StatusUnprocessableEntity, "invalid environment name")
		return
	}
	if err := dbpkg.UpsertEnvironment(s.db, repo.ID, name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]string{"name": name})
}

func (s *Server) deleteEnv(w http.ResponseWriter, r *http.Request) {
	repo := s.repoFromPath(w, r)
	if repo == nil {
		return
	}
	deleted, err := dbpkg.DeleteEnvironment(s.db, repo.ID, r.PathValue("environment"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "environment not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
