// Package api wires up all HTTP handlers for Runway's native and
// GitHub-compatible APIs.
package api

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/LogicalSapien/runway/internal/middleware"
	"github.com/LogicalSapien/runway/internal/secrets"
	"github.com/LogicalSapien/runway/internal/ui"
)

// Server holds shared state (DB handle, secrets cipher) and the multiplexer.
type Server struct {
	db     *sql.DB
	cipher *secrets.Cipher
	mux    *http.ServeMux
}

// NewServer creates a Server, registers all routes, and returns it.
// The caller is responsible for wrapping the returned handler with auth
// middleware before passing it to http.Server.
func NewServer(db *sql.DB, cipher *secrets.Cipher) *Server {
	s := &Server{db: db, cipher: cipher, mux: http.NewServeMux()}
	s.routes()
	return s
}

// ServeHTTP satisfies http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// routes registers every route.  Auth is enforced by the middleware layer
// in main.go; here we just declare paths.
func (s *Server) routes() {
	// ── GitHub-compatible routes ──────────────────────────────────────────
	s.mux.HandleFunc(
		"POST /repos/{owner}/{repo}/actions/workflows/{workflow_file}/dispatches",
		s.dispatch,
	)
	s.mux.HandleFunc(
		"GET /repos/{owner}/{repo}/actions/runs",
		s.ghListRuns,
	)
	s.mux.HandleFunc(
		"GET /repos/{owner}/{repo}/actions/runs/{run_id}",
		s.ghGetRun,
	)
	s.mux.HandleFunc(
		"GET /repos/{owner}/{repo}/actions/runs/{run_id}/jobs",
		s.ghListRunJobs,
	)
	s.mux.HandleFunc(
		"GET /repos/{owner}/{repo}/actions/jobs/{job_id}",
		s.ghGetJob,
	)
	s.mux.HandleFunc(
		"GET /repos/{owner}/{repo}/actions/jobs/{job_id}/logs",
		s.ghGetJobLogs,
	)
	s.mux.HandleFunc(
		"GET /repos/{owner}/{repo}/actions/runs/{run_id}/logs",
		s.ghGetRunLogs,
	)

	// Webhooks (HMAC-authenticated), re-run/cancel, badges, artifacts.
	s.mux.HandleFunc("POST /webhooks/github", s.githubWebhook)
	s.mux.HandleFunc("POST /repos/{owner}/{repo}/actions/runs/{run_id}/rerun", s.rerunRun)
	s.mux.HandleFunc("POST /repos/{owner}/{repo}/actions/runs/{run_id}/cancel", s.cancelRun)
	s.mux.HandleFunc("GET  /repos/{owner}/{repo}/actions/workflows/{workflow_file}/badge.svg", s.badge)
	s.mux.HandleFunc("GET  /api/runs/{id}/artifacts", s.listArtifacts)
	s.mux.HandleFunc("GET  /api/runs/{id}/artifacts/{path...}", s.downloadArtifact)

	// Contents — basic GitHub-compatible read of the local checkout.
	s.mux.HandleFunc("GET /repos/{owner}/{repo}/contents", s.getContents)
	s.mux.HandleFunc("GET /repos/{owner}/{repo}/contents/{path...}", s.getContents)

	// Secrets / variables / environments (GitHub-compatible shapes).
	// Secret values are write-only; reads return metadata. Writes are admin.
	s.mux.HandleFunc("GET    /repos/{owner}/{repo}/actions/secrets/public-key", s.getPublicKey)
	s.mux.HandleFunc("GET    /repos/{owner}/{repo}/actions/secrets", s.listSecrets)
	s.mux.HandleFunc("GET    /repos/{owner}/{repo}/actions/secrets/{name}", s.getSecret)
	s.mux.HandleFunc("PUT    /repos/{owner}/{repo}/actions/secrets/{name}", middleware.RequireAdmin(s.db, s.putSecret))
	s.mux.HandleFunc("DELETE /repos/{owner}/{repo}/actions/secrets/{name}", middleware.RequireAdmin(s.db, s.deleteSecret))

	s.mux.HandleFunc("GET    /repos/{owner}/{repo}/actions/variables", s.listVariables)
	s.mux.HandleFunc("GET    /repos/{owner}/{repo}/actions/variables/{name}", s.getVariable)
	s.mux.HandleFunc("POST   /repos/{owner}/{repo}/actions/variables", middleware.RequireAdmin(s.db, s.createVariable))
	s.mux.HandleFunc("PATCH  /repos/{owner}/{repo}/actions/variables/{name}", middleware.RequireAdmin(s.db, s.patchVariable))
	s.mux.HandleFunc("DELETE /repos/{owner}/{repo}/actions/variables/{name}", middleware.RequireAdmin(s.db, s.deleteVariable))

	s.mux.HandleFunc("GET    /repos/{owner}/{repo}/environments", s.listEnvs)
	s.mux.HandleFunc("PUT    /repos/{owner}/{repo}/environments/{environment}", middleware.RequireAdmin(s.db, s.putEnv))
	s.mux.HandleFunc("DELETE /repos/{owner}/{repo}/environments/{environment}", middleware.RequireAdmin(s.db, s.deleteEnv))

	s.mux.HandleFunc("GET    /repos/{owner}/{repo}/environments/{environment}/secrets", s.listSecrets)
	s.mux.HandleFunc("GET    /repos/{owner}/{repo}/environments/{environment}/secrets/{name}", s.getSecret)
	s.mux.HandleFunc("PUT    /repos/{owner}/{repo}/environments/{environment}/secrets/{name}", middleware.RequireAdmin(s.db, s.putSecret))
	s.mux.HandleFunc("DELETE /repos/{owner}/{repo}/environments/{environment}/secrets/{name}", middleware.RequireAdmin(s.db, s.deleteSecret))

	s.mux.HandleFunc("GET    /repos/{owner}/{repo}/environments/{environment}/variables", s.listVariables)
	s.mux.HandleFunc("GET    /repos/{owner}/{repo}/environments/{environment}/variables/{name}", s.getVariable)
	s.mux.HandleFunc("POST   /repos/{owner}/{repo}/environments/{environment}/variables", middleware.RequireAdmin(s.db, s.createVariable))
	s.mux.HandleFunc("PATCH  /repos/{owner}/{repo}/environments/{environment}/variables/{name}", middleware.RequireAdmin(s.db, s.patchVariable))
	s.mux.HandleFunc("DELETE /repos/{owner}/{repo}/environments/{environment}/variables/{name}", middleware.RequireAdmin(s.db, s.deleteVariable))

	// ── Runway-native API ─────────────────────────────────────────────────
	s.mux.HandleFunc("GET /api/runs", s.listRuns)
	s.mux.HandleFunc("GET /api/runs/{id}", s.getRun)
	s.mux.HandleFunc("GET /api/runs/{id}/jobs", s.listRunJobs)
	s.mux.HandleFunc("GET /api/runs/{id}/logs", s.getRunLogs)

	s.mux.HandleFunc("GET /api/steps/{id}/logs", s.getLogs)
	s.mux.HandleFunc("GET /api/steps/{id}/stream", s.streamLogs)

	s.mux.HandleFunc("GET    /api/queue", s.listQueue)
	s.mux.HandleFunc("DELETE /api/queue/{id}", s.cancelQueue)

	// Repos — list/get open to all; create/delete/sync admin-only.
	s.mux.HandleFunc("GET    /api/repos", s.listRepos)
	s.mux.HandleFunc("GET    /api/repos/{id}", s.getRepo)
	s.mux.HandleFunc("GET    /api/repos/{id}/workflows", s.listRepoWorkflows)
	s.mux.HandleFunc("GET    /api/repos/{id}/gitstatus", s.getGitStatus)
	s.mux.HandleFunc("POST   /api/repos", middleware.RequireAdmin(s.db, s.createRepo))
	s.mux.HandleFunc("DELETE /api/repos/{id}", middleware.RequireAdmin(s.db, s.deleteRepo))
	s.mux.HandleFunc("POST   /api/repos/{id}/sync", middleware.RequireAdmin(s.db, s.syncRepo))

	// Settings — admin-only.
	s.mux.HandleFunc("GET  /api/settings", middleware.RequireAdmin(s.db, s.getSettings))
	s.mux.HandleFunc("POST /api/settings", middleware.RequireAdmin(s.db, s.postSettings))
	s.mux.HandleFunc("POST /api/settings/generate-token", middleware.RequireAdmin(s.db, s.generateAPIToken))

	// Auth — no auth required on login/logout (middleware exempts /login*, /logout).
	s.mux.HandleFunc("POST /api/login", s.login)
	s.mux.HandleFunc("POST /api/logout", s.logout)
	s.mux.HandleFunc("GET  /api/me", s.me)
	s.mux.HandleFunc("POST /api/me/password", s.changePassword)

	// User management — admin-only.
	s.mux.HandleFunc("GET    /api/users", middleware.RequireAdmin(s.db, s.listUsers))
	s.mux.HandleFunc("POST   /api/users", middleware.RequireAdmin(s.db, s.createUser))
	s.mux.HandleFunc("DELETE /api/users/{id}", middleware.RequireAdmin(s.db, s.deleteUser))

	// API keys — any authenticated user can manage their own keys.
	s.mux.HandleFunc("GET    /api/keys", s.listAPIKeys)
	s.mux.HandleFunc("POST   /api/keys", s.createAPIKey)
	s.mux.HandleFunc("DELETE /api/keys/{id}", s.deleteAPIKey)

	// Health — no auth (middleware exempts this path).
	s.mux.HandleFunc("GET /api/health", s.health)

	// ── Static UI (catch-all, lowest priority) ────────────────────────────
	s.mux.Handle("/", ui.Handler())
}

// ── shared helpers ────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg}) //nolint:errcheck
}
