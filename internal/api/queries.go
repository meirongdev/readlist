package api

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/meirongdev/readlist/internal/corpus"
	"github.com/meirongdev/readlist/internal/score"
)

// publishedRun 返回当前已发布的 run_id 与它的标准版本(空串 = 尚未打分)。
// standard_version 从 runs 表读,而不是写死在每个 handler 里 —— 已发布 run 用的
// 那个版本才是真相。
func (s *Server) publishedRun() (runID, version string, err error) {
	err = s.db.SQL().QueryRow(`SELECT p.run_id, COALESCE(r.standard_version, '')
		FROM published_run p LEFT JOIN runs r ON r.run_id = p.run_id
		WHERE p.id = 1`).Scan(&runID, &version)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	return runID, version, err
}

// runInfo 返回任意 run 的标准版本;ok=false 表示这个 run 不存在。
func (s *Server) runInfo(runID string) (version string, ok bool, err error) {
	err = s.db.SQL().QueryRow(
		`SELECT COALESCE(standard_version, '') FROM runs WHERE run_id = ?`, runID).Scan(&version)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return version, true, nil
}

// workBase 书的基本信息(works + editions 聚合)。
type workBase struct {
	WorkID    string `json:"work_id"`
	Title     string `json:"title"`
	Author    string `json:"author"`
	Topic     string `json:"topic"`
	Level     string `json:"level"`
	Publisher string `json:"publisher,omitempty"`
	// Year 是**首版年份**(最早版次)。评分引擎的 min_age_years 用的也是最早版次,
	// 两边口径一致;各版次的日期在 editions 里逐条列出。
	Year        int    `json:"year,omitempty"`
	Language    string `json:"language,omitempty"`
	Format      string `json:"format,omitempty"`
	HasCover    bool   `json:"has_cover,omitempty"`
	HasComments bool   `json:"has_comments,omitempty"`
}

type editionRow struct {
	BookID         int     `json:"book_id"`
	Title          string  `json:"title"`
	Publisher      string  `json:"publisher,omitempty"`
	Format         string  `json:"format,omitempty"`
	Language       string  `json:"language,omitempty"`
	Pubdate        string  `json:"pubdate,omitempty"`
	PubdateSource  string  `json:"pubdate_source,omitempty"`
	ISBN13         string  `json:"isbn13,omitempty"`
	GoogleVolumeID string  `json:"google_volume_id,omitempty"`
	PersonalRating float64 `json:"personal_rating,omitempty"`
}

// readingInfo 阅读状态(facet,不进分数)。
type readingInfo struct {
	Status     string   `json:"status,omitempty"`
	Shelves    []string `json:"shelves,omitempty"`
	HasReading bool     `json:"has_reading"`
}

// readStatusRank 多版次状态合并用:取"最靠前"的那个,而不是最后扫到的那行。
func readStatusRank(status string) int {
	switch status {
	case "read":
		return 3
	case "reading":
		return 2
	case "unread":
		return 1
	default:
		return 0
	}
}

// snapshot 一次请求内的只读视图。
//
// 四个内容 handler 此前各自重复拉同样的四张表、各自把错误丢进 `_`,DB 出问题时
// 表现为静默 200 + 空数据。收敛成一个入口后,错误只需在一处处理。
type snapshot struct {
	RunID    string
	Version  string
	Bases    map[string]workBase
	Editions map[string][]editionRow
	Dims     map[string]map[string]score.DimScore
	Grades   map[string]string
	Reading  map[string]readingInfo
}

func (s *Server) snapshot(runID, version string) (*snapshot, error) {
	snap := &snapshot{RunID: runID, Version: version}
	var err error
	if snap.Bases, snap.Editions, err = s.loadWorkBases(); err != nil {
		return nil, err
	}
	if snap.Dims, err = s.loadDims(runID); err != nil {
		return nil, err
	}
	snap.Grades = gradesFromDims(snap.Dims)
	if snap.Reading, err = s.readingByWork(snap.Editions); err != nil {
		return nil, err
	}
	return snap, nil
}

