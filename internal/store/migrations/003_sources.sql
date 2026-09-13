-- 全站抓取所需字段。
--
-- 从「只抓一个用户」扩展到「从 sitemap 抓各种面经」后，帖子的来源不再单一：
--   source='user'    → 通过 /api/sparta/user/content/my-content 抓的某用户动态
--   source='sitemap' → 从 sitemap 抓的全站公开内容
-- 同时补上作者与互动量，便于后续按作者/热度做筛选与排序。

ALTER TABLE posts ADD COLUMN author_id   BIGINT       NOT NULL DEFAULT 0  COMMENT '作者 userId';
ALTER TABLE posts ADD COLUMN author_name VARCHAR(128) NOT NULL DEFAULT '' COMMENT '作者昵称';
ALTER TABLE posts ADD COLUMN source      VARCHAR(32)  NOT NULL DEFAULT 'user' COMMENT '来源：user | sitemap';
ALTER TABLE posts ADD COLUMN entity_type INT          NOT NULL DEFAULT 0  COMMENT '牛客 entityType';
ALTER TABLE posts ADD COLUMN engage_like INT          NOT NULL DEFAULT 0  COMMENT '点赞数';
ALTER TABLE posts ADD COLUMN engage_cmt  INT          NOT NULL DEFAULT 0  COMMENT '评论数';
ALTER TABLE posts ADD COLUMN engage_view INT          NOT NULL DEFAULT 0  COMMENT '浏览数';
ALTER TABLE posts ADD COLUMN topic_tag   VARCHAR(128) NOT NULL DEFAULT '' COMMENT '正文首行话题标签（若有）';

CREATE INDEX idx_posts_author ON posts (author_id);
CREATE INDEX idx_posts_source ON posts (source);
CREATE INDEX idx_posts_like   ON posts (engage_like);
