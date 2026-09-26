package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/meirongdev/readlist/internal/corpus"
	"github.com/meirongdev/readlist/internal/preset"
	"github.com/meirongdev/readlist/internal/score"
	"github.com/meirongdev/readlist/internal/store"
)

const testVersion = "v0.0.0-test"

func newTestServer(t *testing.T, exposeRead bool) *Server {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/test.db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = corpus.Seed(db)
	require.NoError(t, err)
	presets, err := preset.Load()
	require.NoError(t, err)
	eng := score.NewEngine(db, "1.0", time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC))
	_, err = eng.Run(presets)
	require.NoError(t, err)
	return NewServer(db, presets, exposeRead, testVersion)
}

func doReq(t *testing.T, s *Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, httptest.NewRequest(method, path, nil))
	return rr
}

func doReqWith(t *testing.T, s *Server, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, req)
	return rr
}

func getJSON(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var m map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &m))
	return m
}

func workPath(workID string) string { return "/api/v1/works/" + url.PathEscape(workID) }

// ---------- 榜单清单 ----------

func TestPresetsListExcludesInternal(t *testing.T) {
	m := getJSON(t, doReq(t, newTestServer(t, true), http.MethodGet, "/api/v1/lists"))
	ids := map[string]bool{}
	for _, x := range m["lists"].([]any) {
		ids[x.(map[string]any)["id"].(string)] = true
	}
	require.True(t, ids["timeless"])
	require.False(t, ids["library-hygiene"], "internal 榜不应出现在公开列表")
}

func TestListsCarryWeightProfile(t *testing.T) {
	// 「榜单 = 权重档案」是对外主张,所以口径必须随榜发出来、并且自洽:
	// 权重和为 1、band 维度必有权重。这份响应此前只有 {id,name,description,size},
	// 榜单页就没法把「这个排名按什么算」印给读者。
	m := getJSON(t, doReq(t, newTestServer(t, true), http.MethodGet, "/api/v1/lists"))
	for _, x := range m["lists"].([]any) {
		l := x.(map[string]any)
		id := l["id"].(string)
		weights, ok := l["weights"].(map[string]any)
		require.True(t, ok, "%s 缺 weights", id)
		require.NotEmpty(t, weights, "%s 的 weights 为空", id)
		var sum float64
		for _, w := range weights {
			sum += w.(float64)
		}
		require.InDelta(t, 1.0, sum, 1e-6, "%s 的权重和不为 1", id)
		require.Contains(t, []any{"desc", "asc"}, l["order"], "%s 缺 order", id)
		require.Contains(t, l, "min_coverage", "%s 缺 min_coverage", id)

		// band 是可选的:当前没有任何一份榜声明目标带(唯一用过 band 的维度是 D,
		// 而 D 只来自 labels、生产里恒为 unknown,连同权重一起撤了)。声明了就必须自洽。
		for dim, raw := range asMap(l["bands"]) {
			b := raw.(map[string]any)
			require.Contains(t, b, "target", "%s 的 band %s 缺 target", id, dim)
			require.Contains(t, b, "tol")
			require.Contains(t, weights, dim, "%s 的 band 维度 %s 必须有权重", id, dim)
		}
	}
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func TestBandSerializesWithLowercaseKeys(t *testing.T) {
	// 目标带的 json tag 掉了的话,前端拿到的是 {"Target":..,"Tol":..},读出来全是
	// undefined —— 而榜单页不会报错,只会安静地按未加 band 的分数展示。
	//
	// 这条断言刻意**不依赖 presets.yaml 里恰好有一份榜带 band**:榜单是配置,
	// 会随产品口径增减;序列化格式是契约,不该因为某天没人用 band 就失去防线。
	raw, err := json.Marshal(metaOf(preset.Preset{
		ID: "x", Weights: map[string]float64{"D": 1},
		Bands: map[string]preset.Band{"D": {Target: 75, Tol: 35}},
	}))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, map[string]any{"target": 75.0, "tol": 35.0},
		asMap(got["bands"])["D"], "band 的 json tag 丢了")
}

