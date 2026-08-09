package facts

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/meirongdev/readlist/internal/corpus"
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
	// MentionsReserve 从预算里给 HN 声量查询保底预留的请求数。没有它,editions
	// 阶段(每个版次最多 3 次请求)会把整个预算烧完,HN 在 bootstrap 后的头几晚
	// 一次都轮不到 —— C 维在证据最饥渴的窗口期恒为 0,timeless 榜(needs C)持续为空。
	// 0 = 取 Budget/4;Budget 不限(≤0)时不预留。
	MentionsReserve int
	// WhitelistMainTitles 主标题白名单(主标题 ≤2 词的书默认不查 HN,命中白名单才查)。
	// 与 title_whitelist 表(work_id 键,会随聚类漂移)互补:这份按**规范化主标题**
	// 匹配,由部署方从文件挂进来,book id / work id 漂移都不影响它。
	WhitelistMainTitles []string
	// Sleep 请求之间的最小间隔(礼貌限速;OpenLibrary 建议 0.5s,HN 建议 ≤10 rps)。
	Sleep time.Duration
	// RatingsTTLDays 评分类 30 天,MetaTTLDays 元数据类 180 天(architecture §5)。
	RatingsTTLDays int
	MetaTTLDays    int
	Now            time.Time
}

// Stats 一次摄入的结果,用于日志与 runs.quota_used。
type Stats struct {
	Requests          int  `json:"requests"`
	Throttled         int  `json:"throttled"`
	CacheHits         int  `json:"cache_hits"`
	EditionsSeen      int  `json:"editions_seen"`
	GoogleFound       int  `json:"google_found"`
	OpenLibraryFound  int  `json:"openlibrary_found"`
	OLWorkIDs         int  `json:"ol_work_ids"`
	MentionsFound     int  `json:"mentions_found"`
	PubdatesWritten   int  `json:"pubdates_written"`
	SkippedNoID       int  `json:"skipped_no_identifier"`
	SkippedShortTitle int  `json:"skipped_short_title"`
	Errors            int  `json:"errors"`
	BudgetExhausted   bool `json:"budget_exhausted"`
}

// Ingester 摄入器。
type Ingester struct {
	db     *store.DB
	cfg    Config
	client *client
	stats  Stats
	// mentionsReserve editions 阶段必须给 mentions 留出的预算(见 Config.MentionsReserve)。
	mentionsReserve int
	// fresh 记录已缓存且未过期的 (source, source_id)。
	fresh map[string]bool
	// whitelist ≤2 词标题的人工白名单(title_whitelist 表)。
	whitelist map[string]bool
	// titleAllow 主标题白名单(规范化主标题 → 放行),来自 Config.WhitelistMainTitles。
	titleAllow map[string]bool
	// olWorkIDs work_id → OpenLibrary work id(第一跳的结果)。
	// 有了它,「ISBN→work 映射还新鲜、但评分已过期」时可以只重发第二跳。
	olWorkIDs map[string]string
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
		client:     newClient(cfg.Budget, cfg.Sleep),
		fresh:      map[string]bool{},
		whitelist:  map[string]bool{},
		titleAllow: map[string]bool{},
		olWorkIDs:  map[string]string{},
	}
	switch {
	case cfg.Budget <= 0:
		i.mentionsReserve = 0 // 预算不限,不存在"轮不到"的问题
	case cfg.MentionsReserve <= 0:
		i.mentionsReserve = cfg.Budget / 4
	default:
		i.mentionsReserve = cfg.MentionsReserve
	}
	for _, t := range cfg.WhitelistMainTitles {
		if k := normalizeForMatch(mainTitle(t)); k != "" {
			i.titleAllow[k] = true
		}
	}
	err := i.ingest()
	i.stats.Requests = i.client.used
	i.stats.Throttled = i.client.throttled
	// 无论成败都记一条 run:`score` 在陈旧 facts 上每晚照样成功,所以
	// `last_score` 常绿并不能说明数据是新的 —— 「摄入什么时候最后一次成功」必须
	// 自己有痕迹,否则 snapshot/ingest 挂掉一个月都不会有任何警报(review B1)。
	// pod 日志不算痕迹:successfulJobsHistoryLimit=1 会把它滚掉。
	if wErr := i.writeRun(err); wErr != nil {
		slog.Error("写 ingest run 失败", "err", wErr)
	}
	return i.stats, err
}

func (i *Ingester) ingest() error {
	if err := i.loadCacheState(); err != nil {
		return err
	}
	if err := i.ingestEditions(); err != nil {
		return err
	}
	return i.ingestMentions()
}

