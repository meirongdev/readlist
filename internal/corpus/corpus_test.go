package corpus

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/meirongdev/readlist/internal/store"
)

func TestWorkKeyKeepsNonASCIITitles(t *testing.T) {
	// 归一化只保留 [a-z0-9] 时,中文标题会被整条清空,work 键退化成「姓名/」 ——
	// 同一作者的两本中文书于是聚成同一个 work。
	a := WorkKey("Go 语言高并发与微服务实战", "朱洪波")
	b := WorkKey("深入理解计算机系统", "朱洪波")
	require.NotEqual(t, a, b, "同作者的两本中文书不能聚成同一个 work")
	require.NotEqual(t, "朱洪波/go", a, "中文标题不能被清空")
	require.Contains(t, a, "语言高并发与微服务实战")
}

func TestWorkKeyClustersEditionsOfSameWork(t *testing.T) {
	require.Equal(t,
		WorkKey("Fluent Python", "Luciano Ramalho"),
		WorkKey("Fluent Python, 2nd Edition", "Luciano Ramalho"))
	require.Equal(t,
		WorkKey("Learning Go", "Jon Bodner"),
		WorkKey("Learning Go, Second Edition", "Jon Bodner"))
	require.Equal(t,
		WorkKey("深入理解计算机系统(第3版)", "Randal E. Bryant"),
		WorkKey("深入理解计算机系统", "Randal E. Bryant"),
		"中文版次后缀也该被归一掉")
}

func TestWorkKeyUsesFirstAuthorSurname(t *testing.T) {
	require.Equal(t,
		WorkKey("Some Book", "Alan A. A. Donovan & Brian W. Kernighan"),
		WorkKey("Some Book", "Alan A. A. Donovan"))
	require.Equal(t, "unknown/some book", WorkKey("Some Book", ""))
}

func TestPublisherNormalizesVariants(t *testing.T) {
	for _, raw := range []string{"O'Reilly", "O'Reilly Media", "O'Reilly Media, Inc.", "oreilly media"} {
		pi := Publisher(raw)
		require.Equal(t, "O'Reilly Media", pi.Norm, raw)
		require.Equal(t, 1, pi.Tier, raw)
	}
	for _, raw := range []string{"Packt", "Packt Publishing", "Packt Publishing Ltd"} {
		require.Equal(t, "Packt", Publisher(raw).Norm, raw)
		require.Equal(t, 3, Publisher(raw).Tier, raw)
	}
	require.Equal(t, 4, Publisher("").Tier)
	require.Equal(t, "unknown", Publisher("   ").Norm)
}

func TestPublisherHandlesChineseNames(t *testing.T) {
	// 中文社名此前被归一成空串 → 全部落到 tier 4(最低档),T 维被系统性低估。
	for _, raw := range []string{"电子工业出版社", "机械工业出版社"} {
		pi := Publisher(raw)
		require.Equal(t, raw, pi.Norm, raw)
		require.Equal(t, 3, pi.Tier, "有名字的出版社不该掉到 tier 4")
	}
	require.NotEqual(t, Publisher("电子工业出版社").Norm, Publisher("机械工业出版社").Norm)
}

func TestTierScoreIsMonotone(t *testing.T) {
	require.Greater(t, TierScore(1), TierScore(2))
	require.Greater(t, TierScore(2), TierScore(3))
	require.Greater(t, TierScore(3), TierScore(4))
}

func TestFormatHelpersAreCaseInsensitive(t *testing.T) {
	require.Greater(t, FormatRank("EPUB"), FormatRank("PDF"))
	require.Greater(t, FormatRank("PDF"), FormatRank(""))
	require.Equal(t, FormatRank("EPUB"), FormatRank(" epub "))
	require.Equal(t, 1.0, FormatReadability("epub"))
	require.Equal(t, 0.8, FormatReadability("AZW3"))
	require.Equal(t, 0.5, FormatReadability("PDF"))
	require.Equal(t, 0.5, FormatReadability("djvu"))
}

func TestHalfLifeForRuleChain(t *testing.T) {
	// BISAC 码优先:结构化外部事实,免费且准确(system-design §6)。
	bisac := HalfLifeFor("Whatever", "常青/理论",
		[]string{"Computers", "COM060160 - COMPUTERS / Web / Web Programming"})
	require.Equal(t, "rules-bisac", bisac.Source)
	require.Equal(t, "框架/版本", bisac.Class)
	require.Equal(t, 2.5, bisac.Years, "BISAC 应压过 topic_class")

	// 无 BISAC → 用标注的 topic_class。
	labeled := HalfLifeFor("Whatever", "常青/理论", []string{"Computers"})
	require.Equal(t, "rules-topic-class", labeled.Source)
	require.Equal(t, 25.0, labeled.Years)

	// 两者都没有 → 退到标题关键词。
	kw := HalfLifeFor("Kubernetes in Action", "", nil)
	require.Equal(t, "rules-title-keyword", kw.Source)
	require.Equal(t, "平台/生态", kw.Class)
	require.Equal(t, 5.0, kw.Years)

	// 全不命中 → 默认 10 年,来源必须标 default,不能假装是规则命中。
	def := HalfLifeFor("A Book About Nothing In Particular", "不存在的类", nil)
	require.Equal(t, 10.0, def.Years)
	require.Equal(t, "default", def.Source)
	require.Empty(t, def.Class)
}

func TestBisacClassIgnoresFreeTextTags(t *testing.T) {
	// 实测 tags 里 BISAC 码与自由文本混在一起(data-baseline §1.2),
	// 自由文本不能被误认成分类码。
	_, ok := bisacClass([]string{"Computers", "machine learning", "Pragmatic Bookshelf"})
	require.False(t, ok)
	klass, ok := bisacClass([]string{"machine learning", "COM051260 - COMPUTERS / Programming Languages / JavaScript"})
	require.True(t, ok)
	require.Equal(t, "语言核心", klass)
}

func TestSeedIsIdempotent(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/t.db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	n, err := Seed(db)
	require.NoError(t, err)
	require.Greater(t, n, 40)

	again, err := Seed(db)
	require.NoError(t, err)
	require.Zero(t, again, "重复 seed 不该再写入")
}
