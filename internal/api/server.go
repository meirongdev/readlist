package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/meirongdev/readlist/internal/preset"
	"github.com/meirongdev/readlist/internal/store"
)

// Server 只读 API + 内嵌 SPA。
type Server struct {
	db         *store.DB
	presets    []preset.Preset
	exposeRead bool
}

// NewServer 构建 API 服务。
func NewServer(db *store.DB, presets []preset.Preset, exposeRead bool) *Server {
	return &Server{db: db, presets: presets, exposeRead: exposeRead}
}

// Routes 全部为 GET/HEAD;未注册方法自动 405(mux 行为)→ 满足"零写接口"。
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("GET /api/v1/meta", s.handleMeta)
	mux.HandleFunc("GET /api/v1/lists", s.handleLists)
	mux.HandleFunc("GET /api/v1/lists/{id}", s.handleList)
	mux.HandleFunc("GET /api/v1/works/{id}", s.handleWork)
	mux.HandleFunc("GET /api/v1/matrix/{run}", s.handleMatrix)
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

// loadSnapshot 取已发布 run 的只读视图;未打分时返回 ok=false 并已写好响应。
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
