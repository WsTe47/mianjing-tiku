package scraper

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"
)

// TestIsRecruitAd 锁住「招聘/内推广告」与「真面经」的边界。
//
// 这批用例全部来自真实抓取样本，回归价值很高：
// 广告帖标题里常带「秋招/校招」，会命中面经关键词；
// 而真面经结尾也常挂内推码，不能一见到「内推」就杀。
func TestIsRecruitAd(t *testing.T) {
	cases := []struct {
		name    string
		title   string
		content string
		want    bool
	}{
		{
			name:    "米哈游内推码广告（带编号列表，曾被误判为面经）",
			title:   "米哈游后端/服务器内推｜内推码052BT",
			content: "米哈游2027届校招，后端/服务器方向岗位：\n\n🔹 后端开发实习生\n负责服务端系统技术方案设计与开发维护。\n\n投递方式：\n1. 校招内推码：052BT\n2. 可通过上方具体岗位链接直接投递",
			want:    true,
		},
		{
			name:    "秋招正式批广告（带「项目经历」等备赛建议）",
			title:   "[米哈游]米哈游2027秋招正式批开启啦！米哈游内推码TTTGC",
			content: "适合投递的同学：\n🌟 想做游戏客户端 / 服务端\n📌 项目经历最好突出技术难点\n唯一内推码: TTTGC\n校招内推码：TTTGC",
			want:    true,
		},
		{
			name:    "PDD 校招内推",
			title:   "拼多多集团-PDD｜「2027届校招内推」已启动！",
			content: "✅ 流程加速，内推简历优先筛选\n【招聘详情】\n热招岗位：研发/产品/市场\n招聘对象：海内外院校2027届应届毕业生",
			want:    true,
		},
		{
			name:    "真面经末尾挂内推码（不能误杀）",
			title:   "滴滴 强化学习 一面",
			content: "8月25\n1.项目当中强化学习框架怎么搭建的\n2.verl框架当中agent loop是怎么实现的\n3.平时有用过coding agent吗？\n面试官很nice，反问环节问了下团队方向，内推码可以私信我",
			want:    false,
		},
		{
			name:    "纯面经，没有任何广告词",
			title:   "腾讯 后端开发 二面",
			content: "1. 自我介绍\n2. Go 的 GMP 模型\n3. 面试官问项目",
			want:    false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &SitePost{Title: c.title, Content: c.content}
			if got := IsRecruitAd(p); got != c.want {
				t.Errorf("IsRecruitAd = %v, 期望 %v", got, c.want)
			}
		})
	}
}

// TestLooksLikeInterviewRejectsAds 保证广告在粗筛这一层就被挡住，
// 而不是靠后面的「零题目清理」兜底——那一步会先把广告写进库再删。
func TestLooksLikeInterviewRejectsAds(t *testing.T) {
	ads := []SitePost{
		{Title: "米哈游后端/服务器内推｜内推码052BT", Content: "投递方式：\n1. 校招内推码：052BT\n2. 岗位链接直接投递"},
		{Title: "腾讯内推，腾讯音乐内推码分享", Content: "内推码：ABCD，可内推，招聘详情见链接"},
		{Title: "库洛2026年2027届毕业生秋招", Content: "内推入职奖励可以55分。校招内推码: M9Y69BY"},
	}
	for _, p := range ads {
		p := p
		if LooksLikeInterview(&p) {
			t.Errorf("广告帖未被过滤: %q", p.Title)
		}
	}
}

// TestLooksLikeInterviewKeepsReal 粗筛宁松勿严：真面经必须留下。
func TestLooksLikeInterviewKeepsReal(t *testing.T) {
	reals := []SitePost{
		{Title: "滴滴 强化学习 一面", Content: "1.项目当中强化学习框架怎么搭建的\n2.verl框架当中agent loop是怎么实现的"},
		{Title: "腾讯 后端开发 二面", Content: "1. 自我介绍\n2. GMP 模型"},
		{Title: "记一次字节面试", Content: "面试官先让我自我介绍，然后手撕了一道接雨水。"},
	}
	for _, p := range reals {
		p := p
		if !LooksLikeInterview(&p) {
			t.Errorf("真面经被误杀: %q", p.Title)
		}
	}
}

