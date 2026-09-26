package api

import (
	"database/sql"
	"encoding/json"
	"strings"

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

// listedWorks 返回「公开集合」的子查询与它的参数。
//
// 公开集合 = **公开榜单的并集**,而不是全库。本站展示的是推荐书单与上榜书的元数据;
// 藏书本身不是内容,不该被逐本枚举出去(全库约 2,000 本,上榜并集约百余本)。
//
// 定义收敛在这一处:三个内容端点都从同一个 snapshot 取数,于是「哪些书算公开」
// 只有一份定义 —— 端点各自过滤迟早会漂移出一条泄漏路径。
//
// internal 榜(library-hygiene)不进这个集合:它是给库主人看的,
// 上了 internal 榜不等于对外推荐。
func (s *Server) listedWorks(runID string) (query string, args []any) {
	presets := s.publicPresets()
	if len(presets) == 0 {
		// `IN ()` 在 SQLite 里是语法错误。没有公开榜 = 公开集合为空,如实返回空集。
		return `SELECT work_id FROM lists WHERE 0`, nil
	}
	args = make([]any, 0, len(presets)+1)
	args = append(args, runID)
	ph := make([]string, len(presets))
	for i, p := range presets {
		ph[i] = "?"
		args = append(args, p.ID)
	}
	return `SELECT work_id FROM lists WHERE run_id = ? AND list_id IN (` +
		strings.Join(ph, ",") + `)`, args
}

// workBase 书的基本信息(works + editions 聚合)。
type workBase struct {
	WorkID    string
	Title     string
	Author    string
	Topic     string
	Level     string
	Publisher string
	// Year 是**首版年份**(最早版次,且跳过被污染的日期来源)。评分引擎的 min_age_years
	// 用的是同一口径,两边一致;各版次的原始日期与来源在 editions 里逐条列出。
	Year     int
	Language string
}

// listRow 一份公开榜里的一行(lists 表)。
type listRow struct {
	Rank     int
	WorkID   string
	TBS      float64
	Coverage float64
	Reason   string
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

// privateShelves 不对外输出的书架(FR-46)。说一本书「弃读」带评价意味,等于公开给它
// 打了差评。
//
// 过滤必须在服务端做:它此前只在 app.js 里被跳过,API 照样把书架名发出去 —— 而 API
// 开了 CORS、发布了 OpenAPI 契约,本身就是公开面。评分引擎不受影响,照常读全部书架
// (to-read-next 靠它排除弃读书),这里只管对外输出。
var privateShelves = map[string]bool{"弃读": true}

// snapshot 一个 run 的只读视图,**只含上榜 work**(见 listedWorks)。
//
// 四个内容 handler 此前各自重复拉同样的四张表、各自把错误丢进 `_`,DB 出问题时
// 表现为静默 200 + 空数据。收敛成一个入口后,错误只需在一处处理。
//
// ⚠️ 构造完成后**必须视为不可变**:同一个 *snapshot 会被多个请求共享(见 Server.cached),
// 任何原地修改都是数据竞争。要按请求裁剪就拷一份。
type snapshot struct {
	RunID    string
	Version  string
	Lists    map[string][]listRow // 公开榜 id → 按 rank 升序的行
	Bases    map[string]workBase
	Editions map[string][]editionRow
	Dims     map[string]map[string]score.DimScore
	Grades   map[string]string
	Reading  map[string]readingInfo
}

// snapshot 取一个 run 的视图,命中缓存则零查询返回。
func (s *Server) snapshot(runID, version string) (*snapshot, error) {
	if snap := s.cachedSnapshot(runID, version); snap != nil {
		return snap, nil
	}
	// 冷缓存时只放一个请求去建,其余的等它建完直接命中。换 run 之后同时到达的请求
	// 若各自重建,就是 N 份同样的查询排在那条唯一的连接上(实测 32 个并发请求建了
	// 24 次)—— 正是缓存要防的「爬虫把 /healthz 挤到超时」。
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	if snap := s.cachedSnapshot(runID, version); snap != nil {
		return snap, nil
	}

	snap, err := s.buildSnapshot(runID, version)
	if err != nil {
		return nil, err
	}
	// 只缓存**已发布**的那个 run。当下所有 handler 都只请求已发布 run,这道判断是
	// 防回归的:一旦哪天又出现按任意 run 寻址的端点,交替请求两个 run 就能让每个请求
	// 都重建一遍单槽缓存 —— 把缓存变成放大器。
	if published, _, err := s.publishedRun(); err == nil && published == runID {
		s.mu.Lock()
		s.cached = snap
		s.mu.Unlock()
	}
	return snap, nil
}

// cachedSnapshot 缓存里若正好是这个 run 的视图就返回它,否则 nil。
func (s *Server) cachedSnapshot(runID, version string) *snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if c := s.cached; c != nil && c.RunID == runID && c.Version == version {
		return c
	}
	return nil
}

func (s *Server) buildSnapshot(runID, version string) (*snapshot, error) {
	s.snapshotBuilds.Add(1)
	snap := &snapshot{RunID: runID, Version: version}
	var err error
	if snap.Lists, err = s.loadLists(runID); err != nil {
		return nil, err
	}
	if snap.Bases, snap.Editions, err = s.loadWorkBases(runID); err != nil {
		return nil, err
	}
	if snap.Dims, err = s.loadDims(runID); err != nil {
		return nil, err
	}
	snap.Grades = s.gradesFromDims(snap.Dims)
	if snap.Reading, err = s.readingByWork(snap.Editions); err != nil {
		return nil, err
	}
	return snap, nil
}

// loadLists 加载 run 里全部**公开**榜的行。internal 榜不进快照 —— 它不对外服务。
func (s *Server) loadLists(runID string) (map[string][]listRow, error) {
	out := map[string][]listRow{}
	presets := s.publicPresets()
	if len(presets) == 0 {
		return out, nil // `IN ()` 在 SQLite 里是语法错误
	}
	args := []any{runID}
	ph := make([]string, len(presets))
	for i, p := range presets {
		ph[i] = "?"
		args = append(args, p.ID)
	}
	rows, err := s.db.SQL().Query(`SELECT list_id, rank, work_id, tbs, coverage, reason
		FROM lists WHERE run_id = ? AND list_id IN (`+strings.Join(ph, ",")+`)
		ORDER BY list_id, rank`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var listID string
		var r listRow
		if err := rows.Scan(&listID, &r.Rank, &r.WorkID, &r.TBS, &r.Coverage, &r.Reason); err != nil {
			return nil, err
		}
		out[listID] = append(out[listID], r)
	}
	return out, rows.Err()
}

// loadWorkBases 加载**上榜** work 的聚合信息(work_id → base)。
//
// 出版社、语言、首版年份走 score.WorkInput.AddEdition —— 与评分引擎同一份聚合实现。
// 之前两边各写一份,已经漂移过:展示给读者的出版社与年份必须就是打分用的那一个。
func (s *Server) loadWorkBases(runID string) (map[string]workBase, map[string][]editionRow, error) {
	listed, args := s.listedWorks(runID)
	rows, err := s.db.SQL().Query(`SELECT w.work_id, w.canonical_title, w.first_author,
			w.primary_topic, w.level,
			e.book_id, e.title, e.publisher_norm, e.format, e.language, e.has_cover,
			e.has_comments, e.pubdate, e.pubdate_source, e.isbn13, e.google_volume_id,
			e.personal_rating_stars
		FROM works w JOIN editions e ON e.work_id = w.work_id
		WHERE w.work_id IN (`+listed+`)
		ORDER BY w.work_id, e.book_id`, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	bases := map[string]workBase{}
	editions := map[string][]editionRow{}
	aggs := map[string]*score.WorkInput{}
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
		agg, seen := aggs[workID]
		if !seen {
			agg = &score.WorkInput{}
			aggs[workID] = agg
			bases[workID] = workBase{WorkID: workID, Title: title, Author: author, Topic: topic, Level: level}
		}
		agg.AddEdition(score.Edition{
			PublisherNorm: pubNorm, Format: format, Language: language,
			HasComments: hasComments, HasCover: hasCover, ISBN13: isbn.String,
			Pubdate: pubdate.String, PubdateSource: pubdateSrc.String,
		})

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
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	// 被污染的日期来源(mtime 兜底/缺失)不进首版年份 —— 否则一本 2013 年的书会因为
	// 某个版次的 mtime 兜底值而在页面上显示成 2026。这条规则在 AddEdition 里。
	for wid, agg := range aggs {
		b := bases[wid]
		b.Publisher, b.Language = agg.PublisherNorm, agg.Language
		if agg.FirstPubdate != nil {
			b.Year = agg.FirstPubdate.Year()
		}
		bases[wid] = b
	}
	return bases, editions, nil
}

// loadDims 加载 run 里**上榜** work 的 dim_scores(work_id → dim → DimScore)。
func (s *Server) loadDims(runID string) (map[string]map[string]score.DimScore, error) {
	out := map[string]map[string]score.DimScore{}
	if runID == "" {
		return out, nil
	}
	listed, listedArgs := s.listedWorks(runID)
	// 占位符按文本顺序绑定:外层的 run_id 在子查询之前,所以它排在参数首位。
	args := append([]any{runID}, listedArgs...)
	rows, err := s.db.SQL().Query(`SELECT work_id, dim, raw, pct, score, state, source, confidence
		FROM dim_scores WHERE run_id = ? AND work_id IN (`+listed+`)
		ORDER BY work_id, dim`, args...)
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
//
// graded 用**全部** preset(含 internal)推,和打分时 Engine.Compute 用的是同一个
// 集合 —— 徽章不落库、每次读都重算,两边口径必须同源,否则 /metrics 报的字母分布
// 会和页面上显示的对不上。
func (s *Server) gradesFromDims(dims map[string]map[string]score.DimScore) map[string]string {
	graded := score.GradedDims(s.presets)
	grades := make(map[string]string, len(dims))
	for wid, byDim := range dims {
		typed := make(map[score.Dim]score.DimScore, len(byDim))
		for d, ds := range byDim {
			typed[score.Dim(d)] = ds
		}
		grades[wid] = score.Grade(typed, graded)
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
	return s.gradesFromDims(states), nil
}

// readingByWork 阅读状态,按 book_id join 后挂到 work 上(system-design §4)。
// EXPOSE_READ_STATUS=false 时直接返回空 —— 这个开关此前只在 /meta 里回显,
// 而各内容端点照旧无条件输出阅读状态与个人评分。
//
// 入参 editions 已经只含上榜书,所以 join 不上的行有两类:未上榜的书(绝大多数,
// 正常),以及真正的 book id 漂移。两类都丢掉。
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
			continue // 未上榜 或 孤儿行(book id 漂移),都丢掉 —— NFR-13
		}
		var shelves []string
		_ = json.Unmarshal([]byte(shelvesJSON), &shelves)
		public := make([]string, 0, len(shelves))
		for _, sh := range shelves {
			if !privateShelves[sh] {
				public = append(public, sh)
			}
		}
		// 这一行唯一的内容就是私有书架 → 当它不存在。否则响应里是 has_reading=true
		// 却什么都不显示,等于在说「这里有一条不能给你看的记录」。
		if status == "" && len(shelves) > 0 && len(public) == 0 {
			continue
		}
		ri := out[wid]
		ri.HasReading = true
		// 多版次取最靠前的状态,与评分引擎同一口径(见 corpus.ReadStatusRank)。
		if corpus.ReadStatusRank(status) > corpus.ReadStatusRank(ri.Status) {
			ri.Status = status
		}
		ri.Shelves = append(ri.Shelves, public...)
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
