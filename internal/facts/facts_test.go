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
		// 记录实际发出的短语,供断言"查询用的是主标题"。
		f.record("hnq|" + query)
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
		case strings.Contains(query, "The Pragmatic Programmer"):
			add("40001111", "The Pragmatic Programmer at 25", 1)
			// 词形差一个字母(programmers)→ 词边界匹配必须拒掉。
			add("40002222", "The pragmatic programmers guide to hiring", 2)
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

func TestOpenLibraryRatingsRefreshIndependentlyOfISBNMapping(t *testing.T) {
	// OL 是**两跳**:ISBN→work 的映射稳定(180 天),而评分是 30 天 TTL。
	// 之前查询标记一新鲜就直接 return,于是第二跳永远不会发生 —— 评分实际 180 天
	// 才刷一次,而规格写的是 30 天(review A3)。
	f := newFakeAPIs(t)
	db := newDB(t)
	_, err := facts.Ingest(db, f.cfg(100))
	require.NoError(t, err)
	require.Positive(t, f.count("openlib:ratings"), "前提:首轮取到过评分")
	isbnHops := f.count("openlib") - f.count("openlib:ratings")
	require.Positive(t, isbnHops)

	later := f.cfg(100)
	later.Now = now.AddDate(0, 0, 31)
	_, err = facts.Ingest(db, later)
	require.NoError(t, err)

	require.Greater(t, f.count("openlib:ratings"), 1, "评分过了 30 天 TTL,必须重取")
	require.Equal(t, isbnHops, f.count("openlib")-f.count("openlib:ratings"),
		"ISBN→work 的映射是 180 天的,不该被一起重查")
}

