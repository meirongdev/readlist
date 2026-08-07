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
	Coverage         float64
	AvailableDims    int
	TotalDims        int // 该 preset 加权的维度总数
	// AgeUnverified 这份榜有年龄下限,但这本书的出版日期不可信 → 年龄没被核对过。
	// 过滤器对「日期未知」是放行的(见 score.filterPass 的不对称说明),放行就必须
	// 说出来 —— 否则一本出版日期不明的书会以「经典」的名义静默上榜。
	AgeUnverified bool
	// Pinned 人工置顶。必须出现在理由串里:curation 不该伪装成算法结果 ——
	// 「可解释」这条承诺的全部价值就在于读者能分清哪部分是算出来的。
	Pinned  bool
	Missing []string // "时效—出版日期不可信" 这类确定性描述
}

// Candidate 一个待选 work。
type Candidate struct {
	WorkID      string
	Topic       string
	FirstAuthor string
	TBS         float64
	Coverage    float64
	// Pinned 人工置顶:排在算法结果之前,且不受 coverage 与多样性约束限制。
	Pinned bool
	Facts  Facts
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
		// 人工置顶整体排在算法结果之前。pin 表达的是「我为这个排名辩护」,
		// 不是给它加权 —— 所以它不参与分数比较,而是换一个层级。
		if sorted[i].Pinned != sorted[j].Pinned {
			return sorted[i].Pinned
		}
		if sorted[i].TBS != sorted[j].TBS {
			if cfg.Asc {
				return sorted[i].TBS < sorted[j].TBS
			}
			return sorted[i].TBS > sorted[j].TBS
		}
		// 并列必须有确定的打破键。没有它,同分书的先后取决于调用方传进来的顺序;
		// 而多样性约束(每主题/每作者上限)会把这个顺序放大成**成员差异** ——
		// 同一份语料两次打分会出两份不同的榜(NFR-10 破功)。
		return sorted[i].WorkID < sorted[j].WorkID
	})

	out := make([]Entry, 0, cfg.Size)
	perTopic := map[string]int{}
	perAuthor := map[string]int{}
	for _, c := range sorted {
		author := c.FirstAuthor
		if author == "" {
			author = "unknown"
		}
		// 置顶项跳过三道约束,但**照样计入**下面的计数器:后续算法选出的书仍要
		// 相对置顶项保持多样性,否则 pin 一本 Kubernetes 书就会引来第二本。
		if !c.Pinned {
			if c.Coverage < cfg.MinCoverage {
				continue
			}
			if perTopic[c.Topic] >= cfg.MaxPerTopic {
				continue
			}
			if perAuthor[author] >= cfg.MaxPerAuthor {
				continue
			}
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

// Reason 确定性理由串。顺序固定:
// 人工置顶 → 出版社 → 作者 → HN 提及 → 半衰期 → 深度 → 覆盖 → 年龄未核实 → 缺失。
func Reason(f Facts) string {
	parts := []string{}
	if f.Pinned {
		parts = append(parts, "人工置顶")
	}
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
	// 分母是**这份榜实际用到的维度数**,不是 7。timeless 只用 5 维,一本五维全实测的书
	// 写成「按 5/7 维评出」会被读成缺了两维。
	if f.TotalDims > 0 {
		parts = append(parts, fmt.Sprintf("按 %d/%d 维评出", f.AvailableDims, f.TotalDims))
	}
	if f.AgeUnverified {
		parts = append(parts, "出版日期不可信,年龄未核实")
	}
	if len(f.Missing) > 0 {
		parts = append(parts, "缺："+strings.Join(f.Missing, "、"))
	}
	return strings.Join(parts, " · ")
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