// ---------- 准入语义 ----------

func TestListItemsSatisfyNeedsAndCoverage(t *testing.T) {
	// 准入只有 needs + min_coverage 两道门,证据等级字母不参与(system-design §2)。
	s := newTestServer(t, true)
	for _, p := range s.publicPresets() {
		m := getJSON(t, doReq(t, s, http.MethodGet, "/api/v1/lists/"+p.ID))
		items := m["items"].([]any)
		require.NotEmpty(t, items, "榜 %s 为空", p.ID)
		for _, raw := range items {
			it := raw.(map[string]any)
			dims := it["dims"].(map[string]any)
			for dim, need := range p.Needs {
				d, ok := dims[dim].(map[string]any)
				require.True(t, ok, "榜 %s 的 %s 缺维度 %s", p.ID, it["work_id"], dim)
				require.True(t,
					score.StateAtLeast(score.State(d["state"].(string)), score.State(need)),
					"榜 %s 的 %s 在维度 %s 上未满足 needs=%s", p.ID, it["work_id"], dim, need)
			}
			require.GreaterOrEqual(t, it["coverage"].(float64)+1e-9, p.Select.MinCoverage,
				"榜 %s 的 %s coverage 低于 min_coverage", p.ID, it["work_id"])
			require.NotEmpty(t, it["reason"], "榜 %s 的 %s 缺理由串", p.ID, it["work_id"])
		}
	}
}

