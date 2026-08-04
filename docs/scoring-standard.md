# 评分标准 TBS v1.0（Tech Book Score）

> 日期: 2026-08-04
> 状态: 📐 规格草案 v1.0（未实现）
> 数据可得性依据: [data-baseline.md](data-baseline.md)

这是**规格文档**，不是设计讨论。实现应当逐条对照本文；本文改动 = 标准升版。

---

## 1. 四条设计原则

1. **维度独立测量，榜单靠权重组合。**
   一本书的维度分是"事实"，权重是"价值判断"。加一个新榜 = 加一个权重向量，**不重算数据**。
2. **缺数据 ≠ 0 分。**
   缺数据用贝叶斯收缩到语料先验，并单独记录**证据等级**；证据不足的书不上公开榜
   （借 Metacritic 的 "not enough reviews"），而不是给它一个编出来的分。
3. **可解释。**
   每个榜单条目可展开：各维度分 + 每一分的数据来源 + 算它时用的标准版本。
   自建榜单唯一的信任来源就是"能看见怎么算出来的"。
4. **标准版本化。**
   `standard_version` 落在分数表的每一行上。改公式 / 改权重 = 发新版本，旧分保留可对比。
   **不允许原地改公式覆盖历史分。**

---

## 2. 七个维度

分数一律 0–100。**注意 D 不是"越高越好"**，它是 facet（见 §4）。

| 代号 | 维度 | 测什么 | 数据来源 | 本库可得性 |
|------|------|--------|----------|-----------|
| **A** | Acclaim 口碑 | 外部读者评分的**贝叶斯加权**分 | Google Books（310 volume id + 715 ISBN）、OpenLibrary ratings | ~35%，最大短板 |
| **C** | Community 技术圈声量 | 被技术社区提及的**时间衰减**次数 | HN Algolia API（免费无 key）、GitHub awesome-list | 全库可查，但**误匹配风险高** |
| **F** | Freshness 时效 | 距今年数 ÷ **主题半衰期** | `pubdate`（清洗后）+ LLM 标注的 `topic_class` | 受 pubdate 污染限制 |
| **T** | Trust 权威 | 出版社层级 × 作者身份 | 出版社（归一化后）+ LLM/人工作者标注 | 66%（最可靠的一维） |
| **D** | Depth 深度 | 概念密度 / 前置要求 / 页数 | LLM 标注（简介 + 目录）+ pageCount | 依赖 LLM，47.8% 有简介 |
| **P** | Practicality 可操作 | 动手比例（代码/项目 vs 纯理论） | LLM 标注 | 同上 |
| **L** | Library 馆藏可读性 | 格式（EPUB>PDF）、封面、简介、元数据完整度 | 本地 `metadata.db` | **100%** |

> **为什么是这 7 个**：现成的技术书榜基本只有 1–2 维（评分，或提及数）。技术书区别于一般书的
> 两件事 ——「**会过时**」和「**分层级**」—— 恰好是通用榜最薄的地方，所以 **F 和 D 是本标准的
> 差异点**，也是最需要 LLM 标注兜底的两维。

**阅读状态不是维度**：读过 ≠ 好书。它是 facet，见 [reading-status.md](reading-status.md)。

---

## 3. 各维度公式

### A — 口碑（IMDb 式贝叶斯加权）

解决"1 个人打 5 星就登顶"的问题：

```
A_raw = (v/(v+m))·R + (m/(v+m))·C

  R = 该书外部平均分（归一到 0–5）
  v = 评分人数
  C = 全库加权平均分（先验均值，从数据实算；技术书通常落在 4.0–4.2）
  m = 置信阈值 = max(全库 ratingsCount 中位数, 20)
```

多源合并（Google Books + OpenLibrary）：**各源分别算 `A_raw`，再按各自 `v` 加权合并**。
不做简单平均 —— 否则 3 人评分的源和 3,000 人评分的源等权。

