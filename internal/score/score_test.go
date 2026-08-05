package score

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/meirongdev/readlist/internal/corpus"
	"github.com/meirongdev/readlist/internal/preset"
	"github.com/meirongdev/readlist/internal/selection"
	"github.com/meirongdev/readlist/internal/store"
)

func TestAcclaimBayesianShrinksLowCount(t *testing.T) {
	// 1 人打 5 星 → 被拉回先验,而不是登顶。
	w := &WorkInput{Ratings: []Rating{{Source: "google_books", Rating: 5, Count: 1}}}
	r := w.acclaim(DimParams{AcclaimPrior: 4.1, MinCount: 20})
	require.Equal(t, StateMeasured, r.State)
	require.Less(t, r.Raw, 4.8, "单人高分应被贝叶斯收缩")
	require.Greater(t, r.Raw, 4.1, "有实测证据时不应低于先验太多")
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
	r := w.freshness(time.Now())
	require.Equal(t, StateUnknown, r.State)
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
}

func TestGradeDWhenFreshnessUnknown(t *testing.T) {
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

func TestEngineEndToEnd(t *testing.T) {
	db := openTestDB(t)
	presets, err := preset.Load()
	require.NoError(t, err)

	eng := NewEngine(db, "1.0", time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC))
	res, err := eng.Run(presets)
	require.NoError(t, err)
	require.NotEmpty(t, res.RunID)
	require.GreaterOrEqual(t, len(res.Works), 40)

	// 每个公开榜都有内容且不含 D 级。
	for _, p := range presets {
		if !p.Public() {
			continue
		}
		require.NotEmpty(t, res.Lists[p.ID], "公开榜 %s 为空", p.ID)
		for _, en := range res.Lists[p.ID] {
			require.NotEqual(t, "D", res.Grade[en.WorkID], "%s 出现 D 级书", p.ID)
		}
	}
	// 阅读队列:只有未读。
	next := res.Lists["to-read-next"]
	for _, en := range next {
		w := res.Works[en.WorkID]
		require.Equal(t, "unread", w.ReadStatus)
		for _, sh := range w.Shelves {
			require.NotEqual(t, "弃读", sh)
		}
	}
	// 2026 新书榜:出版日期可信且年份=2026。
	for _, en := range res.Lists["new-2026"] {
		w := res.Works[en.WorkID]
		require.NotNil(t, w.TrustedPubdate)
		require.Equal(t, 2026, w.TrustedPubdate.Year())
	}
}

func TestSelectionDiversity(t *testing.T) {
	cands := []selection.Candidate{}
	for i := 0; i < 10; i++ {
		cands = append(cands, selection.Candidate{WorkID: string(rune('a' + i)), Topic: "t", FirstAuthor: "same", TBS: float64(100 - i), Coverage: 1})
	}
	out := selection.Select(cands, selection.Config{Size: 20, MaxPerTopic: 2, MaxPerAuthor: 1, MinCoverage: 0})
	require.Len(t, out, 1, "同一作者同一主题最多 1 条")
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