// TestLooksLikeInterviewRejectsEmpty 打卡动态这类空内容是主要噪声源
// （实测占抓取量的 ~20%），必须直接丢掉。
func TestLooksLikeInterviewRejectsEmpty(t *testing.T) {
	p := SitePost{Title: "", Content: ""}
	if LooksLikeInterview(&p) {
		t.Error("空内容不应被判定为面经")
	}
}

// TestLooksLikeContentPage 覆盖三种「200 但不是内容」的返回。
//
// 这三种都由实测样本确认，且**状态码都是 200**，只能靠语义判断：
//   - 6KB 骨架页（无 SSR 数据）
//   - 82KB 兜底页（contentData 是空对象 {}）—— 早先只查子串，这类被漏掉了
//   - 空正文但带 uuid 的打卡动态 —— 这类是合法的，必须放行
func TestLooksLikeContentPage(t *testing.T) {
	shell := []byte(`<!DOCTYPE html><html lang="zh"><head><title>牛客网</title></head><body><div id="app"></div></body></html>`)

	// contentData 是空对象：82KB 兜底页的形态
	emptyCD := []byte(`<html><script>window.__INITIAL_STATE__=` +
		`{"prefetchData":{"2":{"ssrCommonData":{"contentData":{}}}}}` + `</script></html>`)

	// 有 uuid、正文为空：打卡动态，合法
	daka := []byte(`<html><script>window.__INITIAL_STATE__=` +
		`{"prefetchData":{"2":{"ssrCommonData":{"contentData":{"uuid":"abc123","content":""}}}}}` +
		`</script></html>`)

	real := []byte(`<html><script>window.__INITIAL_STATE__=` +
		`{"prefetchData":{"2":{"ssrCommonData":{"contentData":{"id":1,"uuid":"u1","content":"<p>hi</p>"}}}}}` +
		`</script></html>`)

	cases := []struct {
		name string
		body []byte
		want bool
	}{
		{"6KB 骨架页", shell, false},
		{"contentData 为空对象（82KB 兜底页）", emptyCD, false},
		{"打卡动态（有 uuid 无正文）", daka, true},
		{"正常内容页", real, true},
		{"空响应", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := LooksLikeContentPage(c.body); got != c.want {
				t.Errorf("LooksLikeContentPage = %v，期望 %v", got, c.want)
			}
		})
	}
}

// TestExtractStateJSONHandlesBracesInStrings 覆盖一个真实发生过的解析 bug。
//
// 早先用正则 `window\.__INITIAL_STATE__=(\{.*?\});` 截取 SSR JSON，
// 而 `.*?` 会在**字符串字面量内部**的 `};` 处提前收尾。
// 牛客面经正文经常内嵌代码，于是受影响的恰恰是算法/手撕类帖子——
// 66 条样本里有 1 条因此报 "unexpected end of JSON input"。
func TestExtractStateJSONHandlesBracesInStrings(t *testing.T) {
	// 关键：JSON 字符串里含有 `}` 紧跟 `;`，以及转义引号
	code := `public V get(K key) {\n    lock.lock();\n    return cache.get(key);\n};\n// 注意 \"引号\"`
	state := `{"prefetchData":{"2":{"ssrCommonData":{"contentData":{"id":1,"content":"` + code + `"}}}}};`
	html := []byte(`<html><script>window.__INITIAL_STATE__=` + state + `</script></html>`)

	got, err := extractStateJSON(html)
	if err != nil {
		t.Fatalf("提取失败: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(got, &root); err != nil {
		t.Fatalf("提取出来的不是完整 JSON（正是旧正则的失败方式）: %v", err)
	}
	cd := digMap(root, "prefetchData", "2", "ssrCommonData", "contentData")
	if cd == nil {
		t.Fatal("提取到的 JSON 里没有 contentData")
	}
	if id, _ := cd["id"].(float64); id != 1 {
		t.Errorf("contentData.id = %v，期望 1", cd["id"])
	}
}

// TestExtractStateJSONErrors 保证异常输入返回错误而不是静默给出半个 JSON。
func TestExtractStateJSONErrors(t *testing.T) {
	cases := map[string]string{
		"没有标记":     `<html><body>nope</body></html>`,
		"标记后不是对象":  `<script>window.__INITIAL_STATE__=[1,2]</script>`,
		"JSON 未闭合": `<script>window.__INITIAL_STATE__={"a":{"b":1}</script>`,
	}
	for name, html := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := extractStateJSON([]byte(html)); err == nil {
				t.Error("期望报错，实际成功")
			}
		})
	}
}

