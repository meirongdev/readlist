package facts_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/meirongdev/readlist/internal/calibre"
	"github.com/meirongdev/readlist/internal/corpus"
	"github.com/meirongdev/readlist/internal/facts"
	"github.com/meirongdev/readlist/internal/preset"
	"github.com/meirongdev/readlist/internal/score"
	"github.com/meirongdev/readlist/internal/store"
)

var now = time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)

// ---------- 假外部源 ----------
//
// 响应形状取自 2026-08-05 对真实 API 的实测:
//   HN Algolia   /search        → {hits:[{objectID,title,created_at,points}], nbHits}
//   OpenLibrary  /isbn/X.json   → {works:[{key:"/works/OL…W"}], publish_date:"Apr 02, 2017"}
//   OpenLibrary  /works/…/ratings.json → {summary:{average,count}}
//   Google Books /volumes/{id} 与 /volumes?q=isbn:X → {items:[{id,volumeInfo{…}}]}

type fakeAPIs struct {
	google, openlib, hn *httptest.Server
	mu                  sync.Mutex
	hits                map[string]int
	googleStatus        int // 非 0 时 Google 一律返回该状态码
}

func (f *fakeAPIs) count(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[key]
}

func (f *fakeAPIs) record(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hits[key]++
}

func (f *fakeAPIs) close() {
	f.google.Close()
	f.openlib.Close()
	f.hn.Close()
}

func googleVolume(id, title, published string, rating float64, count int) map[string]any {
	return map[string]any{
		"id": id,
		"volumeInfo": map[string]any{
			"title": title, "publisher": "O'Reilly Media", "publishedDate": published,
			"averageRating": rating, "ratingsCount": count, "pageCount": 600,
			"categories": []string{"Computers"},
		},
	}
}

