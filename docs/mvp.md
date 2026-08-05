# readlist MVP —— 可部署的单二进制实现

> 状态: 已实现,本地 kind 端到端验证通过(2026-08-05)
> 对应规格: [system-design.md](system-design.md)(三层:facts → judgement → selection)
> 实现评审与修法: [review-2026-08-05.md](review-2026-08-05.md)(4 个阻塞级 + 9 项重要问题已修)

## 1. 这是什么

一个单 Go 二进制的可部署 MVP:SQLite 存储 + 评分引擎 + 只读 API + 内嵌 SPA。
复用同仓库家族 `trends` 的部署形状(单二进制 / arm64 / SQLite / 单副本 Recreate)。
代码全部离线可构建(CGO=0,modernc 纯 Go SQLite)。

## 2. 命令

```
readlist snapshot  # 读 calibre 两库 → works/editions/reading(唯一碰书库的命令,不联网)
readlist ingest    # 外部证据摄入(唯一联网的命令,配额可续跑)
readlist score     # judgement + selection + publish(离线,禁止发起网络请求)
readlist dryrun    # 只数不算:每维 measured 比例 + 每榜已选数
readlist diff A B  # 两个 run 的榜单差异(升版评审材料)
readlist serve     # 只读 API + 内嵌 SPA(未发布时先自愈打分一次)
readlist seed      # 写入演示语料(50 个 work,幂等)—— 仅本地演示用
readlist init      # seed + 首次 score(供 kind initContainer;已发布则跳过)
```

生产上三个命令按 **snapshot → ingest → score** 每夜串行,各自是独立的 CronJob。
这样切分是有意的:**能碰 calibre 卷的作业零网络出口,能出网的作业碰不到 calibre 卷**
(参照清单里由 NetworkPolicy + 卷挂载分离共同保证)。

`snapshot` 的关键性质:
- `metadata.db` 走 `VACUUM INTO`(WAL 库不能直接只读查);目标文件先删,否则第二夜起必然失败;
- `app.db` **只导出 3 张表的少数几列**(含密码 hash 与 OIDC 凭据,绝不整库拷),
  按 `user_id` 过滤、join 书目丢孤儿并把孤儿数写进 `runs`;
- pubdate 与文件 `last_modified` 同一天 → 判为 `mtime-fallback`。**snapshot 产出的三种
  来源没有一种在可信名单里**,所以时效维度在 ingest 之前一律 `unknown`(R-1)。

`ingest` 的关键性质:
- 评分证据按**外部实体 id** 存(Google volume / OL work),同一 work 的多个版次不会把
  同一份评分计两遍;"查不到"也缓存,免得每晚重烧配额;
- 顺手写 `pubdate` + `pubdate_source`,**不需要先修 calibre 的库**(review M2);
- 预算打满就干净停下,下次接着跑;某源 429 则熔断该源,其余照常。

## 3. 覆盖的规格要点

| 规格 | 实现 |
|------|------|
| 7 维评分 | A 贝叶斯加权(work 级汇总后收缩一次)/ C 时间衰减 / F 半衰期 / T 出版社×作者 / D·P LLM 标注 / readability 本地 |
| 归一化 | 每维 CDF 只由 measured 构建,并列 mid-rank(浮点);shrunk 映射到先验位置 |
| 逐维证据状态 | `measured / shrunk / unknown`,preset 用 `needs` 声明要求(硬门,含未加权维度),逐本 renormalize + `coverage` |
| 准入 | **只有** `filters` + `needs` + `min_coverage`;证据字母**不参与准入**,只做徽章 |
| 选材层 | 贪心 + `max_per_topic` / `max_per_author` / `min_coverage` + 确定性理由串 |
| 可复现 | 无 map 迭代序依赖 + `work_id` 并列打破键 + 每个 run 落真实 `corpus_id`/`facts_hash` |
| 原子发布 | `published_run` 单行指针;`lists`/`dim_scores`/`norm_cdf` 按 run 存;发布同事务里回收超出 `KEEP_RUNS` 的旧 run |
| 配置校验 | `presets.yaml` 加载即校验(权重和为 1、`bands ⊆ weights`、维度名与状态合法……),写错则启动失败 |
| 只读 API | `GET /api/v1/lists` `{id}` `works/{id}` `matrix/{run}` `catalog` `/metrics` `/healthz`;非 GET 一律 405;未知 run 404 |
| 前端 | 内嵌 SPA:预设切换 + 权重滑块(纯客户端点积/band/coverage 重排,与后端公式逐位一致)+ 阅读徽章 + 目录页逐本标注缺哪几维 |
| 阅读状态 | 只读镜像,facet 不进分;受 `EXPOSE_READ_STATUS` 控制;`to-read-next` / `read-and-loved` 由它派生 |

