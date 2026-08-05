package api

import "net/http"

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	info, err := s.Health()
	if err != nil {
		fail(w, r, err, "health")
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// Health 健康信息:已发布 run、语料指纹、书量。
func (s *Server) Health() (map[string]any, error) {
	runID, version, err := s.publishedRun()
	if err != nil {
		return nil, err
	}
	var workCount int
	if err := s.db.SQL().QueryRow(`SELECT COUNT(*) FROM works`).Scan(&workCount); err != nil {
		return nil, err
	}
	var corpusID string
	if runID != "" {
		_ = s.db.SQL().QueryRow(
			`SELECT COALESCE(corpus_id, '') FROM runs WHERE run_id=?`, runID).Scan(&corpusID)
	}
	return map[string]any{
		"status":           "ok",
		"run_id":           runID,
		"corpus_id":        corpusID,
		"standard_version": version,
		"works":            workCount,
		"version":          "readlist-mvp",
	}, nil
}
