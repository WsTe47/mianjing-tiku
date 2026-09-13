package importer

import (
	"testing"

	"github.com/climber47/nc-interview/internal/model"
)

// TestSplitInlineQuestions 覆盖「一行里连排了几十道题」这个最大的召回缺口。
//
// 真实形态：
//
//	「Java 基础== 和 equals 的区别？hashCode 为什么要一起重写？String 和 StringBuilder 区别？」
//	「请做一下自我介绍。你的专业偏开发，为什么选择测试开发岗位？你对测试开发岗位有什么了解？」
//
// 逐行抽取时整段只算一行，几十道题被当成一道（还被列宽截断）。
// 实测有 248 行是这种形态，其中一份「高频面试题清单」一段就有 50 多道题。
func TestSplitInlineQuestions(t *testing.T) {
	long := "Java 基础 == 和 equals 的区别？hashCode 为什么要一起重写？String 和 StringBuilder 区别和使用场景？接口和抽象类怎么选？"
	got := splitInlineQuestions(long)
	if len(got) != 4 {
		t.Fatalf("应拆出 4 道题，实际 %d 道: %v", len(got), got)
	}
	if got[0][:5] != "Java " {
		t.Errorf("第一段应以原开头开始: %q", got[0])
	}
	if got[3] != "接口和抽象类怎么选？" {
		t.Errorf("最后一段不对: %q", got[3])
	}

	// 不拆的情况一：短句。避免把「这样对吗？还是那样？」这种一问两半拆散。
	short := "这样对吗？还是那样？"
	if got := splitInlineQuestions(short); len(got) != 1 {
		t.Errorf("短句不该拆，实际拆成 %d 段", len(got))
	}

	// 不拆的情况二：长句但只有一个问号（那是一整道题）
	one := "请详细介绍一下你在上一个项目里是如何设计并落地这套离线评测体系的，包括指标选择、数据构造与结果验证"
	if got := splitInlineQuestions(one); len(got) != 1 {
		t.Errorf("只有一个问号的整句不该拆，实际拆成 %d 段", len(got))
	}

	// 不拆的情况三：长句且没有问号
	noQ := "这段是作者的复盘叙述，讲了他整个秋招的流程和感受，从头到尾没有出现任何问号但是很长很长很长"
	if got := splitInlineQuestions(noQ); len(got) != 1 {
		t.Errorf("没有问号的长句不该拆，实际拆成 %d 段", len(got))
	}
}

// TestExtractSplitsInlineQuestions 端到端验证：连排的整段要产出多道题。
func TestExtractSplitsInlineQuestions(t *testing.T) {
	content := "Java 基础 == 和 equals 的区别？hashCode 为什么要一起重写？String 和 StringBuilder 区别和使用场景？"
	posts := []model.Post{
		{ID: 1, Title: "某公司 Java 一面", Company: "某公司", Content: content},
	}
	rows, _ := Extract(posts)
	if len(rows) < 3 {
		t.Fatalf("连排三段应至少抽出 3 道题，实际 %d 道", len(rows))
	}
	for _, r := range rows {
		if len([]rune(r.text)) > 60 {
			t.Errorf("拆出来的题仍然过长（说明没拆开）: %.60q", r.text)
		}
	}
}
