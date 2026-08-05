package corpus_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/meirongdev/readlist/internal/calibre"
	"github.com/meirongdev/readlist/internal/corpus"
	"github.com/meirongdev/readlist/internal/preset"
	"github.com/meirongdev/readlist/internal/score"
	"github.com/meirongdev/readlist/internal/store"
)

var importNow = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

func openDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/import.db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// fixtureSnapshot 一份覆盖真实语料各类形态的快照(数值取自 data-baseline 的实测特征)。
func fixtureSnapshot() *calibre.Snapshot {
	return &calibre.Snapshot{
		PubdateSuspect: 1,
		Books: []calibre.Book{
			{BookID: 1, Title: "Designing Data-Intensive Applications",
				Authors: []string{"Martin Kleppmann"}, Publisher: "O'Reilly Media",
				Formats: []string{"EPUB"}, Language: "eng", ISBN13: "9781449373320",
				GoogleID: "BM7woQEACAAJ", HasCover: true, HasComments: true,
				Pubdate: "2017-03-16", PubdateSource: calibre.SourceCalibre, RatingStars: 4.5},
			// 同一 work 的两个版次:必须聚成一个 work、两个 edition。
			{BookID: 2, Title: "Learning Go", Authors: []string{"Jon Bodner"},
				Publisher: "O'Reilly Media", Formats: []string{"PDF"}, Language: "eng",
				ISBN13: "9781492077213", HasCover: true,
				Pubdate: "2021-03-16", PubdateSource: calibre.SourceCalibre},
			{BookID: 3, Title: "Learning Go, Second Edition", Authors: []string{"Jon Bodner"},
				Publisher: "O'Reilly Media, Inc.", Formats: []string{"EPUB", "PDF"}, Language: "eng",
				HasCover: true, Pubdate: "2024-03-19", PubdateSource: calibre.SourceCalibre},
			// mtime 兜底污染。
			{BookID: 4, Title: "Some Imported Book", Authors: []string{"Some Author"},
				Publisher: "Packt Publishing Ltd", Formats: []string{"PDF"}, Language: "eng",
				Pubdate: "2026-07-15", PubdateSource: calibre.SourceMtimeFallback},
			// 占位日期 + 作者未知。
			{BookID: 5, Title: "Mystery Manual", Authors: []string{"Unknown"},
				Formats: []string{"PDF"}, PubdateSource: calibre.SourceUnknown},
			// 中文书 + 中文出版社。
			{BookID: 6, Title: "Go 语言高并发与微服务实战", Authors: []string{"朱洪波"},
				Publisher: "电子工业出版社", Formats: []string{"EPUB"}, Language: "zho",
				ISBN13: "9787121418038", HasCover: true, HasComments: true,
				Pubdate: "2021-08-01", PubdateSource: calibre.SourceCalibre},
			// BISAC 码定档。
			{BookID: 7, Title: "Learning Spring Boot 3.0", Authors: []string{"Greg L. Turnquist"},
				Publisher: "Packt Publishing", Formats: []string{"PDF"}, Language: "eng",
				Tags:    []string{"Computers", "COM060160 - COMPUTERS / Web / Web Programming"},
				Pubdate: "2022-12-30", PubdateSource: calibre.SourceCalibre},
		},
		Reading: calibre.Reading{
			Status:    map[int]string{1: "read", 2: "reading"},
			Shelves:   map[int][]string{1: {"精读"}, 5: {"弃读"}},
			Downloads: map[int]int{1: 2, 4: 1},
			Orphans:   2,
		},
	}
}

func TestImportClustersEditionsIntoWorks(t *testing.T) {
	db := openDB(t)
	st, err := corpus.Import(db, fixtureSnapshot(), importNow)
	require.NoError(t, err)

	require.Equal(t, 7, st.Editions)
	require.Equal(t, 6, st.Works, "Learning Go 的两个版次应聚成一个 work")

	var editions int
	require.NoError(t, db.SQL().QueryRow(
		`SELECT COUNT(*) FROM editions WHERE work_id=(SELECT work_id FROM editions WHERE book_id=2)`).
		Scan(&editions))
	require.Equal(t, 2, editions)

	// 规范标题取 book_id 最小的那个版次,保证跨 run 稳定。
	var title string
	require.NoError(t, db.SQL().QueryRow(
		`SELECT canonical_title FROM works WHERE work_id=(SELECT work_id FROM editions WHERE book_id=2)`).
		Scan(&title))
	require.Equal(t, "Learning Go", title)
}

