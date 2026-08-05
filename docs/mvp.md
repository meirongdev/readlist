# readlist MVP —— 可部署的单二进制实现

> 状态: 已实现,本地 kind 端到端验证通过(2026-08-05)
> 对应规格: [system-design.md](system-design.md)(三层:facts → judgement → selection)

## 1. 这是什么

一个单 Go 二进制的可部署 MVP:SQLite 存储 + 评分引擎 + 只读 API + 内嵌 SPA。
复用同仓库家族 `trends` 的部署形状(单二进制 / arm64 / SQLite / 单副本 Recreate)。
代码全部离线可构建(CGO=0,modernc 纯 Go SQLite)。

## 2. 命令

```
readlist init      # seed(演示语料)+ score + publish(供 kind initContainer)
readlist seed      # 写入演示语料(53 个版次 → 50 个 work,幂等)
readlist score     # judgement + selection + publish(离线,不发网络请求)
readlist dryrun    # 只数不算:每维 measured 比例 + 每榜已选数
readlist diff A B  # 两个 run 的榜单差异(升版评审材料)
readlist serve     # 只读 API + 内嵌 SPA(未发布时先自愈打分一次)
```

## 3. 覆盖的规格要点

| 规格 | 实现 |
|------|------|
| 7 维评分 | A 贝叶斯加权 / C 时间衰减 / F 半衰期 / T 出版社×作者 / D·P LLM 标注 / readability 本地 |
| 归一化 | 每维 CDF 只由 measured 构建,并列 mid-rank;shrunk 映射到先验位置 |
| 逐维证据状态 | `measured / shrunk / unknown`,preset 用 `needs` 声明要求,逐本 renormalize + `coverage` |
| 选材层 | 贪心 + `max_per_topic` / `max_per_author` / `min_coverage` + 确定性理由串 |
| 证据徽章 | A=全维实测 / B=有收缩 / C=主要本地 / D=关键维 unknown(不公开) |
| 原子发布 | `published_run` 单行指针;`lists`/`dim_scores`/`norm_cdf` 按 run 存 |
| 只读 API | `GET /api/v1/lists` `{id}` `works/{id}` `matrix/{run}` `catalog` `/metrics` `/healthz`;非 GET 一律 405 |
| 前端 | 内嵌 SPA:预设切换 + 权重滑块(纯客户端点积/band/coverage 重排)+ 阅读徽章 + C 级"数据不足" |
| 阅读状态 | 只读镜像,facet 不进分;`to-read-next` / `read-and-loved` 由它派生 |

## 4. 演示语料

`seed` 生成 50 个 work(含 3 组多版次聚类、2 本中文书、D 级 3 本、低置信 1 本),
覆盖全部 8 个预设,其中 `read-and-loved` 演示"补录个人评分后开起来"。

## 5. 构建 / 测试 / 本地运行

```bash
make check          # fmt + vet + go test(离线)
make smoke          # seed + score + dryrun(本地直跑)
make run            # 本地起服务(:8080,内嵌 SPA)
```

## 6. kind 端到端

```bash
make e2e            # 等价于 ./scripts/e2e-kind.sh
```

脚本做的事:建 kind 集群 → `docker build`(多阶段,arm64)→ `kind load` →
`kubectl apply -k deploy/kind` → 等 rollout → 断言以下行为:

- `/healthz` 正常且有 run_id;`/api/v1/meta` 有 run_id
- 公开榜列表不含 `library-hygiene`(internal)
- `timeless` 榜:有内容、无 D 级书、TBS 为正
- `/api/v1/works/{id}`:得分拆解 + standard_version + 版次
- `/api/v1/catalog`:含 C 级、不含 D 级
- `/metrics`:Prometheus 指标(等级计数 / last_score)
- SPA 首页可访问
- 零写接口:POST/PUT/DELETE 一律 405
- `read-and-loved` 有内容;`matrix/{run}` 可访问(滑块数据)

部署清单:命名空间 + PVC(100Mi,local-path)+ Deployment(单副本 Recreate,
initContainer `readlist init` 建库打分)+ NodePort Service(30080)+ CronJob 每夜 `readlist score`。

## 7. 与生产路线的关系

- 生产上 `snapshot`/`ingest`(读 calibre 两个库)尚未接入 —— 那是 `system-design.md` 的
  第 1–5 步,依赖真实 calibre 卷;本 MVP 用 `seed` 演示同一套三层管道。
- `eval/gold.yaml` 门禁、`half_life_rules.yaml` 外置、外部配额缓存是下一步。
- 部署清单真相源仍在 homelab 仓库;本仓库的 `deploy/kind/` 只是本地验证参照。
