-- 让「人工投入」的三张表真正有消费者(review B5 / B6)。
--
-- 这三张表此前都被文档列为「不可再生 → 必须夜备」(NFR-16),但代码里:
--   publisher_map    每夜被内置规则表整表覆盖,且**没有任何读取方**;
--   overrides        只出现在 facts_hash 里,没有任何消费者;
--   mention_overrides 压根不存在 —— 而 R-3 把「保留 objectID 供人工逐条否决」
--                    当作 HN 误匹配的唯一兜底。
-- 备份一份没人读的数据,等于把承诺写在了备份脚本里。

-- publisher_map 区分规则行与人工行:
--   source='rules'  —— corpus.Publisher 的内置表推出的,每夜可被覆盖;
--   source='manual' —— 人工归一,导入时**优先采用**,且永不被规则覆盖。
-- 人工行只需给出规范名(norm):tier 仍由规则表从规范名推出,所以覆盖的正确用法是
-- 把认不出来的变体映射到一个规则表已知的规范名(如 "O'Reilly & Assoc." → "O'Reilly Media")。
ALTER TABLE publisher_map ADD COLUMN source TEXT NOT NULL DEFAULT 'rules';

-- HN 提及的人工否决。objectID 已经存在 mentions 里,就是为了能逐条点开验证并否决 ——
-- 通用短标题("Clean Code"、"Refactoring")命中无关讨论是 R-3 里概率标「高」的风险,
-- 而在此之前唯一的处置办法是去改代码。
CREATE TABLE mention_overrides (
  work_id   TEXT NOT NULL,
  object_id TEXT NOT NULL,
  verdict   TEXT NOT NULL,   -- reject(不计入声量维) | accept(人工确认,仅留档)
  reason    TEXT,
  at        TEXT,
  PRIMARY KEY (work_id, object_id)
);
