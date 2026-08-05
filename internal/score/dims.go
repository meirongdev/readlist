package score

import (
	"math"
	"strings"
	"time"

	"github.com/meirongdev/readlist/internal/corpus"
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

// acclaim 口碑:IMDb 式贝叶斯加权(scoring-standard §3 A),多源按 v 加权合并。
func (w *WorkInput) acclaim(p DimParams) DimResult {
	if len(w.Ratings) == 0 {
		return DimResult{Raw: p.AcclaimPrior, State: StateShrunk, Source: "prior"}
	}
	m := math.Max(p.MinCount, 1)
	var num, den float64
	for _, r := range w.Ratings {
		if r.Count <= 0 {
			continue
		}
		v := float64(r.Count)
		a := v/(v+m)*r.Rating + m/(v+m)*p.AcclaimPrior
		num += a * v
		den += v
	}
	if den == 0 {
		return DimResult{Raw: p.AcclaimPrior, State: StateShrunk, Source: "prior"}
	}
	return DimResult{Raw: num / den, State: StateMeasured, Source: "external-ratings", Count: int(den)}
}

// community 技术圈声量:时间衰减提及数(scoring-standard §3 C,τ=4 年)。
func (w *WorkInput) community(now time.Time) DimResult {
	tau := 4.0
	var sum float64
	for _, m := range w.Mentions {
		age := now.Sub(m.CreatedAt).Hours() / (24 * 365)
		if age < 0 {
			age = 0
		}
		sum += 1 / (1 + age/tau)
	}
	if len(w.Mentions) == 0 {
		return DimResult{Raw: 0, State: StateShrunk, Source: "prior"}
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
		return DimResult{Raw: value, State: StateMeasured, Source: "llm-label"}
	}
	if conf >= 0.5 {
		return DimResult{Raw: value, State: StateShrunk, Source: "llm-label-lowconf"}
	}
	return DimResult{Raw: 50, State: StateUnknown, Source: "no-label"}
}

// readability 馆藏可读性:纯本地 100% 可算(scoring-standard §3 L)。
func (w *WorkInput) readability() DimResult {
	fs := map[string]float64{"EPUB": 1.0, "AZW3": 0.8, "MOBI": 0.8}[strings.ToUpper(w.Format)]
	if fs == 0 {
		fs = 0.5 // PDF 及其他
	}
	var raw float64
	raw += 30 * fs
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

// Grade 证据等级徽章(system-design §2):
// A=全维 measured,B=有 shrunk,C=主要靠本地,D=关键维(F/T) unknown。
func Grade(dims map[Dim]DimScore) string {
	if dims[DimFreshness].State == StateUnknown || dims[DimTrust].State == StateUnknown {
		return "D"
	}
	allMeasured := true
	for _, d := range AllDims {
		if dims[d].State != StateMeasured {
			allMeasured = false
			break
		}
	}
	if allMeasured {
		return "A"
	}
	if dims[DimAcclaim].State == StateMeasured || dims[DimCommunity].State == StateMeasured {
		return "B"
	}
	return "C"
}
