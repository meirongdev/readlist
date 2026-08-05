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

// available 判断该维是否可参与加权:非 unknown 且满足 needs 要求。
func (n Needs) available(dim Dim, state State) bool {
	if state == StateUnknown {
		return false
	}
	if need, ok := n[dim]; ok && !StateAtLeast(state, need) {
		return false
	}
	return true
}

// NeedsSatisfied 判断一本书是否通过 preset 的 needs 硬门。
//
// needs 声明的每一维都必须达标,**无论它在不在 weights 里** —— 之前 needs 只在
// Combine 内部对加权维度生效,于是 `new-2026` 的 `needs: {F: measured}`
// (F 不参与加权,只用来要求「出版日期可信」)是一个静默的空操作。
// system-design §2 的原话是「准入 = 该 preset 用到的维度都达标」。
func NeedsSatisfied(dims map[Dim]DimScore, needs Needs) bool {
	for dim, need := range needs {
		if !StateAtLeast(dims[dim].State, need) {
			return false
		}
	}
	return true
}

// CombineResult 逐本权重 renormalize 的结果。
type CombineResult struct {
	Available []Dim
	Missing   []Dim
	Coverage  float64
	TotalDims int // 该 preset 加权的维度总数(理由串里「按 N/M 维评出」的 M)
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
	cr := CombineResult{TotalDims: len(weights)}
	// 按 AllDims 的固定顺序遍历,而不是遍历 weights map:map 迭代序是随机的,
	// 会让 Available/Missing 的顺序(进而理由串里「缺:…」的文案)每次运行都不同。
	for _, dim := range AllDims {
		w, weighted := weights[dim]
		if !weighted {
			continue
		}
		ds, ok := dims[dim]
		if !ok || !needs.available(dim, ds.State) {
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