// TestShardOfPartitions 保证分片是「划分」而不是「抽样」：
// 每个 URL 必须恰好落进一个分片，否则多进程并行会漏抓或重复抓。
func TestShardOfPartitions(t *testing.T) {
	var urls []string
	for i := 0; i < 3000; i++ {
		urls = append(urls, "https://www.nowcoder.com/feed/main/detail/"+strconv.Itoa(i))
	}
	const n = 3
	counts := make([]int, n)
	for _, u := range urls {
		hits := 0
		for i := 0; i < n; i++ {
			if ShardOf(u, i, n) {
				counts[i]++
				hits++
			}
		}
		if hits != 1 {
			t.Fatalf("URL %s 落进了 %d 个分片，应为 1 个", u, hits)
		}
	}
	total := 0
	for i, c := range counts {
		total += c
		if c < len(urls)/n/2 {
			t.Errorf("分片 %d 只有 %d 条，分布明显不均: %v", i, c, counts)
		}
	}
	if total != len(urls) {
		t.Errorf("分片并集 %d != 全集 %d", total, len(urls))
	}
}

// TestShardOfIsStable 保证分片归属对同一个 URL 恒定。
//
// 这一点很关键：sitemap 顺序会变（站点随时新增内容），
// 若按下标切片，同一个 URL 会在不同轮次落到不同分片，
// 「本分片已抓完」的判断就失效了。
func TestShardOfIsStable(t *testing.T) {
	u := "https://www.nowcoder.com/discuss/918260041663119360"
	first := ShardOf(u, 1, 3)
	for i := 0; i < 100; i++ {
		if ShardOf(u, 1, 3) != first {
			t.Fatal("分片归属不稳定")
		}
	}
	if !ShardOf(u, 0, 1) {
		t.Error("n=1 时所有 URL 都应属于分片 0")
	}
}

// TestBreakerTripAndWait 覆盖跨进程冷却的核心语义：
// 触发后要挡住后来者，且「更长的冷却不被更短的覆盖」。
func TestBreakerTripAndWait(t *testing.T) {
	dir := t.TempDir()
	path := BreakerPath(dir)

	b := NewBreaker(path, "0/3")
	if d := b.Remaining(); d != 0 {
		t.Fatalf("初始不应有冷却，却剩 %s", d)
	}
	if err := b.Trip(time.Minute, "测试触发"); err != nil {
		t.Fatal(err)
	}
	if d := b.Remaining(); d <= 0 || d > time.Minute {
		t.Fatalf("触发后剩余时长异常: %s", d)
	}

	// 另一个进程读到同一个状态——这正是「跨进程」的意义
	other := NewBreaker(path, "1/3")
	if other.Remaining() <= 0 {
		t.Fatal("别的 worker 读不到冷却状态")
	}
	if st := other.Load(); st.By != "0/3" || st.Reason != "测试触发" {
		t.Fatalf("状态内容不对: %+v", st)
	}

	// 更长的不被更短的缩短
	if err := other.Trip(time.Second, "短冷却"); err != nil {
		t.Fatal(err)
	}
	if d := other.Remaining(); d < 30*time.Second {
		t.Fatalf("冷却被错误缩短到 %s", d)
	}

	if err := b.Clear(); err != nil {
		t.Fatal(err)
	}
	if d := b.Remaining(); d != 0 {
		t.Fatalf("Clear 之后不该还有冷却，剩 %s", d)
	}
}

