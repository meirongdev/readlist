package api

import (
	"net/http"
	"sort"
)

// catalogRow 上榜书目的一行。
type catalogRow struct {
	WorkID   string   `json:"work_id"`
	Title    string   `json:"title"`
	Author   string   `json:"author"`
	Topic    string   `json:"topic"`
	Level    string   `json:"level"`
	Year     int      `json:"year,omitempty"`
	Grade    string   `json:"grade"`
	Language string   `json:"language,omitempty"`
	Missing  []string `json:"missing,omitempty"`
}

// handleCatalog 上榜书目 —— **公开榜单的并集**,逐本标注缺哪几维。
//
// 这里此前收录全库(约 2,000 本)。那是把「藏书」当成了内容:本站要展示的是推荐
// 书单和上榜书的元数据,把整个私人书库逐本枚举出去既不是产品意图,也让「哪些书算
// 公开」多出一个远大于榜单的面。现在行集合 = snapshot 的可见集合 = 上榜并集。
//
// 保留的是另一件事:**不按 grade 过滤**。证据是逐维度产生的,压成一个字母去卡准入,
// 会让「出版日期来自 mtime 兜底」的书从整站消失(review B1,实测全库 23%)。所以
// 上了榜但某几维没证据的书照样出现在这里,并标注缺哪几维,而不是被静默剔除。
func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	snap, ok := s.loadSnapshot(w, r)
	if !ok {
		return
	}
	works := make([]catalogRow, 0, len(snap.Bases))
	for wid, b := range snap.Bases {
		works = append(works, catalogRow{
			WorkID: wid, Title: b.Title, Author: b.Author, Topic: b.Topic,
			Level: b.Level, Year: b.Year, Language: b.Language,
			Grade: snap.Grades[wid], Missing: missingDims(snap.Dims[wid]),
		})
	}
	// 稳定顺序:书名 → work_id。map 迭代序会让同一个 run 每次刷新都换一个排列。
	sort.Slice(works, func(i, j int) bool {
		if works[i].Title != works[j].Title {
			return works[i].Title < works[j].Title
		}
		return works[i].WorkID < works[j].WorkID
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"run_id": snap.RunID, "standard_version": snap.Version,
		"total": len(works), "works": works,
	})
}
