package api

import (
	"net/http"
	"sort"
)

// catalogRow 目录页的一行。
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

// handleCatalog 全库目录 —— 收录**全部** work,逐本标注缺哪几维。
//
// 这里此前按 grade 过滤掉 D 级。那个闸门是 review B1 认定的模型错误:证据是逐维度
// 产生的,压成一个字母去卡全局准入,会让「出版日期来自 mtime 兜底」的书从整站消失
// (实测全库 23%)。system-design §2 的处置是:字母降级为徽章,低覆盖的书**仍进
// 全库目录并标注缺哪几维**,只是进不了那些真正需要该维度的榜单。
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
