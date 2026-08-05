package score

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/meirongdev/readlist/internal/corpus"
	"github.com/meirongdev/readlist/internal/preset"
	"github.com/meirongdev/readlist/internal/selection"
	"github.com/meirongdev/readlist/internal/store"
)

var testNow = time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)

// ---------- 维度公式 ----------

func TestAcclaimBayesianShrinksLowCount(t *testing.T) {
	// 1 人打 5 星 → 被拉回先验,而不是登顶。
	w := &WorkInput{Ratings: []Rating{{Source: "google_books", Rating: 5, Count: 1}}}
	r := w.acclaim(DimParams{AcclaimPrior: 4.1, MinCount: 20})
	require.Equal(t, StateMeasured, r.State)
	require.Less(t, r.Raw, 4.8, "单人高分应被贝叶斯收缩")
	require.Greater(t, r.Raw, 4.1, "有实测证据时不应低于先验太多")
}

func TestAcclaimPoolsCountsBeforeShrinking(t *testing.T) {
	// 同一本书被三个源各收录 30 人评分:必须先汇总成 90 人再收缩**一次**。
	// 逐源各收缩一次再加权平均,等于把同一本书的评分人数拆散,系统性低估多源/
	// 多版次的书(review M6:3,000 人的评分被当成 3 × 600 算)。
	p := DimParams{AcclaimPrior: 4.0, MinCount: 20}
	w := &WorkInput{Ratings: []Rating{
		{Source: "google_books", Rating: 4.6, Count: 30},
		{Source: "openlibrary", Rating: 4.6, Count: 30},
		{Source: "other", Rating: 4.6, Count: 30},
	}}
	got := w.acclaim(p)
	require.Equal(t, 90, got.Count, "评分人数必须在 work 级求和")

	pooled := 90.0/(90+20)*4.6 + 20.0/(90+20)*4.0
	perSource := 30.0/(30+20)*4.6 + 20.0/(30+20)*4.0
	require.InDelta(t, pooled, got.Raw, 1e-9)
	require.Greater(t, got.Raw, perSource, "汇总后收缩必须严格优于逐源收缩再平均")
}

func TestCommunityTimeDecay(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	w := &WorkInput{Mentions: []Mention{{CreatedAt: now}, {CreatedAt: now.AddDate(-8, 0, 0)}}}
	r := w.community(now)
	// 今年 = 1,8 年前 ≈ 1/(1+2)=0.333
	require.InDelta(t, 1.0+1.0/3.0, r.Raw, 0.01)
	require.Equal(t, StateMeasured, r.State)
}

func TestFreshnessUnknownOnUntrustedPubdate(t *testing.T) {
	w := &WorkInput{} // 无可信 pubdate
	r := w.freshness(testNow)
	require.Equal(t, StateUnknown, r.State)
}

func TestFreshnessClampsFuturePubdate(t *testing.T) {
	// 未来日期本身是污染的强信号;万一漏进来也不能让 F > 100(0–100 不变量)。
	future := testNow.AddDate(2, 0, 0)
	w := &WorkInput{TrustedPubdate: &future, HalfLifeYears: 5}
	r := w.freshness(testNow)
	require.LessOrEqual(t, r.Raw, 100.0)
}

func TestTrustUnknownWhenBothUnknown(t *testing.T) {
	w := &WorkInput{FirstAuthor: "Unknown", PublisherTier: 4}
	r := w.trust()
	require.Equal(t, StateUnknown, r.State)
}

func TestBandScorePunishesDeepBook(t *testing.T) {
	band := Band{Target: 35, Tol: 25}
	shallow := BandScore(35, band)
	deep := BandScore(90, band)
	require.InDelta(t, 100, shallow, 0.01)
	require.Less(t, deep, shallow, "太深的书在目标带里应减分")
	require.Equal(t, 0.0, BandScore(150, band))
}

// ---------- 归一化 ----------

