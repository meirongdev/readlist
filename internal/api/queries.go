package api

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/meirongdev/readlist/internal/score"
)

// publishedRun 返回当前已发布的 run_id(空串 = 尚未打分)。
func (s *Server) publishedRun() (string, error) {
	var runID string
	err := s.query().SQL().QueryRow(`SELECT run_id FROM published_run WHERE id=1`).Scan(&runID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return runID, err
}

// dimScoreRow 一行 dim_scores。
type dimScoreRow struct {
	Dim        string  `json:"dim"`
	Raw        float64 `json:"raw"`
	Pct        float64 `json:"pct"`
	Score      float64 `json:"score"`
	State      string  `json:"state"`
	Source     string  `json:"source,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
}

// workBase 书的基本信息(works + editions 聚合)。
type workBase struct {
	WorkID      string `json:"work_id"`
	Title       string `json:"title"`
	Author      string `json:"author"`
	Topic       string `json:"topic"`
	Level       string `json:"level"`
	Publisher   string `json:"publisher,omitempty"`
	Year        int    `json:"year,omitempty"`
	Language    string `json:"language,omitempty"`
	Grade       string `json:"grade"`
	Format      string `json:"format,omitempty"`
	HasCover    bool   `json:"has_cover,omitempty"`
	HasComments bool   `json:"has_comments,omitempty"`
}

// readingInfo 阅读状态(facet)。
type readingInfo struct {
	Status     string   `json:"status,omitempty"`
	Shelves    []string `json:"shelves,omitempty"`
	HasReading bool     `json:"has_reading"`
}

// loadWorkBases 加载全部 work 聚合信息(work_id → base)。
func (s *Server) loadWorkBases() (map[string]workBase, map[string][]editionRow, error) {
	db := s.query().SQL()
	rows, err := db.Query(`SELECT w.work_id, w.canonical_title, w.first_author, w.primary_topic, w.level,
		e.book_id, e.title, e.publisher_norm, e.format, e.language, e.has_cover, e.has_comments,
		e.pubdate, e.pubdate_source, e.personal_rating_stars
		FROM works w JOIN editions e ON e.work_id = w.work_id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	bases := map[string]workBase{}
	editions := map[string][]editionRow{}
	for rows.Next() {
		var (
			workID, title, author, topic, level string
			bookID                              int
			edTitle, pubNorm, format, language  string
			hasCover, hasComments               bool
			pubdate, pubdateSrc                 sql.NullString
			personal                            sql.NullFloat64
		)
		if err := rows.Scan(&workID, &title, &author, &topic, &level,
			&bookID, &edTitle, &pubNorm, &format, &language, &hasCover, &hasComments,
			&pubdate, &pubdateSrc, &personal); err != nil {
			return nil, nil, err
		}
		b, ok := bases[workID]
		if !ok {
			b = workBase{WorkID: workID, Title: title, Author: author, Topic: topic, Level: level,
				Publisher: pubNorm, Language: language, Format: format, HasCover: hasCover, HasComments: hasComments}
			bases[workID] = b
		}
		if pubdate.Valid && pubdate.String != "" {
			if t, err := time.Parse("2006-01-02", pubdate.String[:10]); err == nil {
				if b.Year == 0 || t.Year() < b.Year {
					b.Year = t.Year()
				}
			}
		}
		editions[workID] = append(editions[workID], editionRow{
			BookID: bookID, Title: edTitle, Publisher: pubNorm, Format: format,
			Language: language, Pubdate: pubdate.String, PubdateSource: pubdateSrc.String,
			PersonalRating: personal.Float64,
		})
	}
	return bases, editions, rows.Err()
}

type editionRow struct {
	BookID         int     `json:"book_id"`
	Title          string  `json:"title"`
	Publisher      string  `json:"publisher,omitempty"`
	Format         string  `json:"format,omitempty"`
	Language       string  `json:"language,omitempty"`
	Pubdate        string  `json:"pubdate,omitempty"`
	PubdateSource  string  `json:"pubdate_source,omitempty"`
	PersonalRating float64 `json:"personal_rating,omitempty"`
}

// loadGrades 加载 run 的等级徽章(复用 score.Grade 规则)。
func (s *Server) loadGrades(runID string) (map[string]string, error) {
	if runID == "" {
		return map[string]string{}, nil
	}
	rows, err := s.query().SQL().Query(
		`SELECT work_id, dim, state FROM dim_scores WHERE run_id=?`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := map[string]map[score.Dim]score.State{}
	for rows.Next() {
		var workID, dim, state string
		if err := rows.Scan(&workID, &dim, &state); err != nil {
			return nil, err
		}
		if states[workID] == nil {
			states[workID] = map[score.Dim]score.State{}
		}
		states[workID][score.Dim(dim)] = score.State(state)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	grades := map[string]string{}
	for wid, m := range states {
		dims := map[score.Dim]score.DimScore{}
		for d, st := range m {
			dims[d] = score.DimScore{State: st}
		}
		grades[wid] = score.Grade(dims)
	}
	return grades, nil
}

// readingByWork 阅读状态(book_id → work 已聚合到 work 级)。
func (s *Server) readingByWork(editions map[string][]editionRow) (map[string]readingInfo, error) {
	bookToWork := map[int]string{}
	for wid, eds := range editions {
		for _, e := range eds {
			bookToWork[e.BookID] = wid
		}
	}
	out := map[string]readingInfo{}
	rows, err := s.query().SQL().Query(`SELECT book_id, status, shelves FROM reading`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var bookID int
		var status, shelvesJSON string
		if err := rows.Scan(&bookID, &status, &shelvesJSON); err != nil {
			return nil, err
		}
		wid, ok := bookToWork[bookID]
		if !ok {
			continue
		}
		var shelves []string
		_ = json.Unmarshal([]byte(shelvesJSON), &shelves)
		ri := out[wid]
		ri.HasReading = true
		ri.Status = status
		ri.Shelves = append(ri.Shelves, shelves...)
		out[wid] = ri
	}
	return out, rows.Err()
}
