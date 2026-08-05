package corpus

import (
	"strings"
	"unicode"
)

// PublisherInfo 归一化后的出版社及其层级。
type PublisherInfo struct {
	Norm string
	Tier int
}

// TierScore 出版社层级 → 0-100 分(scoring-standard §3 T 维)。
func TierScore(tier int) float64 {
	switch tier {
	case 1:
		return 100
	case 2:
		return 75
	case 3:
		return 50
	default:
		return 25
	}
}

// publisherTiers 出版社层级表(scoring-standard §3 T 维)。key 是规范形的子串,
// 首个命中生效 —— 所以 "O'Reilly"/"O'Reilly Media, Inc."/"oreilly media" 会
// 归并到同一档(Packt 4 变体 → 1、O'Reilly 2 → 1、BPB 2 → 1)。
var publisherTiers = []struct {
	key       string
	canonical string
	tier      int
}{
	{"oreilly", "O'Reilly Media", 1},
	{"manning", "Manning", 1},
	{"pragmatic", "Pragmatic Bookshelf", 1},
	{"nostarch", "No Starch Press", 1},
	{"mitpress", "MIT Press", 1},
	{"addisonwesley", "Addison-Wesley", 1},
	{"apress", "Apress", 2},
	{"crc", "CRC / Taylor & Francis", 2},
	{"taylorfrancis", "CRC / Taylor & Francis", 2},
	{"wiley", "Wiley", 2},
	{"springer", "Springer", 2},
	{"simonandschuster", "Simon & Schuster", 2},
	{"packt", "Packt", 3},
	{"bpb", "BPB", 3},
	{"pearson", "Pearson", 3},
	{"sams", "SAMS", 3},
	{"elsevier", "Elsevier", 3},
	{"osborne", "Osborne", 3},
	{"sybex", "Sybex", 3},
}

// normalize 把出版社名压成可匹配的规范形:小写、去标点/分隔符。
//
// 保留全部 Unicode 字母数字:只保留 [a-z0-9] 会让「电子工业出版社」的规范形变成
// 空串,于是所有中文出版社都掉进 tier 4(最低档),T 维被系统性低估。
func normalize(raw string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(raw) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '&':
			b.WriteString("and")
		}
	}
	return b.String()
}

// Publisher 把原始出版社名归一为规范名 + tier。
func Publisher(raw string) PublisherInfo {
	n := normalize(raw)
	if n == "" {
		return PublisherInfo{Norm: "unknown", Tier: 4}
	}
	for _, p := range publisherTiers {
		if strings.Contains(n, p.key) {
			return PublisherInfo{Norm: p.canonical, Tier: p.tier}
		}
	}
	// 未在表内但确实有名字的出版社 → tier 3(表外不等于无出版社)。
	return PublisherInfo{Norm: strings.TrimSpace(raw), Tier: 3}
}