// writeRun 把本次摄入记进 runs(kind='ingest'),供 /metrics 与配额管理消费。
func (i *Ingester) writeRun(runErr error) error {
	status := "ok"
	if runErr != nil {
		status = "failed"
	}
	metrics, err := json.Marshal(i.stats)
	if err != nil {
		return err
	}
	stamp := i.cfg.Now.Format(time.RFC3339Nano)
	_, err = i.db.SQL().Exec(`INSERT OR REPLACE INTO runs
		(run_id, kind, corpus_id, standard_version, facts_hash, started_at, ended_at, status,
		 ok_count, fail_count, orphan_rows, quota_used, metrics)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		fmt.Sprintf("ingest-%d", i.cfg.Now.UnixNano()), "ingest", "", "", "",
		stamp, stamp, status,
		i.stats.GoogleFound+i.stats.OpenLibraryFound, i.stats.Errors, 0,
		strconv.Itoa(i.stats.Requests), string(metrics))
	return err
}

// loadCacheState 读出还新鲜的缓存键与人工白名单。
func (i *Ingester) loadCacheState() error {
	rows, err := i.db.SQL().Query(
		`SELECT source, source_id, COALESCE(fetched_at,''), COALESCE(ttl_days,0),
		        COALESCE(payload,'') FROM evidence`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var source, sourceID, fetchedAt, payload string
		var ttl int
		if err := rows.Scan(&source, &sourceID, &fetchedAt, &ttl, &payload); err != nil {
			return err
		}
		t, err := time.Parse(time.RFC3339, fetchedAt)
		if err != nil {
			continue // 时间戳坏了就当过期,重取一次比信一个坏值安全
		}
		// HN 标记还要匹配器版本对得上:旧规则下的「查过且 0 命中」不可信,视为过期。
		if source == sourceHN && !hnMarkerCurrent(payload) {
			continue
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
	if err := wl.Err(); err != nil {
		return err
	}

	ol, err := i.db.SQL().Query(
		`SELECT work_id, ol_work_id FROM works WHERE COALESCE(ol_work_id,'') <> ''`)
	if err != nil {
		return err
	}
	defer ol.Close()
	for ol.Next() {
		var workID, olID string
		if err := ol.Scan(&workID, &olID); err != nil {
			return err
		}
		i.olWorkIDs[workID] = olID
	}
	return ol.Err()
}

func (i *Ingester) isFresh(source, sourceID string) bool {
	return i.fresh[source+"\x1f"+sourceID]
}

// hnMarkerCurrent 该 HN 查询标记是否出自当前版本的匹配器。
func hnMarkerCurrent(payload string) bool {
	var m struct {
		Matcher int `json:"matcher"`
	}
	return json.Unmarshal([]byte(payload), &m) == nil && m.Matcher == hnMatcherVersion
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
	// 新书优先:fresh-releases 的准入(needs F: measured)完全依赖外部 pubdate,
	// 而按 book_id 升序等于"最老的先查"——新入库的书(大概率也是新出版的)排在队尾,
	// bootstrap 追赶期它们最后才拿到证据,新书榜在最需要它的窗口期一直是空的。
	// 无日期的排最后;mtime 污染的日期看起来"新",被优先查到反而正好修正它。
	rows, err := i.db.SQL().Query(`SELECT e.book_id, e.work_id, e.title,
			COALESCE(w.first_author,''), COALESCE(e.isbn13,''), COALESCE(e.google_volume_id,'')
		FROM editions e JOIN works w USING(work_id)
		ORDER BY (COALESCE(e.pubdate,'')=''), e.pubdate DESC, e.book_id DESC`)
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
		// 给 mentions 留出保底预算再停:editions 没查完的明晚接着补,
		// 但 HN 一晚都不能再空着(C 维恒 0 的教训)。
		if i.client.remaining() <= i.mentionsReserve {
			i.stats.BudgetExhausted = true
			slog.Info("editions 预算用到保底线,让位给 mentions,下次运行继续",
				"used", i.client.used, "reserve", i.mentionsReserve)
			return nil
		}
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
	// ⚠️ 查询标记的键必须是**摄入自己不会改写**的标识符,所以优先用 ISBN。
	// 反过来(优先 google id)会自我失效:下面我们会把查出来的 volume id 写回
	// editions,于是下一晚同一个版次算出的键就变了 → 标记全部落空 → 明明刚查过的书
	// 被重查一遍。取哪个键与用哪条路径去查是两件事 —— fetchGoogle 仍然优先直取 volume。
	queryKey := "isbn:" + c.ISBN13
	if c.ISBN13 == "" {
		queryKey = "gvol:" + c.GoogleID
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
	//
	// 命中时标记只能活到**评分 TTL**:Google 一次请求同时返回 volume id 与评分,
	// 所以把「映射」缓存得比「评分」更久没有任何收益 —— 不重发这个请求,评分就刷不了。
	// 之前统一用 180 天,后果是评分类 30 天 TTL 从来没被求值过,榜单实际半年才更新一次。
	// 查不到则压 180 天:没有东西可刷,只需要记住"别再问了"。
	markerTTL := i.cfg.MetaTTLDays
	if found {
		markerTTL = i.cfg.RatingsTTLDays
	}
	marker := map[string]any{"found": found, "volume_id": vol.ID, "queried": queryKey}
	if err := i.putEvidence(sourceGoogleQuery, queryKey, c.WorkID, marker, markerTTL); err != nil {
		return err
	}
	if !found {
		return nil
	}
	i.stats.GoogleFound++

	// 把 ISBN 查出来的 volume id 落库。两个收益:下次可以直取 volume(省掉一次搜索),
	// 且评分行的键(volume id)从此能被**读取时**解析回这个 work —— 否则那 715 本走
	// ISBN 路径的书,其评分只能靠 `evidence.work_id` 这个会随书名变动而失效的快照
	// (见 score.evidenceWorkIndex)。
	if vol.ID != "" && c.GoogleID == "" {
		if _, err := i.db.SQL().Exec(
			`UPDATE editions SET google_volume_id=?
			  WHERE book_id=? AND COALESCE(google_volume_id,'')=''`,
			vol.ID, c.BookID); err != nil {
			return fmt.Errorf("写 google_volume_id book=%d: %w", c.BookID, err)
		}
	}

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
		// OL 是**两跳**:ISBN→work 的映射确实稳定(180 天),但评分是 30 天 TTL。
		// 之前在这里直接 return,于是第二跳永远不会发生,评分实际 180 天才刷一次。
		return i.refreshOpenLibraryRatings(c.WorkID)
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
	// OL work id 是聚类键的最高优先级(system-design §4),存下来供后续升级聚类用,
	// 也让「只刷第二跳」这条短路径有据可依。
	olID := olWorkID(workKey)
	if _, err := i.db.SQL().Exec(
		`UPDATE works SET ol_work_id=? WHERE work_id=? AND COALESCE(ol_work_id,'')=''`,
		olID, c.WorkID); err != nil {
		return err
	}
	if _, ok := i.olWorkIDs[c.WorkID]; !ok {
		i.olWorkIDs[c.WorkID] = olID
	}
	i.stats.OLWorkIDs++

	// 评分是 **work 级**的,所以缓存键用 OL work id:同一 work 的多个版次只查一次。
	if i.isFresh(sourceOpenLibrary, olID) {
		i.stats.CacheHits++
		return nil
	}
	return i.fetchOLRatings(c.WorkID, olID)
}

// refreshOpenLibraryRatings 只重发第二跳:第一跳的结果(ol_work_id)已经存着了。
func (i *Ingester) refreshOpenLibraryRatings(workID string) error {
	olID := i.olWorkIDs[workID]
	if olID == "" {
		return nil // 第一跳没拿到 work key(或该 ISBN 本就查不到)→ 没有可刷的东西
	}
	if i.isFresh(sourceOpenLibrary, olID) {
		i.stats.CacheHits++
		return nil
	}
	return i.fetchOLRatings(workID, olID)
}

// fetchOLRatings 取一个 OL work 的评分并按 work id 缓存(评分 TTL)。
func (i *Ingester) fetchOLRatings(workID, olID string) error {
	r, ok, err := i.fetchOpenLibraryRatings("/works/" + olID)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"rating": r.Summary.Average,
		"count":  r.Summary.Count,
		"raw":    map[string]any{"found": ok, "work": olID},
	}
	return i.putEvidence(sourceOpenLibrary, olID, workID, payload, i.cfg.RatingsTTLDays)
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
		// **主标题** ≤2 词的不查:"Go"、"Clean Code" 这类短语会把 HN 整片误认成
		// 提及(R-3)。要查得先进白名单 —— title_whitelist 表(work_id 键)或
		// 部署方挂进来的主标题白名单文件,两者任一命中即放行。
		if titleWordCount(mainTitle(w.title)) <= 2 &&
			!i.whitelist[w.id] && !i.titleAllow[normalizeForMatch(mainTitle(w.title))] {
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
		// matcher 版本写进标记:规则升级后旧标记自动视为过期(见 hnMatcherVersion)。
		marker := map[string]any{"found": found, "raw_hits": res.NbHits,
			"accepted": len(hits), "matcher": hnMatcherVersion}
		if err := i.putEvidence(sourceHN, w.id, w.id, marker, i.cfg.RatingsTTLDays); err != nil {
			return err
		}
		// 命中集合整组替换:查询结果就是当下的真相,旧规则认下的命中不该残留。
		// 人工否决在 mention_overrides 表,按 (work_id, object_id) 在读取端生效,
		// 不受这次重写影响。
		if _, err := i.db.SQL().Exec(`DELETE FROM mentions WHERE work_id=?`, w.id); err != nil {
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

// writePubdate 用外部日期覆盖该版次的 pubdate,并记下真实来源。
// 优先级用 corpus.PubdateSourcePriority(与 snapshot 同一份):没有这条优先级,
// 当晚的结果就取决于源的遍历顺序 —— OpenLibrary 给的是自由文本日期
// (实测形如 "Apr 02, 2017"),会盖掉 Google 更精确的 "2017-03-16"。
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
	if corpus.PubdateSourcePriority[source] < corpus.PubdateSourcePriority[curSource] {
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
