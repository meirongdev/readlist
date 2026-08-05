package facts

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/meirongdev/readlist/internal/store"
)

// evidence 的 source 取值。
//
// 分成"实体行"与"查询标记行"两类,是为了同时满足两件事:
//   - 评分必须在 **work 级去重**:同一本书的两个版次可能指向同一个 Google volume
//     或同一个 OpenLibrary work,按查询串存会把同一份评分计两遍
//     (正是 review M6 要修的失真的反面) → 实体行用**外部实体 id** 作 source_id;
//   - 查不到也要缓存:否则每晚都为同一批查不到的书重烧配额 → 标记行用**查询串**作
//     source_id,payload 里不含 rating/count,所以评分引擎会自然忽略它们。
const (
	sourceGoogle      = "google_books"
	sourceGoogleQuery = "google_query"
	sourceOpenLibrary = "openlibrary"
	sourceOLQuery     = "openlibrary_query"
	sourceHN          = "hn_search"
)

// Config 一次摄入的配置。
type Config struct {
	GoogleBase      string
	OpenLibraryBase string
	HNBase          string
	GoogleKey       string
	// Budget 本次运行最多发多少个外部请求。打满就干净停下,下次接着跑(NFR-9)。
	Budget int
	// Sleep 请求之间的最小间隔(礼貌限速;OpenLibrary 建议 0.5s,HN 建议 ≤10 rps)。
	Sleep time.Duration
	// RatingsTTLDays 评分类 30 天,MetaTTLDays 元数据类 180 天(architecture §5)。
	RatingsTTLDays int
	MetaTTLDays    int
	Now            time.Time
}

// Stats 一次摄入的结果,用于日志与 runs.quota_used。
type Stats struct {
	Requests          int `json:"requests"`
	Throttled         int `json:"throttled"`
	CacheHits         int `json:"cache_hits"`
	EditionsSeen      int `json:"editions_seen"`
	GoogleFound       int `json:"google_found"`
	OpenLibraryFound  int `json:"openlibrary_found"`
	OLWorkIDs         int `json:"ol_work_ids"`
	MentionsFound     int `json:"mentions_found"`
	PubdatesWritten   int `json:"pubdates_written"`
	SkippedNoID       int `json:"skipped_no_identifier"`
	SkippedShortTitle int `json:"skipped_short_title"`
	Errors            int `json:"errors"`
	BudgetExhausted   bool
}

// Ingester 摄入器。
type Ingester struct {
	db     *store.DB
	cfg    Config
	client *client
	stats  Stats
	// fresh 记录已缓存且未过期的 (source, source_id)。
	fresh map[string]bool
	// whitelist ≤2 词标题的人工白名单(title_whitelist 表)。
	whitelist map[string]bool
}

// Ingest 摄入外部证据。任何单本书的失败都不会中断整轮 —— 外部源本来就不可靠,
// 部分成功比全盘失败有用得多(NFR-4)。
func Ingest(d *store.DB, cfg Config) (Stats, error) {
	if cfg.Now.IsZero() {
		cfg.Now = time.Now().UTC()
	}
	if cfg.RatingsTTLDays <= 0 {
		cfg.RatingsTTLDays = 30
	}
	if cfg.MetaTTLDays <= 0 {
		cfg.MetaTTLDays = 180
	}
	i := &Ingester{
		db: d, cfg: cfg,
		client:    newClient(cfg.Budget, cfg.Sleep),
		fresh:     map[string]bool{},
		whitelist: map[string]bool{},
	}
	if err := i.loadCacheState(); err != nil {
		return i.stats, err
	}
	if err := i.ingestEditions(); err != nil {
		return i.stats, err
	}
	if err := i.ingestMentions(); err != nil {
		return i.stats, err
	}
	i.stats.Requests = i.client.used
	i.stats.Throttled = i.client.throttled
	return i.stats, nil
}