// publicUnion 直接从库里算「公开集合」= 公开榜单的并集。
// 故意不复用被测的 listedWorks:两边同一个实现,断言就成了自证。
func publicUnion(t *testing.T, s *Server) map[string]bool {
	t.Helper()
	runID, _, err := s.publishedRun()
	require.NoError(t, err)
	require.NotEmpty(t, runID)
	ids := map[string]bool{}
	for _, p := range s.publicPresets() {
		rows, err := s.db.SQL().Query(
			`SELECT work_id FROM lists WHERE run_id=? AND list_id=?`, runID, p.ID)
		require.NoError(t, err)
		for rows.Next() {
			var w string
			require.NoError(t, rows.Scan(&w))
			ids[w] = true
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
	}
	return ids
}

func TestCatalogIsListedUnionNotWholeLibrary(t *testing.T) {
	// 本站展示的是推荐书单与上榜书的元数据。目录页此前收录全库,等于把整个私人
	// 书库逐本枚举出去 —— 那不是内容。现在行集合 = 公开榜单的并集。
	s := newTestServer(t, true)
	m := getJSON(t, doReq(t, s, http.MethodGet, "/api/v1/catalog"))
	works := m["works"].([]any)
	require.NotEmpty(t, works)

	var total int
	require.NoError(t, s.db.SQL().QueryRow(`SELECT COUNT(*) FROM works`).Scan(&total))
	listed := publicUnion(t, s)
	require.Len(t, works, len(listed), "目录必须正好是公开榜单的并集")
	require.Equal(t, float64(len(listed)), m["total"].(float64))
	require.Less(t, len(listed), total,
		"演示语料里上榜并集必须真的小于全库,否则这条断言分不出「并集」和「全库」")

	for _, raw := range works {
		w := raw.(map[string]any)
		require.True(t, listed[w["work_id"].(string)], "%v 未上任何公开榜却出现在目录里", w["work_id"])
	}
}

func TestCatalogAnnotatesMissingDimsInsteadOfDroppingRows(t *testing.T) {
	// 目录页此前还按 grade 过滤掉 D 级 → 出版日期来自 mtime 兜底的书从整站消失。
	// 收窄到上榜并集**不能**顺手把这条也改回去:上了榜就该出现,缺哪几维如实标注
	// (review B1 / system-design §2)。
	s := newTestServer(t, true)
	works := getJSON(t, doReq(t, s, http.MethodGet, "/api/v1/catalog"))["works"].([]any)
	var sawAnnotated bool
	for _, raw := range works {
		w := raw.(map[string]any)
		require.NotEmpty(t, w["grade"], "每行都要有徽章")
		if missing, ok := w["missing"].([]any); ok && len(missing) > 0 {
			sawAnnotated = true
		}
	}
	require.True(t, sawAnnotated, "缺维度的书必须被标注出来,而不是被静默剔除")
}

func TestUnlistedWorksAreNotPublic(t *testing.T) {
	// 「不展示全量 catalog」必须是整个公开面的性质,不能只是目录页少画几行:
	// 未上榜的书按 id 直接请求也得 404,否则收窄只是障眼法。
	s := newTestServer(t, true)
	listed := publicUnion(t, s)

	pick := func(query string, args ...any) string {
		t.Helper()
		rows, err := s.db.SQL().Query(query, args...)
		require.NoError(t, err)
		defer rows.Close()
		for rows.Next() {
			var w string
			require.NoError(t, rows.Scan(&w))
			if !listed[w] {
				return w
			}
		}
		require.NoError(t, rows.Err())
		return ""
	}

	unlisted := pick(`SELECT work_id FROM works ORDER BY work_id`)
	require.NotEmpty(t, unlisted, "演示语料里必须有未上榜的书,否则这条断言验不到东西")
	require.Equal(t, http.StatusNotFound, doReq(t, s, http.MethodGet, workPath(unlisted)).Code,
		"未上榜的书不该能按 id 拉到")

	// 上了 internal 榜不等于对外推荐:那些榜是给库主人看的。
	runID, _, err := s.publishedRun()
	require.NoError(t, err)
	internalOnly := pick(
		`SELECT work_id FROM lists WHERE run_id=? AND list_id='library-hygiene' ORDER BY work_id`, runID)
	if internalOnly != "" {
		require.Equal(t, http.StatusNotFound, doReq(t, s, http.MethodGet, workPath(internalOnly)).Code,
			"只上了 internal 榜的书不该进公开面")
	}
}

func TestMatrixEndpointIsGone(t *testing.T) {
	// matrix 是「全库 works × dims」的整块导出,且零消费者(review B4)。
	// 留着它,收窄目录页就等于没做。
	s := newTestServer(t, true)
	run := getJSON(t, doReq(t, s, http.MethodGet, "/api/v1/meta"))["run_id"].(string)
	rr := doReq(t, s, http.MethodGet, "/api/v1/matrix/"+url.PathEscape(run))
	require.Equal(t, http.StatusNotFound, rr.Code)
	require.Empty(t, rr.Header().Get("Cache-Control"), "404 绝不能带长缓存")
}

func TestCatalogOrderIsStable(t *testing.T) {
	s := newTestServer(t, true)
	first := doReq(t, s, http.MethodGet, "/api/v1/catalog").Body.String()
	for i := 0; i < 3; i++ {
		require.Equal(t, first, doReq(t, s, http.MethodGet, "/api/v1/catalog").Body.String())
	}
}

// ---------- 书详情 ----------

func TestWorkDetailBreakdown(t *testing.T) {
	s := newTestServer(t, true)
	lists := getJSON(t, doReq(t, s, http.MethodGet, "/api/v1/lists/timeless"))
	wid := lists["items"].([]any)[0].(map[string]any)["work_id"].(string)
	m := getJSON(t, doReq(t, s, http.MethodGet, workPath(wid)))
	require.NotEmpty(t, m["dims"].(map[string]any))
	require.NotEmpty(t, m["standard_version"], "版本号应取自已发布 run")
	require.NotEmpty(t, m["editions"])
	require.Contains(t, m, "missing")
}

func TestWorkUnknownDimsAreLabelledNotScored(t *testing.T) {
	// F unknown 的书:必须带上「为什么缺」的说明,前端才能显示「数据不足」
	// 而不是把占位的 0 当成真实得分展示。
	s := newTestServer(t, true)
	m := getJSON(t, doReq(t, s, http.MethodGet, workPath("richardson/restful web apis")))
	// 徽章按**参与排序的维度**评(A/C/T/readability 全实测 → A),而缺失的 F 走
	// missing 逐维如实说明 —— 「缺哪几维」是逐维信息,压不进一个字母里。
	require.Equal(t, "A", m["grade"])
	missing := m["missing"].([]any)
	require.NotEmpty(t, missing)
	var sawF bool
	for _, raw := range missing {
		e := raw.(map[string]any)
		require.NotEmpty(t, e["why"], "每个缺失维度都要有原因")
		if e["dim"] == "F" {
			sawF = true
		}
	}
	require.True(t, sawF)
	require.Equal(t, "unknown", m["dims"].(map[string]any)["F"].(map[string]any)["state"])
}

func TestWorkLinksAreWellFormed(t *testing.T) {
	// 之前把书名直接拼进 URL 路径,带空格的书名产出坏链。
	s := newTestServer(t, true)
	m := getJSON(t, doReq(t, s, http.MethodGet, workPath("kleppmann/designing data intensive applications")))
	links := m["links"].(map[string]any)
	require.Len(t, links, 2)
	for name, raw := range links {
		v := raw.(string)
		u, err := url.Parse(v)
		require.NoError(t, err, name)
		require.Equal(t, "https", u.Scheme, name)
		require.NotContains(t, v, " ", "%s 含未转义空格: %s", name, v)
	}
}

// ---------- 开关与只读契约 ----------

func TestExposeReadStatusFalseHidesReadingAndRatings(t *testing.T) {
	// 这个开关此前只在 /meta 回显,各内容端点照旧无条件输出阅读状态。
	off := newTestServer(t, false)
	require.Equal(t, false, getJSON(t, doReq(t, off, http.MethodGet, "/api/v1/meta"))["expose_read_status"])

	items := getJSON(t, doReq(t, off, http.MethodGet, "/api/v1/lists/timeless"))["items"].([]any)
	require.NotEmpty(t, items)
	for _, raw := range items {
		reading := raw.(map[string]any)["reading"].(map[string]any)
		require.Equal(t, false, reading["has_reading"], "关闭开关后不该输出阅读状态")
		require.NotContains(t, reading, "status")
		require.NotContains(t, reading, "shelves")
	}

	// 个人星级同样属于阅读数据。
	m := getJSON(t, doReq(t, off, http.MethodGet, workPath("kleppmann/designing data intensive applications")))
	for _, raw := range m["editions"].([]any) {
		require.NotContains(t, raw.(map[string]any), "personal_rating")
	}

	// 打开时确实有内容,否则上面的断言等于什么都没验。
	on := newTestServer(t, true)
	var sawReading bool
	for _, raw := range getJSON(t, doReq(t, on, http.MethodGet, "/api/v1/lists/timeless"))["items"].([]any) {
		if raw.(map[string]any)["reading"].(map[string]any)["has_reading"] == true {
			sawReading = true
		}
	}
	require.True(t, sawReading)
}

// listWorkIDs 直接从库里读已发布 run 的某份榜。改完 reading 再发请求的测试必须走这里:
// 先调 API 会把改动之前的 snapshot 填进缓存。
func listWorkIDs(t *testing.T, s *Server, listID string) []string {
	t.Helper()
	rows, err := s.db.SQL().Query(`SELECT work_id FROM lists
		WHERE run_id = (SELECT run_id FROM published_run WHERE id = 1) AND list_id = ?
		ORDER BY rank`, listID)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		out = append(out, id)
	}
	require.NoError(t, rows.Err())
	return out
}