func TestImportKeepsChineseWorksSeparate(t *testing.T) {
	db := openDB(t)
	_, err := corpus.Import(db, fixtureSnapshot(), importNow)
	require.NoError(t, err)
	var wid, topic string
	require.NoError(t, db.SQL().QueryRow(
		`SELECT work_id, COALESCE(primary_topic,'') FROM editions e
		   JOIN works w USING(work_id) WHERE e.book_id=6`).Scan(&wid, &topic))
	require.Contains(t, wid, "语言高并发与微服务实战", "中文标题不能被归一成空")
	_ = topic
}

func TestImportNormalizesPublishers(t *testing.T) {
	db := openDB(t)
	st, err := corpus.Import(db, fixtureSnapshot(), importNow)
	require.NoError(t, err)
	require.Positive(t, st.Publishers)

	rows, err := db.SQL().Query(`SELECT raw, norm, tier FROM publisher_map ORDER BY raw`)
	require.NoError(t, err)
	defer rows.Close()
	got := map[string]string{}
	tiers := map[string]int{}
	for rows.Next() {
		var raw, norm string
		var tier int
		require.NoError(t, rows.Scan(&raw, &norm, &tier))
		got[raw] = norm
		tiers[raw] = tier
	}
	// O'Reilly 的两个变体、Packt 的两个变体各自归一到同一个规范名。
	require.Equal(t, "O'Reilly Media", got["O'Reilly Media"])
	require.Equal(t, "O'Reilly Media", got["O'Reilly Media, Inc."])
	require.Equal(t, "Packt", got["Packt Publishing Ltd"])
	require.Equal(t, "Packt", got["Packt Publishing"])
	// 中文出版社不能掉进 tier 4。
	require.Equal(t, "电子工业出版社", got["电子工业出版社"])
	require.Equal(t, 3, tiers["电子工业出版社"])
}

func TestImportTakesBestFormatPerEdition(t *testing.T) {
	db := openDB(t)
	_, err := corpus.Import(db, fixtureSnapshot(), importNow)
	require.NoError(t, err)
	var format string
	require.NoError(t, db.SQL().QueryRow(
		`SELECT format FROM editions WHERE book_id=3`).Scan(&format))
	require.Equal(t, "EPUB", format, "同一版次多格式时取最可读的")
}

func TestImportUsesBisacForTopicAndHalfLife(t *testing.T) {
	db := openDB(t)
	_, err := corpus.Import(db, fixtureSnapshot(), importNow)
	require.NoError(t, err)
	var topic, source string
	var years float64
	require.NoError(t, db.SQL().QueryRow(
		`SELECT w.primary_topic, w.half_life_source, w.half_life_years
		   FROM works w JOIN editions e USING(work_id) WHERE e.book_id=7`).
		Scan(&topic, &source, &years))
	require.Equal(t, "框架/版本", topic)
	require.Equal(t, "rules-bisac", source)
	require.Equal(t, 2.5, years)
}

func TestImportMirrorsReadingAndCountsOrphans(t *testing.T) {
	db := openDB(t)
	st, err := corpus.Import(db, fixtureSnapshot(), importNow)
	require.NoError(t, err)
	require.Equal(t, 2, st.OrphanRows)
	require.Equal(t, 4, st.ReadingRows) // book 1,2,4,5

	var status, shelves string
	var downloads int
	require.NoError(t, db.SQL().QueryRow(
		`SELECT status, shelves, downloads FROM reading WHERE book_id=1`).
		Scan(&status, &shelves, &downloads))
	require.Equal(t, "read", status)
	require.Equal(t, 2, downloads)
	var parsed []string
	require.NoError(t, json.Unmarshal([]byte(shelves), &parsed))
	require.Equal(t, []string{"精读"}, parsed)
}

func TestImportIsIdempotent(t *testing.T) {
	db := openDB(t)
	first, err := corpus.Import(db, fixtureSnapshot(), importNow)
	require.NoError(t, err)
	second, err := corpus.Import(db, fixtureSnapshot(), importNow.Add(time.Hour))
	require.NoError(t, err)

	require.Equal(t, first.Works, second.Works)
	require.Equal(t, first.Editions, second.Editions)
	require.Zero(t, second.DroppedBooks)

	count := func(q string) int {
		var n int
		require.NoError(t, db.SQL().QueryRow(q).Scan(&n))
		return n
	}
	require.Equal(t, 7, count(`SELECT COUNT(*) FROM editions`), "重复导入不应产生重复行")
	require.Equal(t, 6, count(`SELECT COUNT(*) FROM works`))
	require.Equal(t, 4, count(`SELECT COUNT(*) FROM reading`), "阅读镜像是整表替换")
}

