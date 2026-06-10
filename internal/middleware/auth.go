// Package middleware provides HTTP middleware for Runway.
package middleware

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"

	authpkg "github.com/aedatum/runway/internal/auth"
	dbpkg "github.com/aedatum/runway/internal/db"
)

// contextKey is the type used for context values set by this middleware.
type contextKey int

const (
	ctxUser contextKey = iota
)

// UserFromContext returns the authenticated *db.User stored by Auth middleware,
// or nil if the request was not authenticated (e.g. /api/health).
func UserFromContext(ctx context.Context) *dbpkg.User {
	u, _ := ctx.Value(ctxUser).(*dbpkg.User)
	return u
}

// Auth wraps next with authentication enforcement.
//
// Protected paths: /api/* and /repos/* (except /api/health and /login*).
//
// Auth is attempted in this order:
//  1. HttpOnly session cookie "rwy_session" (browser login flow).
//  2. Bearer token in Authorization header (API key — rwy_… prefix).
//
// On success the *db.User is stored in the request context and the request
// is forwarded. On failure a 401 JSON response is returned.
func Auth(db *sql.DB, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requiresAuth(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		user := trySessionCookie(db, r)
		if user == nil {
			user = tryBearerAPIKey(db, r)
		}
		if user == nil {
			// Return a hint so the browser can redirect to /login.
			w.Header().Set("X-Runway-Login", "/login")
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		ctx := context.WithValue(r.Context(), ctxUser, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireAdmin is a per-handler guard that returns 403 when the caller is not
// an admin. It must be called after Auth has already validated the session.
func RequireAdmin(db *sql.DB, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r.Context())
		if u == nil || u.Role != dbpkg.RoleAdmin {
			writeJSONError(w, http.StatusForbidden, "admin role required")
			return
		}
		next(w, r)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// staticPaths is the exact set of unauthenticated paths served by the UI.
// Enumerated explicitly — never use HasSuffix on /api/* or /repos/* paths.
var staticPaths = map[string]bool{
	"/":           true,
	"/app.js":     true,
	"/style.css":  true,
	"/login.html": true,
}

func requiresAuth(path string) bool {
	if path == "/api/health" || path == "/api/login" || path == "/api/logout" {
		return false
	}
	if strings.HasPrefix(path, "/login") || path == "/logout" {
		return false
	}
	if staticPaths[path] {
		return false
	}
	return strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/repos/")
}

func trySessionCookie(db *sql.DB, r *http.Request) *dbpkg.User {
	c, err := r.Cookie("rwy_session")
	if err != nil || c.Value == "" {
		return nil
	}
	hash := authpkg.SHA256Hex(c.Value)
	user, err := dbpkg.GetSessionUser(db, hash)
	if err != nil || user == nil {
		return nil
	}
	return user
}

func tryBearerAPIKey(db *sql.DB, r *http.Request) *dbpkg.User {
	v := r.Header.Get("Authorization")
	if !strings.HasPrefix(v, "Bearer ") {
		return nil
	}
	raw := strings.TrimPrefix(v, "Bearer ")
	if raw == "" {
		return nil
	}
	hash := authpkg.SHA256Hex(raw)
	user, err := dbpkg.GetAPIKeyUser(db, hash)
	if err != nil || user == nil {
		return nil
	}
	return user
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}
