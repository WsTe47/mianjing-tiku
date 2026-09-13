// Package importer 把抓到的帖子转成「问题 + 出现记录」并写入数据库。
//
// 流程：清洗正文 → 逐行判定是否为问题 → 归类模块与主题 → 模糊聚类去重 → 落库。
package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/climber47/nc-interview/internal/model"
	"github.com/climber47/nc-interview/internal/scraper"
	"github.com/climber47/nc-interview/internal/store"
)

// ---------- 清洗 ----------

var (
	reImg     = regexp.MustCompile(`<img[^>]*>`)
	reCard    = regexp.MustCompile(`data-card-emoji="[^"]*"`)
	reEmoji   = regexp.MustCompile(`\[[^\]]{1,6}\]`)
	reTag     = regexp.MustCompile(`<[^>]+>`)
	reBlankLn = regexp.MustCompile(`[\n\r]+`)
)

// CleanText 去掉牛客正文里的图片标签、表情记号与 HTML 标签。
func CleanText(s string) string {
	s = reImg.ReplaceAllString(s, "")
	s = reCard.ReplaceAllString(s, "")
	s = reEmoji.ReplaceAllString(s, "")
	s = reTag.ReplaceAllString(s, "")
	return strings.TrimSpace(html.UnescapeString(s))
}

// ---------- 是否为「问题」 ----------

var (
	reStrongRemark = regexp.MustCompile(`(光速|半小时|已挂|挂了|凉了|感谢信|收到offer|口头offer|等通知|面完|测评挂|进池子|排序中|无hc|结果通知|已oc)`)
	reMetaRemark   = regexp.MustCompile(`^(leader面|面试官|整体|全程|总共|一共|感觉|大概|差不多|项目挨着|深挖|问的挺|聊了)`)
	// 面后感/吐槽：没有问号的主观评价，不是题目。
	reComplain = regexp.MustCompile(`(面试官.{0,8}(装|pua|PUA|不懂|水)|有点装|太装|亏麻|无语|离谱|不会面|这种了|属于是|西装革履|光速搞定|拷打|真给挂|浪费时间|一直被挑战|也不好进|不好进|hhh|233|kpi面|KPI面|难绷|骗分|骗分了|光暴力)`)
	// 面试进展/结果描述：整行只是在记录「这场怎么样了」，不是在提问。
	// 例：过了已约二面 / 过了，等二面 / 预估一两周才能出结果 / 真给挂了别浪费时间吧
	reOutcome = regexp.MustCompile(`^(过了|已过|通过了|已约|约二面|约三面|等二面|等三面|等通知|进二面|进三面|出结果|预估.{0,8}结果|给挂了|真给挂|面完了|已offer|面了\d+)`)
	// 以结果词结尾但整行没有提问意图，例：比较业务相关，过了
	reEndsOutcome = regexp.MustCompile(`(过了|挂了|凉了|通过了|结束了)$`)
	reIsAsking    = regexp.MustCompile(`[？?]|^(请问|你(们)?|怎么|如何|什么|为什么|哪些|介绍|讲|说|手撕|手写)`)
	// 面试攻略/知识总结的「陈述句」：长句 + 句号结尾 + 没有问号。
	//
	// 抓全站之后遇到的新噪声类型：帖子把题目和「怎么答」「复习建议」混在一起写，
	// 于是「记住一个事实：嵌入式八股的题目池大概就 200 来道…」这类整段叙述
	// 也被当成题目。实测 2899 组里这类有 108 条（3.7%），抽样 14 条**全部**不是题目。
	//
	// 判据用「长度」而不是关键词：这类句子没有任何共同的关键词，
	// 但都有一个共同形态——**长**。真人提问写下来通常很短
	//（「介绍一下 GMP」「为什么用 Redis」），因为面试是口头短问。
	// 超过 50 字还带句号且全程没有问号的，基本是作者在写答案或建议。
	//
	// 阈值刻意保守：真被写成长句的题目几乎都会带问号（「…这块怎么权衡？」），
	// 有问号就不进这条规则，所以不会误杀。
	reNarrative = regexp.MustCompile(`[。.]$`)
	// 帖子标题混进正文的情况：形如「互动影游 全栈开发 二面」「百度 服务端开发 一面」。
	// 这类行不是面试官提的问题，而是这篇帖子的标题（复制正文时常带进来）。
	// 判据复用标题解析器：它能解析出轮次的，就是标题格式。
	rePostTitle = regexp.MustCompile(`^\S{2,20}\s+\S{2,20}\s+(一面|二面|三面|四面|终面|hr面|HR面|笔试|机试)$`)
	// 求赞/求关注类收尾语
	reSolicit = regexp.MustCompile(`点个赞|求赞|点赞收藏|评论区可以问|评论区交流|欢迎交流|看到都会回|关注我|码字不易|整理不易`)
	// 至少含一个文字或数字（中英文、数字）。全标点的行不是题目。
	reHasWord = regexp.MustCompile(`[\p{Han}A-Za-z0-9]`)

	// 以下三条规则不是拍脑袋写的，是拿 2466 条「模型判定为非题目」+
	// 3863 条「模型判定为有效题」做统计选出来的（见 docs/DEPLOYMENT.md 5.6）。
	// 括号里是实测精确率，即命中里真的是噪声的比例。

	// 内推/招聘信息（98.7%）
	reRecruitAd = regexp.MustCompile(`内推码|内推|招聘|校招启动|投递链接|扫码|简历投递|base地|福利待遇`)
	// 话题标签行：`#秋招# #面经#` 这种（94.8%）
	reTagLine = regexp.MustCompile(`^#|#[^#]{1,20}#`)
	// 面试指导语：作者在教人怎么答，不是在问（100%）
	reTeachTalk = regexp.MustCompile(`面试官(想听|希望|期待|会|往往|可能)|面试时|面试中|回答这类|候选人(应该|可以|要)`)
	// 攻略/建议的结构词（92.3%）
	reGuideWord = regexp.MustCompile(`第[一二三四五六七八九十]步|第[一二三四五]遍|第一步|总结一下|划重点|小技巧|必刷|复习计划`)
	// Markdown 的列表标记与话题标签。它们**只是排版**，不该据此判死刑——
	// 实测 `- 建立索引什么思路、什么原则`、`- cpu 流水线五个阶段是什么`
	// 这类「用列表项写的真题」有 48 行被整条丢掉。
	// 所以现在的做法是：**先剥掉标记再判断**，而不是见到标记就拒。
	reMarkdownMarker = regexp.MustCompile(`^\s*(?:[-*•]\s+|\[\s*[xX ]?\s*\]\s*)+`)
	reTagMarker      = regexp.MustCompile(`^(?:#[^#\s]{1,24}#?\s*)+`)
	// 强调符号本身不含信息，直接删（保留正文）
	reBold = regexp.MustCompile(`\*\*`)
	// 求职过程词。长句 + 无问号 + 出现这类词，基本是在记录自己的求职进度
	//（实测 92.7% 精确率）。
	reJobProcess = regexp.MustCompile(`秋招|春招|校招|社招|实习|笔试|投递|简历|offer|Offer|OFFER|去年|今年|昨天|今天|上周|这周`)
	// URL / 投递链接。带链接的行几乎都是招聘广告（实测 96.2% 精确率）。
	reHasURL = regexp.MustCompile(`https?://|www\.\S|\.com/|\.cn/`)
	// 用于把 URL 从「有没有提问意图」的判定里剔掉。
	//
	// 必须这么做：URL 的查询串自带 `?`（`…?sharePageId=142899`），
	// 会让一行广告被 `reIsAsking` 当成**问句**，于是所有「没有提问意图才生效」
	// 的噪声规则全部失效。真实踩过：投递链接因此没被 URL 规则拦下。
	reStripURL = regexp.MustCompile(`https?://\S+|www\.\S+`)
	// 编程语言关键字出现在**没有问号**的行里，说明是贴的代码（92.9%）；
	// 带问号的真问题（「Go 里 return 和 defer 的执行顺序？」）会被 asking 放行。
	reCodeKeyword = regexp.MustCompile(`\b(int|void|return|class|struct|public|private|static|func|var|const)\b`)
	// 行首的注释性前缀，例：「备注：…」「注：…」「说明：…」（实测 100%）
	reNotePrefix = regexp.MustCompile(`^\s*(备注|注|说明|提示)\s*[:：]`)
	// 极短的编号残片，例：「1.abc」「3、xyz」。
	//
	// ⚠️ 这里踩过一次严重的误杀：最初的写法是 `\S{1,8}`，把**中文**也算进去了，
	// 于是「1. 自我介绍」「7. 什么时候要用锁」「8. 面向对象的特性？」这类
	// **最常见、最规范的编号真题**全被当成残片丢掉——实测帖子正文里有 996 行命中，
	// 其中 988 行含中文，是真问题。
	//
	// 更值得记的是**为什么当时没发现**：这条规则的精确率是在 `questions.canonical`
	// 上统计的（94.7%），而 canonical 的编号早被 stripLeadLabel 剥掉了，
	// 命中的那 18 条根本不是「编号开头的题」。**用错了统计口径，
	// 得到的是一个漂亮但无关的数字。**
	//
	// 现在只匹配「编号后面只剩 ASCII」的形态——那才是真正的残片。
	reShortNumbered = regexp.MustCompile(`^\s*\d{1,2}\s*[、.．]\s*[A-Za-z0-9_\-./]{1,8}$`)
	// 第一人称叙述（比 reFirstPerson 宽）：出现「我/自己/本人」即算
	reSelfNarrative = regexp.MustCompile(`我|自己|本人|感觉|觉得`)
	// 疑问短语：出现在**任意位置**都说明这行在提问，不该按「求职叙述」剔除
	reQuestionPhrase = regexp.MustCompile(`什么|怎么|如何|为什么|哪些|是否|有没有|是不是|区别|介绍|讲讲|说说|手撕|手写`)
	// 代码/结构化片段：箭头、方法调用。面经里常把代码或日志直接贴进来，
	// 那些行不是题目（实测 98.8% 精确率）。
	reCodeFragment = regexp.MustCompile(`->|=>|\.\w+\(`)
	// 第一人称回忆叙述。「鼠鼠/本鼠」是牛客上很常见的自称，不能漏。
	reFirstPerson = regexp.MustCompile(`鼠鼠|本鼠|我已经|我现在|我当时|我后来|后来我|然后我|其实我|我感觉|我觉得|我投了|我做了|我面了|我笔试`)
)

