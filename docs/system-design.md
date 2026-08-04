# 系统设计 —— 以「产出一份可信书单」为目标

> 日期: 2026-08-04
> 状态: 📐 设计提案，回应 [review-2026-08-04.md](review-2026-08-04.md)
> 关系: 本文**取代** [architecture.md](architecture.md) §3–§5（数据流 / 数据模型 / 缓存），
> 保留并沿用它的 §1（单二进制）、§2（快照隔离）、§6（部署）、§7（可观测）—— 那几节的论证成立。

---

## 0. 一个前提转向

原设计的输出单元是**一个总序**：preset → 按 TBS 排好的 2,054 行。
但书单不是总序。在总序上切 top-20，实际会得到：9 本 Kubernetes、同一本书的 3 个版次、
以及若干「有 ISBN + O'Reilly + 被提及 3 次」的填充书。

书单的形状是 **`(目标 × 主题 × 层级) → 3–7 本，每本一句可核对的理由`**。

所以系统分**三层**，而原设计只有两层（第三层被当成「排序」，于是不存在）：

| 层 | 内容 | 变更频率 | 谁决定 |
|----|------|---------|--------|
| **facts** | 每本书可测量的事实 + 每个事实的来源与置信度。与任何审美无关 | 最低（受外部配额约束，最贵） | 外部世界 |
| **judgement** | 维度分 = 事实经公式与语料归一后的 0–100。公式是价值判断，**版本化** | 中（改公式 = 升版） | 库主人 |
| **selection** | 书单 = 判断 + 逐维准入 + 去重 + 多样性约束 + 截断 + 理由拼装 | 最高（秒级重跑） | preset 配置 |

分层的直接收益：换公式只重跑 judgement（秒级）；加榜只重跑 selection（毫秒级，且
不碰 facts —— 这才真正兑现 FR-30 的「加榜不重算分数」）；facts 是唯一需要外部配额的层，
它被彻底隔离。

---

## 1. 流水线：不可变 run + 原子发布

```
 每夜 CronJob
 ┌──────────────────────────────────────────────────────────────┐
 │ snapshot ──► corpus@C1  (metadata.db VACUUM INTO + reading 最小导出)
 └──────────────────────────────────────────────────────────────┘
                    │
      ┌─────────────┴──────────────┐
      ▼                            ▼
  works 聚类                  facts 摄入（跨 run 复用，按 TTL 增量）
  (edition → work)            Google Books / OpenLibrary / HN / LLM 标注
      │                            │
      └─────────────┬──────────────┘
                    ▼
        judgement@(C1, std=1.0)   dim_scores + norm_cdf
                    ▼
        selection@(preset set)    lists（每榜已选材、已排序、带理由）
                    ▼
        publish: published_run = R_n      ← 单行指针，原子切换
                    ▼
        只读 API + 内嵌 SPA（只读 published_run）
```

四个性质，都是原设计缺的：

| 性质 | 怎么来的 | 原设计的问题 |
|------|---------|-------------|
| **原子发布** | 一切产物写 `*.tmp` 再 `rename(2)`；`published_run` 单行指针最后切 | [architecture.md:41](architecture.md) 直接 `VACUUM INTO` 到固定路径，而 Web 正在读同一个文件 → 撕裂读；且 `VACUUM INTO` 目标已存在会**直接失败**，第二夜起就不工作 |
| **可回滚** | 榜单变糟 → 改 `published_run` 一行 | 原地覆盖，无退路 |
| **可 diff** | `readlist diff R_a R_b` → 谁进了、谁出了、因为哪一维变了 | scoring-standard §8 要求升版附「20 本已知书的排名对比」，但没有产生它的机制 |
| **可复现** | `(corpus_id, standard_version, facts_hash)` 三元组决定分数 | NFR-10 漏了 corpus —— 加 30 本书就让全站分数变（见 review M3） |

---

## 2. 证据模型：逐维度、三态

这是 review B1/B3 的根本修法。**证据状态落在每个 `(work, dim)` 上**，不是每本书一个字母：

