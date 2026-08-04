# language: zh-CN
# 覆盖: FR-10~17 | NFR-9, NFR-13, NFR-16, NFR-17 | AC-P1
@P0
功能: 数据接入
  作为运维
  我想让采集基于一致性快照、不碰活库与凭据、不打爆外部配额
  以便榜单可信、可复算、不静默过期

  场景: 每夜一致性快照
    当 快照 CronJob 运行
    那么 对 metadata.db 执行 VACUUM INTO 生成一致性快照
    而且 快照产物落盘到 readlist 自己的 PVC

  场景: 快照是唯一接触 calibre 卷的容器
    当 集群运行
    那么 能挂载 calibre 两个源卷的只有该 CronJob
    而且 Web 应用永不挂载 calibre 卷

  场景: 阅读状态最小导出，绝不整库快照
    当 CronJob 导出阅读状态
    那么 只导出 read_status / shelf_membership / engagement 三张表
    而且 只取 user_id=1 的记录
    而且 join 时丢弃孤儿行
    而且 绝不包含 user / user_session / oauthProvider / remote_auth_token 表

  场景: 外部响应原样缓存
    当 fetch worker 从 Google Books / OpenLibrary / HN Algolia 抓取
    那么 响应原样存入 evidence 表并带 TTL
    而且 评分重算只读缓存，不重打外部 API

  场景: 配额耗尽即停，不空转
    假如 Google Books 当日免费额度耗尽
    当 fetch worker 运行
    那么 停止当日的该源外部请求
    而且 不产生空转 429 请求
    而且 次日自动恢复

  场景: 采集与评分留审计记录
    当 每次采集或评分运行结束
    那么 在 runs 表写入起止时间、成功/失败数、孤儿行数、配额消耗

  场景: 出版日期必须带来源
    当 富集写入某本书的出版日期
    那么 同时写入 pubdate_source
    而且 mtime 兜底值被标记为 mtime-fallback

  @P1
  场景: 出版社归一表可积累
    当 publisher_map 收录新的出版社变体
    那么 后续评级自动使用归一后的名称
    而且 该表属于不可再生数据，纳入夜备

  场景: 孤儿行数进入审计
    当 每次导出阅读状态
    那么 丢弃的孤儿行数写入 runs 表
    而且 该数值纳入监控（突增 = book id 漂移）