func TestMidRankPctIsFloatMidRank(t *testing.T) {
	// 三个并列值的 mid-rank = (0 + 3/2)/3 = 50%。写成整数除法会得到 (0+1)/3 ≈ 33%。
	require.InDelta(t, 50.0, BuildCDF(DimAcclaim, []float64{7, 7, 7}).MidRankPct(7), 1e-9)
	// 只有一个 measured 值时它该落在中位,而不是 0 分位(那是 min-rank 的症状)。
	require.InDelta(t, 50.0, BuildCDF(DimAcclaim, []float64{4.2}).MidRankPct(4.2), 1e-9)

	c := BuildCDF(DimAcclaim, []float64{1, 2, 3, 4})
	require.InDelta(t, 12.5, c.MidRankPct(1), 1e-9)
	require.InDelta(t, 87.5, c.MidRankPct(4), 1e-9)
	require.InDelta(t, 100.0, c.MidRankPct(99), 1e-9, "超过全部观测值 → 满分位")
	require.InDelta(t, 0.0, c.MidRankPct(-1), 1e-9, "低于全部观测值 → 零分位")
	require.InDelta(t, 0.0, BuildCDF(DimAcclaim, nil).MidRankPct(1), 1e-9, "空 CDF 不应 panic")
}

// ---------- 组合与准入 ----------

func TestCombineCoverageRenormalizes(t *testing.T) {
	weights := map[Dim]float64{DimAcclaim: 0.3, DimCommunity: 0.25, DimTrust: 0.25, DimDepth: 0.1, DimReadability: 0.1}
	dims := map[Dim]DimScore{
		DimAcclaim:     {Score: 90, State: StateMeasured},
		DimCommunity:   {Score: 60, State: StateMeasured},
		DimTrust:       {Score: 80, State: StateMeasured},
		DimDepth:       {Score: 0, State: StateUnknown}, // 缺 D
		DimReadability: {Score: 70, State: StateMeasured},
	}
	cr := Combine(dims, weights, nil, Needs{})
	require.InDelta(t, 0.9, cr.Coverage, 1e-9)
	want := (0.3*90 + 0.25*60 + 0.25*80 + 0.1*70) / 0.9
	require.InDelta(t, want, cr.TBS, 1e-9)
	require.Equal(t, []Dim{DimDepth}, cr.Missing)
	require.Equal(t, 5, cr.TotalDims, "分母是这份榜用到的维度数,不是 7")
}

func TestCombineMissingOrderFollowsAllDims(t *testing.T) {
	// Missing 的顺序进理由串,必须固定 —— 遍历 weights map 会让文案每次都不同。
	weights := map[Dim]float64{}
	dims := map[Dim]DimScore{}
	for _, d := range AllDims {
		weights[d] = 1.0 / float64(len(AllDims))
		dims[d] = DimScore{State: StateUnknown}
	}
	for i := 0; i < 20; i++ {
		require.Equal(t, AllDims, Combine(dims, weights, nil, Needs{}).Missing)
	}
}

func TestNeedsGatesUnweightedDim(t *testing.T) {
	dims := map[Dim]DimScore{
		DimAcclaim:   {Score: 90, State: StateMeasured},
		DimFreshness: {Score: 0, State: StateUnknown},
	}
	require.True(t, NeedsSatisfied(dims, Needs{DimAcclaim: StateMeasured}))
	// F 不在 weights 里,但 needs 声明了它 —— 必须照样挡住。这一条之前是空操作。
	require.False(t, NeedsSatisfied(dims, Needs{DimFreshness: StateMeasured}))
	require.False(t, NeedsSatisfied(dims, Needs{DimTrust: StateShrunk}), "没有该维记录也算不满足")
}

func TestGradeIsBadgeOnly(t *testing.T) {
	dims := map[Dim]DimScore{}
	for _, d := range AllDims {
		dims[d] = DimScore{State: StateMeasured}
	}
	dims[DimFreshness] = DimScore{State: StateUnknown}
	require.Equal(t, "D", Grade(dims))
	dims[DimFreshness] = DimScore{State: StateMeasured}
	require.Equal(t, "A", Grade(dims))
	dims[DimCommunity] = DimScore{State: StateShrunk}
	require.Equal(t, "B", Grade(dims))
}

// ---------- 可复现性(NFR-10)----------

