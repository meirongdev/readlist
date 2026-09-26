package score

import (
	"time"

	"github.com/meirongdev/readlist/internal/corpus"
)

// Edition 一个版次里参与 work 级聚合的字段(editions 表的一行)。
type Edition struct {
	PublisherNorm  string
	Format         string
	Language       string
	HasComments    bool
	HasCover       bool
	ISBN13         string
	Pubdate        string // 以 YYYY-MM-DD 开头;空 = 缺失
	PubdateSource  string
	PersonalRating float64 // 星(0–5);0 = 未评
}

// AddEdition 把一个版次并进它所属的 work —— 「多版次 → work」聚合的唯一实现。
//
// 评分引擎与 API 展示层此前各写一份(出版社取最优 tier、格式取最优、首版年份跳过污染
// 来源……),两份已经漂移过一次:语言在引擎里取首个版次,在展示层取首个非空。展示给
// 读者的出版社与年份必须就是打分用的那一个,所以两边都走这里。
//
// 版次必须按稳定顺序(book_id 升序)喂进来:下面几条「取最优」在并列时都是先到先得。
func (w *WorkInput) AddEdition(e Edition) {
	first := w.editions == 0
	w.editions++

	w.HasComments = w.HasComments || e.HasComments
	w.HasCover = w.HasCover || e.HasCover
	w.HasISBN = w.HasISBN || e.ISBN13 != ""
	if e.Pubdate != "" && e.PublisherNorm != "" {
		w.MetadataFull = true
	}
	if w.Language == "" {
		w.Language = e.Language
	}
	// 出版社取最优 tier(数字小 = 优)。
	if pi := corpus.Publisher(e.PublisherNorm); first || pi.Tier < w.PublisherTier {
		w.PublisherTier = pi.Tier
		w.PublisherNorm = pi.Norm
	}
	// 格式取可读性最优;都认不出时留首个版次的原值,而不是空串。
	if first || corpus.FormatRank(e.Format) > corpus.FormatRank(w.Format) {
		w.Format = e.Format
	}
	// 污染来源的日期一条都不进聚合。mtime 兜底值按构造就落在「最近」,让它进
	// First/Latest 会同时造成两种错:把老书塞进「近一年新书」,又把该上榜的老书
	// 从 min_age_years 里挡掉(review A2)。
	if t, ok := parsePubdate(e.Pubdate); ok && PubdateUsableForAge(e.PubdateSource) {
		if w.FirstPubdate == nil || t.Before(*w.FirstPubdate) {
			w.FirstPubdate = &t
		}
		if w.LatestPubdate == nil || t.After(*w.LatestPubdate) {
			latest := t
			w.LatestPubdate = &latest
		}
		if TrustedPubdateSources[e.PubdateSource] &&
			(w.TrustedPubdate == nil || t.After(*w.TrustedPubdate)) {
			trusted := t
			w.TrustedPubdate = &trusted
			w.PubdateSource = e.PubdateSource
		}
	}
	if e.PersonalRating > 0 && (!w.HasPersonal || e.PersonalRating > w.PersonalRating) {
		w.PersonalRating = e.PersonalRating
		w.HasPersonal = true
	}
}

func parsePubdate(s string) (time.Time, bool) {
	if len(s) < 10 {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02", s[:10])
	return t, err == nil
}
