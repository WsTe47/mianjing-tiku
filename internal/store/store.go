// Package store 封装 MySQL 持久化。
//
// 所有 SQL 只出现在本包内，上层（api / importer / scraper）不感知数据库。
// 想换回 SQLite 或迁到 PostgreSQL，只需重写本包与 migrations 目录。
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/climber47/nc-interview/internal/model"
	_ "github.com/go-sql-driver/mysql"
)

//go:embed all:migrations
var migrationsFS embed.FS

// Store 是数据库句柄。
type Store struct{ db *sql.DB }

// Open 连接 MySQL、建立连接池并应用 migration。
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(time.Hour)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("连接 MySQL 失败（检查 DSN / 服务是否启动）: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭连接池。
func (s *Store) Close() error { return s.db.Close() }

// DB 暴露底层句柄。
func (s *Store) DB() *sql.DB { return s.db }

// splitStatements 按 ; 切分 SQL 文件，并剔除 -- 注释行。
//
// 刻意不启用 DSN 的 multiStatements：那会放大注入面，而 migration 是受信任的嵌入文件，
// 在 Go 侧切分更可控。
func splitStatements(script string) []string {
	var out []string
	var sb strings.Builder
	for _, line := range strings.Split(script, "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "--") || t == "" {
			continue
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	for _, stmt := range strings.Split(sb.String(), ";") {
		if s := strings.TrimSpace(stmt); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// migrate 按文件名顺序应用尚未执行过的 migration。
//
// 必须有追踪表：ALTER TABLE 之类的语句不幂等，重复执行会失败。
func (s *Store) migrate(ctx context.Context) error {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("读取 migrations 失败: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	// 追踪表本身不能依赖 migration（否则先有鸡还是先有蛋）
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name       VARCHAR(128) NOT NULL,
		applied_at BIGINT       NOT NULL,
		PRIMARY KEY (name)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`); err != nil {
		return fmt.Errorf("创建 schema_migrations 失败: %w", err)
	}

	for _, name := range names {
		var n int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		for _, stmt := range splitStatements(string(body)) {
			if _, err := s.db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("应用 %s 失败: %w\n语句: %s", name, err, truncate(stmt, 160))
			}
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`,
			name, time.Now().UnixMilli()); err != nil {
			return err
		}
		log.Printf("已应用 migration %s", name)
	}
	return nil
}

func truncate(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

// ---------- 写入 ----------

// UpsertPost 写入或更新一篇帖子。
func (s *Store) UpsertPost(ctx context.Context, p model.Post) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO posts
			(id,uuid,title,url,company,job,job_group,round,round_group,content,posted_at,posted_date,interview_exp,scraped_at,
			 author_id,author_name,source,entity_type,engage_like,engage_cmt,engage_view,topic_tag,official_q)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) AS new
		ON DUPLICATE KEY UPDATE
			uuid          = new.uuid,
			title         = new.title,
			url           = new.url,
			company       = new.company,
			job           = new.job,
			job_group     = new.job_group,
			round         = new.round,
			round_group   = new.round_group,
			content       = new.content,
			posted_at     = new.posted_at,
			posted_date   = new.posted_date,
			interview_exp = new.interview_exp,
			scraped_at    = new.scraped_at,
			author_id     = new.author_id,
			-- 空值不覆盖已有值：JSONL 导入等路径拿不到作者名，
			-- 不该把之前抓到的作者名冲成空串。
			-- ⚠️ 必须写成 posts.author_name：ON DUPLICATE KEY UPDATE 里带行别名（AS new）时，
			-- 裸列名会被 MySQL 判为 ambiguous 并报 Error 1052，导致**所有插入静默失败**。
			author_name   = IF(new.author_name = '', posts.author_name, new.author_name),
			source        = new.source,
			entity_type   = new.entity_type,
			engage_like   = new.engage_like,
			engage_cmt    = new.engage_cmt,
			engage_view   = new.engage_view,
			topic_tag     = new.topic_tag,
			-- 结构化真题只在抓取详情页时才有；用户时间线那条路径拿不到，
			-- 所以空值不覆盖已有值，避免一次 reimport 把真题冲掉。
			official_q    = IF(new.official_q = '', posts.official_q, new.official_q)`,
		p.ID, p.UUID, p.Title, p.URL, p.Company, p.Job, p.JobGroup, p.Round, p.RoundGroup,
		p.Content, p.PostedAt, p.PostedDate, p.InterviewExp, p.ScrapedAt,
		p.AuthorID, p.AuthorName, p.Source, p.EntityType,
		p.LikeCnt, p.CommentCnt, p.ViewCnt, p.TopicTag, p.OfficialQ)
	return err
}

// ReplaceQuestions 清空并重写全部问题与出现记录。
//
// 聚类是全局操作，整体替换最稳。TRUNCATE 属于 DDL、无法参与事务，且被外键引用的表
// 必须临时关闭外键检查——因此整个清空序列跑在**同一条连接**上（SET FOREIGN_KEY_CHECKS
// 是会话级的，用连接池会飘到别的会话）。插入仍在事务内，保证要么全成要么全不成。
//
// 重建前按 norm 暂存已有的 LLM 归类、插入后还原：模型标注成本高，
// 不该因为改了一条规则分类就被冲掉。
func (s *Store) ReplaceQuestions(ctx context.Context, qs []model.Question) error {
	saved := s.snapshotLLM(ctx)

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		return err
	}
	for _, t := range []string{"occurrences", "question_variants", "questions"} {
		if _, err := conn.ExecContext(ctx, "TRUNCATE TABLE "+t); err != nil {
			_, _ = conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 1")
			return fmt.Errorf("清空 %s 失败: %w", t, err)
		}
	}
	if _, err := conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 1"); err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	qStmt, err := tx.PrepareContext(ctx,
		`INSERT INTO questions (canonical,norm,category,topic,answer,n) VALUES (?,?,?,?,?,0)`)
	if err != nil {
		return err
	}
	defer qStmt.Close()
	vStmt, err := tx.PrepareContext(ctx,
		`INSERT IGNORE INTO question_variants (question_id,text) VALUES (?,?)`)
	if err != nil {
		return err
	}
	defer vStmt.Close()
	oStmt, err := tx.PrepareContext(ctx,
		`INSERT IGNORE INTO occurrences (question_id,post_id,raw_text,official) VALUES (?,?,?,?)`)
	if err != nil {
		return err
	}
	defer oStmt.Close()

	for _, q := range qs {
		// 截断放在写入边界：列宽是 schema 的约束，不该指望每个调用方都记得切
		res, err := qStmt.ExecContext(ctx,
			model.ClipRunes(q.Canonical, model.MaxCanonRunes),
			model.ClipRunes(q.Norm, model.MaxNormRunes),
			model.ClipRunes(q.Category, 32),
			model.ClipRunes(q.Topic, 64),
			nullIfEmpty(q.Answer))
		if err != nil {
			return fmt.Errorf("插入问题 %q 失败: %w", truncate(q.Canonical, 60), err)
		}
		qid, _ := res.LastInsertId()
		for _, v := range q.Variants {
			if v == q.Canonical {
				continue
			}
			if _, err := vStmt.ExecContext(ctx, qid, model.ClipRunes(v, model.MaxCanonRunes)); err != nil {
				return err
			}
		}
		for _, o := range q.Occurs {
			if _, err := oStmt.ExecContext(ctx, qid, o.PostID,
				model.ClipRunes(o.RawText, model.MaxCanonRunes), o.Official); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := s.RecomputeCounts(ctx); err != nil {
		return err
	}
	// 优先用持久化的标注表回填（跨进程、跨失败都可靠）；
	// 内存快照 `saved` 只在标注表还没建立时兜底。
	if _, err := s.ApplyLabelsFromTable(ctx); err != nil {
		return fmt.Errorf("回填 LLM 标注失败: %w", err)
	}
	return s.restoreLLM(ctx, saved)
}

// snapshotLLM 在重建前暂存已有 LLM 归类，键为归一化文本（重建后 norm 不变）。
func (s *Store) snapshotLLM(ctx context.Context) map[string][3]string {
	saved := map[string][3]string{}
	rows, err := s.db.QueryContext(ctx,
		`SELECT norm, llm_cat, llm_domain, llm_merged FROM questions WHERE llm_cat <> ''`)
	if err != nil {
		return saved
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		var v [3]string
		if err := rows.Scan(&n, &v[0], &v[1], &v[2]); err == nil {
			saved[n] = v
		}
	}
	return saved
}

// restoreLLM 按 norm 还原 LLM 归类。
func (s *Store) restoreLLM(ctx context.Context, saved map[string][3]string) error {
	if len(saved) == 0 {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, norm FROM questions`)
	if err != nil {
		return err
	}
	type upd struct {
		id int64
		v  [3]string
	}
	var ups []upd
	for rows.Next() {
		var id int64
		var n string
		if err := rows.Scan(&id, &n); err != nil {
			rows.Close()
			return err
		}
		if v, ok := saved[n]; ok {
			ups = append(ups, upd{id, v})
		}
	}
	rows.Close()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx,
		`UPDATE questions SET llm_cat=?, llm_domain=?, llm_merged=? WHERE id=?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, u := range ups {
		if _, err := stmt.ExecContext(ctx, u.v[0], u.v[1], u.v[2], u.id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Printf("已按 norm 还原 %d 条 LLM 归类", len(ups))
	return nil
}

// RecomputeCounts 依据 occurrences 重算 questions.n。
func (s *Store) RecomputeCounts(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE questions q
		SET q.n = (SELECT COUNT(*) FROM occurrences o WHERE o.question_id = q.id)`); err != nil {
		return err
	}
	// co_n = 被多少家**不同公司**问过。与 n 一起在这里重算，
	// 保证重建 / 去重 / 合并之后两个计数始终自洽。
	_, err := s.db.ExecContext(ctx, `
		UPDATE questions q
		LEFT JOIN (
			SELECT o.question_id, COUNT(DISTINCT p.company) c
			FROM occurrences o
			JOIN posts p ON p.id = o.post_id
			WHERE p.company <> ''
			GROUP BY o.question_id
		) t ON t.question_id = q.id
		SET q.co_n = COALESCE(t.c, 0)`)
	return err
}