// loadCacheState 读出还新鲜的缓存键与人工白名单。
func (i *Ingester) loadCacheState() error {
	rows, err := i.db.SQL().Query(
		`SELECT source, source_id, COALESCE(fetched_at,''), COALESCE(ttl_days,0) FROM evidence`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var source, sourceID, fetchedAt string
		var ttl int
		if err := rows.Scan(&source, &sourceID, &fetchedAt, &ttl); err != nil {
			return err
		}
		t, err := time.Parse(time.RFC3339, fetchedAt)
		if err != nil {
			continue // 时间戳坏了就当过期,重取一次比信一个坏值安全
		}
		if t.AddDate(0, 0, ttl).After(i.cfg.Now) {
			i.fresh[source+"\x1f"+sourceID] = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	wl, err := i.db.SQL().Query(`SELECT work_id FROM title_whitelist`)
	if err != nil {
		return err
	}
	defer wl.Close()
	for wl.Next() {
		var id string
		if err := wl.Scan(&id); err != nil {
			return err
		}
		i.whitelist[id] = true
	}
	return wl.Err()
}

func (i *Ingester) isFresh(source, sourceID string) bool {
	return i.fresh[source+"\x1f"+sourceID]
}

// candidate 一个待查的版次。标识符是**逐版次**的,所以外部查询也按版次发;
// 但评分在 work 级汇总(engine 的 acclaim 会把 Σ 人数加起来)。
type candidate struct {
	BookID   int
	WorkID   string
	Title    string
	Author   string
	ISBN13   string
	GoogleID string
}

func (i *Ingester) ingestEditions() error {
	rows, err := i.db.SQL().Query(`SELECT e.book_id, e.work_id, e.title,
			COALESCE(w.first_author,''), COALESCE(e.isbn13,''), COALESCE(e.google_volume_id,'')
		FROM editions e JOIN works w USING(work_id)
		ORDER BY e.book_id`)
	if err != nil {
		return err
	}
	var cands []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.BookID, &c.WorkID, &c.Title, &c.Author, &c.ISBN13, &c.GoogleID); err != nil {
			rows.Close()
			return err
		}
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, c := range cands {
		i.stats.EditionsSeen++
		if c.ISBN13 == "" && c.GoogleID == "" {
			// 约 1,000–1,300 本既无 ISBN 也无 google id(实测)。用标题搜索去猜
			// 是另一套匹配置信度问题(开放问题 #1),这里明确不做。
			i.stats.SkippedNoID++
			continue
		}
		if err := i.fetchGoogleFor(c); err != nil && !i.tolerable(err) {
			return err
		}
		if err := i.fetchOpenLibraryFor(c); err != nil && !i.tolerable(err) {
			return err
		}
		if i.client.remaining() == 0 {
			i.stats.BudgetExhausted = true
			slog.Info("ingest 预算用完,下次运行继续", "used", i.client.used)
			return nil
		}
	}
	return nil
}

// tolerable 单本书级别可以吞掉的错误:预算用完 / 源被限流 / 单次请求失败。
// 这些都不该让整轮摄入失败 —— 已经拿到的数据必须落库。
func (i *Ingester) tolerable(err error) bool {
	switch {
	case errors.Is(err, ErrBudgetExhausted):
		i.stats.BudgetExhausted = true
		return true
	case errors.Is(err, ErrSourceBlocked):
		return true
	default:
		i.stats.Errors++
		slog.Warn("ingest 单项失败", "err", err)
		return true
	}
}

func (i *Ingester) fetchGoogleFor(c candidate) error {
	queryKey := "gvol:" + c.GoogleID
	if c.GoogleID == "" {
		queryKey = "isbn:" + c.ISBN13
	}
	if i.isFresh(sourceGoogleQuery, queryKey) {
		i.stats.CacheHits++
		return nil
	}
	vol, found, err := i.fetchGoogle(c.GoogleID, c.ISBN13)
	if err != nil {
		return err
	}
	// 先写查询标记(不管有没有命中),这样"查不到"也只花一次配额。
	marker := map[string]any{"found": found, "volume_id": vol.ID, "queried": queryKey}
	if err := i.putEvidence(sourceGoogleQuery, queryKey, c.WorkID, marker, i.cfg.MetaTTLDays); err != nil {
		return err
	}
	if !found {
		return nil
	}
	i.stats.GoogleFound++

	// 评分行按 volume id 存:两个版次指向同一 volume 时自然合成一行,不会把同一份
	// 评分计两遍。ratingsCount 为 0 时也存,靠 count<=0 让评分引擎忽略它。
	if vol.ID != "" {
		payload := map[string]any{
			"rating": vol.VolumeInfo.AverageRating,
			"count":  vol.VolumeInfo.RatingsCount,
			"raw":    vol.VolumeInfo, // FR-11:外部响应原样留档
		}
		if err := i.putEvidence(sourceGoogle, vol.ID, c.WorkID, payload, i.cfg.RatingsTTLDays); err != nil {
			return err
		}
	}
	// review M2 的关键一步:同一个响应里就带 publishedDate,顺手写成**带来源的**
	// pubdate。readlist 要的不是"修好 calibre 的库",而是自己表里有个可信日期。
	if date, ok := parseExternalDate(vol.VolumeInfo.PublishedDate); ok {
		if err := i.writePubdate(c.BookID, date, "google"); err != nil {
			return err
		}
	}
	return nil
}

func (i *Ingester) fetchOpenLibraryFor(c candidate) error {
	if c.ISBN13 == "" {
		return nil // OL 的入口是 ISBN
	}
	queryKey := "isbn:" + c.ISBN13
	if i.isFresh(sourceOLQuery, queryKey) {
		i.stats.CacheHits++
		return nil
	}
	ed, found, err := i.fetchOpenLibraryEdition(c.ISBN13)
	if err != nil {
		return err
	}
	workKey := ""
	if found && len(ed.Works) > 0 {
		workKey = ed.Works[0].Key
	}
	marker := map[string]any{"found": found, "work_key": workKey, "queried": queryKey}
	if err := i.putEvidence(sourceOLQuery, queryKey, c.WorkID, marker, i.cfg.MetaTTLDays); err != nil {
		return err
	}
	if !found {
		return nil
	}
	i.stats.OpenLibraryFound++

	if date, ok := parseExternalDate(ed.PublishDate); ok {
		if err := i.writePubdate(c.BookID, date, "openlibrary"); err != nil {
			return err
		}
	}
	if workKey == "" {
		return nil
	}
	// OL work id 是聚类键的最高优先级(system-design §4),存下来供后续升级聚类用。
	olID := olWorkID(workKey)
	if _, err := i.db.SQL().Exec(
		`UPDATE works SET ol_work_id=? WHERE work_id=? AND COALESCE(ol_work_id,'')=''`,
		olID, c.WorkID); err != nil {
		return err
	}
	i.stats.OLWorkIDs++

	// 评分是 **work 级**的,所以缓存键用 OL work id:同一 work 的多个版次只查一次。
	if i.isFresh(sourceOpenLibrary, olID) {
		i.stats.CacheHits++
		return nil
	}
	r, ok, err := i.fetchOpenLibraryRatings(workKey)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"rating": r.Summary.Average,
		"count":  r.Summary.Count,
		"raw":    map[string]any{"found": ok, "work": olID},
	}
	return i.putEvidence(sourceOpenLibrary, olID, c.WorkID, payload, i.cfg.RatingsTTLDays)
}