// IsQuestion 判定一行文本是否为面试问题（默认是，只剔除明确不是题目的行）。
//
// 所有剔除规则都要先确认「看不出提问意图」：像「面试官问的那道 LRU 你还记得吗？」
// 以「面试官」开头但是合法提问，不能因为开头词就误杀。
func IsQuestion(s string) bool {
	n := len([]rune(s))
	if n <= 5 {
		return false
	}
	// 整行是一个括号补充说明，例：（过程中还收到阿里电话，后面也打不回去了）
	if strings.HasPrefix(s, "（") || strings.HasPrefix(s, "(") {
		return false
	}
	// 帖子标题误入正文（「百度 服务端开发 一面」），以及求赞类收尾语
	if rePostTitle.MatchString(s) || reSolicit.MatchString(s) {
		return false
	}
	// 整行只有标点/符号，没有任何文字或数字：分隔线（————————）、
	// 纯装饰行（***）、只有括号等。这类不可能是一道题，
	// 而且归一化后会得到空串，撞上 norm 的 UNIQUE 约束。
	if !reHasWord.MatchString(s) {
		return false
	}
	// 算法行里的备注，例：手撕：花了挺久
	if reAlgoLead.MatchString(s) && reAlgoExc.MatchString(s) {
		return false
	}
	// 判定「有没有提问意图」时先去掉 URL，避免链接里的 ? 被当成问号
	asking := reIsAsking.MatchString(reStripURL.ReplaceAllString(s, " "))
	if reOutcome.MatchString(s) && !asking {
		return false
	}
	if reStrongRemark.MatchString(s) && n < 32 && !asking {
		return false
	}
	if reMetaRemark.MatchString(s) && n < 34 && !asking {
		return false
	}
	if reComplain.MatchString(s) && !asking && n < 95 {
		return false
	}
	if reEndsOutcome.MatchString(s) && !asking && n < 40 {
		return false
	}
	// 长陈述句：攻略、知识总结、面后感。没有问号说明不是在提问，长度说明不是题目。
	if !asking && n > maxQuestionRunes && reNarrative.MatchString(s) {
		return false
	}
	// 高精确率的噪声规则（精确率≥94%，都是从模型标注里统计出来的）。
	// 全部要求 !asking：真问题里出现「招聘」「面试时」很正常
	//（「你们的招聘流程是怎样的？」），带问号就不该被杀。
	if !asking {
		if reRecruitAd.MatchString(s) || reTagLine.MatchString(s) || reTeachTalk.MatchString(s) {
			return false
		}
		// 长 + 无问号 + 攻略语气 —— 三个条件同时成立才算。
		// 单独用「长 + 无问号」精确率只有 79%，加上语气条件能到 94%~97%。
		if n > 30 && (reGuideWord.MatchString(s) || reFirstPerson.MatchString(s) || reTeachTalk.MatchString(s)) {
			return false
		}
		// 注意：markdown 列表符/话题标签**不再**作为剔除理由——它们在进判定之前
		// 就已被 stripFormatting 剥掉（见 Extract）。曾经「见到 - 就拒」误杀了 48 行
		// 用列表写的真题（「- cpu 流水线五个阶段是什么」）。
		// 长 + 无问号 + 求职过程词 + **第一人称叙述** + **不含疑问短语**。
		//
		// 后面两个条件是补上的：原来只看「长度 + 关键词」，于是
		// 「4. 云agent如果容器迁移或者重启了，上下文信息怎么办（…我简历上根本没有）」
		// 这类**夹在求职叙述里的真题**被整条丢掉（实测这一条规则下就有 81 行
		// 含疑问词却仍被剔除）。真正该杀的是「今天笔试做完了，投递的岗位还没消息」
		// 这种纯叙述——它有第一人称、且没有任何疑问词。
		if n > 30 && reJobProcess.MatchString(s) && reSelfNarrative.MatchString(s) &&
			!reQuestionPhrase.MatchString(s) {
			return false
		}
		// 长 + 无问号 + 代码片段
		if n > 20 && reCodeFragment.MatchString(s) {
			return false
		}
		// 带 URL 的行（含链接的题目几乎不存在，带链接的多是投递入口）
		if reHasURL.MatchString(s) {
			return false
		}
		// 无问号 + 编程语言关键字
		if !asking && reCodeKeyword.MatchString(s) {
			return false
		}
		// 注释性前缀、极短编号残片
		if reNotePrefix.MatchString(s) || reShortNumbered.MatchString(s) {
			return false
		}
	}
	return true
}

