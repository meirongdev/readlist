# homelab k3s 上线剩余工作

> 日期: 2026-08-05(修订)
> 状态: 🟡 本仓库侧的三个拦路项已清空;**剩下的全部在 homelab 仓库与 Cloudflare**。
> 目标集群: `oracle-k3s`(ARM free tier,单节点),命名空间 `personal-services`。
> 本文是「剩下要做的事」的唯一归档;每一项都标注了动作、落点与依据的规格 ID。

---

## 1. 当前边界

| 仓库 | 角色 | 现状 |
|------|------|------|
| **本仓库** (`meirongdev/readlist`) | 需求/评分标准唯一真相源 + 应用源码与镜像 CI | ✅ 全管道已实现:`snapshot`→`ingest`→`score`→`serve`;镜像 CI 就位;生产参照清单在 `deploy/oracle/` |
| **homelab 仓库** (`meirongdev/homelab`) | **部署清单唯一真相源**;ArgoCD 只指向它 | ❌ 尚未写入任何 readlist 清单 |

> 铁律(C-2):本仓库的 `deploy/` 只是参照;集群实际部署的清单在 homelab 仓库。

---

## 2. 已就绪

| 项 | 依据 |
|----|------|
| 单 Go 二进制 + SQLite/WAL + 内嵌 SPA | architecture §1, D-7 |
| **`readlist snapshot`**:`VACUUM INTO` 一致快照 + `app.db` 最小导出(只 3 张表、`user_id` 过滤、join 丢孤儿并计数) | FR-10, FR-14, NFR-13 |
| **`readlist ingest`**:Google Books / OpenLibrary / HN,原样缓存 + TTL、配额预算可续跑、429 熔断、礼貌限速 | FR-11, FR-12, FR-15, NFR-9 |
| **pubdate 自带来源**:外部响应里就有 `publishedDate` → 写成 `pubdate_source=google\|openlibrary`,**不依赖修 calibre 的库**(review M2) | FR-1x, R-1 |
| 半衰期规则链:BISAC 码 → 标注 → 标题关键词 → 默认 | system-design §6 |
| 镜像 CI:`ci.yml`(check + race + arm64 构建验证)、`image.yml`(buildx amd64+arm64 → ghcr,禁 `latest`) | C-3, NFR-5 |
| 生产参照清单 `deploy/oracle/`:ClusterIP + HTTPRoute、三个 CronJob(带 `timeZone: Etc/UTC`)、快照/打分作业**零网络出口**的 NetworkPolicy、PVC 备份标签 | C-1~C-7 |
| run 保留上限 `KEEP_RUNS`(默认 5),发布同事务里回收 | NFR-17 |
| 单副本 `Recreate` / 显式 resources / 只读 API / Prometheus 指标 | NFR-6, NFR-7, FR-53, FR-60 |
| **抗爬**:快照按 run 进程内缓存、内容端点 `ETag` + 304、`/livez` 存活探针不碰数据库、HTTP 读写空闲超时齐备 | NFR-14, review B2 |

---

## 3. 剩余工作清单

### 3.1 homelab 仓库(拦路项 A —— 上线的前提)

| 动作 | 依据 |
|------|------|
| 把 `deploy/oracle/readlist.yaml` 抄成 `cloud/oracle/manifests/personal-services/readlist.yaml` | C-2 |
| 在该目录 `kustomization.yaml` 的 `resources:` **登记**(漏登记 = 静默不生效) | C-2 |
| 核对 `HTTPRoute` 的 `parentRefs`(参照件里写的是 `cilium-gateway`/`gateway`,以 homelab 实际为准) | C-6 |
| 把 image 换成 CI 推出的**版本化 tag 或 digest** | C-3 |
| 参照同集群 `trends` 的量级复核 request/limit | NFR-7 |
| **把 `readlist-data` 加进 oracle 夜备脚本的 sqlite 列表** —— CI 规则 H4 会拦下没有备份归属的 PVC | C-4, NFR-16 |

> 卷上有不可再生数据:`evidence`(重建要烧 2–3 天配额)、`publisher_map`、`overrides`、
> `title_whitelist`。这些不是派生物,丢了就得重新花配额或重新人工判断。

### 3.2 首轮数据(拦路项 B —— 决定上线的是不是真实书库)

| 动作 | 说明 |
|------|------|
| 首次 `snapshot` | 秒级。产出 works/editions/reading + 孤儿数与 pubdate 污染计数 |
| 首轮 `ingest` | 约 1,000–1,500 次请求 → 按 `INGEST_BUDGET=800` 分 **2 晚**跑完 |
| **配 `GOOGLE_BOOKS_KEY`** | ⚠️ 实测(2026-08-05)匿名配额是**按共享项目**计的,一次探测就拿到 429。没有 key 的话 A 维与 F 维基本拿不到数据 |
| 人工:建 `想读`/`弃读`/`精读` 书架,给 23 本已读补个人星级 | 决定 `to-read-next` / `read-and-loved` 有没有内容,**只有库主人能做** |

### 3.3 边缘限流与可观测(拦路项 C —— 公开前的底线)

| 动作 | 落点 | 依据 |
|------|------|------|
| Cloudflare WAF 分档限流:页面一档,`/api/` 更严一档 | Cloudflare(非代码) | NFR-14 |
| 指标接监控(清单见下) | homelab 监控 | FR-60, FR-61, FR-62 |

⚠️ **别只报 `last_score`**:`score` 在陈旧 facts 上每晚照样成功 —— snapshot 或 ingest
挂掉一个月,这个指标依然常绿,而全站数据已经冻结。而那两个才是容易挂的:一个依赖
calibre 卷还在,另一个依赖外部配额。建议的告警:

