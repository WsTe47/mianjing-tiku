package model

import (
	"encoding/json"
	"testing"
)

// TestQuestionJSONContract 锁定 Question 的 JSON 字段名。
//
// 背景：这几个字段名是**前后端的隐式契约**，前端按名字读取并渲染「原文 ↗」链接。
// 曾经把 `occurrences` 误改成 `occurs`，前端 `(q.occurrences || [])` 的兜底让失败
// 完全静默——链接消失、控制台无报错、接口也返回 200。靠人眼很难发现，所以用测试钉住。
func TestQuestionJSONContract(t *testing.T) {
	q := Question{
		ID:        1,
		Canonical: "MCP 和 Skill 的区别是什么？",
		Category:  "Agent/AI",
		Topic:     "MCP / Skill",
		N:         3,
		LLMMerged: "MCP 与 Skill",
		LLMDomain: "Agent 与大模型工程",
		LLMCat:    "MCP 协议与知识冲突",
		Variants:  []string{"MCP跟skill区别"},
		Occurs: []Occur{{
			PostID: 100, RawText: "MCP 和 Skill 的区别是什么？",
			Company: "千寻智能", RoundGroup: "一面", JobGroup: "平台/Infra",
			Date: "2026-08-08", Title: "千寻智能 平台 一面",
			URL: "https://www.nowcoder.com/feed/main/detail/0710cc2eecc9444299dc38be78de3f76",
		}},
	}
	b, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}

	// 前端依赖的顶层字段名
	for _, k := range []string{
		"id", "canonical", "category", "topic", "n",
		"llmMerged", "llmDomain", "llmCat",
		"variants", "occurrences",
	} {
		if _, ok := m[k]; !ok {
			t.Errorf("Question JSON 缺少字段 %q（前端会静默失效）", k)
		}
	}
	// 旧名不能复活，避免两套字段并存造成混乱
	if _, ok := m["occurs"]; ok {
		t.Error("Question JSON 不应再出现 occurs；前端读的是 occurrences")
	}

	// occurrences 里必须带原文链接
	occs, ok := m["occurrences"].([]any)
	if !ok || len(occs) == 0 {
		t.Fatalf("occurrences 不是非空数组: %#v", m["occurrences"])
	}
	first, _ := occs[0].(map[string]any)
	for _, k := range []string{"postId", "url", "company", "roundGroup", "jobGroup", "date", "title"} {
		if v, ok := first[k]; !ok || v == "" {
			t.Errorf("occurrence 缺少字段 %q —— 前端「原文 ↗」链接依赖它", k)
		}
	}
}
