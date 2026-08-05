package corpus

import "strings"

// 电子书格式的可读性语义 —— 唯一真相源。
//
// 评分引擎(选"本 work 最优格式")、readability 维度公式、API 展示层此前各有一份
// 拷贝且大小写处理不一致,导致详情页展示的格式可能不是打分用的那个。

// FormatRank 格式的可读性排序,数字大 = 更好读。用于在多版次里挑最优格式。
func FormatRank(format string) int {
	switch strings.ToUpper(strings.TrimSpace(format)) {
	case "EPUB":
		return 4
	case "AZW3", "MOBI":
		return 3
	case "PDF":
		return 2
	default:
		return 1
	}
}

// FormatReadability 格式的可读性系数 0–1(scoring-standard §3 L 维的 format_score)。
func FormatReadability(format string) float64 {
	switch strings.ToUpper(strings.TrimSpace(format)) {
	case "EPUB":
		return 1.0
	case "AZW3", "MOBI":
		return 0.8
	default: // PDF 及其他
		return 0.5
	}
}