| state | 含义 | 能否参与排序 |
|-------|------|-------------|
| `measured` | 有实测证据（外部评分 / HN 命中 / 可信 pubdate / 高置信标注） | 是，有判别力 |
| `shrunk` | 无证据，贝叶斯收缩到语料先验 | 是，但判别力为 0 |
| `unknown` | 连收缩都不合理（pubdate 被 mtime 污染的 F、作者 Unknown 的 T） | **否** |

preset 声明自己需要什么，而不是卡一个全局闸门：

```yaml
- id: timeless
  name: 经典长青
  weights: { A: 0.30, C: 0.25, T: 0.25, D: 0.10, readability: 0.10 }
  bands:   { D: { target: 75, tol: 35 } }     # 经典书深度中高，但不是越深越好
  needs:   { A: measured, C: measured, T: measured }   # F 压根不出现 → 无需 exempt
  select:  { size: 20, max_per_topic: 2, max_per_author: 1, min_coverage: 0.7 }
  filters: { min_age_years: 3 }
```

**逐本权重 renormalize**（原设计想做但落错了层）：

```
可用维度 = {i : state_i ≠ unknown 且 preset 要求被满足}
coverage = Σ_{i ∈ 可用} w_i
TBS      = (Σ_{i ∈ 可用} w_i · score_i) / coverage
```

- `coverage` 直接进 UI：**「按 5 / 7 维评出」比「78.3 分」诚实得多**；
- `coverage < min_coverage` → 不进这份书单（但仍进全库目录，标注缺哪几维）；
- 于是一本 pubdate 被污染的 O'Reilly 经典**可以正常进 `timeless`** —— 因为那份榜不需要 F。
  477 本被误杀的书回来了（review B1）。

单一 A/B/C/D 字母**保留但降级为 UI 徽章**：`A = 全维 measured`、`B = 有 shrunk`、
`C = 主要靠本地元数据`、`D = 关键维 unknown`。它不再决定任何准入。

---

## 3. 归一化：CDF 只由 measured 构建，并作为版本化产物

修 review B5：

1. 每维的经验 CDF **只用 `state = measured` 的行构建**，存 `norm_cdf(run_id, dim, q, raw)`
   （101 个分位点即可，插值取值）。
2. `shrunk` 的行**不参与 CDF 构建**，而是映射到「先验值在该 CDF 上的位置」，并带标记。
   —— 否则 1,500 个并列值会把有数据的 500 本压进分位空间的一条窄带。
3. 并列一律 **mid-rank**（写死在规格里，否则两个正确实现给不同分 → NFR-10 破功）。
4. 展示用 `log1p(raw)` 保留量级直觉，排序用 pct（沿用原规格 §4.1，这条是对的）。
5. F 加夹逼：`age = max(0, now − pubdate)`；`pubdate` 落在未来 → `state = unknown`
   （未来日期本身就是污染的强信号，正好当交叉校验用）。

---

## 4. work 级实体：先去重，再评分

修 review M6。评分与榜单的主键是 **work**，不是 calibre 的 `book_id`：

```
聚类键优先级：OpenLibrary work id  >  ISBN-13 族（去校验位/同族前缀）
             >  normalize(title) + 首作者姓氏
```

- 评分人数 `v` 在 **work 级求和** → 一本书的 3,000 人评分不再被拆成 3 × 600，
  贝叶斯不再把它错误地拉回先验；
- 榜单在 work 级出 → 不会出现「Learning Python 4th / 5th」两行；
- 详情页展示「本库持有的版次与格式」，这正好是 readability 维度的载体。

**book id 漂移**（实测 2/26 孤儿行，风险 R-8）也顺手解决：work 的聚类键是 ISBN/标题，
不依赖 calibre 的自增 id。阅读状态仍按 `book_id` join，但 join 后挂到 work 上。

---

## 5. 选材：书单真正诞生的地方

TBS 排序只是输入。选材是一个带约束的贪心：

