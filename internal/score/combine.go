package score

// Band 目标带(facet 维度:偏离目标扣分,而非单调加权)。
type Band struct {
	Target float64
	Tol    float64
}

// BandScore band_score(x; target, tol) = 100·max(0, 1 − |x−target|/tol)。
func BandScore(x float64, b Band) float64 {
	if b.Tol <= 0 {
		return 0
	}
	v := 100 * (1 - abs(x-b.Target)/b.Tol)
	if v < 0 {
		return 0
	}
	return v
}

// Needs preset 对每维的最低证据要求(如 {A: measured})。
type Needs map[Dim]State

// available 判断该维是否可用:非 unknown 且满足 needs 要求。
func (n Needs) available(dim Dim, state State) bool {
	if state == StateUnknown {
		return false
	}
	if need, ok := n[dim]; ok && !StateAtLeast(state, need) {
		return false
	}
	return true
}

// CombineResult 逐本权重 renormalize 的结果。
type CombineResult struct {
	Available []Dim
	Missing   []Dim
	Coverage  float64
	TBS       float64
}

// Combine 按 preset 权重/band/needs 计算单本综合分:
// coverage = Σ_{可用} w / Σ_all w;TBS = Σ_{可用} w·eff / coverage。
// eff = band_score(pct) 若 dim 在 bands,否则 pct。
func Combine(dims map[Dim]DimScore, weights map[Dim]float64, bands map[Dim]Band, needs Needs) CombineResult {
	var totalW, availW, acc float64
	for _, w := range weights {
		totalW += w
	}
	cr := CombineResult{}
	for dim, w := range weights {
		ds, ok := dims[dim]
		if !ok {
			cr.Missing = append(cr.Missing, dim)
			continue
		}
		if !needs.available(dim, ds.State) {
			cr.Missing = append(cr.Missing, dim)
			continue
		}
		eff := ds.Score
		if b, isBand := bands[dim]; isBand {
			eff = BandScore(ds.Score, b)
		}
		acc += w * eff
		availW += w
		cr.Available = append(cr.Available, dim)
	}
	if totalW == 0 {
		return cr
	}
	cr.Coverage = availW / totalW
	if cr.Coverage > 0 {
		cr.TBS = acc / cr.Coverage
	}
	return cr
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
