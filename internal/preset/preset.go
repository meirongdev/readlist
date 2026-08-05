package preset

import (
	"embed"
	"fmt"

	"gopkg.in/yaml.v3"
)

//go:embed presets.yaml
var presetFS embed.FS

// Band 目标带(与 score.Band 同构,便于 YAML 反序列化)。
type Band struct {
	Target float64 `yaml:"target"`
	Tol    float64 `yaml:"tol"`
}

// Filters 选材过滤器(命中即入选的必要条件)。
type Filters struct {
	MinAgeYears       int      `yaml:"min_age_years"`
	PubdateYear       int      `yaml:"pubdate_year"`
	PubdateSource     []string `yaml:"pubdate_source"`
	TopicsAny         []string `yaml:"topics_any"`
	Level             []string `yaml:"level"`
	ReadStatus        []string `yaml:"read_status"`
	NotInShelf        []string `yaml:"not_in_shelf"`
	MinPersonalRating float64  `yaml:"min_personal_rating"`
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

// Load 读取内嵌的全部预设。
func Load() ([]Preset, error) {
	body, err := presetFS.ReadFile("presets.yaml")
	if err != nil {
		return nil, err
	}
	var presets []Preset
	if err := yaml.Unmarshal(body, &presets); err != nil {
		return nil, err
	}
	for _, p := range presets {
		if p.ID == "" || len(p.Weights) == 0 {
			return nil, fmt.Errorf("preset %q: id 与 weights 必填", p.ID)
		}
	}
	return presets, nil
}
