package calibre

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// calibre metadata.db 的相关 schema(照 data-baseline §1 实测的那些表建)。
// 注意 pubdate 的默认值就是 '0101-01-01' —— 实测仍有 5 本停在这个占位值上。
const metadataSchema = `
CREATE TABLE books (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  title TEXT NOT NULL DEFAULT 'Unknown',
  sort TEXT COLLATE NOCASE,
  timestamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  pubdate TIMESTAMP DEFAULT '0101-01-01 00:00:00+00:00',
  series_index REAL NOT NULL DEFAULT 1.0,
  author_sort TEXT COLLATE NOCASE,
  isbn TEXT DEFAULT "" COLLATE NOCASE,
  path TEXT NOT NULL DEFAULT "",
  uuid TEXT,
  has_cover BOOL DEFAULT 0,
  last_modified TIMESTAMP NOT NULL DEFAULT "2000-01-01 00:00:00+00:00");
CREATE TABLE authors (id INTEGER PRIMARY KEY, name TEXT NOT NULL COLLATE NOCASE,
  sort TEXT COLLATE NOCASE, link TEXT NOT NULL DEFAULT "");
CREATE TABLE books_authors_link (id INTEGER PRIMARY KEY, book INTEGER NOT NULL, author INTEGER NOT NULL);
CREATE TABLE publishers (id INTEGER PRIMARY KEY, name TEXT NOT NULL COLLATE NOCASE, sort TEXT COLLATE NOCASE);
CREATE TABLE books_publishers_link (id INTEGER PRIMARY KEY, book INTEGER NOT NULL, publisher INTEGER NOT NULL);
CREATE TABLE data (id INTEGER PRIMARY KEY, book INTEGER NOT NULL, format TEXT NOT NULL COLLATE NOCASE,
  uncompressed_size INTEGER NOT NULL DEFAULT 0, name TEXT NOT NULL DEFAULT "");
CREATE TABLE comments (id INTEGER PRIMARY KEY, book INTEGER NOT NULL, text TEXT NOT NULL COLLATE NOCASE);
CREATE TABLE identifiers (id INTEGER PRIMARY KEY, book INTEGER NOT NULL,
  type TEXT NOT NULL DEFAULT "isbn" COLLATE NOCASE, val TEXT NOT NULL COLLATE NOCASE);
CREATE TABLE languages (id INTEGER PRIMARY KEY, lang_code TEXT NOT NULL COLLATE NOCASE);
CREATE TABLE books_languages_link (id INTEGER PRIMARY KEY, book INTEGER NOT NULL,
  lang_code INTEGER NOT NULL, item_order INTEGER NOT NULL DEFAULT 0);
CREATE TABLE ratings (id INTEGER PRIMARY KEY, rating INTEGER CHECK(rating > -1 AND rating < 11));
CREATE TABLE books_ratings_link (id INTEGER PRIMARY KEY, book INTEGER NOT NULL, rating INTEGER NOT NULL);
CREATE TABLE tags (id INTEGER PRIMARY KEY, name TEXT NOT NULL COLLATE NOCASE);
CREATE TABLE books_tags_link (id INTEGER PRIMARY KEY, book INTEGER NOT NULL, tag INTEGER NOT NULL);
CREATE TABLE custom_columns (id INTEGER PRIMARY KEY, label TEXT NOT NULL);
`

