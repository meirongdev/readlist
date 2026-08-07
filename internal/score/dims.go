package score

import (
	"math"
	"strings"
	"time"

	"github.com/meirongdev/readlist/internal/corpus"
	"github.com/meirongdev/readlist/internal/preset"
)

// DimResult 单维原始值与状态。
type DimResult struct {
	Raw    float64
	State  State
	Source string
	Conf   float64
	Count  int // 外部评分总人数(A 维中位数 m 用)
}

// DimParams 语料级参数(prior 与置信阈值,由 engine 从实测数据算)。
type DimParams struct {
	AcclaimPrior float64 // 全库加权平均分(先验均值)
	MinCount     float64 // m = max(median count, 20)
}

// Compute 计算七个维度的 raw + state(归一化由 norm 层负责)。
func (w *WorkInput) Compute(p DimParams, now time.Time) map[Dim]DimResult {
	depthRaw, depthConf := 50.0, 0.0
	pracRaw, pracConf := 50.0, 0.0
	if w.Label != nil {
		depthRaw, depthConf = w.Label.Depth, w.Label.Confidence
		pracRaw, pracConf = w.Label.Practicality, w.Label.Confidence
	}
	return map[Dim]DimResult{
		DimAcclaim:     w.acclaim(p),
		DimCommunity:   w.community(now),
		DimFreshness:   w.freshness(now),
		DimTrust:       w.trust(),
		DimDepth:       dimByLabel(depthConf, depthRaw),
		DimPractical:   dimByLabel(pracConf, pracRaw),
		DimReadability: w.readability(),
	}
}

// acclaim 口碑:IMDb 式贝叶斯加权(scoring-standard §3 A)。
//
// 多源评分先在 work 级**汇总**(Σ 人数、人数加权均分),再整体收缩**一次**。
// 不能逐源各收缩一次再加权平均 —— 那会把同一本书被拆散的评分人数各自往先验拉,
// 系统性低估多版次/多源的书(review M6:3,000 人的评分被当成 3 × 600 算)。
func (w *WorkInput) acclaim(p DimParams) DimResult {
	var sumRV, sumV float64
	for _, r := range w.Ratings {
		if r.Count <= 0 {
			continue
		}
		sumRV += r.Rating * float64(r.Count)
		sumV += float64(r.Count)
	}
	if sumV == 0 {
		return DimResult{Raw: p.AcclaimPrior, State: StateShrunk, Source: "prior"}
	}
	m := math.Max(p.MinCount, 1)
	mean := sumRV / sumV
	raw := sumV/(sumV+m)*mean + m/(sumV+m)*p.AcclaimPrior
	return DimResult{Raw: raw, State: StateMeasured, Source: "external-ratings", Count: int(sumV)}
}

// community 技术圈声量:时间衰减提及数(scoring-standard §3 C,τ=4 年)。
func (w *WorkInput) community(now time.Time) DimResult {
	if len(w.Mentions) == 0 {
		return DimResult{Raw: 0, State: StateShrunk, Source: "prior"}
	}
	tau := 4.0
	var sum float64
	for _, m := range w.Mentions {
		age := now.Sub(m.CreatedAt).Hours() / (24 * 365)
		if age < 0 {
			age = 0
		}
		sum += 1 / (1 + age/tau)
	}
	return DimResult{Raw: sum, State: StateMeasured, Source: "hn-algolia"}
}

// freshness 时效:主题半衰期指数衰减(scoring-standard §3 F)。
func (w *WorkInput) freshness(now time.Time) DimResult {
	if w.TrustedPubdate == nil {
		return DimResult{Raw: 0, State: StateUnknown, Source: "untrusted-pubdate"}
	}
	age := now.Sub(*w.TrustedPubdate).Hours() / (24 * 365)
	if age < 0 {
		age = 0
	}
	hl := w.HalfLifeYears
	if hl <= 0 {
		hl = 10
	}
	return DimResult{Raw: 100 * math.Pow(0.5, age/hl), State: StateMeasured, Source: "pubdate-trusted"}
}

// trust 权威:0.6·出版社层级 + 0.4·作者信号(scoring-standard §3 T)。
func (w *WorkInput) trust() DimResult {
	authorKnown := w.FirstAuthor != "" && !strings.EqualFold(strings.TrimSpace(w.FirstAuthor), "unknown")
	pubKnown := w.PublisherTier <= 3
	if !authorKnown && !pubKnown {
		return DimResult{Raw: 25, State: StateUnknown, Source: "unknown-author+publisher"}
	}
	authorSignal := 25.0
	if !authorKnown {
		authorSignal = 0
	}
	raw := 0.6*corpus.TierScore(w.PublisherTier) + 0.4*authorSignal
	if !pubKnown {
		return DimResult{Raw: raw, State: StateShrunk, Source: "unknown-publisher"}
	}
	return DimResult{Raw: raw, State: StateMeasured, Source: "publisher-tier"}
}

