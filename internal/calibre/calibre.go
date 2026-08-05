// Package calibre 读取 calibre 的两个 SQLite 库,是 readlist 唯一接触书库的地方。
//
// 两个库性质完全不同(data-baseline §1/§3 实测),不能用同一种取法:
//
//	/calibre-library/metadata.db  PVC calibre-books-local          journal_mode=wal
//	/config/app.db                PVC calibre-web-...-config-local  journal_mode=delete
//
// metadata.db 是 WAL 且 calibre-web 一直在写 → 只读挂载直接查会失败(WAL 读也要写
// -shm),`?immutable=1` 也不安全。所以先 VACUUM INTO 出一份一致快照再查。
//
// ☠️ app.db 里有 user(含密码 hash)、user_session、oauthProvider(OIDC 凭据),
// **绝不整库快照**,只按 reading-status.md §4 的口径导出三张表所需的字段。
package calibre

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Book 一本 calibre 藏书的规范化投影。
type Book struct {
	BookID      int
	Title       string
	Authors     []string
	Publisher   string
	Formats     []string
	Language    string
	ISBN13      string
	GoogleID    string
	Tags        []string
	HasCover    bool
	HasComments bool
	// Pubdate 是 YYYY-MM-DD;PubdateSource 见 SourceCalibre/SourceMtimeFallback 的说明。
	Pubdate       string
	PubdateSource string
	// RatingStars 是**星(0–5)**,即 books_ratings_link.rating ÷ 2 —— calibre 存的是 0–10。
	// review M9:这个单位不写死必然踩坑。
	RatingStars float64
}

// Reading app.db 的最小导出(只读镜像,readlist 永不写回)。
type Reading struct {
	Status    map[int]string   // book_id → unread|read|reading
	Shelves   map[int][]string // book_id → 书架名
	Downloads map[int]int      // book_id → 下载次数(弱信号"打算读")
	Orphans   int              // join 不上 books 的行数 —— book id 漂移的唯一观测点
}

// Snapshot 一次快照的产物。
type Snapshot struct {
	Books   []Book
	Reading Reading
	// PubdateSuspect 被判为 mtime 兜底的书数(pubdate 与文件 last_modified 同一天)。
	PubdateSuspect int
}

// pubdate 来源标签。enum 见 store/migrations 的 editions.pubdate_source 注释。
const (
	// SourceCalibre —— 来自 calibre 且看不出是不是 mtime 兜底。**不可信**:
	// 实测 477 本(23%)的 pubdate 是 2026 年那次元数据补全用文件 mtime 填的,
	// 而错误值比缺失更危险,因为它不报警。ingest 拿到外部日期后会覆盖或交叉验证它。
	SourceCalibre = "calibre"
	// SourceMtimeFallback —— pubdate 与文件 last_modified 落在同一天,几乎必然是 mtime 兜底。
	SourceMtimeFallback = "mtime-fallback"
	// SourceUnknown —— 缺失或 calibre 的 '0101-01-01' 占位值(实测 5 本)。
	SourceUnknown = "unknown"
)

// Config 快照的输入。
type Config struct {
	MetadataDB  string // /calibre-library/metadata.db(WAL,需 RW 挂载)
	AppDB       string // /config/app.db(含凭据,只读、只导 3 表)
	SnapshotDir string // VACUUM INTO 的落点(readlist 自己的卷)
	UserID      int    // 只取库主人;Guest 与未来账号一律不参与(NFR-15)
}

// Load 产出一份一致快照。不发起任何网络请求。
func Load(cfg Config) (*Snapshot, error) {
	if cfg.MetadataDB == "" {
		return nil, fmt.Errorf("未配置 SOURCE_METADATA_DB")
	}
	if cfg.UserID == 0 {
		cfg.UserID = 1
	}
	snapPath, err := vacuumInto(cfg.MetadataDB, cfg.SnapshotDir)
	if err != nil {
		return nil, err
	}
	// 快照是静止的,此时 immutable=1 才是安全的(与 calibre 活库不同)。
	lib, err := sql.Open("sqlite", "file:"+snapPath+"?mode=ro&immutable=1")
	if err != nil {
		return nil, err
	}
	defer lib.Close()
	lib.SetMaxOpenConns(1)

	books, suspect, err := readBooks(lib)
	if err != nil {
		return nil, fmt.Errorf("读书目: %w", err)
	}
	snap := &Snapshot{Books: books, PubdateSuspect: suspect,
		Reading: Reading{Status: map[int]string{}, Shelves: map[int][]string{}, Downloads: map[int]int{}}}

	if cfg.AppDB != "" {
		known := make(map[int]bool, len(books))
		for _, b := range books {
			known[b.BookID] = true
		}
		if snap.Reading, err = readReading(cfg.AppDB, cfg.UserID, known); err != nil {
			return nil, fmt.Errorf("读阅读状态: %w", err)
		}
	}
	return snap, nil
}

