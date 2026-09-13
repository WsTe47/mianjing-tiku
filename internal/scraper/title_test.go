package scraper

import "testing"

// TestRoundOf 覆盖「从自由标题里认轮次」。
//
// 背景：ParseTitle 只认「公司 岗位 轮次」严格格式，抓全站后发现绝大多数标题是自由写法，
// 于是轮次字段大面积为空、整个「按轮次筛选」维度近乎失效。
func TestRoundOf(t *testing.T) {
	cases := map[string]string{
		"11.9金山WPS-服务端笔试":      "笔试",
		"oppo笔试，一道做不出，到底怎么个事？": "笔试",
		"百度笔试0820":             "笔试",
		"字节一面挂的后续一些面试...":      "一面",
		"momenta二面":            "二面",
		"Minimax 秋招后端三面":       "三面",
		"遥望科技 hr面":             "HR面",
		"某公司 HR 面":             "HR面",
		"安克 测评挂":               "测评",
		"京东AI面试有点夸张":           "",
		"新凯来机电一体化面经":           "",
	}
	for title, want := range cases {
		if got := RoundOf(title); got != want {
			t.Errorf("RoundOf(%q) = %q，期望 %q", title, got, want)
		}
	}
}

// TestJobOf 覆盖岗位方向识别。
func TestJobOf(t *testing.T) {
	cases := map[string]string{
		"携程Java后端一面面经":       "后端",
		"momenta后端一面":        "后端", // 回归：曾被 "TA\b" 误判成「游戏」（"momenTA"）
		"哔哩哔哩AI应用岗agent开发一面": "Agent/AI 应用",
		"游戏智能测试工程师笔试题求经验":    "测试", // 游戏公司的测试岗 → 测试更贴切
		"速腾聚创嵌入式一面凉经":        "硬件/嵌入式",
		"新凯来机电一体化面经":         "硬件/嵌入式",
		"腾讯游戏引擎开发一面":         "游戏",
		"柠檬微趣笔试复习题":          "", // 没有岗位词就不猜
	}
	for title, want := range cases {
		if got := JobOf(title); got != want {
			t.Errorf("JobOf(%q) = %q，期望 %q", title, got, want)
		}
	}
}

// TestParseTitleLooseCompany 覆盖公司名抽取。
//
// 精度比召回重要：填错公司会让人按公司筛出完全无关的题。
func TestParseTitleLooseCompany(t *testing.T) {
	// 应当抽对
	ok := map[string]string{
		"美团9.12笔试大家aicoding和编程通过多少？": "美团",
		"oppo笔试，一道做不出，到底怎么个事？":       "oppo",
		"Minimax 秋招后端三面":             "minimax",
		"momenta后端一面":                "momenta",
		"11.9金山WPS-服务端笔试":            "金山",
		"阿里笔试":                       "阿里",
		"字节一面挂的后续一些面试...":            "字节",
		"b站 agent开发 一面":              "b站",
		"京东算法笔试":                     "京东",
		"9.2华为笔试":                    "华为",
		"携程Java后端一面面经":               "携程",
	}
	for title, want := range ok {
		got, _, _ := ParseTitleLoose(title, nil)
		if got != want {
			t.Errorf("ParseTitleLoose(%q) 公司 = %q，期望 %q", title, got, want)
		}
	}

	// 抽不出就该留空 —— 这些标题里根本没有公司名，猜出来的都是噪声
	//（曾经真的产出过「可免」「国际」「大模型」「量化」这些假公司）
	empty := []string{
		"可免笔试，PDD等你",
		"国际 二面",
		"最恶心的一次面试",
		"游戏智能测试工程师笔试题求经验",
		"面试问“volatile是干什么用的”？答“防止编译器优化”——直接被追问到怀疑人生",
		"牛客现在怎么一堆机器人评论",
		"社招1周投了1000家感悟",
	}
	for _, title := range empty {
		if got, _, _ := ParseTitleLoose(title, nil); got != "" {
			t.Errorf("ParseTitleLoose(%q) 不该抽出公司，实际 %q", title, got)
		}
	}
}

