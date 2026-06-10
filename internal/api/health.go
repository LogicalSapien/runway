package api

import (
	"net/http"
	"time"
)

// healthResponse is the JSON body returned by GET /api/health.
type healthResponse struct {
	Status string `json:"status"`
	Time   string `json:"time"`
}

// health handles GET /api/health — no authentication required.
// Returns 200 with a JSON body when the server and DB are reachable.
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	// Ping the database to confirm connectivity.
	if err := s.db.PingContext(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable: "+err.Error())
		return
	}
	writeJSON(w, healthResponse{
		Status: "ok",
		Time:   time.Now().UTC().Format(time.RFC3339),
	})
}
