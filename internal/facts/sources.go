package facts

import (
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// ---------- Google Books ----------
//
// 两种取法:有 google identifier 就直取 volume(白捡的,实测 310 本);
// 否则按 ISBN 查(实测 715 本)。同一个响应里就带 publishedDate ——
// 这是 review M2 的关键:readlist 需要的不是"修好 calibre 的 metadata.db",
// 而是"自己表里有一个带来源的 pubdate"。

type gbVolume struct {
	ID         string `json:"id"`
	VolumeInfo struct {
		Title         string   `json:"title"`
		Authors       []string `json:"authors"`
		Publisher     string   `json:"publisher"`
		PublishedDate string   `json:"publishedDate"`
		Categories    []string `json:"categories"`
		PageCount     int      `json:"pageCount"`
		AverageRating float64  `json:"averageRating"`
		RatingsCount  int      `json:"ratingsCount"`
	} `json:"volumeInfo"`
}

type gbSearch struct {
	TotalItems int        `json:"totalItems"`
	Items      []gbVolume `json:"items"`
}

// googleVolumeURL 直取一个 volume。
func (i *Ingester) googleVolumeURL(volumeID string) string {
	q := url.Values{}
	if i.cfg.GoogleKey != "" {
		q.Set("key", i.cfg.GoogleKey)
	}
	return joinURL(i.cfg.GoogleBase, "/volumes/"+url.PathEscape(volumeID), q)
}

// googleISBNURL 按 ISBN 查。
func (i *Ingester) googleISBNURL(isbn string) string {
	q := url.Values{"q": {"isbn:" + isbn}}
	if i.cfg.GoogleKey != "" {
		q.Set("key", i.cfg.GoogleKey)
	}
	return joinURL(i.cfg.GoogleBase, "/volumes", q)
}

// fetchGoogle 返回 volume(找不到则 ok=false)。
func (i *Ingester) fetchGoogle(volumeID, isbn string) (gbVolume, bool, error) {
	if volumeID != "" {
		var v gbVolume
		found, err := i.client.getJSON(sourceGoogle, i.googleVolumeURL(volumeID), &v)
		if err != nil || !found {
			return gbVolume{}, false, err
		}
		if v.ID == "" {
			v.ID = volumeID
		}
		return v, true, nil
	}
	if isbn == "" {
		return gbVolume{}, false, nil
	}
	var res gbSearch
	found, err := i.client.getJSON(sourceGoogle, i.googleISBNURL(isbn), &res)
	if err != nil || !found || len(res.Items) == 0 {
		return gbVolume{}, false, err
	}
	// ISBN 查询按定义最多命中一本书;多于一条时取第一条,不做模糊挑选。
	return res.Items[0], true, nil
}

// ---------- OpenLibrary ----------
//
// 两跳:/isbn/{isbn}.json 拿到 works[].key,再打 /works/{OLID}/ratings.json。
// work id 是 system-design §4 里聚类键的**最高优先级**,顺手存下来。
// 实测提示:OL 对技术书的评分覆盖很薄(样例 count=0),所以这一源主要贡献
// work id 与出版日期,而不是评分。

type olEdition struct {
	Key         string   `json:"key"`
	Title       string   `json:"title"`
	PublishDate string   `json:"publish_date"`
	Publishers  []string `json:"publishers"`
	Works       []struct {
		Key string `json:"key"`
	} `json:"works"`
}

type olRatings struct {
	Summary struct {
		Average float64 `json:"average"`
		Count   int     `json:"count"`
	} `json:"summary"`
}

func (i *Ingester) fetchOpenLibraryEdition(isbn string) (olEdition, bool, error) {
	var ed olEdition
	found, err := i.client.getJSON(sourceOpenLibrary,
		i.cfg.OpenLibraryBase+"/isbn/"+url.PathEscape(isbn)+".json", &ed)
	return ed, found, err
}

func (i *Ingester) fetchOpenLibraryRatings(workKey string) (olRatings, bool, error) {
	var r olRatings
	// workKey 形如 "/works/OL19293745W"。
	found, err := i.client.getJSON(sourceOpenLibrary,
		i.cfg.OpenLibraryBase+workKey+"/ratings.json", &r)
	return r, found, err
}

// olWorkID 从 "/works/OL19293745W" 取出 "OL19293745W"。
func olWorkID(key string) string {
	if idx := strings.LastIndex(key, "/"); idx >= 0 {
		return key[idx+1:]
	}
	return key
}

// ---------- HN Algolia ----------

type hnSearch struct {
	NbHits int `json:"nbHits"`
	Hits   []struct {
		ObjectID  string `json:"objectID"`
		Title     string `json:"title"`
		CreatedAt string `json:"created_at"`
		Points    int    `json:"points"`
	} `json:"hits"`
}

func (i *Ingester) hnSearchURL(title string) string {
	// 精确短语查询:加引号让 Algolia 按短语而不是分词匹配。
	q := url.Values{
		"query":       {`"` + title + `"`},
		"tags":        {"story"},
		"hitsPerPage": {"50"},
	}
	return joinURL(i.cfg.HNBase, "/search", q)
}

// hnMention 一条被接受的提及。
type hnMention struct {
	ObjectID  string
	CreatedAt time.Time
	MatchedBy string
}

// matchHN 把搜索结果过成"宁少不多"的命中集合(R-3)。
//
// 三条规则,每条都为了少认而不是多认:
//   - 标题 ≤2 词的书**根本不查**(除非在人工白名单里):"Go"、"Rust" 这类
//     标题会把整个 HN 首页认成提及;
//   - 命中的 story 标题必须**包含**规范化后的书名(不是反过来);
//   - 保留 objectID,人工可以逐条否决(mention_overrides 的入口)。
func matchHN(bookTitle string, res hnSearch, now time.Time) []hnMention {
	want := normalizeForMatch(bookTitle)
	if want == "" {
		return nil
	}
	out := make([]hnMention, 0, len(res.Hits))
	for _, h := range res.Hits {
		if h.ObjectID == "" || h.Title == "" {
			continue
		}
		if !strings.Contains(normalizeForMatch(h.Title), want) {
			continue
		}
		t, err := time.Parse(time.RFC3339, h.CreatedAt)
		if err != nil || t.After(now) {
			continue
		}
		out = append(out, hnMention{ObjectID: h.ObjectID, CreatedAt: t, MatchedBy: "exact-title-phrase"})
	}
	return out
}

// titleWordCount 规范化后的词数,用于"≤2 词必须白名单"的判定。
func titleWordCount(title string) int {
	return len(strings.Fields(normalizeForMatch(title)))
}

// normalizeForMatch 匹配用的规范形:小写、非字母数字折成空格、折叠空白。
// 保留 Unicode 字母,中文书名才不会被清空。
func normalizeForMatch(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// ---------- 出版日期解析 ----------

// parseExternalDate 解析外部源的出版日期。
//
// 两个源的格式都不统一:Google Books 会给 "2017"、"2017-03" 或 "2017-03-16";
// OpenLibrary 给的是自由文本,实测形如 "Apr 02, 2017"。缺月/日时补 01 ——
// 半衰期是以年为尺度的,月份精度对 F 维没有影响。
func parseExternalDate(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false
	}
	layouts := []string{
		"2006-01-02", "2006-01", "2006",
		"Jan 02, 2006", "January 2, 2006", "January 2006", "Jan 2006",
		"02 January 2006", "2006/01/02",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			// 只信到年:上面那些布局补出来的月/日本身就是猜的。
			if t.Year() < 1400 || t.Year() > 2200 {
				return "", false
			}
			return t.Format("2006-01-02"), true
		}
	}
	// 最后兜底:字符串里找一个像年份的四位数。
	for i := 0; i+4 <= len(s); i++ {
		if y, err := strconv.Atoi(s[i : i+4]); err == nil && y >= 1400 && y <= 2200 {
			return strconv.Itoa(y) + "-01-01", true
		}
	}
	return "", false
}