```python
def select(preset, ranked):          # ranked: 按 TBS 降序的 work 列表
    out, per_topic, per_author = [], Counter(), Counter()
    for w in ranked:
        if w.coverage < preset.min_coverage:            continue
        if per_topic[w.primary_topic]  >= preset.max_per_topic:  continue
        if per_author[w.first_author]  >= preset.max_per_author: continue
        out.append(w); per_topic[w.primary_topic] += 1; per_author[w.first_author] += 1
        if len(out) == preset.size: break
    return out
```

默认值：`size: 20`、`max_per_topic: 2`、`max_per_author: 1`（专题榜放宽到 `max_per_topic: 4`）。

**理由串确定性拼装**，零 LLM 散文 —— 不违反「不做全量书评生成」的非目标，
但让书单读起来像书单：

```
O'Reilly · 核心开发者作者 · HN 提及 34 次（2016–2024）· 主题半衰期 25 年 · 深度 72/100
按 5/7 维评出（缺：时效—出版日期不可信、可操作—标注置信度低）
```

**分主题小榜**（真正的「书单」形态）由同一套机制生成，不需要新代码：
`for topic in top_topics: preset_variant(base, filters={topic: t}, size=5)`。
主导航是「目标榜」，二级是「主题 × 层级」小榜 —— 后者才是访客真正会收藏的东西。

---

## 6. 半衰期：规则优先，LLM 兜底

修 review M8 —— F 是差异点，不能压在一个未验证的离散标签上。

```yaml
# half_life_rules.yaml —— 命中即定档，不问 LLM
bisac_prefix:
  COM051: { class: 语言核心,   half_life_years: 10 }    # Programming Languages
  COM042: { class: 时事/趋势,  half_life_years: 1.5 }   # NLP
  COM060: { class: 框架/版本,  half_life_years: 2.5 }   # Web Programming
title_keywords:
  - { any: [compiler, algorithm, "operating system", "distributed system"], half_life_years: 25 }
  - { any: [kubernetes, terraform, aws, "data engineering"],                half_life_years: 5 }
  - { any: [react, vue, "spring boot", django, rails],                      half_life_years: 2.5 }
  - { any: [llm, agent, prompt, rag, "generative ai"],                      half_life_years: 1.5 }
fallback: llm            # 未命中才问模型，且结果进人工复核队列
```

BISAC 码是结构化的且已确认存在（[data-baseline.md:70-80](data-baseline.md)），
虽只覆盖 22%，但它覆盖的那部分**免费且准确**。关键词表覆盖 Top ~40 主题 —— 技术书的主题
分布是重尾的，40 个词能吃掉大半。剩下的才交 LLM。

**LLM 层必须过门禁才能上公开榜**：人工标注 60–100 本 gold（按「有无简介 × 主题类」分层抽样），
量 `topic_class` 准确率 与 `depth` 的 Spearman ρ。达标线写进规格；不达标 → D/P/F 只进内部榜
（就是 A-3 假设不成立时「退回 4 维」那条路，但现在有触发条件而不是靠感觉）。

---

## 7. 门禁：gold set 先注册，后打分

原设计的 AC-P2① 是「人工逐本核对无明显误入」+ 一句「不许靠调权重糊过去」，
但没有让这句话可执行的机制。把它变成一个数：

```yaml
# eval/gold.yaml —— 必须在第一次打分之前 commit
must_rank_high:      [ ... 30 本你愿意公开为其排名辩护的书 ... ]
must_not_rank_high:  [ ... 30 本明显是填充/过时/水书 ... ]
```

CI 计算并记录在每个 run 上：

| 指标 | 含义 |
|------|------|
| `precision@20` | top-20 里 gold-positive 的比例 |
| `gold_low_in_top100` | 不该高的书混进前 100 的数量（**这个数是硬门槛**） |
| `coverage_p50 / p10` | 榜内书的维度覆盖分布 |
| `measured_ratio[dim]` | 每维有多少本真有数据 —— **判别力的直接度量** |
| `topic_concentration` | top-20 的主题基尼系数（选材有没有生效） |

