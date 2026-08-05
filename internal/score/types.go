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

// AllDims 七个维度的稳定顺序。
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
	WorkID         string
	Title          string
	FirstAuthor    string
	PrimaryTopic   string
	Level          string
	HalfLifeYears  float64
	PublisherNorm  string
	PublisherTier  int
	Format         string
	HasCover       bool
	HasComments    bool
	HasISBN        bool
	MetadataFull   bool
	TrustedPubdate *time.Time // 仅 pubdate_source 可信(google/openlibrary/file-meta)且不晚于今天
	BestPubdate    *time.Time // 任意来源的出版日期(年龄过滤用)
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
	PubdateSource  string   // 用于可信来源过滤
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
