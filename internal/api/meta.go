package api

import (
	"net/http"
)

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	runID, _ := s.publishedRun()
	writeJSON(w, http.StatusOK, map[string]any{
		"run_id":             runID,
		"standard_version":   "1.0",
		"expose_read_status": s.exposeRead,
	})
}
