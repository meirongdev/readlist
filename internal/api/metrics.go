package api

import (
	"fmt"
	"net/http"
	"time"
)

// handleMetrics Prometheus 文本格式指标(FR-60 的 MVP 子集)。
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	runID, _ := s.publishedRun()
	grades, _ := s.loadGrades(runID)
	counts := map[string]int{}
	var public int
	for _, g := range grades {
		counts[g]++
		if g != "D" {
			public++
		}
	}
	var works, lists int
	_ = s.query().SQL().QueryRow(`SELECT COUNT(*) FROM works`).Scan(&works)
	_ = s.query().SQL().QueryRow(`SELECT COUNT(*) FROM lists WHERE run_id=?`, runID).Scan(&lists)

	var lastScore int64
	if runID != "" {
		var started string
		if err := s.query().SQL().QueryRow(`SELECT started_at FROM runs WHERE run_id=?`, runID).Scan(&started); err == nil {
			if t, err := time.Parse(time.RFC3339, started); err == nil {
				lastScore = t.Unix()
			}
		}
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintln(w, "# HELP readlist_works_total 全库 works 数")
	fmt.Fprintln(w, "# TYPE readlist_works_total gauge")
	fmt.Fprintf(w, "readlist_works_total %d\n", works)
	fmt.Fprintln(w, "# HELP readlist_public_works 公开(grade A/B/C)works 数")
	fmt.Fprintln(w, "# TYPE readlist_public_works gauge")
	fmt.Fprintf(w, "readlist_public_works %d\n", public)
	fmt.Fprintln(w, "# HELP readlist_grade_counts 按证据等级划分的 works 数")
	fmt.Fprintln(w, "# TYPE readlist_grade_counts gauge")
	for _, g := range []string{"A", "B", "C", "D"} {
		fmt.Fprintf(w, "readlist_grade_counts{grade=%q} %d\n", g, counts[g])
	}
	fmt.Fprintln(w, "# HELP readlist_lists_total 已发布榜单数")
	fmt.Fprintln(w, "# TYPE readlist_lists_total gauge")
	fmt.Fprintf(w, "readlist_lists_total %d\n", lists)
	fmt.Fprintln(w, "# HELP readlist_last_score_unix 最近一次打分时间戳")
	fmt.Fprintln(w, "# TYPE readlist_last_score_unix gauge")
	fmt.Fprintf(w, "readlist_last_score_unix %d\n", lastScore)
}