// loadWorkBases 加载全部 work 聚合信息(work_id → base)。
//
// 出版社与格式走 corpus 里那份唯一实现(与评分引擎同一套「取最优」规则):
// 之前展示层用"第一行的值"、引擎用"最优 tier/格式",于是详情页展示的出版社
// 可能不是打分用的那个。
func (s *Server) loadWorkBases() (map[string]workBase, map[string][]editionRow, error) {
	rows, err := s.db.SQL().Query(`SELECT w.work_id, w.canonical_title, w.first_author,
			w.primary_topic, w.level,
			e.book_id, e.title, e.publisher_norm, e.format, e.language, e.has_cover,
			e.has_comments, e.pubdate, e.pubdate_source, e.isbn13, e.google_volume_id,
			e.personal_rating_stars
		FROM works w JOIN editions e ON e.work_id = w.work_id
		ORDER BY w.work_id, e.book_id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	bases := map[string]workBase{}
	editions := map[string][]editionRow{}
	tier := map[string]int{}
	for rows.Next() {
		var (
			workID, title, author, topic, level string
			bookID                              int
			edTitle, pubNorm, format, language  string
			hasCover, hasComments               bool
			pubdate, pubdateSrc                 sql.NullString
			isbn, volumeID                      sql.NullString
			personal                            sql.NullFloat64
		)
		if err := rows.Scan(&workID, &title, &author, &topic, &level,
			&bookID, &edTitle, &pubNorm, &format, &language, &hasCover, &hasComments,
			&pubdate, &pubdateSrc, &isbn, &volumeID, &personal); err != nil {
			return nil, nil, err
		}
		b, seen := bases[workID]
		if !seen {
			b = workBase{WorkID: workID, Title: title, Author: author, Topic: topic, Level: level}
		}
		b.HasCover = b.HasCover || hasCover
		b.HasComments = b.HasComments || hasComments
		if language != "" && b.Language == "" {
			b.Language = language
		}
		if pi := corpus.Publisher(pubNorm); tier[workID] == 0 || pi.Tier < tier[workID] {
			tier[workID] = pi.Tier
			b.Publisher = pi.Norm
		}
		if corpus.FormatRank(format) > corpus.FormatRank(b.Format) {
			b.Format = format
		}
		if t, ok := parsePubdate(pubdate); ok && (b.Year == 0 || t.Year() < b.Year) {
			b.Year = t.Year()
		}
		bases[workID] = b

		ed := editionRow{
			BookID: bookID, Title: edTitle, Publisher: pubNorm, Format: format,
			Language: language, Pubdate: pubdate.String, PubdateSource: pubdateSrc.String,
			ISBN13: isbn.String, GoogleVolumeID: volumeID.String,
		}
		if s.exposeRead {
			ed.PersonalRating = personal.Float64
		}
		editions[workID] = append(editions[workID], ed)
	}
	return bases, editions, rows.Err()
}

func parsePubdate(v sql.NullString) (time.Time, bool) {
	if !v.Valid || len(v.String) < 10 {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02", v.String[:10])
	return t, err == nil
}

// loadDims 加载 run 的全部 dim_scores(work_id → dim → DimScore)。
func (s *Server) loadDims(runID string) (map[string]map[string]score.DimScore, error) {
	out := map[string]map[string]score.DimScore{}
	if runID == "" {
		return out, nil
	}
	rows, err := s.db.SQL().Query(`SELECT work_id, dim, raw, pct, score, state, source, confidence
		FROM dim_scores WHERE run_id = ? ORDER BY work_id, dim`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var wid, dim, state, source string
		var raw, pct, scr, conf float64
		if err := rows.Scan(&wid, &dim, &raw, &pct, &scr, &state, &source, &conf); err != nil {
			return nil, err
		}
		if out[wid] == nil {
			out[wid] = map[string]score.DimScore{}
		}
		out[wid][dim] = score.DimScore{Raw: raw, Pct: pct, Score: scr,
			State: score.State(state), Source: source, Confidence: conf}
	}
	return out, rows.Err()
}

// gradesFromDims 由已加载的维度状态算证据等级徽章,复用 score.Grade 的规则。
// 之前这里为了拿 state 又把 dim_scores 查了第二遍。
func gradesFromDims(dims map[string]map[string]score.DimScore) map[string]string {
	grades := make(map[string]string, len(dims))
	for wid, byDim := range dims {
		typed := make(map[score.Dim]score.DimScore, len(byDim))
		for d, ds := range byDim {
			typed[score.Dim(d)] = ds
		}
		grades[wid] = score.Grade(typed)
	}
	return grades
}

// gradesForRun 只为指标端点准备的轻量路径:只读 state,不碰 works/editions/reading。
func (s *Server) gradesForRun(runID string) (map[string]string, error) {
	if runID == "" {
		return map[string]string{}, nil
	}
	rows, err := s.db.SQL().Query(
		`SELECT work_id, dim, state FROM dim_scores WHERE run_id = ?`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := map[string]map[string]score.DimScore{}
	for rows.Next() {
		var workID, dim, state string
		if err := rows.Scan(&workID, &dim, &state); err != nil {
			return nil, err
		}
		if states[workID] == nil {
			states[workID] = map[string]score.DimScore{}
		}
		states[workID][dim] = score.DimScore{State: score.State(state)}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return gradesFromDims(states), nil
}

// readingByWork 阅读状态,按 book_id join 后挂到 work 上(system-design §4)。
// EXPOSE_READ_STATUS=false 时直接返回空 —— 这个开关此前只在 /meta 里回显,
// 而 lists/works/matrix 照旧无条件输出阅读状态与个人评分。
func (s *Server) readingByWork(editions map[string][]editionRow) (map[string]readingInfo, error) {
	out := map[string]readingInfo{}
	if !s.exposeRead {
		return out, nil
	}
	bookToWork := map[int]string{}
	for wid, eds := range editions {
		for _, e := range eds {
			bookToWork[e.BookID] = wid
		}
	}
	rows, err := s.db.SQL().Query(
		`SELECT book_id, status, shelves FROM reading ORDER BY book_id`)
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
			continue // 孤儿行(book id 漂移),丢掉 —— NFR-13
		}
		var shelves []string
		_ = json.Unmarshal([]byte(shelvesJSON), &shelves)
		ri := out[wid]
		ri.HasReading = true
		// 多版次落在同一 work 上时取"最靠前"的状态,而不是最后扫到的那行。
		if readStatusRank(status) > readStatusRank(ri.Status) {
			ri.Status = status
		}
		ri.Shelves = append(ri.Shelves, shelves...)
		out[wid] = ri
	}
	return out, rows.Err()
}

// missingDims 返回该 work 状态为 unknown 的维度(升序,供"数据不足"说明)。
func missingDims(dims map[string]score.DimScore) []string {
	out := []string{}
	for _, d := range score.AllDims {
		if ds, ok := dims[string(d)]; ok && ds.State == score.StateUnknown {
			out = append(out, string(d))
		}
	}
	return out
}
