package score

import "sort"

// CDF 每维的经验累计分布(norm_cdf 的 101 分位点,只由 measured 行构建)。
type CDF struct {
	Dim   Dim
	Quant []float64 // len=101,q=0..100 的 raw 值(线性插值)
	raws  []float64 // 升序的 measured 原始值(精确 mid-rank 用)
}

// BuildCDF 从 measured 原始值构建经验 CDF。并列一律 mid-rank(写死,保可复现)。
func BuildCDF(dim Dim, values []float64) CDF {
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)
	n := len(sorted)
	quant := make([]float64, 101)
	if n == 0 {
		return CDF{Dim: dim, Quant: quant, raws: sorted}
	}
	for q := 0; q <= 100; q++ {
		// 线性插值的分位数
		pos := float64(q) / 100 * float64(n-1)
		lo := int(pos)
		hi := lo + 1
		if hi >= n {
			hi = n - 1
		}
		frac := pos - float64(lo)
		quant[q] = sorted[lo] + (sorted[hi]-sorted[lo])*frac
	}
	return CDF{Dim: dim, Quant: quant, raws: sorted}
}

// MidRankPct 并列一律 mid-rank 的百分位(0–100)。
//
// mid-rank 的定义是 `(#小于 v + #等于 v / 2) / n`。这里的除法必须是浮点的:
// 写成 `equal/2` 的整数除法会把每个奇数并列组截掉半个名次,并让「无并列」的情形
// 整体退化成 min-rank(n=1 时唯一的 measured 值拿 0 分位)。规格把并列规则上升为
// 可复现性条款(system-design §3.3),所以这条公式不能有实现自由度。
func (c CDF) MidRankPct(v float64) float64 {
	n := len(c.raws)
	if n == 0 {
		return 0
	}
	// raws 已升序 → 二分定位,避免逐值 O(n) 扫描(2,054 本 × 7 维时是 O(n²))。
	less := sort.SearchFloat64s(c.raws, v)
	equal := sort.Search(n, func(i int) bool { return c.raws[i] > v }) - less
	return (float64(less) + float64(equal)/2) / float64(n) * 100
}

// PriorPct shrunk 行映射:先验值在 measured CDF 上的位置。
func (c CDF) PriorPct(prior float64) float64 { return c.MidRankPct(prior) }

// Quantile 按分位点取值(展示/复算用)。
func (c CDF) Quantile(q int) float64 {
	if q < 0 {
		q = 0
	}
	if q > 100 {
		q = 100
	}
	return c.Quant[q]
}
