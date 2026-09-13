-- 004_official.sql
--
-- 接入牛客官方结构化真题（ssrCommonData.experienceQuestionList）。
--
-- 背景：抓取样本里 4.2% 的内容页带这个列表，平均每帖 11.1 条，
-- 且 100% 附参考答案。这是质量最高的题源——结构化、无正则噪声、有答案。
-- 按全站 26,623 条推算约 1.2 万条带答案的题目，比从正文里抠题划算得多。

-- posts.official_q：该帖的结构化真题列表（JSON 文本，[{id,title,answer}]）
ALTER TABLE posts
  ADD COLUMN official_q MEDIUMTEXT NULL COMMENT '官方结构化真题列表 JSON';

-- questions.answer：参考答案。只有来自结构化真题的题目才有值。
-- 多条真题聚成同一题时取第一条非空答案（题目措辞不同，答案往往可通用）。
ALTER TABLE questions
  ADD COLUMN answer MEDIUMTEXT NULL COMMENT '参考答案（来自官方结构化真题）';

-- occurrences.official：这条出现记录是否来自官方结构化真题。
-- 保留来源信息，前端才能区分「本人回忆的题目」和「官方整理的真题+答案」，
-- 也便于后续按来源做质量加权。
ALTER TABLE occurrences
  ADD COLUMN official TINYINT(1) NOT NULL DEFAULT 0 COMMENT '1=来自官方结构化真题';

-- 常用组合：筛「有答案的题」
CREATE INDEX idx_questions_answer ON questions (answer(64));