## 4. 演示语料

`seed` 生成 50 个 work(含 3 组多版次聚类、2 本中文书、未来日期 1 本、低置信 1 本),
覆盖全部预设,其中 `read-and-loved` 演示"补录个人评分后开起来"。

⚠️ **演示语料只用于本地与 kind**。生产的 initContainer 不该跑 `seed` ——
`deploy/oracle/` 的清单里没有 `init`,数据全部来自 `snapshot`。

## 5. 构建 / 测试 / 本地运行

```bash
make check          # fmt + vet + go test + test-spa(离线)
make test-spa       # 校验 SPA 客户端重排与后端公式逐位一致(需 node,缺则跳过)
make smoke          # seed + score + dryrun(本地直跑)
make run            # 本地起服务(:8080,内嵌 SPA)
```

`test-spa` 单列一项是因为权重滑块把 `score.Combine` 的公式在 `app.js` 里实现了第二遍,
两份实现之间没有编译器把关。它起一个真实服务,在 node 里加载真正的 `app.js`,
逐本比对客户端与后端算出的 TBS / coverage / 位次。

## 6. kind 端到端

```bash
make e2e            # 等价于 ./scripts/e2e-kind.sh
```

脚本做的事:建 kind 集群 → `docker build`(多阶段,arm64)→ `kind load` →
`kubectl apply -k deploy/kind` → 等 rollout → 断言以下行为:

- `/healthz` 正常且带 run_id 与 corpus_id;`/api/v1/meta` 有 run_id 与 standard_version
- 公开榜列表不含 `library-hygiene`(internal),按 id 直接请求它也 404
- 每份榜都带滑块所需口径:`weights`(和为 1)、`order`、`min_coverage`,且 `bands ⊆ weights`
- `timeless` 榜:有内容、TBS 为正、带理由串、coverage 达门槛;
  **且含至少一本 F 为 unknown 的书** —— 该榜不使用时效维度,证据字母不该当闸门(review B1 回归)
- `/api/v1/works/{id}`:得分拆解 + standard_version + 版次 + 外链为已转义的 https
- `/api/v1/catalog`:收录全库,且至少有一本被标注缺失维度(不静默剔除)
- `/metrics`:Prometheus 指标(等级计数 / 保留 run 数 / last_score)
- `matrix/{run}`:真实 run 可访问且带 immutable 缓存头;**未知 run 必须 404**(否则空矩阵被永久缓存)
- 零写接口:POST/PUT/DELETE 一律 405
- `read-and-loved` 有内容;SPA 首页可访问
- SPA 客户端重排与后端公式逐位一致(有 node 时执行 `scripts/spa-parity.js`)

部署清单:命名空间 + PVC(100Mi,local-path)+ Deployment(单副本 Recreate,
initContainer `readlist init` 建库并在**尚未发布 run 时**打分)+ NodePort Service(30080)
+ CronJob 每夜 `readlist score`(发布同事务里回收超出 `KEEP_RUNS` 的旧 run)。

## 7. 与生产路线的关系

- `snapshot` / `ingest` 已实现并测试(见 `internal/calibre`、`internal/facts`),
  但**尚未对着真实 calibre 卷跑过** —— 那需要 homelab 侧的清单先落地。
  剩余上线工作见 [homelab-deploy.md](homelab-deploy.md)。
- 生产参照清单在 `deploy/oracle/`(真相源仍是 homelab 仓库);`deploy/kind/` 只作本地验证。
- 镜像 CI 已就位:打 `v*` tag → buildx 推 amd64+arm64 到 ghcr(禁 `latest`)。
- **尚未做且不阻塞上线**:LLM 标注(D/P 两维缺失时依赖它们的榜自然为空,诚实且安全)、
  `eval/gold.yaml` 门禁、无标识符那 1,000+ 本的标题搜索(开放问题 #1)。
