package scraper

import (
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// 本文件处理「标题格式不标准」的面经帖。
//
// 背景：ParseTitle 只认「公司 岗位 轮次」这种严格格式，那是某个用户时间线的写作习惯。
// 抓全站后发现绝大多数帖子标题是自由写法：
//
//	美团9.12笔试大家aicoding和编程通过多少？
//	Minimax 秋招后端三面
//	momenta后端一面
//	oppo校招有人进二面了吗
//	河北电信某市公司笔试
//
// 这些帖子的 company 全是空，于是「按公司筛选」这一整个维度在全站数据上几乎失效
//（实测 sitemap 帖 198 篇里只有 11 篇解析出公司）。而「我想看某家公司的面经」
// 恰恰是这个站点最核心的用法之一，所以必须补一条宽松的抽取路径。

// ---------- 轮次 ----------

// 全站标题里出现的轮次/阶段词。按「越具体越靠前」排序，取第一个命中。
var roundPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"复活赛 一面", regexp.MustCompile(`复活赛\s*一面`)},
	{"复活赛 二面", regexp.MustCompile(`复活赛\s*二面`)},
	{"HR面", regexp.MustCompile(`(?i)\bHR\s*面|hr面|人力面`)},
	{"群面", regexp.MustCompile(`群面`)},
	{"主管面", regexp.MustCompile(`主管面`)},
	{"交叉面", regexp.MustCompile(`交叉面`)},
	{"终面", regexp.MustCompile(`终面`)},
	{"初面", regexp.MustCompile(`初面`)},
	{"四面", regexp.MustCompile(`四面`)},
	{"三面", regexp.MustCompile(`三面`)},
	{"二面", regexp.MustCompile(`二面`)},
	{"一面", regexp.MustCompile(`一面`)},
	{"笔试", regexp.MustCompile(`笔试|机试|在线编程|编程题`)},
	{"测评", regexp.MustCompile(`测评|性格测试`)},
}

// RoundOf 从标题里认出轮次/阶段，认不出返回空串。
//
// 与 ParseTitle 的区别：不要求标题是标准格式，只要**出现过**这些词就算。
func RoundOf(title string) string {
	for _, p := range roundPatterns {
		if p.re.MatchString(title) {
			return p.name
		}
	}
	return ""
}

// ---------- 岗位 ----------

// 岗位方向词。同样按「越具体越靠前」排序。
var jobPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"算法", regexp.MustCompile(`(?i)算法|机器学习|深度学习|大模型|NLP|CV|推荐|搜索`)},
	{"Agent/AI 应用", regexp.MustCompile(`(?i)agent|AI\s*应用|AI应用|智能体|大模型应用`)},
	{"前端", regexp.MustCompile(`(?i)前端|web前端|客户端|安卓|android|iOS|鸿蒙`)},
	{"测试", regexp.MustCompile(`(?i)测试|测开|QA`)},
	{"数据", regexp.MustCompile(`(?i)数据分析|数据开发|大数据|数仓|数据挖掘`)},
	{"硬件/嵌入式", regexp.MustCompile(`(?i)嵌入式|硬件|机电|机械|电子|驱动|FPGA|芯片|射频|光学`)},
	// 不要用 `TA\b` 表示技术美术：「momenta后端一面」里的 "ta" 会被误命中
	//（Go 的 \b 是 ASCII 语义，"后" 不是 ASCII 词字符，所以 a 后面算边界）。
	{"游戏", regexp.MustCompile(`游戏|引擎|图形|技术美术`)},
	{"运维/安全", regexp.MustCompile(`(?i)运维|SRE|安全|渗透`)},
	{"产品/运营", regexp.MustCompile(`(?i)产品经理|运营|市场|设计`)},
	{"后端", regexp.MustCompile(`(?i)后端|服务端|后台|全栈|Java|Golang|Go\b|C\+\+|Python|开发工程师`)},
}

// JobOf 从标题里认出岗位方向，认不出返回空串。
func JobOf(title string) string {
	for _, p := range jobPatterns {
		if p.re.MatchString(title) {
			return p.name
		}
	}
	return ""
}

// ---------- 公司 ----------

