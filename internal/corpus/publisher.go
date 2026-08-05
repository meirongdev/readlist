package corpus

import "strings"

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

// normalize 把出版社名压成可匹配的规范形:小写、去标点/分隔符、去后缀词。
func normalize(raw string) string {
	s := strings.ToLower(raw)
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if r == '&' {
			b.WriteString("and")
		}
	}
	return b.String()
}

// match 检查 raw 的规范形是否包含 key(以及 key 的常见变体)。
func match(raw, key string) bool {
	n := normalize(raw)
	if n == key {
		return true
	}
	// 处理连写/空格差异:直接子串匹配(规范形已去分隔符)。
	return strings.Contains(n, key)
}

// Publisher 把原始出版社名归一为规范名 + tier。
// 变体合并(scoring-standard §3 T 维):Packt 4 变体→1、O'Reilly 2→1、BPB 2→1。
func Publisher(raw string) PublisherInfo {
	r := strings.TrimSpace(raw)
	if r == "" {
		return PublisherInfo{Norm: "unknown", Tier: 4}
	}
	tier1 := []string{"oreilly", "manning", "pragmatic", "nostarch", "nostarchpress", "mitpress", "addisonwesley", "addisonwesleyprofessional"}
	tier2 := []string{"apress", "crc", "taylorfrancis", "wiley", "springer", "simonschuster"}
	tier3 := []string{"packt", "bpb", "oreillymedia", "pearson", "sams", "elsevier", "osborne", "sypress"}

	var pi PublisherInfo
	norm := r
	normOK := false
	tier := 4
	for _, k := range tier1 {
		if match(r, k) {
			norm, tier, normOK = canonicalName(k), 1, true
			break
		}
	}
	if !normOK {
		for _, k := range tier2 {
			if match(r, k) {
				norm, tier, normOK = canonicalName(k), 2, true
				break
			}
		}
	}
	if !normOK {
		for _, k := range tier3 {
			if match(r, k) {
				norm, tier, normOK = canonicalName(k), 3, true
				break
			}
		}
	}
	if !normOK {
		// 未知但有名字的技术出版社 → tier 3;完全空 → tier 4。
		if len(normalize(r)) > 0 {
			norm, tier = strings.TrimSpace(raw), 3
		} else {
			norm, tier = "unknown", 4
		}
	}
	pi.Norm = norm
	pi.Tier = tier
	return pi
}

func canonicalName(key string) string {
	switch key {
	case "oreilly", "oreillymedia":
		return "O'Reilly Media"
	case "manning":
		return "Manning"
	case "pragmatic":
		return "Pragmatic Bookshelf"
	case "nostarch", "nostarchpress":
		return "No Starch Press"
	case "mitpress":
		return "MIT Press"
	case "addisonwesley", "addisonwesleyprofessional":
		return "Addison-Wesley"
	case "apress":
		return "Apress"
	case "crc", "taylorfrancis":
		return "CRC / Taylor & Francis"
	case "wiley":
		return "Wiley"
	case "springer":
		return "Springer"
	case "simonschuster":
		return "Simon & Schuster"
	case "packt":
		return "Packt"
	case "bpb":
		return "BPB"
	case "pearson":
		return "Pearson"
	case "sams":
		return "SAMS"
	case "elsevier":
		return "Elsevier"
	case "osborne":
		return "Osborne"
	case "sypress":
		return "Sybex"
	default:
		return key
	}
}