func newFakeAPIs(t *testing.T) *fakeAPIs {
	t.Helper()
	f := &fakeAPIs{hits: map[string]int{}}

	f.google = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.record("google")
		if f.googleStatus != 0 {
			w.WriteHeader(f.googleStatus)
			_, _ = w.Write([]byte(`{"error":{"code":429,"message":"Quota exceeded"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/volumes/GV-DDIA"):
			f.record("google:volume")
			_ = json.NewEncoder(w).Encode(googleVolume("GV-DDIA",
				"Designing Data-Intensive Applications", "2017-03-16", 4.8, 620))
		case strings.HasSuffix(r.URL.Path, "/volumes"):
			q := r.URL.Query().Get("q")
			f.record("google:isbn")
			// 两个版次(ISBN 不同)指向同一个 volume —— 用来验证评分不会被计两遍。
			if q == "isbn:9781492077213" || q == "isbn:9781098139292" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"totalItems": 1,
					"items":      []any{googleVolume("GV-LEARNGO", "Learning Go", "2021-03", 4.5, 95)},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"totalItems": 0})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	f.openlib = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.record("openlib")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/isbn/9781449373320.json":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"title": "Designing Data-Intensive Applications",
				// 实测就是这种自由文本日期。
				"publish_date": "Apr 02, 2017",
				"publishers":   []string{"O'Reilly Media"},
				"works":        []any{map[string]any{"key": "/works/OL19293745W"}},
			})
		case r.URL.Path == "/works/OL19293745W/ratings.json":
			f.record("openlib:ratings")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"summary": map[string]any{"average": 4.7, "count": 210},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	f.hn = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.record("hn")
		w.Header().Set("Content-Type", "application/json")
		query := r.URL.Query().Get("query")
		hits := []any{}
		add := func(id, title string, ago int) {
			hits = append(hits, map[string]any{
				"objectID": id, "title": title, "points": 500,
				"created_at": now.AddDate(-ago, 0, 0).Format(time.RFC3339),
			})
		}
		switch {
		case strings.Contains(query, "Designing Data-Intensive Applications"):
			add("15428526", "Designing Data-Intensive Applications", 9)
			add("22001122", "Review: Designing Data-Intensive Applications (2nd ed)", 2)
			// 这一条标题里没有书名 → 必须被"宁少不多"的规则拒掉。
			add("99999999", "Ask HN: what database book should I read?", 1)
		case strings.Contains(query, "Learning Go"):
			add("30001111", "Learning Go by Jon Bodner", 3)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"nbHits": len(hits), "hits": hits})
	}))
	t.Cleanup(f.close)
	return f
}

func (f *fakeAPIs) cfg(budget int) facts.Config {
	return facts.Config{
		GoogleBase: f.google.URL, OpenLibraryBase: f.openlib.URL, HNBase: f.hn.URL,
		Budget: budget, Sleep: 0, Now: now,
	}
}

// ---------- 语料 ----------

func corpusSnapshot() *calibre.Snapshot {
	return &calibre.Snapshot{Books: []calibre.Book{
		{BookID: 1, Title: "Designing Data-Intensive Applications",
			Authors: []string{"Martin Kleppmann"}, Publisher: "O'Reilly Media",
			Formats: []string{"EPUB"}, Language: "eng", ISBN13: "9781449373320",
			GoogleID: "GV-DDIA", HasCover: true, HasComments: true,
			Pubdate: "2017-01-01", PubdateSource: calibre.SourceCalibre},
		// 同一 work 的两个版次,ISBN 不同但外部 volume 相同。
		{BookID: 2, Title: "Learning Go", Authors: []string{"Jon Bodner"},
			Publisher: "O'Reilly Media", Formats: []string{"EPUB"}, Language: "eng",
			ISBN13: "9781492077213", HasCover: true, HasComments: true,
			Pubdate: "2021-03-16", PubdateSource: calibre.SourceCalibre},
		{BookID: 3, Title: "Learning Go, Second Edition", Authors: []string{"Jon Bodner"},
			Publisher: "O'Reilly Media", Formats: []string{"EPUB"}, Language: "eng",
			ISBN13: "9781098139292", HasCover: true, HasComments: true,
			Pubdate: "2024-03-19", PubdateSource: calibre.SourceCalibre},
		// 既无 ISBN 也无 google id → 跳过(实测约 1,000–1,300 本是这种)。
		{BookID: 4, Title: "Some Unidentifiable Book", Authors: []string{"Nobody"},
			Publisher: "Packt", Formats: []string{"PDF"}, PubdateSource: calibre.SourceUnknown},
		// 单词标题 → HN 不查(否则整片 HN 首页都会被认成提及)。
		{BookID: 5, Title: "Go", Authors: []string{"Some Author"},
			Publisher: "Manning", Formats: []string{"PDF"}, ISBN13: "9780000000019",
			PubdateSource: calibre.SourceUnknown},
	}}
}

func newDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/facts.db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = corpus.Import(db, corpusSnapshot(), now)
	require.NoError(t, err)
	return db
}

func queryOne[T any](t *testing.T, db *store.DB, q string, args ...any) T {
	t.Helper()
	var v T
	require.NoError(t, db.SQL().QueryRow(q, args...).Scan(&v))
	return v
}

// ---------- 测试 ----------

func TestIngestWritesRatingsInEngineShape(t *testing.T) {
	f := newFakeAPIs(t)
	db := newDB(t)
	st, err := facts.Ingest(db, f.cfg(100))
	require.NoError(t, err)
	require.Positive(t, st.GoogleFound)
	require.Positive(t, st.OpenLibraryFound)

	// 评分行的 payload 必须是评分引擎能读的形状(rating/count 在顶层),
	// 同时原样留档外部响应(FR-11)。
	payload := queryOne[string](t, db,
		`SELECT payload FROM evidence WHERE source='google_books' AND source_id='GV-DDIA'`)
	var body struct {
		Rating float64        `json:"rating"`
		Count  int            `json:"count"`
		Raw    map[string]any `json:"raw"`
	}
	require.NoError(t, json.Unmarshal([]byte(payload), &body))
	require.Equal(t, 4.8, body.Rating)
	require.Equal(t, 620, body.Count)
	require.NotEmpty(t, body.Raw, "外部响应要原样留档")

	ol := queryOne[string](t, db,
		`SELECT payload FROM evidence WHERE source='openlibrary' AND source_id='OL19293745W'`)
	require.NoError(t, json.Unmarshal([]byte(ol), &body))
	require.Equal(t, 210, body.Count)
}

func TestIngestDedupesSameVolumeAcrossEditions(t *testing.T) {
	// 两个版次(ISBN 不同)指向同一个 Google volume。若按查询串存,同一份 95 人评分
	// 会被计两遍 —— 那是 review M6 失真的镜像版本。
	f := newFakeAPIs(t)
	db := newDB(t)
	_, err := facts.Ingest(db, f.cfg(100))
	require.NoError(t, err)

	n := queryOne[int](t, db,
		`SELECT COUNT(*) FROM evidence WHERE source='google_books' AND source_id='GV-LEARNGO'`)
	require.Equal(t, 1, n, "同一 volume 只应有一行评分证据")

	// 而两个版次各自的查询标记都在(所以不会重复烧配额)。
	markers := queryOne[int](t, db,
		`SELECT COUNT(*) FROM evidence WHERE source='google_query'`)
	require.Equal(t, 4, markers, "4 个有标识符的版次各留一个查询标记")
}

func TestIngestWritesTrustedPubdate(t *testing.T) {
	f := newFakeAPIs(t)
	db := newDB(t)
	st, err := facts.Ingest(db, f.cfg(100))
	require.NoError(t, err)
	require.Positive(t, st.PubdatesWritten)

	// review M2:同一个 Google 响应里就带 publishedDate → 写成带来源的可信日期,
	// 完全不需要先去修 calibre 的 metadata.db。
	src := queryOne[string](t, db, `SELECT pubdate_source FROM editions WHERE book_id=1`)
	require.Contains(t, []string{"google", "openlibrary"}, src)
	date := queryOne[string](t, db, `SELECT pubdate FROM editions WHERE book_id=1`)
	require.Equal(t, "2017-03-16", date, "Google 的完整日期应优先于 OL 的自由文本日期")

	// 只有年月的日期补成当月 1 号(半衰期以年为尺度,月份精度不影响 F)。
	require.Equal(t, "2021-03-01", queryOne[string](t, db, `SELECT pubdate FROM editions WHERE book_id=2`))
}

func TestIngestStoresOpenLibraryWorkID(t *testing.T) {
	f := newFakeAPIs(t)
	db := newDB(t)
	st, err := facts.Ingest(db, f.cfg(100))
	require.NoError(t, err)
	require.Positive(t, st.OLWorkIDs)
	// OL work id 是聚类键的最高优先级(system-design §4)。
	olID := queryOne[string](t, db,
		`SELECT COALESCE(ol_work_id,'') FROM works w JOIN editions e USING(work_id) WHERE e.book_id=1`)
	require.Equal(t, "OL19293745W", olID)
}

func TestIngestHNMatchingIsConservative(t *testing.T) {
	f := newFakeAPIs(t)
	db := newDB(t)
	st, err := facts.Ingest(db, f.cfg(100))
	require.NoError(t, err)

	// 标题里不含书名的那条("Ask HN: what database book…")必须被拒。
	ids := []string{}
	rows, err := db.SQL().Query(`SELECT object_id FROM mentions ORDER BY object_id`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.Contains(t, ids, "15428526")
	require.Contains(t, ids, "22001122", "书名作为子串出现的 story 应被接受")
	require.NotContains(t, ids, "99999999", "标题不含书名的 story 必须被拒(R-3 宁少不多)")

	// ≤2 词的标题一律不查(除非进白名单):这里是「Go」和「Learning Go」两本。
	// 代价是「Clean Code」「Fluent Python」这类知名书也拿不到声量维,
	// 但方向是刻意的 —— 宁少不多,漏认可以靠白名单补,误认会污染整个 C 维。
	require.Equal(t, 2, st.SkippedShortTitle)
	require.Positive(t, st.MentionsFound, "长标题的书仍应拿到提及")
}

func TestIngestShortTitleQueriedWhenWhitelisted(t *testing.T) {
	f := newFakeAPIs(t)
	db := newDB(t)
	rows, err := db.SQL().Query(`SELECT work_id, canonical_title FROM works`)
	require.NoError(t, err)
	var short []string
	for rows.Next() {
		var id, title string
		require.NoError(t, rows.Scan(&id, &title))
		if len(strings.Fields(title)) <= 2 {
			short = append(short, id)
		}
	}
	rows.Close()
	require.NotEmpty(t, short)
	for _, id := range short {
		_, err := db.SQL().Exec(
			`INSERT INTO title_whitelist (work_id, reason) VALUES (?, '人工确认')`, id)
		require.NoError(t, err)
	}

	st, err := facts.Ingest(db, f.cfg(100))
	require.NoError(t, err)
	require.Zero(t, st.SkippedShortTitle, "白名单里的短标题应该被查")
	require.Positive(t, st.MentionsFound)
}

func TestIngestCachesAndRespectsTTL(t *testing.T) {
	f := newFakeAPIs(t)
	db := newDB(t)
	first, err := facts.Ingest(db, f.cfg(100))
	require.NoError(t, err)
	require.Positive(t, first.Requests)

	// 第二次立刻重跑:全部命中缓存,零外部请求(FR-11「重算不打外部 API」)。
	before := f.count("google") + f.count("openlib") + f.count("hn")
	second, err := facts.Ingest(db, f.cfg(100))
	require.NoError(t, err)
	require.Zero(t, second.Requests, "缓存还新鲜时不该发任何请求")
	require.Positive(t, second.CacheHits)
	require.Equal(t, before, f.count("google")+f.count("openlib")+f.count("hn"))

	// 评分类 TTL 是 30 天:31 天后该重取。
	later := f.cfg(100)
	later.Now = now.AddDate(0, 0, 31)
	third, err := facts.Ingest(db, later)
	require.NoError(t, err)
	require.Positive(t, third.Requests, "TTL 过期后应重新取评分")
}

func TestIngestStopsCleanlyWhenBudgetExhausted(t *testing.T) {
	f := newFakeAPIs(t)
	db := newDB(t)
	st, err := facts.Ingest(db, f.cfg(2)) // 只给 2 个请求
	require.NoError(t, err, "预算用完不是错误 —— 下次接着跑")
	require.True(t, st.BudgetExhausted)
	require.LessOrEqual(t, st.Requests, 2)

	// 已经拿到的必须落库(部分成功优于全盘失败)。
	require.Positive(t, queryOne[int](t, db, `SELECT COUNT(*) FROM evidence`))

	// 续跑:剩下的接着取,不重复已缓存的。
	st2, err := facts.Ingest(db, f.cfg(100))
	require.NoError(t, err)
	require.Positive(t, st2.Requests)
	require.False(t, st2.BudgetExhausted)
}

func TestIngestSurvivesGoogle429(t *testing.T) {
	// 实测:Google Books 的匿名配额是按共享项目计的,很容易一上来就 429。
	// 那时必须熔断该源、继续用其他源,而不是把整轮摄入弄失败。
	f := newFakeAPIs(t)
	f.googleStatus = http.StatusTooManyRequests
	db := newDB(t)

	st, err := facts.Ingest(db, f.cfg(100))
	require.NoError(t, err)
	require.Positive(t, st.Throttled)
	require.Zero(t, st.GoogleFound)
	// 熔断后不再打 Google:4 个有标识符的版次只应付出 1 次 429。
	require.Equal(t, 1, f.count("google"), "429 之后不该继续空转打同一个源")
	// OpenLibrary 与 HN 照常工作。
	require.Positive(t, st.OpenLibraryFound)
	require.Positive(t, st.MentionsFound)
}

func TestIngestSkipsEditionsWithoutIdentifiers(t *testing.T) {
	f := newFakeAPIs(t)
	db := newDB(t)
	st, err := facts.Ingest(db, f.cfg(100))
	require.NoError(t, err)
	require.Equal(t, 5, st.EditionsSeen)
	require.Equal(t, 1, st.SkippedNoID, "无 ISBN 也无 google id 的版次不做标题猜测")
}

// TestIngestUnlocksPublicLists 是这条管道的收益证明:
// snapshot 之后公开榜是空的(诚实),ingest 之后才第一次有内容。
func TestIngestUnlocksPublicLists(t *testing.T) {
	f := newFakeAPIs(t)
	db := newDB(t)
	presets, err := preset.Load()
	require.NoError(t, err)

	before, err := score.NewEngine(db, "1.0", now).Run(presets)
	require.NoError(t, err)
	require.Empty(t, before.Lists["timeless"], "只有 calibre 数据时公开旗舰榜应为空")
	for id, dims := range before.Dims {
		require.Equal(t, score.StateUnknown, dims[score.DimFreshness].State, id)
	}

	_, err = facts.Ingest(db, f.cfg(100))
	require.NoError(t, err)

	after, err := score.NewEngine(db, "1.0", now).Run(presets)
	require.NoError(t, err)
	require.NotEmpty(t, after.Lists["timeless"], "拿到外部评分与提及后旗舰榜必须有内容")

	// 时效维度第一次有判别力 —— 而这一步没有动过 calibre 的库。
	ddia := ""
	for id := range after.Works {
		if strings.Contains(id, "data intensive") {
			ddia = id
		}
	}
	require.NotEmpty(t, ddia)
	require.Equal(t, score.StateMeasured, after.Dims[ddia][score.DimFreshness].State)
	require.Equal(t, score.StateMeasured, after.Dims[ddia][score.DimAcclaim].State)
	require.Equal(t, score.StateMeasured, after.Dims[ddia][score.DimCommunity].State)

	// 评分人数在 work 级汇总(Google 620 + OpenLibrary 210)。
	raw := after.Dims[ddia][score.DimAcclaim].Raw
	require.Greater(t, raw, 4.0)
	require.LessOrEqual(t, raw, 5.0)

	// 榜单条目必须带确定性理由串,且理由要引用真实事实(出版社/提及/半衰期)。
	reason := after.Lists["timeless"][0].Reason
	require.NotEmpty(t, reason)
	require.Contains(t, reason, "HN 提及", "理由串应引用刚摄入的提及证据")
}