// ---------- 查询 ----------

// dateCond 返回按**发布时间**过滤的条件片段（作用于 posts 别名 p）。
//
// 单独抽出来是因为它要在四个地方复用：问题列表、各维度 facet 计数、矩阵、原文列表。
// 时间筛选是全局的——用户选了区间之后，下面看到的每一个数字都该被它限定，
// 否则「筛选了 8 月，但公司榜还是全量」这种不一致比没有筛选更误导。
func dateCond(f model.Filter) (string, []any) {
	var conds []string
	var args []any
	if f.From != "" {
		conds = append(conds, "p.posted_date >= ?")
		args = append(args, f.From)
	}
	if f.To != "" {
		conds = append(conds, "p.posted_date <= ?")
		args = append(args, f.To)
	}
	return strings.Join(conds, " AND "), args
}

// withDate 把时间条件拼到已有条件后面（空则原样返回）。
func withDate(conds []string, args []any, f model.Filter) ([]string, []any) {
	if dc, da := dateCond(f); dc != "" {
		conds = append(conds, dc)
		args = append(args, da...)
	}
	return conds, args
}

func (s *Store) where(f model.Filter) (string, []any) {
	var conds []string
	var args []any
	if f.Round != "" {
		conds = append(conds, "p.round_group = ?")
		args = append(args, f.Round)
	}
	if f.Job != "" {
		conds = append(conds, "p.job_group = ?")
		args = append(args, f.Job)
	}
	if f.Company != "" {
		conds = append(conds, "p.company = ?")
		args = append(args, f.Company)
	}
	if f.Category != "" {
		conds = append(conds, "q.category = ?")
		args = append(args, f.Category)
	}
	if f.Topic != "" {
		conds = append(conds, "q.topic = ?")
		args = append(args, f.Topic)
	}
	if f.LLMMerged != "" {
		conds = append(conds, "q.llm_merged = ?")
		args = append(args, f.LLMMerged)
	}
	if f.LLMDomain != "" {
		conds = append(conds, "q.llm_domain = ?")
		args = append(args, f.LLMDomain)
	}
	if f.LLMCat != "" {
		conds = append(conds, "q.llm_cat = ?")
		args = append(args, f.LLMCat)
	}
	if !f.IncludeNoise {
		conds = append(conds, "q.llm_cat <> '非题目'")
	}
	if f.OnlyLabeled {
		conds = append(conds, "q.llm_cat <> ''")
	}
	if f.HasAnswer {
		// 用 IS NOT NULL 而不是 <> ''：NULL 和空串语义不同，见 nullIfEmpty。
		conds = append(conds, "q.answer IS NOT NULL")
	}
	if f.OfficialOnly {
		conds = append(conds, "EXISTS (SELECT 1 FROM occurrences o2 WHERE o2.question_id = q.id AND o2.official = 1)")
	}
	if dc, da := dateCond(f); dc != "" {
		conds = append(conds, dc)
		args = append(args, da...)
	}
	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		like := "%" + kw + "%"
		conds = append(conds, `(q.canonical LIKE ? OR EXISTS (
			SELECT 1 FROM question_variants v WHERE v.question_id = q.id AND v.text LIKE ?))`)
		args = append(args, like, like)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " AND " + strings.Join(conds, " AND "), args
}

