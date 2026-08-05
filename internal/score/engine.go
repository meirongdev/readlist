package score

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/meirongdev/readlist/internal/corpus"
	"github.com/meirongdev/readlist/internal/preset"
	"github.com/meirongdev/readlist/internal/selection"
	"github.com/meirongdev/readlist/internal/store"
)

// DefaultKeepRuns 默认保留的历史 run 数(回滚窗口)。
const DefaultKeepRuns = 5

// Engine 评分引擎:facts → judgement(dim_scores+norm_cdf) → selection(lists) → publish。
// 约束:score 命令不发起任何网络请求(system-design §9)。
type Engine struct {
	DB       *store.DB
	Version  string
	Now      time.Time
	KeepRuns int
}

// RunResult 一次评分的结果(便于测试断言与 dryrun 复用)。
type RunResult struct {
	RunID     string
	CorpusID  string
	FactsHash string
	Works     map[string]*WorkInput
	Dims      map[string]map[Dim]DimScore
	Grade     map[string]string
	Lists     map[string][]ListEntry
	CDFs      map[Dim]CDF
	Params    DimParams
}

// ListEntry 榜单条目。
type ListEntry struct {
	Rank     int
	WorkID   string
	TBS      float64
	Coverage float64
	Reason   string
}

// NewEngine 构造引擎(now 可注入以便测试)。
func NewEngine(d *store.DB, version string, now time.Time) *Engine {
	return &Engine{DB: d, Version: version, Now: now, KeepRuns: DefaultKeepRuns}
}

// Computation 计算中间产物(dryrun 不落库,便于只数不算)。
type Computation struct {
	WorkIDs   []string // 升序的 work_id —— 一切跨 work 的遍历都走它,保证可复现
	Raws      map[string]map[Dim]DimResult
	Params    DimParams
	CDFs      map[Dim]CDF
	Dims      map[string]map[Dim]DimScore
	Grade     map[string]string
	Lists     map[string][]ListEntry
	Inputs    map[string]*WorkInput
	CorpusID  string
	FactsHash string
}

// Compute 计算 judgement + selection(不持久化)。
//
// 全流程不含任何 map 迭代序依赖:浮点求和的次序会改变最低位,进而翻转并列比较,
// 让「同语料 + 同公式 ⇒ 逐位复现」(NFR-10)失效。
func (e *Engine) Compute(presets []preset.Preset) (*Computation, error) {
	inputs, err := e.loadWorkInputs()
	if err != nil {
		return nil, err
	}
	ids := sortedIDs(inputs)

	corpusID, err := e.corpusID()
	if err != nil {
		return nil, err
	}
	factsHash, err := e.factsHash()
	if err != nil {
		return nil, err
	}

	// 1) 第一遍 raw(初始参数)→ 从实测推导语料先验与置信阈值 m。
	initial := e.Params()
	raws1 := map[string]map[Dim]DimResult{}
	for _, id := range ids {
		raws1[id] = inputs[id].Compute(initial, e.Now)
	}
	params := e.computeParams(ids, raws1)
	// 2) 第二遍 raw(真实参数,保证贝叶斯用的是语料先验)。
	raws := map[string]map[Dim]DimResult{}
	for _, id := range ids {
		raws[id] = inputs[id].Compute(params, e.Now)
	}
	// 3) 每维 CDF(只由 measured 构建)
	cdfs := map[Dim]CDF{}
	for _, dim := range AllDims {
		var vals []float64
		for _, id := range ids {
			if raws[id][dim].State == StateMeasured {
				vals = append(vals, raws[id][dim].Raw)
			}
		}
		cdfs[dim] = BuildCDF(dim, vals)
	}
	// 4) 归一化为 DimScore
	dims := map[string]map[Dim]DimScore{}
	for _, id := range ids {
		dims[id] = e.normalize(raws[id], cdfs, params)
	}
	// 5) 证据等级徽章(仅展示,不做准入 —— 见 Grade 的注释)
	grade := map[string]string{}
	for _, id := range ids {
		grade[id] = Grade(dims[id])
	}
	// 6) selection
	lists := map[string][]ListEntry{}
	for _, p := range presets {
		lists[p.ID] = e.selectList(p, ids, inputs, dims)
	}
	return &Computation{
		WorkIDs: ids, Raws: raws, Params: params, CDFs: cdfs, Dims: dims,
		Grade: grade, Lists: lists, Inputs: inputs,
		CorpusID: corpusID, FactsHash: factsHash,
	}, nil
}

