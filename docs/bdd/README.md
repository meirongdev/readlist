# BDD 规格（Gherkin）

> 日期: 2026-08-04
> 状态: 📐 行为规格（未实现），作为实现与验收的基准
> 上游: [requirements.md](../requirements.md)（需求规格）、[scoring-standard.md](../scoring-standard.md)

本目录把需求与验收标准转成**行为可测**的 Gherkin 场景。每个 feature 文件对应一组用户可见
行为，顶部标注其覆盖的 FR / NFR / AC。

## 文件清单

| 文件 | 覆盖 | 关键需求 |
|------|------|---------|
| [ranking.feature](ranking.feature) | 榜单：预设切换、权重滑块、过滤、豁免、C 级不上榜 | FR-30~35, FR-40, FR-42, AC-P2/AC-P3 |
| [book-detail.feature](book-detail.feature) | 书详情：得分拆解、数据来源、封面外链 | FR-34, FR-45, FR-51, FR-54, NFR-11/12 |
| [reading-status.feature](reading-status.feature) | 阅读状态：徽章、筛选、只读镜像、公开性 | FR-40~46, NFR-13/15, AC-P0 |
| [catalog.feature](catalog.feature) | 全库目录：C 级标注、D 级不公开 | FR-25, FR-52, FR-33, 开放问题 Q2 |
| [scoring.feature](scoring.feature) | 评分行为：复算一致、缺数据收缩、来源可信度 | FR-20~27, NFR-10, AC-P1/AC-P2 |
| [ingestion.feature](ingestion.feature) | 数据接入：快照、最小导出、缓存、配额、归一 | FR-10~17, NFR-9/13/16, AC-P1 |
| [api.feature](api.feature) | 只读 API 与边缘限流、降级 | FR-53, NFR-4/14, AC-P3 |
| [observability.feature](observability.feature) | 指标、静默过期告警、手动重算 | FR-60~63, AC-P4 |

## 约定

- 步骤语言用中文，文件头声明 `# language: zh-CN`，关键字用该 dialect 的中文形式
  （假设/假如/当/那么/而且）。**Gherkin 的 dialect 是互斥的** —— 声明了 `zh-CN`
  就不能混用 Given/When/Then，主流 runner（Cucumber / godog / behave）都按声明的
  dialect 解析。
- 场景中的"系统"指 readlist 应用；"快照"指每夜 CronJob 的产物（`metadata.db` + `reading.db`）。
- **外部依赖不可控**：Google Books / OpenLibrary / HN / LLM 网关都视为外部黑盒，场景只描述
  系统对这些依赖的**确定行为**（缓存、降级、配额停发），不描述外部本身。
- 优先级标签：`@P0`（首版必须有）、`@P1`（首版应有）、`@P2`（可后置）。
- 阻塞标签：`@blocked` = 依赖前置（如 Phase 1 元数据修复）完成才可验收。
- 一个场景 = 一条可执行验收；与 [requirements.md §7](../requirements.md#7-验收标准) 的
  AC 一一对应，AC 是产品层验收，场景是行为层验收。

## 如何在实现阶段使用

1. Phase 2（离线打分管线）先落 `scoring.feature` + `ingestion.feature` 的 `@P0` 场景；
2. Phase 3（Web）落 `ranking` / `book-detail` / `reading-status` / `catalog` / `api` 的 `@P0` 场景；
3. Phase 4（收尾）落 `observability` 与全部 `@P1` 场景；
4. `@blocked` 场景只有在对应前置完成后才从"跳过"改为"必须通过"。
