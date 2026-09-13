-- 006_llm_labels.sql
--
-- 让 LLM 归类结果**按题目文本**持久化，而不是只按 questions.id 保存。
--
-- 背景（真实的数据丢失事故）：
--   llm_cat/llm_domain/llm_merged 只存在 questions 表里，而 questions 每次
--   `make rebuild` 都会被 TRUNCATE 重建。原来的保护是「重建前把标注读进内存、
--   建完再按 norm 写回」。这个保护只覆盖了「成功路径」：
--
--   一次重建因为 `Error 1406 Data too long for column 'norm'` 在插入循环里中途失败，
--   TRUNCATE 已经执行、写回永远不会执行 —— **1477 条模型标注全部丢失**。
--   模型标注是这套流程里最贵的产物（要跑几十个子代理），
--   它不该依赖「重建一定成功」这个假设。
--
-- 修法：把标注单独存一张按 norm 主键的表。它是**关于题目文本的知识**，
-- 不是关于某一行数据的属性，所以本来就不该挂在会重建的表上。
-- 之后每次 ReplaceQuestions 结束都从这张表按 norm 重新回填，
-- 于是重建（无论成功还是中途失败后重试）都不再影响已有标注。
--
-- 附带好处：llm-apply 可以反复执行且幂等，重跑第二步/第三步都不需要再调模型。

CREATE TABLE IF NOT EXISTS llm_labels (
    norm       VARCHAR(768) NOT NULL COMMENT '题目归一化文本（与 questions.norm 一致）',
    cat        VARCHAR(128) NOT NULL COMMENT 'LLM 细分类名',
    updated_at BIGINT       NOT NULL,
    PRIMARY KEY (norm)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='LLM 归类结果，按题目文本持久化，跨重建保留';
