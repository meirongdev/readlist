package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/meirongdev/readlist/internal/score"
)

// handleMetrics Prometheus 文本格式指标(FR-60/61/62)。
//
// 覆盖三类问题,每一类都对应一种「站还活着但内容已经错了」的失效:
//
//  1. **新鲜度** —— 三个作业各自的最后成功时间。只看 `last_score` 是不够的:
//     `score` 在陈旧 facts 上每晚照样成功,snapshot 或 ingest 挂掉一个月它依然常绿,
//     而那两个才是容易挂的(一个依赖 calibre 卷还在,一个依赖外部配额)。
//  2. **判别力** —— 逐维 measured 计数(system-design §7 的 `measured_ratio[dim]`)。
//     一维全是收缩值,就等于这一维不存在,而榜单不会因此报错。
//  3. **数据质量** —— 孤儿行(book id 漂移)与 pubdate 来源分布。后者是 PRD §5
//     那条护栏指标(`mtime-fallback` 书数 → 0)的唯一观测点,它直接决定时效维的上限。
//
// 走轻量路径:等级计数直接读 `runs.metrics`(发布时已经算好),不再每次抓取都重扫
// 1.4 万行 dim_scores —— 指标每 15–30 秒被抓一次。
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	runID, _, err := s.publishedRun()
	if err != nil {
		fail(w, r, err, "published_run")
		return
	}

	counts, err := s.gradeCounts(runID)
	if err != nil {
		fail(w, r, err, "grade counts")
		return
	}
	measured, err := s.measuredByDim(runID)
	if err != nil {
		fail(w, r, err, "measured by dim")
		return
	}
	pubdate, err := s.pubdateSourceCounts()
	if err != nil {
		fail(w, r, err, "pubdate sources")
		return
	}

	db := s.db.SQL()
	var works, lists, runs int
	for _, q := range []struct {
		into *int
		sql  string
		args []any
	}{
		{&works, `SELECT COUNT(*) FROM works`, nil},
		{&lists, `SELECT COUNT(*) FROM lists WHERE run_id=?`, []any{runID}},
		{&runs, `SELECT COUNT(*) FROM runs`, nil},
	} {
		if err := db.QueryRow(q.sql, q.args...).Scan(q.into); err != nil {
			fail(w, r, err, "count")
			return
		}
	}

	// 打分时间取**已发布**的那个 run —— 那才是正在被服务的内容的年龄。
	lastScore, _, err := s.runStamp(`SELECT started_at, COALESCE(metrics,'') FROM runs WHERE run_id=?`, runID)
	if err != nil {
		fail(w, r, err, "last score")
		return
	}
	lastSnapshot, snapMetrics, err := s.lastRunOfKind("snapshot")
	if err != nil {
		fail(w, r, err, "last snapshot")
		return
	}
	lastIngest, ingestMetrics, err := s.lastRunOfKind("ingest")
	if err != nil {
		fail(w, r, err, "last ingest")
		return
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	g := func(name, help string, value any) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s %v\n", name, help, name, name, value)
	}
	labeled := func(name, help, label string, values map[string]float64) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name)
		keys := make([]string, 0, len(values))
		for k := range values {
			keys = append(keys, k)
		}
		sort.Strings(keys) // 稳定输出顺序,便于 diff 与人眼扫读
		for _, k := range keys {
			fmt.Fprintf(w, "%s{%s=%q} %g\n", name, label, k, values[k])
		}
	}

	g("readlist_works_total", "全库 works 数", works)
	g("readlist_lists_total", "已发布榜单条目数", lists)
	g("readlist_runs_retained", "库内保留的 run 数(GC 后)", runs)

	// ── 新鲜度:三个作业各自的最后成功时间 ──
	g("readlist_last_score_unix", "已发布 run 的打分时间戳", lastScore)
	g("readlist_last_snapshot_unix",
		"最近一次成功快照的时间戳(0 = 从未成功;score 成功不代表语料是新的)", lastSnapshot)
	g("readlist_last_ingest_unix",
		"最近一次成功摄入外部证据的时间戳(0 = 从未成功)", lastIngest)

	// ── 判别力与徽章分布 ──
	labeled("readlist_grade_counts", "按证据等级划分的 works 数(仅徽章,不决定准入)",
		"grade", counts)
	labeled("readlist_dim_measured", "该维有实测证据的 works 数 —— 判别力的直接度量",
		"dim", measured)

	// ── 数据质量 ──
	labeled("readlist_pubdate_source", "按 pubdate 来源划分的版次数(mtime-fallback 应趋于 0)",
		"source", pubdate)
	g("readlist_orphan_rows",
		"最近一次快照里 join 不上书目的阅读状态行数(突增 = book id 漂移)",
		numFrom(snapMetrics, "orphan_rows"))

	// ── 配额(architecture §7)──
	g("readlist_ingest_requests", "最近一次摄入的外部请求数", numFrom(ingestMetrics, "requests"))
	g("readlist_ingest_throttled", "最近一次摄入收到的 429 数", numFrom(ingestMetrics, "throttled"))
	g("readlist_ingest_cache_hits", "最近一次摄入的缓存命中数", numFrom(ingestMetrics, "cache_hits"))
	g("readlist_ingest_budget_exhausted",
		"最近一次摄入是否把预算打满(1 = 还有未查完的书,次日会继续)",
		boolFrom(ingestMetrics, "budget_exhausted"))
}