两个副作用正好是想要的：`gold.yaml` 的 commit 时间早于权重的 commit 时间 →
**调权重去迁就自己喜欢的书会留在 git 历史里**；升版时自动产出 §8 版本治理要的排名对比。

---

## 8. Schema（唯一真相源，落成 `schema.sql`）

```sql
-- ── 实体层 ────────────────────────────────────────────────────────────
CREATE TABLE works (
  work_id TEXT PRIMARY KEY,              -- 聚类产生的稳定键
  canonical_title TEXT NOT NULL, first_author TEXT, ol_work_id TEXT,
  primary_topic TEXT, level TEXT, half_life_years REAL, half_life_source TEXT);

CREATE TABLE editions (
  book_id INTEGER PRIMARY KEY,           -- calibre metadata.db 的 id（可能漂移）
  work_id TEXT NOT NULL REFERENCES works, title TEXT, isbn13 TEXT,
  google_volume_id TEXT, publisher_raw TEXT, publisher_norm TEXT,
  format TEXT, language TEXT, has_comments INT, has_cover INT,
  pubdate TEXT, pubdate_source TEXT,     -- file-meta|google|openlibrary|mtime-fallback|unknown
  personal_rating_stars REAL);           -- ← review M9：metadata 值 ÷ 2，单位写死为星

-- ── facts 层（跨 run 复用，最贵的数据）────────────────────────────────
CREATE TABLE evidence (                  -- 外部响应原样存
  source TEXT, source_id TEXT, work_id TEXT, payload TEXT,
  fetched_at TEXT, ttl_days INT, PRIMARY KEY (source, source_id));
CREATE TABLE labels (                    -- LLM/人工标注 + 输入指纹去重
  work_id TEXT PRIMARY KEY, topic_class TEXT, topics TEXT, level TEXT,
  depth REAL, practicality REAL, confidence REAL,
  input_fingerprint TEXT, labeled_by TEXT, labeled_at TEXT);
CREATE TABLE mentions (                  -- HN 命中，保留 objectID 供人工否决
  work_id TEXT, object_id TEXT, created_at TEXT, matched_by TEXT,
  PRIMARY KEY (work_id, object_id));

-- ── 人工投入（不可再生 → 必须夜备，NFR-16）─────────────────────────
CREATE TABLE overrides   (work_id TEXT, field TEXT, value TEXT, reason TEXT, at TEXT);
CREATE TABLE publisher_map (raw TEXT PRIMARY KEY, norm TEXT, tier INT);
CREATE TABLE title_whitelist (work_id TEXT PRIMARY KEY, reason TEXT);  -- ≤2 词标题的 HN 白名单

-- ── judgement 层（run-scoped）─────────────────────────────────────────
CREATE TABLE runs (
  run_id TEXT PRIMARY KEY, kind TEXT, corpus_id TEXT, standard_version TEXT,
  facts_hash TEXT, started_at TEXT, ended_at TEXT, status TEXT,
  ok_count INT, fail_count INT, orphan_rows INT, quota_used TEXT, metrics TEXT);
CREATE TABLE dim_scores (
  run_id TEXT, work_id TEXT, dim TEXT,
  raw REAL, pct REAL, score REAL,
  state TEXT NOT NULL,                   -- measured | shrunk | unknown
  source TEXT, confidence REAL, PRIMARY KEY (run_id, work_id, dim));
CREATE TABLE norm_cdf (                  -- 版本化的经验 CDF，保证可复现
  run_id TEXT, dim TEXT, q INT, raw REAL, PRIMARY KEY (run_id, dim, q));

-- ── selection 层（书单产物）───────────────────────────────────────────
CREATE TABLE lists (
  run_id TEXT, list_id TEXT, rank INT, work_id TEXT,
  tbs REAL, coverage REAL, reason TEXT, PRIMARY KEY (run_id, list_id, rank));

-- ── 只读镜像 ──────────────────────────────────────────────────────────
CREATE TABLE reading (book_id INTEGER PRIMARY KEY, status TEXT, shelves TEXT,
                      downloads INT, last_modified TEXT);   -- app.db 最小导出，永不写回

CREATE TABLE published_run (id INT PRIMARY KEY CHECK (id = 1), run_id TEXT NOT NULL);
```

