# 架构与数据模型

> 日期: 2026-08-04
> 状态: 📐 设计。**§3–§5（数据流 / 数据模型 / 缓存）已被
> [system-design.md](system-design.md) 取代** —— 那边是三层管道（facts → judgement →
> selection）与逐维证据模型，也是实现照着做的那份；本文的 §3–§5 只作历史记录。
> 特别注意 §4 里的 `scores` 表**不存在**：综合分是 `f(dim_scores, preset)` 的纯函数，
> 落库的是选材产物 `lists`。落地 schema 见
> [`internal/store/migrations/0001_schema.sql`](../internal/store/migrations/0001_schema.sql)。
> §1（单二进制）、§2（快照隔离）、§6（部署）、§7（可观测）仍然有效。
> 环境事实依据: [data-baseline.md §4](data-baseline.md#4-环境事实)

---

## 1. 形状：单 Go 二进制

**单 Go 二进制** = cron 采集 worker + 评分 worker + 只读 API + 内嵌 SPA + SQLite/WAL。
镜像推 `ghcr.io/meirongdev/readlist`，`local-path` PVC，单副本 `Recreate`（SQLite 单写锁），
密钥走 External Secrets ← Vault。

同集群已有一个同形状的自研应用（`trends`：GitHub 趋势追踪，也是 cron 采集 + 评分 + 内嵌 SPA
+ SQLite），**这条路径的每个坑都已经踩过了** —— 复用它而不是发明新形状。

> 语言选 Go 不是技术偏好，是**复用已验证的部署形状**：静态二进制、arm64 交叉编译一条命令、
> 镜像小。用 Python 也能做，但要自己重走一遍 arm64 + 镜像 + 资源限额的验证。

## 2. ⚠️ 怎么读 Calibre 的两个库

要读**两个 PVC 上的两个 SQLite 库**，性质还不一样：

| 库 | PVC | journal_mode | 取法 |
|----|-----|--------------|------|
| `/calibre-library/metadata.db` | `calibre-books-local` | **wal** | `VACUUM INTO` 整库快照 |
| `/config/app.db`（阅读状态） | `calibre-web-automated-config-local` | delete | **只导出 3 张表**（库里有密码 hash） |

`metadata.db` 是 WAL 且 `-wal`/`-shm` 长期存在，这意味着：

- **只读挂载 + 直接查会失败**（WAL 读也要写 `-shm` → `attempt to write a readonly database`）；
- `?immutable=1` 只在库确实静止时才安全 —— calibre-web 一直在写，不安全。

### 方案：快照 + 隔离 blast radius

一个**独立的短命 CronJob**（不是长跑的 web 应用）挂两个源 PVC + `readlist-data`，只做两件事：

```bash
# 1) 书库：整库快照（4.4MB，秒级）
sqlite3 /library-src/metadata.db "VACUUM INTO '/data/snapshot/metadata.db'"

# 2) 阅读状态：最小导出，绝不整库拷 app.db —— SQL 见 reading-status.md §4
sqlite3 < /scripts/export-reading.sql
```

（pod 内实测 sqlite 3.45.1，`VACUUM INTO` 需 ≥3.27，可用。）

然后 **`readlist` 只挂自己的 `readlist-data` PVC，永不接触 calibre 的任何卷**。

| 性质 | 说明 |
|------|------|
| RWO 多挂载 | 单节点集群上没问题 —— RWO 是**节点级**语义，calibre-web 自己的清单已有同样论证 |
| 为什么源卷是 RW 挂载 | WAL 读的客观要求；但**能碰 calibre 卷的容器只有这个几十行、无网络访问的 CronJob** |
| 攻击面 | 公开 web 应用的任何 bug 都碰不到书库，也碰不到 `app.db` 里的凭据 |
| 附带收益 | `readlist` 与 calibre 的**可用性解耦** —— calibre 挂了榜单照常服务 |

## 3. 数据流

```
calibre-books-local          ─┐
  metadata.db (wal)          ├─ 每夜 CronJob ─► snapshot/metadata.db  (VACUUM INTO)
calibre-web-...-config-local ─┘               └► snapshot/reading.db   (只导 3 表)
  app.db  (阅读状态/书架)                                  │
                                                          ▼
Google Books / OpenLibrary / HN Algolia ──► fetch worker ──► evidence (缓存 + TTL)
内部 LLM 网关 (本地 GPU) ─────────────────► label worker ──► labels
                                                          │
                                             score worker ▼
                                    dim_scores → scores（按 standard_version）
                                    + reading（徽章/筛选/阅读队列，不进分数）
                                                          │
                                             只读 API + 内嵌 SPA
                                                          ▼
                              Cilium Gateway ◄── Cloudflare Tunnel ◄── readlist.meirong.dev
```

## 4. 数据模型（SQLite）

```
books            书的规范化副本（来自 metadata.db 快照）+ pubdate_source
reading          阅读状态（book_id, status, shelf, last_modified, downloads）——
                 来自 app.db 最小导出，只读镜像，readlist 从不写
evidence         外部原始响应（source, source_id, book_id, payload JSON, fetched_at, ttl）
labels           LLM/人工标注（book_id, topic_class, level, depth, practicality,
                 confidence, input_fingerprint, labeled_by, labeled_at）
dim_scores       维度分（book_id, standard_version, dim, raw, pct, score）
scores           综合分（book_id, standard_version, preset_id, tbs, evidence_grade, rank）
overrides        人工否决/修正（book_id, field, value, reason, author, at）
publisher_map    出版社名归一化 + tier（Packt 4 变体 → 1）
mention_overrides HN 提及的人工否决（book_id, object_id, verdict, reason）
runs             每次采集/评分的运行记录（起止、成功/失败数、孤儿行数、配额消耗）
```

### 哪些数据不可再生

派生分随时可重算，但这三类是**人工投入的沉淀或昂贵缓存**：

| 数据 | 为什么不可再生 |
|------|--------------|
| `overrides` / `mention_overrides` | 人工判断，无处可查 |
| `publisher_map` | 一次性人工归一 + 规则积累 |
| `evidence` | 重建要烧 2–3 天的免费配额 |

→ 必须纳入夜备（[requirements.md](requirements.md) NFR-16）。
阅读状态**不在此列** —— 它的真相源是 calibre-web，那边已有备份。

## 5. 外部数据源与配额

| 源 | 拿什么 | 配额/限制 | 本库适用面 |
|----|--------|----------|-----------|
| **Google Books API** | averageRating, ratingsCount, categories, pageCount, description | 无 key 约 1,000 次/天 | 310 volume id 直取 + 715 ISBN 查询 → **首轮分 2–3 天跑完**，之后只增量 |
| **OpenLibrary** | ratings、works、pubdate | 免费、不限速（礼貌 0.5s 间隔） | 补 Google 查不到的；技术书覆盖一般 |
| **HN Algolia** | 提及次数 + 讨论链接 | 免费无 key，建议 ≤10 rps | 全库（受匹配规则约束） |
| **GitHub Search** | awesome-list 收录次数 | 需 PAT，30 req/min | 可选，次要权重 |
| **内部 LLM 网关** | 深度/可操作/主题/层级标注 | 本地 GPU，无外部成本 | 全库 |
| ~~Goodreads~~ | — | **API 2020 已关停** | 不用 |
| ~~ISBNdb / WorldCat~~ | — | 付费 | 不用 |

**缓存策略**：外部响应**原样存 JSON** + TTL（评分类 30 天，元数据类 180 天）。
所有派生分从缓存重算，**重算不打外部 API**。稳态下每天只有几十次请求。

## 6. 部署与暴露

| 项 | 决定 |
|----|------|
| 集群 | `oracle-k3s`（ARM free tier，单节点），命名空间 `personal-services` |
| 清单位置 | **homelab 仓库** `cloud/oracle/manifests/personal-services/readlist.yaml`，并在该目录树的 `kustomization.yaml` 的 `resources:` **登记**（oracle 侧是显式 kustomize 树，漏登记 = 静默不生效） |
| 镜像 | `ghcr.io/meirongdev/readlist`，**必须含 `linux/arm64`**；集群 Kyverno 禁 `latest` tag |
| 存储 | `readlist-data` PVC，`local-path`，带 `Prune=false` 护栏 |
| 备份 | 加进 homelab 的 oracle 夜备脚本 sqlite 列表（**CI 规则 H4 会拦下没有备份归属的 PVC**） |
| 域名 | **`readlist.meirong.dev`** —— **不用 `books.`**，与私有书库 `book.meirong.dev` 只差一个字母，误配代价高 |
| DNS | **不需要改 DNS/隧道配置**：通配隧道 + external-dns 自动建记录，只写一个 HTTPRoute 即可 |
| ReferenceGrant | oracle 侧**不需要** —— 该集群的 gateway 清单已有覆盖本命名空间的通配授权 |
| SSO | **无**（公开站）。代价是必须在边缘加限流 |
| 限流 | Cloudflare WAF：页面一档，`/api/` 更严一档。应用侧只读、无写接口、无用户输入落库 → 攻击面基本只有爬虫和带宽 |
| 资源 | 命名空间有 LimitRange，须显式声明 request/limit；量级参照同集群 `trends` |
| 更新策略 | 单副本 `Recreate`（SQLite 单写锁；滚动更新会让新旧 pod 抢同一个 db 文件的写锁） |

## 7. 可观测

| 指标 | 用途 |
|------|------|
| 最后一次成功快照 / 采集 / 评分的时间戳 | 榜单静默过期的唯一警报来源 |
| 外部 API 请求数、429 数、当日剩余配额估算 | 配额管理（NFR-9） |
| 各证据等级的书数（A/B/C/D） | 数据质量趋势；补元数据的成效 |
| `runs.orphan_rows` | 孤儿行突增 = book id 漂移（有人删书或重导入） |
| LLM 标注低置信度数量 | 待人工复核队列长度 |
