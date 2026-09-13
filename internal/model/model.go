// Package model 定义领域对象。
package model

// Post 是一篇牛客动态（面试记录的原始来源）。
type Post struct {
	ID           int64  `json:"id"`         // 牛客 contentId
	UUID         string `json:"uuid"`       // 牛客 moment uuid
	Title        string `json:"title"`      //
	URL          string `json:"url"`        // 原文链接 /feed/main/detail/<uuid>
	Company      string `json:"company"`    // 从标题解析
	Job          string `json:"job"`        // 原始岗位串
	JobGroup     string `json:"jobGroup"`   // 归组后的岗位（筛选用）
	Round        string `json:"round"`      // 原始轮次
	RoundGroup   string `json:"roundGroup"` // 归组后的轮次（筛选用）
	Content      string `json:"content"`    // 正文原文
	PostedAt     int64  `json:"postedAt"`
	PostedDate   string `json:"postedDate"`
	InterviewExp string `json:"interviewExp"` // 牛客标注，如「查看18道真题和解析」
	ScrapedAt    int64  `json:"scrapedAt"`
	// 多来源抓取新增（见 migration 003）
	AuthorID   int64  `json:"authorId"`
	AuthorName string `json:"authorName"`
	Source     string `json:"source"` // user | sitemap
	EntityType int    `json:"entityType"`
	LikeCnt    int    `json:"likeCnt"`
	CommentCnt int    `json:"commentCnt"`
	ViewCnt    int    `json:"viewCnt"`
	TopicTag   string `json:"topicTag"`
	// OfficialQ 是牛客官方结构化真题列表的 JSON（[{id,title,answer}]），可能为空。
	// 存 JSON 而不是建表：它只被 importer 读一次用来生成题目，不需要单独查询。
	// 不导出到 API（json:"-"）：列表接口返回全文会让响应体积爆炸。
	OfficialQ string `json:"-"`
}

// Question 是一个去重后的面试问题（由若干措辞变体与出现记录聚合而成）。
// 列宽上限（见 migration 001/005）。
//
// 放在 model 而不是 importer：这是**数据库 schema 的属性**，
// 所有写入路径都得遵守。早先只有 importer.Cluster 做截断，
// 于是任何别的调用方（或以后新增的导入路径）都能写出 Error 1406，
// 而集成测试正是这样把它抓出来的。
const (
	// MaxCanonRunes 对应 questions.canonical VARCHAR(1024)。
	MaxCanonRunes = 1000
	// MaxNormRunes 对应 questions.norm VARCHAR(768)。
	// utf8mb4 下 InnoDB 单列索引上限是 3072 字节 = 768 字符，留一点余量取 700。
	MaxNormRunes = 700
)

// ClipRunes 按**字符**截断，避免多字节字符被切成半个。
func ClipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

type Question struct {
	ID        int64  `json:"id"`
	Canonical string `json:"canonical"` // 代表措辞
	Norm      string `json:"-"`         // 归一化文本，参与聚类
	Category  string `json:"category"`  // 规则分类：八股/基础 | Agent/AI | 项目 | 算法 | HR/软性 …
	Topic     string `json:"topic"`     // 规则主题，如「Go 语言」
	N         int    `json:"n"`         // 出现次数
	// CoN 是「被多少家**不同公司**问过」。与 N 互补：N 会把一家公司刷很多帖
	// 算成高频，CoN 不会，所以它更适合做复习优先级。
	CoN int `json:"coN"`
	// LLM 归类（三层）
	LLMMerged string   `json:"llmMerged,omitempty"`
	LLMDomain string   `json:"llmDomain,omitempty"`
	LLMCat    string   `json:"llmCat,omitempty"`
	Variants  []string `json:"variants,omitempty"`
	// VariantN 是「其他措辞」的总数。列表接口只带前 variantPreviewLimit 条
	//（同义题合并后一条题可能有几十种写法，全带会把响应撑大），
	// VariantN 让前端知道还有多少没展示；详情接口返回全部。
	VariantN int `json:"variantN,omitempty"`
	// Answer 是参考答案，只有来自官方结构化真题列表的题目才有。
	//
	// ⚠️ 列表接口里这个是**截断后**的预览（见 store.answerPreviewRunes）；
	// 完整答案用 /api/questions/{id} 取。AnswerLen 是原始长度，
	// 前端据此判断「是否被截断」并决定要不要懒加载。
	Answer string `json:"answer,omitempty"`
	// AnswerLen 是答案的原始字符数（未截断）。
	AnswerLen int `json:"answerLen,omitempty"`
	// OfficialN 是该题在官方结构化真题里出现的次数（总次数 N 含它）。
	OfficialN int `json:"officialN,omitempty"`
	// ⚠️ 字段名是 API 契约的一部分：前端按 occurrences 读取并渲染「原文 ↗」链接。
	// 改名会让链接静默消失（前端的 `|| []` 兜底不会报错），改动前务必同步 web/app.js。
	Occurs []Occur `json:"occurrences,omitempty"`
}

// Occur 是「某问题在某篇帖子里出现」的一次记录，带原文链接。
type Occur struct {
	PostID     int64  `json:"postId"`
	RawText    string `json:"rawText"`
	Company    string `json:"company"`
	Job        string `json:"job"`
	JobGroup   string `json:"jobGroup"`
	Round      string `json:"round"`
	RoundGroup string `json:"roundGroup"`
	// Official 标记这条出现记录来自牛客官方结构化真题（而非正文正则抽取）。
	// 前端据此区分「本人回忆」与「官方整理」，也便于后续按来源做质量加权。
	Official bool   `json:"official,omitempty"`
	Date     string `json:"date"`
	Title    string `json:"title"`
	URL      string `json:"url"` // ← 指向原始帖子
}

