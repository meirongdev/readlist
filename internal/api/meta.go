package api

import "net/http"

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	runID, version, err := s.publishedRun()
	if err != nil {
		fail(w, r, err, "published_run")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run_id":             runID,
		"standard_version":   version,
		"expose_read_status": s.exposeRead,
	})
}
