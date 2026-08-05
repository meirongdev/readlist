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
	// 近一年新书:窗口判定只认**可信**日期,所以断言也必须打在 TrustedPubdate 上
	// —— 打在 LatestPubdate 上会漏掉「污染日期把老书顶进来」这整类失效(review A2)。
	for _, en := range res.Lists["fresh-releases"] {
		w := res.Works[en.WorkID]
		require.NotNil(t, w.TrustedPubdate)
		require.True(t, w.TrustedPubdate.After(testNow.AddDate(-1, 0, 0)),
			"%s 的可信出版日期不在近 12 个月内", en.WorkID)
		require.True(t, PubdateUsableForAge(w.PubdateSource),
			"%s 的日期来源 %q 不该被信任", en.WorkID, w.PubdateSource)
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
			// 出版日期不可信 → 年龄下限只能放行(见 filterPass 的不对称说明)。
			// 放行必须说出来:一本出版日期不明的书不该以「经典」的名义静默上榜。
			require.Contains(t, en.Reason, "年龄未核实",
				"年龄下限对未知日期放行时,理由串必须披露")
		}
	}
	require.True(t, found, "F 不可信但 A/C/T 齐备的书必须能进 timeless")
}

func TestPollutedPubdateCannotMakeOldBookLookNew(t *testing.T) {
	// 生产形状:一本 2015 年的书有两个版次 —— 一个被 ingest 写上了可信的 google 日期,
	// 另一个没有标识符,pubdate 是 snapshot 用文件 mtime 兜底出来的「今年」。
	// mtime 兜底值按构造就落在最近,所以它绝不能参与「够不够新」的判定。
	//
	// 演示语料里那本 mtime 书的日期恰好是 2013,所以「污染日期被当真」这件事在测试里
	// 看不出来 —— 而生产语料里污染值是 2026(477 本)。测试形状必须对齐生产(review A2)。
	db := openTestDB(t)
	const wid = "oldie/an old platform book"
	mustExec(t, db, `INSERT INTO works
		(work_id, canonical_title, first_author, primary_topic, level, half_life_years, half_life_source)
		VALUES (?,?,?,?,?,?,?)`,
		wid, "An Old Platform Book", "Olga Oldie", "平台/生态", "intermediate", 5.0, "rules-topic-class")
	edition := func(bookID int, isbn, pubdate, source string) {
		mustExec(t, db, `INSERT INTO editions
			(book_id, work_id, title, isbn13, publisher_raw, publisher_norm, format, language,
			 has_comments, has_cover, pubdate, pubdate_source)
			VALUES (?,?,?,?,?,?,?,?,1,1,?,?)`,
			bookID, wid, "An Old Platform Book", isbn, "O'Reilly Media", "O'Reilly Media",
			"EPUB", "eng", pubdate, source)
	}
	edition(9001, "9780000000101", "2015-06-01", "google")         // 真实出版日期
	edition(9002, "9780000000102", "2026-07-11", "mtime-fallback") // 文件 mtime 兜底

	res, err := NewEngine(db, "1.0", testNow).Run(loadPresets(t))
	require.NoError(t, err)

	w := res.Works[wid]
	require.NotNil(t, w)
	require.NotNil(t, w.TrustedPubdate)
	require.Equal(t, "2015-06-01", w.TrustedPubdate.Format("2006-01-02"))
	require.NotNil(t, w.LatestPubdate)
	require.Equal(t, "2015-06-01", w.LatestPubdate.Format("2006-01-02"),
		"被污染的来源一条都不该进日期聚合")

	require.NotContains(t, workIDsOf(res.Lists["fresh-releases"]), wid,
		"2015 年的书不能因为某个版次的 mtime 兜底日期进「近一年新书」")
}

func TestEvidenceSurvivesWorkKeyChange(t *testing.T) {
	// work_id 是「姓氏 + 规范标题」的派生键:在 calibre 里修一个书名 typo 就会让它变。
	// 若证据按写入时的 work_id 绑定,这本书的口碑维会静默消失 —— 而查询标记还新鲜,
	// 最长 180 天不会被重抓。偏偏「补元数据」正是文档反复鼓励库主人做的事(review A4)。
	db := openTestDB(t)
	const oldID = "kleppmann/designing data intensive applications"
	const newID = "kleppmann/designing data intensive applications revised"

	base, err := NewEngine(db, "1.0", testNow).Run(loadPresets(t))
	require.NoError(t, err)
	require.Equal(t, StateMeasured, base.Dims[oldID][DimAcclaim].State, "前提:改名前口碑维是实测的")

	// 模拟改名:新建 work(沿用同一个 OL work id)、把版次挪过去、删掉旧 work。
	// evidence 那几行**保持指向旧 work_id** —— 这正是要验证的场景。
	mustExec(t, db, `INSERT INTO works SELECT ?, canonical_title || ' Revised', first_author,
		ol_work_id, primary_topic, level, half_life_years, half_life_source
		FROM works WHERE work_id=?`, newID, oldID)
	mustExec(t, db, `UPDATE editions SET work_id=? WHERE work_id=?`, newID, oldID)
	mustExec(t, db, `DELETE FROM works WHERE work_id=?`, oldID)

	var stale int
	require.NoError(t, db.SQL().QueryRow(
		`SELECT COUNT(*) FROM evidence WHERE work_id=?`, oldID).Scan(&stale))
	require.Positive(t, stale, "前提:evidence 仍指向旧 work_id —— 验的是读取时解析,不是数据迁移")

	res, err := NewEngine(db, "1.0", testNow).Run(loadPresets(t))
	require.NoError(t, err)
	require.Equal(t, StateMeasured, res.Dims[newID][DimAcclaim].State,
		"改名后证据必须仍解析得到,否则「补元数据」这个动作本身会打掉证据")
	require.InDelta(t, base.Dims[oldID][DimAcclaim].Raw, res.Dims[newID][DimAcclaim].Raw, 1e-9,
		"Google(按 ISBN 键)与 OpenLibrary(按 work id 键)两条解析路径都要通")
}