// maxQuestionRunes 是「还能算作一道题」的长度上限。
//
// 定在 50：抽样看，超过这个长度的句号结尾句全是攻略/答案/叙述，
// 而真正写成长句的题目几乎都带问号（会被 reIsAsking 放行）。
// 这个值调小会开始误杀「请介绍一下你们项目的整体架构以及你负责的部分」这类长题面，
// 调大则会漏掉攻略文本——50 是当前样本下的折中。
const maxQuestionRunes = 50

// ---------- 模块分类 ----------
//
// 七个模块。设计取向：把「项目经历」与「知识点」彻底分开，并把原本笼统的
// 「八股」按考察对象拆成「计算机基础（四大件）」与「后端工程」——前者是 OS/网络原理，
// 后者是语言/存储/中间件/容器/架构/性能。开场套话单独成模块，不参与知识点统计。
const (
	CatAlgo    = "算法"
	CatCS      = "计算机基础"
	CatBackend = "后端工程"
	CatAgent   = "Agent/AI"
	CatProj    = "项目"
	CatIntro   = "开场/通用"
	CatHR      = "HR/软性"
)

var (
	reAlgoStart = regexp.MustCompile(`^(手撕|手写|写一个|实现一个(?:函数|算法)|算法题|编程题)`)
	reAlgoName  = regexp.MustCompile(`\bLRU\b|链表去重|二叉树|动态规划|全排列|三数之和|接雨水|大数相加|中位数|公共祖先|最小栈|令牌桶|阻塞队列|无锁队列|层序遍历|所有子集|平方根|区间合并|有环|深拷贝|回文|岛屿数量|岛屿最大|股票买卖|两数之和|数组的交集|正方形判定|中缀转后缀|快排|归并`)
	reAlgoExc   = regexp.MustCompile(`花了|想不起来|难崩|不让用`)

	// 开场套话：自我介绍、团队/组介绍、反问环节。必须在「项目」「HR」之前判定，
	// 否则「先简单介绍一下你做的事情」会被项目规则抢走。
	reIntro = regexp.MustCompile(`自我介绍|介绍一下自己|介绍(一下)?你自己|简单介绍(一下)?你|先介绍(一下)?你|介绍一下你(的)?(情况|背景|经历|工作)|你们组|你们团队|你们部门|组里做|团队(做|在)(什么|啥)|有什么想问|你想问|还有什么问题|反问`)

	reHR = regexp.MustCompile(`离职|为什么换|换工作|跳槽|动机|看机会|为什么看|薪资|总包|待遇|绩效|职级|晋升|职业规划|未来.{0,6}规划|\bbase\b|base地|地点|老家|offer|诉求|自我评价|优缺点|最有成就|你觉得自己|期望|到岗|你的规划|家庭|结婚|年龄|个人发展|技术规划|转岗|倾向`)

	reProj = regexp.MustCompile(`你的项目|你们(的)?项目|你们(现在|这套|这个|内部)|项目.{0,8}(介绍|讲|难点|挑战|深挖|架构|价值)|介绍.{0,4}项目|讲.{0,4}项目|项目经历|你负责|你的角色|你做过|重构|提效|交付|归因|(?i)ab\s*测试|埋点|灰度|风险|回滚|上线|落地|业务|工作内容|赋能|核心.{0,4}工作|部门|方案|产品|标准化`)

	reAgent = regexp.MustCompile(`(?i)agent|harness|智能体|\bmcp\b|skill|技能|上下文|记忆|memory|rag|知识库|检索|llm|大模型|幻觉|微调|后训练|推理|prompt|token|沙箱|sandbox|越权|multi-?agent|agent\s*team|workflow|工作流|工具调用|向量|embedding|评测|eval|` +
		// AI 辅助研发这一族：原本散落在「后端工程/其他技术」里
		`claude\s?code|codex|cursor|copilot|codebuddy|superpower|openspec|\bsdd\b|\btdd\b|ai\s*coding|ai原生|编码工具|调用模型|流式|stream`)

	// 后端工程：语言 / 存储 / 中间件 / 容器编排 / 架构 / 性能 / RPC 框架。
	// 刻意排在「计算机基础」之前——「Go 的协程调度」属于 Go 语言，而
	// 「进程、线程、协程的区别」不含语言线索，会落到计算机基础。
	reBackend = regexp.MustCompile(`(?i)\bgo\b|golang|\bgmp\b|goroutine|channel|\bslice\b|defer|\bgc\b|\bmap\b|` +
		`java|jvm|python|装饰器|c\+\+|cpp|闭包|泛型|多态|指针|` +
		`mysql|索引|事务|分库|分表|postgres|\bpg\b|mongo|sqlite|数据库|存储引擎|` +
		`redis|缓存|kafka|\bmq\b|消息队列|zookeeper|etcd|` +
		`k8s|kubernetes|docker|容器|镜像|\bpod\b|ingress|kubectl|helm|` +
		`架构|微服务|\bddd\b|高可用|分布式|中台|解耦|限流|熔断|降级|幂等|系统设计|` +
		`性能|优化|延迟|\bqps\b|吞吐|并发|压力测试|` +
		`grpc|trpc|\brpc\b|restful`)

	// 计算机基础（四大件）：操作系统 + 计算机网络原理。
	reCS = regexp.MustCompile(`操作系统|计算机基础|组成原理|编译原理|词法分析|语法分析|文法|有限状态机|` +
		`进程|线程|协程|死锁|内存管理|虚拟内存|页面置换|用户态|内核态|上下文切换|` +
		`多路复用|epoll|select|文件系统|中断|` +
		`(?i)\btcp\b|\budp\b|\bhttp\b|\bhttps\b|\bdns\b|三次握手|四次挥手|网络分层|\bosi\b|` +
		`socket|拥塞|滑动窗口|keep-?alive|反向代理|子网|网关`)
)

// Classify 判定该问题属于哪个模块。顺序即优先级。
func Classify(s string) string {
	switch {
	case (reAlgoStart.MatchString(s) || reAlgoName.MatchString(s)) && !reAlgoExc.MatchString(s):
		return CatAlgo
	case reIntro.MatchString(s):
		return CatIntro
	case reHR.MatchString(s):
		return CatHR
	case reProj.MatchString(s):
		return CatProj
	case reAgent.MatchString(s):
		return CatAgent
	case reBackend.MatchString(s):
		return CatBackend
	case reCS.MatchString(s):
		return CatCS
	default:
		return CatBackend // 技术兜底：这份数据集里非上述类别的基本都是后端题
	}
}

// ---------- 主题分类 ----------

// topicRule 按声明顺序匹配，先命中先用。
type topicRule struct {
	name string
	re   *regexp.Regexp
}

