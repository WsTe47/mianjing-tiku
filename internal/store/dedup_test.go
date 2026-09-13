package store

import "testing"

func TestNormalizeTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"美团内推美团内推码", "美团内推美团内推码"},
		{"美团内推 美团内推码", "美团内推美团内推码"},
		{"美团内推，美团内推码！", "美团内推美团内推码"},
		{"字节 后端 一面", "字节后端一面"},
		{"LRU", "lru"},
		{"  ", ""},
		{"...!!!", ""},
		{"C++ 与 Go 的区别？", "c与go的区别"},
	}
	for _, c := range cases {
		if got := NormalizeTitle(c.in); got != c.want {
			t.Errorf("NormalizeTitle(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

// TestDuplicatePostIDsCollapsesReposts 覆盖真实事故：
// 内推广告把同一份模板题单复制多遍，只应保留最早的一篇。
func TestDuplicatePostIDsCollapsesReposts(t *testing.T) {
	sets := []PostQuestionSet{
		{ID: 1, Title: "美团内推美团内推码", Questions: []int64{10, 11, 12, 13, 14}},
		{ID: 2, Title: "美团内推 美团内推码", Questions: []int64{10, 11, 12, 13, 14}},
		{ID: 3, Title: "美团内推美团内推码", Questions: []int64{10, 11, 12, 13}},     // 少一题，Jaccard=0.8
		{ID: 4, Title: "美团内推美团内推码", Questions: []int64{10, 11, 12, 99, 98}}, // Jaccard=0.43
		{ID: 5, Title: "字节 后端 一面", Questions: []int64{10, 11, 12, 13, 14}},     // 标题不同，不删
	}
	got := DuplicatePostIDs(sets, 0.8)
	want := []int64{2, 3}
	if len(got) != len(want) {
		t.Fatalf("应删 %v，实际 %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("应删 %v，实际 %v", want, got)
		}
	}
}

// TestDuplicatePostIDsKeepsDifferentContentSameTitle 标题撞车但内容不同时不能误删：
// 两个不同的人写「字节一面面经」是完全正常的。
func TestDuplicatePostIDsKeepsDifferentContentSameTitle(t *testing.T) {
	sets := []PostQuestionSet{
		{ID: 1, Title: "字节一面面经", Questions: []int64{1, 2, 3, 4, 5}},
		{ID: 2, Title: "字节一面面经", Questions: []int64{6, 7, 8, 9, 10}},
	}
	if got := DuplicatePostIDs(sets, 0.8); len(got) != 0 {
		t.Fatalf("内容不同不该删，实际删了 %v", got)
	}
}

// TestDuplicatePostIDsIgnoresEmptyTitleAndEmptyQuestions 空标题与空题集不参与去重。
func TestDuplicatePostIDsIgnoresEmptyTitleAndEmptyQuestions(t *testing.T) {
	sets := []PostQuestionSet{
		{ID: 1, Title: "", Questions: []int64{1, 2}},
		{ID: 2, Title: "。。。", Questions: []int64{1, 2}},
		{ID: 3, Title: "同一标题", Questions: nil},
		{ID: 4, Title: "同一标题", Questions: nil},
	}
	if got := DuplicatePostIDs(sets, 0.8); len(got) != 0 {
		t.Fatalf("空标题/空题集不该删，实际删了 %v", got)
	}
}

func TestJaccard(t *testing.T) {
	cases := []struct {
		a, b []int64
		want float64
	}{
		{[]int64{1, 2, 3}, []int64{1, 2, 3}, 1},
		{[]int64{1, 2, 3, 4}, []int64{1, 2, 3, 5}, 0.6},
		{[]int64{1, 2}, []int64{3, 4}, 0},
		{nil, []int64{1}, 0},
		{nil, nil, 0},
	}
	for _, c := range cases {
		if got := jaccard(c.a, c.b); got != c.want {
			t.Errorf("jaccard(%v,%v) = %v, 期望 %v", c.a, c.b, got, c.want)
		}
	}
}

// TestDuplicatePostIDsCrossTitleReposts 覆盖「换了标题的内推模板洗稿」：
// 同一份 25 道题的模板，标题分别写成迅雷/合合信息，两边都带内推码。
func TestDuplicatePostIDsCrossTitleReposts(t *testing.T) {
	q := make([]int64, 25)
	for i := range q {
		q[i] = int64(100 + i)
	}
	sets := []PostQuestionSet{
		{ID: 1, Title: "迅雷内推，迅雷面试经验分享，可礼貌取码", Questions: q},
		{ID: 2, Title: "迅雷内推码分享，迅雷服务器开发面经", Questions: q},
		{ID: 3, Title: "合合信息测试实习面经，合合信息内推", Questions: q},
	}
	got := DuplicatePostIDs(sets, 0.8)
	if len(got) != 2 {
		t.Fatalf("应删 2 篇跨标题洗稿，实际 %v", got)
	}
}

// TestDuplicatePostIDsKeepsTinySetsApart 是跨标题规则的核心不变量：
// 题集太小时 Jaccard 不可靠——两篇各 4 道题恰好重合不该算重复。
// 这正是实测里 44 对误配的来源（《社招复盘》≈《嵌入式百套面试题总结》）。
func TestDuplicatePostIDsKeepsTinySetsApart(t *testing.T) {
	q := []int64{1, 2, 3, 4}
	sets := []PostQuestionSet{
		{ID: 1, Title: "【社招复盘】面试，是人类讲故事能力的巅峰炫技", Questions: q},
		{ID: 2, Title: "嵌入式经典百套大厂面试题总结（含全套解析）", Questions: q},
		{ID: 3, Title: "面经别只存不看：三步把它变成你自己的复习清单", Questions: q},
	}
	if got := DuplicatePostIDs(sets, 0.8); len(got) != 0 {
		t.Fatalf("4 题小帖不该按跨标题规则判重，实际删了 %v", got)
	}
}

// TestDuplicatePostIDsCrossTitleNeedsHighOverlap 题集够大但重合度不够时不能删：
// 两篇不同公司的真面经可以共享大半八股，那不等于洗稿。
func TestDuplicatePostIDsCrossTitleNeedsHighOverlap(t *testing.T) {
	a := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}
	// 共享 18 道、各 20 道 → Jaccard = 18/22 ≈ 0.82，低于 0.9 阈值
	b := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 97, 98}
	sets := []PostQuestionSet{
		{ID: 1, Title: "百度后端一面，我跪了", Questions: a},
		{ID: 2, Title: "字节后端一面", Questions: b},
	}
	if got := DuplicatePostIDs(sets, 0.8); len(got) != 0 {
		t.Fatalf("Jaccard=%.2f 不该判重，实际删了 %v", jaccard(a, b), got)
	}
}