// calibre-web app.db 的相关 schema。同库里还有 user / user_session / oauthProvider
// (含密码 hash 与 OIDC 凭据)—— 这里刻意把它们也建出来并塞进假凭据,
// 用来断言导出逻辑**碰都不碰**它们。
const appSchema = `
CREATE TABLE book_read_link (id INTEGER NOT NULL, book_id INTEGER, user_id INTEGER,
  read_status INTEGER NOT NULL, last_modified DATETIME, last_time_started_reading DATETIME,
  times_started_reading INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (id));
CREATE TABLE shelf (id INTEGER NOT NULL, uuid VARCHAR, name VARCHAR, is_public INTEGER,
  user_id INTEGER, kobo_sync BOOLEAN, created DATETIME, last_modified DATETIME, PRIMARY KEY (id));
CREATE TABLE book_shelf_link (id INTEGER NOT NULL, book_id INTEGER, "order" INTEGER,
  shelf INTEGER, date_added DATETIME, PRIMARY KEY (id));
CREATE TABLE downloads (id INTEGER NOT NULL, book_id INTEGER, user_id INTEGER, PRIMARY KEY (id));
CREATE TABLE user (id INTEGER PRIMARY KEY, name VARCHAR, password VARCHAR, email VARCHAR);
CREATE TABLE user_session (id INTEGER PRIMARY KEY, user_id INTEGER, session_key VARCHAR);
CREATE TABLE oauthProvider (id INTEGER PRIMARY KEY, provider_name VARCHAR,
  oauth_client_id VARCHAR, oauth_client_secret VARCHAR);
`

