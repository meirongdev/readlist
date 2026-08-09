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
	// PubdatePreserved 保住的外部 pubdate 数(ingest 写入的 google/openlibrary 日期,
	// 优先级高于本次快照的 calibre 值)。ingest 追平之后它应≈全部外部覆盖数;
	// 骤降到 0 说明覆写回归又回来了 —— F 维会在一天内跟着归零。
	PubdatePreserved int `json:"pubdate_preserved"`
	Publishers       int `json:"publishers"`
	// PublisherOverrides 命中人工归一表的版次数(publisher_map 里 source='manual' 的行)。
	PublisherOverrides int `json:"publisher_overrides"`
	RunID              string
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
	// 「全量替换」对 pubdate 与 google_volume_id 有一个例外:其余列都是 calibre 的
	// 派生物,但这两列可能已被 ingest 用外部证据升级过 —— 那是烧配额换来的,而且
	// ingest 对已缓存的书**不会**重写(缓存命中直接短路)。这里若无条件覆写,
	// 外部日期活不过下一次快照,F 维的实测覆盖每天归零(2026-08-08 实测:338 → 0)。
	// 所以先读出旧行,upsert 时按 PubdateSourcePriority 决定去留。
	prev, err := scanPrevEditions(tx)
	if err != nil {
		return st, err
	}

	// 先删掉快照里已经不存在的 edition,再 upsert。顺序反了会把刚写的删掉。
	keep := make(map[int]bool, len(books))
	for _, b := range books {
		keep[b.BookID] = true
	}
	for id := range prev {
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

	// 人工归一优先。内置规则表认不出的出版社变体(真实语料里必然有)只能靠这张表,
	// 而它此前是只写不读的(review B5)。
	manualPublishers, err := loadManualPublishers(tx)
	if err != nil {
		return st, err
	}
	// 规则行可以每夜刷新,人工行永不被覆盖 —— 这就是 WHERE source='rules' 的作用。
	pubStmt, err := tx.Prepare(`INSERT INTO publisher_map (raw, norm, tier, source)
		VALUES (?,?,?,'rules')
		ON CONFLICT(raw) DO UPDATE SET norm=excluded.norm, tier=excluded.tier
		WHERE publisher_map.source='rules'`)
	if err != nil {
		return st, err
	}
	defer pubStmt.Close()
	seenPublisher := map[string]bool{}

	for _, b := range books {
		pi := Publisher(b.Publisher)
		if ov, ok := manualPublishers[b.Publisher]; ok {
			pi = ov
			st.PublisherOverrides++
		}
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
		pd, pdSrc, gid := resolveExternalCarryOver(prev[b.BookID], &b, &st)
		if _, err := edStmt.Exec(b.BookID, bookWork[b.BookID], b.Title,
			nullable(b.ISBN13), nullable(gid), nullable(b.Publisher), pi.Norm,
			bestFormat(b.Formats), b.Language, boolInt(b.HasComments), boolInt(b.HasCover),
			nullable(pd), pdSrc, nullableFloat(b.RatingStars)); err != nil {
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

// loadManualPublishers 读出人工归一行(原始名 → 规范名 + tier)。
func loadManualPublishers(tx *sql.Tx) (map[string]PublisherInfo, error) {
	rows, err := tx.Query(
		`SELECT raw, norm, tier FROM publisher_map WHERE source='manual' ORDER BY raw`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]PublisherInfo{}
	for rows.Next() {
		var raw, norm string
		var tier int
		if err := rows.Scan(&raw, &norm, &tier); err != nil {
			return nil, err
		}
		out[raw] = PublisherInfo{Norm: norm, Tier: tier}
	}
	return out, rows.Err()
}

// prevEdition 上一次快照后该版次的存量行(外部证据去留判定用)。
type prevEdition struct {
	pubdate, pubdateSource, isbn13, googleID string
}

func scanPrevEditions(tx *sql.Tx) (map[int]prevEdition, error) {
	rows, err := tx.Query(`SELECT book_id, COALESCE(pubdate,''), COALESCE(pubdate_source,''),
		COALESCE(isbn13,''), COALESCE(google_volume_id,'') FROM editions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]prevEdition{}
	for rows.Next() {
		var id int
		var p prevEdition
		if err := rows.Scan(&id, &p.pubdate, &p.pubdateSource, &p.isbn13, &p.googleID); err != nil {
			return nil, err
		}
		out[id] = p
	}
	return out, rows.Err()
}

// resolveExternalCarryOver 决定本次快照写入的 pubdate/pubdate_source/google_volume_id:
// 默认取快照(calibre)值;当旧行的 pubdate 来源优先级**更高**(google/openlibrary
// 是 ingest 烧配额查回来的)时保留旧值,同优先级取快照值(跟进 calibre 自己的修订)。
// ingest 写回的 volume id 同理保留 —— 它让后续摄入能直取 volume、评分行能解析回 work。
//
// 唯一不保留的情形:版次的外部标识变了(ISBN,或无 ISBN 时的 google id)。
// 那说明这本书被重新识别过 —— 旧外部证据是按旧标识查的,出处已失效,宁可重查。
func resolveExternalCarryOver(prev prevEdition, b *calibre.Book, st *ImportStats) (pubdate, source, googleID string) {
	pubdate, source, googleID = b.Pubdate, b.PubdateSource, strings.TrimSpace(b.GoogleID)
	isbn := strings.TrimSpace(b.ISBN13)
	sameIdentity := prev.isbn13 == isbn
	if isbn == "" {
		sameIdentity = sameIdentity && prev.googleID == googleID
	}
	if !sameIdentity {
		return pubdate, source, googleID
	}
	if PubdateSourcePriority[prev.pubdateSource] > PubdateSourcePriority[source] {
		pubdate, source = prev.pubdate, prev.pubdateSource
		st.PubdatePreserved++
	}
	if googleID == "" {
		googleID = prev.googleID
	}
	return pubdate, source, googleID
}

func nullable(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}