// BDD: reading-status.feature「弃读书架默认不公开」/ book-detail.feature「弃读状态默认不公开」
func TestAbandonedShelfNeverLeavesServer(t *testing.T) {
	// 「弃读」带评价意味(FR-46)。它此前只在 app.js 里被跳过,API 照样把书架名发出去
	// —— 而 API 开了 CORS、发布了 OpenAPI 契约,本身就是公开面。
	s := newTestServer(t, true)
	listed := listWorkIDs(t, s, "timeless")
	require.GreaterOrEqual(t, len(listed), 2, "前提:公开榜上至少两本书")
	mixed, onlyAbandoned := listed[0], listed[1]

	setReading := func(workID, status, shelves string) {
		t.Helper()
		_, err := s.db.SQL().Exec(`DELETE FROM reading
			WHERE book_id IN (SELECT book_id FROM editions WHERE work_id = ?)`, workID)
		require.NoError(t, err)
		_, err = s.db.SQL().Exec(`INSERT INTO reading (book_id, status, shelves, downloads)
			SELECT MIN(book_id), ?, ?, 0 FROM editions WHERE work_id = ?`, status, shelves, workID)
		require.NoError(t, err)
	}
	setReading(mixed, "read", `["弃读","精读"]`)
	setReading(onlyAbandoned, "", `["弃读"]`)

	list := doReq(t, s, http.MethodGet, "/api/v1/lists/timeless")
	require.NotContains(t, list.Body.String(), "弃读", "榜单响应里出现了弃读书架")
	reading := map[string]map[string]any{}
	for _, raw := range getJSON(t, list)["items"].([]any) {
		it := raw.(map[string]any)
		reading[it["work_id"].(string)] = asMap(it["reading"])
	}
	require.Equal(t, "read", reading[mixed]["status"], "其余可公开的状态照常输出")
	require.Equal(t, []any{"精读"}, reading[mixed]["shelves"], "其余可公开的书架照常输出")
	require.Equal(t, false, reading[onlyAbandoned]["has_reading"],
		"弃读是唯一记录时,「有阅读记录」本身也不该泄露")
	require.NotContains(t, reading[onlyAbandoned], "shelves")

	for _, wid := range []string{mixed, onlyAbandoned} {
		require.NotContains(t, doReq(t, s, http.MethodGet, workPath(wid)).Body.String(), "弃读",
			"书详情 %s 的响应里出现了弃读书架", wid)
	}
	detail := getJSON(t, doReq(t, s, http.MethodGet, workPath(onlyAbandoned)))
	require.Equal(t, false, asMap(detail["reading"])["has_reading"])
}

