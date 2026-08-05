package preset

import (
	"embed"
	"fmt"
	"math"

	"gopkg.in/yaml.v3"
)

//go:embed presets.yaml
var presetFS embed.FS

// validDims 合法维度名。
//
// 必须与 score.AllDims 保持一致。preset 不能 import score(反向依赖已存在),
// 所以两边的同步由 score 包里的一条测试锁住,而不是靠人记住。
var validDims = map[string]bool{
	"A": true, "C": true, "F": true, "T": true,
	"D": true, "P": true, "readability": true,
}

// validStates 合法的证据状态(needs 的取值)。
var validStates = map[string]bool{"measured": true, "shrunk": true, "unknown": true}

// ValidDims 返回合法维度名。存在的意义是让 score 包能用一条测试锁住
// validDims 与 score.AllDims 的同步(靠人记住两处保持一致是不可靠的)。
func ValidDims() []string {
	out := make([]string, 0, len(validDims))
	for d := range validDims {
		out = append(out, d)
	}
	return out
}

// weightSumTolerance 权重和的容差(YAML 里写的是两位小数,浮点求和会有末位误差)。
const weightSumTolerance = 1e-6

// Band 目标带(与 score.Band 同构,便于 YAML 反序列化)。
// json tag 是必须的:这个结构会随 /api/v1/lists 发给前端,滑块要按 target/tol 复算。
type Band struct {
	Target float64 `yaml:"target" json:"target"`
	Tol    float64 `yaml:"tol"    json:"tol"`
}

// Filters 选材过滤器(命中即入选的必要条件)。
type Filters struct {
	MinAgeYears int `yaml:"min_age_years"`
	// PubdateWithinMonths 滚动时间窗(月)。「新书」榜用它而不是硬编码年份,
	// 否则跨年后这个榜会静默变成「去年的书」。
	PubdateWithinMonths int      `yaml:"pubdate_within_months"`
	PubdateYear         int      `yaml:"pubdate_year"`
	PubdateSource       []string `yaml:"pubdate_source"`
	TopicsAny           []string `yaml:"topics_any"`
	Level               []string `yaml:"level"`
	ReadStatus          []string `yaml:"read_status"`
	NotInShelf          []string `yaml:"not_in_shelf"`
	// MinPersonalRating 单位写死为**星(0–5)**,即 calibre metadata 值 ÷ 2
	// (calibre 的 books_ratings_link.rating 是 0–10)。review M9。
	MinPersonalRating float64 `yaml:"min_personal_rating"`
}

// SelectConfig 选材约束(system-design §5)。
type SelectConfig struct {
	Size         int     `yaml:"size"`
	MaxPerTopic  int     `yaml:"max_per_topic"`
	MaxPerAuthor int     `yaml:"max_per_author"`
	MinCoverage  float64 `yaml:"min_coverage"`
}

// Preset 一份榜单预设 = 权重 + 目标带 + needs + 选材约束 + 过滤器。
type Preset struct {
	ID          string             `yaml:"id"`
	Name        string             `yaml:"name"`
	Description string             `yaml:"description"`
	Weights     map[string]float64 `yaml:"weights"`
	Bands       map[string]Band    `yaml:"bands"`
	Needs       map[string]string  `yaml:"needs"`
	Select      SelectConfig       `yaml:"select"`
	Filters     Filters            `yaml:"filters"`
	Order       string             `yaml:"order"` // desc|asc
	Visibility  string             `yaml:"visibility"`
}

// Public 是否对外公开(internal 榜不出现在公开导航)。
func (p Preset) Public() bool { return p.Visibility != "internal" }

// Validate 加载期校验。
//
// preset 是「加榜不改代码、不重算分数」的配置面,所以配置错误必须在进程启动时炸,
// 而不是退化成一个静默失效的榜。这里每一条都对应过一个真实缺陷:
//   - weights 和不为 1 → coverage 与 TBS 的量纲失真;
//   - bands 的维度不在 weights → band 项系数为 0,「太深的书不适合速成」这个卖点
//     在公式上根本不成立(ship-this-week 从落地第一天起就是这样);
//   - needs/weights 的维度名拼错 → 该维永远算 Missing,coverage 永远到不了 1。
func (p Preset) Validate() error {
	if p.ID == "" {
		return fmt.Errorf("id 必填")
	}
	if len(p.Weights) == 0 {
		return fmt.Errorf("weights 必填")
	}
	var sum float64
	for dim, w := range p.Weights {
		if !validDims[dim] {
			return fmt.Errorf("weights 含未知维度 %q", dim)
		}
		if w <= 0 {
			return fmt.Errorf("weights[%s] 必须为正(不参与就别写这一维)", dim)
		}
		sum += w
	}
	if math.Abs(sum-1) > weightSumTolerance {
		return fmt.Errorf("weights 之和为 %.4f,必须为 1", sum)
	}
	for dim, b := range p.Bands {
		if _, weighted := p.Weights[dim]; !weighted {
			return fmt.Errorf("bands[%s] 的维度不在 weights 里 → band 项权重为 0,是空操作", dim)
		}
		if b.Tol <= 0 {
			return fmt.Errorf("bands[%s].tol 必须为正", dim)
		}
	}
	for dim, state := range p.Needs {
		if !validDims[dim] {
			return fmt.Errorf("needs 含未知维度 %q", dim)
		}
		if !validStates[state] {
			return fmt.Errorf("needs[%s] = %q 不是合法证据状态", dim, state)
		}
	}
	if p.Select.MinCoverage < 0 || p.Select.MinCoverage > 1 {
		return fmt.Errorf("select.min_coverage 必须落在 [0,1],当前 %v", p.Select.MinCoverage)
	}
	if p.Select.Size < 0 {
		return fmt.Errorf("select.size 不能为负")
	}
	switch p.Order {
	case "", "desc", "asc":
	default:
		return fmt.Errorf("order = %q,只能是 desc 或 asc", p.Order)
	}
	switch p.Visibility {
	case "", "public", "internal":
	default:
		return fmt.Errorf("visibility = %q,只能是 public 或 internal", p.Visibility)
	}
	if r := p.Filters.MinPersonalRating; r < 0 || r > 5 {
		return fmt.Errorf("filters.min_personal_rating = %v,单位是星(0–5)", r)
	}
	if p.Filters.PubdateYear != 0 && (p.Filters.PubdateYear < 1900 || p.Filters.PubdateYear > 2100) {
		return fmt.Errorf("filters.pubdate_year = %d 不像一个年份", p.Filters.PubdateYear)
	}
	return nil
}

// Load 读取并校验内嵌的全部预设。
func Load() ([]Preset, error) {
	body, err := presetFS.ReadFile("presets.yaml")
	if err != nil {
		return nil, err
	}
	var presets []Preset
	if err := yaml.Unmarshal(body, &presets); err != nil {
		return nil, fmt.Errorf("解析 presets.yaml: %w", err)
	}
	if len(presets) == 0 {
		return nil, fmt.Errorf("presets.yaml 里没有任何预设")
	}
	seen := map[string]bool{}
	for _, p := range presets {
		if err := p.Validate(); err != nil {
			return nil, fmt.Errorf("preset %q: %w", p.ID, err)
		}
		if seen[p.ID] {
			return nil, fmt.Errorf("preset id %q 重复", p.ID)
		}
		seen[p.ID] = true
	}
	return presets, nil
}
