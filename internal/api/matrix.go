package api

import (
	"net/http"

	"github.com/meirongdev/readlist/internal/score"
)

// handleMatrix 滑块用的整块矩阵:只含可公开行(grade A/B/C),按 run_id 寻址。
func (s *Server) handleMatrix(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("run")
	if runID == "" {
		writeError(w, http.StatusBadRequest, "run required")
		return
	}
	grades, err := s.loadGrades(runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	bases, editions, _ := s.loadWorkBases()
	dims := s.loadDims(runID)
	reading, _ := s.readingByWork(editions)

	type row struct {
		WorkID  string                    `json:"work_id"`
		Title   string                    `json:"title"`
		Author  string                    `json:"author"`
		Topic   string                    `json:"topic"`
		Level   string                    `json:"level"`
		Year    int                       `json:"year,omitempty"`
		Grade   string                    `json:"grade"`
		Dims    map[string]score.DimScore `json:"dims"`
		Reading readingInfo               `json:"reading"`
	}
	works := make([]row, 0, 256)
	for wid, b := range bases {
		g := grades[wid]
		if g == "" || g == "D" {
			continue // 只含可公开行
		}
		works = append(works, row{
			WorkID: wid, Title: b.Title, Author: b.Author, Topic: b.Topic,
			Level: b.Level, Year: b.Year, Grade: g,
			Dims: dims[wid], Reading: reading[wid],
		})
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Content-Encoding", "")
	writeJSON(w, http.StatusOK, map[string]any{
		"run_id": runID, "standard_version": "1.0", "works": works,
	})
}