// TestParseTitleLooseLearnsCompanies 验证外部词典能提高召回。
//
// cmd/parse 会把库里已有的 company 值当作词典传进来，于是
// 「某家小公司在标准格式里出现过一次」之后，它出现在自由标题里也能被认出来。
func TestParseTitleLooseLearnsCompanies(t *testing.T) {
	title := "卓驭智驾面试记录" // 不含轮次词，也没有种子表命中
	if got, _, _ := ParseTitleLoose(title, nil); got != "" {
		t.Logf("未给词典时抽到 %q（若种子表覆盖了它也属正常）", got)
	}
	// 词典里有它，且名字出现在标题里 → 必须命中
	got, _, _ := ParseTitleLoose(title, []string{"卓驭智驾"})
	if got != "卓驭智驾" {
		t.Errorf("词典命中失败，得到 %q", got)
	}
}

// TestParseTitleLooseStandardFirst 保证标准格式仍然优先。
//
// 宽松规则是**补充**，不能把原本正确的解析搞坏。
func TestParseTitleLooseStandardFirst(t *testing.T) {
	c, j, r := ParseTitleLoose("腾讯 后端开发 一面", nil)
	if c != "腾讯" || j != "后端开发" || r != "一面" {
		t.Errorf("标准格式解析被破坏: %q %q %q", c, j, r)
	}
}

// TestLexiconPoisoningResistance 覆盖一次真实的词典污染事故。
//
// 经过：一个单字「招」混进了公司词典（严格格式对第一个字段不做校验），
// 而词典命中用的是 strings.Contains，于是**所有含「招」的标题**
// （秋招/校招/招聘/招银…）全被认成同一家公司，79 篇帖子被贴错。
//
// 两道防线：① 单字一律不算公司名；② 词典命中结果也要过 looksLikeCompany。
func TestLexiconPoisoningResistance(t *testing.T) {
	poisoned := []string{"招", "秋", "", " ", "面试", "秋招"}

	// 这些标题里没有公司，被污染的词典不该让它们抽出公司
	for _, title := range []string{
		"2027年秋招开始了",
		"末2三无选手，秋招还有救吗",
		"#27秋招进度",
		"校招别等秋天才准备，四阶段时间线帮你卡住机会",
		"真无语，刚开始秋招就和舍友拉开差距了",
	} {
		if got, _, _ := ParseTitleLoose(title, poisoned); got != "" {
			t.Errorf("被污染的词典让 %q 抽出了公司 %q", title, got)
		}
	}

	// 反向：合法词典仍要正常工作，不能因为加了校验就不命中
	if got, _, _ := ParseTitleLoose("卓驭智驾面试记录", []string{"卓驭智驾"}); got != "卓驭智驾" {
		t.Errorf("合法词典命中失败: %q", got)
	}
}

// TestLooksLikeCompanyRejectsSingleRune 单字不是公司名。
func TestLooksLikeCompanyRejectsSingleRune(t *testing.T) {
	for _, s := range []string{"招", "中", "国", "A", "1"} {
		if looksLikeCompany(s) {
			t.Errorf("单字 %q 不应被判为公司名", s)
		}
	}
	for _, s := range []string{"字节跳动", "360", "4399", "b站", "momenta", "虾皮"} {
		if !looksLikeCompany(s) {
			t.Errorf("%q 应被判为公司名", s)
		}
	}
	// 含噪声词的不算
	for _, s := range []string{"面试官", "某公司", "秋招", "挂了", "后续分享"} {
		if looksLikeCompany(s) {
			t.Errorf("%q 含噪声词，不应被判为公司名", s)
		}
	}
	// 含句子标点或分隔符的不算。
	// 这几个都是从库里捞出来的真实脏值——它们不含任何噪声词，
	// 只是「不像公司名」，所以要按字符形态单独挡一道。
	for _, s := range []string{
		"学历给我开了门，也给我挖了坑",
		"祝愿仍在求职的各位，心想事成",
		"文案/演出/任务策划解惑贴",
		"泰凌微电子_嵌软_一/",
		"某公司(北京)",
	} {
		if looksLikeCompany(s) {
			t.Errorf("%q 含标点/分隔符，不应被判为公司名", s)
		}
	}
	// 反向：正常公司名里可能出现的中文括号形式要放行（只挡半角与句读）
	for _, s := range []string{"中电科", "哔哩哔哩", "momenta", "4399", "TP-LINK"} {
		if !looksLikeCompany(s) {
			t.Errorf("%q 应被判为公司名（连字符是允许的）", s)
		}
	}
}

