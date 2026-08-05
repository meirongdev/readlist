package corpus

import "strings"

// HalfLife 主题半衰期定档(scoring-standard §3 F 维 + system-design §6 规则优先)。
type HalfLife struct {
	Years  float64
	Source string // rules | llm-fallback
}

// topicHalfLives 主题类 → 半衰期(scoring-standard §3 F 维表格)。
var topicHalfLives = map[string]HalfLife{
	"常青/理论": {Years: 25, Source: "rules"},
	"语言核心":  {Years: 10, Source: "rules"},
	"平台/生态": {Years: 5, Source: "rules"},
	"框架/版本": {Years: 2.5, Source: "rules"},
	"时事/趋势": {Years: 1.5, Source: "rules"},
}

// HalfLifeByTopic 按主题类定档;未命中走 LLM 兜底(标为 rules 的初值,实际由标注层覆盖)。
func HalfLifeByTopic(topic string) HalfLife {
	if hl, ok := topicHalfLives[strings.TrimSpace(topic)]; ok {
		return hl
	}
	return HalfLife{Years: 10, Source: "llm-fallback"}
}

// TopicKeywordHalfLife 标题关键词 → 半衰期(system-design §6 title_keywords)。
// 命中即定档,不问 LLM。用于 seed/ingest 给标题打主题定档。
func TopicKeywordHalfLife(title string) (string, float64, bool) {
	t := strings.ToLower(title)
	rules := []struct {
		any      []string
		halfLife float64
		klass    string
	}{
		{[]string{"compiler", "algorithm", "operating system", "distributed system", "data-intensive", "interpreter"}, 25, "常青/理论"},
		{[]string{"kubernetes", "terraform", "aws", "data engineering", "microservices", "kafka", "cloud native"}, 5, "平台/生态"},
		{[]string{"react", "vue", "spring boot", "spring in action", "django", "rails", "web programming"}, 2.5, "框架/版本"},
		{[]string{"llm", "agent", "prompt", "rag", "generative ai", "machine learning", "deep learning"}, 1.5, "时事/趋势"},
	}
	for _, r := range rules {
		for _, kw := range r.any {
			if strings.Contains(t, kw) {
				return r.klass, r.halfLife, true
			}
		}
	}
	return "", 0, false
}
