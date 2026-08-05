package config

import "os"

// Config 集中管理 readlist 的运行配置(全部可用环境变量覆盖)。
type Config struct {
	DBPath        string
	APIListenAddr string
	StandardVer   string
	// 生产快照入口:calibre 两个源库(只在 snapshot 命令用,MVP 阶段以 seed 代替)。
	SourceMetadataDB string
	SourceAppDB      string
	// 数据暴露开关
	ExposeReadStatus bool
}

func Load() Config {
	return Config{
		DBPath:           getenv("DB_PATH", "readlist.db"),
		APIListenAddr:    getenv("API_LISTEN_ADDR", ":8080"),
		StandardVer:      getenv("STANDARD_VERSION", "1.0"),
		SourceMetadataDB: os.Getenv("SOURCE_METADATA_DB"),
		SourceAppDB:      os.Getenv("SOURCE_APP_DB"),
		ExposeReadStatus: getenv("EXPOSE_READ_STATUS", "true") == "true",
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
