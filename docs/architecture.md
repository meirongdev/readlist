# 架构与数据模型

> 日期: 2026-08-04
> 状态: 📐 设计。本文只剩仍然有效的四节:§1(单二进制)、§2(快照隔离)、
> §6(部署)、§7(可观测)。**§3–§5 已被 [system-design.md](system-design.md) 取代并删除**,
> 那边是三层管道(facts → judgement → selection)与逐维证据模型,也是实现照着做的那份。
> 环境事实依据: [data-baseline.md §4](data-baseline.md#4-环境事实)

---

## 1. 形状：单 Go 二进制

**单 Go 二进制** = cron 采集 worker + 评分 worker + 只读 API + 内嵌 SPA + SQLite/WAL。
镜像推 `ghcr.io/meirongdev/readlist`，`local-path` PVC，单副本 `Recreate`（SQLite 单写锁），
密钥走 External Secrets ← Vault。

同集群已有一个同形状的自研应用（`trends`：GitHub 趋势追踪，也是 cron 采集 + 评分 + 内嵌 SPA
+ SQLite），**这条路径的每个坑都已经踩过了** —— 复用它而不是发明新形状。

> 语言选 Go 不是技术偏好，是**复用已验证的部署形状**：静态二进制、arm64 交叉编译一条命令、
> 镜像小。用 Python 也能做，但要自己重走一遍 arm64 + 镜像 + 资源限额的验证。

## 2. ⚠️ 怎么读 Calibre 的两个库

要读**两个 PVC 上的两个 SQLite 库**，性质还不一样：

| 库 | PVC | journal_mode | 取法 |
|----|-----|--------------|------|
| `/calibre-library/metadata.db` | `calibre-books-local` | **wal** | `VACUUM INTO` 整库快照 |
| `/config/app.db`（阅读状态） | `calibre-web-automated-config-local` | delete | **只导出 3 张表**（库里有密码 hash） |

`metadata.db` 是 WAL 且 `-wal`/`-shm` 长期存在，这意味着：

- **只读挂载 + 直接查会失败**（WAL 读也要写 `-shm` → `attempt to write a readonly database`）；
- `?immutable=1` 只在库确实静止时才安全 —— calibre-web 一直在写，不安全。

### 方案：快照 + 隔离 blast radius

一个**独立的短命 CronJob**（不是长跑的 web 应用）挂两个源 PVC + `readlist-data`，只做两件事：

```bash
# 1) 书库：整库快照（4.4MB，秒级）
sqlite3 /library-src/metadata.db "VACUUM INTO '/data/snapshot/metadata.db'"

# 2) 阅读状态：最小导出，绝不整库拷 app.db —— SQL 见 reading-status.md §4
sqlite3 < /scripts/export-reading.sql
```

（pod 内实测 sqlite 3.45.1，`VACUUM INTO` 需 ≥3.27，可用。）

然后 **`readlist` 只挂自己的 `readlist-data` PVC，永不接触 calibre 的任何卷**。

| 性质 | 说明 |
|------|------|
| RWO 多挂载 | 单节点集群上没问题 —— RWO 是**节点级**语义，calibre-web 自己的清单已有同样论证 |
| 为什么源卷是 RW 挂载 | WAL 读的客观要求；但**能碰 calibre 卷的容器只有这个几十行、无网络访问的 CronJob** |
| 攻击面 | 公开 web 应用的任何 bug 都碰不到书库，也碰不到 `app.db` 里的凭据 |
| 附带收益 | `readlist` 与 calibre 的**可用性解耦** —— calibre 挂了榜单照常服务 |

## 3–5.（已删除）数据流 / 数据模型 / 外部数据源

这三节曾在这里给出一版设计,后来被 [system-design.md](system-design.md) 整体取代 ——
那边是三层管道(facts → judgement → selection)与逐维证据模型,也是实现真正照着做的
那一份。两份并存只会让人读到过期的那份(本文 §4 里的 `scores` 表就压根不存在),
所以这里不再保留副本:

- 数据流与三层管道 → [system-design.md §0 / §3–§5](system-design.md)
- 数据模型(权威) → [`internal/store/migrations/0001_schema.sql`](../internal/store/migrations/0001_schema.sql)
- 外部数据源与配额 → [system-design.md §6](system-design.md)、[scoring-standard.md](scoring-standard.md)

## 6. 部署与暴露

| 项 | 决定 |
|----|------|
| 集群 | `oracle-k3s`（ARM free tier，单节点），命名空间 `personal-services` |
| 清单位置 | **homelab 仓库** `cloud/oracle/manifests/personal-services/readlist.yaml`，并在该目录树的 `kustomization.yaml` 的 `resources:` **登记**（oracle 侧是显式 kustomize 树，漏登记 = 静默不生效） |
| 镜像 | `ghcr.io/meirongdev/readlist`，**必须含 `linux/arm64`**；集群 Kyverno 禁 `latest` tag |
| 存储 | `readlist-data` PVC，`local-path`，带 `Prune=false` 护栏 |
| 备份 | 加进 homelab 的 oracle 夜备脚本 sqlite 列表（**CI 规则 H4 会拦下没有备份归属的 PVC**） |
| 域名 | **`readlist.meirong.dev`** —— **不用 `books.`**，与私有书库 `book.meirong.dev` 只差一个字母，误配代价高 |
| DNS | **不需要改 DNS/隧道配置**：通配隧道 + external-dns 自动建记录，只写一个 HTTPRoute 即可 |
| ReferenceGrant | oracle 侧**不需要** —— 该集群的 gateway 清单已有覆盖本命名空间的通配授权 |
| SSO | **无**（公开站）。代价是必须在边缘加限流 |
| 限流 | Cloudflare WAF：页面一档，`/api/` 更严一档。应用侧只读、无写接口、无用户输入落库 → 攻击面基本只有爬虫和带宽 |
| 资源 | 命名空间有 LimitRange，须显式声明 request/limit；量级参照同集群 `trends` |
| 更新策略 | 单副本 `Recreate`（SQLite 单写锁；滚动更新会让新旧 pod 抢同一个 db 文件的写锁） |

## 7. 可观测

| 指标 | 用途 |
|------|------|
| 最后一次成功快照 / 采集 / 评分的时间戳 | 榜单静默过期的唯一警报来源 |
| 外部 API 请求数、429 数、当日剩余配额估算 | 配额管理（NFR-9） |
| 各证据等级的书数（A/B/C/D） | 数据质量趋势；补元数据的成效 |
| `runs.orphan_rows` | 孤儿行突增 = book id 漂移（有人删书或重导入） |
| LLM 标注低置信度数量 | 待人工复核队列长度 |