func TestComputeIsBitwiseReproducible(t *testing.T) {
	// 同一份语料重算多次,榜单成员、位次、TBS、理由串、语料指纹必须逐位一致。
	// 之前 selectList 遍历 map 且并列无打破键,连续两次 score 会给出不同的榜。
	db := openTestDB(t)
	presets := loadPresets(t)
	eng := NewEngine(db, "1.0", testNow)

	render := func() string {
		c, err := eng.Compute(presets)
		require.NoError(t, err)
		var b strings.Builder
		fmt.Fprintf(&b, "corpus=%s facts=%s\n", c.CorpusID, c.FactsHash)
		for _, p := range presets {
			for _, en := range c.Lists[p.ID] {
				fmt.Fprintf(&b, "%s|%d|%s|%.12f|%.12f|%s\n",
					p.ID, en.Rank, en.WorkID, en.TBS, en.Coverage, en.Reason)
			}
		}
		for _, id := range c.WorkIDs {
			for _, d := range AllDims {
				ds := c.Dims[id][d]
				fmt.Fprintf(&b, "%s|%s|%.12f|%s\n", id, d, ds.Score, ds.State)
			}
		}
		return b.String()
	}

	first := render()
	require.Contains(t, first, "timeless|1|", "前提:榜单不该是空的")
	for i := 2; i <= 6; i++ {
		require.Equal(t, first, render(), "第 %d 次重算与第 1 次不一致 —— NFR-10 要求逐位可复现", i)
	}
}

func TestSelectionTieBreakIsStableAcrossInputOrder(t *testing.T) {
	// 全部同分:无论候选以什么顺序传进来,选出的都必须是同一批、同一序。
	mk := func(ids []string) []selection.Candidate {
		out := make([]selection.Candidate, 0, len(ids))
		for _, id := range ids {
			out = append(out, selection.Candidate{WorkID: id, Topic: "t" + id, TBS: 42, Coverage: 1})
		}
		return out
	}
	ids := []string{"e", "a", "d", "b", "c"}
	cfg := selection.Config{Size: 3, MaxPerTopic: 1, MaxPerAuthor: 5, MinCoverage: 0}
	want := selection.Select(mk(ids), cfg)

	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	reversed := append([]string(nil), sorted...)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	for _, order := range [][]string{sorted, reversed} {
		require.Equal(t, want, selection.Select(mk(order), cfg))
	}
	require.Equal(t, "a", want[0].WorkID, "并列时按 work_id 升序打破")
}

// ---------- 端到端 ----------

func TestEngineEndToEnd(t *testing.T) {
	db := openTestDB(t)
	presets := loadPresets(t)
	res, err := NewEngine(db, "1.0", testNow).Run(presets)
	require.NoError(t, err)
	require.NotEmpty(t, res.RunID)
	require.NotEmpty(t, res.CorpusID, "语料指纹必须落到 run 上(review M3)")
	require.NotEmpty(t, res.FactsHash)
	require.GreaterOrEqual(t, len(res.Works), 40)

	for _, p := range presets {
		entries := res.Lists[p.ID]
		require.NotEmpty(t, entries, "榜 %s 为空", p.ID)
		require.LessOrEqual(t, len(entries), p.Select.Size, "榜 %s 超过 size 上限", p.ID)

		needs := needsByDim(p)
		seen := map[string]bool{}
		for _, en := range entries {
			require.False(t, seen[en.WorkID], "榜 %s 出现重复 work %s", p.ID, en.WorkID)
			seen[en.WorkID] = true
			dims := res.Dims[en.WorkID]
			// 准入的全部条件:needs + min_coverage。字母徽章不参与。
			require.True(t, NeedsSatisfied(dims, needs), "榜 %s 的 %s 未满足 needs", p.ID, en.WorkID)
			require.GreaterOrEqual(t, en.Coverage+1e-9, p.Select.MinCoverage,
				"榜 %s 的 %s coverage 低于 min_coverage", p.ID, en.WorkID)
			require.NotEmpty(t, en.Reason, "榜 %s 的 %s 缺理由串", p.ID, en.WorkID)
		}
	}

	// 阅读队列:只有未读,且排除弃读书架。
	for _, en := range res.Lists["to-read-next"] {
		w := res.Works[en.WorkID]
		require.Equal(t, "unread", w.ReadStatus)
		require.NotContains(t, w.Shelves, "弃读")
	}
	// 近一年新书:出版日期可信,且落在滚动窗口内。
	for _, en := range res.Lists["fresh-releases"] {
		w := res.Works[en.WorkID]
		require.NotNil(t, w.TrustedPubdate)
		require.True(t, w.LatestPubdate.After(testNow.AddDate(-1, 0, 0)),
			"%s 的最新版次不在近 12 个月内", en.WorkID)
	}
}