// BDD: reading-status.feature「多版次的阅读状态按同一规则合并」
func TestReadingQueueNeverCarriesReadBadge(t *testing.T) {
	// 榜单准入(评分引擎)与徽章(API)各自合并多版次的阅读状态。两边规则一旦不同,
	// 阅读队列里就会出现挂着 ✓ 已读 的书 —— 这条测试把两层钉在一起。
	s := newTestServer(t, true)
	queue := listWorkIDs(t, s, "to-read-next")
	require.NotEmpty(t, queue, "前提:阅读队列非空")
	wid := queue[0]

	// 旧版次已读、新版次显式未读 —— calibre-web 里取消「已读」留下的就是这种行。
	exec := func(q string, args ...any) {
		t.Helper()
		_, err := s.db.SQL().Exec(q, args...)
		require.NoError(t, err)
	}
	exec(`INSERT INTO editions (book_id, work_id, title, format, language, publisher_norm)
		SELECT 99999, work_id, title, format, language, publisher_norm
		  FROM editions WHERE work_id = ? ORDER BY book_id LIMIT 1`, wid)
	exec(`INSERT OR REPLACE INTO reading (book_id, status, shelves)
		SELECT MIN(book_id), 'read', '[]' FROM editions WHERE work_id = ? AND book_id <> 99999`, wid)
	exec(`INSERT OR REPLACE INTO reading (book_id, status, shelves) VALUES (99999, 'unread', '[]')`)
	_, err := score.NewEngine(s.db, "1.0", time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)).Run(s.presets)
	require.NoError(t, err)

	for _, raw := range getJSON(t, doReq(t, s, http.MethodGet, "/api/v1/lists/to-read-next"))["items"].([]any) {
		it := raw.(map[string]any)
		status := asMap(it["reading"])["status"]
		require.NotContains(t, []any{"read", "reading"}, status,
			"阅读队列里的 %s 挂着「%v」徽章", it["work_id"], status)
	}
}

