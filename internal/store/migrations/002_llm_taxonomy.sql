-- LLM 归类维度。
--
-- 为什么是三层：
--   merged（合并后的可用类，~20 个）  ← 页面展示与复习导航用这一层
--     └ domain（顶层域，6 个）        ← 分组标题
--         └ name（模型探索出的细分类，44 个） ← 保留细粒度，便于以后重新合并
--
-- 细分类先由模型「尽可能探索」出来，再人工/模型合并成可用集合；
-- 三层都留在库里，合并规则变了不必重新跑模型。

ALTER TABLE questions ADD COLUMN llm_cat    VARCHAR(64) NOT NULL DEFAULT '' COMMENT '模型判定的细分类';
ALTER TABLE questions ADD COLUMN llm_domain VARCHAR(64) NOT NULL DEFAULT '' COMMENT '细分类所属顶层域';
ALTER TABLE questions ADD COLUMN llm_merged VARCHAR(64) NOT NULL DEFAULT '' COMMENT '合并后的可用类';

CREATE INDEX idx_q_llm_cat    ON questions (llm_cat);
CREATE INDEX idx_q_llm_domain ON questions (llm_domain);
CREATE INDEX idx_q_llm_merged ON questions (llm_merged);

CREATE TABLE IF NOT EXISTS taxonomy (
    name        VARCHAR(64)  NOT NULL COMMENT '细分类名（模型产出）',
    domain      VARCHAR(64)  NOT NULL COMMENT '顶层域',
    merged      VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '合并后的可用类名',
    description VARCHAR(255) NOT NULL DEFAULT '',
    sort_order  INT          NOT NULL DEFAULT 0,
    PRIMARY KEY (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='LLM 分类体系（三层）';