// 频次在筛选范围内重算：JOIN 之后再 GROUP BY，而不是读 questions.n（那是全库计数）。
// MySQL 8 的 ONLY_FULL_GROUP_BY 允许选择函数依赖于主键的列（q.id 是 questions 主键）。
// 频次在筛选范围内重算：JOIN 之后再 COUNT(*)，而不是读 questions.n（那是全库计数）。
// llm_* 列可以随 COUNT(*) 一起选：q.id 是主键，其余列函数依赖于它，符合 ONLY_FULL_GROUP_BY。
// answerPreviewRunes 是列表接口里答案的截断长度。
// 400 字足够判断「这条答案是不是我要的」，完整内容走详情接口。
const answerPreviewRunes = 400

// occurPreviewLimit 是列表接口里每个问题最多带多少条出现记录。
//
// 高频题的出现记录很长：一页 50 条、平均几十条出现记录时，
// 光 occurrences 就占几百 KB（实测 432KB / 页）。而用户看一屏最多扫几条，
// 完整列表按 id 取（/api/questions/{id}）即可。
// n 字段仍然是真实频次，前端据此显示「另有 N 条」。
// 6 是权衡后的值：出现记录占列表单条响应的近九成（实测 5.6KB/6.3KB），
// 而它们只在展开时才需要看。前 6 条足够判断「这题被哪些公司问过」，
// 展开时前端再按 id 取完整列表（详情接口本来就返回全部）。
const occurPreviewLimit = 6

// variantPreviewLimit 是列表接口里每道题最多带多少条「其他措辞」。
// 同义题合并之后，榜首那几条题动辄有几十种写法（实测「自我介绍」有 72 种），
// 全带上单条就多出近 1KB。完整列表在详情接口里。
const variantPreviewLimit = 12

// clipRunes 按字符截断（不切多字节字符）。
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

const qSelect = `
	SELECT q.id, q.canonical, q.category, q.topic, COUNT(*) AS n,
	       q.llm_merged, q.llm_domain, q.llm_cat,
	       -- 必须 COALESCE：answer 对绝大多数题是 NULL，直接 Scan 进 string 会报
	       -- "converting NULL to string is unsupported"，让**整个问题列表**挂掉。
	       COALESCE(q.answer,''),
	       CHAR_LENGTH(COALESCE(q.answer,'')) AS answer_len,
	       CAST(COALESCE(SUM(o.official),0) AS SIGNED) AS official_n,
	       q.co_n
	FROM questions q
	JOIN occurrences o ON o.question_id = q.id
	JOIN posts       p ON p.id = o.post_id
	WHERE 1=1 %s
	GROUP BY q.id
	ORDER BY %s
	LIMIT ? OFFSET ?`

const qCount = `
	SELECT COUNT(*) FROM (
		SELECT q.id FROM questions q
		JOIN occurrences o ON o.question_id = q.id
		JOIN posts       p ON p.id = o.post_id
		WHERE 1=1 %s
		GROUP BY q.id) t`

// orderBy 把排序选项翻成 SQL。
//
// priority 的权重是刻意写得**可解释**的，而不是调出来的黑箱：
// 公司覆盖面最重要（×3），其次频次（上限 20，防止一家公司刷帖把分值顶穿），
// 有参考答案再加 4（带答案的题复习成本低得多）。
func orderBy(f model.Filter) string {
	switch f.Sort {
	case "co":
		return "q.co_n DESC, n DESC, q.id ASC"
	case "priority":
		return "(q.co_n * 3 + LEAST(n, 20) + IF(COALESCE(q.answer,'') <> '', 4, 0)) DESC, q.id ASC"
	default:
		return "n DESC, CHAR_LENGTH(q.canonical) ASC, q.id ASC"
	}
}

