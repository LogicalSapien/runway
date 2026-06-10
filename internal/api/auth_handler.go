package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	authpkg "github.com/aedatum/runway/internal/auth"
	dbpkg "github.com/aedatum/runway/internal/db"
	"github.com/aedatum/runway/internal/middleware"
)

const sessionDuration = 30 * 24 * time.Hour

// secureCookies returns true unless RUNWAY_INSECURE_COOKIES=true is set.
// The Secure flag must be off only for plain-HTTP local development.
func secureCookies() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv("RUNWAY_INSECURE_COOKIES")), "true")
}

// POST /api/login
// Body: {"username": "...", "password": "..."}
// Sets an HttpOnly session cookie on success.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Username == "" || body.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password required")
		return
	}

	user, err := dbpkg.GetUserByUsername(s.db, body.Username)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if user == nil || !authpkg.CheckPassword(user.PasswordHash, body.Password) {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	raw, hash, err := authpkg.NewSessionToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create session")
		return
	}
	expiresAt := time.Now().Add(sessionDuration).Unix()
	if err := dbpkg.CreateSession(s.db, dbpkg.Session{
		TokenHash: hash,
		UserID:    user.ID,
		ExpiresAt: expiresAt,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "could not create session")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "rwy_session",
		Value:    raw,
		Path:     "/",
		HttpOnly: true,
		Secure:   secureCookies(),
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(expiresAt, 0),
	})
	writeJSON(w, map[string]any{
		"ok":             true,
		"role":           user.Role,
		"must_change_pw": user.MustChangePW,
	})
}

// POST /api/logout
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("rwy_session")
	if err == nil && c.Value != "" {
		hash := authpkg.SHA256Hex(c.Value)
		dbpkg.DeleteSession(s.db, hash) //nolint:errcheck
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "rwy_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secureCookies(),
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
	writeJSON(w, map[string]bool{"ok": true})
}

// GET /api/me — returns the current user's profile (no password hash).
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	writeJSON(w, map[string]any{
		"id":             u.ID,
		"username":       u.Username,
		"role":           u.Role,
		"must_change_pw": u.MustChangePW,
	})
}

// POST /api/me/password — change own password.
// Body: {"current_password": "...", "new_password": "..."}
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if !authpkg.CheckPassword(u.PasswordHash, body.CurrentPassword) {
		writeError(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}
	if len(body.NewPassword) < 8 {
		writeError(w, http.StatusBadRequest, "new password must be at least 8 characters")
		return
	}

	hash, err := authpkg.HashPassword(body.NewPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	if err := dbpkg.UpdatePassword(s.db, u.ID, hash); err != nil {
		writeError(w, http.StatusInternalServerError, "could not update password")
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// ── User management (admin only) ──────────────────────────────────────────────

// GET /api/users
func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := dbpkg.ListUsers(s.db)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Strip password hashes before serialising.
	type safeUser struct {
		ID           int64  `json:"id"`
		Username     string `json:"username"`
		Role         string `json:"role"`
		MustChangePW bool   `json:"must_change_pw"`
		CreatedAt    int64  `json:"created_at"`
	}
	out := make([]safeUser, len(users))
	for i, u := range users {
		out[i] = safeUser{u.ID, u.Username, u.Role, u.MustChangePW, u.CreatedAt}
	}
	writeJSON(w, out)
}

// POST /api/users
// Body: {"username": "...", "password": "...", "role": "viewer"}
func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Username == "" || body.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password required")
		return
	}
	if body.Role != dbpkg.RoleAdmin && body.Role != dbpkg.RoleViewer {
		body.Role = dbpkg.RoleViewer
	}
	if len(body.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	hash, err := authpkg.HashPassword(body.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	id, err := dbpkg.CreateUser(s.db, dbpkg.User{
		Username:     body.Username,
		Role:         body.Role,
		PasswordHash: hash,
		MustChangePW: true,
	})
	if err != nil {
		writeError(w, http.StatusConflict, "username already exists")
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]any{"id": id, "username": body.Username, "role": body.Role})
}

// DELETE /api/users/{id}
func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	me := middleware.UserFromContext(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if me != nil && me.ID == id {
		writeError(w, http.StatusBadRequest, "cannot delete your own account")
		return
	}
	if err := dbpkg.DeleteUser(s.db, id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── API Keys ──────────────────────────────────────────────────────────────────

// GET /api/keys
func (s *Server) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFromContext(r.Context())
	keys, err := dbpkg.ListAPIKeys(s.db, u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if keys == nil {
		keys = []dbpkg.APIKey{}
	}
	writeJSON(w, keys)
}

// POST /api/keys
// Body: {"name": "my-agent"}
// Returns the raw key exactly once — it is not stored and cannot be retrieved again.
func (s *Server) createAPIKey(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFromContext(r.Context())
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	raw, hash, err := authpkg.NewAPIKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not generate key")
		return
	}
	id, err := dbpkg.CreateAPIKey(s.db, dbpkg.APIKey{
		KeyHash:   hash,
		Name:      body.Name,
		CreatedBy: u.ID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]any{
		"id":   id,
		"key":  raw, // shown once only
		"name": body.Name,
	})
}

// DELETE /api/keys/{id}
func (s *Server) deleteAPIKey(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFromContext(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := dbpkg.DeleteAPIKey(s.db, id, u.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
