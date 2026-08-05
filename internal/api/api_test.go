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
	return NewServer(db, presets, exposeRead)
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

func TestListsCarryWeightsForClientRescore(t *testing.T) {
	// 权重滑块要在客户端复算 TBS。这份响应此前只有 {id,name,description,size},
	// 于是 SPA 拿到的权重表恒为空,每本书都被显示成「0.0 分 · 覆盖 0%」。
	m := getJSON(t, doReq(t, newTestServer(t, true), http.MethodGet, "/api/v1/lists"))
	var checkedBand bool
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

		if bands, ok := l["bands"].(map[string]any); ok && len(bands) > 0 {
			for dim, raw := range bands {
				b := raw.(map[string]any)
				require.Contains(t, b, "target", "%s 的 band %s 缺 target(json tag 丢了?)", id, dim)
				require.Contains(t, b, "tol")
				require.Contains(t, weights, dim, "%s 的 band 维度 %s 必须有权重", id, dim)
				checkedBand = true
			}
		}
	}
	require.True(t, checkedBand, "至少要有一份榜带 band,否则这条断言什么都没验")
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

func TestCatalogCoversWholeLibraryAndAnnotatesMissing(t *testing.T) {
	// 目录页此前按 grade 过滤掉 D 级 → 出版日期来自 mtime 兜底的书从整站消失。
	// 现在收全库,逐本标注缺哪几维(review B1 / system-design §2)。
	s := newTestServer(t, true)
	m := getJSON(t, doReq(t, s, http.MethodGet, "/api/v1/catalog"))
	works := m["works"].([]any)
	require.NotEmpty(t, works)

	var total int
	require.NoError(t, s.db.SQL().QueryRow(`SELECT COUNT(*) FROM works`).Scan(&total))
	require.Equal(t, total, len(works), "目录必须收录全库")
	require.Equal(t, float64(total), m["total"].(float64))

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
	require.Equal(t, "D", m["grade"])
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

// ---------- matrix ----------

func TestMatrixRejectsUnknownRun(t *testing.T) {
	// 之前任意 run_id(包括不存在的)都返回 200 + immutable,一条拼错的 URL
	// 会被永久缓存成空矩阵。
	s := newTestServer(t, true)
	rr := doReq(t, s, http.MethodGet, "/api/v1/matrix/no-such-run")
	require.Equal(t, http.StatusNotFound, rr.Code)
	require.Empty(t, rr.Header().Get("Cache-Control"), "404 绝不能带长缓存")
}

func TestMatrixServesPublishedRunImmutable(t *testing.T) {
	s := newTestServer(t, true)
	meta := getJSON(t, doReq(t, s, http.MethodGet, "/api/v1/meta"))
	run := meta["run_id"].(string)

	rr := doReq(t, s, http.MethodGet, "/api/v1/matrix/"+url.PathEscape(run))
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "public, max-age=31536000, immutable", rr.Header().Get("Cache-Control"))

	var m map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &m))
	matrix := m["works"].([]any)

	// matrix 与 catalog 的可见集合必须相同,否则「哪些书算公开」有两份定义。
	cat := getJSON(t, doReq(t, s, http.MethodGet, "/api/v1/catalog"))
	require.Equal(t, len(cat["works"].([]any)), len(matrix))
	for _, raw := range matrix {
		require.NotEmpty(t, raw.(map[string]any)["dims"], "滑块需要逐维得分")
	}
}

// ---------- 开关与只读契约 ----------

func TestExposeReadStatusFalseHidesReadingAndRatings(t *testing.T) {
	// 这个开关此前只在 /meta 回显,lists/works/matrix 照旧无条件输出阅读状态。
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
