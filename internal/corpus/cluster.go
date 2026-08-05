package corpus

import (
	"regexp"
	"strings"
	"unicode"
)

var editionRe = regexp.MustCompile(`(?i)(,?\s*(second|third|fourth|fifth|sixth)\s+edition|\s+\d+(st|nd|rd|th)\s+edition|,?\s*revised\s+edition)`)

// cnEditionRe 中文版次后缀（第3版 / 第三版 / （第 2 版））。
var cnEditionRe = regexp.MustCompile(`第\s*[0-9一二三四五六七八九十]+\s*版`)

// normalizeTitle 压出可聚类的规范标题:去版次后缀、标点折成空白、折叠空白。
//
// 保留全部 Unicode 字母与数字:只保留 [a-z0-9] 会把中文标题整条清空,于是
// 《Go 语言高并发与微服务实战》的 work 键退化成 "朱洪波/go",同一作者的两本
// 中文书会被错误合并成一个 work。
func normalizeTitle(title string) string {
	t := strings.ToLower(title)
	t = editionRe.ReplaceAllString(t, "")
	t = cnEditionRe.ReplaceAllString(t, " ")
	var b strings.Builder
	for _, r := range t {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
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
	if len(fields) == 0 {
		return "unknown"
	}
	surname := fields[len(fields)-1]
	// 去掉 "Jr." / "III" 等后缀,统一小写。
	return strings.ToLower(strings.TrimSuffix(surname, "."))
}

// WorkKey 产生稳定 work 键:姓氏 + 规范标题。ISBN 缺失时的聚类回退键
// (system-design §4:normalize(title) + 首作者姓氏)。
func WorkKey(title, author string) string {
	return firstAuthorSurname(author) + "/" + normalizeTitle(title)
}