### C — 技术圈声量（时间衰减提及数）

```
C_raw = Σ over mentions  1 / (1 + age_years/τ)      τ = 4 年
```

⚠️ **误匹配是这一维的头号风险。** `Clean Code`、`Refactoring`、`Fluent Python` 这类
2 词以内的通用短标题，在 HN 上会命中大量与书无关的讨论。匹配规则：

1. 必须 `"<title>"` **精确短语**命中，且满足其一：
   作者姓氏出现在同一条评论 ／ 标题 ≥3 词 ／ 出版社名同现；
2. 标题 ≤2 词的走**人工白名单**（约几十本，可接受）；
3. 保留命中的 HN `objectID`，前端可点开看原始讨论，人工可否决 → `mention_overrides` 表；
4. **宁少不多** —— 漏算一本书只是它排低了，误算一本会让整个榜显得随机。

### F — 时效（按主题半衰期指数衰减）

```
F = 100 · 0.5 ^ (age_years / half_life)
```

`half_life` 由 LLM 标注的 `topic_class` 决定：

| topic_class | 半衰期 | 例 |
|-------------|--------|-----|
| 常青/理论 | 25 年 | 算法、系统原理、设计、数学、编译原理 |
| 语言核心 | 10 年 | C / Go / Python 语言本体 |
| 平台/生态 | 5 年 | Kubernetes、云、数据工程 |
| 框架/版本 | 2.5 年 | React 18、Spring Boot 3、某工具手册 |
| 时事/趋势 | 1.5 年 | LLM 应用、prompt 工程、Agent |

**这一维是本标准与通用书榜的核心差异**：Goodreads 不会告诉你一本 2021 年的框架书已经废了，
而同年的系统设计书还能读十年。