注意 `scores` 表**没了**（review M4）：综合分是 `f(dim_scores, preset)`，
持久化的是选材产物 `lists`，因为选材含去重与多样性约束，不是纯点积。

---

## 9. 代码形状（沿用单 Go 二进制）

```
cmd/readlist/main.go
  readlist snapshot     # 独立 CronJob 用；挂 calibre 两个卷，无网络出口
  readlist ingest       # works 聚类 + facts 摄入（受配额约束，可中断续跑）
  readlist score        # judgement + selection + publish（纯函数，可离线重跑）
  readlist dryrun       # 只数不算：每维 measured 比例、每 preset 候选池大小 ← 第一步就跑这个
  readlist diff A B     # 两个 run 的榜单差异（升版评审材料）
  readlist serve        # 只读 API + 内嵌 SPA

internal/
  corpus/     快照读取、works 聚类、publisher 归一
  facts/      googlebooks/ openlibrary/ hn/ llm/  —— 每个源一个包，共用配额与缓存中间件
  score/      dims/{acclaim,community,freshness,trust,depth,practicality,readability}.go
              norm.go（CDF + mid-rank）  combine.go（逐本 renormalize + band）
  selection/  eligibility.go  diversity.go  reason.go
  web/        只读 handler + embed 的 SPA
  eval/       gold set 评测，score 命令结束时自动跑并写进 runs.metrics
```

一个约束值得写死在代码里：**`score` 命令不许发起任何网络请求**。
这既是 FR-11「重算不打外部 API」的强制实现，也让 `score` 可以在笔记本上对着一份 db
反复迭代公式 —— 这是调好一个评分标准的唯一舒服方式。

---

## 10. 对外接口

| 端点 | 内容 |
|------|------|
| `GET /api/lists` | 公开书单清单（不含 `visibility: internal`） |
| `GET /api/lists/{id}` | 已选材的书单：排名、TBS、coverage、理由串、阅读状态徽章 |
| `GET /api/works/{id}` | 得分拆解：每维 raw / pct / score / state / source / confidence + 版次列表 |
| `GET /api/matrix/{run_id}.json` | **滑块用的整块矩阵**：works × dims + facets |
| `GET /metrics` | Prometheus |

`matrix` 是 review M5 的修法，两条硬规则：

1. **只含可公开行** —— 否则 devtools 里就能看到「不公开」的书，FR-25 的承诺被前端绕过；
2. 按 `run_id` 寻址 → `Cache-Control: immutable, max-age=31536000`，
   零后端成本、CDN 友好，发布新 run 自然换 URL。

体量：~1,800 work × 7 维 + facets ≈ 250 KB JSON / **~70 KB gzip**。一次加载，
之后滑块纯客户端点积（NFR-2 达成，且比原设计的「隐含全量下发」有了明确契约）。

---

## 11. 原设计里保留不动的部分

这些论证成立，不需要重做：

- **单 Go 二进制**复用 `trends` 的部署形状（arm64 / SQLite / 单副本 Recreate / local-path）；
- **快照 CronJob 隔离 blast radius** —— 全套文档里最好的一条设计。
  能挂 calibre 卷的只有那个几十行、无网络出口的 CronJob；Web 永不挂 calibre 卷；
- **`app.db` 最小导出 3 张表**，`user_id = 1`，join 丢孤儿（NFR-13）；
- 阅读状态**只读镜像、永不写回**、是 facet 不进分数；
- 封面**外链**、只发元数据与评分不发书（NFR-12 / R-7）；
- HN 匹配「宁少不多」+ 保留 `objectID` 可人工否决；
- 域名 `readlist.meirong.dev` 不用 `books.`；边缘限流分档。