// ---------- 人工干预(overrides / mention_overrides)----------

func TestMentionVetoRemovesRejectedObjectID(t *testing.T) {
	// R-3 的兜底:通用短标题会命中无关讨论,mentions 保留 objectID 正是为了能逐条否决,
	// 而在此之前没有任何生效路径 —— 唯一的处置办法是改代码。
	db := openTestDB(t)
	const wid = "kleppmann/designing data intensive applications"
	base, err := NewEngine(db, "1.0", testNow).Run(loadPresets(t))
	require.NoError(t, err)
	before := len(base.Works[wid].Mentions)
	require.Positive(t, before)

	obj := queryStr(t, db, `SELECT object_id FROM mentions WHERE work_id=? ORDER BY object_id LIMIT 1`, wid)
	mustExec(t, db, `INSERT INTO mention_overrides (work_id, object_id, verdict, reason, at)
		VALUES (?,?, 'reject', '误匹配', '2026-08-05T00:00:00Z')`, wid, obj)

	after, err := NewEngine(db, "1.0", testNow).Run(loadPresets(t))
	require.NoError(t, err)
	require.Equal(t, before-1, len(after.Works[wid].Mentions), "被否决的提及不该计入声量维")
	require.NotEqual(t, base.FactsHash, after.FactsHash, "人工否决属于事实变化,必须进 facts_hash")
}

func TestManualPinBypassesAdmissionAndIsDisclosed(t *testing.T) {
	// system-design §13 要库主人决定「timeless 是否接受一层人工 curation」——
	// 在此之前想选「接受」也没有开关可拨(review B6)。
	db := openTestDB(t)
	const wid = "unknown/the mystery systems book" // 无外部评分 → 达不到 timeless 的 needs
	base, err := NewEngine(db, "1.0", testNow).Run(loadPresets(t))
	require.NoError(t, err)
	require.NotEqual(t, StateMeasured, base.Dims[wid][DimAcclaim].State,
		"前提:这本书本来进不了 timeless")
	require.NotContains(t, workIDsOf(base.Lists["timeless"]), wid)

	mustExec(t, db, `INSERT INTO overrides (work_id, field, value, reason, at)
		VALUES (?, 'pin', 'timeless', '库主人愿意为它的排名辩护', '2026-08-05T00:00:00Z')`, wid)
	res, err := NewEngine(db, "1.0", testNow).Run(loadPresets(t))
	require.NoError(t, err)

	entries := res.Lists["timeless"]
	require.NotEmpty(t, entries)
	require.Equal(t, wid, entries[0].WorkID, "置顶项排在算法结果之前")
	require.Contains(t, entries[0].Reason, "人工置顶", "curation 不该伪装成算法结果")
	// 置顶不该顺手放宽其他书的准入。
	for _, en := range entries[1:] {
		require.GreaterOrEqual(t, en.Coverage+1e-9, 0.7)
	}
}

func TestManualVetoRemovesFromAllLists(t *testing.T) {
	db := openTestDB(t)
	base, err := NewEngine(db, "1.0", testNow).Run(loadPresets(t))
	require.NoError(t, err)
	victim := base.Lists["timeless"][0].WorkID

	mustExec(t, db, `INSERT INTO overrides (work_id, field, value, reason, at)
		VALUES (?, 'veto', '', '不该出现在公开榜上', '2026-08-05T00:00:00Z')`, victim)
	res, err := NewEngine(db, "1.0", testNow).Run(loadPresets(t))
	require.NoError(t, err)
	for listID, entries := range res.Lists {
		require.NotContains(t, workIDsOf(entries), victim,
			"value 留空 = 全站否决,但榜 %s 里仍然出现", listID)
	}
}

func TestManualVetoValueAcceptsCommaSeparatedLists(t *testing.T) {
	// overrides 的主键是 (work_id, field),一个 work 每种操作只有一行 —— 所以 value
	// 必须支持逗号分隔,否则「只否决这两份榜」根本无法表达。
	db := openTestDB(t)
	mustExec(t, db, `INSERT INTO overrides (work_id, field, value, reason, at)
		VALUES ('w/x', 'veto', 'timeless, deep-dive', '这两份榜不合适', '2026-08-05T00:00:00Z')`)

	manual, err := NewEngine(db, "1.0", testNow).loadManualLists()
	require.NoError(t, err)
	require.True(t, manual.Vetoed("w/x", "timeless"))
	require.True(t, manual.Vetoed("w/x", "deep-dive"), "逗号后带空格的榜 id 也要生效")
	require.False(t, manual.Vetoed("w/x", "to-read-next"), "没被点名的榜不受影响")
	require.False(t, manual.Vetoed("other/work", "timeless"))
	require.False(t, manual.Pinned("w/x", "timeless"), "veto 不该被读成 pin")
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

func mustExec(t *testing.T, db *store.DB, query string, args ...any) {
	t.Helper()
	_, err := db.SQL().Exec(query, args...)
	require.NoError(t, err)
}

func queryStr(t *testing.T, db *store.DB, query string, args ...any) string {
	t.Helper()
	var v string
	require.NoError(t, db.SQL().QueryRow(query, args...).Scan(&v))
	return v
}

func workIDsOf(entries []ListEntry) []string {
	out := make([]string, 0, len(entries))
	for _, en := range entries {
		out = append(out, en.WorkID)
	}
	return out
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