func TestReadOnlyContract(t *testing.T) {
	s := newTestServer(t, true)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		require.Equal(t, http.StatusMethodNotAllowed,
			doReq(t, s, method, "/api/v1/lists/timeless").Code, "%s 应被拒绝", method)
	}
	require.Equal(t, http.StatusNotFound, doReq(t, s, http.MethodGet, "/api/v1/does-not-exist").Code)
	require.Equal(t, http.StatusNotFound, doReq(t, s, http.MethodGet, "/api/v1/works/999999").Code)
	require.Equal(t, http.StatusNotFound, doReq(t, s, http.MethodGet, "/api/v1/lists/library-hygiene").Code,
		"internal 榜不能直接按 id 拉到")
}

func TestHealthzAndMetrics(t *testing.T) {
	s := newTestServer(t, true)
	m := getJSON(t, doReq(t, s, http.MethodGet, "/healthz"))
	require.Equal(t, "ok", m["status"])
	require.NotEmpty(t, m["run_id"])
	require.NotEmpty(t, m["corpus_id"], "健康信息应带语料指纹,便于确认在服务哪份语料")
	require.NotEmpty(t, m["standard_version"])

	rr := doReq(t, s, http.MethodGet, "/metrics")
	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	for _, want := range []string{
		"readlist_works_total", "readlist_grade_counts", "readlist_lists_total",
		"readlist_runs_retained", "readlist_last_score_unix",
	} {
		require.Contains(t, body, want)
	}
}

func TestMetricsCoverFreshnessAndDataQuality(t *testing.T) {
	// 只看 last_score 是不够的:score 在陈旧 facts 上每晚照样成功,snapshot 或 ingest
	// 挂掉一个月它依然常绿(review B1)。判别力与数据质量同理 —— 一维全是收缩值、
	// 或 mtime 污染没被清掉,榜单都不会报错。
	s := newTestServer(t, true)
	body := doReq(t, s, http.MethodGet, "/metrics").Body.String()
	for _, want := range []string{
		"readlist_last_snapshot_unix", "readlist_last_ingest_unix",
		"readlist_dim_measured", "readlist_pubdate_source", "readlist_orphan_rows",
		"readlist_ingest_requests", "readlist_ingest_throttled",
	} {
		require.Contains(t, body, want)
	}
	// 逐维都要出现:某一维归零时要看得出是「归零」,而不是「指标丢了」。
	for _, d := range score.AllDims {
		require.Contains(t, body, fmt.Sprintf("readlist_dim_measured{dim=%q}", d))
	}
	// PRD §5 的护栏指标(mtime-fallback → 0)的唯一观测点。演示语料里确实有这类书。
	require.Contains(t, body, `readlist_pubdate_source{source="mtime-fallback"}`)
}

// ---------- 缓存与探针(review B2)----------

func TestContentEndpointsAreRevalidatable(t *testing.T) {
	// 内容按 run 不可变,而 run 每夜才换一次。没有 ETag,每个爬虫请求都要落到源站,
	// 而源站是「一条 SQLite 连接 + 单副本」—— 这是自伤,边缘限流挡不住。
	s := newTestServer(t, true)
	for _, path := range []string{
		"/api/v1/lists",
		"/api/v1/lists/timeless",
		"/api/v1/catalog",
		workPath("kleppmann/designing data intensive applications"),
		"/",       // SPA 外壳
		"/app.js", // 文件名里没有内容哈希,只能靠 ETag 既拿 304 又能在换镜像后失效
	} {
		rr := doReq(t, s, http.MethodGet, path)
		require.Equal(t, http.StatusOK, rr.Code, path)
		etag := rr.Header().Get("ETag")
		require.NotEmpty(t, etag, "%s 缺 ETag", path)
		require.NotEmpty(t, rr.Header().Get("Cache-Control"), "%s 缺 Cache-Control", path)

		again := doReqWith(t, s, path, map[string]string{"If-None-Match": etag})
		require.Equal(t, http.StatusNotModified, again.Code, "%s 应命中 304", path)
		require.Empty(t, again.Body.String(), "%s 的 304 不该带响应体", path)

		// 弱校验前缀与逗号列表都要认(RFC 9110)。
		weak := doReqWith(t, s, path, map[string]string{"If-None-Match": `W/` + etag + `, "other"`})
		require.Equal(t, http.StatusNotModified, weak.Code, "%s 未处理弱校验/列表形式", path)
	}
}

