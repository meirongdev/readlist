package api

import (
	"net/http"
	"sort"

	"github.com/meirongdev/readlist/internal/score"
)

// matrixRow 矩阵里的一行(works × dims + facets)。
type matrixRow struct {
	WorkID  string                    `json:"work_id"`
	Title   string                    `json:"title"`
	Author  string                    `json:"author"`
	Topic   string                    `json:"topic"`
	Level   string                    `json:"level"`
	Year    int                       `json:"year,omitempty"`
	Grade   string                    `json:"grade"`
	Dims    map[string]score.DimScore `json:"dims"`
	Reading readingInfo               `json:"reading"`
}

// handleMatrix 滑块用的整块矩阵,按 run_id 寻址 → 可以 immutable 长缓存。
//
// 两处修正:
//  1. 先确认这个 run 真的存在。之前任意 run_id(包括根本不存在的)都会拿到
//     200 + `Cache-Control: immutable`,一条拼错的 URL 会被浏览器和 CDN 永久
//     缓存成一份空矩阵,而且无法失效。
//  2. 行集合与 /catalog 完全一致(全部 work + 逐维 state)。两个端点的可见集合
//     必须相同,否则"哪些书算公开"就有两份互相矛盾的定义。
func (s *Server) handleMatrix(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("run")
	if runID == "" {
		writeError(w, http.StatusBadRequest, "run required")
		return
	}
	version, ok, err := s.runInfo(runID)
	if err != nil {
		fail(w, r, err, "run info")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "unknown run")
		return
	}
	snap, err := s.snapshot(runID, version)
	if err != nil {
		fail(w, r, err, "snapshot")
		return
	}

	works := make([]matrixRow, 0, len(snap.Bases))
	for wid, b := range snap.Bases {
		works = append(works, matrixRow{
			WorkID: wid, Title: b.Title, Author: b.Author, Topic: b.Topic,
			Level: b.Level, Year: b.Year, Grade: snap.Grades[wid],
			Dims: snap.Dims[wid], Reading: snap.Reading[wid],
		})
	}
	sort.Slice(works, func(i, j int) bool { return works[i].WorkID < works[j].WorkID })

	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	writeJSON(w, http.StatusOK, map[string]any{
		"run_id": runID, "standard_version": version, "works": works,
	})
}
