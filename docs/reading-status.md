# 阅读状态设计

> 日期: 2026-08-04
> 状态: 📐 设计
> 实测依据: [data-baseline.md §3](data-baseline.md#3-应用库-appdb--阅读状态在这里)

榜单每一项要标出这本书**我读没读过**。状态确实已经在 SQLite 里，但**不在书库那个库**，
覆盖率也远低于直觉 —— 两件事都必须先说清。

---

## 1. 真相源：`app.db` 的 `book_read_link`

阅读状态在 **calibre-web 自己的库** `/config/app.db`，不是书库的 `metadata.db`：

```sql
CREATE TABLE book_read_link (
    id INTEGER NOT NULL, book_id INTEGER, user_id INTEGER,
    read_status INTEGER NOT NULL,          -- calibre-web 枚举: 0=未读 1=已读 2=在读
    last_modified DATETIME, last_time_started_reading DATETIME,
    times_started_reading INTEGER NOT NULL, PRIMARY KEY (id),
    FOREIGN KEY(user_id) REFERENCES user (id));
```

| 事实 | 值 |
|------|-----|
| 所在 PVC | `calibre-web-automated-config-local`（**与书库不是同一个卷**） |
| `journal_mode` | `delete`（不是书库那个 WAL） |
| id 空间 | `book_read_link.book_id` == `metadata.db.books.id`，可直接 join |
| 备份 | **已就位** —— `calibre-web-automated-config` 已在 homelab 夜备名单里，无需新增备份责任 |

**书库侧没有阅读状态**：实测 `custom_columns` 为 **0 个**，没有 Calibre 桌面版常用的 `#read`
自定义列。别从 `metadata.db` 找。

---

## 2. ⚠️ 覆盖率：2,054 本里只有 23 本标了已读

| 信号 | 实测值 | 说明 |
|------|--------|------|
| `book_read_link` 总行数 | **26** | 全部属于 `user_id=1`；`Guest` 零行 |
| `read_status=1`（已读）且书仍存在 | **23** | = 全库 **1.1%** |
| `read_status=0`（显式未读） | 1 | 手滑标的，无意义 |
| `read_status=2`（在读） | **0** | 从未用过 |
| **孤儿行** | **2 / 26** | ⚠️ book id 漂移是实测事实 |
| `downloads` | 40 本 | 弱信号"打算读"；表无时间戳 |
| `kobo_reading_state` | 24 本 | 弱信号"设备上打开过" |
| **三者并集** | **54 本** | "可能读过"的候选池上限 |
| 已读 ∩ 有库内评分 | **3 本** | 见 §5 |
| `kobo_bookmark.progress_percent` | 字段有，28 行**全 NULL** | **拿不到百分比进度** |
| `kosync_progress` | **0 行** | KOReader 未接 |
| `shelf` | **0 个** | 扩展状态载体是空的 |

已读的 23 本时间戳集中在 2026-06 ~ 2026-08，说明**标记习惯是最近才开始的**，往后会自然增长。

### 结论

管道（读状态 → 徽章 → 筛选 → 阅读队列榜）代码量很小，**一天能做完**；
**真正的工作量是补录历史阅读记录，而这只有库主人自己能做**（§6）。
不补的话，公开榜上就是 2,000 多个空徽章 + 23 个 ✓。

---

## 3. 状态模型：核心三态 + 书架承载扩展状态

`read_status` 只有三态，而书单实际需要更多。**不改 calibre-web schema**，
用它现成的 `shelf` 表承载多出来的状态：

| 状态 | 载体 | 徽章 |
|------|------|------|
| 已读 | `book_read_link.read_status = 1` | `✓ 已读` |
| 在读 | `book_read_link.read_status = 2` | `◐ 在读` |
| 想读 | 书架 `想读` | `☆ 想读` |
| 弃读 | 书架 `弃读` | `✗ 弃读` |
| 精读/推荐 | 书架 `精读` | `★ 精读` |
| 未读 | 无记录（默认） | **不显示徽章** —— 2,000 个"未读"是噪声不是信息 |

`shelf` 表实测有 `name` / `user_id` / `is_public` / `kobo_sync` 列，**零 schema 改动**，
在 calibre-web UI 里点几下就能建和维护，readlist 只读 join。

**要做的动作**：在 calibre-web 里建 `想读` / `弃读` / `精读` 三个书架。

> **为什么不用 `archived_book` 表表示"弃读"**：calibre-web 的 Archive 语义是
> **从视图里隐藏**，不等于读了一半放弃。硬套语义会在半年后把自己坑了。
> （实测 `archived_book` 为空，也没有历史包袱要迁。）

---

## 4. ☠️ 只导出需要的表 —— `app.db` 含密码 hash 和 OIDC 凭据

`app.db` 里有 `user`（2 行，含**密码 hash**）、`user_session`（2 行）、
`oauthProvider`（**3 行**，OIDC provider 配置）、`remote_auth_token`。

**绝不能把整个 `app.db` 快照进公开应用的卷。** 快照 CronJob 对 `app.db` 走**最小导出**
（不是 `VACUUM INTO` 整库）：

```sql
-- 快照 CronJob 内执行；两个源库只读附加，输出只含公开所需的三张表
ATTACH 'file:/config-src/app.db?mode=ro'       AS app;
ATTACH 'file:/library-src/metadata.db?mode=ro' AS lib;
ATTACH '/data/snapshot/reading.db'             AS out;

CREATE TABLE out.read_status AS
  SELECT r.book_id, r.read_status, r.last_modified, r.times_started_reading
  FROM app.book_read_link r
  JOIN lib.books b ON b.id = r.book_id     -- 丢弃孤儿行（实测 26 行里 2 行）
  WHERE r.user_id = 1;                     -- 只取库主人；Guest 与未来账号不参与

CREATE TABLE out.shelf_membership AS
  SELECT s.name AS shelf, bs.book_id
  FROM app.shelf s
  JOIN app.book_shelf_link bs ON bs.shelf = s.id
  JOIN lib.books b ON b.id = bs.book_id
  WHERE s.user_id = 1;

CREATE TABLE out.engagement AS
  SELECT book_id, COUNT(*) AS downloads
  FROM app.downloads WHERE user_id = 1 GROUP BY 1;
```

**孤儿行数写进 `runs` 表**：孤儿数突然上升 = 有人在删书或重导入，
这是唯一能察觉 book id 漂移的地方。

### readlist 只读，绝不写阅读状态

真相源永远是 calibre-web —— **阅读发生在 calibre-web / Kobo，状态就该在那里产生**。
加第二个写入点必然分歧，而且会把不可再生的阅读记录塞进 readlist 那个
"纯派生、可丢弃"的卷里，破坏 [architecture.md](architecture.md) 的备份论证前提。

---

## 5. 阅读状态是 facet，不进 TBS 分 —— 但它解锁两个最有用的榜

**不加"已读"维度到 TBS**：读过 ≠ 好书。真正携带信号的是"已读 **+** 个人高分"或"弃读"，
而不是"已读"本身。

它衍生的两个 preset 才是重点（完整定义见 [scoring-standard.md §6](scoring-standard.md#6-榜单预设权重档案)）：

| preset | 是什么 | 状态 |
|--------|--------|------|
| `to-read-next`「下一本读什么」 | **高分 ∩ 未读**。把客观排行变成阅读队列 —— 这是整个项目对库主人最大的价值 | 立刻可做，几乎零成本 |
| `read-and-loved`「我读过且推荐」 | **已读 ∩ 个人评分高**。真人读过的推荐 > 算法分，是公开书单最有说服力的部分 | ⚠️ **现在开不起来** |

### `read-and-loved` 的问题

实测**已读 ∩ 有库内评分只有 3 本**。两条路：

1. **给那 23 本已读的书补个人星级**（calibre-web UI 里点星，几分钟的事）—— **建议做这个**；
2. 先退一步，把"已读"本身当背书（弱一点，但立刻有 23 本）。

无论选哪条，`min_personal_rating` 都保留在标准里。

### 前端表达

对已读的书，在榜单行上加一句**"作者读过"** —— 这是与纯算法榜最容易做出的差异。

---

## 6. 补录历史阅读记录（真正的工作量）

候选池 54 本（已读 23 ∪ downloads 40 ∪ kobo 24）。

### 走 UI，不要直接写 `app.db`

`app.db` 是 `journal_mode=delete` 且被运行中的 calibre-web **独占写**，直接 INSERT
要么撞锁，要么被应用层缓存覆盖。批量写就得先把副本数降到 0，而 calibre-web 是
`Recreate` 策略 + 有个已知的 init 容器坑（空卷上必然失败）。**不值得为 54 本冒这个险。**

### 候选清单 SQL

```bash
POD=$(kubectl --context oracle-k3s -n personal-services get pod -l app=calibre-web \
      -o jsonpath='{.items[0].metadata.name}')
kubectl --context oracle-k3s -n personal-services exec "$POD" -c calibre-web -- sqlite3 <<'SQL'
ATTACH "file:/config/app.db?mode=ro" AS app;
ATTACH "file:/calibre-library/metadata.db?mode=ro" AS lib;
.mode column
.width 6 60 8 6
SELECT b.id, substr(b.title,1,58) AS title,
       CASE WHEN r.read_status=1 THEN 'read' ELSE '' END AS mark,
       (SELECT COUNT(*) FROM app.downloads d WHERE d.book_id=b.id) AS dl
FROM lib.books b
LEFT JOIN app.book_read_link r ON r.book_id=b.id AND r.user_id=1
WHERE b.id IN (SELECT book_id FROM app.book_read_link WHERE read_status=1
               UNION SELECT book_id FROM app.downloads
               UNION SELECT book_id FROM app.kobo_reading_state)
ORDER BY mark DESC, dl DESC;
SQL
```

### 其他可挖的信号（按性价比排）

1. **笔记系统里的书籍笔记** —— 做过笔记 = 真读进去了，是比"下载过"强得多的信号。
   值得先看一眼有多少条能对上书名（homelab 侧跑着 open-notebook）。
2. **接 KOReader + kosync**：`kosync_progress` 表已存在（0 行）。这是**唯一**能让
   "在读 / 进度"自动产生的路径 —— Kobo sync 虽然在用，但
   `kobo_bookmark.progress_percent` 实测全 NULL，Kobo 只能给"开过这本书"。
   **若想让 `◐ 在读` 徽章真正活起来，这是前提。**
3. 外部阅读日志（Goodreads 导出 CSV / Notion）如果有，一次性 ISBN 匹配导入。

---

## 7. 公开性

已读徽章公开 = 公开自己的阅读轨迹。技术书书名敏感度低，且**"我读过哪些技术书"正是公开书单的
价值来源**（否则它就只是个算法榜）。所以默认公开，但：

| 措施 |
|------|
| 导出只取 `user_id = 1`，`Guest` 与未来任何账号不参与（§4 的 SQL 已写死） |
| 配置开关 `expose_read_status: true\|false`，一行改回私有 |
| `弃读` 书架默认**不公开** —— 说某本书弃读带评价意味；书架的 `is_public` 列可另作控制 |