func TestNewRunInvalidatesETagAndCache(t *testing.T) {
	// 缓存与 ETag 都以 published_run 为键。搞错的话表现是:重算成功了,
	// 但站点继续服务旧榜,而且没有任何报错。
	s := newTestServer(t, true)
	before := doReq(t, s, http.MethodGet, "/api/v1/lists/timeless")
	beforeETag := before.Header().Get("ETag")
	beforeRun := getJSON(t, before)["run_id"].(string)

	presets, err := preset.Load()
	require.NoError(t, err)
	_, err = score.NewEngine(s.db, "1.0", time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)).Run(presets)
	require.NoError(t, err)

	after := doReq(t, s, http.MethodGet, "/api/v1/lists/timeless")
	require.NotEqual(t, beforeETag, after.Header().Get("ETag"), "换 run 后 ETag 必须变")
	require.NotEqual(t, beforeRun, getJSON(t, after)["run_id"], "换 run 后必须服务新 run")
	// 旧 ETag 不能再命中 304,否则读者会被永久钉在旧榜上。
	require.Equal(t, http.StatusOK,
		doReqWith(t, s, "/api/v1/lists/timeless", map[string]string{"If-None-Match": beforeETag}).Code)
}

func TestSnapshotCacheIsSafeUnderConcurrency(t *testing.T) {
	// 快照缓存被所有请求共享,而 -race 只有在**真的并发**时才看得见问题。
	// 缓存里那个 *snapshot 是只读共享的,任何原地修改都会在这里暴露。
	s := newTestServer(t, true)
	paths := []string{
		"/api/v1/lists/timeless", "/api/v1/catalog", "/api/v1/lists",
		workPath("kleppmann/designing data intensive applications"),
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			rr := httptest.NewRecorder()
			s.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
			if rr.Code != http.StatusOK {
				t.Errorf("%s → %d", path, rr.Code)
			}
		}(paths[i%len(paths)])
	}
	wg.Wait()
}

func TestLivezSurvivesDatabaseFailure(t *testing.T) {
	// 存活探针必须**不碰数据库**:数据库慢或坏是「暂时别收流量」(readiness),
	// 不是「进程坏了该重启」。让 liveness 查库,等于在高负载时杀掉唯一副本 ——
	// 而 SQLite 单写锁下这个副本不可替代。
	s := newTestServer(t, true)
	require.NoError(t, s.db.Close())

	live := doReq(t, s, http.MethodGet, "/livez")
	require.Equal(t, http.StatusOK, live.Code, "/livez 不该依赖数据库")
	require.Equal(t, http.StatusInternalServerError,
		doReq(t, s, http.MethodGet, "/healthz").Code, "/healthz 该如实报告数据库故障")
}

