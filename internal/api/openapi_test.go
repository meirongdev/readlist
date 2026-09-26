package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// 契约防漂移:server.go 里注册的每个 /api/v1 路由都必须在 openapi.yaml 有条目,反之亦然。
// 直接扫源码而不是维护第二份路由清单 —— 第二份清单本身就会漂。
func TestOpenAPICoversEveryRoute(t *testing.T) {
	src, err := os.ReadFile("server.go")
	require.NoError(t, err)
	var routes []string
	for _, m := range regexp.MustCompile(`"GET (/api/v1/[^"]*)"`).FindAllStringSubmatch(string(src), -1) {
		routes = append(routes, m[1])
	}
	require.NotEmpty(t, routes, "正则没扫到任何路由,等于什么都没验")

	var doc struct {
		OpenAPI string                    `yaml:"openapi"`
		Paths   map[string]map[string]any `yaml:"paths"`
	}
	require.NoError(t, yaml.Unmarshal(openapiSpec, &doc))
	require.Equal(t, "3.1.0", doc.OpenAPI)

	var documented []string
	for p, ops := range doc.Paths {
		require.Contains(t, ops, "get", "%s 应只有 GET(零写接口)", p)
		require.Len(t, ops, 1, "%s 不应声明 GET 以外的方法", p)
		documented = append(documented, p)
	}
	sort.Strings(routes)
	sort.Strings(documented)
	require.Equal(t, routes, documented)
}

func TestOpenAPIServed(t *testing.T) {
	s := newTestServer(t, true)
	rr := doReq(t, s, http.MethodGet, "/api/v1/openapi.yaml")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Header().Get("Content-Type"), "application/yaml")
	require.Equal(t, openapiSpec, rr.Body.Bytes())
}

func TestCORSOnAPIOnly(t *testing.T) {
	s := newTestServer(t, true)

	rr := doReq(t, s, http.MethodGet, "/api/v1/lists")
	require.Equal(t, "*", rr.Header().Get("Access-Control-Allow-Origin"))
	require.Equal(t, "ETag", rr.Header().Get("Access-Control-Expose-Headers"))

	// SPA 与运维端点不需要跨域。
	require.Empty(t, doReq(t, s, http.MethodGet, "/healthz").Header().Get("Access-Control-Allow-Origin"))
}

func preflight(t *testing.T, s *Server, method string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/lists/timeless", nil)
	req.Header.Set("Origin", "https://apidocs.meirong.dev")
	req.Header.Set("Access-Control-Request-Method", method)
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, req)
	return rr
}

func TestCORSPreflightIsReadOnly(t *testing.T) {
	s := newTestServer(t, true)

	rr := preflight(t, s, http.MethodGet)
	require.Equal(t, http.StatusNoContent, rr.Code)
	require.Equal(t, "GET, HEAD", rr.Header().Get("Access-Control-Allow-Methods"))

	// 预检不能成为写方法的后门。
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		require.Equal(t, http.StatusMethodNotAllowed, preflight(t, s, m).Code, "%s 预检应被拒绝", m)
	}
}