// topicRules 按模块分组。统一次序匹配会让「Go 的协程调度」落到「操作系统」主题上
// （模块是后端工程），所以每个模块只在自己的主题集里匹配。
var topicRulesByCat = map[string][]topicRule{
	CatCS: {
		{"操作系统", regexp.MustCompile(`进程|线程|协程|死锁|内存管理|虚拟内存|页面置换|用户态|内核态|上下文切换|多路复用|epoll|select|文件系统|中断|锁`)},
		{"计算机网络", regexp.MustCompile(`(?i)\btcp\b|\budp\b|\bhttp\b|\bhttps\b|\bdns\b|握手|挥手|网络分层|\bosi\b|socket|拥塞|滑动窗口|keep-?alive|反向代理|子网|网关`)},
	},
	CatBackend: {
		{"语言特性", regexp.MustCompile(`(?i)\bgo\b|golang|\bgmp\b|goroutine|channel|\bslice\b|defer|\bgc\b|\bmap\b|java|jvm|python|装饰器|c\+\+|cpp|闭包|泛型|多态|指针`)},
		{"数据库", regexp.MustCompile(`(?i)mysql|索引|事务|分库|分表|postgres|\bpg\b|mongo|sqlite|数据库|存储引擎`)},
		{"缓存 / 中间件", regexp.MustCompile(`(?i)redis|缓存|kafka|\bmq\b|消息队列|zookeeper|etcd`)},
		{"容器 / K8s", regexp.MustCompile(`(?i)k8s|kubernetes|docker|容器|镜像|\bpod\b|ingress|kubectl|helm`)},
		{"架构 / 系统设计", regexp.MustCompile(`架构|微服务|\bddd\b|高可用|分布式|中台|解耦|限流|熔断|降级|幂等|系统设计|重构`)},
		{"性能优化", regexp.MustCompile(`性能|优化|延迟|\bqps\b|吞吐|并发|压力测试`)},
		{"RPC / 协议", regexp.MustCompile(`(?i)grpc|trpc|\brpc\b|restful|协议`)},
		{"工程实践", regexp.MustCompile(`(?i)研发效能|\bci\b|\bcd\b|ci/?cd|测试|单测|回归|code\s?review|代码审查|开源|二开|分叉|上线流程|发布流程|监控|告警|运维|landing|灰度|值班|事故|复盘`)},
		{"前端 / 客户端", regexp.MustCompile(`(?i)vue|react|前端|浏览器|页面|css|javascript|\bjs\b|webpack|小程序|安卓|android|\bios\b|客户端|\bsdk\b|flutter|rn\b|鸿蒙|harmony`)},
		// 抓全站后新增：原来只有「语言特性」一个大桶，把 C++/Java/Python 的技术题全混在一起，
		// 于是「shared_ptr 的引用计数」和「Go 的 GMP」在同一个主题下，看不出一家公司到底考什么。
		{"C / C++", regexp.MustCompile(`(?i)c\+\+|cpp|shared_ptr|unique_ptr|智能指针|虚函数|左值|右值|移动语义|模板|\bstl\b|\braii\b|构造函数|析构|内存泄漏|指针|mcu\b|嵌入式|\brtos\b|\bcan\b|寄存器|中断服务`)},
		{"Java / JVM", regexp.MustCompile(`(?i)\bjava\b|\bjvm\b|\bjdk\b|spring|netty|\bbean\b|\baop\b|\bioc\b|垃圾回收器|类加载|线程池|synchronized|\bvolatile\b|hashmap|concurrenthashmap`)},
	},
	CatAgent: {
		{"MCP / Skill", regexp.MustCompile(`(?i)\bmcp\b|skill|技能`)},
		{"上下文 / 记忆", regexp.MustCompile(`(?i)上下文|记忆|memory|context`)},
		{"RAG / 知识库", regexp.MustCompile(`(?i)rag|知识库|检索|向量|embedding`)},
		{"Multi-Agent", regexp.MustCompile(`(?i)multi-?agent|agent\s*team|协作|多个agent|多智能体`)},
		{"AI 编码工具", regexp.MustCompile(`(?i)codex|claude\s?code|cursor|codebuddy|copilot|superpower|openspec|sdd|\btdd\b|ai\s*coding|编码工具|\bcli\b|\bide\b`)},
		{"Agent 架构 / Harness", regexp.MustCompile(`(?i)agent|harness|智能体|工作流|workflow|工具调用|规划`)},
		{"LLM / 大模型", regexp.MustCompile(`(?i)llm|大模型|幻觉|微调|后训练|推理|prompt|token|评测|eval`)},
		{"沙箱 / 权限", regexp.MustCompile(`沙箱|sandbox|越权|权限|隔离`)},
		// 抓全站后新增：算法岗的题目和「手撕代码」性质不同，混在一起会看不出岗位差异
		{"推荐 / 搜索", regexp.MustCompile(`推荐|召回|排序|\bctr\b|\bcvr\b|特征工程|冷启动|长尾|搜索|\b粗排\b|\b精排\b|用户画像`)},
		{"训练 / 微调", regexp.MustCompile(`微调|\bsft\b|\bdpo\b|\bppo\b|\bgrpo\b|预训练|蒸馏|量化|分布式训练|\blora\b|强化学习|奖励模型|\brlhf\b`)},
	},
}

// TopicOf 判定问题主题。算法/项目/开场/HR 的模块与主题一一对应，直接返回。
func TopicOf(s, category string) string {
	switch category {
	case CatAlgo:
		return "算法/手撕"
	case CatProj:
		return "项目深挖"
	case CatIntro:
		return CatIntro
	case CatHR:
		return "HR/动机"
	}
	for _, r := range topicRulesByCat[category] {
		if r.re.MatchString(s) {
			return r.name
		}
	}
	return "其他技术"
}

// IsKnowledge 表示该模块承载「知识点」。
// 开场/通用 只是流程套话（自我介绍、团队介绍、反问），不参与知识点统计。
func IsKnowledge(category string) bool { return category != CatIntro }

// ---------- 归一化与相似度 ----------

var (
	reNonWord = regexp.MustCompile(`[^\p{L}\p{N}]+`)
	// 填充词：去掉后同义问题的归一化文本才可能一致
	fillers = []string{"你们", "我们", "你", "一下", "怎么", "如何", "什么是", "什么", "哪些",
		"为什么", "吗", "呢", "请问", "讲讲", "讲一下", "说说", "介绍", "大概", "主要", "具体", "的", "了"}
)

var (
	// 行首的题号/标签。面经里普遍写成「7.xxx」「4、xxx」「(3) xxx」「第 3 题 xxx」
	// 「Q：xxx」「面试问题：xxx」，不去掉的话同一道题会因为编号不同聚成两簇。
	reLeadNum   = regexp.MustCompile(`^\s*\d{1,2}\s*[.、)．]\s*`)
	reLeadParen = regexp.MustCompile(`^\s*[（(]\s*\d{1,2}\s*[)）]\s*`)
	reLeadDi    = regexp.MustCompile(`^\s*第\s*\d{1,2}\s*[题问]\s*[:：]?\s*`)
	reLeadTag   = regexp.MustCompile(`^\s*(?:面试问题|面试题|问题|题目|Q|q)\s*[:：]\s*`)
)

