-- readlist 真相源 schema —— 对应 docs/system-design.md §8
-- 三层:facts(实体+证据) → judgement(dim_scores+norm_cdf) → selection(lists)

-- ── 实体层 ────────────────────────────────────────────────────────────
CREATE TABLE works (
  work_id          TEXT PRIMARY KEY,          -- 聚类产生的稳定键(ISBN族/标题+首作者)
  canonical_title  TEXT NOT NULL,
  first_author     TEXT,
  ol_work_id       TEXT,
  primary_topic    TEXT,
  level            TEXT,
  half_life_years  REAL,
  half_life_source TEXT
);

CREATE TABLE editions (
  book_id             INTEGER PRIMARY KEY,    -- calibre metadata.db 的 id(可能漂移)
  work_id             TEXT NOT NULL REFERENCES works(work_id),
  title               TEXT NOT NULL,
  isbn13              TEXT,
  google_volume_id    TEXT,
  publisher_raw       TEXT,
  publisher_norm      TEXT,
  format              TEXT,
  language            TEXT,
  has_comments        INT NOT NULL DEFAULT 0,
  has_cover           INT NOT NULL DEFAULT 0,
  pubdate             TEXT,
  pubdate_source      TEXT,   -- file-meta|google|openlibrary|mtime-fallback|unknown
  personal_rating_stars REAL
);
CREATE INDEX idx_editions_work ON editions(work_id);

-- ── facts 层(跨 run 复用,最贵)──────────────────────────────────────
CREATE TABLE evidence (                        -- 外部响应原样存
  source TEXT, source_id TEXT, work_id TEXT, payload TEXT,
  fetched_at TEXT, ttl_days INT,
  PRIMARY KEY (source, source_id)
);

CREATE TABLE labels (                          -- LLM/人工标注 + 输入指纹去重
  work_id TEXT PRIMARY KEY, topic_class TEXT, topics TEXT, level TEXT,
  depth REAL, practicality REAL, confidence REAL,
  input_fingerprint TEXT, labeled_by TEXT, labeled_at TEXT
);

CREATE TABLE mentions (                        -- HN 命中,保留 objectID 供人工否决
  work_id TEXT, object_id TEXT, created_at TEXT, matched_by TEXT,
  PRIMARY KEY (work_id, object_id)
);

CREATE TABLE overrides (                       -- 人工 pin/veto(不可再生,NFR-16)
  work_id TEXT, field TEXT, value TEXT, reason TEXT, at TEXT,
  PRIMARY KEY (work_id, field)
);

CREATE TABLE publisher_map (                   -- 出版社归一 + tier
  raw TEXT PRIMARY KEY, norm TEXT NOT NULL, tier INT NOT NULL
);

CREATE TABLE title_whitelist (                 -- ≤2 词标题的 HN 白名单
  work_id TEXT PRIMARY KEY, reason TEXT
);

-- ── judgement 层(run-scoped)────────────────────────────────────────
CREATE TABLE runs (
  run_id TEXT PRIMARY KEY, kind TEXT, corpus_id TEXT, standard_version TEXT,
  facts_hash TEXT, started_at TEXT, ended_at TEXT, status TEXT,
  ok_count INT, fail_count INT, orphan_rows INT, quota_used TEXT, metrics TEXT
);

CREATE TABLE dim_scores (
  run_id TEXT, work_id TEXT, dim TEXT,
  raw REAL, pct REAL, score REAL,
  state TEXT NOT NULL,                         -- measured|shrunk|unknown
  source TEXT, confidence REAL,
  PRIMARY KEY (run_id, work_id, dim)
);
CREATE INDEX idx_dims_work ON dim_scores(run_id, work_id);
CREATE INDEX idx_dims_dim  ON dim_scores(run_id, dim);

CREATE TABLE norm_cdf (                        -- 版本化经验 CDF(只由 measured 构建)
  run_id TEXT, dim TEXT, q INT, raw REAL,
  PRIMARY KEY (run_id, dim, q)
);

-- ── selection 层(书单产物)──────────────────────────────────────────
CREATE TABLE lists (
  run_id TEXT, list_id TEXT, rank INT, work_id TEXT,
  tbs REAL, coverage REAL, reason TEXT,
  PRIMARY KEY (run_id, list_id, rank)
);
CREATE INDEX idx_lists_work ON lists(run_id, list_id, work_id);

-- ── 只读镜像 ─────────────────────────────────────────────────────────
CREATE TABLE reading (                         -- app.db 最小导出,永不写回
  book_id INTEGER PRIMARY KEY, status TEXT, shelves TEXT,
  downloads INT, last_modified TEXT
);

CREATE TABLE published_run (                   -- 原子发布指针
  id INT PRIMARY KEY CHECK (id = 1), run_id TEXT NOT NULL
);
