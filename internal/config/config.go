package config

import (
	"os"
	"strconv"
)

// Config 集中管理 readlist 的运行配置(全部可用环境变量覆盖)。
type Config struct {
	DBPath        string
	APIListenAddr string
	StandardVer   string
	// KeepRuns 保留多少个历史 run(回滚窗口)。每夜一个 run 的产物约 1.5 万行,
	// 不回收会在几个月内把 PVC 写满。
	KeepRuns int
	// ExposeReadStatus 是否对外输出阅读状态与个人评分(公开站可关)。
	ExposeReadStatus bool

	// ── snapshot 命令:calibre 的两个源库(只有快照 CronJob 挂这两个卷)──
	// MetadataDB 是 WAL 库,需 RW 挂载(WAL 的读路径也要写 -shm);
	// AppDB 含密码 hash 与 OIDC 凭据,只读且只导出 3 张表。
	SourceMetadataDB string
	SourceAppDB      string
	SnapshotDir      string // VACUUM INTO 的落点(readlist 自己的卷)
	CalibreUserID    int    // 只取库主人;Guest 与其他账号不参与(NFR-15)

	// ── ingest 命令:外部证据源 ──
	GoogleBooksKey  string // 可选;匿名配额是按共享项目算的,很容易已被打满
	GoogleBooksBase string
	OpenLibraryBase string
	HNSearchBase    string
	IngestBudget    int // 本次运行最多发多少个外部请求(配额闸门)
	// IngestMentionsReserve 预算里给 HN 声量查询保底预留的请求数;0 = 自动(Budget/4)。
	// editions 阶段烧到只剩保底线就让位 —— 否则 bootstrap 后的头几晚 HN 一次都
	// 轮不到,C 维恒为 0,timeless 榜(needs C)持续为空。
	IngestMentionsReserve int
	// TitleWhitelistFile 主标题白名单文件路径(每行一个主标题,# 注释、空行忽略)。
	// 主标题 ≤2 词的书默认不查 HN("Clean Code" 这类短语误认代价太高),进名单才放行。
	TitleWhitelistFile string
}

func Load() Config {
	return Config{
		DBPath:           getenv("DB_PATH", "readlist.db"),
		APIListenAddr:    getenv("API_LISTEN_ADDR", ":8080"),
		StandardVer:      getenv("STANDARD_VERSION", "1.0"),
		KeepRuns:         getenvInt("KEEP_RUNS", 5),
		ExposeReadStatus: getenv("EXPOSE_READ_STATUS", "true") == "true",

		SourceMetadataDB: getenv("SOURCE_METADATA_DB", "/library-src/metadata.db"),
		SourceAppDB:      getenv("SOURCE_APP_DB", "/config-src/app.db"),
		SnapshotDir:      getenv("SNAPSHOT_DIR", "/data/snapshot"),
		CalibreUserID:    getenvInt("CALIBRE_USER_ID", 1),

		GoogleBooksKey:        os.Getenv("GOOGLE_BOOKS_KEY"),
		GoogleBooksBase:       getenv("GOOGLE_BOOKS_BASE", "https://www.googleapis.com/books/v1"),
		OpenLibraryBase:       getenv("OPENLIBRARY_BASE", "https://openlibrary.org"),
		HNSearchBase:          getenv("HN_SEARCH_BASE", "https://hn.algolia.com/api/v1"),
		IngestBudget:          getenvInt("INGEST_BUDGET", 800),
		IngestMentionsReserve: getenvInt("MENTIONS_RESERVE", 0),
		TitleWhitelistFile:    os.Getenv("TITLE_WHITELIST_FILE"),
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return def
}