// stripLeadLabel 去掉行首的编号或标签。
//
// 单独写成函数而不是塞进正则，是因为要防一个反例：「3.2 版本的 Go 有什么变化」
// 里的「3.」是版本号不是题号。规则是——**分隔符后面紧跟数字就放弃剥离**。
// Go 的 RE2 不支持 lookahead，所以这一步只能用代码判断。
func stripLeadLabel(s string) string {
	for _, re := range []*regexp.Regexp{reLeadNum, reLeadParen, reLeadDi, reLeadTag} {
		loc := re.FindStringIndex(s)
		if loc == nil {
			continue
		}
		rest := strings.TrimLeft(s[loc[1]:], " \t")
		// 「3.2 版本」这种：分隔符后还是数字，说明是版本号而不是题号
		if rest != "" && rest[0] >= '0' && rest[0] <= '9' {
			return s
		}
		if rest == "" {
			return s // 整行只有编号，没有内容
		}
		return rest
	}
	return s
}

// Norm 归一化文本用于聚类。保留字母数字——技术题里 DNS/Channel/gRPC 才是主体。
func Norm(s string) string {
	s = stripLeadLabel(s)
	s = strings.ToLower(s)
	s = reNonWord.ReplaceAllString(s, "")
	for _, f := range fillers {
		s = strings.ReplaceAll(s, f, "")
	}
	return s
}

// Similar 返回 0~1 的相似度，基于 Levenshtein 距离。
func Similar(a, b string) float64 {
	if a == b {
		return 1
	}
	la, lb := len([]rune(a)), len([]rune(b))
	if la == 0 || lb == 0 {
		return 0
	}
	d := levenshtein([]rune(a), []rune(b))
	mx := la
	if lb > mx {
		mx = lb
	}
	return 1 - float64(d)/float64(mx)
}

func levenshtein(a, b []rune) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

const similarThreshold = 0.82

// similarAtLeast 判断 Similar(a,b) >= threshold，但用**带状 + 提前退出**的
// Levenshtein，只算有可能落在阈值内的那部分。
//
// 为什么需要它：聚类里是「行数 × 簇数」量级的比较（10 万行 / 6 万簇 ≈ 30 亿次），
// 而原来的写法是 `Similar(n, g.key) >= similarThreshold`——Similar 会把整张
// la×lb 的 DP 表算完才返回，哪怕两条字符串毫不相干。实测全量重建因此跑了一小时
// 还没进到写库阶段。
//
// 两处剪枝都**不改变判定结果**（由 TestSimilarAtLeastMatchesSimilar 覆盖）：
//  1. 长度界：lev >= |la-lb|，所以超过 (1-T)*max 的长度差不可能达标；
//  2. 带内 DP + 行最小值提前退出：某一行的最小值已经超过 maxD，后面只会更差。
func similarAtLeast(a, b string, threshold float64) bool {
	if a == b {
		return true
	}
	return similarRunesAtLeast([]rune(a), []rune(b), threshold)
}

// similarRunesAtLeast 是 similarAtLeast 的核心：接收已经转好的 rune 切片，
// 且**不做任何堆分配**。
//
// 为什么这点很重要：聚类热路径上这个函数要跑几千万次，而每次
// `[]rune(a)`/`[]rune(b)` 加两个 DP 缓冲就是 4 次分配——实测这一项就占了
// 一次全量重建里的大部分时间。调用方因此缓存好 rune 切片（簇上一次、每行一次），
// 这里再用栈数组当 DP 缓冲。
func similarRunesAtLeast(ra, rb []rune, threshold float64) bool {
	la, lb := len(ra), len(rb)
	if la == 0 || lb == 0 {
		return false
	}
	mx := la
	if lb > mx {
		mx = lb
	}
	maxD := int((1 - threshold) * float64(mx))
	diff := la - lb
	if diff < 0 {
		diff = -diff
	}
	if diff > maxD {
		return false
	}

	// 带状 DP：只算 |i-j| <= maxD 的格子（因为 DP[i][j] >= |i-j|，
	// 带外的格子不可能 ≤ maxD）。
	//
	// 边界处理是这段代码唯一容易错的地方：两个缓冲区是复用的，带外格子会残留
	// 两行前的旧值。所以每行显式写两个哨兵——
	//   cur[lo-1]（本行的左外）与 cur[hi+1]（本行的右外，供下一行读 prev[hi+1]）。
	// 漏掉任何一个都会让「毫不相干的两条」判成相似；这不是理论风险，
	// 第一版就是漏了 prev[hi+1]，被 TestSimilarAtLeastMatchesSimilar 当场对拍出来。
	big := maxD + 1
	// 栈缓冲：题目文本绝大多数在 128 个 rune 以内，超出才退化成堆分配。
	const stackMax = 128
	var pbuf, cbuf [stackMax + 1]int
	var prev, cur []int
	if lb <= stackMax {
		prev, cur = pbuf[:lb+1], cbuf[:lb+1]
	} else {
		prev, cur = make([]int, lb+1), make([]int, lb+1)
	}
	for j := range prev {
		prev[j] = big
	}
	hi0 := maxD
	if hi0 > lb {
		hi0 = lb
	}
	for j := 0; j <= hi0; j++ {
		prev[j] = j
	}
	if hi0+1 <= lb {
		prev[hi0+1] = big
	}

	for i := 1; i <= la; i++ {
		lo := i - maxD
		if lo < 1 {
			lo = 1
		}
		hi := i + maxD
		if hi > lb {
			hi = lb
		}
		if lo > 1 {
			cur[lo-1] = big
		}
		if hi+1 <= lb {
			cur[hi+1] = big
		}
		for j := lo; j <= hi; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			v := prev[j-1] + cost
			if x := prev[j] + 1; x < v {
				v = x
			}
			if x := cur[j-1] + 1; x < v {
				v = x
			}
			cur[j] = v
		}
		prev, cur = cur, prev
	}
	return prev[lb] <= maxD
}

// ---------- 导入 ----------

// Stats 汇总一次导入的结果。
type Stats struct {
	Posts     int
	RawLines  int
	Questions int
	Skipped   int
}

// row 是一条「原始问题」及其来源。
type row struct {
	text     string
	postID   int64
	cat      string
	topic    string
	answer   string // 参考答案，只有官方结构化真题才有
	official bool   // 是否来自官方结构化真题列表
}

// Extract 从帖子中抽取问题行。
// reInterviewTitle 判定标题是否在讲一场面试。
//
// 不能只看 company：company 来自「公司 岗位 轮次」这个严格格式，
// 而大量真面经的标题并不符合它——「上海某量化开发 一面」只有一个空格、
// 「转转 FDE 线下面试」没有标准轮次词，都会被解析出空的 company。
var reInterviewTitle = regexp.MustCompile(`面经|面试|笔试|机试|测评|一面|二面|三面|四面|终面|初面|复试|群面|主管面|交叉面|HR面|hr面`)

// isInterviewPost 判定这篇帖子是否值得抽题。
//
// 早先这里写的是 `p.Company == ""` 就跳过，本意是「感悟、吐槽类帖子不参与抽题」，
// 但那是个很差的代理指标：它把 6 篇标题不含标准轮次词的真面经
// （「上海某量化开发 一面」「遥望科技 hr面」「转转 FDE 线下面试」…）整篇丢掉了。
// 现在改为「标题里明确提到面试」或「标题能解析出公司」二者之一。
func isInterviewPost(p model.Post) bool {
	if p.Company != "" {
		return true
	}
	return reInterviewTitle.MatchString(p.Title)
}