// 常见公司/机构名种子表。
//
// 为什么要有种子表而不是纯启发式：从标题里「猜」公司名几乎必然产生噪声
// （「最恶心的一次面试」里的任何片段都不是公司）。先拿一份高置信度的名字去**匹配**，
// 匹配不上才退回启发式，这样精度可控。
//
// 这张表不需要穷举：cmd/parse 每次运行时会把库里已有的 company 值一起喂进来，
// 所以随着抓取推进会自动扩充（第一期就从用户时间线里学到了 73 个）。
var companySeed = []string{
	// 互联网大厂
	"字节跳动", "字节", "腾讯", "阿里", "阿里巴巴", "淘天", "淘宝", "天猫", "蚂蚁", "支付宝",
	"百度", "京东", "美团", "拼多多", "快手", "滴滴", "网易", "小米", "华为", "荣耀",
	"哔哩哔哩", "B站", "小红书", "微博", "知乎", "豆瓣", "携程", "去哪儿", "飞猪", "同程",
	"得物", "唯品会", "苏宁", "国美", "当当", "蘑菇街", "shopee", "虾皮", "lazada",
	"oppo", "vivo", "一加", "realme", "魅族", "中兴", "联想", "大疆", "insta360",
	// 外企
	"微软", "google", "谷歌", "亚马逊", "amazon", "meta", "apple", "苹果", "英伟达", "nvidia",
	"intel", "英特尔", "amd", "qualcomm", "高通", "三星", "sony", "索尼", "oracle", "甲骨文",
	"ibm", "sap", "vmware", "西门子", "博世", "tesla", "特斯拉", "shopify", "canva",
	// AI / 独角兽
	"minimax", "月之暗面", "moonshot", "智谱", "zhipu", "百川", "零一万物", "阶跃星辰",
	"deepseek", "深度求索", "商汤", "旷视", "云从", "依图", "第四范式", "出门问问",
	"科大讯飞", "讯飞", "寒武纪", "地平线", "小马智行", "pony", "文远知行", "momenta",
	"蔚来", "小鹏", "理想", "比亚迪", "极氪", "零跑", "哪吒", "赛力斯", "奇瑞", "吉利",
	"字节ai",
	// 金融
	"中信", "中金", "华泰", "国泰", "招商银行", "工商银行", "建设银行", "中国银行", "农业银行",
	"平安", "蚂蚁金服", "微众", "度小满", "京东金融", "陆金所", "幻方", "九坤",
	"鸣熙", "明汯", "灵均", "宽德", "世纪前沿", "稳博", "黑翼",
	// 游戏
	"米哈游", "miHoYo", "莉莉丝", "鹰角", "叠纸", "完美世界", "三七互娱", "巨人网络",
	"游族", "游族网络", "盛趣", "心动", "库洛", "趣加", "funplus", "网易游戏", "腾讯游戏",
	// 通信/硬件/国企
	"中国移动", "中国电信", "中国联通", "河北电信", "广东电信", "移动", "电信", "联通",
	"中兴通讯", "新华三", "h3c", "浪潮", "曙光", "中科曙光", "海康", "海康威视", "大华",
	"360", "4399",
	"京东方", "tcl", "创维", "康佳", "格力", "美的", "海尔", "tplink", "普联",
	"中物院", "中电科", "航天", "航空工业", "中船", "兵器", "核工业", "国家电网", "南方电网",
	// 造车/新能源/工业
	"宁德时代", "catl", "隆基", "阳光电源", "汇川", "禾赛", "速腾聚创", "图达通",
	"新凯来", "中微", "北方华创", "长鑫", "长江存储", "韦尔", "兆易",
	// 工具/软件/服务
	"金山", "wps", "金山办公", "用友", "金蝶", "广联达", "恒生", "同花顺", "东方财富",
	"神策", "神策数据", "涂鸦", "涂鸦智能", "柏楚", "柏楚电子", "中控", "汇顶",
	"thoughtworks", "埃森哲", "德勤", "普华永道", "安永", "毕马威",
}

// companyIndex 是按长度降序排好的 (小写名 → 原名) 列表，用于最长匹配。
type companyIndex struct {
	names []string // 原名，按长度降序
}

