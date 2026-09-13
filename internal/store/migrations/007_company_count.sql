-- co_n：这道题被多少家**不同公司**问过。
--
-- 为什么频次之外还要这个：一道题被同一家公司问 30 次，不如被 20 家公司各问 1 次
-- 更能说明它「普遍重要」。而 n 会把「一家公司刷很多帖」算成高频。
-- 两者互补，复习优先级按 co_n 排更贴近实际需要。
ALTER TABLE questions ADD COLUMN co_n INT NOT NULL DEFAULT 0;
ALTER TABLE questions ADD INDEX idx_questions_co_n (co_n);
