# 评分标准 TBS v1.0（Tech Book Score）

> 日期: 2026-08-04（§1 / §4.3 / §5 / §6 于 2026-08-05 随实现修订）
> 状态: ✅ v1.0 已实现，见 [mvp.md](mvp.md)
> 数据可得性依据: [data-baseline.md](data-baseline.md)
> 证据模型: 逐维三态 + preset `needs`，见 [system-design.md §2](system-design.md)

这是**规格文档**，不是设计讨论。实现应当逐条对照本文；本文改动 = 标准升版。

> **本文取代过的三处写法**（都曾在实现里造成静默失效，修订记录见
> [review-2026-08-05.md](../archive/review-2026-08-05.md)）：
> 单一 A/B/C/D 证据闸门 → 逐维三态 + `needs`（§5）；preset 级 `exempt` → 逐本
> renormalize 与 `coverage`（§4.3）；`filters.min_evidence` → `needs`（§6）。

---

## 1. 四条设计原则

1. **维度独立测量，榜单靠权重组合。**
   一本书的维度分是"事实"，权重是"价值判断"。加一个新榜 = 加一个权重向量，**不重算数据**。
2. **缺数据 ≠ 0 分。**
   缺数据用贝叶斯收缩到语料先验（`shrunk`）；连收缩都不合理的记 `unknown`，该维
   **不参与任何计算**，而不是给它一个编出来的分。
   借 Metacritic 的 "not enough reviews"，但判定是**逐维度**的：一本书某一维证据不足，
   只影响需要那一维的榜单，不影响它在其他榜单里的资格（§5）。
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
不可信 → **F = 未知**，不参与任何按 F 排序的榜。

它**不影响证据徽章** —— 当前没有一份榜给 F 权重，一个不参与排序的维度不该给排名结果
打分（见 §5.2）。缺失照样如实披露，只是走「逐维标注缺哪几维」那条路，不压进一个字母里。

⚠️ **守住 F 维不等于守住时效**：preset 的时间**过滤器**走的是另一条路径，
被污染的日期同样不许进去（review A2）。两个方向刻意**不对称**，因为代价不对称：

| 问题 | 依据 | 日期未知时 |
|------|------|-----------|
| 够不够**新**（`pubdate_within_months` / `pubdate_year`） | 仅 `TrustedPubdate` | **排除** —— 想上「新书榜」得先证明自己新 |
| 够不够**老**（`min_age_years`） | 未被污染来源里的最早版次 | **放行**，且理由串写明「年龄未核实」 |

「够老」若也失败，那 477 本（23%）只有 mtime 兜底日期、又没有标识符可供 `ingest` 补救的书
会整批从「经典长青」消失 —— 那正是 [review-2026-08-04](../archive/review-2026-08-04.md) B1 判定为
「模型错了」的全局闸门，只是换成从过滤器进来。要严格，preset 自己声明 `needs: {F: measured}`。

「未被污染」比「可信」宽一档：`calibre` 来源（pubdate ≠ 文件 mtime，所以不是兜底值）
够不上 F 维的证据标准，但拿来算年龄是合理的。严格排除的只有 `mtime-fallback` 与缺失。

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

权重和为 1。**renormalize 是逐本的，不是逐 preset 的**：某本书某一维不可用时，把这一维
的权重摊回其余维度，而不是让它吃一个编出来的先验值：

```
可用维度 = { i : state_i ≠ unknown 且 preset 的 needs_i 被满足 }
coverage = Σ_{i ∈ 可用} w_i          # 权重和为 1，所以这就是覆盖率
TBS      = (Σ_{i ∈ 可用} w_i · eff_i) / coverage
eff_i    = band_score(score_i) 若 i 在 bands，否则 score_i
```

- `coverage` 直接进 UI：**「按 5/5 维评出」比「78.3 分」诚实得多**；
- `coverage < min_coverage` → 不进这份书单（但只要上了别的榜，就仍出现在上榜书目里，标注缺哪几维）；
- **不需要 preset 级的 `exempt`**：不参与的维度不写权重即为豁免。原先的写法是恒等变换 ——
  剔除一个权重为 0 的维度、再把已经和为 1 的权重「重新归一」，两步都什么也没做。

