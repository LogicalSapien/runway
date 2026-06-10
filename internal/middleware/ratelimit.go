package middleware

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	authpkg "github.com/LogicalSapien/runway/internal/auth"
)

// -----------------------------------------------------------------------
// Configuration
// -----------------------------------------------------------------------

const (
	// DefaultRateLimit is the number of requests allowed per window.
	DefaultRateLimit = 1000

	// WindowSeconds is the length of a rate-limit window in seconds (1 hour).
	WindowSeconds = 3600
)

// -----------------------------------------------------------------------
// Schema migration helper (called once from db.Open via middleware init)
// -----------------------------------------------------------------------

// EnsureRateLimitTable creates the rate_limit_counts table if it does not
// exist.  This is intentionally separate from the core db schema so that the
// middleware package owns its own storage requirement.
func EnsureRateLimitTable(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS rate_limit_counts (
    token_hash TEXT    NOT NULL,
    window     INTEGER NOT NULL,  -- unix_ts / 3600
    count      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (token_hash, window)
);
CREATE INDEX IF NOT EXISTS idx_rl_window ON rate_limit_counts(window);
`)
	return err
}

// -----------------------------------------------------------------------
// Identity extraction — what identifies the caller for rate-limiting?
// -----------------------------------------------------------------------

// callerKey returns a stable, opaque identifier for the request's authenticated
// caller so we can bucket counts without storing raw credentials.
//
// Priority:
//  1. SHA-256 of the Bearer token (already computed during auth — we rehash here
//     because the middleware is stateless between calls; the cost is negligible).
//  2. SHA-256 of the CF Access JWT assertion header value.
//  3. The remote IP address (best-effort fallback).
func callerKey(r *http.Request) string {
	// Prefer API key identity for rate-limiting (stable across sessions).
	if raw := bearerRaw(r); raw != "" {
		return "bearer:" + authpkg.SHA256Hex(raw)
	}

	// Session cookie — use its hash as the key.
	if c, err := r.Cookie("rwy_session"); err == nil && c.Value != "" {
		return "session:" + authpkg.SHA256Hex(c.Value)
	}

	// Fallback: strip port from RemoteAddr.
	addr := r.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		addr = addr[:i]
	}
	return "ip:" + addr
}

func bearerRaw(r *http.Request) string {
	v := r.Header.Get("Authorization")
	if !strings.HasPrefix(v, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(v, "Bearer ")
}

// -----------------------------------------------------------------------
// Rate limit store — thin wrapper around SQLite
// -----------------------------------------------------------------------

// currentWindow returns the current window bucket (unix seconds / 3600).
func currentWindow() int64 {
	return time.Now().Unix() / WindowSeconds
}

// windowStart returns the unix timestamp at which the given window started.
func windowStart(window int64) int64 {
	return window * WindowSeconds
}

// windowEnd returns the unix timestamp at which the given window ends (start of next).
func windowEnd(window int64) int64 {
	return (window + 1) * WindowSeconds
}

// increment atomically increments the request count for (tokenHash, window) and
// returns the new count.
func increment(db *sql.DB, tokenHash string, window int64) (int64, error) {
	_, err := db.Exec(`
INSERT INTO rate_limit_counts(token_hash, window, count)
VALUES (?, ?, 1)
ON CONFLICT(token_hash, window) DO UPDATE SET count = count + 1`,
		tokenHash, window,
	)
	if err != nil {
		return 0, err
	}

	var count int64
	err = db.QueryRow(
		`SELECT count FROM rate_limit_counts WHERE token_hash = ? AND window = ?`,
		tokenHash, window,
	).Scan(&count)
	return count, err
}

// -----------------------------------------------------------------------
// Background cleanup
// -----------------------------------------------------------------------

// cleanupMu prevents multiple concurrent purges.
var cleanupMu sync.Mutex

// cleanOldWindows deletes rows whose window is older than 24 hours.
func cleanOldWindows(db *sql.DB) {
	if !cleanupMu.TryLock() {
		return // another goroutine is cleaning; skip
	}
	defer cleanupMu.Unlock()

	cutoff := currentWindow() - 24 // 24 hourly windows = 24h
	if _, err := db.Exec(`DELETE FROM rate_limit_counts WHERE window < ?`, cutoff); err != nil {
		log.Printf("middleware/ratelimit: cleanup error: %v", err)
	}
}

// StartCleanup launches a background goroutine that purges expired rate-limit
// rows every hour.  Call once at server startup.
func StartCleanup(db *sql.DB) {
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			cleanOldWindows(db)
		}
	}()
}

// -----------------------------------------------------------------------
// Rate limit middleware
// -----------------------------------------------------------------------

// RateLimit wraps next with per-caller rate limiting.
//
// Only paths that require auth (/api/*, /repos/*) are subject to rate limiting;
// all other paths are passed through.  The limit and window size are constant
// (DefaultRateLimit requests per WindowSeconds seconds) but can be overridden
// by the caller with the optional limit argument (pass 0 to use the default).
//
// Headers added to every response:
//
//	X-RateLimit-Limit:     maximum requests per window
//	X-RateLimit-Remaining: requests left in the current window
//	X-RateLimit-Reset:     unix timestamp when the current window ends
//	X-RateLimit-Window:    window length in seconds
//
// When the limit is exceeded the handler returns 429 JSON before calling next.
func RateLimit(db *sql.DB, limit int64, next http.Handler) http.Handler {
	if limit <= 0 {
		limit = DefaultRateLimit
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requiresAuth(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		key := callerKey(r)
		window := currentWindow()

		count, err := increment(db, key, window)
		if err != nil {
			// On DB error: log and allow the request (fail open — availability
			// over strict enforcement for an internal tool).
			log.Printf("middleware/ratelimit: increment error: %v", err)
			next.ServeHTTP(w, r)
			return
		}

		remaining := limit - count
		if remaining < 0 {
			remaining = 0
		}
		reset := windowEnd(window)

		setRateLimitHeaders(w, limit, remaining, reset)

		if count > limit {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprintf(w,
				`{"error":"rate limit exceeded","limit":%d,"reset":%d}`,
				limit, reset,
			)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// setRateLimitHeaders writes the four standard rate-limit headers.
func setRateLimitHeaders(w http.ResponseWriter, limit, remaining, reset int64) {
	h := w.Header()
	h.Set("X-RateLimit-Limit", fmt.Sprintf("%d", limit))
	h.Set("X-RateLimit-Remaining", fmt.Sprintf("%d", remaining))
	h.Set("X-RateLimit-Reset", fmt.Sprintf("%d", reset))
	h.Set("X-RateLimit-Window", fmt.Sprintf("%d", int64(WindowSeconds)))
}
