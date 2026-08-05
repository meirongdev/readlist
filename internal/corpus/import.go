package corpus

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/meirongdev/readlist/internal/calibre"
	"github.com/meirongdev/readlist/internal/store"
)

// ImportStats 一次快照导入的统计,进 runs 表供监控。
type ImportStats struct {
	Works          int `json:"works"`
	Editions       int `json:"editions"`
	DroppedBooks   int `json:"dropped_books"`   // 快照里已消失、本次删掉的 edition 数
	ReadingRows    int `json:"reading_rows"`    //
	OrphanRows     int `json:"orphan_rows"`     // app.db 里 join 不上书目的行 —— book id 漂移
	PubdateSuspect int `json:"pubdate_suspect"` // 判为 mtime 兜底的书数
	PubdateUnknown int `json:"pubdate_unknown"` // 缺失或占位值
	Publishers     int `json:"publishers"`
	RunID          string
}

// Import 把 calibre 快照写进 readlist 自己的表:works 聚类 + publisher 归一 +
// editions + reading 只读镜像。单事务,并记一条 kind='snapshot' 的 run。
//
// works/editions/reading 全都是 calibre 的派生物,所以按快照做**全量替换**;
// evidence / labels / mentions / overrides 这些跨 run 复用或人工投入的表不动。
func Import(d *store.DB, snap *calibre.Snapshot, now time.Time) (ImportStats, error) {
	st := ImportStats{
		OrphanRows:     snap.Reading.Orphans,
		PubdateSuspect: snap.PubdateSuspect,
	}

	// 聚类:work 键 = 首作者姓氏 + 去版次后缀的规范标题(system-design §4 的回退键)。
	// OpenLibrary work id 这一级更优,但它要等 ingest 拿到外部数据才有。
	type workAgg struct {
		id       string
		title    string
		author   string
		topic    string
		halfLife HalfLife
		minBook  int
	}
	works := map[string]*workAgg{}
	bookWork := make(map[int]string, len(snap.Books))

	books := append([]calibre.Book(nil), snap.Books...)
	sort.Slice(books, func(i, j int) bool { return books[i].BookID < books[j].BookID })

	for _, b := range books {
		author := firstAuthor(b.Authors)
		wid := WorkKey(b.Title, author)
		bookWork[b.BookID] = wid
		hl := HalfLifeFor(b.Title, "", b.Tags)
		w, ok := works[wid]
		if !ok {
			works[wid] = &workAgg{id: wid, title: b.Title, author: author,
				topic: hl.Class, halfLife: hl, minBook: b.BookID}
			continue
		}
		// 同一 work 的多个版次:规范标题取 book_id 最小的那个(稳定),
		// 主题定档取"规则级别最高"的那次命中。
		if b.BookID < w.minBook {
			w.minBook, w.title = b.BookID, b.Title
		}
		if halfLifeRank(hl.Source) > halfLifeRank(w.halfLife.Source) {
			w.halfLife, w.topic = hl, hl.Class
		}
		if w.author == "" || strings.EqualFold(w.author, "unknown") {
			w.author = author
		}
	}

	tx, err := d.SQL().Begin()
	if err != nil {
		return st, err
	}
	defer tx.Rollback()

	// ---- 全量替换 editions / works ----
	// 先删掉快照里已经不存在的 edition,再 upsert。顺序反了会把刚写的删掉。
	keep := make(map[int]bool, len(books))
	for _, b := range books {
		keep[b.BookID] = true
	}
	existing, err := scanInts(tx, `SELECT book_id FROM editions`)
	if err != nil {
		return st, err
	}
	for _, id := range existing {
		if !keep[id] {
			if _, err := tx.Exec(`DELETE FROM editions WHERE book_id=?`, id); err != nil {
				return st, fmt.Errorf("删除失效 edition %d: %w", id, err)
			}
			st.DroppedBooks++
		}
	}

	workStmt, err := tx.Prepare(`INSERT INTO works
		(work_id, canonical_title, first_author, primary_topic, level, half_life_years, half_life_source)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(work_id) DO UPDATE SET
		  canonical_title=excluded.canonical_title, first_author=excluded.first_author,
		  primary_topic=excluded.primary_topic, half_life_years=excluded.half_life_years,
		  half_life_source=excluded.half_life_source`)
	if err != nil {
		return st, err
	}
	defer workStmt.Close()

	ids := make([]string, 0, len(works))
	for id := range works {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		w := works[id]
		// level 留空:层级要靠标注,快照阶段没有。按 level 过滤的榜因此暂时为空,
		// 这是诚实的结果,不该编一个值出来。
		if _, err := workStmt.Exec(w.id, w.title, w.author, w.topic, "",
			w.halfLife.Years, w.halfLife.Source); err != nil {
			return st, fmt.Errorf("写 work %s: %w", w.id, err)
		}
		st.Works++
	}

	edStmt, err := tx.Prepare(`INSERT INTO editions
		(book_id, work_id, title, isbn13, google_volume_id, publisher_raw, publisher_norm,
		 format, language, has_comments, has_cover, pubdate, pubdate_source, personal_rating_stars)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(book_id) DO UPDATE SET
		  work_id=excluded.work_id, title=excluded.title, isbn13=excluded.isbn13,
		  google_volume_id=excluded.google_volume_id, publisher_raw=excluded.publisher_raw,
		  publisher_norm=excluded.publisher_norm, format=excluded.format,
		  language=excluded.language, has_comments=excluded.has_comments,
		  has_cover=excluded.has_cover, pubdate=excluded.pubdate,
		  pubdate_source=excluded.pubdate_source,
		  personal_rating_stars=excluded.personal_rating_stars`)
	if err != nil {
		return st, err
	}
	defer edStmt.Close()

	pubStmt, err := tx.Prepare(
		`INSERT OR REPLACE INTO publisher_map (raw, norm, tier) VALUES (?,?,?)`)
	if err != nil {
		return st, err
	}
	defer pubStmt.Close()
	seenPublisher := map[string]bool{}

	for _, b := range books {
		pi := Publisher(b.Publisher)
		if b.Publisher != "" && !seenPublisher[b.Publisher] {
			seenPublisher[b.Publisher] = true
			if _, err := pubStmt.Exec(b.Publisher, pi.Norm, pi.Tier); err != nil {
				return st, fmt.Errorf("写 publisher_map %q: %w", b.Publisher, err)
			}
			st.Publishers++
		}
		if b.PubdateSource == calibre.SourceUnknown {
			st.PubdateUnknown++
		}
		if _, err := edStmt.Exec(b.BookID, bookWork[b.BookID], b.Title,
			nullable(b.ISBN13), nullable(b.GoogleID), nullable(b.Publisher), pi.Norm,
			bestFormat(b.Formats), b.Language, boolInt(b.HasComments), boolInt(b.HasCover),
			nullable(b.Pubdate), b.PubdateSource, nullableFloat(b.RatingStars)); err != nil {
			return st, fmt.Errorf("写 edition %d: %w", b.BookID, err)
		}
		st.Editions++
	}

	// 孤立的 work(所有版次都没了)一并清掉,否则它们会以零版次的形态留在目录里。
	if _, err := tx.Exec(
		`DELETE FROM works WHERE work_id NOT IN (SELECT work_id FROM editions)`); err != nil {
		return st, fmt.Errorf("清理孤立 work: %w", err)
	}

	// ---- reading 只读镜像:整表替换 ----
	if _, err := tx.Exec(`DELETE FROM reading`); err != nil {
		return st, err
	}
	readStmt, err := tx.Prepare(`INSERT INTO reading
		(book_id, status, shelves, downloads, last_modified) VALUES (?,?,?,?,?)`)
	if err != nil {
		return st, err
	}
	defer readStmt.Close()

	touched := map[int]bool{}
	for id := range snap.Reading.Status {
		touched[id] = true
	}
	for id := range snap.Reading.Shelves {
		touched[id] = true
	}
	for id := range snap.Reading.Downloads {
		touched[id] = true
	}
	readIDs := make([]int, 0, len(touched))
	for id := range touched {
		readIDs = append(readIDs, id)
	}
	sort.Ints(readIDs)
	stamp := now.Format(time.RFC3339Nano)
	for _, id := range readIDs {
		shelves := snap.Reading.Shelves[id]
		sort.Strings(shelves)
		payload, err := json.Marshal(shelves)
		if err != nil {
			return st, err
		}
		if _, err := readStmt.Exec(id, snap.Reading.Status[id], string(payload),
			snap.Reading.Downloads[id], stamp); err != nil {
			return st, fmt.Errorf("写 reading %d: %w", id, err)
		}
		st.ReadingRows++
	}

	// ---- 记一条 snapshot run,孤儿数与污染计数进监控 ----
	st.RunID = fmt.Sprintf("snap-%d", now.UnixNano())
	metrics, err := json.Marshal(st)
	if err != nil {
		return st, err
	}
	if _, err := tx.Exec(`INSERT INTO runs
		(run_id, kind, corpus_id, standard_version, facts_hash, started_at, ended_at,
		 status, ok_count, fail_count, orphan_rows, quota_used, metrics)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		st.RunID, "snapshot", "", "", "", stamp, stamp, "ok",
		st.Editions, 0, st.OrphanRows, "0", string(metrics)); err != nil {
		return st, fmt.Errorf("写 snapshot run: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return st, err
	}
	return st, nil
}

// halFLifeRank 规则级别:BISAC > 标注 > 标题关键词 > 默认(与 HalfLifeFor 的链一致)。
func halfLifeRank(source string) int {
	switch source {
	case "rules-bisac":
		return 4
	case "rules-topic-class":
		return 3
	case "rules-title-keyword":
		return 2
	default:
		return 1
	}
}

// firstAuthor 取首个作者;Unknown/Anonymous 原样保留 —— T 维靠它降级(实测 252 本)。
func firstAuthor(authors []string) string {
	for _, a := range authors {
		if a = strings.TrimSpace(a); a != "" {
			return a
		}
	}
	return "Unknown"
}

// bestFormat 一本书可能有多个格式,取最可读的那个(EPUB > AZW3/MOBI > PDF)。
func bestFormat(formats []string) string {
	best := ""
	for _, f := range formats {
		if FormatRank(f) > FormatRank(best) {
			best = strings.ToUpper(strings.TrimSpace(f))
		}
	}
	return best
}

func scanInts(tx *sql.Tx, query string) ([]int, error) {
	rows, err := tx.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func nullable(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}