---

## 5. 证据状态与证据徽章

### 5.1 逐维三态 —— 准入的唯一依据

证据是**逐维度**产生的，所以状态也落在每个 `(work, dim)` 上，而不是每本书一个字母：

| state | 含义 | 能否参与排序 |
|-------|------|-------------|
| `measured` | 有实测证据（外部评分 / HN 命中 / 可信 `pubdate` / 高置信标注） | 是，有判别力 |
| `shrunk` | 无证据，贝叶斯收缩到语料先验 | 是，但判别力为 0 |
| `unknown` | 连收缩都不合理（`pubdate` 被 mtime 污染的 F、作者与出版社均未知的 T） | **否** |

preset 用 `needs` 声明自己要什么。**准入 = `needs` 全部满足 且 `coverage ≥ min_coverage`**，
没有第三个条件。`needs` 可以声明未加权的维度 —— 「近一年新书」只要求 `F: measured`
（出版日期必须可信），但不给 F 权重（新书之间比时效没有意义）。

### 5.2 证据徽章 —— 只用于展示

徽章评的是**实际参与排序的维度**（`graded` = 在任意一份 preset 里占非零权重的维度，
由 `score.GradedDims` 从 `presets.yaml` 推出），不是七维全看：

| 徽章 | 条件 |
|------|------|
| **A** | `graded` 维全部 `measured` |
| **B** | 有 `shrunk`，但至少一个 `graded` 的**外部证据维**（A/C/F/D/P）有实测信号 |
| **C** | 没有任何外部信号，分数基本靠本地元数据（T/readability）撑着 |
| **D** | 有 `graded` 维为 `unknown` —— 那一维被整个 renormalize 出这本书的权重，它的分是在**比榜单声明更少的维度**上算出来的 |

> ⚠️ **为什么不是七维全看。** 老规则遍历全部七维求 A，并且把 F 或 T 的 `unknown`
> 直接判成 D。两条都出了问题：D/P 没有任何生产数据源（`labels` 表只有 `corpus.Seed`
> 会写，LLM 标注尚未落地），于是 **A 级永远不可达、九成以上的书压在同一个 B 上**，
> 徽章退化成一个恒定值；而 F 当前不被任何一份榜加权 —— 拿一个不影响排序的维度去
> 给排名结果打分，得到的字母没有含义。实测（50 本演示语料，清空 `labels` 模拟生产）：
>
> | | A | B | C | D |
> |---|---|---|---|---|
> | 老规则 | **0** | 46 | 1 | 3 |
> | 新规则 | 37 | 10 | 3 | 0 |
>
> 榜单增删是配置行为，所以 `graded` 必须从 `presets.yaml` 推导而不是写死一份清单 ——
> LLM 标注落地、D/P 重新拿到权重之后，徽章口径自动跟着扩，不需要改代码。

> ⚠️ **这个字母不决定任何准入。**
> 它曾被当成全局闸门，后果是：一本 A/C/T 三维齐备的 O'Reilly 经典，仅因出版日期来自
> mtime 兜底，就被排除在**明确声明不使用时效维度**的「经典长青」榜之外 —— 实测影响
> 全库 23%（477 本）。根因是把逐维度的证据压成一个标量，去卡所有榜单。
> 现在某维缺证据只挡住需要那一维的榜；书只要上了别的榜，就带着「缺哪几维」的标注正常出现。

按实测推算 A/B 合计约 **700–900 本**。这是特性不是缺陷：可信的书单比 2,054 本掺着
1,300 本猜测分的榜有用得多。但「可信」是**逐榜、逐维**判定的，不是一道全站闸门。

⚠️ 别把这个字母和**公开范围**混为一谈。公开范围由「是否上了公开榜」决定（约百余本），
与 A/B/C/D 无关：全库既不按等级公开，也不按等级隐藏 —— 它整个就不进公开面
（见 [system-design.md §10](system-design.md)）。

---