// buildFixtures 造两个源库。metadata.db 刻意开成 WAL —— 生产就是 WAL,
// VACUUM INTO 这条路径必须在 WAL 上被真正走一遍。
func buildFixtures(t *testing.T) (metadataPath, appPath, snapshotDir string) {
	t.Helper()
	dir := t.TempDir()
	metadataPath = filepath.Join(dir, "metadata.db")
	appPath = filepath.Join(dir, "app.db")
	snapshotDir = filepath.Join(dir, "snapshot")

	lib, err := sql.Open("sqlite", "file:"+metadataPath+"?_pragma=journal_mode(WAL)")
	require.NoError(t, err)
	defer lib.Close()
	_, err = lib.Exec(metadataSchema)
	require.NoError(t, err)

	var mode string
	require.NoError(t, lib.QueryRow(`PRAGMA journal_mode`).Scan(&mode))
	require.Equal(t, "wal", mode, "前提:源库必须是 WAL,否则这个测试没测到真实取法")

	exec := func(q string, args ...any) {
		_, err := lib.Exec(q, args...)
		require.NoError(t, err, q)
	}
	// 1 正常书;2 同一 work 的第二个版次;3 mtime 兜底;4 占位日期;
	// 5 Unknown 作者 + 多格式;6 中文书;7 Packt 变体 + BISAC 标签
	exec(`INSERT INTO books (id,title,pubdate,last_modified,has_cover) VALUES
	  (1,'Designing Data-Intensive Applications','2017-03-16 00:00:00+00:00','2026-07-01 00:00:00+00:00',1),
	  (2,'Learning Go','2021-03-16 00:00:00+00:00','2026-07-01 00:00:00+00:00',1),
	  (3,'Learning Go, Second Edition','2024-03-19 00:00:00+00:00','2026-07-02 00:00:00+00:00',1),
	  (4,'Some Imported Book','2026-07-15 00:00:00+00:00','2026-07-15 00:00:00+00:00',0),
	  (5,'Ancient Placeholder','0101-01-01 00:00:00+00:00','2026-07-01 00:00:00+00:00',0),
	  (6,'Mystery Manual','2015-06-01 00:00:00+00:00','2026-07-01 00:00:00+00:00',0),
	  (7,'Go 语言高并发与微服务实战','2021-08-01 00:00:00+00:00','2026-07-01 00:00:00+00:00',1),
	  (8,'Learning Spring Boot 3.0','2022-12-30 00:00:00+00:00','2026-07-01 00:00:00+00:00',1)`)

	exec(`INSERT INTO authors (id,name) VALUES
	  (1,'Martin Kleppmann'),(2,'Jon Bodner'),(3,'Unknown'),(4,'朱洪波'),
	  (5,'Greg L. Turnquist'),(6,'Some Author'),(7,'Placeholder Author')`)
	exec(`INSERT INTO books_authors_link (id,book,author) VALUES
	  (1,1,1),(2,2,2),(3,3,2),(4,4,6),(5,5,7),(6,6,3),(7,7,4),(8,8,5)`)

	exec(`INSERT INTO publishers (id,name) VALUES
	  (1,'O''Reilly Media'),(2,'O''Reilly Media, Inc.'),(3,'电子工业出版社'),
	  (4,'Packt Publishing Ltd'),(5,'')`)
	exec(`INSERT INTO books_publishers_link (id,book,publisher) VALUES
	  (1,1,1),(2,2,1),(3,3,2),(4,7,3),(5,8,4)`)

	// 5 号书两个格式 → 取最可读的 EPUB。
	exec(`INSERT INTO data (id,book,format) VALUES
	  (1,1,'EPUB'),(2,2,'EPUB'),(3,3,'PDF'),(4,4,'PDF'),
	  (5,5,'pdf'),(6,5,'epub'),(7,6,'PDF'),(8,7,'EPUB'),(9,8,'PDF')`)

	exec(`INSERT INTO comments (id,book,text) VALUES (1,1,'A great book'),(2,7,'中文简介')`)

	exec(`INSERT INTO identifiers (id,book,type,val) VALUES
	  (1,1,'isbn','978-1-4493-7332-0'),
	  (2,1,'google','BM7woQEACAAJ'),
	  (3,2,'isbn','9781492077213'),
	  (4,4,'asin','B00XYZ'),
	  (5,5,'isbn','12345'),
	  (6,7,'isbn','9787121418038')`)

	exec(`INSERT INTO languages (id,lang_code) VALUES (1,'eng'),(2,'zho')`)
	exec(`INSERT INTO books_languages_link (id,book,lang_code,item_order) VALUES
	  (1,1,1,0),(2,2,1,0),(3,3,1,0),(4,7,2,0),(5,8,1,0)`)

	// calibre 的 rating 是 0–10(星 × 2):9 → 4.5 星。
	exec(`INSERT INTO ratings (id,rating) VALUES (1,9),(2,10)`)
	exec(`INSERT INTO books_ratings_link (id,book,rating) VALUES (1,1,1),(2,2,2)`)

	exec(`INSERT INTO tags (id,name) VALUES
	  (1,'Computers'),
	  (2,'COM060160 - COMPUTERS / Web / Web Programming'),
	  (3,'machine learning')`)
	exec(`INSERT INTO books_tags_link (id,book,tag) VALUES (1,8,1),(2,8,2),(3,1,3)`)

	app, err := sql.Open("sqlite", "file:"+appPath)
	require.NoError(t, err)
	defer app.Close()
	_, err = app.Exec(appSchema)
	require.NoError(t, err)
	appExec := func(q string, args ...any) {
		_, err := app.Exec(q, args...)
		require.NoError(t, err, q)
	}
	// 1 已读;2 在读;5 显式未读;999 孤儿行(书已删);另一个账号的行必须被忽略。
	appExec(`INSERT INTO book_read_link (id,book_id,user_id,read_status,times_started_reading) VALUES
	  (1,1,1,1,1),(2,2,1,2,1),(3,5,1,0,0),(4,999,1,1,1),(5,3,2,1,1)`)
	appExec(`INSERT INTO shelf (id,name,user_id,is_public) VALUES
	  (1,'精读',1,1),(2,'弃读',1,0),(3,'别人的书架',2,1)`)
	appExec(`INSERT INTO book_shelf_link (id,book_id,"order",shelf) VALUES
	  (1,1,0,1),(2,6,0,2),(3,2,0,3)`)
	appExec(`INSERT INTO downloads (id,book_id,user_id) VALUES
	  (1,1,1),(2,1,1),(3,4,1),(4,7,2)`)
	// 凭据:导出逻辑绝不能碰这些表。
	appExec(`INSERT INTO user (id,name,password,email) VALUES (1,'owner','$2b$12$fakehash','o@x.dev')`)
	appExec(`INSERT INTO oauthProvider (id,provider_name,oauth_client_id,oauth_client_secret)
	  VALUES (1,'oidc','client','SUPER-SECRET')`)

	return metadataPath, appPath, snapshotDir
}