// TestIsRecruitAdCatchesBareReferral 覆盖「不带码字的内推广告」这个漏洞。
//
// 原来 reKwAdStrong 只列了 `内推码`，于是「华为27届校招内推」「阿里27届校招内推开始啦！」
// 这类标题里只有「内推」两字的广告**一个强广告词都命中不了**，全部漏过粗筛。
// 实测补上裸 `内推` 后新增命中 67 篇，其中只有 5 篇真有有效题（精确率约 92%）。
func TestIsRecruitAdCatchesBareReferral(t *testing.T) {
	ads := []*SitePost{
		{Title: "华为27年应届生秋招内推", Content: "秋招岗位投递传送门：https://www.nowcoder.com/jobs/detail/458158?jobId=458158"},
		{Title: "阿里27届校招内推开始啦！", Content: "内推岗位：控股集团：AI数据工程师"},
		{Title: "华为27届校招内推", Content: "欢迎投递，岗位众多"},
		{Title: "米哈游内推～", Content: "社招校招都有岗位"},
	}
	for _, p := range ads {
		if !IsRecruitAd(p) {
			t.Errorf("内推广告未被识别: %q", p.Title)
		}
		if LooksLikeInterview(p) {
			t.Errorf("内推广告未被粗筛拦下: %q", p.Title)
		}
	}

	// 反向：真面经提到内推（结尾挂内推码）不能被误杀——面试信号够多时不算广告
	real := &SitePost{
		Title: "2027面经|内推码分享",
		Content: "一面问了 GMP 调度模型和 etcd 的 watch 机制。二面聊了项目，面试官还问了 Redis 缓存穿透。" +
			"三面是 HR 面，聊了职业规划。内推码可以私信我。",
	}
	if IsRecruitAd(real) {
		t.Error("真面经（面试信号充足）被误判为广告")
	}
}

// TestCreatedAtPrefersEntitySpecificField 覆盖「发布时间字段名跟实体类型有关」。
//
// 实测：/feed/ 的动态给 createdAt，/discuss/ 的讨论与题解给 createTime，
// 两类都另有 editTime（最后编辑时间）。只读 createdAt 会让 36% 的帖子
// 退到 editTime——被编辑过的帖子日期就偏晚了，按时间筛选会跟着偏。
func TestCreatedAtPrefersEntitySpecificField(t *testing.T) {
	mk := func(fields map[string]any) int64 {
		cd := map[string]any{"id": "1", "title": "x", "content": "y"}
		for k, v := range fields {
			cd[k] = v
		}
		// 结构与真实页面一致：prefetchData.<n>.ssrCommonData.contentData
		raw, _ := json.Marshal(map[string]any{
			"prefetchData": map[string]any{
				"2": map[string]any{"ssrCommonData": map[string]any{"contentData": cd}},
			},
		})
		html := `<html><script>window.__INITIAL_STATE__=` + string(raw) + `;</script></html>`
		p, err := ExtractContent([]byte(html))
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		return p.CreatedAt
	}
	cases := []struct {
		name   string
		fields map[string]any
		want   int64
	}{
		{"动态只有 createdAt", map[string]any{"createdAt": 1650097894000, "editTime": 1788421064000}, 1650097894000},
		{"讨论只有 createTime", map[string]any{"createTime": 1787811123000, "editTime": 1788421064000}, 1787811123000},
		{"题解只有 createTime", map[string]any{"createTime": 1787811123000, "editTime": 1788421064000}, 1787811123000},
		{"两者都有时优先 createdAt", map[string]any{"createdAt": 1650097894000, "createTime": 1787811123000}, 1650097894000},
		{"都没有才退 editTime", map[string]any{"editTime": 1788421064000}, 1788421064000},
	}
	for _, c := range cases {
		if got := mk(c.fields); got != c.want {
			t.Errorf("%s: CreatedAt=%d，期望 %d", c.name, got, c.want)
		}
	}
}
