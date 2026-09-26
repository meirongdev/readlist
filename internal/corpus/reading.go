package corpus

// 多版次阅读状态的合并语义 —— 唯一真相源。
//
// 评分引擎(to-read-next 的准入)与 API(页面徽章)此前各有一份合并逻辑:引擎是
// 「按 book_id 最后扫到的那行说了算」,API 是「取最靠前的状态」。calibre-web 里取消
// 「已读」留下的是一行 read_status=0 而不是删行,于是「旧版次已读、新版次显式未读」
// 的书会被引擎判成未读、重新进入阅读队列,页面上却挂着 ✓ 已读。

// ReadStatusRank 阅读状态的合并优先级,数字大 = 更靠前:read > reading > unread > 其他。
// 同一 work 的多个版次取 rank 最大的那个,与版次顺序无关。
func ReadStatusRank(status string) int {
	switch status {
	case "read":
		return 3
	case "reading":
		return 2
	case "unread":
		return 1
	default:
		return 0
	}
}