// ingestMentions 打 HN,产出 mentions 行。
func (i *Ingester) ingestMentions() error {
	rows, err := i.db.SQL().Query(
		`SELECT work_id, canonical_title FROM works ORDER BY work_id`)
	if err != nil {
		return err
	}
	type work struct{ id, title string }
	var works []work
	for rows.Next() {
		var w work
		if err := rows.Scan(&w.id, &w.title); err != nil {
			rows.Close()
			return err
		}
		works = append(works, w)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, w := range works {
		if i.client.remaining() == 0 {
			i.stats.BudgetExhausted = true
			return nil
		}
		// ≤2 词的标题不查:"Go"、"Rust" 这类会把 HN 首页整片认成提及(R-3)。
		// 要查就得先进人工白名单。
		if titleWordCount(w.title) <= 2 && !i.whitelist[w.id] {
			i.stats.SkippedShortTitle++
			continue
		}
		if i.isFresh(sourceHN, w.id) {
			i.stats.CacheHits++
			continue
		}
		var res hnSearch
		found, err := i.client.getJSON(sourceHN, i.hnSearchURL(w.title), &res)
		if err != nil {
			if !i.tolerable(err) {
				return err
			}
			continue
		}
		hits := matchHN(w.title, res, i.cfg.Now)
		marker := map[string]any{"found": found, "raw_hits": res.NbHits, "accepted": len(hits)}
		if err := i.putEvidence(sourceHN, w.id, w.id, marker, i.cfg.RatingsTTLDays); err != nil {
			return err
		}
		for _, m := range hits {
			// 保留 objectID:人工可以逐条否决(R-3)。
			if _, err := i.db.SQL().Exec(`INSERT OR REPLACE INTO mentions
				(work_id, object_id, created_at, matched_by) VALUES (?,?,?,?)`,
				w.id, m.ObjectID, m.CreatedAt.Format(time.RFC3339), m.MatchedBy); err != nil {
				return err
			}
			i.stats.MentionsFound++
		}
	}
	return nil
}

// putEvidence 原样存外部响应 + 归一化的 rating/count(FR-11:派生分只从缓存重算)。
func (i *Ingester) putEvidence(source, sourceID, workID string, payload any, ttlDays int) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = i.db.SQL().Exec(`INSERT OR REPLACE INTO evidence
		(source, source_id, work_id, payload, fetched_at, ttl_days) VALUES (?,?,?,?,?,?)`,
		source, sourceID, workID, string(body), i.cfg.Now.Format(time.RFC3339), ttlDays)
	if err != nil {
		return fmt.Errorf("写 evidence %s/%s: %w", source, sourceID, err)
	}
	return nil
}

