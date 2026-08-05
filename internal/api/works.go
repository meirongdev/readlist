package api

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/meirongdev/readlist/internal/score"
)

func (s *Server) handleWork(w http.ResponseWriter, r *http.Request) {
	workID := r.PathValue("id")
	snap, ok := s.loadSnapshot(w, r)
	if !ok {
		return
	}
	b, found := snap.Bases[workID]
	if !found {
		writeError(w, http.StatusNotFound, "unknown work")
		return
	}

	// 缺失维度说明(前端据此展示"数据不足"而不是一个编出来的 0 分)。
	missing := []map[string]string{}
	for _, dim := range missingDims(snap.Dims[workID]) {
		missing = append(missing, map[string]string{"dim": dim, "why": missingWhy(dim)})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"work_id": workID, "title": b.Title, "author": b.Author,
		"topic": b.Topic, "level": b.Level, "publisher": b.Publisher,
		"year": b.Year, "language": b.Language,
		"grade":  snap.Grades[workID],
		"run_id": snap.RunID, "standard_version": snap.Version,
		"dims":     snap.Dims[workID],
		"missing":  missing,
		"editions": snap.Editions[workID],
		"reading":  snap.Reading[workID],
		"links":    externalLinks(b, snap.Editions[workID]),
	})
}

// externalLinks 外链。封面与阅读入口一律外链,本站不保存正文(NFR-12)。
//
// 有强标识符就直连该条目,否则退回搜索 —— 查询串必须转义:之前是把书名直接拼进
// 路径("https://…/edition/_/" + title),带空格的书名产出的是一条坏链。
func externalLinks(b workBase, editions []editionRow) map[string]string {
	var isbn, volumeID string
	for _, e := range editions {
		if volumeID == "" {
			volumeID = strings.TrimSpace(e.GoogleVolumeID)
		}
		if isbn == "" {
			isbn = strings.TrimSpace(e.ISBN13)
		}
	}
	q := strings.TrimSpace(b.Title + " " + b.Author)

	google := "https://www.google.com/search?tbm=bks&q=" + url.QueryEscape(q)
	if volumeID != "" {
		google = "https://books.google.com/books?id=" + url.QueryEscape(volumeID)
	}
	openlib := "https://openlibrary.org/search?q=" + url.QueryEscape(q)
	if isbn != "" {
		openlib = "https://openlibrary.org/isbn/" + url.PathEscape(isbn)
	}
	return map[string]string{"google_books": google, "openlibrary": openlib}
}

func missingWhy(dim string) string {
	switch score.Dim(dim) {
	case score.DimFreshness:
		return "出版日期不可信或缺失"
	case score.DimTrust:
		return "作者与出版社均未知"
	case score.DimDepth, score.DimPractical:
		return "标注置信度低"
	case score.DimAcclaim:
		return "无外部评分"
	case score.DimCommunity:
		return "无 HN 提及"
	default:
		return "证据不足"
	}
}
