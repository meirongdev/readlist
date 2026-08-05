package api

import (
	"net/http"

	"github.com/meirongdev/readlist/internal/score"
)

// loadDims 加载 run 的全部 dim_scores(work_id → dim → DimScore)。
func (s *Server) loadDims(runID string) map[string]map[string]score.DimScore {
	if runID == "" {
		return map[string]map[string]score.DimScore{}
	}
	rows, err := s.query().SQL().Query(
		`SELECT work_id, dim, raw, pct, score, state, source, confidence FROM dim_scores WHERE run_id=?`, runID)
	if err != nil {
		return map[string]map[string]score.DimScore{}
	}
	defer rows.Close()
	out := map[string]map[string]score.DimScore{}
	for rows.Next() {
		var wid, dim, state, source string
		var raw, pct, scr, conf float64
		if err := rows.Scan(&wid, &dim, &raw, &pct, &scr, &state, &source, &conf); err != nil {
			continue
		}
		if out[wid] == nil {
			out[wid] = map[string]score.DimScore{}
		}
		out[wid][dim] = score.DimScore{Raw: raw, Pct: pct, Score: scr, State: score.State(state), Source: source, Confidence: conf}
	}
	return out
}

func (s *Server) handleWork(w http.ResponseWriter, r *http.Request) {
	workID := r.PathValue("id")
	runID, _ := s.publishedRun()
	if runID == "" {
		writeError(w, http.StatusNotFound, "no published run")
		return
	}
	bases, editions, _ := s.loadWorkBases()
	b, ok := bases[workID]
	if !ok {
		writeError(w, http.StatusNotFound, "unknown work")
		return
	}
	grades, _ := s.loadGrades(runID)
	dims := s.loadDims(runID)
	reading, _ := s.readingByWork(editions)

	// 缺失维度说明(供 C 级"数据不足"解释)。
	missing := []map[string]string{}
	for dim, d := range dims[workID] {
		if d.State == "unknown" {
			missing = append(missing, map[string]string{"dim": dim, "why": missingWhy(dim)})
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"work_id": workID, "title": b.Title, "author": b.Author,
		"topic": b.Topic, "level": b.Level, "grade": grades[workID],
		"run_id": runID, "standard_version": "1.0",
		"dims":     dims[workID],
		"missing":  missing,
		"editions": editions[workID],
		"reading":  reading[workID],
		"links": map[string]string{
			"google_books": "https://www.google.com/books/edition/_/" + b.Title,
			"openlibrary":  "https://openlibrary.org/search?q=" + b.Title,
		},
	})
}

func missingWhy(dim string) string {
	switch dim {
	case "F":
		return "出版日期不可信或缺失"
	case "T":
		return "作者与出版社均未知"
	case "D":
		return "标注置信度低"
	case "P":
		return "标注置信度低"
	default:
		return "证据不足"
	}
}