// answerPrefix 匹配官方答案开头的前缀。
// 原文形如「- 正确答案：  \n  您好，我…」「正确答案：…」「答案：…」。
var answerPrefix = regexp.MustCompile(`^\s*[-•·]?\s*(正确答案|参考答案|答案)\s*[:：]\s*`)

// reMetaAnswer 匹配「答非所问」的答案。
//
// 官方真题的答案是用模型生成的。题目过于简短或指代不明时（「我的输出是什么」
// 「讲解以下主要区别」「用什么方法解决」），模型会反过来**请求澄清**而不是作答，
// 于是答案变成「请提供具体的面试问题…」「看起来您的输入可能不完整…」。
// 这类内容留在题库里比没有答案更糟——用户点开「参考答案」看到的是模型在问他要题。
//
// 只在**答案开头**匹配：这些套话都出现在第一句。
// 放宽到全文会误伤——例如某条 2000 字的正常答案中间就出现了「请补充」。
var reMetaAnswer = regexp.MustCompile(`^\s*(请提供|请补充|请说明|看起来您的输入|您是想了解|你的输出是|我的输出是|` +
	`(我?作为|我是)一?个?\s*(AI|人工智能|语言模型|面试官)|我无法回答|抱歉[，,]?我)`)
var reMetaAnswerWindow = regexp.MustCompile(`看起来您的输入|您是想了解|请提供(具体|您希望)的`)

// isMetaAnswer 判断这条答案是不是「模型在请求澄清」而不是真的作答。
func isMetaAnswer(a string) bool {
	a = strings.TrimSpace(a)
	if a == "" {
		return true
	}
	head := a
	if r := []rune(a); len(r) > 60 {
		head = string(r[:60])
	}
	if reMetaAnswer.MatchString(head) {
		return true
	}
	// 少数会把澄清语放在第二句
	return reMetaAnswerWindow.MatchString(head)
}

// maxAnswerRunes 是答案的存储上限。
//
// 为什么截断：答案会随列表接口一起返回，50 条一页。
// 官方答案里有不少是长篇科普（能到上万字），不截断会让单页响应膨胀到几 MB，
// 而参考价值的边际收益在 2000 字之后已经很低。原始 HTML 还在 data/raw/，
// 真要完整答案可以重解析。
const maxAnswerRunes = 2000

// TrimAnswer 清洗并截断参考答案。
func TrimAnswer(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = answerPrefix.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	// 折叠连续空行
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxAnswerRunes {
		return strings.TrimSpace(string(r[:maxAnswerRunes])) + "…"
	}
	return s
}

// officialRows 把帖子里存的官方结构化真题列表转成抽取行。
//
// 输入是 posts.official_q（抓取时从 ssrCommonData.experienceQuestionList 取到的 JSON）。
// 解析失败就当作没有——这是一条增益路径，不该因为它把整篇帖子搞崩。
func officialRows(p model.Post) []row {
	if p.OfficialQ == "" {
		return nil
	}
	var qs []scraper.ExperienceQuestion
	if err := json.Unmarshal([]byte(p.OfficialQ), &qs); err != nil {
		return nil
	}
	out := make([]row, 0, len(qs))
	for _, q := range qs {
		text := CleanText(q.Title)
		if text == "" {
			continue
		}
		if ct := Classify(text); ct != "" {
			// 先 TrimAnswer 去掉「- 正确答案：」这类前缀，再做答非所问判定。
			// 顺序不能反：原文几乎都带前缀，先判前缀会把开头挡住，
			// 于是「- 正确答案：作为AI助手，我本身不是…」这种就被漏掉了。
			ans := TrimAnswer(strings.TrimSpace(q.Answer))
			if isMetaAnswer(ans) {
				ans = "" // 宁可不给答案，也不给一条「请提供具体问题」
			}
			out = append(out, row{
				text: text, postID: p.ID, cat: ct, topic: TopicOf(text, ct),
				answer: ans, official: true,
			})
		}
	}
	return out
}

