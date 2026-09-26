package api

import (
	_ "embed"
	"net/http"
	"strings"
)

// openapiSpec API 契约,随二进制内嵌 —— 与代码同版本发布,不会和线上漂移。
// openapi_test.go 断言每个已注册的 /api/v1 路由都在里面有条目。
//
//go:embed openapi.yaml
var openapiSpec []byte

func handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	// 文档只随镜像变,不随 run 变,所以不走 writeRunCache。
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(openapiSpec)
}

// withCORS 给 /api/ 放开跨域读。
//
// 为的是 API 文档站(apidocs.meirong.dev,Scalar)里的「Try it」能直接从浏览器调本站。
// 放 `*` 是安全的:接口只读、全部公开、不认 cookie(浏览器对 `*` 本就不带凭据)。
// ETag 要显式暴露,否则跨域脚本读不到它,If-None-Match 就无从谈起。
//
// 预检只应答 GET/HEAD 的请求 —— 这不是开了写口子:非预检的写方法照旧落到 mux 回 405。
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Expose-Headers", "ETag")
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			switch r.Header.Get("Access-Control-Request-Method") {
			case http.MethodGet, http.MethodHead:
				h.Set("Access-Control-Allow-Methods", "GET, HEAD")
				h.Set("Access-Control-Allow-Headers", "If-None-Match")
				h.Set("Access-Control-Max-Age", "86400")
				w.WriteHeader(http.StatusNoContent)
			default:
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}
