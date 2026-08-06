# language: zh-CN
# 覆盖: FR-30~35, FR-40, FR-42 | AC-P2, AC-P3
@P0
功能: 榜单
  作为访客或库主人
  我想按不同口径浏览技术书榜单
  以便找到"下一本读什么"或某一方向的值得读的书

  背景:
    假设 系统已完成一次评分，dim_scores 表包含 standard_version=1.0 的逐维得分与证据状态
    而且 综合分 TBS 是 (dim_scores, preset) 的纯函数，不落库；落库的是选材产物 lists
    而且 存在预设 timeless / ship-this-week / deep-dive / fresh-releases / ai-llm / to-read-next / read-and-loved

  场景: 按预设切换榜单
    当 访客打开榜单页并选择预设 "deep-dive"
    那么 榜单按 deep-dive 的权重、目标带与过滤器排序
    而且 每个榜单项展示综合分、证据等级与阅读状态徽章

  @P1
  场景: 权重滑块即时重排，不经过网络
    假如 榜单响应带有该预设的 weights、bands、order 与 min_coverage
    当 访客拖动权重滑块调整 A 维权重
    那么 榜单在浏览器内即时重排，并重新编号位次
    而且 不发起任何网络请求
    而且 重排结果等于「维度分 × 调整后权重」的纯客户端点积
    而且 默认权重下客户端算出的 TBS 与 coverage 与后端逐位一致

  场景: 不参与的维度既不占权重也不隐式惩罚
    假如 预设 timeless 的 weights 里不含 F
    当 计算该榜的综合分
    那么 F 的证据状态不影响该书的 coverage
    而且 一本 F 为 unknown 但 A、C、T 均为 measured 的书仍可进入该榜

  场景: 逐本 renormalize —— 缺维度按可用权重归一
    假如 某本书在 D 维的证据状态为 unknown，而预设给 D 的权重是 0.10
    当 计算该书的综合分
    那么 coverage 为 0.90
    而且 TBS 等于「可用维度加权和 ÷ 0.90」，而不是让 D 吃一个编出来的先验值

  场景: 深度按目标带打分而非单调加权
    假如 预设 ship-this-week 声明 bands: { D: { target: 35, tol: 25 } }
    而且 D 在该预设的 weights 里占一份权重
    当 计算一本深度分 90 的书
    那么 该书在 D 维的得分低于一本深度分 35 的书

  场景: 目标带维度必须占权重，否则配置被拒绝
    假如 某预设声明了 bands 却没给同一维度权重
    当 进程加载预设
    那么 加载失败并指出该 band 是空操作
    而且 进程不进入服务状态

  场景: 覆盖不足只挡住这一份榜，不让书从站上消失
    假如 某本书在该预设需要的维度上未达到 needs，或 coverage 低于 min_coverage
    当 访客浏览该公开预设榜单
    那么 该书不出现在这份榜里
    而且 若该书上了别的公开榜，它仍出现在"上榜书目"页，并标注"数据不足"以及**缺哪几维**
    而且 该书未达标的维度不会被显示成 0 分

  场景: needs 是逐维硬门，可作用于未加权的维度
    假如 预设 fresh-releases 声明 needs: { F: measured } 但没有给 F 权重
    当 榜单生成
    那么 F 为 unknown 的书不出现
    而且 F 的得分不参与该榜的加权

  场景: 近一年新书榜排除不可信出版日期
    假如 预设 fresh-releases 声明 pubdate_source 仅含 [google, openlibrary, file-meta]
    当 榜单生成
    那么 来源为 mtime-fallback 或 unknown 的书不出现
    而且 出版日期落在未来的书不出现（未来日期本身即污染信号，F 记 unknown）

  场景: 新书榜用滚动时间窗而非硬编码年份
    假如 预设 fresh-releases 声明 pubdate_within_months: 12
    当 系统跨年后重新生成榜单
    那么 榜内仍是「最近 12 个月」的书，而不是变成「去年的书」

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
    而且 按 id 直接请求该榜返回 404

  场景: 同一份语料重算两次，榜单逐位一致
    假如 语料与证据都没有变化
    当 连续执行两次评分
    那么 两个 run 的榜单成员、位次、TBS 与理由串完全相同
    而且 两个 run 的 corpus_id 与 facts_hash 相同

  @P1
  场景: 已读且个人高分进入推荐榜
    假如 某本书已读且库内个人评分 ≥ 4
    当 预设 read-and-loved 生成榜单
    那么 该书出现且标记"作者读过"