// vacuumInto 对 WAL 库做整库一致快照。
//
// 目标文件**必须先删**:VACUUM INTO 在目标已存在时直接失败 —— 少了这一步,
// 第一夜正常、第二夜起静默不工作。
func vacuumInto(src, dir string) (string, error) {
	if dir == "" {
		dir = filepath.Dir(src)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, "metadata-snapshot.db")
	for _, p := range []string{dst, dst + "-wal", dst + "-shm"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("清理旧快照 %s: %w", p, err)
		}
	}
	// 源库以读写打开:WAL 的读路径也要写 -shm,只读挂载会得到
	// "attempt to write a readonly database"。能碰 calibre 卷的只有这个短命进程。
	db, err := sql.Open("sqlite", "file:"+src+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return "", err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`VACUUM INTO ?`, dst); err != nil {
		return "", fmt.Errorf("VACUUM INTO %s: %w", dst, err)
	}
	return dst, nil
}

func readBooks(lib *sql.DB) ([]Book, int, error) {
	// 一次查完主表 + 需要聚合的关联表。calibre 的 schema 见 data-baseline §1。
	rows, err := lib.Query(`
		SELECT b.id, b.title, b.pubdate, b.last_modified, b.has_cover,
		       COALESCE(p.name, ''), COALESCE(l.lang_code, ''),
		       (SELECT COUNT(*) FROM comments c WHERE c.book = b.id),
		       (SELECT r.rating FROM books_ratings_link brl
		          JOIN ratings r ON r.id = brl.rating WHERE brl.book = b.id LIMIT 1)
		FROM books b
		LEFT JOIN books_publishers_link bpl ON bpl.book = b.id
		LEFT JOIN publishers p ON p.id = bpl.publisher
		LEFT JOIN books_languages_link bll ON bll.book = b.id AND bll.item_order = 0
		LEFT JOIN languages l ON l.id = bll.lang_code
		GROUP BY b.id
		ORDER BY b.id`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	// 用 *Book 而不是切片元素的地址:append 扩容会搬迁底层数组,
	// 先前取到的 &books[i] 会指向旧数组,后面 collect 写进去的作者/格式/标签全丢。
	var books []*Book
	byID := map[int]*Book{}
	suspect := 0
	for rows.Next() {
		var (
			id                    int
			title                 string
			pubdate, lastModified sql.NullString
			hasCover              sql.NullBool
			publisher, language   string
			commentCount          int
			rating                sql.NullInt64
		)
		if err := rows.Scan(&id, &title, &pubdate, &lastModified, &hasCover,
			&publisher, &language, &commentCount, &rating); err != nil {
			return nil, 0, err
		}
		date, source := classifyPubdate(pubdate.String, lastModified.String)
		if source == SourceMtimeFallback {
			suspect++
		}
		b := &Book{
			BookID: id, Title: strings.TrimSpace(title), Publisher: strings.TrimSpace(publisher),
			Language: strings.TrimSpace(language), HasCover: hasCover.Valid && hasCover.Bool,
			HasComments: commentCount > 0, Pubdate: date, PubdateSource: source,
			RatingStars: float64(rating.Int64) / 2, // 0–10 → 星(0–5)
		}
		books = append(books, b)
		byID[id] = b
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	// 作者 / 格式 / 标签 / 标识符分别聚合,顺序稳定(ORDER BY 是可复现性要求)。
	if err := collect(lib, byID,
		`SELECT bal.book, a.name FROM books_authors_link bal
		   JOIN authors a ON a.id = bal.author ORDER BY bal.book, bal.id`,
		func(b *Book, v string) { b.Authors = append(b.Authors, v) }); err != nil {
		return nil, 0, err
	}
	if err := collect(lib, byID,
		`SELECT book, UPPER(format) FROM data ORDER BY book, format`,
		func(b *Book, v string) { b.Formats = append(b.Formats, v) }); err != nil {
		return nil, 0, err
	}
	if err := collect(lib, byID,
		`SELECT btl.book, t.name FROM books_tags_link btl
		   JOIN tags t ON t.id = btl.tag ORDER BY btl.book, t.name`,
		func(b *Book, v string) { b.Tags = append(b.Tags, v) }); err != nil {
		return nil, 0, err
	}
	// 标识符:只要 isbn 与 google,其余(asin/doi/pub-id…)对外部匹配无用。
	idRows, err := lib.Query(
		`SELECT book, LOWER(type), val FROM identifiers
		  WHERE LOWER(type) IN ('isbn','google') ORDER BY book, type`)
	if err != nil {
		return nil, 0, err
	}
	defer idRows.Close()
	for idRows.Next() {
		var book int
		var typ, val string
		if err := idRows.Scan(&book, &typ, &val); err != nil {
			return nil, 0, err
		}
		b, ok := byID[book]
		if !ok {
			continue
		}
		switch typ {
		case "isbn":
			if isbn := normalizeISBN13(val); isbn != "" && b.ISBN13 == "" {
				b.ISBN13 = isbn
			}
		case "google":
			if b.GoogleID == "" {
				b.GoogleID = strings.TrimSpace(val)
			}
		}
	}
	if err := idRows.Err(); err != nil {
		return nil, 0, err
	}
	out := make([]Book, 0, len(books))
	for _, b := range books {
		out = append(out, *b)
	}
	return out, suspect, nil
}

func collect(lib *sql.DB, byID map[int]*Book, query string, add func(*Book, string)) error {
	rows, err := lib.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var book int
		var val string
		if err := rows.Scan(&book, &val); err != nil {
			return err
		}
		if b, ok := byID[book]; ok {
			if v := strings.TrimSpace(val); v != "" {
				add(b, v)
			}
		}
	}
	return rows.Err()
}

// classifyPubdate 把 calibre 的 pubdate 归一成 YYYY-MM-DD + 来源判定。
//
// calibre 的 pubdate 默认值是 '0101-01-01'(实测 5 本仍是它)。更麻烦的是那 477 本
// 被 mtime 兜底填出来的日期 —— 它们与文件 last_modified 落在同一天,这是唯一
// 不用外部数据就能识别的特征,所以在这里就打上 mtime-fallback。
func classifyPubdate(pubdate, lastModified string) (string, string) {
	d := datePart(pubdate)
	if d == "" || strings.HasPrefix(d, "0101") {
		return "", SourceUnknown
	}
	if lm := datePart(lastModified); lm != "" && lm == d {
		return d, SourceMtimeFallback
	}
	return d, SourceCalibre
}

// datePart 从 calibre 的 TIMESTAMP 文本里取 YYYY-MM-DD;不合法则返回空。
func datePart(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 10 {
		return ""
	}
	d := s[:10]
	if _, err := time.Parse("2006-01-02", d); err != nil {
		return ""
	}
	return d
}

// normalizeISBN13 去掉连字符/空格,只接受 13 位。实测 715 本有 ISBN,其中
// isbn13 711、isbn10 1、异常长度 3 —— 后面两类直接丢,宁少不多。
func normalizeISBN13(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	if s := b.String(); len(s) == 13 {
		return s
	}
	return ""
}

// readReading 按 reading-status.md §4 的口径最小导出 app.db。
//
// 三条硬约束:只取 user_id = 库主人;join 书目丢掉孤儿行(实测 26 行里 2 行孤儿);
// 除这三张表的这几列之外**什么都不读** —— 同库里有密码 hash 与 OIDC 凭据。
func readReading(appPath string, userID int, known map[int]bool) (Reading, error) {
	out := Reading{Status: map[int]string{}, Shelves: map[int][]string{}, Downloads: map[int]int{}}
	// app.db 是 journal_mode=delete,只读打开是安全的。
	db, err := sql.Open("sqlite", "file:"+appPath+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return out, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	statusRows, err := db.Query(
		`SELECT book_id, read_status FROM book_read_link WHERE user_id = ? ORDER BY book_id`, userID)
	if err != nil {
		return out, err
	}
	defer statusRows.Close()
	for statusRows.Next() {
		var bookID, status int
		if err := statusRows.Scan(&bookID, &status); err != nil {
			return out, err
		}
		if !known[bookID] {
			out.Orphans++ // 孤儿数进 runs:突然上升 = 有人删书或重导入
			continue
		}
		if s := readStatusName(status); s != "" {
			out.Status[bookID] = s
		}
	}
	if err := statusRows.Err(); err != nil {
		return out, err
	}

	shelfRows, err := db.Query(`SELECT bs.book_id, s.name
		FROM shelf s JOIN book_shelf_link bs ON bs.shelf = s.id
		WHERE s.user_id = ? ORDER BY bs.book_id, s.name`, userID)
	if err != nil {
		return out, err
	}
	defer shelfRows.Close()
	for shelfRows.Next() {
		var bookID int
		var name string
		if err := shelfRows.Scan(&bookID, &name); err != nil {
			return out, err
		}
		if !known[bookID] {
			out.Orphans++
			continue
		}
		if name = strings.TrimSpace(name); name != "" {
			out.Shelves[bookID] = append(out.Shelves[bookID], name)
		}
	}
	if err := shelfRows.Err(); err != nil {
		return out, err
	}

	dlRows, err := db.Query(
		`SELECT book_id, COUNT(*) FROM downloads WHERE user_id = ? GROUP BY book_id ORDER BY book_id`, userID)
	if err != nil {
		return out, err
	}
	defer dlRows.Close()
	for dlRows.Next() {
		var bookID, n int
		if err := dlRows.Scan(&bookID, &n); err != nil {
			return out, err
		}
		if !known[bookID] {
			out.Orphans++
			continue
		}
		out.Downloads[bookID] = n
	}
	return out, dlRows.Err()
}

// readStatusName calibre-web 的枚举:0=未读 1=已读 2=在读。
func readStatusName(status int) string {
	switch status {
	case 1:
		return "read"
	case 2:
		return "reading"
	case 0:
		return "unread"
	default:
		return ""
	}
}