// pubdateSourcePriority 出版日期来源的优先级(数字大 = 更可信/更精确)。
//
// 没有这条优先级,当晚的结果就取决于源的遍历顺序:OpenLibrary 给的是自由文本日期
// (实测形如 "Apr 02, 2017"),会盖掉 Google 更精确的 "2017-03-16"。
var pubdateSourcePriority = map[string]int{
	"google":         5,
	"openlibrary":    4,
	"file-meta":      3,
	"calibre":        2,
	"mtime-fallback": 1,
	"unknown":        0,
	"":               0,
}

// writePubdate 用外部日期覆盖该版次的 pubdate,并记下真实来源。
//
// 这一步做完,时效维度才第一次真正有判别力 —— 而它完全不需要动 calibre 的库
// (review M2:readlist 要的是"自己表里有个带来源的 pubdate")。
func (i *Ingester) writePubdate(bookID int, date, source string) error {
	var curDate, curSource string
	err := i.db.SQL().QueryRow(
		`SELECT COALESCE(pubdate,''), COALESCE(pubdate_source,'') FROM editions WHERE book_id=?`,
		bookID).Scan(&curDate, &curSource)
	if err != nil {
		return fmt.Errorf("读 pubdate book=%d: %w", bookID, err)
	}
	if pubdateSourcePriority[source] < pubdateSourcePriority[curSource] {
		return nil // 已有更可信的来源,不降级
	}
	if curDate == date && curSource == source {
		return nil
	}
	if _, err := i.db.SQL().Exec(
		`UPDATE editions SET pubdate=?, pubdate_source=? WHERE book_id=?`,
		date, source, bookID); err != nil {
		return fmt.Errorf("写 pubdate book=%d: %w", bookID, err)
	}
	i.stats.PubdatesWritten++
	return nil
}