// BDD: book-detail.feature「详情页展示的版次聚合与评分同源」
func TestDisplayedEditionFactsMatchScoring(t *testing.T) {
	// 「多版次 → work」的聚合(出版社取最优 tier、首版年份跳过污染来源、语言……)
	// 展示层与评分引擎此前各写一份,已经漂移过一次:语言在引擎里取首个版次,在
	// 展示层取首个非空。两份拷贝迟早再漂 —— 这条测试把两层钉在同一个结果上。
	s := newTestServer(t, true)
	listed := listWorkIDs(t, s, "timeless")
	require.NotEmpty(t, listed, "前提:公开榜非空")
	wid := listed[0]

	exec := func(q string, args ...any) {
		t.Helper()
		_, err := s.db.SQL().Exec(q, args...)
		require.NoError(t, err)
	}
	// 已有版次的语言全部置空 → 语言必须取后面第一个非空的版次。
	exec(`UPDATE editions SET language = '' WHERE work_id = ?`, wid)
	for _, ed := range []struct {
		id                   int
		lang, pub, date, src string
	}{
		// 出版社更差、日期更早且来源可用于判断年龄 → 首版年份变成 1999,出版社不变。
		{99991, "deu", "Some Tiny Press", "1999-05-01", "calibre"},
		// 更早,但是 mtime 兜底 → 评分与展示都不该认它。
		{99992, "fra", "Some Tiny Press", "1990-01-01", "mtime-fallback"},
	} {
		exec(`INSERT INTO editions (book_id, work_id, title, format, language, publisher_norm, pubdate, pubdate_source)
			VALUES (?, ?, 'alt edition', 'PDF', ?, ?, ?, ?)`, ed.id, wid, ed.lang, ed.pub, ed.date, ed.src)
	}

	c, err := score.NewEngine(s.db, "1.0", time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)).Compute(s.presets)
	require.NoError(t, err)
	w := c.Inputs[wid]
	require.NotNil(t, w.FirstPubdate)
	require.Equal(t, 1999, w.FirstPubdate.Year(), "calibre 来源可用于判断年龄;mtime 兜底的 1990 不算")

	detail := getJSON(t, doReq(t, s, http.MethodGet, workPath(wid)))
	require.Equal(t, w.PublisherNorm, detail["publisher"], "展示的出版社应是评分用的那个")
	require.EqualValues(t, w.FirstPubdate.Year(), detail["year"], "首版年份与 min_age_years 同一口径")
	require.Equal(t, "deu", detail["language"], "语言取第一个非空的版次语言")
	require.Equal(t, w.Language, detail["language"], "展示的语言与评分输入不一致")
}

// BDD: api.feature「爬虫负载下内容请求不反复查库」
func TestSnapshotBuiltOncePerRunUnderConcurrency(t *testing.T) {
	// 换 run 之后缓存是冷的。单连接下,同时到达的 N 个请求会各自把快照重建一遍,
	// 排队占住那条连接 —— 正是缓存要防的「爬虫把 /healthz 挤到超时」。
	s := newTestServer(t, true)
	listed := listWorkIDs(t, s, "timeless")
	require.NotEmpty(t, listed)
	paths := []string{"/api/v1/lists/timeless", "/api/v1/catalog", workPath(listed[0])}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			<-start
			rr := httptest.NewRecorder()
			s.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
			if rr.Code != http.StatusOK {
				t.Errorf("%s → %d", path, rr.Code)
			}
		}(paths[i%len(paths)])
	}
	close(start)
	wg.Wait()
	require.EqualValues(t, 1, s.snapshotBuilds.Load(), "同一个 run 的快照只该从库里构建一次")
}

// BDD: api.feature「爬虫负载下内容请求不反复查库」
func TestListServedFromSnapshotOnceCached(t *testing.T) {
	s := newTestServer(t, true)
	before := getJSON(t, doReq(t, s, http.MethodGet, "/api/v1/lists/timeless"))["items"]
	require.NotEmpty(t, before)

	// 缓存建好之后删掉榜单行:还能原样返回,说明这条路径没有再查 lists 表。
	_, err := s.db.SQL().Exec(`DELETE FROM lists`)
	require.NoError(t, err)
	after := getJSON(t, doReq(t, s, http.MethodGet, "/api/v1/lists/timeless"))["items"]
	require.Equal(t, before, after, "已缓存的 run 不该再按请求查榜单表")
}

// BDD: observability.feature「健康信息可确认正在运行的版本」
func TestHealthzReportsBuildVersion(t *testing.T) {
	m := getJSON(t, doReq(t, newTestServer(t, true), http.MethodGet, "/healthz"))
	require.Equal(t, testVersion, m["version"], "版本号应来自构建注入,而不是写死的字符串")
}

// BDD: catalog.feature「不存在全库导出端点」
func TestHealthzDoesNotExposeLibrarySize(t *testing.T) {
	// /healthz 同样挂在公开域名上。全库规模是运维信号,只在 /metrics 上报。
	m := getJSON(t, doReq(t, newTestServer(t, true), http.MethodGet, "/healthz"))
	require.NotContains(t, m, "works", "全库计数只该出现在 /metrics")
}
