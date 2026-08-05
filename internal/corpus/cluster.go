package corpus

import (
	"regexp"
	"strings"
)

var editionRe = regexp.MustCompile(`(?i)(,?\s*(second|third|fourth|fifth|sixth)\s+edition|\s+\d+(st|nd|rd|th)\s+edition|,?\s*revised\s+edition)`)
var punctRe = regexp.MustCompile(`[^a-z0-9]+`)

// normalizeTitle 压出可聚类的规范标题:去版本后缀、去标点、折叠空白。
func normalizeTitle(title string) string {
	t := editionRe.ReplaceAllString(strings.ToLower(title), "")
	t = punctRe.ReplaceAllString(t, " ")
	return strings.Join(strings.Fields(t), " ")
}

// firstAuthorSurname 取首个作者的姓氏(work 聚类的第二键)。
func firstAuthorSurname(author string) string {
	a := strings.TrimSpace(author)
	if a == "" {
		return "unknown"
	}
	// 处理 "A & B"、"A, B"、"A and B" 多作者。
	for _, sep := range []string{" & ", " and ", ","} {
		if i := strings.Index(a, sep); i > 0 {
			a = a[:i]
			break
		}
	}
	fields := strings.Fields(a)
	surname := fields[len(fields)-1]
	// 去掉 "Jr." / "III" 等后缀,统一小写。
	return strings.ToLower(strings.TrimSuffix(surname, "."))
}

// WorkKey 产生稳定 work 键:姓氏 + 规范标题。ISBN 缺失时的聚类回退键
// (system-design §4:normalize(title) + 首作者姓氏)。
func WorkKey(title, author string) string {
	return firstAuthorSurname(author) + "/" + normalizeTitle(title)
}
