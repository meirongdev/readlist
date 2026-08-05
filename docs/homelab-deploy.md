# homelab k3s 上线剩余工作（归档）

> 日期: 2026-08-05
> 状态: 📌 归档清单 —— 当前 MVP 已可在 kind 端到端验证,但**尚未具备 homelab k3s 上线条件**。
> 目标集群: `oracle-k3s`(ARM free tier,单节点),命名空间 `personal-services`。
> 本文是「剩下要做的事」的唯一归档;每一项都标注了动作、落点(homelab 仓库 / 本仓库)与依据的规格 ID。

---

## 1. 当前边界

| 仓库 | 角色 | 现状 |
|------|------|------|
| **本仓库** (`meirongdev/readlist`) | 需求/评分标准唯一真相源 + 应用源码与镜像 CI | ✅ MVP 已实现:seed/score/dryrun/diff/serve、只读 API + SPA、SQLite 三层管道、kind E2E 通过 |
| **homelab 仓库** (`meirongdev/homelab`) | **部署清单唯一真相源**;ArgoCD 只指向它 | ❌ 尚未写入任何 readlist 清单 |

> 铁律(C-2):本仓库的 `deploy/kind/` 只是本地参照;集群实际部署的清单在 homelab 仓库。

---

## 2. 已就绪(无需再做)

| 项 | 依据 |
|----|------|
| 单 Go 二进制 + SQLite/WAL + 内嵌 SPA | architecture §1, D-7 |
| 镜像含 `linux/arm64`(kind 内按 arm64 构建验证),CGO=0,distroless nonroot | NFR-5 |
| 单副本 + `Recreate` 策略 | NFR-6 |
| 显式 resources request/limit(LimitRange 要求) | NFR-7 |
| 只读 API(非 GET 一律 405)+ 内嵌 SPA + Prometheus 指标端点 | FR-53, FR-60(端点已具备) |
| 原子发布(`published_run` 指针)、run 版本化产物 | system-design §1 |

---

## 3. 剩余工作清单

### 3.1 部署清单(拦路项 A —— 上线的前提)

| 动作 | 落点 | 依据 |
|------|------|------|
| 新建 `cloud/oracle/manifests/personal-services/readlist.yaml`(Namespace/PVC/Deployment/Service/CronJob 的 **oracle 版**) | homelab 仓库 | C-2 |
| 在 `cloud/oracle/manifests/personal-services/kustomization.yaml` 的 `resources:` **登记**(漏登记 = 静默不生效) | homelab 仓库 | C-2 |
| 新建 HTTPRoute:`readlist.meirong.dev`;域名**禁止用 `books.`** | homelab 仓库 | C-6, C-7 |
| ReferenceGrant:**不需要**(该集群 gateway 清单已有通配授权) | — | architecture §6 |
| 命名空间 `personal-services`;镜像 tag 用版本化(禁 `latest`) | homelab 仓库 | C-1, C-3 |
| 参照同集群 `trends` 的量级设定 request/limit | homelab 仓库 | NFR-7 |

### 3.2 镜像与 CI(拦路项 B —— 没有可拉的镜像)

| 动作 | 落点 | 依据 |
|------|------|------|
| 建 CI workflow:buildx 多架构(amd64+arm64)→ push `ghcr.io/meirongdev/readlist:<version>` | 本仓库 `.github/workflows` | C-3, NFR-5 |
| 确定 tag 策略(版本化 tag;Kyverno 禁 `latest`) | 本仓库 | C-3 |
| (可选)配 image-updater 写回 digest,替代可变 tag | 开放问题 Q4 | roadmap |

### 3.3 真实数据管道(拦路项 C —— 否则上线的是演示书单)