// stripFormatting 去掉**纯排版**标记：markdown 列表符、复选框、话题标签、加粗。
//
// 这些标记本身不含语义，剥掉之后剩下的才是内容。放在判定之前做，
// 才能避免「因为作者用了列表/标签就丢掉一道真题」。
func stripFormatting(s string) string {
	s = reMarkdownMarker.ReplaceAllString(s, "")
	s = reTagMarker.ReplaceAllString(s, "")
	s = reBold.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

// splitInlineQuestions 把「一整段里塞了多道题」的行拆成多道题。
//
// ⚠️ 这是继「company=="" 丢真面经」和「锚点漂移吞题」之后的第三个召回缺口，
// 而且是最大的一次：不少帖子把题目**连排在一段里、没有任何换行**，例如
//
//	「请做一下自我介绍。你的专业偏开发，为什么选择测试开发岗位？你对测试开发岗位有什么了解？…」
//	「Java 基础 == 和 equals 的区别？hashCode 为什么要一起重写？String 和 StringBuilder 区别？…」
//
// 逐行抽取时，整段就是「一行」，于是几十道题被当成**一道**（还被 1000 字的列宽截断）。
// 实测有 248 行是这种形态，其中一份「高频面试题清单」一段就有 50 多道题。
//
// 判据用问号：真人写题目几乎都以「？」收尾。所以
//   - 长度 >40 且问号数 ≥2 才拆（短句不拆，避免把「A？B？」这种一问两半拆散）
//   - 在每个问号后切一刀，切出的片段各自走后面的判定流程
func splitInlineQuestions(line string) []string {
	if len([]rune(line)) <= inlineSplitMinRunes {
		return []string{line}
	}
	if strings.Count(line, "？")+strings.Count(line, "?") < 2 {
		return []string{line}
	}
	var out []string
	var cur []rune
	for _, r := range line {
		cur = append(cur, r)
		if r == '？' || r == '?' {
			if seg := strings.TrimSpace(string(cur)); seg != "" {
				out = append(out, seg)
			}
			cur = cur[:0]
		}
	}
	if seg := strings.TrimSpace(string(cur)); seg != "" {
		out = append(out, seg)
	}
	if len(out) <= 1 {
		return []string{line}
	}
	return out
}

// inlineSplitMinRunes 是触发「一行多题」拆分的最短长度。
// 定在 40：正常一道题写下来很少超过这个长度，超过且带多个问号基本就是连排。
const inlineSplitMinRunes = 40

func Extract(posts []model.Post) ([]row, int) {
	var rows []row
	skipped := 0
	for _, p := range posts {
		// ① 官方结构化真题优先：这批数据是整理过的，无正则噪声且带参考答案。
		//    它们**不经过 IsQuestion**——判据是给「从正文里抠出来的行」用的，
		//    官方列表本身就是题目的权威定义，再拿启发式规则去筛只会误杀。
		offRows := officialRows(p)
		rows = append(rows, offRows...)

		// 有官方真题的帖子一定算面经帖，标题格式不该成为拦路条件
		if !isInterviewPost(p) && len(offRows) == 0 { // 感悟、吐槽、内推等帖子不参与题目抽取
			continue
		}
		for _, line := range reBlankLn.Split(p.Content, -1) {
			s := stripFormatting(CleanText(line))
			if s == "" {
				continue
			}
			// 先拆「一行多题」（连排的整段），再逐条判定与归类
			for _, piece := range splitInlineQuestions(s) {
				if !IsQuestion(piece) {
					skipped++
					continue
				}
				// 一行还可能塞了多道算法题（「手撕：大数相加 & 接雨水」），再拆一层
				for _, one := range SplitCompound(piece) {
					cat := Classify(one)
					rows = append(rows, row{text: one, postID: p.ID, cat: cat, topic: TopicOf(one, cat)})
				}
			}
		}
	}
	return rows, skipped
}

// 复合题：一行里用 & / 分隔了多道独立的题。
// 只对「手撕/手写」这类算法行生效——其他题型里的 & 和 、 往往是同一道题的并列成分
// （如「上下文管理、Skill 和 MCP 的理解」是一道题，不能拆）。
var (
	reAlgoLead  = regexp.MustCompile(`^\s*(手撕|手写|写一个|实现一个)[：:、\s]*`)
	reCompound  = regexp.MustCompile(`\s*(?:&|＆|以及|加上)\s*`)
	reAlgoSplit = regexp.MustCompile(`\s*[、,，]\s*`)
)

// SplitCompound 把一行里的多道算法题拆成独立题目，并补回「手撕：」前缀。
// 「手撕：大数相加 & 接雨水」→ ["手撕：大数相加", "手撕：接雨水"]
func SplitCompound(line string) []string {
	lead := reAlgoLead.FindString(line)
	if lead == "" {
		return []string{line}
	}
	body := strings.TrimSpace(strings.TrimPrefix(line, lead))
	if body == "" {
		return []string{line}
	}
	// 先按 & 拆，再按顿号拆（算法行里的顿号同样是并列的多道题）
	var parts []string
	for _, seg := range reCompound.Split(body, -1) {
		for _, sub := range reAlgoSplit.Split(seg, -1) {
			if sub = strings.TrimSpace(sub); sub != "" {
				parts = append(parts, sub)
			}
		}
	}
	if len(parts) <= 1 {
		return []string{line}
	}
	out := make([]string, 0, len(parts))
	// lead 会把尾部的「：」一起吃进来，必须去掉再自行拼接，否则变成「手撕：：」
	prefix := strings.TrimRight(strings.TrimSpace(lead), "：:、 \t")
	for _, p := range parts {
		if len([]rune(p)) < 2 {
			return []string{line}
		}
		out = append(out, prefix+"："+p)
	}
	return out
}

// Cluster 把问题行聚成去重后的问题集合。
// 列宽上限与截断函数统一放在 model（schema 的属性），这里只是本地别名，
// 避免调用处写一长串 model. 前缀。
const (
	maxCanonRunes = model.MaxCanonRunes
	maxNormRunes  = model.MaxNormRunes
)

func clip(s string, n int) string { return model.ClipRunes(s, n) }

func Cluster(rows []row) []model.Question { return ClusterWithMerges(rows, nil, nil) }

// ClusterWithMerges 在聚类前先把「同义题」映射到同一个规范形，让同义问法落进同一簇。
//
// merges 是「源 norm → 目标 norm」，mergeCanon 是「目标 norm → 目标原文」。
// 为什么需要 mergeCanon：整簇最终的 norm 是 Norm(代表措辞)，如果代表措辞仍是
// 簇内最长的那条变体，norm 就不会等于合并目标，而大模型标注是**按 norm 存**的，
// 于是合并后的簇会整片丢掉标注。所以要把代表措辞钉在合并目标上。
func ClusterWithMerges(rows []row, merges, mergeCanon map[string]string) []model.Question {
	type cluster struct {
		key      string // **稳定**的合并锚点：进簇第一行的归一化文本，此后永不改变
		klen     int    // len([]rune(key))，建簇时算一次
		krunes   []rune // key 的 rune 切片，避免热路径上反复转换
		forceRep string // 同义合并目标原文；非空时覆盖代表措辞
		norm     string // 最终写库的键：Norm(代表措辞)，与 canonical 保持一致
		occ      []row
	}
	var cs []cluster
	for _, r := range rows {
		n := Norm(r.text)
		if n == "" {
			n = r.text
		}
		n = clip(n, maxNormRunes)
		if t, ok := merges[n]; ok {
			n = t
		}
		// 长度只跟当前行有关，必须在 merge 映射之后算，且**提到循环外**。
		//
		// 原来写成 `la, lb := len([]rune(g.key)), len([]rune(n))` 放在循环里，
		// 每次比较都要做两次 []rune 转换。这个循环是「行数 × 簇数」量级
		// （实测 10 万行 / 6 万簇 ≈ 30 亿次），于是光分配就压垮了整个 parse：
		// 一次重建跑 60 分钟还没进到写库阶段。
		nrunes := []rune(n)
		lb := len(nrunes)
		// Similar 的长度必要条件：Sim = 1 - lev/max，且 lev >= |la-lb|，
		// 所以 Sim >= T 必然要求 |la-lb| <= (1-T)*max。长度差距超过这个界的
		// 两条**根本不可能**相似，可以直接跳过，不必付 Levenshtein 的 O(la*lb)。
		// 这是等价剪枝，不改变任何聚类结果。
		simSlack := int((1 - similarThreshold) * float64(lb))
		var hit *cluster
		for i := range cs {
			g := &cs[i]
			// ⚠️ 合并只能用 key，绝不能用 norm。
			//
			// 这里踩过一次严重事故：norm 是「簇内最长/官方那条的归一化文本」，
			// 会随着新成员加入而变长。用它做合并判据时，簇的身份会**漂移**，
			// 叠加上「并入第一个匹配簇」的单链接策略，就变成了链式吞噬——
			// 实测有一个簇吃掉了 2063 个变体、360 次出现，
			// 成员从「智能指针」到「agent runtime」到「YUV 格式」毫无关系。
			//
			// key 取自进簇第一行，永不改变，所以合并判据是稳定的。
			// 包含关系只允许「长度相当」的两条互相合并。
			//
			// 否则一条长句子当锚点时（「在ReAct Agent调用外部工具时，如果工具服务
			// 启动较慢…」）会把任何碰巧作为子串出现的短问题都吸进来。
			// 真人问同一道题的两种写法长度不会差太多，用 2 倍做上限足够。
			la := g.klen
			comparable := la >= 6 && lb >= 6 && la <= 2*lb && lb <= 2*la
			if g.key == n ||
				(comparable && (strings.Contains(g.key, n) || strings.Contains(n, g.key))) {
				hit = g
				break
			}
			// 长度差超出必要条件的直接跳过（见上面 simSlack 的推导）
			if la < lb-simSlack || la > lb+simSlack {
				continue
			}
			if similarRunesAtLeast(nrunes, g.krunes, similarThreshold) {
				hit = g
				break
			}
		}
		if hit != nil {
			hit.occ = append(hit.occ, r)
		} else {
			nr := []rune(n)
			cs = append(cs, cluster{key: n, klen: len(nr), krunes: nr,
				norm: n, forceRep: mergeCanon[n], occ: []row{r}})
		}
	}

	out := make([]model.Question, 0, len(cs))
	for _, g := range cs {
		// 代表措辞优先取官方结构化真题的标题：它是人工整理过的，
		// 比「正文里抠出来的一行」更规范（没有「自我介绍？」这类口语残留）。
		// 同一来源内再取最长的一条（信息量最大）。
		var rep string
		var answer string
		for _, o := range g.occ {
			if o.official {
				if rep == "" || len([]rune(o.text)) > len([]rune(rep)) {
					rep = o.text
				}
			}
			// 答案取第一条非空的：同一道题的不同帖子里答案措辞不同，但都可参考
			if answer == "" && o.answer != "" {
				answer = o.answer
			}
		}
		if rep == "" {
			rep = g.occ[0].text
			for _, o := range g.occ {
				if len([]rune(o.text)) > len([]rune(rep)) {
					rep = o.text
				}
			}
		}

		// 展示用的措辞也去掉行首编号/标签（「7.算法：…」→「算法：…」）。
		// 原文措辞仍然完整保存在 occurrences.raw_text 里，所以不丢信息。
		// 不影响下面的不变式：Norm 本来就会剥掉这层前缀。
		rep = stripLeadLabel(rep)

		// 同义合并：把整簇的代表措辞钉在合并目标上，这样下面的不变式
		// Norm(代表措辞) == norm 依然成立，按 norm 存的大模型标注才不会丢。
		if g.forceRep != "" {
			rep = stripLeadLabel(g.forceRep)
		}

		// 不变式：norm 必须由**代表措辞**推出，即 Norm(canonical) == norm。
		//
		// 早先 norm 取自「进簇的第一条」，而 canonical 取「最长/官方的那条」，
		// 两者常常不是同一条文本，于是 3038 个簇里有 35 个满足
		// Norm(canonical) != norm。这在按行 id 落地时看不出来，
		// 但归类结果改成**按题目原文**落地后就暴露了：
		// 批次文件给的是 canonical，导入时算 Norm(canonical)，两边对不上就丢标注。
		//
		// 代表措辞是这道题对外呈现的样子，归一化键跟着它才是自洽的。
		//
		// 归一化会把标点全部去掉，于是「————————」这类纯符号行会得到空串，
		// 多个空串撞上 norm 的 UNIQUE 约束（Error 1062 Duplicate entry ''）。
		// 退化时用原文兜底，保证 norm 非空。
		g.norm = clip(Norm(rep), maxNormRunes)
		if g.norm == "" {
			g.norm = clip(rep, maxNormRunes)
		}

		variants := map[string]bool{}
		occurs := make([]model.Occur, 0, len(g.occ))
		seenPost := map[int64]bool{}
		officialN := 0
		for _, o := range g.occ {
			if o.text != rep {
				variants[o.text] = true
			}
			if o.official {
				officialN++
			}
			key := o.postID
			if seenPost[key] {
				continue
			}
			seenPost[key] = true
			occurs = append(occurs, model.Occur{
				PostID: o.postID, RawText: o.text, Official: o.official,
			})
		}
		var vs []string
		for v := range variants {
			vs = append(vs, v)
		}
		sort.Strings(vs)
		out = append(out, model.Question{
			Canonical: clip(rep, maxCanonRunes),
			Norm:      g.norm,
			Category:  g.occ[0].cat,
			Topic:     g.occ[0].topic,
			N:         len(occurs),
			Answer:    TrimAnswer(answer),
			OfficialN: officialN,
			Variants:  vs,
			Occurs:    occurs,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return len([]rune(out[i].Canonical)) < len([]rune(out[j].Canonical))
	})
	return out
}

// Import 抽取、聚类并写入数据库（含帖子 upsert）。
func Import(ctx context.Context, st *store.Store, posts []model.Post, log func(string, ...any)) (Stats, error) {
	if log == nil {
		log = func(string, ...any) {}
	}
	for _, p := range posts {
		if err := st.UpsertPost(ctx, p); err != nil {
			return Stats{}, fmt.Errorf("写入帖子 %d: %w", p.ID, err)
		}
	}
	rows, skipped := Extract(posts)
	log("抽取问题行 %d 条（跳过备注 %d 行）", len(rows), skipped)

	qs := Cluster(rows)
	log("聚类为 %d 组问题", len(qs))

	if err := st.ReplaceQuestions(ctx, qs); err != nil {
		return Stats{}, fmt.Errorf("写入问题: %w", err)
	}
	if log != nil {
		log("导入 %d 篇：%d 行 → %d 组", len(posts), len(rows), len(qs))
	}
	return Stats{Posts: len(posts), RawLines: len(rows), Questions: len(qs), Skipped: skipped}, nil
}

// ImportAll 从数据库里已有的帖子重新抽取聚类（改分类规则后无需重抓）。
func ImportAll(ctx context.Context, st *store.Store, log func(string, ...any)) (Stats, error) {
	// 必须用 AllPosts：Posts 的 size 钳制会把「取全部」静默压成 50 条，
	// 漏帖会让重建后的问题集合丢掉大部分出现记录。
	posts, err := st.AllPosts(ctx)
	if err != nil {
		return Stats{}, err
	}
	rows, skipped := Extract(posts)
	merges, mergeCanon, err := LoadMerges(mergesPath)
	if err != nil {
		return Stats{}, err
	}
	before := len(rows)
	qs := ClusterWithMerges(rows, merges, mergeCanon)
	if err := st.ReplaceQuestions(ctx, qs); err != nil {
		return Stats{}, err
	}
	if log != nil {
		log("从库内 %d 篇帖子重建：%d 行 → %d 组（同义合并 %d 条映射）",
			len(posts), before, len(qs), len(merges))
	}
	return Stats{Posts: len(posts), RawLines: len(rows), Questions: len(qs), Skipped: skipped}, nil
}

// mergesPath 是同义题合并表的位置。由 LLM 提候选 + 人工逐组复核产生，
// 缺文件时行为与「没有合并」完全一致（便于测试与冷启动）。
const mergesPath = "data/llm/merges.json"

// LoadMerges 读取同义题合并表，返回（源 norm → 目标 norm, 目标 norm → 目标原文）。
func LoadMerges(path string) (map[string]string, map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	var f struct {
		Merges []struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"merges"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, nil, fmt.Errorf("解析 %s: %w", path, err)
	}
	merges := make(map[string]string, len(f.Merges))
	canon := make(map[string]string)
	for _, m := range f.Merges {
		from := clip(Norm(m.From), maxNormRunes)
		to := clip(Norm(m.To), maxNormRunes)
		if from == "" || to == "" || from == to {
			continue
		}
		merges[from] = to
		canon[to] = m.To
	}
	// 解析链式映射：A→B 且 B→C 时，A 必须直接落到 C。
	//
	// 否则单层查表会把 A 归到 B，而 B 自己的簇已经并进了 C——
	// A 于是变成一个只有自己的孤簇，频次也丢了。第二轮「合并目标之间再去重」
	// 会大量产生这种链。
	for from := range merges {
		k, guard := from, 0
		for {
			t, ok := merges[k]
			if !ok || t == k || guard > 32 {
				break
			}
			k = t
			guard++
		}
		merges[from] = k
	}
	// canon 只保留最终目标
	for from, to := range merges {
		if from == to {
			delete(merges, from)
		}
	}
	return merges, canon, nil
}