// Run 完整跑一遍 judgement + selection + publish。
func (e *Engine) Run(presets []preset.Preset) (*RunResult, error) {
	c, err := e.Compute(presets)
	if err != nil {
		return nil, err
	}
	runID, err := e.persist(runPersistInput{
		IDs: c.WorkIDs, Presets: presets, Inputs: c.Inputs, Dims: c.Dims, Grade: c.Grade,
		CDFs: c.CDFs, Params: c.Params, Lists: c.Lists,
		CorpusID: c.CorpusID, FactsHash: c.FactsHash,
	})
	if err != nil {
		return nil, err
	}
	return &RunResult{
		RunID: runID, CorpusID: c.CorpusID, FactsHash: c.FactsHash,
		Works: c.Inputs, Dims: c.Dims, Grade: c.Grade,
		Lists: c.Lists, CDFs: c.CDFs, Params: c.Params,
	}, nil
}

// sortedIDs work_id 的稳定升序 —— 所有跨 work 的循环都必须用它,不能遍历 map。
func sortedIDs(inputs map[string]*WorkInput) []string {
	ids := make([]string, 0, len(inputs))
	for id := range inputs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Params 语料先验与置信阈值(独立可测)。
func (e *Engine) Params() DimParams {
	return DimParams{AcclaimPrior: 4.1, MinCount: 20}
}

func (e *Engine) computeParams(ids []string, raws map[string]map[Dim]DimResult) DimParams {
	p := e.Params()
	var sum, cnt float64
	var counts []int
	for _, id := range ids {
		if a, ok := raws[id][DimAcclaim]; ok && a.State == StateMeasured {
			sum += a.Raw
			cnt++
			counts = append(counts, a.Count)
		}
	}
	if cnt > 0 {
		p.AcclaimPrior = sum / cnt
	}
	median := 0.0
	if len(counts) > 0 {
		sort.Ints(counts)
		median = float64(counts[len(counts)/2])
	}
	p.MinCount = math.Max(median, 20)
	return p
}

// normalize 把 raw 转成 0–100 的 score(pct),shrunk 映射到先验位置。
func (e *Engine) normalize(raws map[Dim]DimResult, cdfs map[Dim]CDF, p DimParams) map[Dim]DimScore {
	out := map[Dim]DimScore{}
	for _, dim := range AllDims {
		r, ok := raws[dim]
		if !ok {
			continue
		}
		ds := DimScore{Raw: r.Raw, State: r.State, Source: r.Source, Confidence: r.Conf}
		switch r.State {
		case StateMeasured:
			ds.Pct = cdfs[dim].MidRankPct(r.Raw)
			ds.Score = ds.Pct
		case StateShrunk:
			ds.Pct = cdfs[dim].PriorPct(e.prior(dim, p))
			ds.Score = ds.Pct
		default:
			// unknown:不发明分数。0 只是占位,消费方必须先看 state
			// (前端把 unknown 渲染成「—」而不是 0.0 分)。
			ds.Pct, ds.Score = 0, 0
		}
		out[dim] = ds
	}
	return out
}

// prior 各维收缩先验。
func (e *Engine) prior(dim Dim, p DimParams) float64 {
	switch dim {
	case DimAcclaim:
		return p.AcclaimPrior
	case DimCommunity, DimFreshness:
		return 0
	case DimTrust:
		return 25
	case DimDepth, DimPractical:
		return 50
	default:
		return 0
	}
}

// selectList 按预设选材(过滤 → needs 硬门 → coverage/TBS → 多样性 → 理由)。
//
// 准入只有三道门:preset 的 filters、preset 的 needs、以及 min_coverage。
// **证据等级字母不参与准入** —— 拿 grade=="D" 当全局闸门会把「pubdate 来自 mtime
// 兜底」的书从明确不使用 F 维的榜单里一并踢掉(review B1,实测全库 23%)。
func (e *Engine) selectList(p preset.Preset, ids []string, inputs map[string]*WorkInput,
	dims map[string]map[Dim]DimScore) []ListEntry {

	weights := weightsByDim(p)
	bands := bandsByDim(p)
	needs := needsByDim(p)

	cands := make([]selection.Candidate, 0, len(ids))
	for _, id := range ids {
		w := inputs[id]
		if !e.filterPass(p, w) {
			continue
		}
		if !NeedsSatisfied(dims[id], needs) {
			continue
		}
		cr := Combine(dims[id], weights, bands, needs)
		cands = append(cands, selection.Candidate{
			WorkID:      id,
			Topic:       w.PrimaryTopic,
			FirstAuthor: w.FirstAuthor,
			TBS:         cr.TBS,
			Coverage:    cr.Coverage,
			Facts:       e.facts(w, dims[id], cr),
		})
	}
	entries := selection.Select(cands, selection.Config{
		Size:         p.Select.Size,
		MaxPerTopic:  p.Select.MaxPerTopic,
		MaxPerAuthor: p.Select.MaxPerAuthor,
		MinCoverage:  p.Select.MinCoverage,
		Asc:          p.Order == "asc",
	})
	out := make([]ListEntry, 0, len(entries))
	for i, en := range entries {
		out = append(out, ListEntry{Rank: i + 1, WorkID: en.WorkID, TBS: en.TBS,
			Coverage: en.Coverage, Reason: en.Reason})
	}
	return out
}

// filterPass 命中预设过滤器(必要条件)。
func (e *Engine) filterPass(p preset.Preset, w *WorkInput) bool {
	f := p.Filters
	// 「经历过时间检验」看最早版次:1996 年的经典出了 2024 年新版,它依然够老。
	if f.MinAgeYears > 0 {
		if w.FirstPubdate == nil || yearsSince(*w.FirstPubdate, e.Now) < float64(f.MinAgeYears) {
			return false
		}
	}
	// 「新书」看最新版次。用滚动窗口而不是硬编码年份,否则跨年后这个榜会
	// 静默变成「去年的书」(review m3)。
	if f.PubdateWithinMonths > 0 {
		if w.LatestPubdate == nil || w.LatestPubdate.Before(e.Now.AddDate(0, -f.PubdateWithinMonths, 0)) {
			return false
		}
	}
	if f.PubdateYear > 0 {
		if w.LatestPubdate == nil || w.LatestPubdate.Year() != f.PubdateYear {
			return false
		}
	}
	if len(f.PubdateSource) > 0 && !contains(f.PubdateSource, w.PubdateSource) {
		return false
	}
	if len(f.TopicsAny) > 0 && !topicIntersects(w.Topics, f.TopicsAny) {
		return false
	}
	if len(f.Level) > 0 && !contains(f.Level, w.Level) {
		return false
	}
	if len(f.ReadStatus) > 0 {
		status := w.ReadStatus
		if status == "" {
			status = "unread"
		}
		if !contains(f.ReadStatus, status) {
			return false
		}
	}
	if len(f.NotInShelf) > 0 && w.HasReading {
		for _, s := range f.NotInShelf {
			if contains(w.Shelves, s) {
				return false
			}
		}
	}
	if f.MinPersonalRating > 0 && (!w.HasPersonal || w.PersonalRating < f.MinPersonalRating) {
		return false
	}
	return true
}

// facts 组装理由串所需事实。
func (e *Engine) facts(w *WorkInput, dims map[Dim]DimScore, cr CombineResult) selection.Facts {
	f := selection.Facts{
		Publisher:       w.PublisherNorm,
		Author:          w.FirstAuthor,
		AuthorIsUnknown: strings.EqualFold(strings.TrimSpace(w.FirstAuthor), "unknown"),
		HasHalfLife:     w.HalfLifeYears > 0,
		HalfLifeYears:   w.HalfLifeYears,
		Grade:           Grade(dims),
		Coverage:        cr.Coverage,
		AvailableDims:   len(cr.Available),
		TotalDims:       cr.TotalDims,
	}
	if len(w.Mentions) > 0 {
		times := make([]time.Time, 0, len(w.Mentions))
		for _, m := range w.Mentions {
			times = append(times, m.CreatedAt)
		}
		f.HasMentions = true
		f.MentionCount = len(w.Mentions)
		f.MentionFirstYear, f.MentionLastYear = selection.YearRange(times)
	}
	if d, ok := dims[DimDepth]; ok && d.State != StateUnknown {
		f.HasDepth = true
		f.Depth = d.Score
	}
	for _, m := range cr.Missing {
		f.Missing = append(f.Missing, dimMissingLabel(m))
	}
	return f
}

func dimMissingLabel(dim Dim) string {
	switch dim {
	case DimFreshness:
		return "时效—出版日期不可信"
	case DimTrust:
		return "权威—作者未知"
	case DimDepth:
		return "深度—标注置信度低"
	case DimPractical:
		return "可操作—标注置信度低"
	case DimAcclaim:
		return "口碑—无外部评分"
	case DimCommunity:
		return "声量—无 HN 提及"
	default:
		return string(dim) + "—证据不足"
	}
}

// runPersistInput Run 传给 persist 的中间数据。
type runPersistInput struct {
	IDs       []string
	Presets   []preset.Preset
	Inputs    map[string]*WorkInput
	Dims      map[string]map[Dim]DimScore
	Grade     map[string]string
	CDFs      map[Dim]CDF
	Params    DimParams
	Lists     map[string][]ListEntry
	CorpusID  string
	FactsHash string
}

// persist 写 runs / dim_scores / norm_cdf / lists / published_run(单事务),
// 并在同一事务里回收过期 run —— 发布与回收要么一起成功,要么一起回滚。
func (e *Engine) persist(in runPersistInput) (string, error) {
	// run_id 用纳秒:秒级精度会让同一秒内的两次 score 写进同一个 run_id,
	// 两份榜单互相覆盖成一份混合体。
	runID := fmt.Sprintf("std%s-%d", e.Version, e.Now.UnixNano())
	tx, err := e.DB.SQL().Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	// 同 run_id 重跑时先清干净:否则新榜单比旧的短时,旧的尾部行会留下来。
	for _, t := range []string{"lists", "norm_cdf", "dim_scores"} {
		if _, err := tx.Exec(`DELETE FROM `+t+` WHERE run_id=?`, runID); err != nil {
			return "", fmt.Errorf("clear %s: %w", t, err)
		}
	}

	metrics, err := json.Marshal(runMetrics(in.Grade))
	if err != nil {
		return "", err
	}
	// 纳秒精度:run_id 是纳秒的,时间戳若只到秒,同一秒内的多个 run 就无法按时间
	// 排序 —— 而 gcRuns 正是按 started_at 决定留谁。
	stamp := e.Now.Format(time.RFC3339Nano)
	if _, err := tx.Exec(`INSERT OR REPLACE INTO runs
		(run_id, kind, corpus_id, standard_version, facts_hash, started_at, ended_at, status,
		 ok_count, fail_count, orphan_rows, quota_used, metrics)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		runID, "score", in.CorpusID, e.Version, in.FactsHash, stamp, stamp, "ok",
		len(in.Inputs), 0, 0, "0", string(metrics)); err != nil {
		return "", fmt.Errorf("insert run: %w", err)
	}

	// 预编译:2,054 works × 7 维 ≈ 1.4 万次插入,逐次拼 SQL 会重复解析 1.4 万遍。
	dimStmt, err := tx.Prepare(`INSERT INTO dim_scores
		(run_id, work_id, dim, raw, pct, score, state, source, confidence) VALUES (?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return "", err
	}
	defer dimStmt.Close()
	for _, id := range in.IDs {
		for _, dim := range AllDims {
			d, ok := in.Dims[id][dim]
			if !ok {
				continue
			}
			if _, err := dimStmt.Exec(runID, id, string(dim), d.Raw, d.Pct, d.Score,
				string(d.State), d.Source, d.Confidence); err != nil {
				return "", fmt.Errorf("insert dim_score %s/%s: %w", id, dim, err)
			}
		}
	}

	cdfStmt, err := tx.Prepare(`INSERT INTO norm_cdf (run_id, dim, q, raw) VALUES (?,?,?,?)`)
	if err != nil {
		return "", err
	}
	defer cdfStmt.Close()
	for _, dim := range AllDims {
		cdf, ok := in.CDFs[dim]
		if !ok {
			continue
		}
		for q := 0; q <= 100; q++ {
			if _, err := cdfStmt.Exec(runID, string(dim), q, cdf.Quantile(q)); err != nil {
				return "", fmt.Errorf("insert norm_cdf %s/%d: %w", dim, q, err)
			}
		}
	}

	listStmt, err := tx.Prepare(`INSERT INTO lists
		(run_id, list_id, rank, work_id, tbs, coverage, reason) VALUES (?,?,?,?,?,?,?)`)
	if err != nil {
		return "", err
	}
	defer listStmt.Close()
	for _, p := range in.Presets { // 按 preset 声明序,不遍历 map
		for _, en := range in.Lists[p.ID] {
			if _, err := listStmt.Exec(runID, p.ID, en.Rank, en.WorkID, en.TBS,
				en.Coverage, en.Reason); err != nil {
				return "", fmt.Errorf("insert list %s/%d: %w", p.ID, en.Rank, err)
			}
		}
	}

	if err := gcRuns(tx, runID, e.KeepRuns); err != nil {
		return "", fmt.Errorf("gc runs: %w", err)
	}
	// 原子发布:单行指针最后切。
	if _, err := tx.Exec(`INSERT OR REPLACE INTO published_run (id, run_id) VALUES (1, ?)`, runID); err != nil {
		return "", fmt.Errorf("publish: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return runID, nil
}

// gcRuns 只保留最近 keep 个 run(含刚发布的 keepRun)。
//
// 每夜一个 run 的产物是 2,054 works × 7 维 ≈ 1.4 万行 dim_scores + 707 行
// norm_cdf + 榜单行,约 1.5 万行/夜、一年 ~550 万行 —— 而 PVC 只有 100Mi。
// 回滚只需要最近几个 run,更早的没人会看。
func gcRuns(tx *sql.Tx, keepRun string, keep int) error {
	if keep <= 0 {
		keep = DefaultKeepRuns
	}
	rows, err := tx.Query(
		`SELECT run_id, COALESCE(kind, '') FROM runs ORDER BY started_at DESC, run_id DESC`)
	if err != nil {
		return err
	}
	var doomed []string
	// 按 kind 分别保留:score 的产物是大头,而 snapshot 每夜只有一行,
	// 把两者混在一个配额里会让快照历史被打分历史挤掉。
	kept := map[string]int{"score": 1} // 刚发布的那个已占一个名额
	for rows.Next() {
		var id, kind string
		if err := rows.Scan(&id, &kind); err != nil {
			rows.Close()
			return err
		}
		if id == keepRun {
			continue
		}
		if kept[kind] < keep {
			kept[kind]++
			continue
		}
		doomed = append(doomed, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	// 必须先关掉 rows:连接池上限是 1,同一事务上边读边写会卡死。
	rows.Close()

	for _, id := range doomed {
		for _, t := range []string{"lists", "norm_cdf", "dim_scores", "runs"} {
			if _, err := tx.Exec(`DELETE FROM `+t+` WHERE run_id=?`, id); err != nil {
				return fmt.Errorf("delete %s of %s: %w", t, id, err)
			}
		}
	}
	return nil
}

func runMetrics(grade map[string]string) map[string]any {
	counts := map[string]int{"A": 0, "B": 0, "C": 0, "D": 0}
	for _, g := range grade {
		counts[g]++
	}
	return map[string]any{"grade_counts": counts}
}

// ---------- 语料与证据指纹(可复现性契约,review M3)----------

// corpusID 快照实体集合的 hash。分数是语料相对的(分位归一),所以「分数变了」
// 必须能区分是公式变了还是语料变了 —— 这就是那个区分依据。
func (e *Engine) corpusID() (string, error) {
	return e.hashQueries(
		`SELECT work_id, canonical_title, first_author, primary_topic, level,
		        half_life_years, half_life_source FROM works ORDER BY work_id`,
		`SELECT book_id, work_id, title, isbn13, google_volume_id, publisher_norm, format,
		        language, has_comments, has_cover, pubdate, pubdate_source,
		        personal_rating_stars FROM editions ORDER BY book_id`,
	)
}

// factsHash 外部证据 + 标注 + 人工投入 + 阅读镜像的 hash。
func (e *Engine) factsHash() (string, error) {
	return e.hashQueries(
		`SELECT source, source_id, work_id, payload FROM evidence ORDER BY source, source_id`,
		`SELECT work_id, topic_class, topics, level, depth, practicality, confidence
		   FROM labels ORDER BY work_id`,
		`SELECT work_id, object_id, created_at, matched_by FROM mentions ORDER BY work_id, object_id`,
		`SELECT work_id, field, value FROM overrides ORDER BY work_id, field`,
		`SELECT book_id, status, shelves, downloads FROM reading ORDER BY book_id`,
	)
}

func (e *Engine) hashQueries(queries ...string) (string, error) {
	h := sha256.New()
	for _, q := range queries {
		if err := hashRows(e.DB.SQL(), h, q); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

// hashRows 把查询结果逐行喂进 hash。查询必须自带 ORDER BY —— SQLite 不保证
// 无序查询的行序稳定,少了 ORDER BY 这个 hash 就失去了「同语料同 hash」的意义。
func hashRows(db *sql.DB, h io.Writer, query string) error {
	if !strings.Contains(query, "ORDER BY") {
		return fmt.Errorf("hash query lacks ORDER BY: %q", query)
	}
	rows, err := db.Query(query)
	if err != nil {
		return fmt.Errorf("hash rows: %w", err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		for _, v := range vals {
			fmt.Fprintf(h, "%v\x1f", v)
		}
		fmt.Fprint(h, "\x1e")
	}
	return rows.Err()
}

// ---------- 小工具 ----------

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func yearsSince(t time.Time, now time.Time) float64 {
	return now.Sub(t).Hours() / (24 * 365)
}

func topicIntersects(topics, want []string) bool {
	for _, t := range topics {
		if contains(want, t) {
			return true
		}
	}
	return false
}

// weightsByDim preset 权重 → score.Dim 键。
func weightsByDim(p preset.Preset) map[Dim]float64 {
	out := make(map[Dim]float64, len(p.Weights))
	for k, v := range p.Weights {
		out[Dim(k)] = v
	}
	return out
}

// bandsByDim preset 目标带 → score.Dim 键。
func bandsByDim(p preset.Preset) map[Dim]Band {
	out := make(map[Dim]Band, len(p.Bands))
	for k, v := range p.Bands {
		out[Dim(k)] = Band{Target: v.Target, Tol: v.Tol}
	}
	return out
}

// needsByDim preset needs → score.Needs。
func needsByDim(p preset.Preset) Needs {
	out := make(Needs, len(p.Needs))
	for k, v := range p.Needs {
		out[Dim(k)] = State(v)
	}
	return out
}

// ---------- 加载层 ----------

func (e *Engine) loadWorkInputs() (map[string]*WorkInput, error) {
	// ORDER BY 不是为了好看:下面的「取最优出版社 / 最优格式」在并列时是先到先得,
	// 没有稳定行序就会出不同结果。
	rows, err := e.DB.SQL().Query(`SELECT w.work_id, w.canonical_title, w.first_author, w.primary_topic,
			w.level, w.half_life_years,
			e.book_id, e.publisher_norm, e.format, e.has_comments, e.has_cover, e.isbn13,
			e.pubdate, e.pubdate_source, e.personal_rating_stars, e.language
		FROM works w JOIN editions e ON e.work_id = w.work_id
		ORDER BY w.work_id, e.book_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	inputs := map[string]*WorkInput{}
	bookToWork := map[int]string{}
	for rows.Next() {
		var (
			workID, title, author, topic, level string
			hl                                  sql.NullFloat64
			bookID                              int
			pubNorm, format, language           string
			hasComments, hasCover               bool
			isbn, pubdate, pubdateSrc           sql.NullString
			personal                            sql.NullFloat64
		)
		if err := rows.Scan(&workID, &title, &author, &topic, &level, &hl,
			&bookID, &pubNorm, &format, &hasComments, &hasCover, &isbn, &pubdate,
			&pubdateSrc, &personal, &language); err != nil {
			return nil, err
		}
		w, ok := inputs[workID]
		if !ok {
			w = &WorkInput{
				WorkID: workID, Title: title, FirstAuthor: author,
				PrimaryTopic: topic, Level: level,
				HalfLifeYears: hl.Float64,
				PublisherNorm: pubNorm, Format: format, Language: language,
			}
			inputs[workID] = w
		}
		bookToWork[bookID] = workID
		w.HasComments = w.HasComments || hasComments
		w.HasCover = w.HasCover || hasCover
		w.HasISBN = w.HasISBN || (isbn.Valid && isbn.String != "")
		if !w.MetadataFull && pubdate.Valid && pubdate.String != "" && pubNorm != "" {
			w.MetadataFull = true
		}
		// 出版社取最优 tier(数字小 = 优)。
		if pi := corpus.Publisher(pubNorm); w.PublisherTier == 0 || pi.Tier < w.PublisherTier {
			w.PublisherTier = pi.Tier
			w.PublisherNorm = pi.Norm
		}
		// 格式取可读性最优。
		if corpus.FormatRank(format) > corpus.FormatRank(w.Format) {
			w.Format = format
		}
		if t, ok := parsePubdate(pubdate); ok {
			if w.FirstPubdate == nil || t.Before(*w.FirstPubdate) {
				first := t
				w.FirstPubdate = &first
			}
			if w.LatestPubdate == nil || t.After(*w.LatestPubdate) {
				latest := t
				w.LatestPubdate = &latest
			}
			if TrustedPubdateSources[pubdateSrc.String] &&
				(w.TrustedPubdate == nil || t.After(*w.TrustedPubdate)) {
				trusted := t
				w.TrustedPubdate = &trusted
				w.PubdateSource = pubdateSrc.String
			}
		}
		if personal.Valid && personal.Float64 > 0 {
			if !w.HasPersonal || personal.Float64 > w.PersonalRating {
				w.PersonalRating = personal.Float64
				w.HasPersonal = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 未来日期 = 污染强信号 → F 记 unknown(夹逼,system-design §3.5)。
	for _, w := range inputs {
		if w.TrustedPubdate != nil && w.TrustedPubdate.After(e.Now) {
			w.TrustedPubdate = nil
			w.PubdateSource = "unknown"
		}
	}

	for _, load := range []func(map[string]*WorkInput) error{
		e.loadRatings, e.loadMentions, e.loadLabels,
	} {
		if err := load(inputs); err != nil {
			return nil, err
		}
	}
	if err := e.loadReading(inputs, bookToWork); err != nil {
		return nil, err
	}
	// 无记录 = 未读(统一模型语义)。
	for _, w := range inputs {
		if !w.HasReading {
			w.ReadStatus = "unread"
		}
	}
	return inputs, nil
}

func parsePubdate(v sql.NullString) (time.Time, bool) {
	if !v.Valid || len(v.String) < 10 {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02", v.String[:10])
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// 下面四个加载器都带 ORDER BY:评分与提及衰减是浮点累加,加法次序会改变最低位。
func (e *Engine) loadRatings(inputs map[string]*WorkInput) error {
	rows, err := e.DB.SQL().Query(
		`SELECT source, work_id, payload FROM evidence ORDER BY source, source_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var source, workID, payload string
		if err := rows.Scan(&source, &workID, &payload); err != nil {
			return err
		}
		w, ok := inputs[workID]
		if !ok {
			continue
		}
		var body struct {
			Rating float64 `json:"rating"`
			Count  int     `json:"count"`
		}
		if json.Unmarshal([]byte(payload), &body) != nil || body.Count <= 0 {
			continue
		}
		w.Ratings = append(w.Ratings, Rating{Source: source, Rating: body.Rating, Count: body.Count})
	}
	return rows.Err()
}

func (e *Engine) loadMentions(inputs map[string]*WorkInput) error {
	rows, err := e.DB.SQL().Query(
		`SELECT work_id, created_at FROM mentions ORDER BY work_id, object_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var workID, created string
		if err := rows.Scan(&workID, &created); err != nil {
			return err
		}
		w, ok := inputs[workID]
		if !ok {
			continue
		}
		if t, err := time.Parse(time.RFC3339, created); err == nil {
			w.Mentions = append(w.Mentions, Mention{CreatedAt: t})
		}
	}
	return rows.Err()
}

func (e *Engine) loadLabels(inputs map[string]*WorkInput) error {
	rows, err := e.DB.SQL().Query(`SELECT work_id, topic_class, topics, level, depth,
		practicality, confidence FROM labels ORDER BY work_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var workID, topicClass, topicsJSON, level string
		var depth, practicality, confidence float64
		if err := rows.Scan(&workID, &topicClass, &topicsJSON, &level, &depth,
			&practicality, &confidence); err != nil {
			return err
		}
		w, ok := inputs[workID]
		if !ok {
			continue
		}
		w.Label = &Label{TopicClass: topicClass, Level: level, Depth: depth,
			Practicality: practicality, Confidence: confidence}
		var topics []string
		_ = json.Unmarshal([]byte(topicsJSON), &topics)
		w.Topics = append(w.Topics, topics...)
		if topicClass != "" {
			w.Topics = append(w.Topics, topicClass)
		}
	}
	return rows.Err()
}

func (e *Engine) loadReading(inputs map[string]*WorkInput, bookToWork map[int]string) error {
	rows, err := e.DB.SQL().Query(
		`SELECT book_id, status, shelves FROM reading ORDER BY book_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var bookID int
		var status, shelvesJSON string
		if err := rows.Scan(&bookID, &status, &shelvesJSON); err != nil {
			return err
		}
		workID, ok := bookToWork[bookID]
		if !ok {
			continue
		}
		w := inputs[workID]
		if w == nil {
			continue
		}
		w.HasReading = true
		if status != "" {
			w.ReadStatus = status
		}
		var shelves []string
		_ = json.Unmarshal([]byte(shelvesJSON), &shelves)
		w.Shelves = append(w.Shelves, shelves...)
	}
	return rows.Err()
}