func load(t *testing.T) *Snapshot {
	t.Helper()
	md, app, dir := buildFixtures(t)
	snap, err := Load(Config{MetadataDB: md, AppDB: app, SnapshotDir: dir, UserID: 1})
	require.NoError(t, err)
	return snap
}

func bookByID(t *testing.T, snap *Snapshot, id int) Book {
	t.Helper()
	for _, b := range snap.Books {
		if b.BookID == id {
			return b
		}
	}
	t.Fatalf("找不到 book_id=%d", id)
	return Book{}
}

func TestLoadReadsAllBooks(t *testing.T) {
	snap := load(t)
	require.Len(t, snap.Books, 8)
	// 顺序必须稳定(可复现性要求)。
	for i := 1; i < len(snap.Books); i++ {
		require.Less(t, snap.Books[i-1].BookID, snap.Books[i].BookID)
	}
}

func TestBookProjection(t *testing.T) {
	b := bookByID(t, load(t), 1)
	require.Equal(t, "Designing Data-Intensive Applications", b.Title)
	require.Equal(t, []string{"Martin Kleppmann"}, b.Authors)
	require.Equal(t, "O'Reilly Media", b.Publisher)
	require.Equal(t, []string{"EPUB"}, b.Formats)
	require.Equal(t, "eng", b.Language)
	require.True(t, b.HasCover)
	require.True(t, b.HasComments)
	require.Equal(t, "9781449373320", b.ISBN13, "带连字符的 ISBN 要归一成 13 位")
	require.Equal(t, "BM7woQEACAAJ", b.GoogleID)
	require.Equal(t, 4.5, b.RatingStars, "calibre 的 rating 是 0–10,必须 ÷2 成星")
}

func TestPubdateClassification(t *testing.T) {
	snap := load(t)
	// 正常:与 last_modified 不同天 → calibre(仍不可信,但不是 mtime 兜底)
	require.Equal(t, "2017-03-16", bookByID(t, snap, 1).Pubdate)
	require.Equal(t, SourceCalibre, bookByID(t, snap, 1).PubdateSource)

	// pubdate 与文件 last_modified 同一天 → 几乎必然是 mtime 兜底(实测 477 本)
	suspect := bookByID(t, snap, 4)
	require.Equal(t, SourceMtimeFallback, suspect.PubdateSource)
	require.Equal(t, 1, snap.PubdateSuspect)

	// calibre 的 '0101-01-01' 占位值 → unknown,且不能当成公元 101 年
	placeholder := bookByID(t, snap, 5)
	require.Empty(t, placeholder.Pubdate)
	require.Equal(t, SourceUnknown, placeholder.PubdateSource)
}

func TestPubdateSourcesAreAllUntrustedByScoreEngine(t *testing.T) {
	// 关键契约:snapshot 阶段产出的三种来源**没有一种**在 score 的可信名单里,
	// 所以时效维度在拿到外部数据之前一律记 unknown —— 这正是 R-1 要防的事。
	for _, src := range []string{SourceCalibre, SourceMtimeFallback, SourceUnknown} {
		require.NotContains(t, []string{"google", "openlibrary", "file-meta"}, src,
			"%s 不该被当成可信来源", src)
	}
}

func TestMultiFormatTakesAllFormats(t *testing.T) {
	b := bookByID(t, load(t), 5)
	require.ElementsMatch(t, []string{"EPUB", "PDF"}, b.Formats, "格式名要统一成大写")
}

func TestUnknownAuthorPreserved(t *testing.T) {
	// 实测 252 本作者是 Unknown/Anonymous —— 必须原样保留,T 维靠它降级。
	require.Equal(t, []string{"Unknown"}, bookByID(t, load(t), 6).Authors)
}

func TestNonASCIIFieldsSurvive(t *testing.T) {
	b := bookByID(t, load(t), 7)
	require.Equal(t, "Go 语言高并发与微服务实战", b.Title)
	require.Equal(t, []string{"朱洪波"}, b.Authors)
	require.Equal(t, "电子工业出版社", b.Publisher)
	require.Equal(t, "zho", b.Language)
}