// TestStrictPathCompanyValidated 严格格式切出来的公司名也要过校验。
func TestStrictPathCompanyValidated(t *testing.T) {
	// 「招 银 一面」符合「A B 轮次」格式，但「招」不是公司名
	c, _, r := ParseTitleLoose("招 银 一面", nil)
	if c != "" {
		t.Errorf("严格路径未校验公司名，得到 %q", c)
	}
	if r != "一面" {
		t.Errorf("轮次应仍被识别，得到 %q", r)
	}
	// 正常情况不受影响
	if c, _, _ := ParseTitleLoose("腾讯 后端开发 一面", nil); c != "腾讯" {
		t.Errorf("正常严格格式被破坏: %q", c)
	}
}

// TestContainsCompanyWordBoundary 覆盖 ASCII 公司名的词边界要求。
//
// 真实事故：库里有 7 篇帖子被认成一家叫 **Coding** 的公司，
// 来源是标题「AI Coding经验分享」——"coding" 是 "aicoding" 的子串，
// 而词典匹配用的是裸 strings.Contains。
// 同类隐患：meta 会命中 metadata、go 会命中 google、ai 会命中 said。
//
// 中文没有词边界，所以只对纯 ASCII 的名字卡边界。
func TestContainsCompanyWordBoundary(t *testing.T) {
	bad := [][2]string{
		{"aicoding 平台设计", "coding"},
		{"聊聊 metadata 管理", "meta"},
		{"google 的搜索架构", "go"},
		{"django 的 orm 怎么用", "go"},
		{"he said hello", "ai"},
	}
	for _, p := range bad {
		if containsCompany(p[0], p[1]) {
			t.Errorf("%q 里不该把 %q 认成公司名（子串误命中）", p[0], p[1])
		}
	}
	good := [][2]string{
		{"coding 一面面经", "coding"}, // 独立词，containsCompany 层面应通过（是不是真公司由通用词表管）
		{"meta 27届秋招", "meta"},
		{"go 语言后端一面", "go"},
		{"字节跳动 后端 一面", "字节跳动"},
		{"momenta后端一面", "momenta"},
		{"opencode 面试", "opencode"},
	}
	for _, p := range good {
		if !containsCompany(p[0], p[1]) {
			t.Errorf("%q 里应认出 %q", p[0], p[1])
		}
	}
}

// TestGuessCompanyRejectsDigits 覆盖「纯数字/日期被当成公司名」。
//
// 真实脏值：「0904 中望 ZW3D 研发 一面」→ 公司被猜成 "0904"（那是日期）；
// 「27秋招Shoppe笔试」→ "27Shoppe"（年份粘到了名字上）。
func TestGuessCompanyRejectsDigits(t *testing.T) {
	// 去掉开头年份后应还原成真名字
	c, _, _ := ParseTitleLoose("27秋招Shoppe笔试", nil)
	if c == "27Shoppe" {
		t.Errorf("年份没剥掉: %q", c)
	}
	// 纯数字的候选一律不采用
	for _, title := range []string{"0904 一面", "2024 笔试", "11.9 笔试"} {
		if c, _, _ := ParseTitleLoose(title, nil); c != "" && !reHasAnyLetter.MatchString(c) {
			t.Errorf("%q 抽出了纯数字公司名 %q", title, c)
		}
	}
	// 种子表里的数字公司名仍要能用（4399 / 360）
	if c, _, _ := ParseTitleLoose("4399 AIAgent 笔试挂", nil); c != "4399" {
		t.Errorf("种子表里的数字公司名 4399 应可用，实际 %q", c)
	}
	if c, _, _ := ParseTitleLoose("360 全栈开发 一面", nil); c != "360" {
		t.Errorf("种子表里的数字公司名 360 应可用，实际 %q", c)
	}
	// 「猜」这条路不能产出通用技术词
	for _, title := range []string{"AI Coding一面", "Agent 平台 一面", "Infra 团队 一面"} {
		if c, _, _ := ParseTitleLoose(title, nil); c != "" {
			t.Errorf("%q 不该猜出公司名，实际 %q", title, c)
		}
	}
}

