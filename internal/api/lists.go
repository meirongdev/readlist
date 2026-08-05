package api

import (
	"net/http"

	"github.com/meirongdev/readlist/internal/preset"
	"github.com/meirongdev/readlist/internal/score"
)

// listMeta 一份榜单的口径声明。
//
// weights / bands / order / min_coverage 必须发给前端:权重滑块要在客户端复算
// TBS 与 coverage,而这份响应此前只有 {id,name,description,size} —— 于是 SPA 拿到
// 的权重表恒为空,rescore() 在 totalW=0 下把每本书都算成「0.0 分 · 覆盖 0%」。
type listMeta struct {
	ID          string                 `json:"id"`
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Size        int                    `json:"size"`
	Order       string                 `json:"order"`
	MinCoverage float64                `json:"min_coverage"`
	Weights     map[string]float64     `json:"weights"`
	Bands       map[string]preset.Band `json:"bands,omitempty"`
	Needs       map[string]string      `json:"needs,omitempty"`
}

func metaOf(p preset.Preset) listMeta {
	order := p.Order
	if order == "" {
		order = "desc"
	}
	return listMeta{
		ID: p.ID, Name: p.Name, Description: p.Description,
		Size: p.Select.Size, Order: order, MinCoverage: p.Select.MinCoverage,
		Weights: p.Weights, Bands: p.Bands, Needs: p.Needs,
	}
}

func (s *Server) handleLists(w http.ResponseWriter, r *http.Request) {
	runID, version, err := s.publishedRun()
	if err != nil {
		fail(w, r, err, "published_run")
		return
	}
	if writeRunCache(w, r, runID) {
		return
	}
	presets := s.publicPresets()
	out := make([]listMeta, 0, len(presets))
	for _, p := range presets {
		out = append(out, metaOf(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run_id": runID, "standard_version": version, "lists": out,
	})
}

// listItem 榜单里的一本书。
type listItem struct {
	Rank     int                       `json:"rank"`
	WorkID   string                    `json:"work_id"`
	Title    string                    `json:"title"`
	Author   string                    `json:"author"`
	Topic    string                    `json:"topic"`
	Level    string                    `json:"level"`
	Year     int                       `json:"year,omitempty"`
	Grade    string                    `json:"grade"`
	TBS      float64                   `json:"tbs"`
	Coverage float64                   `json:"coverage"`
	Reason   string                    `json:"reason"`
	Reading  readingInfo               `json:"reading"`
	Dims     map[string]score.DimScore `json:"dims"`
}

// handleList 单榜详情。先取列表行并立即释放连接(连接池上限 1),再跑其余查询。
func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, ok := presetByID(s.publicPresets(), id)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown preset")
		return
	}
	runID, version, err := s.publishedRun()
	if err != nil {
		fail(w, r, err, "published_run")
		return
	}
	if runID == "" {
		writeError(w, http.StatusNotFound, "no published run")
		return
	}
	if writeRunCache(w, r, runID) {
		return
	}

	type row struct {
		rank     int
		workID   string
		tbs      float64
		coverage float64
		reason   string
	}
	rows, err := s.db.SQL().Query(`SELECT rank, work_id, tbs, coverage, reason
		FROM lists WHERE run_id = ? AND list_id = ? ORDER BY rank`, runID, id)
	if err != nil {
		fail(w, r, err, "query list")
		return
	}
	selected := make([]row, 0, 64)
	for rows.Next() {
		var rr row
		if err := rows.Scan(&rr.rank, &rr.workID, &rr.tbs, &rr.coverage, &rr.reason); err != nil {
			rows.Close()
			fail(w, r, err, "scan list")
			return
		}
		selected = append(selected, rr)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		fail(w, r, err, "scan list")
		return
	}
	rows.Close()

	snap, err := s.snapshot(runID, version)
	if err != nil {
		fail(w, r, err, "snapshot")
		return
	}

	items := make([]listItem, 0, len(selected))
	for _, rr := range selected {
		b := snap.Bases[rr.workID]
		items = append(items, listItem{
			Rank: rr.rank, WorkID: rr.workID, Title: b.Title, Author: b.Author,
			Topic: b.Topic, Level: b.Level, Year: b.Year,
			Grade: snap.Grades[rr.workID], TBS: rr.tbs, Coverage: rr.coverage,
			Reason: rr.reason, Reading: snap.Reading[rr.workID], Dims: snap.Dims[rr.workID],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"list": metaOf(p), "run_id": runID, "standard_version": version,
		// 顶层平铺一份 id/name/description,兼容既有消费者。
		"id": p.ID, "name": p.Name, "description": p.Description,
		"items": items,
	})
}
