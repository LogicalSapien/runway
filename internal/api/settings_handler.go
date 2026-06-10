package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	dbpkg "github.com/LogicalSapien/runway/internal/db"
)

// getSettings handles GET /api/settings
// Returns all settings as a flat key→value map.
// api_token_hash is never returned — only api_token_set (bool) and api_token_set_at.
func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`SELECT key, value FROM settings ORDER BY key`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if k == "api_token_hash" {
			// Never expose the hash — instead return whether a token is set
			if v != "" {
				out["api_token_set"] = "true"
			}
			continue
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, out)
}

// generateAPIToken handles POST /api/settings/generate-token
// Generates a new random rwy_ prefixed API token, stores its hash, returns the plaintext once.
func (s *Server) generateAPIToken(w http.ResponseWriter, r *http.Request) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}
	token := "rwy_" + hex.EncodeToString(b)
	hash := settingsSHA256Hex(token)
	setAt := fmt.Sprintf("%d", time.Now().Unix())

	if err := dbpkg.SetSetting(s.db, "api_token_hash", hash); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := dbpkg.SetSetting(s.db, "api_token_set_at", setAt); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]string{"token": token, "set_at": setAt})
}

// postSettings handles POST /api/settings
// Accepts a flat key→value map and upserts each pair.
// Special key "api_token": the plaintext value is SHA-256 hashed and stored as
// "api_token_hash" to match the middleware validation scheme.
func (s *Server) postSettings(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	for k, v := range body {
		if k == "api_token" {
			if v == "" {
				continue // blank = keep existing token
			}
			setAt := fmt.Sprintf("%d", time.Now().Unix())
			if err := dbpkg.SetSetting(s.db, "api_token_hash", settingsSHA256Hex(v)); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if err := dbpkg.SetSetting(s.db, "api_token_set_at", setAt); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			continue
		}
		if err := dbpkg.SetSetting(s.db, k, v); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// settingsSHA256Hex returns the lowercase hex-encoded SHA-256 of s.
// Defined here (not imported from middleware) to avoid a circular dependency.
func settingsSHA256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
