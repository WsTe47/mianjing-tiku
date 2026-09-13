package scraper

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ExperienceQuestion 是牛客官方标注的「真题」条目。
//
// 它出现在 ssrCommonData.experienceQuestionList 里，是**结构化数据**：
// 一篇面经被整理成 {题目, 参考答案} 的列表。
//
// 为什么单独抽出来：实测 4.2% 的帖子带这个列表，平均每帖 11.1 条，
// 且 100% 附参考答案。按全站 26,623 条推算约 1,100 篇帖子、1.2 万条带答案的真题——
// 相比用正则从正文里抠题（有噪声、无答案），这是质量高一个量级的数据源。
type ExperienceQuestion struct {
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	Answer string `json:"answer,omitempty"`
}

// SitePost 是从一页内容详情里提取出的帖子。
type SitePost struct {
	ID         int64
	UUID       string
	Title      string
	Content    string // 已去 HTML 的纯文本（保留换行）
	RawHTML    string // 原始正文 HTML（保留，便于以后重解析）
	AuthorID   int64
	AuthorName string
	CreatedAt  int64
	IPLocation string
	EntityType int
	LikeCnt    int
	CommentCnt int
	ViewCnt    int
	TopicTag   string
	// ExperienceQuestions 是官方结构化真题列表（可能为空）。
	ExperienceQuestions []ExperienceQuestion
}

var reTagStrip = regexp.MustCompile(`<[^>]+>`)
var reTopicLink = regexp.MustCompile(`<a href="/creation/subject/[0-9a-f]+"[^>]*>([^<]{1,40})</a>`)
var reBr = regexp.MustCompile(`(?i)<br\s*/?>|</p>|</div>`)

// stateMarker 是 SSR 数据在 HTML 里的起点。
var stateMarker = []byte("window.__INITIAL_STATE__=")

// extractStateJSON 从 HTML 里切出 window.__INITIAL_STATE__ 的 JSON 文本。
//
// 不能用正则 `(\{.*?\});`：那个写法会在**字符串内部**出现的 `};` 处提前收尾。
// 而牛客的面经正文常常内嵌代码（`return cache.get(key);\n        }`），
// 于是受影响的恰恰是最有价值的算法/手撕类帖子——实测 66 条里有 1 条
// 因此解析失败（unexpected end of JSON input），且从报错完全看不出原因。
//
// 正确做法是花括号配对扫描，并跳过字符串字面量（含反斜杠转义）。
func extractStateJSON(html []byte) ([]byte, error) {
	i := bytes.Index(html, stateMarker)
	if i < 0 {
		return nil, fmt.Errorf("页面里没有 __INITIAL_STATE__（可能被 WAF 拦截或页面结构变了）")
	}
	start := i + len(stateMarker)
	if start >= len(html) || html[start] != '{' {
		return nil, fmt.Errorf("__INITIAL_STATE__ 后面不是 JSON 对象")
	}

	depth := 0
	inStr := false
	esc := false
	for p := start; p < len(html); p++ {
		c := html[p]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return html[start : p+1], nil
			}
		}
	}
	return nil, fmt.Errorf("__INITIAL_STATE__ 的 JSON 在页面结束前没有闭合（响应可能被截断）")
}

