package selection

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Facts 构造理由串所需的可核对事实(全部来自数据,零 LLM 散文)。
type Facts struct {
	Publisher        string
	Author           string
	AuthorIsUnknown  bool
	MentionCount     int
	MentionFirstYear int
	MentionLastYear  int
	HasMentions      bool
	HalfLifeYears    float64
	HasHalfLife      bool
	Depth            float64
	HasDepth         bool
	Grade            string
	Coverage         float64
	AvailableDims    int
	Missing          []string // "时效—出版日期不可信" 这类确定性描述
}

// Candidate 一个待选 work。
type Candidate struct {
	WorkID        string
	Topic         string
	FirstAuthor   string
	TBS           float64
	Coverage      float64
	AvailableDims int
	Facts         Facts
}

// Config 选材约束(system-design §5 默认:size 20 / max_per_topic 2 / max_per_author 1)。
type Config struct {
	Size         int
	MaxPerTopic  int
	MaxPerAuthor int
	MinCoverage  float64
	Asc          bool
}

// Entry 选材产物。
type Entry struct {
	WorkID   string
	TBS      float64
	Coverage float64
	Reason   string
}

// Select 带多样性约束的贪心选材:按 TBS 排序,逐本校验 coverage 与去重约束。
func Select(cands []Candidate, cfg Config) []Entry {
	if cfg.Size <= 0 {
		cfg.Size = 20
	}
	if cfg.MaxPerTopic <= 0 {
		cfg.MaxPerTopic = 2
	}
	if cfg.MaxPerAuthor <= 0 {
		cfg.MaxPerAuthor = 1
	}
	sorted := make([]Candidate, len(cands))
	copy(sorted, cands)
	sort.SliceStable(sorted, func(i, j int) bool {
		if cfg.Asc {
			return sorted[i].TBS < sorted[j].TBS
		}
		return sorted[i].TBS > sorted[j].TBS
	})

	out := make([]Entry, 0, cfg.Size)
	perTopic := map[string]int{}
	perAuthor := map[string]int{}
	for _, c := range sorted {
		if c.Coverage < cfg.MinCoverage {
			continue
		}
		if perTopic[c.Topic] >= cfg.MaxPerTopic {
			continue
		}
		author := c.FirstAuthor
		if author == "" {
			author = "unknown"
		}
		if perAuthor[author] >= cfg.MaxPerAuthor {
			continue
		}
		out = append(out, Entry{WorkID: c.WorkID, TBS: c.TBS, Coverage: c.Coverage, Reason: Reason(c.Facts)})
		perTopic[c.Topic]++
		perAuthor[author]++
		if len(out) == cfg.Size {
			break
		}
	}
	return out
}

// Reason 确定性理由串。顺序固定:出版社 → HN 提及 → 半衰期 → 深度 → 覆盖 → 缺失。
func Reason(f Facts) string {
	parts := []string{}
	if f.Publisher != "" && f.Publisher != "unknown" {
		parts = append(parts, f.Publisher)
	}
	if !f.AuthorIsUnknown && f.Author != "" {
		parts = append(parts, "作者 "+f.Author)
	}
	if f.HasMentions && f.MentionCount > 0 {
		yr := ""
		if f.MentionFirstYear > 0 && f.MentionLastYear > 0 {
			yr = fmt.Sprintf("（%d–%d）", f.MentionFirstYear, f.MentionLastYear)
		}
		parts = append(parts, fmt.Sprintf("HN 提及 %d 次%s", f.MentionCount, yr))
	}
	if f.HasHalfLife {
		parts = append(parts, fmt.Sprintf("主题半衰期 %g 年", f.HalfLifeYears))
	}
	if f.HasDepth {
		parts = append(parts, fmt.Sprintf("深度 %.0f/100", f.Depth))
	}
	avail := f.AvailableDims
	if avail <= 0 {
		avail = coverageDims(f.Coverage)
	}
	parts = append(parts, fmt.Sprintf("按 %d/7 维评出", avail))
	if len(f.Missing) > 0 {
		parts = append(parts, "缺："+strings.Join(f.Missing, "、"))
	}
	return strings.Join(parts, " · ")
}

func coverageDims(cov float64) int {
	dims := 7
	for cov < float64(dims-1)/7+0.0001 {
		dims--
	}
	return dims
}

// YearRange 返回提及年份区间(供 Facts)。
func YearRange(times []time.Time) (int, int) {
	if len(times) == 0 {
		return 0, 0
	}
	first, last := times[0].Year(), times[0].Year()
	for _, t := range times[1:] {
		if t.Year() < first {
			first = t.Year()
		}
		if t.Year() > last {
			last = t.Year()
		}
	}
	return first, last
}