// gradeCounts 证据等级计数。优先读发布时算好的 runs.metrics;
// 老 run(或字段缺失)才回退到现算,以免指标端点因为历史数据而报错。
func (s *Server) gradeCounts(runID string) (map[string]float64, error) {
	out := map[string]float64{"A": 0, "B": 0, "C": 0, "D": 0}
	if runID == "" {
		return out, nil
	}
	_, metrics, err := s.runStamp(`SELECT started_at, COALESCE(metrics,'') FROM runs WHERE run_id=?`, runID)
	if err != nil {
		return nil, err
	}
	var body struct {
		GradeCounts map[string]int `json:"grade_counts"`
	}
	if metrics != "" && json.Unmarshal([]byte(metrics), &body) == nil && len(body.GradeCounts) > 0 {
		for g, n := range body.GradeCounts {
			out[g] = float64(n)
		}
		return out, nil
	}
	grades, err := s.gradesForRun(runID)
	if err != nil {
		return nil, err
	}
	for _, g := range grades {
		out[g]++
	}
	return out, nil
}

// measuredByDim 每维有多少本书真有实测证据。
func (s *Server) measuredByDim(runID string) (map[string]float64, error) {
	out := make(map[string]float64, len(score.AllDims))
	for _, d := range score.AllDims {
		out[string(d)] = 0 // 缺的维度也要出现,否则「这一维归零了」看起来像指标丢了
	}
	if runID == "" {
		return out, nil
	}
	rows, err := s.db.SQL().Query(`SELECT dim, COUNT(*) FROM dim_scores
		WHERE run_id=? AND state='measured' GROUP BY dim`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var dim string
		var n int
		if err := rows.Scan(&dim, &n); err != nil {
			return nil, err
		}
		out[dim] = float64(n)
	}
	return out, rows.Err()
}

// pubdateSourceCounts 版次的 pubdate 来源分布(数据质量护栏)。
func (s *Server) pubdateSourceCounts() (map[string]float64, error) {
	rows, err := s.db.SQL().Query(
		`SELECT COALESCE(NULLIF(pubdate_source,''),'unknown'), COUNT(*) FROM editions GROUP BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var source string
		var n int
		if err := rows.Scan(&source, &n); err != nil {
			return nil, err
		}
		out[source] = float64(n)
	}
	return out, rows.Err()
}

// lastRunOfKind 某一类作业最近一次**成功**运行的时间戳与 metrics。
func (s *Server) lastRunOfKind(kind string) (int64, string, error) {
	return s.runStamp(`SELECT started_at, COALESCE(metrics,'') FROM runs
		WHERE kind=? AND status='ok' ORDER BY started_at DESC LIMIT 1`, kind)
}

// runStamp 取一条 run 的时间戳(unix,取不到则 0)与 metrics JSON。
func (s *Server) runStamp(query string, args ...any) (int64, string, error) {
	var started, metrics string
	err := s.db.SQL().QueryRow(query, args...).Scan(&started, &metrics)
	if err == sql.ErrNoRows {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", err
	}
	// 写入用的是 RFC3339Nano;RFC3339 布局能同时吃下带不带小数秒两种形式。
	t, perr := time.Parse(time.RFC3339, started)
	if perr != nil {
		return 0, metrics, nil
	}
	return t.Unix(), metrics, nil
}

// numFrom / boolFrom 从 metrics JSON 里取一个标量。指标端点不该因为某个历史 run
// 的 metrics 结构不同而整体失败,所以取不到一律当 0。
func numFrom(metrics, key string) float64 {
	var body map[string]any
	if metrics == "" || json.Unmarshal([]byte(metrics), &body) != nil {
		return 0
	}
	if v, ok := body[key].(float64); ok {
		return v
	}
	return 0
}

func boolFrom(metrics, key string) int {
	var body map[string]any
	if metrics == "" || json.Unmarshal([]byte(metrics), &body) != nil {
		return 0
	}
	if v, ok := body[key].(bool); ok && v {
		return 1
	}
	return 0
}
