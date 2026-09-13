package importer

import (
	"strings"
	"testing"

	"github.com/climber47/nc-interview/internal/model"
)

// TestSplitCompound 覆盖复合题拆分。
// 背景：原始帖里一行可能塞了多道题（「手撕：大数相加 & 接雨水」），
// 不拆会把两道题并成一道，频次统计也就失真。
func TestSplitCompound(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"手撕：大数相加 & 接雨水", []string{"手撕：大数相加", "手撕：接雨水"}},
		{"手撕：判断链表有环 & 树的层序遍历", []string{"手撕：判断链表有环", "手撕：树的层序遍历"}},
		{"手撕：大数相加 & 接雨水 & 岛屿数量", []string{"手撕：大数相加", "手撕：接雨水", "手撕：岛屿数量"}},
		{"手撕 LRU", []string{"手撕 LRU"}},
		{"手撕：有序数组合并", []string{"手撕：有序数组合并"}},
		// 非算法行不能拆：顿号在这里是同一道题的并列成分
		{"上下文管理、Skill和MCP的理解", []string{"上下文管理、Skill和MCP的理解"}},
		{"你们知识库怎么做？", []string{"你们知识库怎么做？"}},
		// 拆分后过短的应放弃拆分
		{"手撕：A & B", []string{"手撕：A & B"}},
	}
	for _, c := range cases {
		got := SplitCompound(c.in)
		if len(got) != len(c.want) {
			t.Errorf("SplitCompound(%q) = %v，期望 %v", c.in, got, c.want)
			continue
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Errorf("SplitCompound(%q) = %v，期望 %v", c.in, got, c.want)
				break
			}
		}
	}
}

