package corpus

import "strings"

// HalfLife 主题定档结果(scoring-standard §3 F 维 + system-design §6 规则优先)。
type HalfLife struct {
	Years  float64
	Class  string // 主题类,同时作为选材多样性约束的 primary_topic
	Source string // rules-bisac | rules-topic-class | rules-title-keyword | default
}

// topicHalfLives 主题类 → 半衰期(scoring-standard §3 F 维表格)。
var topicHalfLives = map[string]float64{
	"常青/理论": 25,
	"语言核心":  10,
	"平台/生态": 5,
	"框架/版本": 2.5,
	"时事/趋势": 1.5,
}

// bisacHalfLife BISAC 码前缀 → 主题类。
//
// 实测(data-baseline §1.2)calibre 的 tags 里有大量 BISAC 分类码,虽然只覆盖 22%,
// **但它覆盖的那部分免费且准确** —— 这正是 system-design §6 要「规则优先」的依据。
// 只收录能对上号的前缀;其余落到下一级规则。表可以按需要扩。
var bisacHalfLife = map[string]string{
	"COM051": "语言核心",  // Programming / Programming Languages
	"COM060": "框架/版本", // Web / Web Programming
	"COM042": "时事/趋势", // Natural Language Processing
	"COM004": "时事/趋势", // Intelligence (AI) & Semantics
	"COM014": "常青/理论", // Computer Science
	"COM021": "常青/理论", // Database Management
	"COM046": "平台/生态", // Operating Systems
	"COM043": "平台/生态", // Networking
	"COM053": "平台/生态", // Security
}

// TopicKeywordHalfLife 标题关键词 → 主题类(system-design §6 title_keywords)。
// 规则表按"越常青越先匹配"排列。
func TopicKeywordHalfLife(title string) (string, float64, bool) {
	t := strings.ToLower(title)
	rules := []struct {
		any   []string
		klass string
	}{
		{[]string{"compiler", "algorithm", "operating system", "distributed system", "data-intensive", "interpreter"}, "常青/理论"},
		{[]string{"kubernetes", "terraform", "aws", "data engineering", "microservices", "kafka", "cloud native"}, "平台/生态"},
		{[]string{"react", "vue", "spring boot", "spring in action", "django", "rails", "web programming"}, "框架/版本"},
		{[]string{"llm", "agent", "prompt", "rag", "generative ai", "machine learning", "deep learning"}, "时事/趋势"},
	}
	for _, r := range rules {
		for _, kw := range r.any {
			if strings.Contains(t, kw) {
				return r.klass, topicHalfLives[r.klass], true
			}
		}
	}
	return "", 0, false
}

// bisacClass 从 calibre 的 tags 里认 BISAC 码。标签形如
// "COM060160 - COMPUTERS / Web / Web Programming",取前 6 位前缀匹配。
func bisacClass(tags []string) (string, bool) {
	for _, tag := range tags {
		t := strings.ToUpper(strings.TrimSpace(tag))
		if len(t) < 6 {
			continue
		}
		if klass, ok := bisacHalfLife[t[:6]]; ok {
			return klass, true
		}
	}
	return "", false
}

// HalfLifeFor 主题与半衰期的唯一入口(system-design §6 的规则链)。
//
// 顺序:BISAC 码 → 标注的 topic_class → 标题关键词 → 默认 10 年。
//
// 为什么 topic_class 排在标题关键词之前:关键词表是粗糙启发式,会把 Goodfellow 的
// 《Deep Learning》判成「时事/趋势」(1.5 年)这种明显错误;而标注的可信度是由
// gold set 门禁单独把关的(system-design §7:LLM 不达标则 D/P/F 不上公开榜),
// 不该靠在这条链里压低它的位置来对冲。BISAC 是结构化外部事实,所以排第一。
func HalfLifeFor(title, topicClass string, tags []string) HalfLife {
	if klass, ok := bisacClass(tags); ok {
		return HalfLife{Years: topicHalfLives[klass], Class: klass, Source: "rules-bisac"}
	}
	if klass := strings.TrimSpace(topicClass); klass != "" {
		if years, ok := topicHalfLives[klass]; ok {
			return HalfLife{Years: years, Class: klass, Source: "rules-topic-class"}
		}
	}
	if klass, years, ok := TopicKeywordHalfLife(title); ok {
		return HalfLife{Years: years, Class: klass, Source: "rules-title-keyword"}
	}
	return HalfLife{Years: 10, Class: "", Source: "default"}
}
