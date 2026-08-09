package corpus

// PubdateSourcePriority 出版日期来源的优先级(数字大 = 更可信/更精确)。
//
// snapshot(本包)与 ingest(facts 包)**必须共用**这一份:两边各持一份口径迟早漂移。
// 这不是假想 —— 2026-08 之前 snapshot 的 upsert 无条件用 calibre 值覆写
// pubdate/pubdate_source,ingest 辛苦查回的 google 日期活不过次日凌晨的快照,
// F 维(时效)的实测覆盖每天被清空一次,「近一年新书」榜因此结构性为空。
//
// 语义:同优先级取新值(快照对 calibre 自己的修订要跟进),低优先级不许覆盖高优先级
// (facts.writePubdate 与 corpus.Import 都执行这条)。
var PubdateSourcePriority = map[string]int{
	"google":         5,
	"openlibrary":    4,
	"file-meta":      3,
	"calibre":        2,
	"mtime-fallback": 1,
	"unknown":        0,
	"":               0,
}