// TestLearnedLexiconIsFiltered 覆盖「词典自我强化」这个坑。
//
// 词典是从库里学来的，于是**坏名字一旦进库就永远清不掉**：
// 下次回填时它又作为词典命中被应用回来。实测「Coding」「0904」
// 在抽取逻辑修好之后仍赖在库里，就是这个循环造成的。
//
// 所以学来的名字必须过筛：通用技术词不要、纯数字不要（除非在种子表里）。
func TestLearnedLexiconIsFiltered(t *testing.T) {
	idx := newCompanyIndex([]string{"Coding", "coding", "0904", "Agent", "卓驭智驾", "4399"})

	// 学来的坏名字不该被收录
	for _, bad := range []string{"AI Coding一面", "0904 一面"} {
		if c, _, _ := ParseTitleLoose(bad, []string{"Coding", "0904"}); c != "" {
			t.Errorf("%q 从被污染的词库里抽出了公司名 %q", bad, c)
		}
	}
	// 学来的正常名字要能用（用真实公司名做例子：含「公司」二字的会被 reCompanyNoise 拒掉，那是另一条规则）
	if c, _, _ := ParseTitleLoose("卓驭智驾 后端 一面", []string{"卓驭智驾"}); c != "卓驭智驾" {
		t.Errorf("学来的正常公司名应可用，实际 %q", c)
	}
	// 种子表里的数字公司名不受「学来的纯数字」限制
	if c, _, _ := ParseTitleLoose("4399 笔试", []string{"Coding"}); c != "4399" {
		t.Errorf("种子表里的 4399 应可用，实际 %q", c)
	}
	_ = idx
}

func TestRoundFromContent(t *testing.T) {
	cases := []struct{ in, want string }{
		// 标题式行才认
		{"一面\n问了项目", "一面"},
		{"【二面】\n聊了架构", "二面"},
		{"第三轮HR面\n薪资和城市", "HR面"},
		{"第一轮技术面\n八股", "一面"},
		{"终面\n部门负责人", "终面"},
		// 横跨多轮 → 判不了
		{"第一轮技术面\n八股\n第三轮HR面\n薪资", ""},
		{"一面\n问了项目\n二面\n问了架构", ""},
		// 叙述性提及不算（真实事故：八股合辑被整篇标成一面）
		{"写在开头你有没有过这样的面试窘境：结果一面开场 10 分钟，就栽在了基础题上。", ""},
		{"简历写了熟悉 CAN，面试官下一刀通常问什么？很多人一面却卡在第一层。", ""},
		// 标题式行但太长，不算标题
		{"一面的时候面试官问了我很多关于项目的问题并且追问了非常多的细节", ""},
		{"就是聊了聊项目和实习", ""},
		{"", ""},
		// 「另一方面」里没有连续的「一面」
		{"另一方面，我也考虑了稳定性", ""},
	}
	for _, c := range cases {
		if got := RoundFromContent(c.in); got != c.want {
			t.Errorf("RoundFromContent(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

// TestRoundFromContentNeverReturnsAmbiguous 是这条兜底规则的核心不变量：
// 只要正文里出现两种以上轮次标记，就必须留空——错标比不标更糟。
func TestRoundFromContentNeverReturnsAmbiguous(t *testing.T) {
	texts := []string{
		"一面\n八股\n二面\n项目",
		"HR面\n薪资\n终面\n负责人",
		"第一轮技术面\nx\n第二轮技术面\ny",
	}
	for _, s := range texts {
		if got := RoundFromContent(s); got != "" {
			t.Errorf("RoundFromContent(%q) 应为空（多种轮次），实际 %q", s, got)
		}
	}
}
