package api

import (
	"net/http"
	"strconv"

	dbpkg "github.com/LogicalSapien/runway/internal/db"
)

// listQueue handles GET /api/queue
// Returns queue items filtered by optional ?status= query param.
// Defaults to returning all items (queued + running + done + cancelled).
func (s *Server) listQueue(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	items, err := dbpkg.ListQueue(s.db, status)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if items == nil {
		items = []dbpkg.QueueItem{}
	}
	writeJSON(w, items)
}

// cancelQueue handles DELETE /api/queue/{id}
// Cancels a queued or running queue item.  Returns 204 on success.
func (s *Server) cancelQueue(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := dbpkg.CancelQueueItem(s.db, id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
