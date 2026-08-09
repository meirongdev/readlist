# 日常使用

写给库主人的操作手册：加书单、查问题、对着真实书库跑、该盯哪些指标。
规格问题看 [spec/](../spec/)，上线看 [deploy.md](deploy.md)。

---

## 1. 加一份书单

**加榜 = 加一段 YAML**，不改代码、不重算分数。编辑
[`internal/preset/presets.yaml`](../../internal/preset/presets.yaml)，重启即生效。

```yaml
- id: my-list                 # 稳定标识,会进 URL
  name: 我的新书单
  description: 一句话说清楚这份榜挑的是什么
  weights: { A: 0.5, T: 0.3, readability: 0.2 }   # 之和必须为 1
  needs: { A: measured }                          # 逐维硬门:不满足则不进这份榜
  select: { size: 20, max_per_topic: 3, max_per_author: 1, min_coverage: 0.7 }
  filters: { min_age_years: 3 }                   # 可选
  order: desc
  visibility: public                              # internal = 不进公开导航
```

维度代号：`A` 口碑 · `C` 技术圈声量 · `F` 时效 · `T` 权威 · `D` 深度 ·
`P` 可操作 · `readability` 馆藏可读性。

**加载期会强制校验**（`internal/preset.Validate`），写错直接起不来 —— 这是故意的，
配置错误如果只是静默失效，你会得到一份看起来正常、实际上卖点不成立的榜。校验包括：
权重和为 1、`bands` 的维度必须占非零权重、维度名与证据状态合法。

三个容易踩的点：

- **不参与的维度直接不写**，不需要"豁免"。逐本 renormalize 由 coverage 承担。
- **`needs` 与 `weights` 是两件事。** `needs` 是准入硬门，可以声明未加权的维度 ——
  「近一年新书」要求 `F: measured`（出版日期必须可信）却不给 F 权重，因为新书之间
  比时效没有意义。
- ⚠️ **别给 `D`/`P` 权重，别按 `level` / `topics_any` 过滤。** 这四样只来自 `labels` 表，
  而真实库上那张表是空的（见 §3）。这样的榜在演示语料上一切正常，在生产库上恒为空。

改完先 `make smoke` 看一眼每份榜选出几本，再 `make check`。

## 2. 书单空了，怎么查

按这个顺序，**不要先怀疑权重** —— 权重不影响准入。

```bash
make smoke        # 或:DB_PATH=... ./bin/readlist dryrun
```

`dryrun` 只数不算，输出每维的实测比例与每份榜选出几本：

```
  dim A           measured  44 / 50 (88%)
  dim F           measured  47 / 50 (94%)
  ...
  preset timeless         selected 10
  preset my-list          selected  0      ← 这里
```

| 症状 | 多半是 |
|------|--------|
| 某维 measured 比例是 **0%** | 这一维没有数据源。`D`/`P` 恒为 0，见 §3 |
| **`C` 维一直是 0** | 主标题 ≤2 词的书不查 HN（宁少不多），需要进白名单（`title_whitelist` 表或 `TITLE_WHITELIST_FILE`）；再查 ingest 是不是每晚预算都被 editions 烧光（`MENTIONS_RESERVE` 保底，默认 Budget/4） |
| 某维比例很低，榜也空 | `needs` 卡住了。放宽 `needs`，或先补证据（跑 `ingest`） |
| 各维都正常但榜仍空 | `filters` 太严（`min_age_years` / `pubdate_within_months` / `read_status`） |
| **`F` 维隔夜骤降** | snapshot 覆写了外部 pubdate —— 这是修过的回归（`resolveExternalCarryOver`），看 snapshot 日志里的「外部 pubdate 保留: N」，N 掉回 0 就是它又回来了 |
| 只选出两三本 | `max_per_topic` / `max_per_author` 的多样性上限咬住了 |
| 数量对但排序不对 | 这才是权重的事 |

准入只有三道门：**`filters` → `needs` → `min_coverage`**。
A/B/C/D 徽章**不参与准入** —— 它曾被当成全局闸门用，后果是 477 本（全库 23%）
仅因出版日期来自 mtime 兜底就从整站消失。别再把它接回准入路径。

## 3. ⚠️ 深度 / 可操作 两维目前没有数据

`D`（深度）、`P`（可操作）读 `labels` 表，而**全仓唯一往 `labels` 写数据的地方是
`corpus.Seed`**（50 本演示语料）—— LLM 标注是 [roadmap](../roadmap.md) 第 6 步，还没做。
`works.level` 被 `corpus.Import` 显式写成空串，主题标签也只从 `labels` 来。