// ExtractContent 从 HTML 里取 __INITIAL_STATE__ → ssrCommonData.contentData。
//
// 这是全站内容页的通用结构（/feed/main/detail/<uuid> 与 /discuss/<id> 都一样）。
func ExtractContent(htmlBytes []byte) (*SitePost, error) {
	state, err := extractStateJSON(htmlBytes)
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if err := json.Unmarshal(state, &root); err != nil {
		return nil, fmt.Errorf("解析 __INITIAL_STATE__ 失败: %w", err)
	}
	cd := digMap(root, "prefetchData", "2", "ssrCommonData", "contentData")
	if cd == nil {
		return nil, fmt.Errorf("没有 contentData（不是内容详情页）")
	}

	p := &SitePost{
		ID:         int64Of(cd["id"]),
		UUID:       strOf(cd["uuid"]),
		Title:      strings.TrimSpace(strOf(cd["title"])),
		RawHTML:    strOf(cd["content"]),
		// 发布时间的字段名**跟实体类型有关**。实测 400 篇抽样：
		//   entityType 74（动态 /feed/main/detail/…）→ createdAt
		//   entityType 8 / 73（讨论、题解 /discuss/…）→ createTime
		// 两类都另有 editTime（最后编辑时间）。
		//
		// 以前只读 createdAt，于是 /discuss/ 那批（36%）全部落到 editTime 兜底上，
		// 存的是**最后编辑时间**而不是发布时间——被编辑过的帖子日期会偏晚，
		// 而按时间筛选恰恰会被这种偏差带偏。
		CreatedAt:  firstNonZero(int64Of(cd["createdAt"]), int64Of(cd["createTime"])),
		IPLocation: strOf(cd["ip4Location"]),
		EntityType: int(int64Of(cd["entityType"])),
	}
	// 兜底：确实没有创建时间才用 editTime。它只会偏晚不会偏早，总好过落成 1970。
	if p.CreatedAt == 0 {
		p.CreatedAt = int64Of(cd["editTime"])
	}
	p.AuthorID = int64Of(digMap(cd, "userBrief")["userId"])
	p.AuthorName = strOf(digMap(cd, "userBrief")["nickname"])
	fd := digMap(cd, "frequencyData")
	p.LikeCnt = int(int64Of(fd["likeCnt"]))
	p.CommentCnt = int(int64Of(fd["totalCommentCnt"]))
	p.ViewCnt = int(int64Of(fd["viewCnt"]))

	if tm := reTopicLink.FindStringSubmatch(p.RawHTML); tm != nil {
		p.TopicTag = strings.TrimSpace(tm[1])
	}
	p.Content = HTMLToText(p.RawHTML)
	p.ExperienceQuestions = extractExperienceQuestions(root)
	return p, nil
}

// extractExperienceQuestions 取 ssrCommonData.experienceQuestionList。
//
// 注意它在 ssrCommonData 下、与 contentData 平级（不是 contentData 的字段）。
// 列表里的每条形如 {id, title, answer, entityType}；answer 是 Markdown 文本，
// 开头常带「- 正确答案：」这类前缀。
func extractExperienceQuestions(root map[string]any) []ExperienceQuestion {
	ssr := digMap(root, "prefetchData", "2", "ssrCommonData")
	if ssr == nil {
		return nil
	}
	list, ok := ssr["experienceQuestionList"].([]any)
	if !ok || len(list) == 0 {
		return nil
	}
	out := make([]ExperienceQuestion, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		title := strings.TrimSpace(strOf(m["title"]))
		if title == "" {
			continue // 没有题目文本的条目没有价值
		}
		out = append(out, ExperienceQuestion{
			ID:     int64Of(m["id"]),
			Title:  title,
			Answer: strings.TrimSpace(strOf(m["answer"])),
		})
	}
	return out
}

