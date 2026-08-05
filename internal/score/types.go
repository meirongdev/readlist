package score

import "time"

// State 逐维证据状态(system-design §2:measured|shrunk|unknown)。
type State string

const (
	StateMeasured State = "measured"
	StateShrunk   State = "shrunk"
	StateUnknown  State = "unknown"
)

// stateRank 用于 needs 要求的"至少"比较。
var stateRank = map[State]int{StateUnknown: 1, StateShrunk: 2, StateMeasured: 3}

func StateAtLeast(s State, need State) bool {
	return stateRank[s] >= stateRank[need]
}

// Dim 维度 ID(system-design 用 readability,scoring-standard 用 L)。
type Dim string

const (
	DimAcclaim     Dim = "A"
	DimCommunity   Dim = "C"
	DimFreshness   Dim = "F"
	DimTrust       Dim = "T"
	DimDepth       Dim = "D"
	DimPractical   Dim = "P"
	DimReadability Dim = "readability"
)

// AllDims 七个维度的稳定顺序。所有跨维度的遍历都必须走它 —— 遍历
// map[Dim]... 的迭代序是随机的,会让输出(Missing 顺序、理由串、浮点求和)不可复现。
var AllDims = []Dim{DimAcclaim, DimCommunity, DimFreshness, DimTrust, DimDepth, DimPractical, DimReadability}

// DimLabel 维度展示名。
var DimLabel = map[Dim]string{
	DimAcclaim:     "口碑",
	DimCommunity:   "技术圈声量",
	DimFreshness:   "时效",
	DimTrust:       "权威",
	DimDepth:       "深度",
	DimPractical:   "可操作",
	DimReadability: "馆藏可读性",
}

// Rating 单个外部评分源(Google Books / OpenLibrary)。
type Rating struct {
	Source string
	Rating float64 // 0–5
	Count  int
}

// Mention HN 提及(时间衰减用)。
type Mention struct {
	CreatedAt time.Time
}

// Label LLM/人工结构化标注。
type Label struct {
	TopicClass   string
	Level        string
	Depth        float64
	Practicality float64
	Confidence   float64
}

// WorkInput 评分引擎需要的单本 work 全部事实(score 命令只读 DB,不联网)。
type WorkInput struct {
	WorkID        string
	Title         string
	FirstAuthor   string
	PrimaryTopic  string
	Level         string
	HalfLifeYears float64
	PublisherNorm string
	PublisherTier int
	Format        string
	HasCover      bool
	HasComments   bool
	HasISBN       bool
	MetadataFull  bool

	// 三个出版日期各有各的用途,不能混用:
	//   TrustedPubdate —— 仅可信来源(google/openlibrary/file-meta)且不晚于今天的**最新**
	//                     版次日期。F 维与所有**按时间取书**的过滤器只认这个。
	//   FirstPubdate   —— 未被污染的来源里的**最早**版次日期。min_age_years 只认这个:
	//                     1996 年的经典出了 2024 年新版,它依然是一本经历过时间检验的书。
	//   LatestPubdate  —— 未被污染的来源里的**最新**版次日期(展示与兜底用)。
	// 后两个都经 PubdateUsableForAge 过滤 —— 见那里的说明。
	TrustedPubdate *time.Time
	FirstPubdate   *time.Time
	LatestPubdate  *time.Time

	Ratings        []Rating
	Mentions       []Mention
	Label          *Label
	PersonalRating float64
	HasPersonal    bool
	ReadStatus     string
	Shelves        []string
	HasReading     bool
	Language       string
	Topics         []string // 主题标签(筛选 topics_any 用)
	PubdateSource  string   // 可信来源过滤用(TrustedPubdate 的来源)
}

// DimScore 单维得分(judgement 层产物)。
type DimScore struct {
	Raw        float64 `json:"raw"`
	Pct        float64 `json:"pct"`
	Score      float64 `json:"score"`
	State      State   `json:"state"`
	Source     string  `json:"source,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
}

// TrustedPubdateSources 可信任的 pubdate 来源(scoring-standard §3 F 前置)。
var TrustedPubdateSources = map[string]bool{
	"google":      true,
	"openlibrary": true,
	"file-meta":   true,
}

// pollutedPubdateSources 已知被污染、不能参与**任何时间判断**的 pubdate 来源。
//
// `mtime-fallback` 是实测的 477 本(全库 23%):文件修改时间被写成了出版日期,
// 所以它们的值全部集中在「最近」。`unknown` 与空串则是缺失或占位。
var pollutedPubdateSources = map[string]bool{
	"mtime-fallback": true,
	"unknown":        true,
	"":               true,
}

// PubdateUsableForAge 这个来源的日期能不能用来判断「书有多老 / 有多新」。
//
// 比 TrustedPubdateSources 宽一档:`calibre`(pubdate ≠ 文件 mtime,所以不是兜底值)
// 够不上 F 维的证据标准,但拿来算年龄是合理的 —— 否则 min_age_years 会把大量本该
// 上「经典长青」的老书一并挡掉。严格排除的只有已知被污染与缺失的来源。
//
// ⚠️ 守住 F 维不等于守住时效:F 只认 TrustedPubdate,而 preset 的
// pubdate_within_months 之类的过滤器走的是另一条代码路径。放污染日期进那条路径,
// 「近一年新书」就会塞满老书 —— 正是 R-1 描述的「整站可信度一次性崩塌」。
func PubdateUsableForAge(source string) bool { return !pollutedPubdateSources[source] }