func newCompanyIndex(extra []string) *companyIndex {
	seen := map[string]bool{}
	var all []string
	// 种子表原样收录——它本身就是可信来源，4399 / 360 这类纯数字公司名也在里面。
	for _, n := range companySeed {
		n = strings.TrimSpace(n)
		if n == "" || seen[strings.ToLower(n)] {
			continue
		}
		seen[strings.ToLower(n)] = true
		all = append(all, n)
	}
	// 从库里学来的名字要过一道筛。
	//
	// ⚠️ 这一步是必须的：词典是**自我强化**的——一旦某个坏名字进了库，
	// 下次回填时它又会被当作词典命中，于是永远清不掉。
	// 实测「Coding」「0904」在修好抽取逻辑之后仍然赖在库里，就是因为这个循环。
	for _, n := range extra {
		n = strings.TrimSpace(n)
		key := strings.ToLower(n)
		if n == "" || seen[key] {
			continue
		}
		if isGenericCompanyWord(n) {
			continue // 通用技术词不是公司名
		}
		if !reHasAnyLetter.MatchString(n) {
			continue // 纯数字（多半是日期）：只有种子表里的才认
		}
		seen[key] = true
		all = append(all, n)
	}
	// 长名优先：先匹配「哔哩哔哩」再匹配「B站」这类重叠
	sort.Slice(all, func(i, j int) bool { return len(all[i]) > len(all[j]) })
	return &companyIndex{names: all}
}

// find 在标题里找最长命中的公司名。返回原名与是否命中。
//
// 命中后仍要过 looksLikeCompany：词典可能被污染。
// 真实事故：一个单字「招」混进词典后，`strings.Contains` 让**所有含「招」的标题**
// （秋招/校招/招聘/招银…）全部被认成同一家公司，79 篇帖子被贴错。
func (c *companyIndex) find(title string) (string, bool) {
	low := strings.ToLower(title)
	for _, n := range c.names {
		if !looksLikeCompany(n) {
			continue
		}
		if containsCompany(low, strings.ToLower(n)) {
			return n, true
		}
	}
	return "", false
}

// containsCompany 判断公司名是否作为一个**独立的词**出现在标题里。
//
// 中文没有词边界，直接 Contains 就行；但纯 ASCII 的名字必须卡边界，
// 否则短名字会在长单词里被误命中——实测「AI Coding经验分享」被认成
// 一家叫 Coding 的公司（7 篇帖子），因为 "coding" 是 "aicoding" 的子串。
// 同理 "meta" 会命中 "metadata"、"go" 会命中 "google"。
func containsCompany(lowTitle, lowName string) bool {
	idx := strings.Index(lowTitle, lowName)
	if idx < 0 {
		return false
	}
	if !isASCIIWord(lowName) {
		return true
	}
	// 名字两侧不能紧挨着另一个字母或数字
	if idx > 0 && isWordByte(lowTitle[idx-1]) {
		return false
	}
	if end := idx + len(lowName); end < len(lowTitle) && isWordByte(lowTitle[end]) {
		return false
	}
	return true
}

