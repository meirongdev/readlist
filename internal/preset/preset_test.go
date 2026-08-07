package preset

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadEmbeddedPresetsAreValid(t *testing.T) {
	presets, err := Load()
	require.NoError(t, err)
	require.NotEmpty(t, presets)

	ids := map[string]bool{}
	for _, p := range presets {
		require.False(t, ids[p.ID], "preset id %q 重复", p.ID)
		ids[p.ID] = true
		require.NotEmpty(t, p.Name, "preset %s 缺 name", p.ID)

		var sum float64
		for _, w := range p.Weights {
			sum += w
		}
		require.InDelta(t, 1.0, sum, weightSumTolerance, "preset %s 权重和不为 1", p.ID)

		// 每个 band 维度都必须**占一份非零权重**,否则 band 项的系数是 0 = 空操作,
		// 「目标带」这个卖点在公式上根本不成立(review B4:曾有一份榜从落地第一天
		// 起就是这样,声明了 D 的目标带却没给 D 权重)。
		for dim := range p.Bands {
			require.Greater(t, p.Weights[dim], 0.0,
				"preset %s 的 band 维度 %s 没有非零权重,band 项系数为 0", p.ID, dim)
		}
	}
	require.True(t, ids["timeless"])
	require.True(t, ids["library-hygiene"])
}

func base() Preset {
	return Preset{
		ID:      "x",
		Weights: map[string]float64{"A": 0.5, "T": 0.5},
		Select:  SelectConfig{Size: 10, MinCoverage: 0.5},
	}
}

func TestValidateRejectsBadConfig(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Preset)
		want   string
	}{
		{"权重和不为 1", func(p *Preset) { p.Weights["A"] = 0.9 }, "必须为 1"},
		{"未知维度", func(p *Preset) { p.Weights = map[string]float64{"X": 1.0} }, "未知维度"},
		{"负权重", func(p *Preset) { p.Weights = map[string]float64{"A": 1.5, "T": -0.5} }, "必须为正"},
		{"band 无权重", func(p *Preset) {
			p.Bands = map[string]Band{"D": {Target: 50, Tol: 20}}
		}, "空操作"},
		{"band tol 为 0", func(p *Preset) {
			p.Weights = map[string]float64{"A": 0.5, "D": 0.5}
			p.Bands = map[string]Band{"D": {Target: 50}}
		}, "tol 必须为正"},
		{"needs 未知维度", func(p *Preset) { p.Needs = map[string]string{"Z": "measured"} }, "未知维度"},
		{"needs 非法状态", func(p *Preset) { p.Needs = map[string]string{"A": "maybe"} }, "不是合法证据状态"},
		{"coverage 越界", func(p *Preset) { p.Select.MinCoverage = 1.5 }, "min_coverage"},
		{"order 非法", func(p *Preset) { p.Order = "random" }, "只能是 desc 或 asc"},
		{"visibility 非法", func(p *Preset) { p.Visibility = "secret" }, "只能是 public 或 internal"},
		{"个人评分单位错", func(p *Preset) { p.Filters.MinPersonalRating = 8 }, "单位是星"},
		{"年份不像年份", func(p *Preset) { p.Filters.PubdateYear = 26 }, "不像一个年份"},
		{"缺 id", func(p *Preset) { p.ID = "" }, "id 必填"},
		{"缺 weights", func(p *Preset) { p.Weights = nil }, "weights 必填"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base()
			tc.mutate(&p)
			err := p.Validate()
			require.Error(t, err, "这份配置本该被拒绝")
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestValidateAcceptsGoodConfig(t *testing.T) {
	p := base()
	p.Weights = map[string]float64{"A": 0.4, "D": 0.3, "readability": 0.3}
	p.Bands = map[string]Band{"D": {Target: 60, Tol: 30}}
	p.Needs = map[string]string{"A": "measured", "F": "measured"} // F 未加权也可作硬门
	p.Order = "asc"
	p.Visibility = "internal"
	p.Filters = Filters{MinPersonalRating: 4, PubdateWithinMonths: 12}
	require.NoError(t, p.Validate())
	require.Less(t, math.Abs(p.Weights["A"]+p.Weights["D"]+p.Weights["readability"]-1), weightSumTolerance)
}
