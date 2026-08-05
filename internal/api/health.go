package api

import "net/http"

// handleLivez 存活探针:只回答「进程还在响应 HTTP 吗」,**不碰数据库**。
//
// livenessProbe 必须走这里,readinessProbe 才走 /healthz。两者语义不同:
// 数据库慢或榜单没打分是「暂时不该收流量」(readiness),不是「进程坏了该重启」
// (liveness)。让 liveness 去查库,等于在高负载时主动杀掉唯一副本 —— SQLite 单写锁
// 下这个副本还是不可替代的(review B2)。
func handleLivez(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte("ok\n"))
}

// handleHealthz 就绪探针:查库,回答「现在能不能正常提供内容」。
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	info, err := s.Health()
	if err != nil {
		fail(w, r, err, "health")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, info)
}

// Health 健康信息:已发布 run、语料指纹、书量。
func (s *Server) Health() (map[string]any, error) {
	runID, version, err := s.publishedRun()
	if err != nil {
		return nil, err
	}
	var workCount int
	if err := s.db.SQL().QueryRow(`SELECT COUNT(*) FROM works`).Scan(&workCount); err != nil {
		return nil, err
	}
	var corpusID string
	if runID != "" {
		_ = s.db.SQL().QueryRow(
			`SELECT COALESCE(corpus_id, '') FROM runs WHERE run_id=?`, runID).Scan(&corpusID)
	}
	return map[string]any{
		"status":           "ok",
		"run_id":           runID,
		"corpus_id":        corpusID,
		"standard_version": version,
		"works":            workCount,
		"version":          "readlist-mvp",
	}, nil
}
