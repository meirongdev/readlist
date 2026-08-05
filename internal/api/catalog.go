package api

import "net/http"

// handleCatalog 全库目录:A/B/C 级都列,C 级由前端标注"数据不足"。
func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	runID, _ := s.publishedRun()
	if runID == "" {
		writeError(w, http.StatusNotFound, "no published run")
		return
	}
	bases, _, _ := s.loadWorkBases()
	grades, _ := s.loadGrades(runID)
	dims := s.loadDims(runID)

	type row struct {
		WorkID  string   `json:"work_id"`
		Title   string   `json:"title"`
		Author  string   `json:"author"`
		Topic   string   `json:"topic"`
		Level   string   `json:"level"`
		Year    int      `json:"year,omitempty"`
		Grade   string   `json:"grade"`
		Missing []string `json:"missing,omitempty"`
	}
	works := make([]row, 0, 256)
	for wid, b := range bases {
		g := grades[wid]
		if g == "" || g == "D" {
			continue
		}
		rw := row{WorkID: wid, Title: b.Title, Author: b.Author, Topic: b.Topic,
			Level: b.Level, Year: b.Year, Grade: g}
		for dim, d := range dims[wid] {
			if d.State == "unknown" {
				rw.Missing = append(rw.Missing, dim)
			}
		}
		works = append(works, rw)
	}
	writeJSON(w, http.StatusOK, map[string]any{"run_id": runID, "total": len(works), "works": works})
}