| 动作 | 落点 | 依据 |
|------|------|------|
| `readlist snapshot`:对 `metadata.db` 做 `VACUUM INTO` + 对 `app.db` **最小导出 3 张表**(绝不整库拷,user_id=1,join 丢孤儿) | 本仓库 cmd + 快照 CronJob | FR-10, FR-14, NFR-13 |
| `readlist ingest`:works 聚类 + Google Books/OpenLibrary/HN 的 facts 摄入(响应原样缓存 + TTL,配额感知,可续跑) | 本仓库 cmd | FR-11, FR-12, FR-15, NFR-9 |
| LLM 标注(内部网关)→ D/P/F 兜底;gold set 门禁 | 本仓库 | FR-16, system-design §6/§7 |
| `publisher_map` 名称归一 + HN 标题白名单/可否决 | 本仓库 + 人工 | FR-15, R-3 |
| 快照 CronJob 只挂 calibre 两个卷、**无网络出口**;Web 永不挂 calibre 卷 | homelab 仓库清单 | NFR-17 |

### 3.4 存储与备份(拦路项 D —— 否则 CI 规则 H4 会拦)

| 动作 | 落点 | 依据 |
|------|------|------|
| `readlist-data` PVC 采用 `local-path`,带 `Prune=false` 护栏 | homelab 仓库 | C-5 |
| 把 `readlist-data` 登记进 oracle 夜备脚本的 sqlite 列表(**新增 PVC 必须有备份归属**) | homelab 仓库 | C-4 / 规则 H4 |
| 不可再生数据(evidence / publisher_map / overrides)在夜备范围内 | homelab 仓库 | NFR-16 |

### 3.5 边缘限流与可观测(拦路项 E —— 公开前的安全/运营底线)

| 动作 | 落点 | 依据 |
|------|------|------|
| Cloudflare WAF 分档限流:页面一档,`/api/` 更严一档 | Cloudflare(非代码) | NFR-14 |
| 应用只读、无写接口、无用户输入落库(已满足) | 已满足 | NFR-14 |
| 指标接入监控(采集成功率 / 配额消耗 / 各证据等级书数 / 孤儿行数 / 最后成功采集时间) | homelab 监控 | FR-60, FR-61 |
| `runs.orphan_rows` 异常可见(book id 漂移) | 本仓库(P2) | FR-62 |

---

## 4. 验收口径(上线门槛)

| 门槛 | 内容 | 对应 |
|------|------|------|
| AC-P1 | 每本书有 `pubdate_source`;`mtime-fallback` 余量可报;出版社归一表覆盖 Top 20 | Phase 1 |
| AC-P2 | `timeless` 前 20 人工核对无误;`to-read-next` 可出;同版本重跑逐位一致 | Phase 2 |
| AC-P3 | 公开可访问;得分拆解可展开;阅读徽章/筛选可用;限流生效;C 级标"数据不足" | Phase 3 |
| AC-P4 | 指标进监控;标准定版;新 PVC 已入夜备 | Phase 4 |

## 5. 上线前必须守住的红线

1. ⚠️ **`pubdate` 污染未修就上时效维度** → "今年新书"榜整站可信度崩塌(R-1);不可信来源 F 记 `unknown`,压 C 级。
2. ☠️ **绝不整库快照 `app.db`**(含密码 hash / OIDC 凭据)→ 只导出 3 张表(NFR-13 / R-2)。
3. HN 标题误匹配 → 精确短语 + 作者/出版社共现 + ≤2 词白名单 + 保留 `objectID` 可否决;宁少不多(R-3)。
4. 只发元数据与评分,不发书;封面外链,不与私有书库跳转(NFR-12 / R-7)。

## 6. 建议顺序(依赖关系)

```
3.2 镜像/CI ──┐
3.3 数据管道 ──┼──► 3.1 homelab 清单 ──► 3.4 备份归属 ──► 3.5 限流/监控 ──► AC-P3/P4
3.4 备份归属 ──┘
```

> 3.3 是工作量最大的部分(外部配额 + LLM 标注),但它决定上线的是"真实书库"还是"演示书单";
> 3.1/3.2/3.4 是纯清单/CI 活,可以先行。
