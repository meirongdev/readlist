package score

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/meirongdev/readlist/internal/corpus"
	"github.com/meirongdev/readlist/internal/preset"
	"github.com/meirongdev/readlist/internal/selection"
	"github.com/meirongdev/readlist/internal/store"
)

// Engine 评分引擎:facts → judgement(dim_scores+norm_cdf) → selection(lists) → publish。
// 约束:score 命令不发起任何网络请求(system-design §9)。
type Engine struct {
	DB      *store.DB
	Version string
	Now     time.Time
}

// RunResult 一次评分的结果(便于测试断言与 dryrun 复用)。
type RunResult struct {
	RunID  string
	Works  map[string]*WorkInput
	Dims   map[string]map[Dim]DimScore
	Grade  map[string]string
	Lists  map[string][]ListEntry
	CDFs   map[Dim]CDF
	Params DimParams
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
	return &Engine{DB: d, Version: version, Now: now}
}

// Computation 计算中间产物(dryrun 不落库,便于只数不算)。
type Computation struct {
	Raws   map[string]map[Dim]DimResult
	Params DimParams
	CDFs   map[Dim]CDF
	Dims   map[string]map[Dim]DimScore
	Grade  map[string]string
	Lists  map[string][]ListEntry
	Inputs map[string]*WorkInput
}

// Compute 计算 judgement + selection(不持久化)。
func (e *Engine) Compute(presets []preset.Preset) (*Computation, error) {
	inputs, err := e.loadWorkInputs()
	if err != nil {
		return nil, err
	}
	// 1) 第一遍 raw(初始参数)→ 从实测推导语料先验与置信阈值 m。
	initial := e.Params()
	raws1 := map[string]map[Dim]DimResult{}
	for id, w := range inputs {
		raws1[id] = w.Compute(initial, e.Now)
	}
	params := e.computeParams(raws1)
	// 2) 第二遍 raw(真实参数,保证贝叶斯用的是语料先验)。
	raws := map[string]map[Dim]DimResult{}
	for id, w := range inputs {
		raws[id] = w.Compute(params, e.Now)
	}
	// 3) 每维 CDF(只由 measured 构建)
	cdfs := map[Dim]CDF{}
	for _, dim := range AllDims {
		var vals []float64
		for id := range inputs {
			if raws[id][dim].State == StateMeasured {
				vals = append(vals, raws[id][dim].Raw)
			}
		}
		cdfs[dim] = BuildCDF(dim, vals)
	}
	// 4) 归一化为 DimScore
	dims := map[string]map[Dim]DimScore{}
	for id := range inputs {
		dims[id] = e.normalize(raws[id], cdfs, params)
	}
	// 5) 证据等级徽章
	grade := map[string]string{}
	for id := range inputs {
		grade[id] = Grade(dims[id])
	}
	// 6) selection
	lists := map[string][]ListEntry{}
	for _, p := range presets {
		entries := e.selectList(p, inputs, dims, grade)
		lists[p.ID] = entries
	}
	return &Computation{Raws: raws, Params: params, CDFs: cdfs, Dims: dims,
		Grade: grade, Lists: lists, Inputs: inputs}, nil
}

// Run 完整跑一遍 judgement + selection + publish。
func (e *Engine) Run(presets []preset.Preset) (*RunResult, error) {
	c, err := e.Compute(presets)
	if err != nil {
		return nil, err
	}
	runID := e.persist(runPersistInput{
		Presets: presets, Inputs: c.Inputs, Dims: c.Dims, Grade: c.Grade,
		CDFs: c.CDFs, Params: c.Params, Lists: c.Lists,
	})
	return &RunResult{
		RunID: runID, Works: c.Inputs, Dims: c.Dims, Grade: c.Grade,
		Lists: c.Lists, CDFs: c.CDFs, Params: c.Params,
	}, nil
}

