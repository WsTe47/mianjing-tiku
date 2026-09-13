-- 005_widen_norm.sql
--
-- 放宽 questions.norm / canonical 的长度。
--
-- 背景：接入官方结构化真题（migration 004）后发现，官方题目标题是**完整句子**
-- （如「能否把模糊需求拆解为清晰可执行的任务」），比从正文抠出来的短行（多为
-- 「介绍一下 GMP」这种十几个字）长得多。
-- 原来 norm 是 VARCHAR(255)，插入时报 Error 1406 Data too long。
--
-- 255 对「归一化后的短句」够用，但归一化并不会缩短句子——它只是去标点、
-- 转小写、折叠空白。所以长度上限必须按原始题面来估。
--
-- norm 有 UNIQUE 索引，所以不能直接改成 TEXT（InnoDB 的 TEXT 需要前缀索引，
-- 而前缀索引无法保证唯一性语义）。VARCHAR(768) 在 utf8mb4 下是 3072 字节，
-- 正好是 InnoDB 单列索引的长度上限（DYNAMIC 行格式），既能保持 UNIQUE 又够宽。

ALTER TABLE questions
  MODIFY COLUMN canonical VARCHAR(1024) NOT NULL COMMENT '代表措辞';

-- 注意：norm 的写入长度由 importer 的 maxNormRunes 保证（截断到 700 字符），
-- 而不是靠把列改得更宽——UNIQUE 索引在 utf8mb4 下最多 768 字符，改宽会直接建不上索引。

ALTER TABLE questions
  MODIFY COLUMN norm VARCHAR(768) NOT NULL COMMENT '归一化文本，聚类键';