// HTMLToText 把正文 HTML 转成纯文本：块级标签转换行，其余标签剥掉，并解实体。
func HTMLToText(h string) string {
	h = reBr.ReplaceAllString(h, "\n")
	h = reTagStrip.ReplaceAllString(h, "")
	h = strings.ReplaceAll(h, "&nbsp;", " ")
	h = strings.ReplaceAll(h, "&amp;", "&")
	h = strings.ReplaceAll(h, "&lt;", "<")
	h = strings.ReplaceAll(h, "&gt;", ">")
	h = strings.ReplaceAll(h, "&quot;", `"`)
	h = strings.ReplaceAll(h, "&#39;", "'")
	// 折叠连续空行
	lines := strings.Split(h, "\n")
	var out []string
	blank := 0
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// ---------- 粗筛：这条内容像不像面经 ----------

var (
	// 面试相关信号词。命中标题即高置信；只命中正文需要达到一定数量。
	reKwInterview = regexp.MustCompile(`面经|面试|一面|二面|三面|四面|终面|初面|复试|群面|交叉面|主管面|技术面|HR面|hr面|笔试|机试|秋招|春招|校招|社招|实习|offer|Offer|OFFER|意向书|开奖|泡池子`)
	// 明确的非面经：题解、内推广告、招聘启事、资料分享
	reKwExcludeTitle = regexp.MustCompile(`^题解|^【?题解|^内推$|^招聘|^岗位汇总|^面经汇总$|^资料|^面经模板`)
	// 招聘/内推广告的强信号。这类帖常常因为标题里有「秋招/校招」而骗过关键词命中。
	// 注意这里写的是裸的 `内推` 而不是只写 `内推码`。
	//
	// 曾经只列了 `内推码`，于是「华为27届校招内推」「阿里27届校招内推开始啦！」
	// 这类**不带「码」字的内推广告**全部漏过粗筛（它们的标题里有内推、正文里有投递链接，
	// 但一个强广告词都没命中）。实测补上裸 `内推` 后会新增命中 67 篇，
	// 其中只有 5 篇真有有效题——精确率约 92%，而漏掉的那 5 篇还会被
	// 「面试信号 < 2」这道闸再挡一次。
	reKwAdStrong = regexp.MustCompile(`内推|内推码|招聘详情|热招岗位|职类|投递CD|简历直给|扫码投递|帮你内推|可内推|内推群|招聘启事|网申|宣讲会|投递链接|简历优先筛选|锁定[Oo]ffer|招聘对象|热招|投递传送门|传送门`)
	// 面经的强信号。注意不要把「编号列表」「项目经历」算进来：
	// 内推广告也常写成「投递方式：1. 2. 3.」，这两条会把广告误判成面经。
	reKwInterviewStrong = regexp.MustCompile(`面经|面试|一面|二面|三面|四面|面试官|八股|手撕|自我介绍|反问`)
)

// IsRecruitAd 判定是不是招聘/内推广告。
//
// 判据是「广告信号 vs 面试信号」的对比，而不是单看某一侧：
//   - 有强广告词（内推码/招聘详情/热招岗位…）
//   - 且几乎没有面试信号（少于 2 处）
//
// 真面经偶尔也会在结尾挂内推码，但那种帖子面试信号必然远多于 2 处，不会被误杀。
func IsRecruitAd(p *SitePost) bool {
	text := p.Title + "\n" + p.Content
	if !reKwAdStrong.MatchString(text) {
		return false
	}
	return len(reKwInterviewStrong.FindAllString(text, -1)) < 2
}

// InterviewScore 给出「像面经」的粗略评分，便于按置信度排序或设阈值。
func InterviewScore(p *SitePost) int {
	score := 0
	if reKwInterview.MatchString(p.Title) {
		score += 10
	}
	// 标题本身是「公司 岗位 轮次」结构，属于格式最规范的那类面经
	if _, _, r := ParseTitle(p.Title); r != "" {
		score += 8
	}
	score += len(reKwInterview.FindAllString(p.Content, -1))
	return score
}

// LooksLikeInterview 判定是否疑似面经。阈值是启发式的，宁松勿严：
// 漏掉真面经的代价（数据缺失）高于误收少量杂帖（清洗阶段还能再筛）。
func LooksLikeInterview(p *SitePost) bool {
	if p.Content == "" && p.Title == "" {
		return false
	}
	if reKwExcludeTitle.MatchString(p.Title) {
		return false
	}
	if IsRecruitAd(p) {
		return false
	}
	if reKwInterview.MatchString(p.Title) {
		return true
	}
	return InterviewScore(p) >= 4
}

// ---------- 小工具 ----------

func digMap(root map[string]any, path ...string) map[string]any {
	cur := root
	for _, k := range path {
		v, ok := cur[k]
		if !ok {
			return nil
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		cur = m
	}
	return cur
}

func strOf(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func int64Of(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case json.Number:
		n, _ := t.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	}
	return 0
}

// firstNonZero 返回第一个非零值。用于「同一个语义在不同实体类型下字段名不同」的取值。
func firstNonZero(vs ...int64) int64 {
	for _, v := range vs {
		if v != 0 {
			return v
		}
	}
	return 0
}