只加两点实现细节：**产物一律 `*.tmp` → `rename(2)`**；Web 以 `mode=ro&immutable=1` 打开
已发布的 run（此时库确实静止，`immutable` 是安全的 —— 与 calibre 活库不同）。

> 顺带修一个论据：[reading-status.md:127-128](reading-status.md) 用「readlist 的卷是纯派生、
> 可丢弃」来论证不写回阅读状态。这条论据不成立 —— NFR-16 已经把 `evidence` /
> `overrides` / `publisher_map` 这些不可再生数据放在同一个卷上了。**结论对，理由该换成
> 「真相源唯一」**：阅读发生在 calibre-web / Kobo，状态就该在那里产生。

---

## 12. 重排后的落地顺序

原则：**把「手上有一份可信书单」提前到第一周**，把最贵、最不确定的东西（外部配额、LLM）推后。

| 步 | 做什么 | 产出 | 外部依赖 |
|----|--------|------|---------|
| **0** | `git commit`；落 `schema.sql`；写 `eval/gold.yaml`（打分之前！） | 可回滚的地基 + 门禁 | 无 |
| **1** | `readlist dryrun` —— 只数不算：每维 measured 比例、每 preset 候选池大小、主题分布 | **权重与准入的真实数字**（review B2/M1 的输入） | 只需 metadata 快照 |
| **2** | snapshot CronJob + works 聚类 + publisher 归一 + `T` / `readability` 两维 + selection + gold 评测 | **第一份内部书单，零外部依赖、零 LLM** | 无 |
| **3** | Google Books / OpenLibrary fetch（顺手写 `pubdate` + `pubdate_source`）→ `A` + `F` | 「经典长青」「今年新书」可信 | 2–4 天配额 |
| **4** | HN → `C`（独立验收：抽 30 本核对命中） | 声量维 | 免费 |
| **5** | half-life 规则表 + LLM 兜底 + gold 门禁达标 → `D` / `P` | 「最快上手」「深挖原理」 | 本地 GPU |
| **6** | Web + `matrix` 接口 + 限流 + PVC 备份归属 + 指标 | 公开上线 | 无 |

两处与原 roadmap 的实质差异：

1. **Phase 1 不再是跨仓库硬阻塞**（review M2）。readlist 的 fetch worker 为了 `A` 维本来就要
   打 Google Books，同一个响应里就带 `publishedDate` —— 它需要的是「自己表里有个带来源的
   pubdate」，不是「修好 calibre 的 metadata.db」。回写 calibre 降级为可选的书库卫生任务。
2. **第 2 步就能交付一份书单**（只用出版社 tier + 格式 + 选材去重多样性）。它不完美，
   但它让 gold set、选材约束、理由串、diff 工具全部在**没有任何外部不确定性**的条件下调通。
   等外部数据进来时，要调的只剩权重。

Phase 0（阅读状态补录、建 3 个书架、给 23 本已读补星级）仍然纯手工、与上面全程并行 ——
它决定 `to-read-next` 和 `read-and-loved` 有没有内容，且只有库主人能做。

---

## 13. 仍需库主人决定的两件事

| # | 问题 | 为什么必须现在定 |
|---|------|----------------|
| 1 | **约 1,000–1,300 本既无 ISBN 也无 google id** —— 要不要给它们打 title+author 搜索？ | 打 → 多 2 天配额 + 一套匹配置信度规则（同 HN 那套）；不打 → 全站可信书量锁在 ~800 本。**这一个决定直接决定整站的体量**，且它改变第 3 步的时长 |
| 2 | `timeless`（经典长青）是**最难自动出对**的榜 —— 接受一层人工 curation 吗？ | 若接受：`overrides` 表加一个 `pin` / `veto`，前 20 人工兜底，其余算法出 —— 首版就能做到「拿得出手」。若不接受：这个榜要等到 `A`+`C`+`D` 三维都达标（第 5 步之后），首版旗舰位应该换成 `to-read-next` 或主题小榜 |
