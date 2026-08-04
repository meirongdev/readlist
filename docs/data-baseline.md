# 数据基线实测

> 日期: 2026-08-04
> 对象: 生产 calibre-web（oracle-k3s / `personal-services` 命名空间）的两个 SQLite 库
> 性质: **实测，不是估计。** 本项目所有设计决定的地基。

全部查询都是**只读**（`?mode=ro`），在运行中的 calibre-web pod 内执行，未做任何写入。

```bash
# 所有本文查询的通用前缀
POD=$(kubectl --context oracle-k3s -n personal-services get pod -l app=calibre-web \
      -o jsonpath='{.items[0].metadata.name}')
kubectl --context oracle-k3s -n personal-services exec "$POD" -c calibre-web -- \
  sqlite3 "file:/calibre-library/metadata.db?mode=ro" "SELECT COUNT(*) FROM books;"
```

**两个库、两个 PVC、两种性质**，这是最容易搞错的前提：

| 库 | 路径 | PVC | journal_mode | 装什么 |
|----|------|-----|--------------|--------|
| 书库 | `/calibre-library/metadata.db` | `calibre-books-local` | **wal** | 书目、作者、出版社、标签、标识符、简介 |
| 应用库 | `/config/app.db` | `calibre-web-automated-config-local` | delete | **阅读状态**、书架、下载记录、用户与凭据 |

---

## 1. 书库（`metadata.db`）

| 指标 | 实测值 | 设计含义 |
|------|--------|---------|
| 总藏书 | **2,054** | 规模小 → 全量重算秒级，**不需要增量算分** |
| 库文件大小 | 4.4 MB，`journal_mode=wal` | ⚠️ WAL → 只读挂载不能直接查（[architecture.md](architecture.md) §取法） |
| 有 ISBN | **715**（34.8%；isbn13 711、isbn10 1、异常长度 3） | 只有 1/3 能靠 ISBN 精确匹配外部数据 |
| 有 `google` identifier | **310** | 白捡的：可直接取 Google Books volume（含 averageRating / ratingsCount / categories / pageCount） |
| 有 `asin` | 106 | Amazon 无公开 API（PA-API 需联盟资质）→ 只能存链接 |
| 其他标识符 | `bookid` 73 · `pub-id` 52 · `doi` 48 · `pub-identifier` 47 · `mobi-asin` 13 | 对外部匹配无用 |
| **库内评分**（`books_ratings_link`） | **14** | ⚠️ 本地评分维度**等于不存在**，口碑必须全靠外部源 |
| 有简介（`comments`） | 982（47.8%） | LLM 标注对另一半书只能靠"标题 + 出版社 + 文件名" |
| 有出版社 | 1,356（66%） | 覆盖最好的强信号，但**名字未归一**（见下） |
| 有标签 | 457（22.2%），757 个不同标签 | 覆盖太低，不能作主分类依据 |
| 有系列 | 1 | 系列信息不可用 |
| 语言 | eng 1,726 · zho 20 · spa 5 · fra 2 · ita 2 · 其他 4，**约 296 本无语言字段** | 榜单默认按 eng 出，中文书单独小榜 |
| 格式 | EPUB 1,517 · PDF 542 · MOBI 3 · AZW3 1 | 格式进"馆藏可读性"维度（EPUB 重排好 > PDF） |
| 作者为 Unknown/Anonymous | **252** | 无法做作者权威度，且外部匹配几乎必错 → 直接降级 |
| **Calibre 自定义列**（`custom_columns`） | **0 个** | 没有 `#read` 之类自定义列 → **别指望从书库侧读阅读状态** |

### 1.1 出版社分布 —— 最强信号，但需要先归一化

| 出版社（原样） | 本数 |
|---|---|
| Apress | 145 |
| O'Reilly Media, Inc. | 125 |
| Packt Publishing Pvt Ltd | 83 |
| BPB Publications | 75 |
| Packt Publishing Ltd | 64 |
| O'Reilly Media | 50 |
| Packt | 48 |
| Packt Publishing | 43 |
| Simon and Schuster | 40 |
| CRC Press | 30 |
| BPB Online LLP | 27 |
| Manning Publications Co. | 27 |

⚠️ **Packt 有 4 个变体（合计 238）、O'Reilly 有 2 个（175）、BPB 有 2 个（102）。**
不归一化就评级，等于把同一家出版社拆成四家分别打分 → 需要 `publisher_map`
（[requirements.md](requirements.md) FR-15）。

技术书出版社的质量分层明显且覆盖 66%，这是本库**最可靠的一维**。

### 1.2 标签是 BISAC 码 —— 覆盖低但机器可读

Top 标签：

```
Computers 264 · Pragmatic Bookshelf 17 · COM004000 - COMPUTERS / Intelligence (AI) & Semantics 13
Business & Economics 10 · COM060160 - COMPUTERS / Web / Web Programming 10 · machine learning 9
COM051000 - COMPUTERS / Programming / General 7 · COM042000 - COMPUTERS / NLP 6
COM051260 - COMPUTERS / Programming Languages / JavaScript 6 · Artificial Intelligence 5 ...
```

