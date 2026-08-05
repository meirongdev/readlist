package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/meirongdev/readlist/internal/corpus"
	"github.com/meirongdev/readlist/internal/preset"
	"github.com/meirongdev/readlist/internal/score"
	"github.com/meirongdev/readlist/internal/store"
)

func newTestServer(t *testing.T) *Server {
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
	return NewServer(db, presets, true)
}

func doReq(t *testing.T, s *Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, httptest.NewRequest(method, path, nil))
	return rr
}

func getJSON(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	require.Equal(t, http.StatusOK, rr.Code)
	var m map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &m))
	return m
}

func TestPresetsListExcludesInternal(t *testing.T) {
	s := newTestServer(t)
	m := getJSON(t, doReq(t, s, http.MethodGet, "/api/v1/lists"))
	ids := map[string]bool{}
	for _, x := range m["lists"].([]any) {
		ids[x.(map[string]any)["id"].(string)] = true
	}
	require.True(t, ids["timeless"])
	require.False(t, ids["library-hygiene"], "internal 榜不应出现在公开列表")
}

func TestTimelessListHasNoDGradedBook(t *testing.T) {
	s := newTestServer(t)
	m := getJSON(t, doReq(t, s, http.MethodGet, "/api/v1/lists/timeless"))
	items := m["items"].([]any)
	require.NotEmpty(t, items)
	for _, it := range items {
		require.NotEqual(t, "D", it.(map[string]any)["grade"])
		require.Greater(t, it.(map[string]any)["tbs"].(float64), 0.0)
	}
}

func TestWorkDetailBreakdown(t *testing.T) {
	s := newTestServer(t)
	lists := getJSON(t, doReq(t, s, http.MethodGet, "/api/v1/lists/timeless"))
	wid := lists["items"].([]any)[0].(map[string]any)["work_id"].(string)
	m := getJSON(t, doReq(t, s, http.MethodGet, "/api/v1/works/"+url.PathEscape(wid)))
	require.Contains(t, m, "dims")
	require.Contains(t, m, "standard_version")
	require.Contains(t, m, "editions")
	require.NotEmpty(t, m["dims"].(map[string]any))
}

func TestCatalogExcludesD(t *testing.T) {
	s := newTestServer(t)
	m := getJSON(t, doReq(t, s, http.MethodGet, "/api/v1/catalog"))
	works := m["works"].([]any)
	require.NotEmpty(t, works)
	for _, w := range works {
		require.NotEqual(t, "D", w.(map[string]any)["grade"])
	}
}

func TestReadOnlyContract(t *testing.T) {
	s := newTestServer(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rr := doReq(t, s, method, "/api/v1/lists/timeless")
		require.Equal(t, http.StatusMethodNotAllowed, rr.Code, "%s 应被拒绝", method)
	}
	// 未知资源 404。
	require.Equal(t, http.StatusNotFound, doReq(t, s, http.MethodGet, "/api/v1/does-not-exist").Code)
	require.Equal(t, http.StatusNotFound, doReq(t, s, http.MethodGet, "/api/v1/works/999999").Code)
}

func TestMatrixImmutablePublicOnly(t *testing.T) {
	s := newTestServer(t)
	meta := getJSON(t, doReq(t, s, http.MethodGet, "/api/v1/meta"))
	run := meta["run_id"].(string)
	rr := doReq(t, s, http.MethodGet, "/api/v1/matrix/"+run)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "public, max-age=31536000, immutable", rr.Header().Get("Cache-Control"))
	var m map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &m))
	for _, w := range m["works"].([]any) {
		require.NotEqual(t, "D", w.(map[string]any)["grade"])
	}
}

func TestHealthz(t *testing.T) {
	s := newTestServer(t)
	m := getJSON(t, doReq(t, s, http.MethodGet, "/healthz"))
	require.Equal(t, "ok", m["status"])
	require.NotEmpty(t, m["run_id"])
}
