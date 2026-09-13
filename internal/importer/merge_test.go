package importer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/climber47/nc-interview/internal/model"
)

// TestClusterMergesSynonyms 覆盖同义题合并：两种问法应落进同一簇，
// 且代表措辞必须钉在合并目标上。
func TestClusterMergesSynonyms(t *testing.T) {
	mk := func() []row {
		return []row{
			{text: "请做一下自我介绍", postID: 1},
			{text: "自我介绍一下", postID: 2},
			{text: "讲讲 MVCC", postID: 3},
		}
	}
	// 不合并时是 3 个簇
	if qs := Cluster(mk()); len(qs) != 3 {
		t.Fatalf("未合并时应为 3 簇，实际 %d", len(qs))
	}

	merges := map[string]string{Norm("请做一下自我介绍"): Norm("自我介绍一下")}
	canon := map[string]string{Norm("自我介绍一下"): "自我介绍一下"}
	qs := ClusterWithMerges(mk(), merges, canon)
	if len(qs) != 2 {
		t.Fatalf("合并后应为 2 簇，实际 %d", len(qs))
	}
	var intro *int
	for i := range qs {
		if qs[i].Canonical == "自我介绍一下" {
			intro = &i
		}
	}
	if intro == nil {
		t.Fatalf("找不到合并后的簇，实际：%v", questionTexts(qs))
	}
	if qs[*intro].N != 2 {
		t.Errorf("合并后频次应为 2，实际 %d", qs[*intro].N)
	}
	if len(qs[*intro].Occurs) != 2 {
		t.Errorf("合并后应有 2 条出现记录，实际 %d", len(qs[*intro].Occurs))
	}
}

// TestClusterMergeKeepsNormInvariant 是这次改动的核心不变量：
// 合并后仍必须满足 Norm(canonical) == norm。
//
// 大模型标注是**按 norm 存**的（llm_labels 表）。一旦代表措辞被簇内更长的变体
// 抢走，norm 就不等于合并目标，合并后的整簇会静默丢掉标注——
// 这类「不报错但数据没了」的问题在本项目已经踩过一次。
func TestClusterMergeKeepsNormInvariant(t *testing.T) {
	rows := []row{
		{text: "自我介绍一下", postID: 1},
		// 这条更长，是簇内「最长代表措辞」的默认赢家，必须被 forceRep 压住
		{text: "请先做一个简单的自我介绍，重点讲项目", postID: 2},
	}
	merges := map[string]string{
		Norm("请先做一个简单的自我介绍，重点讲项目"): Norm("自我介绍一下"),
	}
	canon := map[string]string{Norm("自我介绍一下"): "自我介绍一下"}
	qs := ClusterWithMerges(rows, merges, canon)
	if len(qs) != 1 {
		t.Fatalf("应为 1 簇，实际 %d：%v", len(qs), questionTexts(qs))
	}
	if qs[0].Canonical != "自我介绍一下" {
		t.Errorf("代表措辞应被钉在合并目标上，实际 %q", qs[0].Canonical)
	}
	if got := Norm(qs[0].Canonical); got != qs[0].Norm {
		t.Errorf("不变式被破坏：Norm(canonical)=%q 但 norm=%q（会导致按 norm 存的标注丢失）", got, qs[0].Norm)
	}
}

// TestLoadMergesMissingFileIsNoop 缺文件时必须等价于「没有合并」，便于冷启动与测试。
func TestLoadMergesMissingFileIsNoop(t *testing.T) {
	m, c, err := LoadMerges("testdata/definitely-not-here.json")
	if err != nil {
		t.Fatalf("缺文件不该报错，实际 %v", err)
	}
	if len(m) != 0 || len(c) != 0 {
		t.Fatalf("缺文件应返回空映射，实际 %v / %v", m, c)
	}
}

func questionTexts(qs []model.Question) []string {
	var out []string
	for _, q := range qs {
		out = append(out, q.Canonical)
	}
	return out
}