// QueryQuestions 按维度筛选问题，返回列表与总数。
func (s *Store) QueryQuestions(ctx context.Context, f model.Filter) ([]model.Question, int, error) {
	clause, args := s.where(f)

	var total int
	if err := s.db.QueryRowContext(ctx, fmt.Sprintf(qCount, clause), args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计问题数失败: %w", err)
	}
	if total == 0 {
		return nil, 0, nil
	}

	size := f.Size
	if size <= 0 || size > 500 {
		size = 50
	}
	page := f.Page
	if page <= 0 {
		page = 1
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(qSelect, clause, orderBy(f)),
		append(append([]any{}, args...), size, (page-1)*size)...)
	if err != nil {
		return nil, 0, fmt.Errorf("查询问题失败: %w", err)
	}
	defer rows.Close()

	var out []model.Question
	var ids []int64
	for rows.Next() {
		var q model.Question
		if err := rows.Scan(&q.ID, &q.Canonical, &q.Category, &q.Topic, &q.N,
			&q.LLMMerged, &q.LLMDomain, &q.LLMCat, &q.Answer, &q.AnswerLen, &q.OfficialN,
			&q.CoN); err != nil {
			return nil, 0, err
		}
		// 列表里只带答案预览。
		//
		// 原因：一页 50 条时完整答案约占 278KB（总响应 447KB），
		// 而用户真正展开看的通常只有一两条。截断后响应降到 ~170KB，
		// 完整答案在被展开时才按 id 取（/api/questions/{id}）。
		q.Answer = clipRunes(q.Answer, answerPreviewRunes)
		out = append(out, q)
		ids = append(ids, q.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if len(ids) == 0 {
		return out, total, nil
	}

	occ, err := s.occurrencesFor(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	for i := range out {
		if l := occ[out[i].ID]; len(l) > occurPreviewLimit {
			occ[out[i].ID] = l[:occurPreviewLimit]
		}
	}
	varMap, err := s.variantsFor(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	for i := range out {
		out[i].Occurs = occ[out[i].ID]
		vs := varMap[out[i].ID]
		out[i].VariantN = len(vs)
		if len(vs) > variantPreviewLimit {
			vs = vs[:variantPreviewLimit]
		}
		out[i].Variants = vs
	}
	return out, total, nil
}

// nullIfEmpty 把空串转成 NULL。
//
// 用途：questions.answer 用 NULL 表示「没有答案」，而不是空串。
// 两者在 SQL 里语义不同（`answer IS NOT NULL` 才是准确的「有答案」判断），
// 混用会让后续统计写出 `answer <> ”` 这种容易漏掉 NULL 的条件。
func nullIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func (s *Store) occurrencesFor(ctx context.Context, ids []int64) (map[int64][]model.Occur, error) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT o.question_id, o.post_id, o.raw_text, o.official,
		       p.company, p.job, p.job_group, p.round, p.round_group,
		       DATE_FORMAT(p.posted_date,'%Y-%m-%d'), p.title, p.url
		FROM occurrences o
		JOIN posts p ON p.id = o.post_id
		WHERE o.question_id IN (`+placeholders(len(ids))+`)
		ORDER BY p.posted_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[int64][]model.Occur{}
	for rows.Next() {
		var qid int64
		var o model.Occur
		if err := rows.Scan(&qid, &o.PostID, &o.RawText, &o.Official, &o.Company, &o.Job, &o.JobGroup,
			&o.Round, &o.RoundGroup, &o.Date, &o.Title, &o.URL); err != nil {
			return nil, err
		}
		m[qid] = append(m[qid], o)
	}
	return m, rows.Err()
}

func (s *Store) variantsFor(ctx context.Context, ids []int64) (map[int64][]string, error) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT question_id, text FROM question_variants WHERE question_id IN (`+
			placeholders(len(ids))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[int64][]string{}
	for rows.Next() {
		var qid int64
		var t string
		if err := rows.Scan(&qid, &t); err != nil {
			return nil, err
		}
		m[qid] = append(m[qid], t)
	}
	return m, rows.Err()
}

// Question 取单个问题及其全部出现记录。
func (s *Store) Question(ctx context.Context, id int64) (*model.Question, error) {
	var q model.Question
	err := s.db.QueryRowContext(ctx,
		`SELECT id,canonical,category,topic,n,llm_merged,llm_domain,llm_cat,
		        COALESCE(answer,''), CHAR_LENGTH(COALESCE(answer,'')),
		        CAST(COALESCE((SELECT SUM(o.official) FROM occurrences o
		                        WHERE o.question_id = questions.id),0) AS SIGNED),
		        co_n
		 FROM questions WHERE id = ?`, id).
		Scan(&q.ID, &q.Canonical, &q.Category, &q.Topic, &q.N,
			&q.LLMMerged, &q.LLMDomain, &q.LLMCat, &q.Answer, &q.AnswerLen, &q.OfficialN,
			&q.CoN)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	occ, err := s.occurrencesFor(ctx, []int64{id})
	if err != nil {
		return nil, err
	}
	vm, err := s.variantsFor(ctx, []int64{id})
	if err != nil {
		return nil, err
	}
	q.Occurs, q.Variants = occ[id], vm[id]
	return &q, nil
}

// facet 统计 posts 表某列的取值分布。
func (s *Store) facet(ctx context.Context, col string, f model.Filter) ([]model.Facet, error) {
	q := `SELECT ` + col + ` AS v, COUNT(*) AS c FROM posts p
	      WHERE p.` + col + ` <> ''`
	var args []any
	if dc, da := dateCond(f); dc != "" {
		q += " AND " + dc
		args = append(args, da...)
	}
	q += ` GROUP BY v ORDER BY c DESC, v ASC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Facet
	for rows.Next() {
		var f model.Facet
		if err := rows.Scan(&f.Value, &f.Count); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return demotePlaceholders(out), rows.Err()
}

// demotePlaceholders 把「其他」「未知」这类兜底桶排到最后。
//
// 它们按计数排往往最大（岗位维度里「其他」有 2414 条，比第一名真岗位多几十倍），
// 结果筛选栏第一个 chip 就是最没信息量的那个，用户第一眼看到的是噪声。
// 计数本身不改，只调顺序。
func demotePlaceholders(fs []model.Facet) []model.Facet {
	if len(fs) < 2 {
		return fs
	}
	var real, ph []model.Facet
	for _, f := range fs {
		switch f.Value {
		case "其他", "其他技术", "未知", "不限", "":
			ph = append(ph, f)
		default:
			real = append(real, f)
		}
	}
	return append(real, ph...)
}

// facetQ 统计 questions 表某列的取值分布，计数为该维度下的**原始问题条数**（SUM(n)）。
//
// knowledgeOnly=true 时排除「开场/通用」——那只是流程套话（自我介绍、团队介绍、反问），
// 不是知识点，不该出现在考察主题的频次统计里。
func (s *Store) facetQ(ctx context.Context, col, category string, knowledgeOnly bool, f model.Filter) ([]model.Facet, error) {
	// 计数口径 = 该维度值的**出现次数**（同一道题被 N 篇帖子问到算 N）。
	//
	// 这里特意不读 questions.n：带时间筛选时那个「全量总数」用不上，
	// 必须回到 occurrences × posts 上按日期重算。两种写法在无筛选时结果完全一致
	// （n 本来就是由 occurrences 统计出来的），所以可以统一走这一条。
	q := `SELECT q.` + col + ` AS v, CAST(COUNT(*) AS SIGNED) AS c
	      FROM questions q
	      JOIN occurrences o ON o.question_id = q.id
	      JOIN posts p ON p.id = o.post_id
	      WHERE q.` + col + ` <> ''`
	var args []any
	if category != "" {
		q += ` AND q.category = ?`
		args = append(args, category)
	}
	if knowledgeOnly {
		q += ` AND q.category <> ?`
		args = append(args, "开场/通用")
	}
	if dc, da := dateCond(f); dc != "" {
		q += " AND " + dc
		args = append(args, da...)
	}
	q += ` GROUP BY v ORDER BY c DESC, v ASC`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Facet
	for rows.Next() {
		var f model.Facet
		if err := rows.Scan(&f.Value, &f.Count); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Facets 返回全部筛选维度的取值与计数。
//
// 模块/主题的计数口径：**该维度下的原始问题条数**（SUM(n)）。
// 主题维度排除「开场/通用」，让考察主题的分布只反映知识点。
func (s *Store) Facets(ctx context.Context, flt model.Filter) (model.Facets, error) {
	var f model.Facets
	var err error
	if f.Rounds, err = s.facet(ctx, "round_group", flt); err != nil {
		return f, err
	}
	if f.Jobs, err = s.facet(ctx, "job_group", flt); err != nil {
		return f, err
	}
	if f.Companies, err = s.facet(ctx, "company", flt); err != nil {
		return f, err
	}
	if f.Categories, err = s.facetQ(ctx, "category", "", false, flt); err != nil {
		return f, err
	}
	if f.Topics, err = s.facetQ(ctx, "topic", "", true, flt); err != nil {
		return f, err
	}
	if f.LLMMerged, err = s.facetLLM(ctx, "llm_merged", flt); err != nil {
		return f, err
	}
	if f.LLMDomain, err = s.facetLLM(ctx, "llm_domain", flt); err != nil {
		return f, err
	}
	if f.LLMCat, err = s.facetLLM(ctx, "llm_cat", flt); err != nil {
		return f, err
	}
	return f, nil
}

// facetLLM 统计 LLM 归类维度的分布，排除「非题目」——那不是知识点。
func (s *Store) facetLLM(ctx context.Context, col string, f model.Filter) ([]model.Facet, error) {
	q := `SELECT q.` + col + ` AS v, CAST(COUNT(*) AS SIGNED) AS c
	      FROM questions q
	      JOIN occurrences o ON o.question_id = q.id
	      JOIN posts p ON p.id = o.post_id
	      WHERE q.` + col + ` <> '' AND q.llm_cat <> '非题目'`
	var args []any
	if dc, da := dateCond(f); dc != "" {
		q += " AND " + dc
		args = append(args, da...)
	}
	q += ` GROUP BY v ORDER BY c DESC, v ASC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Facet
	for rows.Next() {
		var f model.Facet
		if err := rows.Scan(&f.Value, &f.Count); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// TopicFacets 返回指定模块下的主题；category 为空则返回全部主题。
func (s *Store) TopicFacets(ctx context.Context, category string, f model.Filter) ([]model.Facet, error) {
	// 已经限定在某个模块内时不再重复排除：选中的就是「开场/通用」也要能看到它的主题
	return s.facetQ(ctx, "topic", category, false, f)
}

// LLMCatFacets 返回某个规则模块内、各「LLM 细分类」的条数。
//
// 用途：替代「按模块」页原来的二级下钻（规则主题）。
// 实测规则主题在该页已经失效——「后端工程」模块下 **75% 的题落进「其他技术」**，
// 下钻等于没下钻；而同一批题的 LLM 细分类分布是可用信息
// （系统设计与领域建模 / 编程语言特性 / 个人特质与团队文化…）。
//
// 排除「非题目」：那不是一个知识点，不该出现在知识点下钻里。
func (s *Store) LLMCatFacets(ctx context.Context, category string, f model.Filter) ([]model.Facet, error) {
	q := `SELECT q.llm_cat AS v, CAST(COUNT(*) AS SIGNED) AS c
	      FROM questions q
	      JOIN occurrences o ON o.question_id = q.id
	      JOIN posts p ON p.id = o.post_id
	      WHERE q.llm_cat <> '' AND q.llm_cat <> '非题目'`
	var args []any
	if category != "" {
		q += ` AND q.category = ?`
		args = append(args, category)
	}
	if dc, da := dateCond(f); dc != "" {
		q += " AND " + dc
		args = append(args, da...)
	}
	q += ` GROUP BY v ORDER BY c DESC, v ASC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Facet
	for rows.Next() {
		var f model.Facet
		if err := rows.Scan(&f.Value, &f.Count); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Meta 汇总概览统计。
func (s *Store) Meta(ctx context.Context, flt model.Filter) (model.Meta, error) {
	var m model.Meta
	// 时间范围同样限制顶部的统计卡：筛了区间却显示全量统计，
	// 会和下面被筛过的列表自相矛盾。不设区间时 d 退化成 "1=1"，结果与原来完全一致。
	//
	// 注意题目维度的口径：带时间范围时不能再数 questions 表的行数，
	// 而要数「在这个区间里**出现过**的不同题目」——所以统一从 occurrences 连回去。
	d := "1=1"
	var dArgs []any
	if dc, da := dateCond(flt); dc != "" {
		d, dArgs = dc, da
	}
	occ := `FROM occurrences o JOIN posts p ON p.id = o.post_id`
	withQ := occ + ` JOIN questions q ON q.id = o.question_id`
	err := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM posts p WHERE `+d+`),
			(SELECT COUNT(*) FROM posts p WHERE `+d+` AND EXISTS (
				SELECT 1 FROM occurrences o WHERE o.post_id = p.id)),
			(SELECT COUNT(DISTINCT p.company) FROM posts p WHERE p.company <> '' AND `+d+`),
			(SELECT CAST(COUNT(*) AS SIGNED) `+occ+` WHERE `+d+`),
			(SELECT COUNT(DISTINCT o.question_id) `+occ+` WHERE `+d+`),
			(SELECT COUNT(*) `+occ+` WHERE `+d+`),
			(SELECT COUNT(DISTINCT o.question_id) `+withQ+` WHERE `+d+` AND q.answer IS NOT NULL),
			(SELECT COUNT(DISTINCT o.question_id) `+withQ+` WHERE `+d+` AND q.llm_cat <> '' AND q.llm_cat <> '非题目'),
			(SELECT COUNT(DISTINCT o.question_id) `+withQ+` WHERE `+d+` AND q.llm_cat = '非题目'),
			(SELECT COUNT(DISTINCT o.question_id) `+withQ+` WHERE `+d+` AND q.llm_cat = ''),
			(SELECT COALESCE(DATE_FORMAT(MIN(p.posted_date),'%Y-%m-%d'),'') FROM posts p WHERE p.company <> '' AND `+d+`),
			(SELECT COALESCE(DATE_FORMAT(MAX(p.posted_date),'%Y-%m-%d'),'') FROM posts p WHERE p.company <> '' AND `+d+`),
			-- 这两项刻意不带时间条件：时间栏要用全库范围做 min/max，否则筛过一次就缩死了
			(SELECT COALESCE(DATE_FORMAT(MIN(posted_date),'%Y-%m-%d'),'') FROM posts WHERE company <> ''),
			(SELECT COALESCE(DATE_FORMAT(MAX(posted_date),'%Y-%m-%d'),'') FROM posts WHERE company <> '')`,
		repeatArgs(dArgs, 12)...).
		Scan(&m.Posts, &m.Interviews, &m.Companies, &m.RawCount, &m.Questions,
			&m.Occurrence, &m.WithAnswer,
			&m.RealQuestions, &m.NoiseQuestions, &m.Unlabeled, &m.DateFrom, &m.DateTo,
			&m.GlobalFrom, &m.GlobalTo)
	return m, err
}

// PruneEmptyPosts 删除一条问题都没抽出来的帖子，返回被删的帖子与遗留出现记录数。
//
// 这是入库侧最后一道、也最精确的噪声闸门：粗筛靠关键词，总会漏掉
// 「恐怖的秋招」「笔试题求经验」这类看着像、其实没有题目的帖子；
// 而「一个题目都抽不出来」是数据自己给出的结论，不需要再猜规则。
//
// 必须在 ReplaceQuestions 之后调用：那时 occurrences 才是最新的。
func (s *Store) PruneEmptyPosts(ctx context.Context) (posts, occs int64, err error) {
	// 先清掉指向已删问题的出现记录（正常情况下 ReplaceQuestions 已经清干净）
	r1, err := s.db.ExecContext(ctx, `
		DELETE o FROM occurrences o
		LEFT JOIN questions q ON q.id = o.question_id
		WHERE q.id IS NULL`)
	if err != nil {
		return 0, 0, err
	}
	orphan, _ := r1.RowsAffected()

	r2, err := s.db.ExecContext(ctx, `
		DELETE p FROM posts p
		LEFT JOIN occurrences o ON o.post_id = p.id
		WHERE o.post_id IS NULL`)
	if err != nil {
		return 0, 0, err
	}
	n, err := r2.RowsAffected()
	return n, orphan, err
}

// Companies 返回库里已有的公司名，用作宽松标题解析的词典。
//
// 词典随抓取自动扩充：一家公司在任何一篇标准格式标题里出现过，
// 之后它出现在自由格式标题里也能被认出来。
func (s *Store) Companies(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT company FROM posts WHERE company <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// HasLabels 报告库里是否已经有 LLM 归类结果。
//
// 用于决定「默认只展示已核验题目」这条规则要不要生效：
// 全新部署还没跑归类时必须退回「全部展示」，否则页面会是空的。
func (s *Store) HasLabels(ctx context.Context) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM questions WHERE llm_cat <> '' LIMIT 1`).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// AllPostTitles 返回所有有标题的帖子，供标题派生字段的回填使用。
//
// 刻意取**全部**而不是「字段为空的」：回填要能自我修复。
// 解析规则改好后，之前按旧规则填错的值（例如单字「招」）也必须被纠正，
// 而只挑空字段的写法永远碰不到它们。
func (s *Store) AllPostTitles(ctx context.Context) (map[int64]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title FROM posts WHERE title <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			return nil, err
		}
		out[id] = title
	}
	return out, rows.Err()
}

// UpdatePostMeta 只更新标题派生字段，不动正文与时间。
func (s *Store) UpdatePostMeta(ctx context.Context, id int64, company, job, jobGroup, round, roundGroup string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE posts SET company = ?, job = ?, job_group = ?, round = ?, round_group = ?
		WHERE id = ?`, company, job, jobGroup, round, roundGroup, id)
	return err
}

// AllPosts 返回全部帖子，供聚类重建使用。//
// 刻意不走 Posts 的分页：那里的 size 钳制会把「取全部」静默压成 50 条，
// 而重建是全局操作，漏帖会直接毁掉整个问题集合。
func (s *Store) AllPosts(ctx context.Context) ([]model.Post, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,uuid,title,url,company,job,job_group,round,round_group,
		       content,posted_at,DATE_FORMAT(posted_date,'%Y-%m-%d'),interview_exp,scraped_at,
		       author_id,author_name,source,entity_type,engage_like,engage_cmt,engage_view,topic_tag,
		       COALESCE(official_q,'')
		FROM posts ORDER BY posted_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Post
	for rows.Next() {
		var p model.Post
		if err := rows.Scan(&p.ID, &p.UUID, &p.Title, &p.URL, &p.Company, &p.Job, &p.JobGroup,
			&p.Round, &p.RoundGroup, &p.Content, &p.PostedAt, &p.PostedDate,
			&p.InterviewExp, &p.ScrapedAt,
			&p.AuthorID, &p.AuthorName, &p.Source, &p.EntityType,
			&p.LikeCnt, &p.CommentCnt, &p.ViewCnt, &p.TopicTag, &p.OfficialQ); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Posts 按条件分页取帖子。size 会被钳制到 [1,500]；需要全部请用 AllPosts。
func (s *Store) Posts(ctx context.Context, company, round string, withContentOnly, onlyWithQuestions bool, page, size int, flt model.Filter) ([]model.Post, int, error) {
	var conds []string
	var args []any
	if company != "" {
		conds = append(conds, "company = ?")
		args = append(args, company)
	}
	if round != "" {
		conds = append(conds, "round_group = ?")
		args = append(args, round)
	}
	if withContentOnly {
		conds = append(conds, "content <> ''")
	}
	// onlyWithQuestions：只列出「至少有一道有效题」的帖子。
	//
	// 为什么用这个判据而不是加强广告关键词规则：拿库内 432 篇标题含「内推」的帖子
	// 量过，把「面试/面经出现次数」当阈值拦广告，精确率最高只有 **67%**——
	// 会误伤 128 篇**真面经**（它们只是结尾挂了内推码）。
	// 而「一条有效题都抽不出来」是模型逐条判定后的结论，精确得多：
	// 432 篇里 276 篇是这个状态，全是纯内推广告。
	//
	// 判据用「至少有一条**未被判为**非题目的出现记录」，而不是「至少有一条已归类的」：
	// 未归类 ≠ 噪声，不能因为归类还没跑就把帖子藏起来；
	// 这样也天然覆盖了全新部署（库里没有任何标注时全部通过，不会出现空列表）。
	//
	// 注意这是**展示侧过滤**，不删数据：帖子仍在库里，只是不进「面经原文」列表。
	if onlyWithQuestions {
		conds = append(conds, `EXISTS (
			SELECT 1 FROM occurrences o JOIN questions q ON q.id = o.question_id
			WHERE o.post_id = posts.id AND q.llm_cat <> '非题目')`)
	}
	// 全局时间范围。这里不能用 dateCond：它的片段写死了别名 p，
	// 而这个查询的主表就是 posts（没有别名），所以单独拼一次。
	if flt.From != "" {
		conds = append(conds, "posted_date >= ?")
		args = append(args, flt.From)
	}
	if flt.To != "" {
		conds = append(conds, "posted_date <= ?")
		args = append(args, flt.To)
	}
	w := ""
	if len(conds) > 0 {
		w = " WHERE " + strings.Join(conds, " AND ")
	}
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM posts"+w, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if size <= 0 || size > 500 {
		size = 50
	}
	if page <= 0 {
		page = 1
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,uuid,title,url,company,job,job_group,round,round_group,
		       content,posted_at,DATE_FORMAT(posted_date,'%Y-%m-%d'),interview_exp,scraped_at,
		       author_id,author_name,source,entity_type,engage_like,engage_cmt,engage_view,topic_tag
		FROM posts`+w+` ORDER BY posted_at DESC LIMIT ? OFFSET ?`,
		append(append([]any{}, args...), size, (page-1)*size)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []model.Post
	for rows.Next() {
		var p model.Post
		if err := rows.Scan(&p.ID, &p.UUID, &p.Title, &p.URL, &p.Company, &p.Job, &p.JobGroup,
			&p.Round, &p.RoundGroup, &p.Content, &p.PostedAt, &p.PostedDate,
			&p.InterviewExp, &p.ScrapedAt,
			&p.AuthorID, &p.AuthorName, &p.Source, &p.EntityType,
			&p.LikeCnt, &p.CommentCnt, &p.ViewCnt, &p.TopicTag); err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

// Post 取单篇帖子。
func (s *Store) Post(ctx context.Context, id int64) (*model.Post, error) {
	var p model.Post
	err := s.db.QueryRowContext(ctx, `
		SELECT id,uuid,title,url,company,job,job_group,round,round_group,
		       content,posted_at,DATE_FORMAT(posted_date,'%Y-%m-%d'),interview_exp,scraped_at,
		       author_id,author_name,source,entity_type,engage_like,engage_cmt,engage_view,topic_tag
		FROM posts WHERE id = ?`, id).
		Scan(&p.ID, &p.UUID, &p.Title, &p.URL, &p.Company, &p.Job, &p.JobGroup,
			&p.Round, &p.RoundGroup, &p.Content, &p.PostedAt, &p.PostedDate,
			&p.InterviewExp, &p.ScrapedAt,
			&p.AuthorID, &p.AuthorName, &p.Source, &p.EntityType,
			&p.LikeCnt, &p.CommentCnt, &p.ViewCnt, &p.TopicTag)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Now 返回当前毫秒时间戳。
func Now() int64 { return time.Now().UnixMilli() }

// ---------- LLM 归类 ----------

// ApplyTaxonomy 覆盖写入分类体系表。
func (s *Store) ApplyTaxonomy(ctx context.Context, entries []model.TaxonomyEntry) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM taxonomy"); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO taxonomy (name,domain,merged,description,sort_order) VALUES (?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i, e := range entries {
		if _, err := stmt.ExecContext(ctx, e.Name, e.Domain, e.Merged, e.Description, i); err != nil {
			return fmt.Errorf("写入分类 %q 失败: %w", e.Name, err)
		}
	}
	return tx.Commit()
}

// ApplyClassifications 把「问题 id → 细分类名」写回 questions。
// 域与合并类从 taxonomy 表查得，因此合并规则变了只需重跑 ApplyMerged，不必再调模型。
func (s *Store) ApplyClassifications(ctx context.Context, byID map[int64]string) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, domain, merged FROM taxonomy`)
	if err != nil {
		return 0, err
	}
	type pair struct{ domain, merged string }
	meta := map[string]pair{}
	for rows.Next() {
		var n string
		var p pair
		if err := rows.Scan(&n, &p.domain, &p.merged); err != nil {
			rows.Close()
			return 0, err
		}
		meta[n] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx,
		`UPDATE questions SET llm_cat=?, llm_domain=?, llm_merged=? WHERE id=?`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	// 同时按 norm 持久化一份。llm_labels 是「标注的唯一真相」，
	// questions.llm_* 只是它的物化视图——重建会把视图清掉，但标注还在。
	labStmt, err := tx.PrepareContext(ctx,
		`INSERT INTO llm_labels (norm, cat, updated_at)
		 SELECT norm, ?, ? FROM questions WHERE id = ?
		 ON DUPLICATE KEY UPDATE cat = VALUES(cat), updated_at = VALUES(updated_at)`)
	if err != nil {
		return 0, err
	}
	defer labStmt.Close()

	now := time.Now().UnixMilli()
	n := 0
	for id, cat := range byID {
		p := meta[cat] // 未知分类时 domain/merged 为空串
		if _, err := stmt.ExecContext(ctx, cat, p.domain, p.merged, id); err != nil {
			return 0, fmt.Errorf("更新问题 %d 失败: %w", id, err)
		}
		if _, err := labStmt.ExecContext(ctx, cat, now, id); err != nil {
			return 0, fmt.Errorf("持久化标注 %d 失败: %w", id, err)
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// ApplyClassificationsByNorm 按**题目归一化文本**写入归类结果。
//
// 为什么没有 byID 版本：id 是自增主键，`make rebuild` 会 TRUNCATE 重建并重排，
// 因此「旧结果 + 新数据」用 id 对不上，而且 UPDATE ... WHERE id=? 对任何 id 都成功，
// **不会报错**。真实事故：1478 条标注被贴到无关题目上，匹配率 0/212。
// 题目文本才是稳定键。
//
// 写入后立即物化到 questions（按 norm JOIN），返回命中条数。
func (s *Store) ApplyClassificationsByNorm(ctx context.Context, byNorm map[string]string) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, domain, merged FROM taxonomy`)
	if err != nil {
		return 0, err
	}
	type pair struct{ domain, merged string }
	meta := map[string]pair{}
	for rows.Next() {
		var n string
		var p pair
		if err := rows.Scan(&n, &p.domain, &p.merged); err != nil {
			rows.Close()
			return 0, err
		}
		meta[n] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO llm_labels (norm, cat, updated_at) VALUES (?,?,?)
		 ON DUPLICATE KEY UPDATE cat = VALUES(cat), updated_at = VALUES(updated_at)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	now := time.Now().UnixMilli()
	for norm, cat := range byNorm {
		if norm == "" {
			continue
		}
		if _, err := stmt.ExecContext(ctx, model.ClipRunes(norm, model.MaxNormRunes), cat, now); err != nil {
			return 0, fmt.Errorf("写入标注失败: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}

	// 物化到 questions。用 JOIN 而不是逐行 UPDATE：命中的行数由数据库算，
	// 比在 Go 里维护 id 列表可靠。
	if _, err := s.ApplyLabelsFromTable(ctx); err != nil {
		return 0, err
	}
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM questions WHERE llm_cat <> ''`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// ApplyLabelsFromTable 用 llm_labels 按 norm 回填 questions 的 llm_* 三列。
//
// ReplaceQuestions 之后必须调用：重建把 questions 清空了，
// 而标注在 llm_labels 里安然无恙，这里把它们物化回新行。
// 返回回填条数。
func (s *Store) ApplyLabelsFromTable(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE questions q
		JOIN llm_labels l ON l.norm = q.norm
		JOIN taxonomy   t ON t.name = l.cat
		SET q.llm_cat    = l.cat,
		    q.llm_domain = t.domain,
		    q.llm_merged = t.merged`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// LabelStats 返回标注表的规模，便于确认「重建没把标注弄丢」。
func (s *Store) LabelStats(ctx context.Context) (labels, applied int, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM llm_labels),
		       (SELECT COUNT(*) FROM questions WHERE llm_cat <> '')`).Scan(&labels, &applied)
	return
}

// Taxonomy 读回分类体系（按 sort_order）。
func (s *Store) Taxonomy(ctx context.Context) ([]model.TaxonomyEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT name, domain, merged, description FROM taxonomy ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.TaxonomyEntry
	for rows.Next() {
		var e model.TaxonomyEntry
		if err := rows.Scan(&e.Name, &e.Domain, &e.Merged, &e.Description); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AllPostContents 返回 id → 正文，供轮次兜底推断使用。
//
// 与 AllPostTitles 同样是**全量**读取：兜底规则修好后，
// 旧帖也要能被重算到，而不是只处理新的。
func (s *Store) AllPostContents(ctx context.Context) (map[int64]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, COALESCE(content, '') FROM posts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]string)
	for rows.Next() {
		var id int64
		var c string
		if err := rows.Scan(&id, &c); err != nil {
			return nil, err
		}
		out[id] = c
	}
	return out, rows.Err()
}

// repeatArgs 把同一组参数重复 n 次。
//
// Meta 是一个大 SQL，里面有 12 个子查询各带一遍时间条件——占位符是 12 份，
// 参数也必须给 12 份（database/sql 是位置绑定，不会自动复用）。
// 少给会在运行时报 "sql: expected N arguments, got M"。
func repeatArgs(args []any, n int) []any {
	out := make([]any, 0, len(args)*n)
	for i := 0; i < n; i++ {
		out = append(out, args...)
	}
	return out
}