大量标签是 BISAC 分类码。**这是好事** —— 结构化、可解析出主题层级，比自由文本标签可靠。
但只覆盖 22%，只能作为 LLM 主题标注的**辅助输入**，不能当主分类。

---

## 2. ⚠️ 发现一：`pubdate` 已被 mtime 污染

```bash
kubectl --context oracle-k3s -n personal-services exec "$POD" -c calibre-web -- \
  sqlite3 "file:/calibre-library/metadata.db?mode=ro" \
  "SELECT substr(pubdate,1,4) y, COUNT(*) c FROM books GROUP BY 1 ORDER BY c DESC LIMIT 12;"
```

| 年 | 本数 |
|----|------|
| **2026** | **477** |
| 2021 | 392 |
| 2022 | 391 |
| 2023 | 364 |
| 2025 | 180 |
| 2024 | 118 |
| 2020 | 66 |
| 2019 | 23 |
| 2018 | 20 |
| 2017 | 11 |
| `0101`（占位） | 5 |
| 2016 | 3 |

**2026 年才过去 7 个月，却有 477 本"2026 年出版"。**

成因明确：homelab 在 2026-07 做过一次元数据补全，其最后阶段用**文件 mtime 兜底**填出版日期。
所以 `pubdate` 里混着**导入时间**。表面上"只有 5 本缺日期"是假象 —— 缺失被换成了错误值，
而错误值比缺失更危险，因为它不报警。

### 后果

时效维度如果直接吃 `pubdate`，会把几百本老书当成新书顶上榜首，**整个榜的可信度一次性崩掉**。
"今年新书"榜会变成"今年导入的书"榜。

### 处置（必须在时效维度上线前做完）

1. 建 `pubdate_source` 字段：`file-meta` / `google` / `openlibrary` / `mtime-fallback` / `unknown`。
   历史数据无法回溯来源 → **重跑一遍富集并记录来源**（对 715 个 ISBN + 310 个 Google volume id
   命中率应该高）。
2. 来源为 `mtime-fallback` / `unknown` 的书：时效分记为 **"未知"而非某个数值**，
   证据等级压到 C，**不进任何按时效排序的榜**。
3. 交叉校验兜底：`pubdate` 年 == 文件 mtime 年，且该书有 ISBN 但外部源查不到 → 标记可疑。

---

## 3. 应用库（`app.db`）—— 阅读状态在这里

```bash
kubectl --context oracle-k3s -n personal-services exec "$POD" -c calibre-web -- \
  sqlite3 "file:/config/app.db?mode=ro" ".tables"
```

```
archived_book  book_read_link  book_shelf_link  bookmark  downloads
kobo_annotation_sync  kobo_bookmark  kobo_reading_state  kobo_statistics
kobo_synced_books  kosync_progress  magic_shelf  oauthProvider  registration
remote_auth_token  settings  shelf  shelf_archive  thumbnail  user  user_session ...
```

阅读状态表（实测 schema）：

```sql
CREATE TABLE book_read_link (
    id INTEGER NOT NULL, book_id INTEGER, user_id INTEGER,
    read_status INTEGER NOT NULL,          -- calibre-web 枚举: 0=未读 1=已读 2=在读
    last_modified DATETIME, last_time_started_reading DATETIME,
    times_started_reading INTEGER NOT NULL, PRIMARY KEY (id),
    FOREIGN KEY(user_id) REFERENCES user (id));
```

`book_read_link.book_id` 与 `metadata.db.books.id` 是**同一个 id 空间**，可直接 join。

### 3.1 ⚠️ 发现二：阅读状态只覆盖 23 / 2,054 本

```bash
kubectl --context oracle-k3s -n personal-services exec "$POD" -c calibre-web -- sqlite3 <<'SQL'
ATTACH "file:/config/app.db?mode=ro" AS app;
ATTACH "file:/calibre-library/metadata.db?mode=ro" AS lib;
SELECT "by_status", read_status, COUNT(*) FROM app.book_read_link GROUP BY 2;
SELECT "orphans", COUNT(*) FROM app.book_read_link r
  LEFT JOIN lib.books b ON b.id=r.book_id WHERE b.id IS NULL;
SELECT "union_signals", COUNT(*) FROM (
  SELECT book_id FROM app.book_read_link WHERE read_status=1
  UNION SELECT book_id FROM app.downloads
  UNION SELECT book_id FROM app.kobo_reading_state) s
  JOIN lib.books b ON b.id=s.book_id;
SQL
```

