# readlist

> 一个人的技术书单站。

我有一个 2,054 本的 Calibre 技术书库。`readlist` 给里面的书打分，挑出几份书单公开出来 ——
**站上只有上过榜的那百来本**，全库既不展示也不提供检索。不发书，只发书单和上榜书的元数据。

## 三份书单

| 书单 | 挑的是什么 | 怎么排的 |
|------|-----------|---------|
| **经典长青** | 出版满 3 年、经得起时间检验的书 | 口碑 35% · 技术圈声量 30% · 权威 25% · 馆藏可读性 10% |
| **近一年新书** | 近 12 个月出版、且**出版日期可信**的书 | 口碑 40% · 技术圈声量 33% · 权威 27% |
| **下一本读什么** | 高分 ∩ 我还没读的 | 口碑 35% · 技术圈声量 30% · 权威 25% · 馆藏可读性 10% |

每份书单就是一组权重 —— 同一批分数，换一组权重就是另一份榜。权重直接印在书单页标题下面，
不藏在后台：**你看到的排名是按什么算的，页面上写着**。

## 分数怎么读

书名旁的数字（TBS，0–100）是四个维度的加权平均：

| 维度 | 看的是什么 | 数据从哪来 |
|------|-----------|-----------|
| **口碑** | 跨源评分，按评分人数做贝叶斯收缩 —— 3,000 人打的 4.5 分比 5 个人打的 5.0 分更可信 | Google Books / OpenLibrary |
| **技术圈声量** | Hacker News 上被提到过几次，越久远的提及权重越低 | HN Algolia |
| **权威** | 出版社层级 × 作者是否可考 | 本地元数据 |
| **馆藏可读性** | 格式、封面、简介、ISBN 是否齐全 | 本地元数据 |

还有三件事值得知道：

- **「按 4/4 维评出 · 覆盖 100%」** —— 某一维实在没有证据时，它不会被填一个猜出来的数，
  而是被**整个移出这本书的权重**再重新归一。所以覆盖率低的书，分数是在更少的维度上算的。
- **A/B/C/D 徽章** —— 只表示证据有多足，**不参与任何准入**。A = 四维全有实测证据；
  D = 有维度连收缩都不合理。它不是"书好不好"的评级。
- **「为什么」那一行** —— 每本书上榜的理由是算出来的，不是写出来的。人工置顶的书会明说
  「人工置顶」：策展不该伪装成算法结果。

分数**只在本库内部可比**。它衡量的是「在我这 2,054 本里，这本书的证据有多强」，
不是这本书在世界上的绝对排名。

## 为什么搜不到某本书

公开面 = **三份书单的并集**，不是全库目录。没上榜的书按 id 直接请求也是 404。
「上榜书目」页是这三份榜去重后的总表，不是我的藏书清单。

有些书进不了榜是因为**缺证据**而不是因为差：一本没有 ISBN 的自出版好书拿不到外部评分，
就进不了要求「口碑可信」的榜。这是诚实，不是筛选。

## 自己跑一份

```bash
make run      # 起服务 :8080,内嵌页面,首次会自动用演示语料打一次分
make smoke    # 不起服务,只看每维实测比例与每份榜选出几本
```

演示语料是 50 本内置的书，不需要 Calibre 库。想对着真实书库跑见
[docs/guide/operating.md](docs/guide/operating.md)。

## 文档

**日常使用（库主人）**

| | |
|---|---|
| [guide/operating.md](docs/guide/operating.md) | 加一份书单、书单空了怎么查、对着真实书库跑管道、该盯哪些指标 |
| [guide/deploy.md](docs/guide/deploy.md) | 上线：homelab 清单、镜像 CI、首轮引导、备份与限流 |

**规格与设计（改代码前读）**

| | |
|---|---|
| [spec/requirements.md](docs/spec/requirements.md) | 需求分析（主文档）：目标、场景、FR/NFR、验收标准 |
| [spec/prd.md](docs/spec/prd.md) | 产品需求：定位、成功指标、发布计划 |
| [spec/data-baseline.md](docs/spec/data-baseline.md) | **实测数据基线** —— 全部设计决定的地基，含可复跑 SQL |
| [spec/scoring-standard.md](docs/spec/scoring-standard.md) | **评分标准 TBS v1.0**：7 维公式、归一化、证据分级、榜单预设 |
| [spec/system-design.md](docs/spec/system-design.md) | **架构权威规格**：三层管道、逐维证据、选材层、公开面定义 |
| [spec/reading-status.md](docs/spec/reading-status.md) | 阅读状态：真相源、状态模型、补录、最小导出 |
| [spec/architecture.md](docs/spec/architecture.md) | 单二进制形状、快照隔离、部署与可观测 |
| [spec/mvp.md](docs/spec/mvp.md) | 已实现的命令与 API、kind 端到端验证 |
| [spec/bdd/](docs/spec/bdd/) | BDD 行为规格：Gherkin 场景，需求 → 验收的可执行映射 |
| [roadmap.md](docs/roadmap.md) | 分期落地、风险、开放问题 |

**历史评审记录** —— 记录当时的缺陷与修法，**不是现行规格**：
[一轮·文档](docs/archive/review-2026-08-04.md) ·
[二轮·实现](docs/archive/review-2026-08-05.md) ·
[三轮·面向上线](docs/archive/review-2026-08-05-b.md)

## 状态

全管道已实现（`snapshot` → `ingest` → `score` → `serve`），kind 端到端通过，镜像 CI 就位。
上线剩余工作在 [homelab](https://github.com/meirongdev/homelab) 仓库与 Cloudflare（限流），
见 [guide/deploy.md](docs/guide/deploy.md)。

⚠️ 深度 / 可操作 两维尚无数据源（需要 LLM 标注，roadmap 第 6 步），靠它们准入的书单
在真实库上恒为空，所以暂不公开。标注管道落地后把 YAML 加回来即可，不用改代码。

**部署清单的唯一真相源在 [homelab](https://github.com/meirongdev/homelab) 仓库**
（`cloud/oracle/manifests/`），ArgoCD 只指向那里；本仓库的 `deploy/` 只是 kind 与参照用。
反过来，**需求与评分标准的唯一真相源是本仓库**，homelab 侧不保留副本。