func TestGoogleQueryMarkerKeyIsStableAcrossRuns(t *testing.T) {
	// 摄入会把查出来的 volume id 写回 editions(为了让评分行能被读取时解析回 work)。
	// 若查询标记的键优先用 google id,那次写回就会让次夜算出的键变掉 → 标记全部落空
	// → 刚查过的书被整批重查。所以键必须用摄入自己不会改写的标识符(ISBN 优先)。
	f := newFakeAPIs(t)
	db := newDB(t)
	_, err := facts.Ingest(db, f.cfg(100))
	require.NoError(t, err)

	// 前提:ISBN 查出来的 volume id 确实落了库(否则这条测试什么都没验)。
	require.Equal(t, "GV-LEARNGO", queryOne[string](t, db,
		`SELECT COALESCE(google_volume_id,'') FROM editions WHERE book_id=2`))

	second, err := facts.Ingest(db, f.cfg(100))
	require.NoError(t, err)
	require.Zero(t, second.Requests, "volume id 写回之后,查询标记必须照旧命中")
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

// subtitledSnapshot 一份带副标题/短主标题形态的语料,给 v2 匹配器的测试用
// (不动 corpusSnapshot:那份的计数断言遍布多个测试)。
func subtitledSnapshot() *calibre.Snapshot {
	return &calibre.Snapshot{Books: []calibre.Book{
		// calibre 里的真实形态:主标题 + 冒号 + 长副标题。HN 帖子只写主标题。
		{BookID: 1, Title: "Designing Data-Intensive Applications: The Big Ideas Behind Reliable, Scalable, and Maintainable Systems",
			Authors: []string{"Martin Kleppmann"}, Publisher: "O'Reilly Media",
			Formats: []string{"EPUB"}, Language: "eng", ISBN13: "9781449373320",
			GoogleID: "GV-DDIA", HasCover: true, HasComments: true,
			Pubdate: "2017-01-01", PubdateSource: calibre.SourceCalibre},
		// 主标题只有 4 词但存在"差一个词形"的 HN 帖 → 词边界测试。
		{BookID: 2, Title: "The Pragmatic Programmer: Your Journey to Mastery",
			Authors: []string{"David Thomas"}, Publisher: "Addison-Wesley",
			Formats: []string{"EPUB"}, Language: "eng", ISBN13: "9780135957059",
			HasCover: true, Pubdate: "2019-09-13", PubdateSource: calibre.SourceCalibre},
		// 主标题 ≤2 词 + 副标题:默认必须跳过,进白名单才查。
		{BookID: 3, Title: "Learning Go: An Idiomatic Approach to Real-World Go Programming",
			Authors: []string{"Jon Bodner"}, Publisher: "O'Reilly Media",
			Formats: []string{"EPUB"}, Language: "eng", ISBN13: "9781492077213",
			HasCover: true, Pubdate: "2021-03-16", PubdateSource: calibre.SourceCalibre},
	}}
}

func newDBWith(t *testing.T, snap *calibre.Snapshot) *store.DB {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/facts.db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = corpus.Import(db, snap, now)
	require.NoError(t, err)
	return db
}

// TestIngestMatchesHNByMainTitle 回归:带副标题的书名必须按主标题查询与匹配。
// v1 匹配器拿整名(含副标题)做短语匹配,生产环境跑了三天 C 维 measured 恒为 0。
func TestIngestMatchesHNByMainTitle(t *testing.T) {
	f := newFakeAPIs(t)
	db := newDBWith(t, subtitledSnapshot())
	st, err := facts.Ingest(db, f.cfg(100))
	require.NoError(t, err)

	// 发出的查询必须是主标题短语,不带副标题。
	require.Positive(t, f.count(`hnq|"Designing Data-Intensive Applications"`),
		"HN 查询应当只用主标题")

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
	require.Contains(t, ids, "22001122")
	require.Contains(t, ids, "40001111", "主标题按词边界出现的 story 应被接受")
	require.NotContains(t, ids, "40002222", "词形不同(programmers)必须被词边界规则拒掉")
	require.NotContains(t, ids, "99999999")

	// "Learning Go: An Idiomatic Approach…" 主标题只有 2 词 → 默认跳过。
	require.Equal(t, 1, st.SkippedShortTitle)
}

// TestIngestShortMainTitleQueriedWhenFileWhitelisted 主标题白名单(文件挂载)放行短标题。
func TestIngestShortMainTitleQueriedWhenFileWhitelisted(t *testing.T) {
	f := newFakeAPIs(t)
	db := newDBWith(t, subtitledSnapshot())
	cfg := f.cfg(100)
	cfg.WhitelistMainTitles = []string{"Learning Go"}
	st, err := facts.Ingest(db, cfg)
	require.NoError(t, err)

	require.Zero(t, st.SkippedShortTitle, "白名单里的短主标题应该被查")
	var n int
	require.NoError(t, db.SQL().QueryRow(
		`SELECT COUNT(*) FROM mentions WHERE object_id='30001111'`).Scan(&n))
	require.Equal(t, 1, n, "白名单放行后应拿到 Learning Go 的提及")
}

// TestIngestReQueriesHNWhenMatcherVersionChanges 匹配器升级后,旧版本的查询标记
// 必须自动视为过期并重查 —— 否则修复要等 30 天 TTL 走完才生效。
func TestIngestReQueriesHNWhenMatcherVersionChanges(t *testing.T) {
	f := newFakeAPIs(t)
	db := newDBWith(t, subtitledSnapshot())
	_, err := facts.Ingest(db, f.cfg(100))
	require.NoError(t, err)

	// 伪造 v1 时代的状态:标记还在 TTL 内但没有 matcher 字段,且当时 0 命中。
	_, err = db.SQL().Exec(
		`UPDATE evidence SET payload='{"found":true,"raw_hits":3,"accepted":0}' WHERE source='hn_search'`)
	require.NoError(t, err)
	_, err = db.SQL().Exec(`DELETE FROM mentions`)
	require.NoError(t, err)

	hnBefore := f.count("hn")
	st, err := facts.Ingest(db, f.cfg(100))
	require.NoError(t, err)
	require.Greater(t, f.count("hn"), hnBefore, "旧版本标记必须触发重查,即使 TTL 未过")
	require.Positive(t, st.MentionsFound)

	var n int
	require.NoError(t, db.SQL().QueryRow(`SELECT COUNT(*) FROM mentions`).Scan(&n))
	require.Positive(t, n, "重查后提及必须重新落库")
}

// TestIngestReservesBudgetForMentions editions 阶段必须给 HN 留出保底预算:
// 没有保底时,editions(每版次最多 3 次请求)会把预算烧光,bootstrap 后的头几晚
// HN 一次都轮不到 —— C 维在证据最饥渴的窗口期恒为 0(2026-08 生产实测)。
func TestIngestReservesBudgetForMentions(t *testing.T) {
	f := newFakeAPIs(t)
	db := newDB(t) // 4 本有标识符的书,全查需要 ~7 次请求
	cfg := f.cfg(8)
	cfg.MentionsReserve = 2
	st, err := facts.Ingest(db, cfg)
	require.NoError(t, err)

	require.True(t, st.BudgetExhausted, "editions 尚未查完,应标记预算耗尽")
	require.Positive(t, st.MentionsFound,
		"预算紧张的晚上 HN 也必须分到保底配额(这正是 C 维恒 0 的修复)")
	require.LessOrEqual(t, st.Requests, 8)
}

// TestIngestPrefersNewBooksFirst editions 按 pubdate 新→旧查:fresh-releases 的
// 准入完全依赖外部 pubdate,追赶期里新书必须最先拿到证据。
func TestIngestPrefersNewBooksFirst(t *testing.T) {
	f := newFakeAPIs(t)
	db := newDB(t)
	// 预算只够查第一本书(google + openlibrary 两跳)。
	st, err := facts.Ingest(db, f.cfg(2))
	require.NoError(t, err)
	require.True(t, st.BudgetExhausted)

	// 2024 年的 Learning Go 2nd(book 3)是最新的,必须最先被查。
	var src string
	require.NoError(t, db.SQL().QueryRow(
		`SELECT COALESCE(pubdate_source,'') FROM editions WHERE book_id=3`).Scan(&src))
	require.Equal(t, "google", src, "最新出版的书应最先拿到外部 pubdate")
	// 最老的 DDIA(2017)这一轮还轮不到。
	require.Zero(t, queryOne[int](t, db,
		`SELECT COUNT(*) FROM evidence WHERE source='google_query' AND source_id='isbn:9781449373320'`))
}
