package api

import (
	"fmt"
	"net/http"
	"time"
)

// handleMetrics Prometheus 文本格式指标(FR-60 的 MVP 子集)。
// 走轻量路径:只读 dim_scores 的 state,不拉 works/editions/reading ——
// 指标每 15–30 秒被抓一次,不该每次都全表扫四张表。
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	runID, _, err := s.publishedRun()
	if err != nil {
		fail(w, r, err, "published_run")
		return
	}
	grades, err := s.gradesForRun(runID)
	if err != nil {
		fail(w, r, err, "grades")
		return
	}
	counts := map[string]int{"A": 0, "B": 0, "C": 0, "D": 0}
	for _, g := range grades {
		counts[g]++
	}

	var works, lists, runs int
	db := s.db.SQL()
	if err := db.QueryRow(`SELECT COUNT(*) FROM works`).Scan(&works); err != nil {
		fail(w, r, err, "count works")
		return
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM lists WHERE run_id=?`, runID).Scan(&lists); err != nil {
		fail(w, r, err, "count lists")
		return
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&runs); err != nil {
		fail(w, r, err, "count runs")
		return
	}

	var lastScore int64
	if runID != "" {
		var started string
		if err := db.QueryRow(`SELECT started_at FROM runs WHERE run_id=?`, runID).Scan(&started); err == nil {
			if t, err := time.Parse(time.RFC3339, started); err == nil {
				lastScore = t.Unix()
			}
		}
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	gauge := func(name, help string, value any) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s %v\n", name, help, name, name, value)
	}
	gauge("readlist_works_total", "全库 works 数", works)
	fmt.Fprintln(w, "# HELP readlist_grade_counts 按证据等级划分的 works 数(仅徽章,不决定准入)")
	fmt.Fprintln(w, "# TYPE readlist_grade_counts gauge")
	for _, g := range []string{"A", "B", "C", "D"} {
		fmt.Fprintf(w, "readlist_grade_counts{grade=%q} %d\n", g, counts[g])
	}
	gauge("readlist_lists_total", "已发布榜单条目数", lists)
	gauge("readlist_runs_retained", "库内保留的 run 数(GC 后)", runs)
	gauge("readlist_last_score_unix", "最近一次打分时间戳", lastScore)
}