⚠️ **前置**：`pubdate_source` 必须可信（[data-baseline §2](data-baseline.md#2--发现一pubdate-已被-mtime-污染)）。
不可信 → **F = 未知**，不参与任何按 F 排序的榜，且证据等级压到 C。

### T — 权威

```
T = 0.6·publisher_tier + 0.4·author_signal
```

**出版社层级**（必须先做名称归一化：Packt 4 变体→1、O'Reilly 2→1、BPB 2→1）：

| tier | 分 | 出版社 |
|------|----|--------|
| 1 | 100 | O'Reilly · Manning · Pragmatic Bookshelf · No Starch · MIT Press · Addison-Wesley |
| 2 | 75 | Apress · CRC/Taylor&Francis · Wiley · Springer · Simon & Schuster |
| 3 | 50 | Packt · BPB · 其他有名号的技术社 |
| 4 | 25 | 自出版 / 未知出版社 |

> 这个分层是**主观的**，但它主观得**明示且可改**（一张表），比藏在综合分里的隐式偏好好。
> Packt 放 tier 3 不是说 Packt 的书都差，是说"Packt 出品"这个信号的**先验信息量低**。

**`author_signal`**（LLM / 人工标注）：

| 情形 | 分 |
|------|-----|
| 该项目/语言的核心开发者 | 100 |
| 领域公认作者 | 75 |
| 有其他高分著作 | 50 |
| 无信号 | 25 |
| 作者为 `Unknown`（那 252 本） | 0 |

### D / P — 深度与可操作（LLM 标注）

对每本书用 `title + authors + publisher + comments + tags + pageCount` 让模型输出结构化标签：

```json
{
  "topic_class": "平台/生态",
  "topics": ["kubernetes", "observability"],
  "level": "intermediate",
  "depth_0_100": 65,
  "practicality_0_100": 80,
  "prerequisites": ["linux", "docker"],
  "confidence": 0.8
}
```

`level` 取值：`beginner` | `intermediate` | `advanced` | `reference`。

规则：

- 走**内部 LLM 网关**（模型跑本地 GPU）→ 2,054 本一轮标注**零外部推理成本**，
  可随标准版本重跑；
- **强制 JSON schema 输出**；
- 无简介的书（52%）模型须自报低置信度；`confidence < 0.5` 的标注**不用于榜单**，
  只进待人工复核队列；
- 标注结果与**输入指纹**（输入字段的 hash）一起存 → 元数据没变就不重复调用。

### L — 馆藏可读性（纯本地，100% 可算）

```
L = 30·format_score + 20·has_cover + 20·has_comments + 15·has_isbn + 15·metadata_complete

format_score: EPUB=1.0 · AZW3/MOBI=0.8 · PDF=0.5   （PDF 在手机/阅读器上重排差）
```

L 的第二个用途：**它就是书库卫生度仪表盘** —— 低 L 的书正是该补元数据的书。

---

## 4. 归一化与组合

### 4.1 归一化用百分位，不用 min-max

原始值 → **语料内百分位排名** → 0–100。

理由：HN 提及数、`ratingsCount` 都是**幂律分布**。min-max 会被单个 outlier
（比如某本被提 800 次的书）把其余 2,053 本全压到 0–3 分区间。百分位天然抗 outlier，
且让不同维度可比 —— "这本书的口碑在本库排前 10%" 比 "口碑 73.4 分" 更有意义。

count 类维度**展示**时用 `log1p` 后的值（保留量级直觉），**排序**用百分位。

### 4.2 单调维度用权重，facet 维度用目标带

单调维度（A/C/F/T/P/L）越高越好 → 权重 `w_i`。

facet 维度（D 深度，以及 `level`）**有目标值**：在"最快上手"榜里，深度 95 分的书是**减分项**。
所以用**目标带**打分：

```
band_score(x; target, tol) = 100 · max(0, 1 - |x - target| / tol)
```

### 4.3 综合分

```
TBS = Σ_i w_i · score_i  +  Σ_j w_j · band_score(score_j; target_j, tol_j)
```

- 权重和为 1；
- 被 preset 声明 `exempt` 的维度**从分母里剔除并重新归一**（"经典长青"榜豁免时效，
  不是给时效权重 0 —— 那会让老书被隐式惩罚）。

---

## 5. 证据等级（决定能否上公开榜）

| 等级 | 条件 | 待遇 |
|------|------|------|
| **A** | 有外部评分且 `v ≥ m`；`pubdate_source` 可信；LLM 标注 `confidence ≥ 0.7` | 全部榜单 |
| **B** | 有外部评分但 `v < m`，**或** HN 提及 ≥1；`pubdate_source` 可信 | 全部榜单（前端标"证据有限"） |
| **C** | 只有本地元数据 + LLM 标注，无任何外部信号 | **不上榜**，只进"全库目录"页并标注"数据不足" |
| **D** | `pubdate` 来自 mtime 兜底 ／ 作者 `Unknown` ／ 元数据残缺 | **不公开** |

按实测推算，能进 A/B 的约 **700–900 本**（715 ISBN + 310 google id 去重后，
加上一批靠 HN 提及进 B 的）。

**这是特性不是缺陷**：一个 800 本的可信榜，比 2,054 本掺着 1,300 本猜测分的榜有用得多。

---

## 6. 榜单预设（权重档案）

配置即数据，一个 preset 一段 YAML，放 `presets/*.yaml`。加榜不改代码、不重算分数。

```yaml
- id: timeless
  name: 经典长青
  weights: { A: 0.35, C: 0.30, T: 0.20, L: 0.15 }
  exempt: [F]                      # 明确豁免时效 —— 老不是缺点
  filters: { min_evidence: A, min_age_years: 3 }

- id: ship-this-week
  name: 最快上手
  weights: { F: 0.30, P: 0.35, A: 0.20, L: 0.15 }
  bands:   { D: { target: 35, tol: 25 } }        # 太深的书不适合速成
  filters: { min_evidence: B, level: [beginner, intermediate] }

- id: deep-dive
  name: 深挖原理
  weights: { D: 0.35, T: 0.25, A: 0.25, C: 0.15 }
  filters: { min_evidence: B, level: [advanced, reference] }

- id: new-2026
  name: 今年新书
  weights: { A: 0.30, C: 0.25, P: 0.25, T: 0.20 }
  filters:
    pubdate_year: 2026
    pubdate_source: [google, openlibrary, file-meta]   # ⚠️ 排除 mtime 兜底
  # 新书评分人数少 → A 的贝叶斯先验自动把它们拉回均值，不会被"3 个 5 星"刷榜

- id: ai-llm
  name: AI / LLM 专题
  weights: { F: 0.35, P: 0.25, A: 0.25, C: 0.15 }      # 该领域半衰期最短，时效权重最高
  filters: { topics_any: [ai, llm, ml, nlp], min_evidence: B }

- id: to-read-next
  name: 下一本读什么
  weights: { A: 0.30, C: 0.25, T: 0.20, P: 0.15, L: 0.10 }
  filters: { min_evidence: B, read_status: [unread], not_in_shelf: [弃读] }
  # 多维评分对库主人最大的价值：把客观排行变成阅读队列

- id: read-and-loved
  name: 我读过且推荐
  weights: { A: 0.40, T: 0.30, D: 0.30 }
  filters: { read_status: [read], min_personal_rating: 4 }
  # ⚠️ 实测已读 ∩ 有库内评分只有 3 本 —— 需先补个人星级，否则这个榜开不起来

- id: library-hygiene
  name: 待补元数据
  weights: { L: 1.0 }
  order: asc
  visibility: internal            # 不公开，书库卫生榜
```

**前端权重滑块**：分数已按维度算好存表，重排是 2,054 行的向量点积，浏览器内毫秒级完成。
现有技术书榜全是固定权重的死榜，**"自己调口径"是这个站最容易做出的差异点**。

---

## 7. 参考的现有榜单：借了什么、没借什么

| 榜单 | 借 | 不借 |
|------|-----|------|
| **IMDb Top 250** | 贝叶斯加权评分（§3 的 A 就是它） | 单维度排序 |
| **Metacritic** | "评分不足"显式分级、来源透明可点开 | 编辑部加权（没有专家池） |
| **Goodreads** | 评分 + 人数双指标的思路 | ⚠️ **API 2020 年已关停**，不设计任何依赖它的抓取；只存人工填的链接 |
| **HN Books / hackernewsbooks.com** | HN 提及数作技术圈口碑代理（§3 的 C） | 它只有提及数一维，无时效/深度 |
| **Reddit "best of"（Wilson 下界）** | 稀疏投票的置信下界思想 | 需自有投票流量，本站没有 |
| **HN / Reddit hot ranking** | 时间衰减（用在 C 的提及衰减上） | `score/(t+2)^gravity` 是给**新鲜内容**排序的，书不适用 |
| **Amazon / O'Reilly 畅销榜** | 出版社层级信号 | 销量数据拿不到（PA-API 需联盟资质），不设计依赖 |
| **awesome-\* / free-programming-books** | 收录次数作补充声量信号 | 列表本身的收录标准不透明，只做次要权重 |

---

## 8. 版本治理

| 规则 |
|------|
| `standard_version` 写进分数表每一行，格式 `major.minor`（如 `1.0`） |
| 改公式、改归一化、改维度定义 → `major` 升版 |
| 改出版社层级表、改半衰期表、改权重预设 → `minor` 升版 |
| 升版**必须附「20 本已知书的排名对比」**，防止悄悄调权重把自己喜欢的书调上去 |
| 历史分不删；前端可切换版本查看同一本书的分数演变 |
| 同一 `standard_version` + 同一份 evidence 快照 ⇒ 分数**逐位可复现**（[requirements.md](requirements.md) NFR-10） |
