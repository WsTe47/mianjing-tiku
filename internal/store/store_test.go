package store_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/climber47/nc-interview/internal/model"
	"github.com/climber47/nc-interview/internal/store"
)

// 为什么要有这组「真连库」的测试：
//
// 项目里已经踩过三个**只在 SQL 层才会暴露**的 bug，而它们都通过了编译和单元测试：
//   - `ON DUPLICATE KEY UPDATE ... IF(new.x='', x, new.x)` 里裸列名被判为 ambiguous
//     （Error 1052），结果**所有新帖插入静默失败**，日志里只有一行写入失败；
//   - `official_q` 是 NULL，Scan 进 string 报 "converting NULL to string is unsupported"，
//     整个聚类重建挂掉；
//   - 官方真题是完整句子，撑爆 norm 的列宽（Error 1406 Data too long）。
//
// 这类错误的共同点是：纯函数测不出来，必须在真 MySQL 上跑一遍真实的建表与写入。
//
// 用独立的测试库（默认 nc_interview_test），绝不碰生产库。
// 未提供 DSN 时整组跳过，这样 CI 上没有 MySQL 也能过。

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("NC_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置 NC_TEST_DSN，跳过需要 MySQL 的集成测试")
	}
	return dsn
}

// openTestStore 打开测试库并清空数据，保证每个用例从干净状态开始。
func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(testDSN(t))
	if err != nil {
		t.Fatalf("打开测试库失败（NC_TEST_DSN 是否正确？）: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	// 清空顺序：先删引用方。questions 被 occurrences/question_variants 引用。
	for _, q := range []string{
		"DELETE FROM occurrences",
		"DELETE FROM question_variants",
		"DELETE FROM questions",
		"DELETE FROM posts",
	} {
		if _, err := st.DB().ExecContext(ctx, q); err != nil {
			t.Fatalf("清理测试库失败 (%s): %v", q, err)
		}
	}
	return st
}

func samplePost() model.Post {
	return model.Post{
		ID: 1001, UUID: "uuid-1001", Title: "某公司 后端开发 一面",
		URL:     "https://www.nowcoder.com/feed/main/detail/uuid-1001",
		Company: "某公司", Job: "后端开发", JobGroup: "后端",
		Round: "一面", RoundGroup: "一面",
		Content:  "1. 介绍一下 GMP\n2. 手撕：LRU",
		PostedAt: 1788000000000, PostedDate: "2026-08-30",
		Source: "sitemap", EntityType: 74,
		OfficialQ: `[{"id":1,"title":"介绍一下 GMP 模型","answer":"- 正确答案：G 是 goroutine"}]`,
	}
}

// TestUpsertPostRoundTrip 覆盖插入 + 读回，并**重复插入一次**。
//
// 重复插入是关键：ON DUPLICATE KEY UPDATE 分支只有在第二次才走到，
// 而 ambiguous column（Error 1052）恰好只在那条分支上。
func TestUpsertPostRoundTrip(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	p := samplePost()

	for i := 0; i < 2; i++ {
		if err := st.UpsertPost(ctx, p); err != nil {
			t.Fatalf("第 %d 次 UpsertPost 失败: %v", i+1, err)
		}
	}

	posts, err := st.AllPosts(ctx)
	if err != nil {
		t.Fatalf("AllPosts 失败: %v", err)
	}
	if len(posts) != 1 {
		t.Fatalf("应有 1 篇帖子，实际 %d 篇", len(posts))
	}
	got := posts[0]
	if got.OfficialQ == "" {
		t.Error("official_q 没有落库或读回为空")
	}
	if got.Company != "某公司" || got.RoundGroup != "一面" {
		t.Errorf("字段读回不对: %+v", got)
	}
}

// TestUpsertPostKeepsAuthorNameWhenEmpty 锁住「空值不覆盖已有值」这条语义。
//
// 两条写入路径能力不同：详情页抓取能拿到作者名，JSONL 导入拿不到。
// 若空值直接覆盖，一次 reimport 就会把作者名冲成空串。
func TestUpsertPostKeepsAuthorNameWhenEmpty(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	p := samplePost()
	p.AuthorID, p.AuthorName = 125006155, "赛文X"
	if err := st.UpsertPost(ctx, p); err != nil {
		t.Fatal(err)
	}
	// 第二次写入不带作者名（模拟 JSONL 导入）
	p.AuthorName = ""
	if err := st.UpsertPost(ctx, p); err != nil {
		t.Fatal(err)
	}

	posts, err := st.AllPosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if posts[0].AuthorName != "赛文X" {
		t.Errorf("空作者名覆盖了已有值，现在是 %q", posts[0].AuthorName)
	}
}

// TestReplaceQuestionsRoundTrip 覆盖聚类重建的完整写入路径。
//
// 这里同时验证三件事，每件都曾经坏过：
//   - answer 写入（migration 004 新增列）
//   - occurrences.official 标志写入
//   - 超长 norm 被截断而不是报 Error 1406
func TestReplaceQuestionsRoundTrip(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	first := samplePost()
	second := samplePost()
	second.ID, second.UUID = 1002, "uuid-1002"
	second.Title = "另一家公司 后端开发 二面"
	for _, p := range []model.Post{first, second} {
		if err := st.UpsertPost(ctx, p); err != nil {
			t.Fatal(err)
		}
	}

	longCanon := ""
	for i := 0; i < 900; i++ {
		longCanon += "很"
	}
	qs := []model.Question{
		{
			Canonical: "介绍一下 GMP 模型", Norm: "介绍一下gmp模型",
			Category: "计算机基础", Topic: "Go 语言", N: 2,
			Answer: "- 正确答案：G 是 goroutine 的缩写",
			// 同一道题出现在两篇帖子里：一篇是官方真题（带答案），一篇是正文回忆。
			// 频次 N 统计的是**去重后的帖子数**（occurrences 有 UNIQUE(question_id,post_id)），
			// 不是出现行数——这才是「多少场面试问过它」的正确口径。
			Occurs: []model.Occur{
				{PostID: 1001, RawText: "介绍一下 GMP 模型", Official: true},
				{PostID: 1002, RawText: "介绍一下GMP模型"},
			},
			OfficialN: 1,
		},
		{
			// 超长题目：验证 clip 生效、列宽够用
			Canonical: longCanon, Norm: longCanon,
			Category: "后端工程", Topic: "其他", N: 1,
			Occurs: []model.Occur{{PostID: 1002, RawText: longCanon}},
		},
	}
	if err := st.ReplaceQuestions(ctx, qs); err != nil {
		t.Fatalf("ReplaceQuestions 失败: %v", err)
	}

	list, total, err := st.QueryQuestions(ctx, model.Filter{Size: 50})
	if err != nil {
		t.Fatalf("QueryQuestions 失败: %v", err)
	}
	if total != 2 || len(list) != 2 {
		t.Fatalf("应有 2 组问题，实际 total=%d len=%d", total, len(list))
	}

	var gmp *model.Question
	for i := range list {
		if list[i].Canonical == "介绍一下 GMP 模型" {
			gmp = &list[i]
		}
		if len([]rune(list[i].Canonical)) > 1024 {
			t.Errorf("canonical 超长未截断: %d 字符", len([]rune(list[i].Canonical)))
		}
	}
	if gmp == nil {
		t.Fatal("没找到 GMP 那道题")
	}
	if gmp.Answer == "" {
		t.Error("answer 没写入或没读回")
	}
	if gmp.OfficialN != 1 {
		t.Errorf("officialN = %d，期望 1", gmp.OfficialN)
	}
	if gmp.N != 2 {
		t.Errorf("n = %d，期望 2", gmp.N)
	}
}

// TestPruneEmptyPostsOnlyRemovesQuestionless 保证清理只删「一条题都抽不出来」的帖子。
//
// 这个操作是破坏性的，且曾经差点删掉真面经（Extract 的召回 bug 会让真面经
// 看起来和打卡动态一模一样）。所以必须有测试钉住它的边界。
func TestPruneEmptyPostsOnlyRemovesQuestionless(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	keep := samplePost()
	noise := samplePost()
	noise.ID, noise.UUID = 1002, "uuid-1002"
	noise.Title, noise.Company = "牛客现在怎么一堆机器人评论", ""

	for _, p := range []model.Post{keep, noise} {
		if err := st.UpsertPost(ctx, p); err != nil {
			t.Fatal(err)
		}
	}

	// 只有 keep 抽出了题
	if err := st.ReplaceQuestions(ctx, []model.Question{{
		Canonical: "介绍一下 GMP 模型", Norm: "介绍一下gmp模型",
		Category: "计算机基础", Topic: "Go 语言", N: 1,
		Occurs: []model.Occur{{PostID: keep.ID, RawText: "介绍一下 GMP 模型"}},
	}}); err != nil {
		t.Fatal(err)
	}

	removed, _, err := st.PruneEmptyPosts(ctx)
	if err != nil {
		t.Fatalf("PruneEmptyPosts 失败: %v", err)
	}
	if removed != 1 {
		t.Errorf("应删 1 篇，实际 %d 篇", removed)
	}

	posts, err := st.AllPosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 1 || posts[0].ID != keep.ID {
		t.Errorf("删错了：剩下 %d 篇，期望只剩 %d", len(posts), keep.ID)
	}
}

// TestHasAnswerFilter 覆盖「只看有答案」筛选。
func TestHasAnswerFilter(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	if err := st.UpsertPost(ctx, samplePost()); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceQuestions(ctx, []model.Question{
		{
			Canonical: "有答案的题", Norm: "有答案的题", Category: "项目", Topic: "项目",
			N: 1, Answer: "参考答案", Occurs: []model.Occur{{PostID: 1001, RawText: "有答案的题", Official: true}},
		},
		{
			Canonical: "没答案的题", Norm: "没答案的题", Category: "项目", Topic: "项目",
			N: 1, Occurs: []model.Occur{{PostID: 1001, RawText: "没答案的题"}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	withAns, total, err := st.QueryQuestions(ctx, model.Filter{HasAnswer: true, Size: 50})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(withAns) != 1 || withAns[0].Canonical != "有答案的题" {
		t.Errorf("hasAnswer 筛选不对: total=%d items=%+v", total, withAns)
	}

	off, total, err := st.QueryQuestions(ctx, model.Filter{OfficialOnly: true, Size: 50})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(off) != 1 || off[0].Canonical != "有答案的题" {
		t.Errorf("officialOnly 筛选不对: total=%d", total)
	}
}

// TestLabelsSurviveRebuild 锁住一次真实数据丢失事故的修复。
//
// 事故经过：llm_cat/llm_domain/llm_merged 只存在 questions 表里，而 questions
// 每次 rebuild 都被 TRUNCATE。原保护是「重建前读进内存、建完按 norm 写回」，
// 只覆盖成功路径——一次重建在插入循环中途因为列宽报错失败，TRUNCATE 已执行、
// 写回永不执行，**1477 条模型标注全部丢失**。模型标注是最贵的产物
// （要跑几十个子代理），不该依赖「重建一定成功」这个假设。
//
// 修法：标注单独存 llm_labels（norm 主键），重建后按 norm 从表回填。
// 本测试验证：写入标注 → 重建（且新一批题目的 id 完全变了）→ 标注仍在。
func TestLabelsSurviveRebuild(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	if err := st.UpsertPost(ctx, samplePost()); err != nil {
		t.Fatal(err)
	}

	// 先写一个分类体系，否则回填时 JOIN taxonomy 会落空
	if err := st.ApplyTaxonomy(ctx, []model.TaxonomyEntry{
		{Name: "Go 语言与运行时", Domain: "计算机基础与编程语言", Merged: "编程语言特性", Description: "x"},
	}); err != nil {
		t.Fatal(err)
	}

	// 第一次：3 道题，按 norm 打标注
	if err := st.ReplaceQuestions(ctx, []model.Question{
		{Canonical: "题目甲", Norm: "题目甲", Category: "后端工程", Topic: "语言特性", N: 1,
			Occurs: []model.Occur{{PostID: 1001, RawText: "题目甲"}}},
		{Canonical: "题目乙", Norm: "题目乙", Category: "后端工程", Topic: "语言特性", N: 1,
			Occurs: []model.Occur{{PostID: 1001, RawText: "题目乙"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := st.ApplyClassificationsByNorm(ctx, map[string]string{
		"题目甲": "Go 语言与运行时",
		"题目乙": "Go 语言与运行时",
	}); err != nil {
		t.Fatal(err)
	} else if n != 2 {
		t.Fatalf("应有 2 条被回填，实际 %d", n)
	}

	// 第二次重建：题目集合变了（模拟新数据涌入导致 id 重排）
	if err := st.ReplaceQuestions(ctx, []model.Question{
		{Canonical: "新题", Norm: "新题", Category: "后端工程", Topic: "语言特性", N: 1,
			Occurs: []model.Occur{{PostID: 1001, RawText: "新题"}}},
		{Canonical: "题目甲", Norm: "题目甲", Category: "后端工程", Topic: "语言特性", N: 1,
			Occurs: []model.Occur{{PostID: 1001, RawText: "题目甲"}}},
		{Canonical: "题目乙", Norm: "题目乙", Category: "后端工程", Topic: "语言特性", N: 1,
			Occurs: []model.Occur{{PostID: 1001, RawText: "题目乙"}}},
	}); err != nil {
		t.Fatal(err)
	}

	list, _, err := st.QueryQuestions(ctx, model.Filter{Size: 50})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, q := range list {
		got[q.Canonical] = q.LLMCat
	}
	if got["题目甲"] != "Go 语言与运行时" || got["题目乙"] != "Go 语言与运行时" {
		t.Errorf("重建后标注丢失: %v", got)
	}
	if got["新题"] != "" {
		t.Errorf("新题不该有标注，实际 %q", got["新题"])
	}
	if len(list) != 3 {
		t.Errorf("应有 3 组问题，实际 %d", len(list))
	}
}

// TestApplyLabelsIgnoresUnknownNorm 保证按 norm 落标注时，库内没有的 norm
// 不会造成错误（批次文件可能比库新，也可能含已合并掉的题）。
func TestApplyLabelsIgnoresUnknownNorm(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	if err := st.UpsertPost(ctx, samplePost()); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyTaxonomy(ctx, []model.TaxonomyEntry{
		{Name: "分类A", Domain: "域A", Merged: "合并A", Description: "x"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceQuestions(ctx, []model.Question{
		{Canonical: "真实存在的题", Norm: "真实存在的题", Category: "后端工程", Topic: "语言特性", N: 1,
			Occurs: []model.Occur{{PostID: 1001, RawText: "真实存在的题"}}},
	}); err != nil {
		t.Fatal(err)
	}

	n, err := st.ApplyClassificationsByNorm(ctx, map[string]string{
		"真实存在的题": "分类A",
		"库里没有的题": "分类A",
	})
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if n != 1 {
		t.Errorf("只有 1 条能命中，实际报告 %d", n)
	}
}

// TestLabelsRecoverAfterQuestionTableReset 直接验证「重建中途失败」后的恢复能力。
//
// 上一个测试走的是成功路径，而成功路径本来就有内存快照兜底，
// 所以它**不能**区分修复前后。真正的事故形态是：
// TRUNCATE 已经执行 → 插入循环报错返回 → 内存里的快照随进程/调用一起丢掉。
//
// 这里显式模拟那个状态：清空 questions、用不同的 id 重新插入同一批 norm，
// 然后只靠 llm_labels 恢复。修复前这里必然恢复不出来。
func TestLabelsRecoverAfterQuestionTableReset(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	if err := st.UpsertPost(ctx, samplePost()); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyTaxonomy(ctx, []model.TaxonomyEntry{
		{Name: "分类A", Domain: "域A", Merged: "合并A", Description: "x"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceQuestions(ctx, []model.Question{
		{Canonical: "题一", Norm: "题一", Category: "后端工程", Topic: "语言特性", N: 1,
			Occurs: []model.Occur{{PostID: 1001, RawText: "题一"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApplyClassificationsByNorm(ctx, map[string]string{"题一": "分类A"}); err != nil {
		t.Fatal(err)
	}

	// 模拟「重建中途失败」留下的状态：questions 被清空，只剩标注表
	if _, err := st.DB().ExecContext(ctx, "DELETE FROM occurrences"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, "DELETE FROM questions"); err != nil {
		t.Fatal(err)
	}

	// 重新插入（自增 id 与之前不同），此时 llm_* 全空
	if err := st.ReplaceQuestions(ctx, []model.Question{
		{Canonical: "题一", Norm: "题一", Category: "后端工程", Topic: "语言特性", N: 1,
			Occurs: []model.Occur{{PostID: 1001, RawText: "题一"}}},
		{Canonical: "题二", Norm: "题二", Category: "后端工程", Topic: "语言特性", N: 1,
			Occurs: []model.Occur{{PostID: 1001, RawText: "题二"}}},
	}); err != nil {
		t.Fatal(err)
	}

	// ① ReplaceQuestions 内部就应该按 norm 从标注表回填好（这才是修复点）
	byCanon := map[string]model.Question{}
	list, _, err := st.QueryQuestions(ctx, model.Filter{Size: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range list {
		byCanon[x.Canonical] = x
	}
	q, ok := byCanon["题一"]
	if !ok {
		t.Fatalf("题一不存在，实际有 %v", len(byCanon))
	}
	if q.LLMCat != "分类A" || q.LLMDomain != "域A" || q.LLMMerged != "合并A" {
		t.Errorf("重建后未按 norm 回填: cat=%q domain=%q merged=%q", q.LLMCat, q.LLMDomain, q.LLMMerged)
	}
	if byCanon["题二"].LLMCat != "" {
		t.Errorf("题二不该有标注，实际 %q", byCanon["题二"].LLMCat)
	}

	// ② 再模拟一次「标注列被清掉」，验证只靠标注表就能恢复，不依赖任何内存状态
	if _, err := st.DB().ExecContext(ctx, "UPDATE questions SET llm_cat='', llm_domain='', llm_merged=''"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApplyLabelsFromTable(ctx); err != nil {
		t.Fatal(err)
	}
	q2, err := st.Question(ctx, q.ID)
	if err != nil || q2 == nil {
		t.Fatalf("取题失败: %v", err)
	}
	if q2.LLMCat != "分类A" {
		t.Errorf("从标注表恢复失败: %q", q2.LLMCat)
	}
}

// TestListReturnsPreviewDetailReturnsFull 锁住「列表给预览、详情给全文」的契约。
//
// 为什么必须有测试：`answer` 和 `occurrences` 在列表与详情里**字段名完全一样**，
// 只是一个被截断了。这种「同名不同义」的契约最容易在改代码时被破坏，
// 而且破坏了不会报错——只会让响应悄悄变大（回到一页 447KB），
// 或者让前端以为已经拿到全文、不再去取。
//
// 断言刻意**不写死具体上限**，只断言「列表确实短于详情」以及
// 「answerLen / n 是真值」。这样调整 answerPreviewRunes 这类常量时测试不会误报，
// 而真正破坏契约（比如把截断去掉、或让 answerLen 跟着截断）才会红。
func TestListReturnsPreviewDetailReturnsFull(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	// 造 30 篇帖子：既让答案超长，也让出现记录条数超过展示上限
	const nPosts = 30
	if _, err := st.DB().ExecContext(ctx, "DELETE FROM posts"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < nPosts; i++ {
		p := samplePost()
		p.ID = int64(3000 + i)
		p.UUID = fmt.Sprintf("uuid-%04d", i)
		if err := st.UpsertPost(ctx, p); err != nil {
			t.Fatal(err)
		}
	}

	longAnswer := strings.Repeat("答", 3000)
	occ := make([]model.Occur, 0, nPosts)
	for i := 0; i < nPosts; i++ {
		occ = append(occ, model.Occur{PostID: int64(3000 + i), RawText: "题", Official: true})
	}
	if err := st.ReplaceQuestions(ctx, []model.Question{{
		Canonical: "一条带超长答案的题", Norm: "一条带超长答案的题",
		Category: "项目", Topic: "项目", N: nPosts, Answer: longAnswer, Occurs: occ,
	}}); err != nil {
		t.Fatal(err)
	}

	// ① 列表
	list, total, err := st.QueryQuestions(ctx, model.Filter{Size: 10})
	if err != nil || total != 1 || len(list) != 1 {
		t.Fatalf("列表查询异常: total=%d len=%d err=%v", total, len(list), err)
	}
	got := list[0]

	// ② 详情
	full, err := st.Question(ctx, got.ID)
	if err != nil || full == nil {
		t.Fatalf("详情查询失败: %v", err)
	}

	// 契约一：列表的 answer 是截断预览，详情是全文
	if len([]rune(got.Answer)) >= len([]rune(full.Answer)) {
		t.Errorf("列表 answer（%d 字）应短于详情（%d 字）",
			len([]rune(got.Answer)), len([]rune(full.Answer)))
	}
	// 契约二：answerLen 必须是**原始**长度，不能跟着截断
	if got.AnswerLen != len([]rune(full.Answer)) {
		t.Errorf("answerLen=%d 应等于完整答案长度 %d", got.AnswerLen, len([]rune(full.Answer)))
	}
	// 契约三：出现记录同理
	if len(got.Occurs) >= len(full.Occurs) {
		t.Errorf("列表 occurrences（%d 条）应少于详情（%d 条）", len(got.Occurs), len(full.Occurs))
	}
	// 契约四：n 是真实频次，不因展示截断而变小
	if got.N != full.N || got.N != nPosts {
		t.Errorf("n 应为真实频次 %d，列表=%d 详情=%d", nPosts, got.N, full.N)
	}
}

// TestPostsHidesAdOnly 覆盖「面经原文」列表的展示过滤。
//
// 背景：标题含「内推」的帖子里有 276/432 是**纯内推广告**（一条有效题都抽不出来）。
// 曾经考虑加强广告关键词规则，但拿库内数据量过：把「面试/面经出现次数」
// 当阈值拦广告，精确率最高只有 67%，会误伤 128 篇真面经（它们只是结尾挂了内推码）。
//
// 所以改用「有没有有效题」这个模型逐条判定后的结论做展示过滤。它是**展示侧**的，
// 不删数据——帖子仍在库里，只是不进浏览列表；`all=1` 可以看到全部。
func TestPostsHidesAdOnly(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	// 注意「非题目」必须在 taxonomy 里：ApplyLabelsFromTable 是 JOIN taxonomy 回填的，
	// 不在体系里的分类名不会落到 questions.llm_cat 上（生产环境由 LoadLLM 写入）。
	if err := st.ApplyTaxonomy(ctx, []model.TaxonomyEntry{
		{Name: "分类A", Domain: "域A", Merged: "合并A", Description: "x"},
		{Name: "非题目", Domain: "非知识点", Merged: "非题目", Description: "不是题目"},
	}); err != nil {
		t.Fatal(err)
	}

	// 两篇帖子：一篇抽出有效题，一篇的题全被判为非题目（纯广告）
	real := samplePost()
	real.ID, real.UUID, real.Title = 4001, "uuid-4001", "真面经"
	ad := samplePost()
	ad.ID, ad.UUID, ad.Title = 4002, "uuid-4002", "内推码分享"
	for _, p := range []model.Post{real, ad} {
		if err := st.UpsertPost(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.ReplaceQuestions(ctx, []model.Question{
		{Canonical: "一道真题", Norm: "一道真题", Category: "项目", Topic: "项目", N: 1,
			Occurs: []model.Occur{{PostID: real.ID, RawText: "一道真题"}}},
		{Canonical: "内推投递方式", Norm: "内推投递方式", Category: "项目", Topic: "项目", N: 1,
			Occurs: []model.Occur{{PostID: ad.ID, RawText: "内推投递方式"}}},
	}); err != nil {
		t.Fatal(err)
	}
	// 把广告那条标成非题目
	if _, err := st.ApplyClassificationsByNorm(ctx, map[string]string{"内推投递方式": "非题目"}); err != nil {
		t.Fatal(err)
	}

	// 默认：只列有有效题的
	list, total, err := st.Posts(ctx, "", "", false, true, 1, 50, model.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(list) != 1 || list[0].ID != real.ID {
		t.Errorf("默认应只列真面经 1 篇，实际 total=%d ids=%v", total, idsOf(list))
	}

	// all：两篇都在
	_, total2, err := st.Posts(ctx, "", "", false, false, 1, 50, model.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if total2 != 2 {
		t.Errorf("不过滤时应列出 2 篇，实际 %d", total2)
	}
}

func idsOf(ps []model.Post) []int64 {
	var out []int64
	for _, p := range ps {
		out = append(out, p.ID)
	}
	return out
}

// TestDeduplicatePostsCollapsesRepostsAndKeepsQuestions 覆盖真实事故：
// 内推广告把同一份模板题单复制多遍，被刷成整齐的假频次。
// 去重后帖子数应减少，但**一道有效题都不能丢**，频次应回落到真实值。
func TestDeduplicatePostsCollapsesRepostsAndKeepsQuestions(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	for _, id := range []int64{2001, 2002, 2003} {
		p := samplePost()
		p.ID = id
		p.UUID = fmt.Sprintf("uuid-%d", id)
		p.Title = "美团内推美团内推码"
		if err := st.UpsertPost(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	// 标题不同的一篇真面经，用来验证不会被误删
	other := samplePost()
	other.ID = 2004
	other.UUID = "uuid-2004"
	other.Title = "字节 后端 一面"
	if err := st.UpsertPost(ctx, other); err != nil {
		t.Fatal(err)
	}

	// 三篇转帖题目集相同（每题 3 次出现），第四篇不同。
	// ReplaceQuestions 收的是**已聚类**的列表：一个簇一条，出现记录挂在 Occurs 上。
	cluster := func(canonical string, postIDs ...int64) model.Question {
		q := model.Question{
			Canonical: canonical, Norm: canonical,
			Category: "后端工程", Topic: "语言特性", N: len(postIDs),
		}
		for _, id := range postIDs {
			q.Occurs = append(q.Occurs, model.Occur{PostID: id, RawText: canonical})
		}
		return q
	}
	all := []model.Question{
		cluster("介绍一下 GMP", 2001, 2002, 2003),
		cluster("手撕：LRU", 2001, 2002, 2003),
		cluster("TCP 三次握手", 2004),
	}
	if err := st.ReplaceQuestions(ctx, all); err != nil {
		t.Fatal(err)
	}

	meta, err := st.Meta(ctx, model.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if meta.Posts != 4 {
		t.Fatalf("去重前应有 4 篇，实际 %d", meta.Posts)
	}

	removed, _, err := st.DeduplicatePosts(ctx, 0.8)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("应去掉 2 篇转帖（保留最早的一篇），实际 %d", removed)
	}

	meta, err = st.Meta(ctx, model.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if meta.Posts != 2 {
		t.Fatalf("去重后应剩 2 篇，实际 %d", meta.Posts)
	}

	// 关键断言：有效题一道都没少，只是频次从 3 回落到 1
	qs, _, err := st.QueryQuestions(ctx, model.Filter{Size: 50})
	if err != nil {
		t.Fatal(err)
	}
	var gmp *model.Question
	for i := range qs {
		if qs[i].Canonical == "介绍一下 GMP" {
			gmp = &qs[i]
		}
	}
	if gmp == nil {
		t.Fatal("去重后「介绍一下 GMP」整簇消失了——去重不该丢题")
	}
	if gmp.N != 1 {
		t.Errorf("转帖去重后频次应为 1，实际 %d（说明重复计数还在）", gmp.N)
	}
	// 标题不同那篇不受影响
	var tcp *model.Question
	for i := range qs {
		if qs[i].Canonical == "TCP 三次握手" {
			tcp = &qs[i]
		}
	}
	if tcp == nil || tcp.N != 1 {
		t.Errorf("无关帖子被误伤：TCP 三次握手 = %v", tcp)
	}
}

// TestRecomputeCountsSetsCompanyCoverage 覆盖 co_n：它必须数「不同公司」，
// 而不是出现次数——否则「一家公司刷很多帖」会被算成普遍重要。
func TestRecomputeCountsSetsCompanyCoverage(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	for _, pc := range []struct {
		id int64
		co string
	}{{4001, "甲公司"}, {4002, "乙公司"}, {4003, "甲公司"}} {
		p := samplePost()
		p.ID = pc.id
		p.UUID = fmt.Sprintf("uuid-%d", pc.id)
		p.Company = pc.co
		if err := st.UpsertPost(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	cluster := func(c string, ids ...int64) model.Question {
		q := model.Question{Canonical: c, Norm: c, Category: "后端工程", Topic: "语言特性", N: len(ids)}
		for _, id := range ids {
			q.Occurs = append(q.Occurs, model.Occur{PostID: id, RawText: c})
		}
		return q
	}
	// 前者横跨甲乙两家（co_n=2，n=2）；后者只被甲公司问了两次（co_n=1，n=2）
	if err := st.ReplaceQuestions(ctx, []model.Question{
		cluster("被两家公司问过的题", 4001, 4002),
		cluster("被同一家公司问两次的题", 4001, 4003),
	}); err != nil {
		t.Fatal(err)
	}

	qs, _, err := st.QueryQuestions(ctx, model.Filter{Size: 50})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][2]int{}
	for _, q := range qs {
		got[q.Canonical] = [2]int{q.N, q.CoN}
	}
	if g := got["被两家公司问过的题"]; g != [2]int{2, 2} {
		t.Errorf("两家公司各问一次：n=%d co_n=%d，期望 n=2 co_n=2", g[0], g[1])
	}
	if g := got["被同一家公司问两次的题"]; g != [2]int{2, 1} {
		t.Errorf("同一家公司问两次：n=%d co_n=%d，期望 n=2 co_n=1（这正是 n 会虚高的情形）", g[0], g[1])
	}
}

// TestQueryQuestionsSortByCoverage 覆盖排序选项：sort=co 必须让
// 「覆盖面广但次数少」的题排在「次数多但只来自一家公司」的题前面。
func TestQueryQuestionsSortByCoverage(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	posts := []struct {
		id int64
		co string
	}{{5001, "甲公司"}, {5002, "乙公司"}, {5003, "丙公司"}, {5004, "丁公司"}}
	for _, p := range posts {
		post := samplePost()
		post.ID = p.id
		post.UUID = fmt.Sprintf("uuid-%d", p.id)
		post.Company = p.co
		if err := st.UpsertPost(ctx, post); err != nil {
			t.Fatal(err)
		}
	}
	cluster := func(c string, ids ...int64) model.Question {
		q := model.Question{Canonical: c, Norm: c, Category: "后端工程", Topic: "语言特性", N: len(ids)}
		for _, id := range ids {
			q.Occurs = append(q.Occurs, model.Occur{PostID: id, RawText: c})
		}
		return q
	}
	// 广度题：4 家公司各 1 次。深度题：同一家公司 4 次（用同一篇帖子重复不了，
	// 所以给甲公司的两篇帖子 + 另外两篇同公司帖子）
	if err := st.ReplaceQuestions(ctx, []model.Question{
		cluster("覆盖面广的题", 5001, 5002, 5003, 5004),
		cluster("只被一家公司反复问的题", 5001, 5001),
	}); err != nil {
		t.Fatal(err)
	}

	qs, _, err := st.QueryQuestions(ctx, model.Filter{Size: 50, Sort: "co"})
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) == 0 || qs[0].Canonical != "覆盖面广的题" {
		t.Fatalf("sort=co 时覆盖面广的题应排第一，实际首条 = %v", func() string {
			if len(qs) == 0 {
				return "(空)"
			}
			return qs[0].Canonical
		}())
	}
}

// TestDateRangeFiltersEverything 覆盖全局时间范围。
//
// 关键不只是「问题列表被筛了」，而是**各维度计数也一起被筛**——
// 用户选了区间之后，公司榜/知识点分布还显示全量的话，
// 那种自相矛盾比不提供筛选更误导。
func TestDateRangeFiltersEverything(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	// 8 月一篇（甲公司），9 月一篇（乙公司）
	aug := samplePost()
	aug.ID = 6001
	aug.UUID = "uuid-6001"
	aug.Company = "甲公司"
	aug.PostedDate = "2026-08-10"
	aug.PostedAt = 1786000000000
	if err := st.UpsertPost(ctx, aug); err != nil {
		t.Fatal(err)
	}
	sep := samplePost()
	sep.ID = 6002
	sep.UUID = "uuid-6002"
	sep.Company = "乙公司"
	sep.PostedDate = "2026-09-10"
	sep.PostedAt = 1788000000000
	if err := st.UpsertPost(ctx, sep); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceQuestions(ctx, []model.Question{
		{Canonical: "八月的题", Norm: "八月的题", Category: "后端工程", Topic: "语言特性", N: 1,
			Occurs: []model.Occur{{PostID: 6001, RawText: "八月的题"}}},
		{Canonical: "九月的题", Norm: "九月的题", Category: "后端工程", Topic: "语言特性", N: 1,
			Occurs: []model.Occur{{PostID: 6002, RawText: "九月的题"}}},
	}); err != nil {
		t.Fatal(err)
	}

	// 全量
	_, totalAll, err := st.QueryQuestions(ctx, model.Filter{Size: 50})
	if err != nil {
		t.Fatal(err)
	}
	if totalAll != 2 {
		t.Fatalf("不限时间应有 2 道题，实际 %d", totalAll)
	}

	// 只看 9 月
	sepOnly := model.Filter{Size: 50, From: "2026-09-01", To: "2026-09-30"}
	got, total, err := st.QueryQuestions(ctx, sepOnly)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(got) != 1 || got[0].Canonical != "九月的题" {
		t.Fatalf("9 月区间应只剩「九月的题」，实际 total=%d items=%v", total, func() []string {
			var o []string
			for _, q := range got {
				o = append(o, q.Canonical)
			}
			return o
		}())
	}

	// 单边区间（从某天起）
	if _, total, err = st.QueryQuestions(ctx, model.Filter{Size: 50, From: "2026-09-01"}); err != nil {
		t.Fatal(err)
	} else if total != 1 {
		t.Errorf("只给 from 应剩 1 道题，实际 %d", total)
	}
	// 空区间
	if _, total, err = st.QueryQuestions(ctx, model.Filter{Size: 50, From: "2026-01-01", To: "2026-01-02"}); err != nil {
		t.Fatal(err)
	} else if total != 0 {
		t.Errorf("区间外应为 0 道题，实际 %d", total)
	}

	// 维度计数也必须跟着时间走：只看 9 月时，公司榜里不该出现甲公司
	fs, err := st.Facets(ctx, sepOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range fs.Companies {
		if c.Value == "甲公司" {
			t.Errorf("公司维度没跟着时间筛：9 月区间里出现了甲公司 %+v", c)
		}
	}
	found := false
	for _, c := range fs.Companies {
		if c.Value == "乙公司" {
			found = true
		}
	}
	if !found {
		t.Errorf("9 月区间里应出现乙公司，实际 %+v", fs.Companies)
	}

	// 面经原文列表同样受限（这一条曾经漏掉：参数加了但没拼进 SQL）
	_, postsAll, err := st.Posts(ctx, "", "", false, false, 1, 50, model.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	_, postsSep, err := st.Posts(ctx, "", "", false, false, 1, 50, sepOnly)
	if err != nil {
		t.Fatal(err)
	}
	if postsAll != 2 || postsSep != 1 {
		t.Errorf("原文列表没跟着时间筛：全量 %d（期望 2），9 月 %d（期望 1）", postsAll, postsSep)
	}

	// 时间栏要用**不受筛选影响**的全库范围做 min/max，否则筛过一次就缩死了
	mAll, err := st.Meta(ctx, model.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	mSep, err := st.Meta(ctx, sepOnly)
	if err != nil {
		t.Fatal(err)
	}
	if mAll.GlobalFrom != mSep.GlobalFrom || mAll.GlobalTo != mSep.GlobalTo {
		t.Errorf("全库范围不该随筛选变化：全量 %s~%s，9 月 %s~%s",
			mAll.GlobalFrom, mAll.GlobalTo, mSep.GlobalFrom, mSep.GlobalTo)
	}
	if mSep.DateFrom == mAll.DateFrom && mSep.DateTo == mAll.DateTo {
		t.Errorf("被筛的 dateFrom/dateTo 应该反映筛选后的范围，实际仍是 %s~%s", mSep.DateFrom, mSep.DateTo)
	}

	// 矩阵同样受限
	mx, err := st.Matrix(ctx, 10, 10, sepOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range mx.Companies {
		if c == "甲公司" {
			t.Errorf("矩阵没跟着时间筛：9 月区间里出现了甲公司 %v", mx.Companies)
		}
	}
}
