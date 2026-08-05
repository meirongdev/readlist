package api

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// staticETags 内嵌资源的内容指纹(path → ETag)。
//
// embed.FS 的文件 modtime 是零值,所以 http.FileServer 既不发 Last-Modified 也不发
// ETag —— 每次打开页面都要重新下载 app.js / style.css。而文件名里没有内容哈希
// (本项目刻意没有前端构建步骤),所以也不能简单地长缓存。用内容指纹做 ETag 两头都占:
// 平时是 304,换镜像后立刻失效。
var staticETags = map[string]string{}

func init() {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	// 失败意味着二进制本身是坏的,和 fs.Sub 一样只能 panic。
	if err := fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		f, err := sub.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		staticETags[p] = `"` + hex.EncodeToString(h.Sum(nil))[:16] + `"`
		return nil
	}); err != nil {
		panic(err)
	}
}

// reservedPath 这些路径不是 SPA 路由,不该回落到 index.html。
// (它们都已在 mux 上单独注册,这里是防御性的第二道。)
func reservedPath(p string) bool {
	return strings.HasPrefix(p, "/api/") || p == "/healthz" || p == "/livez" || p == "/metrics"
}

// staticHandler 托管内嵌 SPA。
func staticHandler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reservedPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if f, err := sub.Open(p); err == nil {
			f.Close()
			if staticCache(w, r, p) {
				return
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		// 未知路径回落到 SPA 外壳(hash 路由)。
		if staticCache(w, r, "index.html") {
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	})
}

// staticCache 打上 ETag 与缓存头;返回 true 表示已回 304。
func staticCache(w http.ResponseWriter, r *http.Request, name string) bool {
	etag, ok := staticETags[name]
	if !ok {
		return false
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=60, must-revalidate")
	if m := r.Header.Get("If-None-Match"); m != "" && etagMatches(m, etag) {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	return false
}