// TestLoadMergesResolvesChains 覆盖链式映射：A→B 且 B→C 时，A 必须直接落到 C。
//
// 第二轮「合并目标之间再去重」会大量产生这种链。如果只做单层查表，
// A 会被归到 B，而 B 自己的簇已经并进了 C——A 于是变成一个只有自己的孤簇，
// 频次也一起丢掉。这种错误不会报错，只会让数字悄悄偏小。
func TestLoadMergesResolvesChains(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "merges.json")
	body := `{"merges":[{"from":"甲问法","to":"乙问法"},{"from":"乙问法","to":"丙问法"}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m, canon, err := LoadMerges(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := m[Norm("甲问法")], Norm("丙问法"); got != want {
		t.Errorf("链式映射未解析：甲问法 → %q，期望直接落到丙问法 %q", got, want)
	}
	if _, ok := m[Norm("丙问法")]; ok {
		t.Error("最终目标不该再出现在映射里（会造成自环）")
	}
	if _, ok := canon[Norm("丙问法")]; !ok {
		t.Error("最终目标的原文应保留在 canon 里")
	}

	// 端到端：三条问法应聚成一个簇，代表措辞是最终目标
	rows := []row{{text: "甲问法", postID: 1}, {text: "乙问法", postID: 2}, {text: "丙问法", postID: 3}}
	qs := ClusterWithMerges(rows, m, canon)
	if len(qs) != 1 {
		t.Fatalf("链式合并后应为 1 簇，实际 %d：%v", len(qs), questionTexts(qs))
	}
	if qs[0].Canonical != "丙问法" || qs[0].N != 3 {
		t.Errorf("期望代表措辞=丙问法 且 n=3，实际 %q n=%d", qs[0].Canonical, qs[0].N)
	}
	if Norm(qs[0].Canonical) != qs[0].Norm {
		t.Error("不变式被破坏：Norm(canonical) != norm")
	}
}

// TestPartitionSplitsCodeOverMerge 覆盖「代码初步合并 + 大模型精确合并」的分工。
//
// 代码的字符串规则会把「都含 prompt」的不同问题聚成一簇，而代码侧无法拆开；
// 大模型分区的作用就是**按组 id 强制合并 + 禁止跨组**。
func TestPartitionSplitsCodeOverMerge(t *testing.T) {
	rows := []row{
		{text: "prompt", postID: 1},
		{text: "微调与 Prompt 的区别是什么？", postID: 2},
		{text: "Prompt Cache是什么？", postID: 3},
		{text: "如何防止用户prompt攻击", postID: 4},
	}
	// 先确认：没有分区时，代码确实把它们合成一簇（这就是要修的问题）
	if qs := Cluster(rows); len(qs) != 1 {
		t.Fatalf("前提不成立：代码本应把它们错合成 1 簇，实际 %d 簇", len(qs))
	}

	// 大模型给出的三个组
	rules := &ClusterRules{
		PartGroup: map[string]string{
			NormKeepCase("prompt"):             "p0",
			NormKeepCase("微调与 Prompt 的区别是什么？"): "p1",
			NormKeepCase("Prompt Cache是什么？"):   "p2",
			NormKeepCase("如何防止用户prompt攻击"):     "p3",
		},
		PartCanon: map[string]string{
			"p0": "prompt", "p1": "微调与 Prompt 的区别是什么？",
			"p2": "Prompt Cache是什么？", "p3": "如何防止用户prompt攻击",
		},
	}
	qs := ClusterWithRules(rows, rules)
	if len(qs) != 4 {
		t.Fatalf("按分区应拆成 4 簇，实际 %d：%v", len(qs), questionTexts(qs))
	}
	for _, q := range qs {
		if q.N != 1 {
			t.Errorf("拆分后每簇应各 1 条，实际 %q n=%d", q.Canonical, q.N)
		}
	}
}

// TestPartitionGroupsByIDNotString 是分区机制的核心不变量：
// **同组必然合并**（哪怕字符串毫无相似度），**异组必然不合并**（哪怕只差一点）。
func TestPartitionGroupsByIDNotString(t *testing.T) {
	// 两条字符串毫不相干，但 LLM 说它们是同一道题 → 必须合
	same := []row{{text: "讲讲你对 RAG 的理解", postID: 1}, {text: "检索增强生成是什么", postID: 2}}
	r1 := &ClusterRules{
		PartGroup: map[string]string{NormKeepCase("讲讲你对 RAG 的理解"): "p0", NormKeepCase("检索增强生成是什么"): "p0"},
		PartCanon: map[string]string{"p0": "讲讲你对 RAG 的理解"},
	}
	if qs := ClusterWithRules(same, r1); len(qs) != 1 || qs[0].N != 2 {
		t.Fatalf("同组必须合并，实际 %d 簇", len(qs))
	}
	if qs := ClusterWithRules(same, r1); qs[0].Canonical != "讲讲你对 RAG 的理解" {
		t.Errorf("组标题应取自 PartCanon，实际 %q", qs[0].Canonical)
	}

	// 两条字符串几乎一样，但 LLM 说它们是两道题 → 必须拆
	diff := []row{{text: "讲一下对React的理解", postID: 1}, {text: "讲一下对ReAct的理解", postID: 2}}
	r2 := &ClusterRules{
		PartGroup: map[string]string{NormKeepCase("讲一下对React的理解"): "p0", NormKeepCase("讲一下对ReAct的理解"): "p1"},
		PartCanon: map[string]string{"p0": "讲一下对React的理解", "p1": "讲一下对ReAct的理解"},
	}
	if qs := ClusterWithRules(diff, r2); len(qs) != 2 {
		t.Fatalf("异组必须拆开（React ≠ ReAct），实际 %d 簇：%v", len(qs), questionTexts(qs))
	}
}

func TestLoadPartitionMissingFileIsNoop(t *testing.T) {
	p, err := LoadPartition("testdata/definitely-not-here.json")
	if err != nil || p != nil {
		t.Fatalf("缺文件应返回 (nil, nil)，实际 (%v, %v)", p, err)
	}
}

// TestClusterNormStaysUniqueWhenCanonicalsCollide 覆盖唯一性兜底。
//
// norm 在库里是唯一键。而分区/合并表指定的代表措辞**归一化后可能撞车**：
// 实测「介绍mysql」与「-- MySQL --」都归一化成 mysql（「介绍」是填充词），
// 直接触发 Error 1062，让整次重建失败、questions 表被留在清空状态。
func TestClusterNormStaysUniqueWhenCanonicalsCollide(t *testing.T) {
	cases := [][2]string{
		{"介绍mysql", "-- MySQL --"},
		{"&和&&的区别", "==和===的区别"},
	}
	for _, c := range cases {
		rows := []row{{text: c[0], postID: 1}, {text: c[1], postID: 2}}
		rules := &ClusterRules{
			PartExact: map[string]string{c[0]: "p0", c[1]: "p1"},
			PartGroup: map[string]string{
				NormKeepCase(c[0]): "p0",
				NormKeepCase(c[1]): "p1",
			},
			PartCanon: map[string]string{"p0": c[0], "p1": c[1]},
		}
		qs := ClusterWithRules(rows, rules)
		if len(qs) != 2 {
			t.Fatalf("%v / %v：应为 2 簇，实际 %d", c[0], c[1], len(qs))
		}
		if qs[0].Norm == qs[1].Norm {
			t.Errorf("%v / %v：norm 撞车了，都是 %q（会触发 Error 1062）", c[0], c[1], qs[0].Norm)
		}
		for _, q := range qs {
			if q.Norm == "" {
				t.Errorf("norm 不能为空")
			}
		}
	}
}