// isASCIIWord 判断名字是否完全由 ASCII 字母数字构成（不含中文）。
func isASCIIWord(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// reCompanyNoise 判定「这不像公司名」。
var reCompanyNoise = regexp.MustCompile(`面试|笔试|机试|面经|测评|秋招|春招|校招|社招|实习|暑期|提前批|正式批|内推|offer|Offer|一面|二面|三面|四面|hr|HR|招聘|岗位|简历|大厂|小厂|小公司|某公司|公司|面试官|后续|分享|记录|总结|经验|流程|收到|有人|怎么|如何|为什么|挂了|凉了|求助|请问|大家`)

// looksLikeCompany 判断一个候选片段像不像公司名。
//
// 刻意保守：宁可漏掉（company 留空，只是少一个筛选维度），也不要填错
// （填错会让人按公司筛出完全无关的题）。
func looksLikeCompany(s string) bool {
	s = strings.TrimSpace(s)
	r := []rune(s)
	// 单字不是公司名。这一条挡的是「招」「中」「国」这类会在长标题里
	// 到处被 strings.Contains 命中的碎片。
	if len(r) < 2 || len(r) > 14 {
		return false
	}
	if reCompanyNoise.MatchString(s) {
		return false
	}
	// 公司名里不会出现句子标点，也不会出现 / _ | \ 这类分隔符。
	// 实测漏进库的脏值：「学历给我开了门，也给我挖了坑」（含逗号）、
	// 「文案/演出/任务策划解惑贴」（含斜杠）、「泰凌微电子_嵌软_一/」（含下划线与斜杠）。
	// 这三个都通过了噪声词检查——它们不含「面试/笔试」之类的词，
	// 只是「不像公司名」而已，所以要单独按字符形态挡一道。
	if reCompanyPunct.MatchString(s) {
		return false
	}
	// 至少要有汉字、字母或数字——纯符号不是公司名。
	// 数字名也要放行：「360」「4399」都是真实公司。
	hasWord := false
	for _, c := range r {
		if ('\u4e00' <= c && c <= '\u9fff') || ('a' <= c && c <= 'z') ||
			('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') {
			hasWord = true
			break
		}
	}
	return hasWord
}

// reSentencePunct 是句子标点。出现在「公司名」候选里说明它不是公司名，而是闲聊。
var reSentencePunct = regexp.MustCompile(`[，。！？；、,.!?;~～…]|了$|吗$|吧$|呢$`)

var (
	reLeadDigitRun = regexp.MustCompile(`^\d{2,4}(?:\.\d{1,2})?`)
	reHasAnyLetter = regexp.MustCompile(`[A-Za-z\p{Han}]`)
)

// reCompanyPunct 是绝不该出现在公司名里的字符：句子标点与路径分隔符。
var reCompanyPunct = regexp.MustCompile(`[，。！？；：、,.!?;:/\\|_（）()【】\[\]{}<>《》""'']`)

// reNonCompanyLead 是标题开头常见但绝不是公司名的前缀。
var reNonCompanyLead = regexp.MustCompile(`^(面试|笔试|面经|秋招|春招|校招|社招|暑期|提前批|正式批|今天|昨天|分享|记录|总结|请教|求助|终于|第一次|刚|昨天|关于|聊聊)`)

// guessCompany 从「轮次词之前的片段」里猜公司名。
//
// 例：「momenta后端一面」→ 取「momenta后端」→ 剥掉岗位词「后端」→「momenta」
//
//	「Minimax 秋招后端三面」→「Minimax 秋招后端」→ 剥掉「秋招/后端」→「Minimax」
//
// shouldGuess 为 false 时只走词典匹配、不做「猜」。
func guessCompany(title string, known *companyIndex, shouldGuess bool) string {
	if c, ok := known.find(title); ok {
		return c
	}
	if !shouldGuess {
		return ""
	}
	// 截到第一个轮次/阶段词之前
	cut := len(title)
	for _, p := range roundPatterns {
		if loc := p.re.FindStringIndex(title); loc != nil && loc[0] < cut {
			cut = loc[0]
		}
	}
	head := strings.TrimSpace(title[:cut])
	head = strings.Trim(head, " |·-—:：,，.。[]【】()（）")
	if head == "" || reNonCompanyLead.MatchString(head) {
		return ""
	}
	// 「轮次词之前那段」应该短而干净——公司名不会是一整句话。
	// 实测放进来的是「末2三无选手，秋招还有救吗」这种整体闲聊标题。
	if len([]rune(head)) > 12 || reSentencePunct.MatchString(head) {
		return ""
	}
	// 剥掉混在前面的批次/岗位/角色词。
	// 顺序有讲究：长词先剥，否则「软件工程师」会被「工程师」先吃掉一半。
	for _, w := range []string{
		"暑期实习", "暑期", "秋招", "春招", "校招", "社招", "提前批", "正式批", "实习",
		"后端开发", "服务端开发", "前端开发", "全栈开发", "软件开发", "平台开发", "中台开发",
		"算法工程师", "开发工程师", "软件工程师", "测试工程师", "研发工程师",
		"测试开发", "测开", "开发岗", "工程师", "研发", "平台", "中台",
		"后端", "服务端", "前端", "全栈", "客户端", "测试", "算法", "数据",
		"产品", "运营", "设计", "运维", "安全", "硬件", "嵌入式", "游戏", "引擎", "智能",
		"岗位", "方向", "团队", "部门", "Agent", "agent", "AI", "ai",
		"Java", "java", "Golang", "golang", "Go", "go", "Python", "python", "C++", "c++",
	} {
		head = strings.ReplaceAll(head, w, " ")
	}
	head = strings.Join(strings.Fields(head), "")
	// 剥离会在中间留下标点（「卓驭秋招｜硬件工程师」→「卓驭｜」），再修一次首尾
	head = strings.Trim(head, " |·-—:：,，.。[]【】()（）!！?？~～")
	// 去掉开头的年份/日期：「27秋招Shoppe笔试」剥完是「27Shoppe」，
	// 前两位是年份不是公司名的一部分；「0904 中望 ZW3D 研发 一面」同理。
	head = reLeadDigitRun.ReplaceAllString(head, "")
	// 纯数字不是公司名（「0904」曾被当成公司）。4399 这类真·数字公司
	// 在种子表里，走词典匹配那条路，不受这条限制。
	if head != "" && !reHasAnyLetter.MatchString(head) {
		return ""
	}
	// 剥完只剩一个通用词，说明原本就不是「公司 + 岗位」结构，宁可留空
	if genericWords[head] {
		return ""
	}
	if isGenericCompanyWord(head) {
		return ""
	}
	// 走「猜」这条路时要求至少 3 个字。
	//
	// 理由：这个维度**精度比召回重要**——填错了会让人按公司筛出完全无关的题，
	// 而漏掉只是少一个筛选入口。而两字中文片段里噪声太多
	//（「可免笔试，PDD等你」→「可免」；「国际 二面」→「国际」）。
	// 真正的两字公司（蚂蚁/滴滴/京东/美团/携程）都在种子表或学到的词典里，
	// 走 `known.find` 那条路，不受这条限制。
	if len([]rune(head)) < 3 {
		return ""
	}
	if looksLikeCompany(head) {
		return head
	}
	return ""
}

// genericCompanyWords 是「猜」这条路要排除的通用技术词。
//
// 这些词在标题里普遍是**技术名词**而不是公司名：
// 「AI Coding一面」剥完剩「Coding」，于是库里长出一家叫 Coding 的公司（7 篇帖子）。
// 它们仍可能作为词典命中而生效（比如真有公司叫这个），只是不从猜测里产生——
// 猜测本来就容易把技术词当公司名，而**一旦进了库就会被词典传播放大**。
var genericCompanyWords = map[string]bool{
	"coding": true, "code": true, "agent": true, "ai": true, "llm": true, "rag": true,
	"infra": true, "platform": true, "backend": true, "frontend": true, "fullstack": true,
	"devops": true, "data": true, "cloud": true, "test": true, "sre": true, "dev": true,
	"ops": true, "api": true, "sdk": true, "ios": true, "android": true, "web": true,
	"app": true, "demo": true, "project": true, "intern": true, "campus": true,
	"算法": true, "开发": true, "后端": true, "前端": true, "客户端": true, "测试": true,
	"运维": true, "数据": true, "平台": true, "架构": true, "安全": true, "游戏": true,
}

// isGenericCompanyWord 判断一个候选是不是通用技术词。
func isGenericCompanyWord(s string) bool {
	return genericCompanyWords[strings.ToLower(strings.TrimSpace(s))]
}

// genericWords 是剥完剥离词后**仍然**不该被当作公司名的残留。
//
// 例：「游戏智能测试工程师笔试题求经验」剥完只剩「智能」，
// 「诺瓦软件工程师二面」剥完是「诺瓦」（好），但「某公司软件工程师」剥完是「某公司」（坏）。
var genericWords = map[string]bool{
	"智能": true, "技术": true, "科技": true, "软件": true, "信息": true, "网络": true,
	"系统": true, "平台": true, "中台": true, "中心": true, "事业部": true, "部门": true,
	"团队": true, "小组": true, "公司": true, "某公司": true, "大厂": true, "小厂": true,
	"小公司": true, "工程师": true, "开发": true, "研发": true, "岗位": true, "方向": true,
	"面试": true, "笔试": true, "面经": true, "秋招": true, "春招": true, "校招": true,
}

// ParseTitleLoose 是 ParseTitle 的宽松版：标准格式解析不出来时尽力给出公司/岗位/轮次。
//
// extraCompanies 由调用方从库里已有的 company 值传入，使词典随抓取自动扩充。
func ParseTitleLoose(title string, extraCompanies []string) (company, job, round string) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", "", ""
	}
	// 标准格式优先：它对「公司 岗位 轮次」的切分最准。
	// 但**切出来的公司名仍要过校验**——严格格式只保证「有三个字段」，
	// 不保证第一个字段就是公司（「招 银 一面」这种切分是合法的，但「招」不是公司）。
	if c, j, r := ParseTitle(title); c != "" || r != "" {
		// 严格格式只保证「有三个字段」，不保证第一个字段就是公司。
		// 纯数字尤其可疑：「0904 中望 ZW3D 研发 一面」里的 0904 是**日期**。
		// （真·数字公司 360/4399 在种子表里，走词典匹配，不受这条影响。）
		if c != "" && (!looksLikeCompany(c) || !reHasAnyLetter.MatchString(c) ||
			isGenericCompanyWord(c)) {
			c = "" // 不可信，交给下面的宽松路径重新判断
		}
		if c == "" {
			c = guessCompany(title, newCompanyIndex(extraCompanies), r != "" || RoundOf(title) != "")
		}
		if r == "" {
			r = RoundOf(title)
		}
		if j == "" {
			j = JobOf(title)
		}
		return c, j, r
	}
	round = RoundOf(title)
	job = JobOf(title)
	// 「猜」只在标题里**确实有轮次/阶段词**时才做。
	//
	// 理由：这条路子的原理是「轮次词之前那段是公司」，所以没有轮次词就没有依据，
	// 整条标题都会被当成候选——实测这样产出的是「2027年开始了」「#27进度」
	// 这种纯粹的噪声。没有轮次词时留给词典匹配就够了。
	company = guessCompany(title, newCompanyIndex(extraCompanies), round != "")
	return company, job, round
}