| 信号 | 实测值 | 说明 |
|------|--------|------|
| `book_read_link` 总行数 | **26** | 全部属于 `user_id=1`；`Guest`（id 2）零行 |
| `read_status=1`（已读）且书仍存在 | **23** | = 全库的 **1.1%** |
| `read_status=0`（显式未读） | 1 | 手滑标的，无意义 |
| `read_status=2`（在读） | **0** | 从未用过"在读"状态 |
| **孤儿行**（`book_id` 指向已删除的书） | **2 / 26** | ⚠️ book id 漂移是**实测事实**，不是理论风险 |
| `downloads` | 41 行 / 40 本 | 弱信号"打算读"；表**无时间戳**，只能知道下过 |
| `kobo_reading_state` | 28 行 / 24 本 | 弱信号"设备上打开过" |
| **三者并集** | **54 本** | 这就是"可能读过"的候选池上限 |
| 已读 ∩ 有库内评分 | **3 本** | ⚠️ "读过且推荐"榜现在开不起来 |
| `kobo_bookmark.progress_percent` | 字段存在，**28 行值全为 NULL** | Kobo 从未回报进度 → **拿不到百分比** |
| `kosync_progress`（KOReader） | **0 行** | KOReader 同步未接 |
| `shelf` | **0 个书架** | 扩展状态的载体是空的 |
| `bookmark`（站内阅读位置） | 0 行 | 没在网页端读过 |
| `archived_book` | 0 行 | 无历史包袱 |

最近标记的已读书（时间戳集中在 2026-06 ~ 2026-08）：

```
Claude Code for Product Managers        2026-08-02
Claude Code Architect                   2026-08-02
Claude Code: Up and Running             2026-07-31
Hello FinOps                            2026-07-31
Kubernetes Autoscaling                  2026-07-30
Practical Data Engineering with Apache… 2026-06-30
SRE Made Simple                         2026-06-17
Agentic AI for Engineers                2026-06-16
```

说明**标记习惯是最近才开始的**，往后会自然增长 —— 但历史的两千本需要手工补录。

### 3.2 ☠️ `app.db` 里有凭据

| 表 | 行数 | 内容 |
|----|------|------|
| `user` | 2 | 用户名 + **密码 hash** |
| `user_session` | 2 | 活动会话 |
| `oauthProvider` | **3** | OIDC provider 配置（含 client 凭据） |
| `remote_auth_token` | 0 | 远程登录令牌（当前空，但表在） |

**结论：绝不能把整个 `app.db` 快照到公开应用能读的卷上。**
只导出所需的 3 张表（SQL 见 [reading-status.md](reading-status.md)）。
这是"图省事就会犯"的那类错，必须写进代码 review 清单。

### 3.3 `shelf` 表可承载扩展状态

```sql
CREATE TABLE shelf (
    id INTEGER NOT NULL, uuid VARCHAR, name VARCHAR,
    is_public INTEGER, user_id INTEGER, kobo_sync BOOLEAN,
    created DATETIME, last_modified DATETIME, PRIMARY KEY (id),
    FOREIGN KEY(user_id) REFERENCES user (id));
```

有 `name` / `user_id` / `is_public` / `kobo_sync` 列，当前 **0 个书架**。
`read_status` 只有三态，而书单需要更多（想读/弃读/精读）—— 用书架承载，
**零 schema 改动**，在 calibre-web UI 里点几下就能维护。

---

## 4. 环境事实

| 项 | 值 |
|----|-----|
| pod 内 sqlite3 版本 | **3.45.1**（2024-01-30）→ `VACUUM INTO` 可用（需 ≥3.27） |
| `metadata.db` 伴生文件 | `-wal` 375 KB、`-shm` 32 KB 长期存在 + 7 个历史 `.bak*` 备份文件 |
| calibre-web 部署策略 | 单副本 `Recreate`（SQLite 独占写锁） |
| 两个源 PVC 的访问模式 | 均 RWO；单节点集群上多 pod 共挂**没有问题**（RWO 是节点级语义） |
| 阅读状态的备份 | **已就位** —— `calibre-web-automated-config` 已在 homelab 的夜备名单里 |

---

## 5. 一句话总结每条实测对项目的影响

| 实测 | 影响 |
|------|------|
| 2,054 本 / 4.4 MB | 不需要任何分布式或增量设计 |
| 库内评分 14 本 | 口碑维度 100% 依赖外部源 |
| ISBN 35% + google id 310 | A/B 级书上限约 700–900 本 → **必须**有"证据不足"分级 |
| **pubdate 477 本污染** | 时效维度有阻塞前置 |
| 出版社 66% 但 4 变体 | 权威维度需归一化表 |
| 标签 22% 且是 BISAC 码 | 主题分类交给 LLM，BISAC 作辅助输入 |
| 简介 47.8% | LLM 标注需为无简介的书设计降级路径 + 自报置信度 |
| 252 本 Unknown 作者 | 直接降级，不做作者权威度 |
| **阅读状态 23 本** | 管道小、补录大；`◐ 在读` 需先接 KOReader 才可能有数据 |
| **2/26 孤儿行** | join 必须丢弃孤儿，并把孤儿数纳入监控 |
| **app.db 含凭据** | 最小导出，不整库快照 |
| sqlite 3.45.1 | `VACUUM INTO` 可用，快照方案成立 |