| 指标 | 告警条件 | 说的是什么 |
|------|---------|-----------|
| `readlist_last_snapshot_unix` | 距今 > 36h | 语料停止更新(calibre 卷 / CronJob 出问题) |
| `readlist_last_ingest_unix` | 距今 > 72h | 外部证据停止更新(配额 / 网络 / 429 熔断) |
| `readlist_last_score_unix` | 距今 > 36h | 榜单停止重算 |
| `readlist_orphan_rows` | 突增 | book id 漂移(有人删书或重导入) |
| `readlist_pubdate_source{source="mtime-fallback"}` | 长期不降 | 时效维度的上限卡在这儿(PRD §5 护栏) |
| `readlist_dim_measured{dim="…"}` | 某维接近 0 | 这一维没有判别力 —— 榜单不会因此报错 |
| `readlist_ingest_throttled` | > 0 | 被限流,通常意味着该配 / 该换 `GOOGLE_BOOKS_KEY` |
| `readlist_ingest_budget_exhausted` | 持续 = 1 | 首轮还没跑完,或预算给太小 |
| `readlist_grade_counts` / `readlist_runs_retained` | 趋势观察 | 数据质量与 PVC 占用 |

### 3.4 尚未做,且**不阻塞**上线

| 项 | 为什么可以后置 |
|----|--------------|
| LLM 标注(D/P 两维)+ gold set 门禁 | 没有标注时 D/P 记 `unknown`,依赖它们的榜自然为空 —— 诚实且安全。上线不必等 |
| `eval/gold.yaml` | 需要库主人亲自选 30+30 本书 |
| 约 1,000–1,300 本无 ISBN 也无 google id 的书 | 当前明确跳过(不做标题猜测)。要不要打 title+author 搜索是**开放问题 #1**,它决定全站可信书量 |
| OL work id 升级聚类键 | `ingest` 已把 `ol_work_id` 存下来,但聚类仍用「姓氏+规范标题」。等数据攒够再切 |

---

## 4. 上线顺序

```
CI 打 tag → ghcr 有镜像 ──┐
                          ├──► 应用清单(先不建 HTTPRoute)──► 手动引导 ──► 核对 ──► 建 HTTPRoute 公开
homelab 清单 + 备份归属 ──┘
```

### 4.1 ⚠️ 必须手动引导一次,否则站点会空一整天

三个 CronJob 分别在 01:05 / 01:20 / 01:40 触发。**清单一应用,`serve` 会在空库上
自愈打分一次并发布一个 0 本书的 run** —— 不崩、全部 200,但站点是空的,
要等到次日凌晨才有内容。所以第一次部署后立刻按顺序跑一遍:

```bash
K="kubectl --context oracle-k3s -n personal-services"

# 1) 快照:秒级。先看孤儿行数与 pubdate 污染计数是否符合预期
$K create job bootstrap-snapshot --from=cronjob/readlist-snapshot
$K wait --for=condition=complete job/bootstrap-snapshot --timeout=300s
$K logs job/bootstrap-snapshot

# 2) 打分:此时只有 T 与 readability 两维,先确认 publisher-picks 出得来
$K create job bootstrap-score --from=cronjob/readlist-score
$K wait --for=condition=complete job/bootstrap-score --timeout=300s
$K logs job/bootstrap-score

# 3) 核对无误后再摄入外部证据(约 800 次请求,10–15 分钟)
$K create job bootstrap-ingest --from=cronjob/readlist-ingest
$K wait --for=condition=complete job/bootstrap-ingest --timeout=1800s
$K logs job/bootstrap-ingest

# 4) 再打一次分,公开榜才会有内容
$K create job bootstrap-score2 --from=cronjob/readlist-score

# 5) ⚠️ 收尾:删掉引导 Job。已完成的 Job pod 仍算 PVC 使用者,
#    留着会让日后删 PVC 卡在 Terminating
$K delete job bootstrap-snapshot bootstrap-score bootstrap-ingest bootstrap-score2
```

**建议在第 2 步之后、建 HTTPRoute 之前先看一眼 `publisher-picks`(内部榜)**——
它零外部依赖,能直接反映出版社归一、work 聚类、孤儿数是否正常。
确认没问题再放 ingest,最后才把域名接上。

### 4.2 首轮 ingest 分几晚

`INGEST_BUDGET=800` 是**每次运行**的上限。全库约需 1,000–1,500 次请求,
所以首轮要 2 晚跑完(第二晚会自动跳过已缓存的,只补没查过的)。
中途看进度:`$K logs job/<name>` 里的 `缓存命中` 与 `本次预算已用完`。

## 5. 上线前必须守住的红线

1. ⚠️ **`pubdate` 污染**:不可信来源的书 F 记 `unknown`,**已在代码里强制**——
   `snapshot` 产出的三种来源(`calibre`/`mtime-fallback`/`unknown`)没有一种在可信名单里,
   只有 `ingest` 拿到的 `google`/`openlibrary` 才算数(R-1)。
2. ☠️ **绝不整库快照 `app.db`**(含密码 hash / OIDC 凭据)→ 只导出 3 张表的少数几列(NFR-13 / R-2)。
   有测试断言凭据不出现在产物里。
3. HN 匹配「宁少不多」:精确短语 + 标题必须含书名 + ≤2 词标题需白名单 + 保留 `objectID` 可否决(R-3)。
4. 只发元数据与评分,不发书;封面外链,不与私有书库跳转(NFR-12 / R-7)。
5. **能碰 calibre 卷的容器零网络出口,能出网的容器碰不到 calibre 卷** —— 参照清单里由
   NetworkPolicy + 卷挂载分离共同保证(architecture §2)。
