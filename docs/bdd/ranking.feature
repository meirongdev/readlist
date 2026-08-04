# language: zh-CN
# 覆盖: FR-30~35, FR-40, FR-42 | AC-P2, AC-P3
@P0
功能: 榜单
  作为访客或库主人
  我想按不同口径浏览技术书榜单
  以便找到"下一本读什么"或某一方向的值得读的书

  背景:
    假设 系统已完成一次评分，scores 表包含 standard_version=1.0 的维度分与综合分
    而且 存在预设 timeless / ship-this-week / deep-dive / new-2026 / ai-llm / to-read-next / read-and-loved

  场景: 按预设切换榜单
    当 访客打开榜单页并选择预设 "deep-dive"
    那么 榜单按 deep-dive 的权重、目标带与过滤器排序
    而且 每个榜单项展示综合分、证据等级与阅读状态徽章

  @P1
  场景: 权重滑块即时重排，不经过网络
    当 访客拖动权重滑块调整 A 维权重
    那么 榜单在浏览器内即时重排
    而且 不发起任何网络请求
    而且 重排结果等于「维度分 × 调整后权重」的纯客户端点积

  场景: 豁免维度不隐式惩罚
    假如 预设 timeless 声明 exempt: [F]
    当 计算该榜的综合分
    那么 F 从分母剔除
    而且 剩余权重重新归一化后再加权

  场景: 深度按目标带打分而非单调加权
    假如 预设 ship-this-week 声明 bands: { D: { target: 35, tol: 25 } }
    当 计算一本深度分 90 的书
    那么 该书在 D 维的得分低于一本深度分 35 的书

  场景: C 级书不进公开榜单
    假如 某本书证据等级为 C
    当 访客浏览任何公开预设榜单
    那么 该书不出现
    而且 该书只在"全库目录"页出现并标注"数据不足"

  场景: 证据等级过滤
    假如 访客把过滤条件设为 min_evidence=A
    当 榜单生成
    那么 榜内只包含证据等级 A 的书

  @blocked
  场景: 今年新书榜排除不可信出版日期
    假如 预设 new-2026 声明 pubdate_source 仅含 [google, openlibrary, file-meta]
    当 榜单生成
    那么 来源为 mtime-fallback 或 unknown 的书不出现

  场景: 阅读队列榜只含未读
    假如 预设 to-read-next 声明 filters: { read_status: [unread] }
    当 榜单生成
    那么 榜内只包含未读的书
    而且 不包含在"弃读"书架中的书

  @P1
  场景: 内部榜单不出现在公开导航
    假如 预设 library-hygiene 的 visibility 为 internal
    当 访客浏览公开站点
    那么 该榜不出现在预设列表与任何公开入口

  @P1
  场景: 已读且个人高分进入推荐榜
    假如 某本书已读且库内个人评分 ≥ 4
    当 预设 read-and-loved 生成榜单
    那么 该书出现且标记"作者读过"