// Facet 是筛选维度的一个取值及其计数。
type Facet struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// Facets 汇总所有可筛选维度的取值。
type Facets struct {
	Rounds     []Facet `json:"rounds"`
	Jobs       []Facet `json:"jobs"`
	Companies  []Facet `json:"companies"`
	Categories []Facet `json:"categories"`
	Topics     []Facet `json:"topics"`
	// LLM 归类维度（三层：合并类 / 顶层域 / 细分类）
	LLMMerged []Facet `json:"llmMerged"`
	LLMDomain []Facet `json:"llmDomain"`
	LLMCat    []Facet `json:"llmCat"`
}

// TaxonomyEntry 是 LLM 分类体系的一条：细分类 → 顶层域 → 合并后的可用类。
type TaxonomyEntry struct {
	Name        string `json:"name"`
	Domain      string `json:"domain"`
	Merged      string `json:"merged"`
	Description string `json:"description"`
}

// Meta 是概览统计。
type Meta struct {
	Posts      int `json:"posts"`
	Interviews int `json:"interviews"`
	Companies  int `json:"companies"`
	RawCount   int `json:"rawCount"`  // 原始问题行数
	Questions  int `json:"questions"` // 聚类后组数
	Occurrence int `json:"occurrences"`
	WithAnswer int `json:"withAnswer"`
	// 首页副标题用的三个数，必须**互相自洽**，也要和列表默认展示的条数一致：
	//   RealQuestions  = 已归类且非「非题目」  ← 列表默认就是这个集合
	//   NoiseQuestions = 被判为「非题目」
	//   Unlabeled      = 还没判定的（归结落后才会有值）
	// 早先 RealQuestions 把未归类的也算进去，副标题因此比列表多出一截、对不上。
	RealQuestions  int    `json:"realQuestions"`
	NoiseQuestions int    `json:"noiseQuestions"`
	Unlabeled      int    `json:"unlabeled"`
	DateFrom       string `json:"dateFrom"`
	DateTo         string `json:"dateTo"`
	// GlobalFrom / GlobalTo 是**不受时间筛选影响**的全库日期范围。
	//
	// 为什么需要：DateFrom/DateTo 会跟着筛选变，而时间栏的 min/max 与「可选范围」
	// 提示必须永远是全库的——否则筛过一次之后，输入框的可选范围被收窄到刚筛的
	// 那一段，用户就再也选不回更宽的范围了。
	GlobalFrom string `json:"globalFrom"`
	GlobalTo   string `json:"globalTo"`
}

// Filter 是问题列表的查询条件。空字符串表示不筛。
type Filter struct {
	Round    string
	Job      string
	Company  string
	Category string
	Topic    string
	// LLM 归类维度
	LLMMerged string
	LLMDomain string
	LLMCat    string
	Keyword   string
	// From / To 是「发布时间」闭区间（"YYYY-MM-DD"），空表示不限。
	// 全局筛选：列表、各维度计数、矩阵、原文列表都受它约束。
	From string
	To   string
	// Sort 决定排序："" 按频次（默认），"co" 按公司覆盖面，"priority" 按复习优先级。
	Sort string
	// HasAnswer 只保留带参考答案的题目（答案来自官方结构化真题）。
	// 这条筛选的价值在于：有答案 ≈ 官方整理过 ≈ 质量更高，
	// 是「我想系统刷题」和「我只想看看都问什么」之间的分界线。
	HasAnswer bool
	// OfficialOnly 只保留出现在官方结构化真题里的题目。
	OfficialOnly bool
	// IncludeNoise 把 LLM 判定为「非题目」的条目也列出来。
	//
	// 默认**不列**：实测规则过滤之后仍有一批漏网噪声（面后感、笔试记录、
	// 话题标签行、内推广告），模型判得比规则准——3038 条题里有 489 条（16%）。
	// 留在默认列表里会明显稀释题库的可用性，但它们也不是毫无价值
	//（比如「第二题：滑动窗口 100%」记录了考了什么），所以给一个开关而不是删掉。
	IncludeNoise bool
	// OnlyLabeled 只保留已经过 LLM 判定的题目。
	//
	// 存在的理由：模型判定是「非题目」的唯一权威来源，而未判定的题
	//（llm_cat=''）默认是**会显示**的——一旦归类跟不上抓取，
	// 列表里就会混进大量未过滤的噪声。这个开关让「展示已核验的题库」成为默认。
	//
	// 注意不能无条件打开：全新部署时还没跑归类，那样会一条都不显示。
	// 所以由 API 层先用 HasLabels 探一下库里到底有没有标注。
	OnlyLabeled bool
	Page        int
	Size        int
}

// Matrix 是「公司 × 知识点」的交叉计数，用来回答
// 「某家公司特别爱问哪个方向」这类只看单维度看不出来的问题。
//
// Cells[i][j] = 公司 Companies[i] 在知识点 Cats[j] 上的题目数（按问题簇去重，
// 同一道题被同一家公司问 5 次只算 1）。行列都按总量降序取前 N，避免铺成几百列。
type Matrix struct {
	Companies []string `json:"companies"`
	Cats      []string `json:"cats"`
	Cells     [][]int  `json:"cells"`
	RowTotal  []int    `json:"rowTotal"`
	ColTotal  []int    `json:"colTotal"`
}