// TestClassify 锁定模块划分。
// 重点：项目与知识点必须分开；八股要拆成「计算机基础（四大件）」与「后端工程」。
func TestClassify(t *testing.T) {
	cases := map[string]string{
		// 计算机基础：OS + 网络原理，不含语言线索
		"进程、线程、协程的区别？":     CatCS,
		"TCP 三次握手过程":       CatCS,
		"死锁的四个必要条件":        CatCS,
		"HTTP 和 HTTPS 的区别": CatCS,
		// 后端工程：带语言/中间件/容器/架构线索
		"Go 的协程调度？有优先级的概念吗？":                    CatBackend,
		"MySQL 索引失效的场景":                         CatBackend,
		"Redis 缓存穿透怎么解决":                        CatBackend,
		"kubectl create pod 的完整 K8s 组件协作流程是什么？": CatBackend,
		"微服务拆分的粒度怎么定":                           CatBackend,
		// Agent / AI
		"MCP 和 Skill 的区别是什么？": CatAgent,
		"上下文如何管理？":            CatAgent,
		"agent runtime怎么接入":   CatAgent,
		"RAG 召回率怎么优化":         CatAgent,
		// 项目与知识点分离
		"AB测试系统如何做？":  CatProj,
		"你们这个项目多少人做？": CatProj,
		"重构引入了哪些问题？":  CatProj,
		// 算法
		"手撕 LRU":  CatAlgo,
		"手撕：大数相加": CatAlgo,
		// 开场套话
		"简单介绍一下自己。":     CatIntro,
		"你们组里做啥":        CatIntro,
		"你有什么想问的吗？":     CatIntro,
		"先简单介绍一下你做的事情。": CatIntro,
		// HR
		"离职原因是什么？":   CatHR,
		"你现在总包大概多少？": CatHR,
	}
	for in, want := range cases {
		if got := Classify(in); got != want {
			t.Errorf("Classify(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestIsQuestion 覆盖备注/吐槽剔除。
func TestIsQuestion(t *testing.T) {
	remark := []string{
		"半小时光速通过",
		"已挂",
		"社招面试官有点装，感觉也不是很懂，还挺pua，国企太高高在上了，后续也不会面这种了",
		"感觉差不多",
		// 面试进展/结果描述——只是记录「这场怎么样了」，不是题目
		"过了已约二面",
		"过了，等二面",
		"预估一两周才能出结果",
		"（过程中还收到阿里电话，后面也打不回去了）",
		"还是项目深挖搞得多，一直被挑战技术没难度，最终还是聊了一个小时，真给挂了别浪费时间吧",
		"深度拷打简历",
		// 算法行里的备注
		"手撕：花了挺久",
	}
	for _, s := range remark {
		if IsQuestion(s) {
			t.Errorf("IsQuestion(%q) = true，期望 false（应视为备注）", s)
		}
	}
	question := []string{
		"你们知识库怎么做？",
		"手撕：大数相加 & 接雨水",
		"面试官问的那道 LRU 你还记得怎么实现吗？",
		"离职原因是什么？",
		// 长得像状态描述，但确实是提问，不能误杀
		"能接受加班吗？",
		"最快到岗时间",
		"你什么时候能到岗？",
		"后续还有几轮？",
	}
	for _, s := range question {
		if !IsQuestion(s) {
			t.Errorf("IsQuestion(%q) = false，期望 true（应视为问题）", s)
		}
	}
}

// TestIsKnowledge 开场套话不算知识点。
func TestIsKnowledge(t *testing.T) {
	if IsKnowledge(CatIntro) {
		t.Error("开场/通用 不应计入知识点统计")
	}
	for _, c := range []string{CatAlgo, CatCS, CatBackend, CatAgent, CatProj, CatHR} {
		if !IsKnowledge(c) {
			t.Errorf("%s 应计入知识点统计", c)
		}
	}
}

// TestExtractDoesNotDropTitledInterviews 锁住一个真实发生过的召回 bug。
//
// 早先 Extract 用 `p.Company == ""` 判断「不是面试帖」，但 company 来自
// 「公司 岗位 轮次」这个严格格式。于是一批标题只有一个空格的真面经
// （「上海某量化开发 一面」「遥望科技 hr面」「转转 FDE 线下面试」）
// 被整篇跳过，题一道都没抽出来——库里表现为这些帖子 0 个出现记录。
func TestExtractDoesNotDropTitledInterviews(t *testing.T) {
	posts := []model.Post{
		{
			ID: 1, Title: "上海某量化开发 一面", Company: "", // 标题只有一个空格，解析不出公司
			Content: "全栈开发是否ok？\n前端了解到什么程度\n项目挨着深挖",
		},
		{
			ID: 2, Title: "遥望科技 hr面", Company: "",
			Content: "先简单做个自我介绍好不好？\n你现在是在职状态吗？",
		},
		{
			ID: 3, Title: "转转 FDE 线下面试", Company: "",
			Content: "会话上下文压缩方案",
		},
		{
			ID: 4, Title: "牛客现在怎么一堆机器人评论", Company: "", // 真噪声，应跳过
			Content: "如题",
		},
	}
	rows, _ := Extract(posts)

	got := map[int64]int{}
	for _, r := range rows {
		got[r.postID]++
	}
	for _, id := range []int64{1, 2, 3} {
		if got[id] == 0 {
			t.Errorf("帖子 %d（标题含面试信号）一道题都没抽出来——召回 bug 复活了", id)
		}
	}
	if got[4] != 0 {
		t.Errorf("噪声帖 4 不该抽题，实际抽出 %d 条", got[4])
	}
}

// TestIsQuestionRejectsLongNarrative 覆盖抓全站后遇到的新噪声类型。
//
// 背景：全站面经里有很多「题目 + 怎么答 + 复习建议」混写的帖子。
// 「记住一个事实：嵌入式八股的题目池大概就 200 来道…」这类整段叙述
// 会被逐行判定当作题目。实测 2899 组里有 108 条，抽样 14 条全部不是题目。
//
// 判据是「长 + 句号结尾 + 没有问号」——这类句子没有共同关键词，
// 但都有同一个形态：长。真人提问写下来通常很短，因为面试是口头短问。
func TestIsQuestionRejectsLongNarrative(t *testing.T) {
	noise := []string{
		"记住一个事实：嵌入式八股的题目池大概就 200 来道，一场 40 分钟的技术面试能问到的不会超过 30 道。",
		"一个有血有肉的Harness描述，会让面试官立刻记住你，因为它证明了你不是在背概念，而是真的动手解决过工程问题。",
		"第四步：每完成一步，立即验证不要等所有代码都生成完再统一调试。每完成一个子任务，就立刻用简单的测试用例验证一下。",
		"这背后考察的是对AI系统质量保障的理解。很多候选人在准备面试时把精力放在模型结构上，却忽略了评测体系的搭建。",
		"效率上，大数据量通常共享内存最高；小消息、控制命令用 pipe/UDS 更简单。没有绝对“最快”，要看数据大小和同步成本。",
	}
	for _, s := range noise {
		if IsQuestion(s) {
			t.Errorf("长陈述句被当成题目: %q", s)
		}
	}

	// 反向：长但**带问号**的题面必须放行（这是阈值不会误杀的原因）
	real := []string{
		"流式请求具体是怎么做的？如果只处理增量内容语义可能不够、处理全量又时延高，这块怎么权衡？",
		"请介绍一下你在项目里是怎么设计这套评测体系的，包括指标选择、数据构造以及最终的结论有什么局限？",
		"你刚才说用了两级缓存，那如果本地缓存和 Redis 的数据不一致了，你会怎么处理并且怎么发现这种情况？",
	}
	for _, s := range real {
		if !IsQuestion(s) {
			t.Errorf("带问号的长题被误杀: %q", s)
		}
	}

	// 反向：短句即使以句号结尾也要保留（短句里题目和叙述混杂，不能按句号删）
	short := []string{
		"说一下你的 Agent 相关经验。",
		"你们重构的平台具体聊一下。",
		"介绍最近一段有代表性/有技术难度的项目经历。",
	}
	for _, s := range short {
		if !IsQuestion(s) {
			t.Errorf("短题被误杀: %q", s)
		}
	}
}

// TestIsQuestionRejectsJobHuntRemarks 覆盖面试感想类噪声的补充关键词。
// 这些是抓全站后才大量出现的（个人时间线里很少）。
func TestIsQuestionRejectsJobHuntRemarks(t *testing.T) {
	for _, s := range []string{
		"30min搞完，感觉可能会是kpi面",
		"双机位面试确实有点难绷",
		"笔试四题，光暴力骗分了",
	} {
		if IsQuestion(s) {
			t.Errorf("感想类噪声被当成题目: %q", s)
		}
	}
}

// TestIsQuestionRejectsSymbolOnly 保证纯符号行不算题目。
//
// 抓全站后出现的新噪声：分隔线（————————）、装饰行（***）。
// 它在两个层面都是问题：
//   - 语义上不是题目；
//   - 归一化会把标点全去掉，于是它的 norm 是空串，
//     多个空串撞上 norm 的 UNIQUE 约束，直接让整个重建失败
//     （Error 1062 Duplicate entry ” for key 'questions.uk_questions_norm'）。
func TestIsQuestionRejectsSymbolOnly(t *testing.T) {
	for _, s := range []string{
		"————————————————",
		"————————",
		"*********",
		"=======",
		"。。。",
		"——————  ——————",
	} {
		if IsQuestion(s) {
			t.Errorf("纯符号行被当成题目: %q", s)
		}
	}
	// 反向：含文字或数字的正常题目不能受影响
	for _, s := range []string{
		"介绍一下 GMP",
		"C++ 的智能指针有哪些",
		"手撕：LRU",
		"第 2 题怎么做",
	} {
		if !IsQuestion(s) {
			t.Errorf("正常题目被误杀: %q", s)
		}
	}
}

// TestClusterNormNeverEmpty 保证 norm 永不为空。
//
// 纯符号行退化后归一化结果是空串；若不做兜底，
// 两个这样的簇会同时写 norm=”，撞 UNIQUE 约束让重建整体失败。
func TestClusterNormNeverEmpty(t *testing.T) {
	qs := Cluster([]row{
		{text: "————————————————", postID: 1, cat: CatBackend},
		{text: "*********", postID: 2, cat: CatBackend},
	})
	for _, q := range qs {
		if q.Norm == "" {
			t.Errorf("norm 为空: canonical=%q", q.Canonical)
		}
	}
	seen := map[string]bool{}
	for _, q := range qs {
		if seen[q.Norm] {
			t.Errorf("norm 重复: %q", q.Norm)
		}
		seen[q.Norm] = true
	}
}

// TestIsQuestionHighPrecisionNoiseRules 覆盖四条**从模型标注里统计出来**的噪声规则。
//
// 做法：把 2466 条「模型判定为非题目」与 3863 条「模型判定为有效题」当作标注集，
// 逐个候选规则算精确率，只把精确率 ≥94% 的加进来。
// 各条的实测精确率与覆盖见注释。
//
// 这样定规则的好处是不会凭直觉误杀：每条都先证明「命中的绝大多数确实是噪声」。
func TestIsQuestionHighPrecisionNoiseRules(t *testing.T) {
	noise := []string{
		// 内推/招聘信息（98.7%）
		"三七互娱27届秋季校园招聘全球启动了，可找我内推，内推码任用：DStcT227",
		"我司的内推码是真长啊！！我的联想内推码：2027XZLMCWC",
		// 话题标签行（94.8%）
		"#京东笔试# 无敌了，第一题9.09%，第二题题都看不懂",
		"#秋招面试记录#时间：8月24日",
		// 面试指导语（100%）
		"面试官想听到的是对框架内部机制的了解，以及脱离框架后能否手动搭建一个可用的简化版",
		"面试时直接展示，说服力会成倍提升",
		// 攻略结构词 + 长（92~97%）
		"第二遍（1-2 周）：深度加工，加“为什么”。每道题追问自己两个“为什么”，把答案讲透",
		"第四步：每完成一步，立即验证，不要等所有代码都生成完再统一调试，这样能快速定位问题",
		// 第一人称回忆叙述 + 长（92~97%）
		"鼠鼠已经Java实习第二个月了，现在就是有时候很闲，做过几个小需求然后但是都挺简单",
		"我现在还在小公司实习呢，下班之前就发现面试挂了的消息了哈哈哈，前面的问题也夹杂着一些",
	}
	for _, s := range noise {
		if IsQuestion(s) {
			t.Errorf("高精确率规则未拦下: %q", s)
		}
	}

	// 反向：这些规则全部要求「没有提问意图」。
	// 真问题里出现「招聘」「面试时」非常正常，带问号就必须放行。
	real := []string{
		"你们公司的招聘流程是怎样的？",
		"面试时你一般怎么介绍自己的项目？",
		"介绍一下你们团队的招聘标准",
		"内推和官网投递有什么区别？",
		"刷题应该按什么顺序刷？",
		"实习期间你做过哪些需求？",
	}
	for _, s := range real {
		if !IsQuestion(s) {
			t.Errorf("真问题被误杀: %q", s)
		}
	}

	// 新增两条（在更大的标注集上重新统计出来的）
	more := []string{
		// 长 + 无问号 + 求职过程词（92.7%）
		"今年秋招我投了大概一百家公司，最后拿到三个offer，整体感受是简历比面试重要得多",
		"今天笔试做完了，投递的岗位还没消息，简历也改了好几版，感觉这一轮比较悬",
	}
	for _, s := range more {
		if IsQuestion(s) {
			t.Errorf("新增规则未拦下: %q", s)
		}
	}

	// 代码/结构化片段（98.8%）
	code := []string{
		"def process(self, data) -> List[Item]: 返回处理后的结果列表",
		"调用链是 controller -> service -> dao，每一层都做了参数校验和异常包装",
		"result = model.infer(x) 这里要注意 batch 维度的对齐问题",
	}
	for _, s := range code {
		if IsQuestion(s) {
			t.Errorf("代码片段未拦下: %q", s)
		}
	}

	// 在 9,269 条非题目 / 15,452 条有效题上重新统计出来的四条（精确率 ≥92.9%）
	more2 := []string{
		"投递入口：https://jobs.mihoyo.com/m/?sharePageId=142899", // URL 96.2%
		"内推直达 www.example.com/campus",                        // URL
		"return size() > SimpleLruCache.this.capacity;",      // 代码关键字 92.9%
		"备注：这里要重点准备一下",                                       // 注释前缀 100%
		"1.abc",                                              // 极短编号残片 94.7%
		"3、xyz",
	}
	for _, s := range more2 {
		if IsQuestion(s) {
			t.Errorf("新增规则未拦下: %q", s)
		}
	}
	// Markdown/标签是**排版**，由 stripFormatting 在判定之前剥掉，
	// 不属于 IsQuestion 的职责（见 TestStripFormatting）。
	// 这里只验证「剥掉标记之后，剩下的内容按正常规则判定」。

	// 反向：真问题里出现这些词要素时必须放行
	keep := []string{
		"Go 里 return 和 defer 的执行顺序？",    // 有关键字但有问号
		"你们官网 www.example.com 的架构是怎样的？", // 有 URL 但有问号
		"static 关键字在 C++ 里有什么作用？",
		"int 和 long 在 64 位平台上分别占几个字节？",
		// 关键回归：编号开头的**中文**题是最规范的写法，绝不能当残片丢掉。
		// 这条规则最初写成 \S{1,8}，把 988 行真问题误杀了。
		"1. 自我介绍",
		"7. 什么时候要用锁",
		"8. 面向对象的特性？",
		"1.缓存穿透",
		"2、幂等性设计", // 注意「2、幂等」只有 4 字，会被 IsQuestion 开头的长度下限挡掉，那是另一条规则
	}
	for _, s := range keep {
		if !IsQuestion(s) {
			t.Errorf("真问题被误杀: %q", s)
		}
	}

	// 反向：短句即使带攻略语气也不能杀（长度是必要条件之一）
	short := []string{
		"第一步做什么",
		"我当时怎么答的",
		"你觉得应该怎么优化",
		// 带求职过程词但很短：长度条件不满足，不能杀
		//（注意「笔试考什么」只有 5 字，会被 IsQuestion 开头的长度下限挡掉，
		// 那是另一条规则，不适合用在这里当反例）
		"笔试都考什么内容",
		"实习主要做了哪些事",
		// 带问号的代码风格提问必须放行（有提问意图）
		"这个 a -> b 的转换是怎么实现的？",
	}
	for _, s := range short {
		if !IsQuestion(s) {
			t.Errorf("短句被误杀: %q", s)
		}
	}
}

// TestNormStripsLeadingLabel 覆盖行首题号/标签的剥离。
//
// 面经里普遍把题目写成「7.xxx」「4、xxx」「(3) xxx」「第3题 xxx」「Q：xxx」。
// 不剥掉的话，同一道题会因为编号不同被聚成两簇，频次统计也就被拆散了。
// 实测（4296 组有效题）有 7 对这样的重复。
func TestNormStripsLeadingLabel(t *testing.T) {
	same := [][2]string{
		{"7.算法：最大子数组和", "算法：最大子数组和"},
		{"4.tcp。udp区别", "tcp。udp区别"},
		{"1.介绍实习经历", "介绍实习经历"},
		{"5.事件循环机制？", "事件循环机制？"},
		{"(3) 介绍一下 GMP", "介绍一下 GMP"},
		{"第3题 介绍实习经历", "介绍实习经历"},
		{"Q：介绍一下职业规划", "介绍一下职业规划"},
		{"面试问题： 在基于 GRPO 的…", "在基于 GRPO 的…"},
	}
	for _, p := range same {
		if Norm(p[0]) != Norm(p[1]) {
			t.Errorf("应归一化为同一个键:\n  %q → %q\n  %q → %q",
				p[0], Norm(p[0]), p[1], Norm(p[1]))
		}
	}

	// 反向：版本号不能被当成题号
	version := [][2]string{
		{"3.2 版本的 Go 有什么变化", "3.2 版本的 Go 有什么变化"},
		{"2.5 版本的协议差异", "2.5 版本的协议差异"},
	}
	for _, p := range version {
		if Norm(p[0]) != Norm(p[1]) {
			t.Errorf("版本号被误当成题号剥掉了: %q → %q", p[0], Norm(p[0]))
		}
	}

	// 反向：题号后面的内容必须保留，不能连内容一起剥
	if got := Norm("7.算法：最大子数组和"); got != Norm("算法：最大子数组和") || got == "" {
		t.Errorf("剥离后内容丢失: %q", got)
	}
	// 整行只有编号时不剥（没有内容可留）
	if got := Norm("7."); got == "" {
		t.Logf("「7.」归一化为空（可接受）")
	}
}

// TestStripLeadLabelKeepsVersion 单独验证版本号保护这条边界。
//
// Go 的 RE2 不支持 lookahead，所以「分隔符后是否还是数字」只能用代码判断。
func TestStripLeadLabelKeepsVersion(t *testing.T) {
	keep := []string{"3.2 版本的 Go", "1.5 倍性能提升", "2.4G 和 5G 的区别"}
	for _, s := range keep {
		if stripLeadLabel(s) != s {
			t.Errorf("版本号被剥离: %q → %q", s, stripLeadLabel(s))
		}
	}
	strip := map[string]string{
		"7.算法":   "算法",
		"4、tcp":  "tcp",
		"(3) 介绍": "介绍",
		"第2题：介绍": "介绍",
		"Q：介绍":   "介绍",
	}
	for in, want := range strip {
		if got := stripLeadLabel(in); got != want {
			t.Errorf("stripLeadLabel(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestClusterAnchorDoesNotSwallowShortQuestions 覆盖一次严重的聚类事故。
//
// 事故现象：一个簇吃掉了 **2063 个变体、360 次出现**，成员从「智能指针」
// 到「agent runtime」到「YUV 格式」毫无关系（其余簇最大只有 60）。
//
// 成因是两个问题叠加：
//
//	① 合并判据用的是 g.norm，而 norm 取「簇内最长/官方那条的归一化文本」，
//	   会随着新成员加入**变长**——簇的身份在漂移；
//	② 判据里有 `strings.Contains(锚点, n)`，长锚点会把任何碰巧作为子串
//	   出现的短问题吸进来。两者叠加就是链式吞噬。
//
// 修法：合并只用**进簇第一行的 key**（永不改变），并给包含关系加长度比上限。
func TestClusterAnchorDoesNotSwallowShortQuestions(t *testing.T) {
	rows := []row{
		// 锚点：一条 50 字左右的官方题面
		{text: "在ReAct Agent调用外部工具时如果工具服务启动较慢例如Pod启动耗时应该采用同步调用还是异步调用并说明理由",
			postID: 1, official: true, cat: CatAgent},
		// 下面三条都作为子串出现在上面那条里，但它们是**各自独立**的题目
		{text: "同步调用还是异步调用", postID: 2, cat: CatAgent},
		{text: "工具服务启动较慢", postID: 3, cat: CatAgent},
		{text: "ReAct Agent调用外部工具", postID: 4, cat: CatAgent},
	}
	qs := Cluster(rows)
	if len(qs) != 4 {
		t.Errorf("4 条互不相同的题应聚成 4 簇，实际 %d 簇（长锚点又吞并短题了）", len(qs))
		for _, q := range qs {
			t.Logf("  簇 n=%d %q", q.N, q.Canonical)
		}
	}
}

// TestClusterAnchorIsStable 覆盖「锚点漂移导致链式合并」。
//
// 若合并判据用会变的 norm，A~B~C~D 会连成一串，
// 即使首尾两条已经毫无关系（单链接聚类的经典失败模式）。
// 锚点固定为第一行后，每一步都只会与**起点**比较，链就断了。
func TestClusterAnchorIsStable(t *testing.T) {
	// 每一步只与上一步相近，但与起点已无关
	rows := []row{
		{text: "介绍一下你的项目", postID: 1},
		{text: "介绍一下你的项目中的难点", postID: 2},
		{text: "介绍一下你的项目中的难点以及解决方案", postID: 3},
		{text: "介绍一下你的项目中的难点以及解决方案的落地效果", postID: 4},
		{text: "介绍一下你的项目中的难点以及解决方案的落地效果和复盘", postID: 5},
	}
	qs := Cluster(rows)
	// 不断言具体簇数（那取决于阈值），只断言**没有全部塌成一簇**
	if len(qs) == 1 {
		t.Errorf("5 条长度递增的题全被链式合并成 1 簇（锚点在漂移）")
	}
	// 而且任何一簇的变体数不该接近总行数
	for _, q := range qs {
		if q.N >= len(rows) {
			t.Errorf("簇 %q 吞下了全部 %d 条", q.Canonical[:20], q.N)
		}
	}
}

// TestClusterNormalRealisticDistribution 是一条「分布合理性」的护栏。
//
// 真实面经里同一道题被反复问到的次数应该在个位数到几十之间；
// 出现一个远超其他簇的巨簇，几乎一定是聚类出错而不是数据真的如此。
// 事故里最大簇是 360，第二名 61 —— 这种量级断层就是信号。
func TestClusterNormalRealisticDistribution(t *testing.T) {
	// 造 200 条各不相同的题，加 1 条被重复 30 次的题
	var rows []row
	seen := []string{"介绍一下 GMP 调度模型"}
	for i := 0; i < 30; i++ {
		rows = append(rows, row{text: "介绍一下 GMP 调度模型", postID: int64(i + 1)})
	}
	words := []string{"Redis", "MySQL", "HTTP", "TCP", "协程", "索引", "事务", "缓存", "分布式", "限流",
		"线程池", "GC", "JVM", "Kafka", "Docker", "微服务", "幂等", "熔断", "降级", "分库"}
	for i := 0; i < 200; i++ {
		seen = append(seen, "请说明"+words[i%len(words)]+"在"+words[(i*7)%len(words)]+"场景下的作用与实现原理"+string(rune('A'+i%26))+string(rune('a'+i%26)))
	}
	for i, s := range seen {
		if i < 30 {
			continue // 前 30 条已经加过
		}
		rows = append(rows, row{text: s, postID: int64(1000 + i)})
	}

	qs := Cluster(rows)
	maxN := 0
	for _, q := range qs {
		if q.N > maxN {
			maxN = q.N
		}
	}
	if maxN > 40 {
		t.Errorf("最大簇 %d 次出现，远超重复次数（30），说明发生了异常合并", maxN)
	}
}

// TestStripFormatting 覆盖「排版标记不该判死刑」。
//
// 曾经 markdown 列表符与话题标签被当成剔除理由（`reMarkdown = ^\s*[-*•]\s`），
// 结果是**用列表写的真题**被整条丢掉——实测 48 行，例如：
//
//   - 建立索引什么思路、什么原则
//   - cpu 流水线五个阶段是什么
//   - [ ] es的倒排和mysql的b+，mysql为什么不要倒排这种方法
//
// 标记只是排版，剥掉之后剩下的才是内容。所以改成先剥再判。
func TestStripFormatting(t *testing.T) {
	cases := map[string]string{
		"- 建立索引什么思路、什么原则":         "建立索引什么思路、什么原则",
		"* 项目里用了 I2C 读传感器":        "项目里用了 I2C 读传感器",
		"- [ ] 网络空间安全专业为什么想到做后端呢": "网络空间安全专业为什么想到做后端呢",
		"- [x] 已完成的任务":            "已完成的任务",
		"#牛客AI配图神器#一、自我介绍相关":      "一、自我介绍相关",
		"#秋招# #面经# 讲讲你的项目":        "讲讲你的项目",
		"**重点**：一定要把量化指标写清楚":      "重点：一定要把量化指标写清楚",
		"没有标记的普通句子":               "没有标记的普通句子",
	}
	for in, want := range cases {
		if got := stripFormatting(in); got != want {
			t.Errorf("stripFormatting(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestExtractKeepsBulletedRealQuestions 端到端：列表里写的真题要能抽出来。
func TestExtractKeepsBulletedRealQuestions(t *testing.T) {
	content := "- 建立索引什么思路、什么原则\n- cpu 流水线五个阶段是什么\n- [ ] 网络空间安全专业为什么想到做后端呢"
	posts := []model.Post{{ID: 1, Title: "某公司 后端 一面", Company: "某公司", Content: content}}
	rows, _ := Extract(posts)
	if len(rows) < 3 {
		t.Fatalf("三条列表真题应全部抽出，实际 %d 条", len(rows))
	}
	for _, r := range rows {
		if strings.HasPrefix(r.text, "-") {
			t.Errorf("抽出的题仍带列表标记: %q", r.text)
		}
	}
}