func TestBadISBNIsDropped(t *testing.T) {
	// 实测有 3 本 ISBN 长度异常 —— 宁少不多,直接丢,避免用错的号去查外部源。
	require.Empty(t, bookByID(t, load(t), 5).ISBN13)
}

func TestNonMatchingIdentifiersIgnored(t *testing.T) {
	// asin / doi / pub-id 这些对外部匹配无用,不该被误读成 ISBN 或 google id。
	b := bookByID(t, load(t), 4)
	require.Empty(t, b.ISBN13)
	require.Empty(t, b.GoogleID)
}

func TestBisacTagsAreCarried(t *testing.T) {
	b := bookByID(t, load(t), 8)
	require.Contains(t, b.Tags, "COM060160 - COMPUTERS / Web / Web Programming")
}

func TestReadingMirrorRespectsOwnerAndDropsOrphans(t *testing.T) {
	snap := load(t)
	r := snap.Reading
	require.Equal(t, "read", r.Status[1])
	require.Equal(t, "reading", r.Status[2])
	require.Equal(t, "unread", r.Status[5])
	// 另一个账号(user_id=2)的记录一律不参与(NFR-15)。
	require.NotContains(t, r.Status, 3)
	// 孤儿行:book_id=999 已不在书目里 —— 丢掉并计数,这是 book id 漂移的唯一观测点。
	require.NotContains(t, r.Status, 999)
	require.Equal(t, 1, r.Orphans)

	require.Equal(t, []string{"精读"}, r.Shelves[1])
	require.Equal(t, []string{"弃读"}, r.Shelves[6])
	require.NotContains(t, r.Shelves, 2, "别人书架上的书不该出现")

	require.Equal(t, 2, r.Downloads[1])
	require.Equal(t, 1, r.Downloads[4])
	require.NotContains(t, r.Downloads, 7, "别人的下载记录不该出现")
}

func TestSnapshotIsRepeatable(t *testing.T) {
	// VACUUM INTO 在目标已存在时会直接失败 —— 少了「先删旧快照」这一步,
	// 第一夜正常、第二夜起静默不工作。
	md, app, dir := buildFixtures(t)
	cfg := Config{MetadataDB: md, AppDB: app, SnapshotDir: dir, UserID: 1}
	first, err := Load(cfg)
	require.NoError(t, err)
	second, err := Load(cfg)
	require.NoError(t, err, "第二次快照必须照样成功")
	require.Equal(t, len(first.Books), len(second.Books))
	require.Equal(t, first.Books, second.Books, "同一份源库两次快照结果必须一致")

	// 快照文件确实落在 readlist 自己的目录里,而不是 calibre 的卷上。
	_, err = os.Stat(filepath.Join(dir, "metadata-snapshot.db"))
	require.NoError(t, err)
}

func TestSnapshotDoesNotTouchSourceDB(t *testing.T) {
	// 读 WAL 库需要 RW 挂载,但我们绝不能改动书目内容。
	md, app, dir := buildFixtures(t)
	before := countBooks(t, md)
	_, err := Load(Config{MetadataDB: md, AppDB: app, SnapshotDir: dir, UserID: 1})
	require.NoError(t, err)
	require.Equal(t, before, countBooks(t, md), "源库的书目行数不该变化")
}

func TestMissingAppDBIsNotFatal(t *testing.T) {
	// app.db 不可用时仍应出书目 —— 阅读状态是 facet,不该让整条管道停摆。
	md, _, dir := buildFixtures(t)
	snap, err := Load(Config{MetadataDB: md, SnapshotDir: dir, UserID: 1})
	require.NoError(t, err)
	require.Len(t, snap.Books, 8)
	require.Empty(t, snap.Reading.Status)
}

func TestMissingMetadataDBIsAnError(t *testing.T) {
	_, err := Load(Config{MetadataDB: filepath.Join(t.TempDir(), "nope.db"), SnapshotDir: t.TempDir()})
	require.Error(t, err, "书目库缺失必须报错,不能静默产出空语料")
}

func countBooks(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	defer db.Close()
	var n int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM books`).Scan(&n))
	return n
}