// Params 语料先验与置信阈值(独立可测)。
func (e *Engine) Params() DimParams {
	return DimParams{AcclaimPrior: 4.1, MinCount: 20}
}

func (e *Engine) computeParams(raws map[string]map[Dim]DimResult) DimParams {
	p := e.Params()
	var sum, cnt float64
	var counts []int
	for _, r := range raws {
		if a, ok := r[DimAcclaim]; ok && a.State == StateMeasured {
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
		mid := len(counts) / 2
		median = float64(counts[mid])
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

// selectList 按预设选材(过滤 → coverage/TBS → 多样性 → 理由)。
func (e *Engine) selectList(p preset.Preset, inputs map[string]*WorkInput,
	dims map[string]map[Dim]DimScore, grade map[string]string) []ListEntry {

	weights := weightsByDim(p)
	bands := bandsByDim(p)
	needs := needsByDim(p)

	var cands []selection.Candidate
	for id, w := range inputs {
		if grade[id] == "D" {
			continue // D 级不公开
		}
		if !e.filterPass(p, w) {
			continue
		}
		cr := Combine(dims[id], weights, bands, needs)
		cands = append(cands, selection.Candidate{
			WorkID:        id,
			Topic:         w.PrimaryTopic,
			FirstAuthor:   w.FirstAuthor,
			TBS:           cr.TBS,
			Coverage:      cr.Coverage,
			AvailableDims: len(cr.Available),
			Facts:         e.facts(id, w, dims[id], cr.Missing, cr.Coverage, len(cr.Available)),
		})
	}
	cfg := selection.Config{
		Size:         p.Select.Size,
		MaxPerTopic:  p.Select.MaxPerTopic,
		MaxPerAuthor: p.Select.MaxPerAuthor,
		MinCoverage:  p.Select.MinCoverage,
		Asc:          p.Order == "asc",
	}
	entries := selection.Select(cands, cfg)
	out := make([]ListEntry, 0, len(entries))
	for i, en := range entries {
		out = append(out, ListEntry{Rank: i + 1, WorkID: en.WorkID, TBS: en.TBS, Coverage: en.Coverage, Reason: en.Reason})
	}
	return out
}

// filterPass 命中预设过滤器(必要条件)。
func (e *Engine) filterPass(p preset.Preset, w *WorkInput) bool {
	f := p.Filters
	if f.MinAgeYears > 0 {
		if w.BestPubdate == nil || yearsSince(*w.BestPubdate, e.Now) < float64(f.MinAgeYears) {
			return false
		}
	}
	if f.PubdateYear > 0 {
		if w.BestPubdate == nil || w.BestPubdate.Year() != f.PubdateYear {
			return false
		}
	}
	if len(f.PubdateSource) > 0 {
		if !contains(f.PubdateSource, w.PubdateSource) {
			return false
		}
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
func (e *Engine) facts(id string, w *WorkInput, dims map[Dim]DimScore, missing []Dim, coverage float64, avail int) selection.Facts {
	f := selection.Facts{
		Publisher:       w.PublisherNorm,
		Author:          w.FirstAuthor,
		AuthorIsUnknown: stringsEqualFold(w.FirstAuthor, "unknown"),
		HasHalfLife:     w.HalfLifeYears > 0,
		HalfLifeYears:   w.HalfLifeYears,
		Grade:           Grade(dims),
		Coverage:        coverage,
		AvailableDims:   avail,
	}
	if len(w.Mentions) > 0 {
		var times []time.Time
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
	for _, m := range missing {
		f.Missing = append(f.Missing, dimMissingLabel(m, dims[m]))
	}
	return f
}

func dimMissingLabel(dim Dim, ds DimScore) string {
	switch dim {
	case DimFreshness:
		return "时效—出版日期不可信"
	case DimTrust:
		return "权威—作者未知"
	case DimDepth:
		return "深度—标注置信度低"
	case DimPractical:
		return "可操作—标注置信度低"
	default:
		return string(dim) + "—证据不足"
	}
}

// persist 写 runs / dim_scores / norm_cdf / lists / published_run(单事务)。
func (e *Engine) persist(in runPersistInput) string {
	runID := fmt.Sprintf("std%s-%d", e.Version, e.Now.Unix())
	db := e.DB.SQL()
	tx, err := db.Begin()
	if err != nil {
		panic(err)
	}
	defer tx.Rollback()

	metrics, _ := json.Marshal(runMetrics(in.Grade))
	started := in.startedAt(e.Now)
	if _, err := tx.Exec(`INSERT OR REPLACE INTO runs
		(run_id, kind, corpus_id, standard_version, facts_hash, started_at, ended_at, status, ok_count, fail_count, orphan_rows, quota_used, metrics)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		runID, "score", corpusID(in.Inputs), e.Version, "mvp", started, e.Now.Format(time.RFC3339), "ok",
		len(in.Inputs), 0, 0, "0", string(metrics)); err != nil {
		panic(err)
	}

	for id, ds := range in.Dims {
		for dim, d := range ds {
			if _, err := tx.Exec(`INSERT OR REPLACE INTO dim_scores
				(run_id, work_id, dim, raw, pct, score, state, source, confidence)
				VALUES (?,?,?,?,?,?,?,?,?)`,
				runID, id, string(dim), d.Raw, d.Pct, d.Score, string(d.State), d.Source, d.Confidence); err != nil {
				panic(err)
			}
		}
	}
	for dim, cdf := range in.CDFs {
		for q := 0; q <= 100; q++ {
			if _, err := tx.Exec(`INSERT OR REPLACE INTO norm_cdf (run_id, dim, q, raw) VALUES (?,?,?,?)`,
				runID, string(dim), q, cdf.Quantile(q)); err != nil {
				panic(err)
			}
		}
	}
	for listID, entries := range in.Lists {
		for _, en := range entries {
			if _, err := tx.Exec(`INSERT OR REPLACE INTO lists
				(run_id, list_id, rank, work_id, tbs, coverage, reason) VALUES (?,?,?,?,?,?,?)`,
				runID, listID, en.Rank, en.WorkID, en.TBS, en.Coverage, en.Reason); err != nil {
				panic(err)
			}
		}
	}
	// 原子发布:单行指针最后切。
	if _, err := tx.Exec(`INSERT OR REPLACE INTO published_run (id, run_id) VALUES (1, ?)`, runID); err != nil {
		panic(err)
	}
	if err := tx.Commit(); err != nil {
		panic(err)
	}
	return runID
}

// runPersistInput Run 传给 persist 的中间数据。
type runPersistInput struct {
	Presets []preset.Preset
	Inputs  map[string]*WorkInput
	Dims    map[string]map[Dim]DimScore
	Grade   map[string]string
	CDFs    map[Dim]CDF
	Params  DimParams
	Lists   map[string][]ListEntry
}

func (r runPersistInput) startedAt(now time.Time) string { return now.Format(time.RFC3339) }

func runMetrics(grade map[string]string) map[string]any {
	counts := map[string]int{"A": 0, "B": 0, "C": 0, "D": 0}
	for _, g := range grade {
		counts[g]++
	}
	return map[string]any{"grade_counts": counts}
}

func corpusID(inputs map[string]*WorkInput) string {
	return fmt.Sprintf("mvp-%d", len(inputs))
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func stringsEqualFold(a, b string) bool { return strings.EqualFold(a, b) }

func yearsSince(t time.Time, now time.Time) float64 {
	return now.Sub(t).Hours() / (24 * 365)
}

func topicIntersects(topics, want []string) bool {
	for _, t := range topics {
		for _, w := range want {
			if t == w {
				return true
			}
		}
	}
	return false
}

// weightsByDim preset 权重 → score.Dim 键。
func weightsByDim(p preset.Preset) map[Dim]float64 {
	out := map[Dim]float64{}
	for k, v := range p.Weights {
		out[Dim(k)] = v
	}
	return out
}

// bandsByDim preset 目标带 → score.Dim 键。
func bandsByDim(p preset.Preset) map[Dim]Band {
	out := map[Dim]Band{}
	for k, v := range p.Bands {
		out[Dim(k)] = Band{Target: v.Target, Tol: v.Tol}
	}
	return out
}

// needsByDim preset needs → score.Needs。
func needsByDim(p preset.Preset) Needs {
	out := Needs{}
	for k, v := range p.Needs {
		out[Dim(k)] = State(v)
	}
	return out
}

// ---------- 加载层 ----------

func (e *Engine) loadWorkInputs() (map[string]*WorkInput, error) {
	db := e.DB.SQL()
	rows, err := db.Query(`SELECT w.work_id, w.canonical_title, w.first_author, w.primary_topic, w.level, w.half_life_years,
		e.book_id, e.publisher_norm, e.format, e.has_comments, e.has_cover, e.isbn13, e.pubdate, e.pubdate_source,
		e.personal_rating_stars, e.language
		FROM works w JOIN editions e ON e.work_id = w.work_id`)
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
			&bookID, &pubNorm, &format, &hasComments, &hasCover, &isbn, &pubdate, &pubdateSrc, &personal, &language); err != nil {
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
		pi := corpus.Publisher(pubNorm)
		if w.PublisherTier == 0 || pi.Tier < w.PublisherTier {
			w.PublisherTier = pi.Tier
			w.PublisherNorm = pi.Norm
		}
		// 格式取可读性最优。
		if formatRank(format) > formatRank(w.Format) {
			w.Format = format
		}
		// pubdate:best = 任意来源最新;trusted = 仅可信来源最新。
		if pubdate.Valid && pubdate.String != "" {
			if t, err := time.Parse("2006-01-02", pubdate.String[:10]); err == nil {
				if w.BestPubdate == nil || t.After(*w.BestPubdate) {
					w.BestPubdate = &t
				}
				if TrustedPubdateSources[pubdateSrc.String] && (w.TrustedPubdate == nil || t.After(*w.TrustedPubdate)) {
					wt := t
					w.TrustedPubdate = &wt
					w.PubdateSource = pubdateSrc.String
				}
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

	// 外部评分
	if err := e.loadRatings(inputs); err != nil {
		return nil, err
	}
	// HN 提及
	if err := e.loadMentions(inputs); err != nil {
		return nil, err
	}
	// 标注
	if err := e.loadLabels(inputs); err != nil {
		return nil, err
	}
	// 阅读状态(book_id → work)
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

func formatRank(f string) int {
	switch f {
	case "EPUB":
		return 4
	case "AZW3", "MOBI":
		return 3
	case "PDF":
		return 2
	default:
		return 1
	}
}

func (e *Engine) loadRatings(inputs map[string]*WorkInput) error {
	rows, err := e.DB.SQL().Query(`SELECT source, work_id, payload FROM evidence`)
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
	rows, err := e.DB.SQL().Query(`SELECT work_id, created_at FROM mentions`)
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
	rows, err := e.DB.SQL().Query(`SELECT work_id, topic_class, topics, level, depth, practicality, confidence FROM labels`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var workID, topicClass, topicsJSON, level string
		var depth, practicality, confidence float64
		if err := rows.Scan(&workID, &topicClass, &topicsJSON, &level, &depth, &practicality, &confidence); err != nil {
			return err
		}
		w, ok := inputs[workID]
		if !ok {
			continue
		}
		w.Label = &Label{TopicClass: topicClass, Level: level, Depth: depth, Practicality: practicality, Confidence: confidence}
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
	rows, err := e.DB.SQL().Query(`SELECT book_id, status, shelves FROM reading`)
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