## 6. 榜单预设（权重档案）

配置即数据。**唯一真相源是 [`internal/preset/presets.yaml`](../../internal/preset/presets.yaml)**
（内嵌进二进制）。本文只定义字段语义 —— 此前这里抄了一份完整预设清单，两边随即漂移。

| 字段 | 语义 |
|------|------|
| `weights` | 各维权重，**之和必须为 1**。不参与的维度**直接不写** —— 这就是「豁免」的表达方式，不需要单独的 `exempt` |
| `bands` | 目标带；键**必须同时出现在 `weights` 里**，否则 band 项的系数是 0（空操作） |
| `needs` | 逐维最低证据状态，**硬门**；可以声明未加权的维度 |
| `select` | `size` / `max_per_topic` / `max_per_author` / `min_coverage`（选材约束，见 [system-design.md §5](system-design.md)） |
| `filters` | `min_age_years`（按**最早**版次算年龄，日期未知则放行）/ `pubdate_within_months`（滚动窗口，只认 `TrustedPubdate`）/ `pubdate_source` / `topics_any` / `level` / `read_status` / `not_in_shelf` / `min_personal_rating`（单位：**星 0–5**，即 calibre metadata 值 ÷ 2）。两个时间过滤器的不对称语义见 §3 的 F 维 |
| `order` | `desc`（默认）或 `asc`（`library-hygiene` 要的是最差的那些） |
| `visibility` | `public`（默认）或 `internal`（不出现在公开导航，也不能按 id 直接拉到） |

**人工干预**（`overrides` 表，不走 YAML —— 它是逐本的判断，不是口径）：

| `field` | `value` | 语义 |
|---------|---------|------|
| `veto` | 留空 = 全部榜单；或逗号分隔的榜 id | 这本书不进这些榜。误入公开榜的唯一即时处置手段 —— 此前只能改代码或调权重，而后者会为一本书扭曲所有书的排名 |
| `pin` | 逗号分隔的榜 id（必填） | 强制入榜，排在算法结果之前，**绕过 `filters` / `needs` / `min_coverage` 与多样性约束** |

`pin` 是 [system-design §13](system-design.md) 那个「`timeless` 是否接受一层人工 curation」
的开关。它的代价必须被读者看见：理由串会写明「人工置顶」，**curation 不该伪装成算法结果**。
`mention_overrides`（`verdict='reject'`）同理，是 R-3 承诺的「HN 提及可逐条否决」的落点。

以上每一条都在**加载时强制校验**（`internal/preset.Validate`），写错则进程启动即失败。
理由是这类错误不会报错、只会静默失效：曾有一份榜声明 `bands: { D: … }` 却没给 D 权重，
于是「这份榜要的是中等深度」这个卖点在公式上根本不成立，而榜单看起来一切正常。

**口径随榜公开**：`/api/v1/lists` 随每份榜返回 `weights` / `bands` / `order` /
`min_coverage`，榜单页把 `weights` 直接印在标题下面 —— 「榜单 = 权重档案」是对外主张，
读者有权看见这份排名按什么算。

这里曾经还有一个**前端权重滑块**，让访客在浏览器里改权重实时重排。它已撤除：客户端把
`score.Combine` 又实现了一遍，两份公式之间没有编译器把关，只能靠一条跑真服务的 parity
测试钉住（连带 CI 的 Node 依赖）；而它能做的仅是在**本榜已选出的 ≤20 本内**换个顺序，
把榜外的书换进来需要重跑选材（去重 + 多样性约束），那不是客户端点积能做的事。

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
| 同一 `standard_version` + 同一 `corpus_id` + 同一 `facts_hash` ⇒ 分数**逐位可复现**（[requirements.md](requirements.md) NFR-10）。三元组都要:分位归一是**语料相对**的,往库里加 30 本书会让全部书的分数变化 |
| 每个 run 落 `corpus_id`(works+editions 的内容 hash)与 `facts_hash`(evidence/labels/mentions/overrides/reading 的内容 hash),前端与评审才能区分「分数变了是因为公式变了」还是「因为语料变了」 |
