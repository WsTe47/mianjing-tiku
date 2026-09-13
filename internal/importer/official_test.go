package importer

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/climber47/nc-interview/internal/model"
	"github.com/climber47/nc-interview/internal/scraper"
)

func mustJSON(t *testing.T, qs []scraper.ExperienceQuestion) string {
	t.Helper()
	b, err := json.Marshal(qs)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestOfficialRowsAreExtracted 覆盖官方结构化真题这条高价值管线。
//
// 实测 4.2% 的帖子带 experienceQuestionList，平均每帖 11.1 条且全部附答案。
// 这条路径抽不出来，题库就丢掉了质量最高的那部分数据。
func TestOfficialRowsAreExtracted(t *testing.T) {
	qs := []scraper.ExperienceQuestion{
		{ID: 1, Title: "自我介绍", Answer: "- 正确答案：\n  您好，我是XX大学…"},
		{ID: 2, Title: "实习项目拷打", Answer: "正确答案：考察实习经历的真实性…"},
		{ID: 3, Title: "  ", Answer: "空题目应被丢弃"},
	}
	p := model.Post{ID: 42, Title: "某公司面经", OfficialQ: mustJSON(t, qs)}

	rows := officialRows(p)
	if len(rows) != 2 {
		t.Fatalf("应抽出 2 条（空题目丢弃），实际 %d 条", len(rows))
	}
	for _, r := range rows {
		if !r.official {
			t.Errorf("行 %q 未被标记为 official", r.text)
		}
		if r.answer == "" {
			t.Errorf("行 %q 丢了答案", r.text)
		}
		if r.postID != 42 {
			t.Errorf("行 %q 的 postID 应为 42，实际 %d", r.text, r.postID)
		}
		if r.cat == "" {
			t.Errorf("行 %q 没有归类", r.text)
		}
	}
}

// TestOfficialRowsToleratesBadJSON 保证坏数据不会让整篇帖子崩掉。
// official_q 是一条增益路径，解析失败应当当作「没有」而不是报错。
func TestOfficialRowsToleratesBadJSON(t *testing.T) {
	for _, bad := range []string{"", "not json", "{", "[1,2,3]", `[{"title":123}]`} {
		p := model.Post{ID: 1, Title: "x", OfficialQ: bad}
		if rows := officialRows(p); len(rows) != 0 && bad != `[{"title":123}]` {
			t.Errorf("输入 %q 不该抽出 %d 条", bad, len(rows))
		}
	}
}

// TestOfficialRowsBypassIsQuestion 官方真题不经过 IsQuestion 启发式过滤。
//
// 判据（「面试官有点装」这类是面后感不是题）是给正则从正文里抠出来的行用的；
// 官方列表本身就是题目的权威定义，再筛只会误杀。
// 例：「自我介绍」在正文里常被 reMetaRemark 之类规则误伤，但在官方列表里必须保留。
func TestOfficialRowsBypassIsQuestion(t *testing.T) {
	qs := []scraper.ExperienceQuestion{
		{ID: 1, Title: "自我介绍", Answer: "a"},    // 短、无问号，正文里容易被当套话
		{ID: 2, Title: "整体感觉怎么样", Answer: "b"}, // 以「整体」开头，命中 reMetaRemark
	}
	p := model.Post{ID: 7, Title: "面经", OfficialQ: mustJSON(t, qs)}
	if got := len(officialRows(p)); got != 2 {
		t.Fatalf("官方真题不应被启发式规则筛掉，期望 2 条，实际 %d 条", got)
	}

	// 对照：同样的文本走正文路径就会被 IsQuestion 挡掉，说明这条豁免是必要的
	if IsQuestion("整体感觉怎么样") {
		t.Log("注意：IsQuestion 现在放行了「整体感觉怎么样」，该对照已失效（不影响本测试结论）")
	}
}

// TestExtractKeepsPostWithOfficialQuestions 有官方真题的帖子必须参与抽题，
// 即使标题不符合「公司 岗位 轮次」格式、也不含面试关键词。
func TestExtractKeepsPostWithOfficialQuestions(t *testing.T) {
	qs := []scraper.ExperienceQuestion{{ID: 1, Title: "介绍一下 RAG 的召回策略", Answer: "…"}}
	posts := []model.Post{{
		ID: 9, Title: "随便写写的标题", Company: "", Content: "",
		OfficialQ: mustJSON(t, qs),
	}}
	rows, _ := Extract(posts)
	if len(rows) == 0 {
		t.Fatal("带官方真题的帖子被整体跳过了")
	}
}

// TestPrefixedMetaAnswerIsFiltered 覆盖「先剥前缀再判定」这个顺序要求。
//
// 官方答案的原文形态是「- 正确答案：<正文>」。早先的实现先做答非所问判定、
// 后剥前缀，于是开头被「- 正确答案：」挡住，6 条「作为AI助手，我本身不是…」
// 一直没被过滤掉——而且不报错，只有去 DB 里数才看得出来。
func TestPrefixedMetaAnswerIsFiltered(t *testing.T) {
	raw := []string{
		"- 正确答案：作为AI助手，我本身不是“Agent项目”的开发者或实施者，而是基于大语言模型构建…",
		"正确答案：作为AI模型，我本身没有传统意义上的“记忆架构”…",
		"- 正确答案：\n  请提供具体的面试问题或技术场景，例如“如何判断链表是否有环”…",
	}
	for _, a := range raw {
		clean := TrimAnswer(a)
		if !isMetaAnswer(clean) {
			t.Errorf("剥掉前缀后应判为答非所问: %.48q", a)
		}
	}
	// 正常答案剥前缀后仍要保留
	okRaw := "- 正确答案：\n  G 是 goroutine，M 是内核线程，P 是处理器上下文。"
	if clean := TrimAnswer(okRaw); isMetaAnswer(clean) {
		t.Errorf("正常答案被误判: %q", clean)
	}
}

// TestTrimAnswer 覆盖答案清洗。
func TestTrimAnswer(t *testing.T) {
	cases := []struct{ in, want string }{
		{"- 正确答案：\n  您好", "您好"},
		{"正确答案：在项目中搭建…", "在项目中搭建…"},
		{"参考答案：略", "略"},
		{"答案：42", "42"},
		{"  ", ""},
		{"有\n\n\n\n多行", "有\n\n多行"},
		{"没有前缀的内容", "没有前缀的内容"},
	}
	for _, c := range cases {
		if got := TrimAnswer(c.in); got != c.want {
			t.Errorf("TrimAnswer(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}

	// 超长答案要截断并加省略号——否则单页 50 条会让响应膨胀到几 MB
	long := strings.Repeat("字", maxAnswerRunes+500)
	got := TrimAnswer(long)
	if n := len([]rune(got)); n > maxAnswerRunes+1 {
		t.Errorf("答案未截断：%d 字符", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Error("截断后应加省略号")
	}
}

// TestClipKeepsColumnLimits 保证写入长度不超过列宽。
//
// 这个 bug 真实发生过并让整个重建失败：
// Error 1406 Data too long for column 'norm'——官方真题是完整句子，
// 比正文里抠出来的短行长得多，撑爆了 norm 的 UNIQUE 索引上限。
func TestClipKeepsColumnLimits(t *testing.T) {
	long := strings.Repeat("题", 5000)
	if n := len([]rune(clip(long, maxNormRunes))); n != maxNormRunes {
		t.Errorf("norm 截断后 %d 字符，期望 %d", n, maxNormRunes)
	}
	if n := len([]rune(clip(long, maxCanonRunes))); n != maxCanonRunes {
		t.Errorf("canonical 截断后 %d 字符，期望 %d", n, maxCanonRunes)
	}
	// 不超过上限时原样返回，不该多切
	if got := clip("短题", maxNormRunes); got != "短题" {
		t.Errorf("短文本被改动: %q", got)
	}
	// 多字节字符不能被切成半个
	if got := clip("中文题目", 2); got != "中文" {
		t.Errorf("按字符截断失败: %q", got)
	}
}

// TestClusterAnswerAndOfficialN 覆盖「答案与官方计数聚到同一题」。
//
// 同一道题可能既出现在官方真题里（带答案），又出现在别人的正文回忆里（无答案）。
// 聚成一簇后答案必须保住，且 officialN 要能反映官方出现次数。
func TestClusterAnswerAndOfficialN(t *testing.T) {
	rows := []row{
		{text: "介绍一下 GMP 模型", postID: 1, cat: CatBackend, official: true, answer: "- 正确答案：G 是 goroutine…"},
		{text: "介绍一下GMP模型", postID: 2, cat: CatBackend}, // 正文回忆，无答案
	}
	qs := Cluster(rows)
	if len(qs) != 1 {
		t.Fatalf("应聚成 1 簇，实际 %d 簇: %+v", len(qs), qs)
	}
	q := qs[0]
	if q.Answer == "" {
		t.Error("聚类后答案丢了")
	}
	if strings.HasPrefix(q.Answer, "- 正确答案") {
		t.Errorf("答案前缀未清洗: %q", q.Answer)
	}
	if q.OfficialN != 1 {
		t.Errorf("officialN = %d，期望 1", q.OfficialN)
	}
	if q.N != 2 {
		t.Errorf("n = %d，期望 2（官方 + 正文各一次）", q.N)
	}
	// 代表措辞应优先取官方那条（人工整理过，更规范）
	if q.Canonical != "介绍一下 GMP 模型" {
		t.Errorf("代表措辞应取官方版本，实际 %q", q.Canonical)
	}
}

// TestClusterZeroQuestionsForNoise 噪声帖抽不出题时 Cluster 必须返回空。
// 这是 PruneEmptyPosts 能安全删帖的前提。
func TestClusterZeroQuestionsForNoise(t *testing.T) {
	if qs := Cluster(nil); len(qs) != 0 {
		t.Errorf("空输入应聚出 0 簇，实际 %d", len(qs))
	}
}

// TestNormMatchesCanonical 锁住一条支撑「按题目原文归类」的不变式。
//
// 不变式：对每个簇，Norm(Canonical) == Norm（截断后）。
//
// 为什么必须有它：LLM 归类结果按**题目原文**落地（批次文件里给的是 canonical，
// 导入时算 Norm(canonical)）。若簇内的 norm 取自别的文本，两边就对不上，
// 标注会静默丢失。项目里真发生过：3038 个簇有 35 个不满足这条不变式。
//
// 根源是 norm 早先取自「进簇的第一条」，而 canonical 取「最长/官方的那条」。
func TestNormMatchesCanonical(t *testing.T) {
	rows := []row{
		// 同簇，但第一条不是最长的那条 —— 正是出问题的形状
		{text: "介绍一下 GMP", postID: 1, cat: CatBackend},
		{text: "介绍一下 GMP 模型以及 Goroutine 的调度时机", postID: 2, cat: CatBackend},
		// 官方真题版本（最长，会成为 canonical）
		{text: "请详细介绍一下 Go 的 GMP 调度模型是怎么工作的", postID: 3, cat: CatBackend, official: true, answer: "…"},
		// 另一簇
		{text: "Redis 缓存穿透怎么解决", postID: 1, cat: CatBackend},
		{text: "缓存穿透", postID: 4, cat: CatBackend},
	}
	qs := Cluster(rows)
	if len(qs) == 0 {
		t.Fatal("没有聚出任何簇")
	}
	for _, q := range qs {
		want := clip(Norm(q.Canonical), maxNormRunes)
		if q.Norm != want {
			t.Errorf("不变式被破坏：canonical=%q\n  norm=%q\n  期望=%q", q.Canonical, q.Norm, want)
		}
	}
}

// TestClusterNormUnique 保证不同簇的 norm 互不相同。
//
// norm 在库里是 UNIQUE 键，重复会让插入报错；更重要的是，
// 归类结果按 norm 落地，重复会让两个簇抢同一个标注。
func TestClusterNormUnique(t *testing.T) {
	rows := []row{
		{text: "介绍一下 GMP 调度", postID: 1, cat: CatBackend},
		{text: "Redis 的持久化机制 RDB 和 AOF", postID: 2, cat: CatBackend},
		{text: "三次握手为什么不是两次", postID: 3, cat: CatCS},
		{text: "手撕：LRU 缓存", postID: 4, cat: CatAlgo},
	}
	qs := Cluster(rows)
	seen := map[string]string{}
	for _, q := range qs {
		if prev, ok := seen[q.Norm]; ok {
			t.Errorf("norm 重复: %q 同时属于 %q 和 %q", q.Norm, prev, q.Canonical)
		}
		seen[q.Norm] = q.Canonical
	}
}

// TestIsMetaAnswer 覆盖「答非所问」的答案过滤。
//
// 官方真题的答案由模型生成。题目过于简短或指代不明时（「我的输出是什么」
// 「讲解以下主要区别」「用什么方法解决」），模型会反过来**请求澄清**而不是作答。
// 这类内容留在题库里比没有答案更糟——用户点开「参考答案」看到的是模型在问他要题。
//
// 判据只在**开头**匹配：这些套话都在第一句，放宽到全文会误伤
// （实测有条约 2000 字的正常答案中间就出现了「请补充」）。
func TestIsMetaAnswer(t *testing.T) {
	meta := []string{
		"你的输出是：一段符合指定格式要求的纯文本回答，包含正确答案、解答思路…",
		// 关键回归：官方答案几乎都带「- 正确答案：」前缀。
		// 若先判断再剥前缀，开头被前缀挡住，这类就漏了（真实发生过：
		// 6 条「作为AI助手，我本身不是…」始终没被过滤掉）。
		"作为AI助手，我本身不是“Agent项目”的开发者或实施者",
		"作为AI模型，我本身没有传统意义上的“记忆架构”",
		// 「我作为…」是另一种常见开头，同样要拦
		"我作为AI助手，并没有个人身份，也没有开源项目。",
		"我作为AI模型，并不实际拥有或构建任何真实的数据集。",
		"我作为AI面试官，并不实际使用 Copilot 进行编程工作。",
		"请提供具体的面试问题或技术场景，例如“如何判断链表是否有环”…",
		"请提供您希望我讲解“主要区别”的具体对象或概念。例如…",
		"看起来您的输入可能不完整，只发送了一个“https”。请问您是想了解…",
		"抱歉，我无法回答这个问题。",
		"我是一个人工智能助手，无法…",
		"",
		"   ",
	}
	for _, a := range meta {
		if !isMetaAnswer(a) {
			t.Errorf("应判为答非所问: %.40q", a)
		}
	}

	// 正常答案不能误伤，哪怕中间出现了看起来像套话的词
	ok := []string{
		"HTTPS 在 HTTP 与 TCP 之间加了一层 TLS，解决三件事：机密性、完整性、身份认证。",
		"变分原理是一种数学方法，主要用于物理学和工程学中，特别是在优化问题中…",
		// 关键回归：套话出现在**中后段**时不该被判定
		"连接补偿块的作用是把多个连接件合并成一个整体受力单元。设计中需要注意，" +
			"如果边界条件缺失请补充说明，否则刚度矩阵会奇异。除此之外还要检查单元划分…",
		// 「我作为」本身没问题，只有后面紧跟 AI/模型 才算答非所问
		"我作为项目负责人，主导了这套评测体系的设计与落地，覆盖离线与在线两个环节。",
	}
	for _, a := range ok {
		if isMetaAnswer(a) {
			t.Errorf("正常答案被误判为答非所问: %.40q", a)
		}
	}
}