func TestUntrustedPubdateStillEntersTimeless(t *testing.T) {
	// review B1 的回归测试。「经典长青」明确不使用时效维度,所以一本 pubdate 来自
	// mtime 兜底的 O'Reilly 经典必须进得去。用 grade 字母当全局闸门时,这类书
	// (实测占全库 23%)会从整站消失。
	db := openTestDB(t)
	res, err := NewEngine(db, "1.0", testNow).Run(loadPresets(t))
	require.NoError(t, err)

	const untrusted = "richardson/restful web apis"
	require.Equal(t, StateUnknown, res.Dims[untrusted][DimFreshness].State, "前提:该书 F 维应为 unknown")
	require.Equal(t, "D", res.Grade[untrusted], "前提:该书的字母徽章应为 D")

	var found bool
	for _, en := range res.Lists["timeless"] {
		if en.WorkID == untrusted {
			found = true
			require.InDelta(t, 1.0, en.Coverage, 1e-9, "timeless 不加权 F → coverage 不该被扣")
		}
	}
	require.True(t, found, "F 不可信但 A/C/T 齐备的书必须能进 timeless")
}

func TestRunGCKeepsOnlyRecentRuns(t *testing.T) {
	db := openTestDB(t)
	presets := loadPresets(t)
	var runIDs []string
	for i := 0; i < 8; i++ {
		eng := NewEngine(db, "1.0", testNow.Add(time.Duration(i)*time.Hour))
		eng.KeepRuns = 3
		res, err := eng.Run(presets)
		require.NoError(t, err)
		runIDs = append(runIDs, res.RunID)
	}

	count := func(q string, args ...any) int {
		var n int
		require.NoError(t, db.SQL().QueryRow(q, args...).Scan(&n))
		return n
	}
	require.Equal(t, 3, count(`SELECT COUNT(*) FROM runs`))
	require.Equal(t, 3, count(`SELECT COUNT(DISTINCT run_id) FROM dim_scores`), "维度分要跟着 run 一起回收")
	require.Equal(t, 3, count(`SELECT COUNT(DISTINCT run_id) FROM lists`))
	require.Equal(t, 3, count(`SELECT COUNT(DISTINCT run_id) FROM norm_cdf`))
	require.Zero(t, count(`SELECT COUNT(*) FROM runs WHERE run_id=?`, runIDs[0]), "最早的 run 应已回收")

	var published string
	require.NoError(t, db.SQL().QueryRow(`SELECT run_id FROM published_run WHERE id=1`).Scan(&published))
	require.Equal(t, runIDs[len(runIDs)-1], published, "最新 run 必须仍是已发布的那个")
	require.Equal(t, 1, count(`SELECT COUNT(*) FROM runs WHERE run_id=?`, published))
}

func TestRunIDsAreDistinctWithinOneSecond(t *testing.T) {
	// 秒级 run_id 会让同一秒内的两次 score 写进同一个 run,两份榜单互相覆盖。
	db := openTestDB(t)
	presets := loadPresets(t)
	base := testNow
	a, err := NewEngine(db, "1.0", base).Run(presets)
	require.NoError(t, err)
	b, err := NewEngine(db, "1.0", base.Add(time.Millisecond)).Run(presets)
	require.NoError(t, err)
	require.NotEqual(t, a.RunID, b.RunID)
}

// ---------- 与 preset 包的契约 ----------

func TestPresetDimNamesMatchAllDims(t *testing.T) {
	// preset 不能 import score(反向依赖已存在),它自己带一份合法维度名清单。
	// 这条测试就是那份拷贝的同步锁。
	want := make([]string, 0, len(AllDims))
	for _, d := range AllDims {
		want = append(want, string(d))
	}
	got := preset.ValidDims()
	sort.Strings(want)
	sort.Strings(got)
	require.Equal(t, want, got, "preset.validDims 与 score.AllDims 已漂移")
}

// ---------- 助手 ----------

func loadPresets(t *testing.T) []preset.Preset {
	t.Helper()
	presets, err := preset.Load()
	require.NoError(t, err)
	return presets
}

func openTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/test.db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = corpus.Seed(db)
	require.NoError(t, err)
	return db
}
