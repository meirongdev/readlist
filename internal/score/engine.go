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
	// 5) 证据等级徽章(仅展示,不做准入 —— 见 Grade 的注释)。
	//
	// 徽章看的是**实际参与排序的维度**,所以它的口径随 presets 走,而不是七维全看。
	// 这个集合是全局的(所有 preset 的加权维并集),不是逐榜的:同一本书在目录页、
	// 详情页和三份榜上必须是同一个字母,否则读者看到的是三个互相矛盾的评价。
	graded := GradedDims(presets)
	grade := map[string]string{}
	for _, id := range ids {
		grade[id] = Grade(dims[id], graded)
	}
	// 6) selection(含人工 veto / pin)
	manual, err := e.loadManualLists()
	if err != nil {
		return nil, err
	}
	lists := map[string][]ListEntry{}
	for _, p := range presets {
		lists[p.ID] = e.selectList(p, ids, inputs, dims, manual)
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

// anyList veto 的通配目标:不写 value 就是「所有榜单都不要它」。
const anyList = "*"

// ManualLists 人工干预榜单的两个开关(overrides 表,`field` 取 veto / pin):
//
//	field='veto', value='' 或 '<list_id>'  → 该书不进(全部 / 指定)榜单
//	field='pin',  value='<list_id>'        → 该书强制进该榜,排在算法结果之前
//
// 为什么需要它:一本明显不该出现在公开榜上的书,此前唯一的处置办法是改代码或改权重
// —— 后者会为了一本书扭曲所有书的排名。而 system-design §13 把「timeless 是否接受
// 一层人工 curation」列为必须由库主人决定的两件事之一,在此之前想选「接受」也没有
// 开关可拨(review B6)。
//
// pin 刻意绕过 filters / needs / coverage / 多样性约束 —— 它的用途正是
// 「这本书证据薄,但我愿意为它的排名辩护」。代价是必须让读者看见:理由串会写明
// 「人工置顶」,curation 不该伪装成算法结果。
type ManualLists struct {
	veto map[string]map[string]bool // work_id → {list_id | "*"}
	pin  map[string]map[string]bool // work_id → {list_id}
}

// Vetoed 这本书是否被否决出这份榜(或被全站否决)。
func (m ManualLists) Vetoed(workID, listID string) bool {
	s := m.veto[workID]
	return s[listID] || s[anyList]
}

// Pinned 这本书是否被人工置顶到这份榜。
func (m ManualLists) Pinned(workID, listID string) bool { return m.pin[workID][listID] }

func (e *Engine) loadManualLists() (ManualLists, error) {
	out := ManualLists{veto: map[string]map[string]bool{}, pin: map[string]map[string]bool{}}
	rows, err := e.DB.SQL().Query(`SELECT work_id, field, COALESCE(value,'') FROM overrides
		WHERE field IN ('veto','pin') ORDER BY work_id, field, value`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var workID, field, value string
		if err := rows.Scan(&workID, &field, &value); err != nil {
			return out, err
		}
		target := out.veto
		if field == "pin" {
			target = out.pin
		}
		if target[workID] == nil {
			target[workID] = map[string]bool{}
		}
		// overrides 的主键是 (work_id, field),一个 work 每种操作只有一行 ——
		// 所以 value 支持逗号分隔多个榜 id,否则「否决出两份榜」就无法表达。
		for _, listID := range strings.Split(value, ",") {
			listID = strings.TrimSpace(listID)
			if listID == "" {
				if field == "pin" {
					continue // pin 必须指明榜单:「置顶到所有榜」不是一个有意义的意图
				}
				listID = anyList // veto 不写 value = 全站否决
			}
			target[workID][listID] = true
		}
	}
	return out, rows.Err()
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
	dims map[string]map[Dim]DimScore, manual ManualLists) []ListEntry {

	weights := weightsByDim(p)
	bands := bandsByDim(p)
	needs := needsByDim(p)

	cands := make([]selection.Candidate, 0, len(ids))
	for _, id := range ids {
		w := inputs[id]
		if manual.Vetoed(id, p.ID) {
			continue
		}
		// 人工置顶绕过全部准入 —— 见 ManualLists 的说明。
		pinned := manual.Pinned(id, p.ID)
		if !pinned {
			if !e.filterPass(p, w) {
				continue
			}
			if !NeedsSatisfied(dims[id], needs) {
				continue
			}
		}
		cr := Combine(dims[id], weights, bands, needs)
		facts := e.facts(p, w, dims[id], cr)
		facts.Pinned = pinned
		cands = append(cands, selection.Candidate{
			WorkID:      id,
			Topic:       w.PrimaryTopic,
			FirstAuthor: w.FirstAuthor,
			TBS:         cr.TBS,
			Coverage:    cr.Coverage,
			Pinned:      pinned,
			Facts:       facts,
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
	//
	// ⚠️ 两个方向的时间过滤器刻意**不对称**,因为代价不对称:
	//   「够不够新」(pubdate_within_months)日期未知 → 排除。想上「新书榜」得先证明自己新。
	//   「够不够老」(min_age_years)  日期未知 → **不判失败**。
	// 若这里也失败,那 477 本(23%)只有 mtime 兜底日期、且没有标识符可供 ingest 补救的书
	// 会整批从「经典长青」消失 —— 那正是 review B1 判定为「模型错了」的那种全局闸门,
	// 只不过换成从过滤器进来。个人技术书库里「有些年头了」本来就是默认状态,
	// 误收一本两年新书的代价远小于丢掉 477 本经典。
	// 要严格,preset 自己声明 `needs: {F: measured}` —— 那才是「我要求日期可信」的表达方式。
	if f.MinAgeYears > 0 && w.FirstPubdate != nil {
		if yearsSince(*w.FirstPubdate, e.Now) < float64(f.MinAgeYears) {
			return false
		}
	}
	// 「新书」看**可信**的最新版次日期,不是任意来源的最新日期 —— 一本 2015 年的书只要
	// 还有一个版次的 pubdate 被 mtime 写成了今年,就能靠 LatestPubdate 混进这个榜。
	// 用滚动窗口而不是硬编码年份,否则跨年后这个榜会静默变成「去年的书」(review m3)。
	if f.PubdateWithinMonths > 0 {
		if w.TrustedPubdate == nil || w.TrustedPubdate.Before(e.Now.AddDate(0, -f.PubdateWithinMonths, 0)) {
			return false
		}
	}
	if f.PubdateYear > 0 {
		if w.TrustedPubdate == nil || w.TrustedPubdate.Year() != f.PubdateYear {
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
func (e *Engine) facts(p preset.Preset, w *WorkInput, dims map[Dim]DimScore,
	cr CombineResult) selection.Facts {

	f := selection.Facts{
		Publisher:       w.PublisherNorm,
		Author:          w.FirstAuthor,
		AuthorIsUnknown: strings.EqualFold(strings.TrimSpace(w.FirstAuthor), "unknown"),
		HasHalfLife:     w.HalfLifeYears > 0,
		HalfLifeYears:   w.HalfLifeYears,
		Coverage:        cr.Coverage,
		AvailableDims:   len(cr.Available),
		TotalDims:       cr.TotalDims,
		// 年龄下限对「日期未知」是放行的 —— 放行就得在理由串里写明。
		AgeUnverified: p.Filters.MinAgeYears > 0 && w.FirstPubdate == nil,
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
//
// evidence 不含 `work_id`:一条外部证据的**事实**是 `(source, source_id, payload)`,
// 「它属于哪个 work」是语料侧的信息(由 editions/works 的标识符决定,已被 corpus_id 覆盖)。
// 把那一列算进来,会让「只改了书名」表现为 facts 变化,给升版 diff 评审添假信号。
func (e *Engine) factsHash() (string, error) {
	return e.hashQueries(
		`SELECT source, source_id, payload FROM evidence ORDER BY source, source_id`,
		`SELECT work_id, topic_class, topics, level, depth, practicality, confidence
		   FROM labels ORDER BY work_id`,
		`SELECT work_id, object_id, created_at, matched_by FROM mentions ORDER BY work_id, object_id`,
		`SELECT work_id, field, value FROM overrides ORDER BY work_id, field`,
		`SELECT work_id, object_id, verdict FROM mention_overrides ORDER BY work_id, object_id`,
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
		// 污染来源的日期一条都不进聚合。mtime 兜底值按构造就落在「最近」,让它进
		// First/Latest 会同时造成两种错:把老书塞进「近一年新书」,又把该上榜的老书
		// 从 min_age_years 里挡掉(review A2)。
		if t, ok := parsePubdate(pubdate); ok && PubdateUsableForAge(pubdateSrc.String) {
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

// evidenceWorkIndex 外部实体 id → **当前** work_id。
//
// evidence 行按外部实体 id 存(volume id / OL work id / ISBN),但每行还记着写入当时的
// work_id。而 work_id 是 `姓氏 + 规范标题` 的派生键 —— 在 calibre 里修一个书名 typo
// 就会让它变,于是那本书的证据静默失联,且因为查询标记还新鲜,最长 180 天不会被重抓。
// 「补元数据」恰恰是文档反复鼓励库主人做的事,这个动作本身不该打掉证据。
//
// 所以消费侧**读取时解析**:以 editions/works 里当前的标识符为准,`evidence.work_id`
// 降级为兜底。这同时拆掉了「OL work id 升级聚类键」那颗地雷 —— 换聚类键不再让
// evidence 整体失联。
func (e *Engine) evidenceWorkIndex() (map[string]string, error) {
	idx := map[string]string{}
	// 首个命中优先 + 查询自带 ORDER BY = 确定性(NFR-10)。
	put := func(key, workID string) {
		if key == "" {
			return
		}
		if _, seen := idx[key]; !seen {
			idx[key] = workID
		}
	}
	rows, err := e.DB.SQL().Query(`SELECT work_id, COALESCE(google_volume_id,''),
			COALESCE(isbn13,'') FROM editions ORDER BY book_id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var workID, volumeID, isbn string
		if err := rows.Scan(&workID, &volumeID, &isbn); err != nil {
			rows.Close()
			return nil, err
		}
		put(volumeID, workID)
		put(isbn, workID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	wrows, err := e.DB.SQL().Query(
		`SELECT work_id, COALESCE(ol_work_id,'') FROM works ORDER BY work_id`)
	if err != nil {
		return nil, err
	}
	defer wrows.Close()
	for wrows.Next() {
		var workID, olID string
		if err := wrows.Scan(&workID, &olID); err != nil {
			return nil, err
		}
		put(olID, workID)
		put(workID, workID) // HN 标记行的 source_id 就是 work_id
	}
	return idx, wrows.Err()
}

// resolveEvidenceWork 把一条 evidence 的 source_id 解析到当前 work;解析不出才用兜底值。
func resolveEvidenceWork(idx map[string]string, sourceID, stored string) string {
	if wid, ok := idx[sourceID]; ok {
		return wid
	}
	// 兼容带前缀的键(`isbn:…` / `gvol:…`)。work_id 里不会出现冒号 ——
	// normalizeTitle 已经把标点折成空白。
	if i := strings.IndexByte(sourceID, ':'); i >= 0 {
		if wid, ok := idx[sourceID[i+1:]]; ok {
			return wid
		}
	}
	return stored
}

// 下面四个加载器都带 ORDER BY:评分与提及衰减是浮点累加,加法次序会改变最低位。
func (e *Engine) loadRatings(inputs map[string]*WorkInput) error {
	idx, err := e.evidenceWorkIndex()
	if err != nil {
		return err
	}
	rows, err := e.DB.SQL().Query(
		`SELECT source, source_id, work_id, payload FROM evidence ORDER BY source, source_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var source, sourceID, storedWorkID, payload string
		if err := rows.Scan(&source, &sourceID, &storedWorkID, &payload); err != nil {
			return err
		}
		w, ok := inputs[resolveEvidenceWork(idx, sourceID, storedWorkID)]
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

// loadMentions 载入 HN 提及。被人工否决的 objectID 直接不计入声量维 ——
// 通用短标题("Clean Code"、"Refactoring")命中无关讨论是 R-3 里概率标「高」的风险,
// mentions 保留 objectID 就是为了能逐条点开验证并否决,而在此之前没有任何生效路径。
func (e *Engine) loadMentions(inputs map[string]*WorkInput) error {
	rows, err := e.DB.SQL().Query(
		`SELECT m.work_id, m.created_at FROM mentions m
		 LEFT JOIN mention_overrides o
		        ON o.work_id = m.work_id AND o.object_id = m.object_id
		  WHERE COALESCE(o.verdict,'') <> 'reject'
		  ORDER BY m.work_id, m.object_id`)
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
