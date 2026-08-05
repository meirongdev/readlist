package api

import (
	"net/http"

	"github.com/meirongdev/readlist/internal/score"
)

func (s *Server) handleLists(w http.ResponseWriter, r *http.Request) {
	runID, _ := s.publishedRun()
	presets := s.publicPresets()
	type listMeta struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Size        int    `json:"size"`
	}
	out := make([]listMeta, 0, len(presets))
	for _, p := range presets {
		out = append(out, listMeta{ID: p.ID, Name: p.Name, Description: p.Description, Size: p.Select.Size})
	}
	writeJSON(w, http.StatusOK, map[string]any{"run_id": runID, "lists": out})
}

// handleList 单榜详情。先取列表行并立即释放连接(单连接池),再跑嵌套查询。
func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	runID, _ := s.publishedRun()
	if runID == "" {
		writeError(w, http.StatusNotFound, "no published run")
		return
	}
	p, ok := presetByID(s.publicPresets(), id)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown preset")
		return
	}

	type row struct {
		rank     int
		workID   string
		tbs      float64
		coverage float64
		reason   string
	}
	rows, err := s.query().SQL().Query(
		`SELECT rank, work_id, tbs, coverage, reason FROM lists WHERE run_id=? AND list_id=? ORDER BY rank`, runID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	selected := make([]row, 0, 64)
	for rows.Next() {
		var rr row
		if err := rows.Scan(&rr.rank, &rr.workID, &rr.tbs, &rr.coverage, &rr.reason); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "db error")
			return
		}
		selected = append(selected, rr)
	}
	rows.Close()

	bases, editions, _ := s.loadWorkBases()
	grades, _ := s.loadGrades(runID)
	reading, _ := s.readingByWork(editions)
	dims := s.loadDims(runID)

	type item struct {
		Rank     int                       `json:"rank"`
		WorkID   string                    `json:"work_id"`
		Title    string                    `json:"title"`
		Author   string                    `json:"author"`
		Topic    string                    `json:"topic"`
		Level    string                    `json:"level"`
		Year     int                       `json:"year,omitempty"`
		Grade    string                    `json:"grade"`
		TBS      float64                   `json:"tbs"`
		Coverage float64                   `json:"coverage"`
		Reason   string                    `json:"reason"`
		Reading  readingInfo               `json:"reading"`
		Dims     map[string]score.DimScore `json:"dims"`
	}
	items := make([]item, 0, len(selected))
	for _, rr := range selected {
		b := bases[rr.workID]
		items = append(items, item{Rank: rr.rank, WorkID: rr.workID, Title: b.Title, Author: b.Author,
			Topic: b.Topic, Level: b.Level, Year: b.Year,
			Grade: grades[rr.workID], TBS: rr.tbs, Coverage: rr.coverage, Reason: rr.reason,
			Reading: reading[rr.workID], Dims: dims[rr.workID]})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "name": p.Name, "description": p.Description,
		"run_id": runID, "standard_version": "1.0", "items": items,
	})
}