所以真实库上：`D` / `P` 恒为 `unknown`，`level` 恒为空，`topics_any` 永不命中。

**这意味着 `make smoke` 会骗你** —— 演示语料里 D/P 有 90% 的实测率，和真实库正好相反。
想复现生产状态：

```bash
DB_PATH=/tmp/t.db ./bin/readlist seed
sqlite3 /tmp/t.db "DELETE FROM labels;"
DB_PATH=/tmp/t.db ./bin/readlist score && DB_PATH=/tmp/t.db ./bin/readlist dryrun
```

动任何与评分相关的东西之前，用这个 DB 验一遍。

## 4. 对着真实书库跑

```bash
make pipeline SOURCE_METADATA_DB=/path/to/metadata.db SOURCE_APP_DB=/path/to/app.db
```

它依次跑三步，各自的边界是**刻意分开的**：

| 命令 | 碰什么 | 联网 |
|------|--------|------|
| `snapshot` | Calibre 的两个库（唯一会碰的命令） | **否** |
| `ingest` | 只碰自己的库 | **是**（唯一联网的命令，配额受限、可续跑） |
| `score` | 只碰自己的库 | 否 |

生产上这三步是三个独立的 CronJob。**能碰 Calibre 卷的作业没有出网权限，能出网的作业
不挂 Calibre 卷** —— 这是整套系统的安全边界，别把它们合成一个。

只想验证快照、不联网：`make snapshot SOURCE_METADATA_DB=...`

⚠️ **上线前必须配 `GOOGLE_BOOKS_KEY`。** Google Books 的匿名配额按共享项目计，
一次探测请求就能直接拿到 429，届时口碑与时效两维基本拿不到数据。

⚠️ 首轮 `ingest` 要跑几晚。`INGEST_BUDGET=800` 是**每次运行**的上限，全库
editions 约需 1,000–1,500 次请求、HN 声量再加一批；下一晚会自动跳过已缓存的。
每晚有 `MENTIONS_RESERVE`（默认 Budget/4）给 HN 保底 —— editions 烧到只剩保底线
就让位，所以 `C` 维从第一晚就开始积累，不用等 editions 全部查完。
editions 按 **pubdate 新→旧**排队：新书最先拿到外部证据，「近一年新书」榜最先受益。

## 5. 补录阅读状态

「下一本读什么」和已读徽章的输入。实测**只有 23 本（1.1%）有阅读状态** ——
管道好写，补录才是工作量，而那只有你自己能做。

状态在 calibre-web 的 `app.db` 里（不是书库的 `metadata.db`），`readlist` **只读、永不写回**。
建 `想读` / `弃读` / `精读` 书架，给已读的书补个人星级，下一次 `snapshot` 就会带进来。

完整的状态模型、补录方式与最小导出 SQL 见 [spec/reading-status.md](../spec/reading-status.md)。

## 6. 该盯哪些指标

`/metrics`（Prometheus 格式，不进公开导航）：

| 指标 | 看它做什么 |
|------|-----------|
| `readlist_dim_measured{dim=...}` | **判别力的直接度量。** 某维掉到 0 = 数据源断了 |
| `readlist_pubdate_source{source="mtime-fallback"}` | 应当随 `ingest` 推进**趋于 0** |
| `readlist_last_{snapshot,ingest,score}_unix` | 三个作业各自的新鲜度，任一停摆都能看出来 |
| `readlist_ingest_budget_exhausted` | 配额打满，说明还没收敛，第二晚会接着跑 |
| `readlist_orphan_rows` | 孤儿行，聚类或删书出问题时会涨 |
| `readlist_works_total` | 全库规模。**这是唯一对外暴露全库信息的地方**，是运维信号不是内容 |
| `readlist_grade_counts{grade=...}` | 证据徽章分布。注意它取自发布时算好的值，改了徽章口径后会滞后到下一次 `score` |

`/healthz` 查库（readiness），`/livez` 不碰库（liveness）—— 数据库慢是「别给我流量」，
不是「重启我」，两者不能混。

## 7. 回滚

每次 `score` 写一个新 `run_id`，`published_run` 是个单行指针，切换是原子的。
默认保留最近 5 个 run（`KEEP_RUNS`），发布时在同一事务里回收更老的。

比较两次打分的排名差异（改评分标准时的必备材料）：

```bash
./bin/readlist diff <runA> <runB>
```

调低 `KEEP_RUNS` 前先确认回滚窗口还够用。
