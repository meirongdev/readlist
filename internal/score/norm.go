package score

import "sort"

// CDF 每维的经验累计分布(norm_cdf 的 101 分位点,只由 measured 行构建)。
type CDF struct {
	Dim   Dim
	Quant []float64 // len=101,q=0..100 的 raw 值(线性插值)
	raws  []float64 // 原始 measured 值(用于精确 mid-rank)
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
func (c CDF) MidRankPct(v float64) float64 {
	n := len(c.raws)
	if n == 0 {
		return 0
	}
	less, equal := 0, 0
	for _, r := range c.raws {
		if r < v {
			less++
		} else if r == v {
			equal++
		}
	}
	return float64(less+equal/2) / float64(n) * 100
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
