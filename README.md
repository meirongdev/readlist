# readlist

> 技术书多维评分标准 + 公开书单站。
> 把私有 Calibre 书库（2,054 本）按 **7 个独立维度**打分，榜单 = 维度分上的**权重档案**，
> 并标注每本书的**阅读状态**。公开站只发元数据与评分，**不发书**。

**状态**：✅ 全管道已实现（`snapshot` → `ingest` → `score` → `serve`），kind 端到端通过，镜像 CI 就位。
🟡 上线剩余工作在 **homelab 仓库**（清单登记 + 备份归属）与 Cloudflare（限流）——
见 [docs/homelab-deploy.md](docs/homelab-deploy.md)。
**日期**：2026-08-05

---

## 这是什么

网上的技术书排行榜基本只有一两个维度（读者评分，或社区提及数），而技术书区别于一般书的
两件事恰好是通用榜最薄的地方：

1. **会过时** —— 一本讲 React 18 的书和一本讲编译原理的书，"三年前出版"的含义完全不同；
2. **分层级** —— 深度不是"越高越好"，它取决于你现在要解决什么问题。

`readlist` 把这两件事做成显式的维度（时效按**主题半衰期**衰减、深度按**目标带**而非单调加权），
再让榜单成为权重向量而不是写死的排序。同一份分数因此能同时支撑
「经典长青」「最快上手」「深挖原理」「今年新书」，以及最有用的那个 ——
**「下一本读什么」= 高分 ∩ 未读**。

## 文档

按这个顺序读：

| 文档 | 回答什么 |
|------|---------|
| [docs/requirements.md](docs/requirements.md) | **需求分析**（主文档）：目标、场景、FR/NFR、验收标准 |
| [docs/prd.md](docs/prd.md) | **产品需求文档（PRD）**：定位、成功指标、发布计划 |
| [docs/bdd/](docs/bdd/) | **BDD 行为规格**：Gherkin 场景，需求 → 验收的可执行映射 |
| [docs/data-baseline.md](docs/data-baseline.md) | **实测数据基线** —— 全部设计决定的地基，含可复跑 SQL |
| [docs/scoring-standard.md](docs/scoring-standard.md) | **评分标准 TBS v1.0** 规格：7 维公式、归一化、证据分级、榜单预设 |
| [docs/reading-status.md](docs/reading-status.md) | **阅读状态**：真相源、状态模型、补录、最小导出 |
| [docs/architecture.md](docs/architecture.md) | 架构、数据模型、部署形状 |
| [docs/mvp.md](docs/mvp.md) | **MVP 实现**：命令、API、kind 端到端验证 |
| [docs/review-2026-08-05.md](docs/review-2026-08-05.md) | **实现评审**：4 个阻塞级缺陷（可复现性 / 准入闸门 / 公开面 / 滑块）与修法 |
| [docs/homelab-deploy.md](docs/homelab-deploy.md) | **上线剩余工作归档**：homelab 清单 / 镜像 CI / 数据管道 / 备份 / 限流 |
| [docs/roadmap.md](docs/roadmap.md) | 分期落地、风险、开放问题 |

## 两条必须先知道的实测结论

摸过生产库之后，有两件事和直觉不一样，它们决定了排期：

1. ⚠️ **`pubdate` 已被污染。** 477 本书标着"2026 年出版"（今年才过去 7 个月）——
   来自 2026-07 那次元数据补全的 mtime 兜底，把「文件修改时间」写成了出版日期。
   **不先修，时效维度和「今年新书」榜就是假的。**
2. ⚠️ **阅读状态只覆盖 23 本（1.1%）。** 状态确实在 SQLite 里（calibre-web 的 `app.db`，
   不是书库的 `metadata.db`），但管道好写、**补录才是工作量**，而那只有库主人自己能做。

细节见 [docs/data-baseline.md](docs/data-baseline.md)。

第 1 条现在由代码强制：`snapshot` 产出的 `pubdate` 来源（`calibre` / `mtime-fallback` /
`unknown`）**没有一种在可信名单里**，时效维度一律记 `unknown`；只有 `ingest` 从
Google Books / OpenLibrary 拿到的日期才算数。这也意味着修 `pubdate` **不需要**先去改
calibre 的库 —— 外部响应里本来就带 `publishedDate`。

第 3 条实测新增（2026-08-05）：⚠️ **Google Books 的匿名配额是按共享项目计的**，
一次探测请求就直接拿到 429。上线前必须配 `GOOGLE_BOOKS_KEY`，否则 A 维与 F 维基本拿不到数据。

## 与 homelab 仓库的关系

- **本仓库**：产品需求、评分标准规格、应用源码与镜像 CI。
- **[homelab](https://github.com/meirongdev/homelab) 仓库**：**部署清单的唯一真相源**
  （`cloud/oracle/manifests/`），ArgoCD 只指向那里。上线所需的动作（清单 + kustomize 登记
  + HTTPRoute + 备份归属 + 边缘限流）挂在那边的 `docs/ROADMAP.md` 开放项里；
  **本仓库是需求与评分标准的唯一真相源**，homelab 侧不再保留副本。

本仓库里如果日后放 `deploy/` 参照清单，那只是参照 —— 集群实际部署的是 homelab 仓库那份。
