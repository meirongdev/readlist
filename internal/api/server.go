package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/meirongdev/readlist/internal/preset"
	"github.com/meirongdev/readlist/internal/store"
)

// Server 只读 API + 内嵌 SPA。
type Server struct {
	db         *store.DB
	presets    []preset.Preset
	exposeRead bool

	// 已发布 run 的快照缓存。内容按 run 不可变,而 run 每夜才换一次。
	//
	// 没有它,每个内容请求都要重新拉 works+editions+dim_scores+reading,而连接池
	// 只有一条连接 → 一阵爬虫就能让请求排队,让同样查库的 /healthz 超过
	// livenessProbe 的默认 1 秒超时,于是 kubelet 在高负载时杀掉唯一副本
	// (review B2)。限流挡不住这种自伤:阈值之下的正常流量就足够触发。
	//
	// 快照收窄到上榜并集之后每次重建便宜了一个量级,但便宜不等于免费 —— 单连接下
	// 仍是串行的,缓存照留。
	mu     sync.RWMutex
	cached *snapshot
}

// NewServer 构建 API 服务。
func NewServer(db *store.DB, presets []preset.Preset, exposeRead bool) *Server {
	return &Server{db: db, presets: presets, exposeRead: exposeRead}
}

// Routes 全部为 GET/HEAD;未注册方法自动 405(mux 行为)→ 满足"零写接口"。
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /livez", handleLivez)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("GET /api/v1/meta", s.handleMeta)
	mux.HandleFunc("GET /api/v1/lists", s.handleLists)
	mux.HandleFunc("GET /api/v1/lists/{id}", s.handleList)
	mux.HandleFunc("GET /api/v1/works/{id}", s.handleWork)
	mux.HandleFunc("GET /api/v1/catalog", s.handleCatalog)
	// /api/ 兜底:未知资源 404;非 GET 一律 405(零写接口)。
	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		http.NotFound(w, r)
	}))
	mux.Handle("/", staticHandler())
	return mux
}

// writeRunCache 给「内容随 run 变」的响应打上 ETag 与缓存头,并处理 If-None-Match。
// 返回 true 表示已经回了 304,调用方必须直接返回。
//
// 榜单每夜才换一次,而公开站的流量以爬虫为主 —— 一个 ETag 就能让边缘把绝大部分请求
// 挡在源站之外。这与边缘限流是两件事:限流防滥用,缓存防**常态**负载。
// max-age 故意短:配合 ETag,换 run 之后最多 60 秒被发现,而重验证只花一个 304。
func writeRunCache(w http.ResponseWriter, r *http.Request, runID string) bool {
	if runID == "" {
		return false // 还没打分:别缓存一个空站
	}
	etag := `"` + runID + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=60, must-revalidate")
	if m := r.Header.Get("If-None-Match"); m != "" && etagMatches(m, etag) {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	return false
}

// etagMatches 按 RFC 9110 比对 If-None-Match:允许逗号列表、弱校验前缀与 `*`。
func etagMatches(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		p := strings.TrimSpace(part)
		if p == "*" || p == etag || strings.TrimPrefix(p, "W/") == etag {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// fail 记录内部错误并回 500。数据库出问题必须是可见的失败,而不是静默的空数据 ——
// 后者在排障时表现为「榜单突然空了但一切正常」。
func fail(w http.ResponseWriter, r *http.Request, err error, what string) {
	slog.Error("request failed", "path", r.URL.Path, "op", what, "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

// loadSnapshot 取已发布 run 的只读视图;未打分或已回 304 时返回 ok=false,
// 且响应已经写好。
func (s *Server) loadSnapshot(w http.ResponseWriter, r *http.Request) (*snapshot, bool) {
	runID, version, err := s.publishedRun()
	if err != nil {
		fail(w, r, err, "published_run")
		return nil, false
	}
	if runID == "" {
		writeError(w, http.StatusNotFound, "no published run")
		return nil, false
	}
	if writeRunCache(w, r, runID) {
		return nil, false
	}
	snap, err := s.snapshot(runID, version)
	if err != nil {
		fail(w, r, err, "snapshot")
		return nil, false
	}
	return snap, true
}

// publicPresets 过滤 internal 榜。
func (s *Server) publicPresets() []preset.Preset {
	out := make([]preset.Preset, 0, len(s.presets))
	for _, p := range s.presets {
		if p.Public() {
			out = append(out, p)
		}
	}
	return out
}

func presetByID(presets []preset.Preset, id string) (preset.Preset, bool) {
	for _, p := range presets {
		if p.ID == id {
			return p, true
		}
	}
	return preset.Preset{}, false
}