func TestImportDropsBooksRemovedFromLibrary(t *testing.T) {
	db := openDB(t)
	_, err := corpus.Import(db, fixtureSnapshot(), importNow)
	require.NoError(t, err)

	// 库主人删掉了 Learning Go 的第二版和那本中文书。
	shrunk := fixtureSnapshot()
	var kept []calibre.Book
	for _, b := range shrunk.Books {
		if b.BookID != 3 && b.BookID != 6 {
			kept = append(kept, b)
		}
	}
	shrunk.Books = kept

	st, err := corpus.Import(db, shrunk, importNow.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, 2, st.DroppedBooks)

	count := func(q string) int {
		var n int
		require.NoError(t, db.SQL().QueryRow(q).Scan(&n))
		return n
	}
	require.Equal(t, 5, count(`SELECT COUNT(*) FROM editions`))
	// 中文书的 work 所有版次都没了 → work 也要清掉,否则目录里会留下零版次的空壳。
	require.Equal(t, 5, count(`SELECT COUNT(*) FROM works`))
	require.Zero(t, count(`SELECT COUNT(*) FROM works WHERE work_id NOT IN (SELECT work_id FROM editions)`))
	// Learning Go 的 work 还在(第一版仍在库里)。
	require.Equal(t, 1, count(`SELECT COUNT(*) FROM editions WHERE book_id=2`))
}

func TestImportRecordsSnapshotRun(t *testing.T) {
	db := openDB(t)
	st, err := corpus.Import(db, fixtureSnapshot(), importNow)
	require.NoError(t, err)
	require.NotEmpty(t, st.RunID)

	var kind, metrics string
	var orphans int
	require.NoError(t, db.SQL().QueryRow(
		`SELECT kind, orphan_rows, metrics FROM runs WHERE run_id=?`, st.RunID).
		Scan(&kind, &orphans, &metrics))
	require.Equal(t, "snapshot", kind)
	require.Equal(t, 2, orphans, "孤儿行数必须进 runs —— 这是 book id 漂移的唯一警报")

	var parsed corpus.ImportStats
	require.NoError(t, json.Unmarshal([]byte(metrics), &parsed))
	require.Equal(t, 1, parsed.PubdateSuspect)
	require.Equal(t, 1, parsed.PubdateUnknown)
}

// TestSnapshotDataScoresEndToEnd 证明真实语料形态的数据能跑通整条管道。
func TestSnapshotDataScoresEndToEnd(t *testing.T) {
	db := openDB(t)
	_, err := corpus.Import(db, fixtureSnapshot(), importNow)
	require.NoError(t, err)

	presets, err := preset.Load()
	require.NoError(t, err)
	res, err := score.NewEngine(db, "1.0", importNow).Run(presets)
	require.NoError(t, err)
	require.Len(t, res.Works, 6)
	require.NotEmpty(t, res.CorpusID)

	// 关键契约:snapshot 阶段的 pubdate 来源都不可信 → F 全部 unknown。
	// 时效维度在拿到外部数据之前不许参与排序(R-1)。
	for id, dims := range res.Dims {
		require.Equal(t, score.StateUnknown, dims[score.DimFreshness].State,
			"%s 的时效维度不该在只有 calibre 数据时就 measured", id)
	}
	// 而 T 与 readability 是纯本地可算的 → 必须已经有判别力。
	for id, dims := range res.Dims {
		require.Equal(t, score.StateMeasured, dims[score.DimReadability].State, id)
	}
	// 零外部证据时,依赖外部数据的公开榜应当为空 —— 这是诚实,不是 bug。
	require.Empty(t, res.Lists["timeless"], "无外部评分时 timeless 该是空的")
	// 而只用本地信号的榜必须出得来。
	require.NotEmpty(t, res.Lists["library-hygiene"])
	require.NotEmpty(t, res.Lists["publisher-picks"],
		"零外部依赖的那份榜必须能出内容,否则 snapshot 阶段交付不了任何书单")
}