// 正文兜底识轮次时，只认「标题式行」：整行很短、且以轮次词开头。
//
// 绝不能在正文里任意位置搜轮次词——实测那样会把一批「八股合辑」整篇标错：
// 《嵌入式CAN总线必备八股文总结》正文里只有一句叙述「一面却卡在第一层」，
// 整篇 40 道通用八股就被算进了「一面」这个筛选项。
// 收紧成标题式行后，同一批数据从 129 篇 / 1,544 题收敛到 20 篇 / 279 题，
// 被剔掉的 122 篇正是这类叙述性提及。
var reRoundHeadingLead = regexp.MustCompile(`^[\s#*\-—–·>【\[（(》）)\]:：、.．0-9]*`)

// roundHeadingMaxRunes 是「标题式行」的长度上限。
const roundHeadingMaxRunes = 24

// roundHeadingRules 把标题式行归一成「轮次键」。
//
// 比较用键而不是名字，是为了识别「横跨多轮」：正文同时出现
// 「第一轮技术面」与「第三轮HR面」的帖子，键是 {1,hr}，
// 单看正文判断不了某道题属于哪一轮，必须留空。
var roundHeadingRules = []struct {
	key  string
	name string
	re   *regexp.Regexp
}{
	// HR 不要求出现在行首：「第三轮HR面」也是 HR 面，用非锚定匹配优先命中
	{"hr", "HR面", regexp.MustCompile(`(?i)HR\s*面|hr面|人力面`)},
	{"5", "终面", regexp.MustCompile(`^终面`)},
	{"4", "四面", regexp.MustCompile(`^(第\s*[四4]\s*(轮|面)|四面)`)},
	{"3", "三面", regexp.MustCompile(`^(第\s*[三3]\s*(轮|面)|三面)`)},
	{"2", "二面", regexp.MustCompile(`^(第\s*[二2两]\s*(轮|面)|二面)`)},
	{"1", "一面", regexp.MustCompile(`^(第\s*[一1]\s*(轮|面)|一面)`)},
}

// RoundFromContent 在标题没给出轮次时，从正文的标题式行兜底推断。
//
// 只有**恰好一种**轮次键出现才返回；出现两种以上（帖子横跨多轮）
// 或一种都没有，都返回 ""——错标比留空更糟。
func RoundFromContent(content string) string {
	name, seen := "", ""
	for _, line := range strings.Split(content, "\n") {
		l := strings.TrimSpace(reRoundHeadingLead.ReplaceAllString(line, ""))
		if l == "" || utf8.RuneCountInString(l) > roundHeadingMaxRunes {
			continue
		}
		for _, r := range roundHeadingRules {
			if r.re.MatchString(l) {
				if seen != "" && seen != r.key {
					return ""
				}
				seen, name = r.key, r.name
				break
			}
		}
	}
	return name
}
