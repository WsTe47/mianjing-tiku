-- nc-interview schema (MySQL 8.0)
--
-- 三层结构：posts（原始帖子）→ occurrences（出现记录）→ questions（去重后的问题）
-- occurrences 是连接两端的桥，同时携带该帖里的原始措辞，从而支持「点问题跳原文」。
--
-- 列宽依据实测：问题行最长 98 字符、归一化后最长 75、正文最长 913，均留足余量。
-- 字符集一律 utf8mb4：题干含中文与 emoji。

CREATE TABLE IF NOT EXISTS posts (
    id            BIGINT       NOT NULL COMMENT '牛客 contentId',
    uuid          VARCHAR(64)  NOT NULL COMMENT '牛客 moment uuid，拼原文链接用',
    title         VARCHAR(512) NOT NULL,
    url           VARCHAR(512) NOT NULL COMMENT '原文链接 /feed/main/detail/<uuid>',
    company       VARCHAR(128) NOT NULL DEFAULT '',
    job           VARCHAR(128) NOT NULL DEFAULT '' COMMENT '原始岗位串',
    job_group     VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '归组后的岗位（筛选用）',
    round         VARCHAR(32)  NOT NULL DEFAULT '' COMMENT '原始轮次',
    round_group   VARCHAR(32)  NOT NULL DEFAULT '' COMMENT '归组后的轮次（筛选用）',
    content       TEXT         NOT NULL COMMENT '正文原文',
    posted_at     BIGINT       NOT NULL COMMENT '毫秒时间戳，排序用',
    posted_date   DATE         NOT NULL COMMENT '展示用日期',
    interview_exp VARCHAR(128) NOT NULL DEFAULT '' COMMENT '牛客标注：查看N道真题和解析',
    scraped_at    BIGINT       NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_posts_uuid (uuid),
    KEY idx_posts_company     (company),
    KEY idx_posts_job_group   (job_group),
    KEY idx_posts_round_group (round_group),
    KEY idx_posts_posted_at   (posted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='牛客动态（面经原文）';

CREATE TABLE IF NOT EXISTS questions (
    id        BIGINT       NOT NULL AUTO_INCREMENT,
    canonical VARCHAR(512) NOT NULL COMMENT '代表措辞（该簇中最长的一条）',
    norm      VARCHAR(255) NOT NULL COMMENT '归一化文本，聚类键',
    category  VARCHAR(32)  NOT NULL COMMENT '模块',
    topic     VARCHAR(64)  NOT NULL COMMENT '主题',
    n         INT          NOT NULL DEFAULT 0 COMMENT '出现次数，重建时重算',
    PRIMARY KEY (id),
    UNIQUE KEY uk_questions_norm (norm),
    KEY idx_questions_n        (n),
    KEY idx_questions_category (category),
    KEY idx_questions_topic    (topic)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='去重后的面试问题';

CREATE TABLE IF NOT EXISTS question_variants (
    question_id BIGINT       NOT NULL,
    text        VARCHAR(512) NOT NULL COMMENT '同一问题的其他措辞',
    PRIMARY KEY (question_id, text),
    CONSTRAINT fk_qv_question FOREIGN KEY (question_id)
        REFERENCES questions (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='问题的措辞变体';

CREATE TABLE IF NOT EXISTS occurrences (
    id          BIGINT       NOT NULL AUTO_INCREMENT,
    question_id BIGINT       NOT NULL,
    post_id     BIGINT       NOT NULL,
    raw_text    VARCHAR(512) NOT NULL COMMENT '该帖中的原始措辞',
    PRIMARY KEY (id),
    UNIQUE KEY uk_occ_question_post (question_id, post_id),
    KEY idx_occ_question (question_id),
    KEY idx_occ_post     (post_id),
    CONSTRAINT fk_occ_question FOREIGN KEY (question_id)
        REFERENCES questions (id) ON DELETE CASCADE,
    CONSTRAINT fk_occ_post FOREIGN KEY (post_id)
        REFERENCES posts (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='问题 × 帖子的出现记录';

CREATE TABLE IF NOT EXISTS schema_migrations (
    name       VARCHAR(128) NOT NULL,
    applied_at BIGINT       NOT NULL,
    PRIMARY KEY (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='migration 追踪';