// dimByLabel 深度/可操作:LLM 标注置信度门禁。
// conf ≥ 0.7 → measured;0.5 ≤ conf < 0.7 → shrunk;否则 unknown(不用于榜单)。
func dimByLabel(conf, value float64) DimResult {
	if conf >= 0.7 {
		return DimResult{Raw: value, State: StateMeasured, Source: "llm-label", Conf: conf}
	}
	if conf >= 0.5 {
		return DimResult{Raw: value, State: StateShrunk, Source: "llm-label-lowconf", Conf: conf}
	}
	return DimResult{Raw: 50, State: StateUnknown, Source: "no-label", Conf: conf}
}

// readability 馆藏可读性:纯本地 100% 可算(scoring-standard §3 L)。
func (w *WorkInput) readability() DimResult {
	raw := 30 * corpus.FormatReadability(w.Format)
	if w.HasCover {
		raw += 20
	}
	if w.HasComments {
		raw += 20
	}
	if w.HasISBN {
		raw += 15
	}
	if w.MetadataFull {
		raw += 15
	}
	return DimResult{Raw: raw, State: StateMeasured, Source: "local-metadata"}
}

// GradedDims 「实际参与排序的维度」= 在任意一份 preset 里占非零权重的维度。
//
// 徽章的口径必须从 presets.yaml 推导,不能写死一份维度清单:榜单增删是配置行为
// (加榜 = 加一段 YAML),写死的清单迟早和实际排序用的维度对不上。这不是假想 ——
// 旧版 Grade 遍历 AllDims 求「全维 measured」才给 A,而 D / P 两维根本没有生产
// 数据源(labels 表只有 corpus.Seed 会写),于是 A 级永远不可达、九成以上的书压在
// 同一个 B 上,一个恒定值占着每本书标题旁边的位置。
//
// 用「占权重」而不是「出现在 needs 里」:needs 是准入闸门,不满足的书压根不在这份
// 榜上,轮不到给它发徽章;真正决定分数由多少证据支撑的是权重。
func GradedDims(presets []preset.Preset) []Dim {
	weighted := map[Dim]bool{}
	for _, p := range presets {
		for name, w := range p.Weights {
			if w > 0 {
				weighted[Dim(name)] = true
			}
		}
	}
	// 走 AllDims 而不是遍历 map:输出顺序必须稳定(见 AllDims 的注释)。
	out := make([]Dim, 0, len(weighted))
	for _, d := range AllDims {
		if weighted[d] {
			out = append(out, d)
		}
	}
	return out
}

// externalDims 需要**外部证据**才能 measured 的维度。
// T(出版社层级 × 作者知名度)与 readability(格式/封面/简介齐全度)只读本地
// calibre 元数据,它们 measured 说明不了「这本书被本库之外的世界验证过」——
// 徽章里 B 与 C 的分界正是这件事。
var externalDims = map[Dim]bool{
	DimAcclaim: true, DimCommunity: true, DimFreshness: true,
	DimDepth: true, DimPractical: true,
}

// Grade 证据等级徽章(system-design §2)。graded 是参与排序的维度集合,由
// GradedDims 从 preset 推出:
//
//	A  graded 维全部 measured
//	B  有 shrunk,但至少一个 graded 的外部证据维 measured
//	C  没有任何外部信号,分数基本靠本地元数据撑着
//	D  有 graded 维为 unknown —— 那一维被整个 renormalize 出这本书的权重,
//	   也就是说它的分是在**比榜单声明更少的维度**上算出来的
//
// 不参与排序的维度不看:F 的证据状态曾经能直接把徽章压到 D,而当前没有任何一份
// 榜给 F 权重 —— 拿一个不影响排序的维度去给排名结果打分,得到的字母没有含义。
//
// ⚠️ 这个字母**只是 UI 徽章,不决定任何准入** —— 榜单准入由 preset 的
// needs + min_coverage 逐维判定(见 Engine.selectList)。把它当全局闸门用,会让
// 一本 pubdate 被 mtime 污染的 O'Reilly 经典无法进入「明确声明不关心出版日期」的
// timeless 榜(review B1,实测影响全库 23%)。
func Grade(dims map[Dim]DimScore, graded []Dim) string {
	if len(graded) == 0 {
		return "" // 没有任何加权维度 → 无从评级,不编一个字母出来
	}
	allMeasured, anyUnknown, extMeasured := true, false, false
	for _, d := range graded {
		switch dims[d].State {
		case StateMeasured:
			if externalDims[d] {
				extMeasured = true
			}
		case StateShrunk:
			allMeasured = false
		default:
			// unknown,以及「这一维压根没有记录」—— 后者同样意味着它没进排序。
			allMeasured, anyUnknown = false, true
		}
	}
	switch {
	case anyUnknown:
		return "D"
	case allMeasured:
		return "A"
	case extMeasured:
		return "B"
	default:
		return "C"
	}
}
