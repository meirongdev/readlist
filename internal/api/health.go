package api

import "net/http"

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	info, err := s.Health()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// Health 健康信息:已发布 run、书量。
func (s *Server) Health() (map[string]any, error) {
	runID, _ := s.publishedRun()
	var workCount int
	if err := s.query().SQL().QueryRow(`SELECT COUNT(*) FROM works`).Scan(&workCount); err != nil {
		return nil, err
	}
	return map[string]any{
		"status":  "ok",
		"run_id":  runID,
		"works":   workCount,
		"version": "readlist-mvp",
	}, nil
}
