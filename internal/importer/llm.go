package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/climber47/nc-interview/internal/model"
	"github.com/climber47/nc-interview/internal/store"
)

// taxonomyFile 是模型探索出的细粒度分类体系。
type taxonomyFile struct {
	Domains []struct {
		Name       string `json:"name"`
		Categories []struct {
			Name     string   `json:"name"`
			Desc     string   `json:"desc"`
			Examples []string `json:"examples"`
		} `json:"categories"`
	} `json:"domains"`
	Unclassified struct {
		Name  string `json:"name"`
		Desc  string `json:"desc"`
		Count int    `json:"count"`
	} `json:"unclassified"`
}

// batchFile 是单个归类子代理产出的结果。
//
// ⚠️ 归类的键是 **text（题目原文）**，不是 id。
//
// 这不是风格选择，是一次真实事故换来的：
// 早先 out/*.json 只存 {id, cat}，而 id 是 questions 表的自增主键——
// 每次 `make rebuild` 都会 TRUNCATE 重建，id 全部重排。
// 于是一份旧结果被重新 apply 时，**1478 条标注被贴到了完全无关的题目上**
// （实测旧 id 与当前题目的匹配率是 0/212）。
// 更糟的是它没有任何报错：UPDATE ... WHERE id=? 对每个 id 都「成功」。
//
// 题目原文是稳定的业务键，id 不是。所以 text 是必填字段。
type batchFile struct {
	Batch       int `json:"batch"`
	Assignments []struct {
		ID   int64  `json:"id,omitempty"` // 仅用于人工核对，导入时不用
		Text string `json:"text"`
		Cat  string `json:"cat"`
	} `json:"assignments"`
}

// mergeFile 是「细分类 → 合并后的可用类」的映射。
// 由人（或再跑一次模型）在看过各细分类的条数后编写。
// 形如：{"Agent 工程": ["Agent 总体架构与设计", "Agent 运行时与选型", ...], "其他": ["..."]}
type mergeFile map[string][]string

// LLMStats 汇报一次 LLM 归类导入的结果。
type LLMStats struct {
	Taxonomy    int
	Merged      int
	Batches     int
	Assignments int
	UnknownCat  map[string]int // 模型返回了但体系里没有的分类名
	Unmerged    []string       // 没被任何合并类覆盖的细分类名
	NoCategory  int            // 非题目条数
	NoText      int            // 因缺 text 而无法安全落地的条数
}

// LoadLLM 读取 dir 下的 taxonomy.json、merge.json 与 out/*.json，写入数据库。
//
// 三步分离是刻意的：
//   - taxonomy.json 由模型探索产生（细粒度）
//   - merge.json 是人工决定的合并规则
//   - out/*.json 是逐条归类结果
//
// 因此**调整合并规则只需重跑这一步，不必再调模型**。
func LoadLLM(ctx context.Context, st *store.Store, dir string, log func(string, ...any)) (LLMStats, error) {
	if log == nil {
		log = func(string, ...any) {}
	}
	var stats LLMStats
	stats.UnknownCat = map[string]int{}

	// 1) 分类体系
	raw, err := os.ReadFile(filepath.Join(dir, "taxonomy.json"))
	if err != nil {
		return stats, fmt.Errorf("读取 taxonomy.json 失败: %w", err)
	}
	var tf taxonomyFile
	if err := json.Unmarshal(raw, &tf); err != nil {
		return stats, fmt.Errorf("解析 taxonomy.json 失败: %w", err)
	}

	// 2) 合并规则（可选）：细分类 → 可用类
	mergedOf := map[string]string{}
	if mb, err := os.ReadFile(filepath.Join(dir, "merge.json")); err == nil {
		var mf mergeFile
		if err := json.Unmarshal(mb, &mf); err != nil {
			return stats, fmt.Errorf("解析 merge.json 失败: %w", err)
		}
		for merged, names := range mf {
			for _, n := range names {
				mergedOf[n] = merged
			}
		}
		stats.Merged = len(mf)
		log("合并规则：%d 个可用类覆盖 %d 个细分类", len(mf), len(mergedOf))
	} else {
		log("未找到 merge.json，细分类将直接作为可用类")
	}

	// 3) 写 taxonomy 表
	var entries []model.TaxonomyEntry
	known := map[string]bool{}
	for _, dom := range tf.Domains {
		for _, c := range dom.Categories {
			m := mergedOf[c.Name]
			if m == "" {
				m = c.Name
				stats.Unmerged = append(stats.Unmerged, c.Name)
			}
			known[c.Name] = true
			entries = append(entries, model.TaxonomyEntry{
				Name: c.Name, Domain: dom.Name, Merged: m, Description: c.Desc,
			})
		}
	}
	if u := tf.Unclassified.Name; u != "" {
		known[u] = true
		entries = append(entries, model.TaxonomyEntry{
			Name: u, Domain: "非知识点", Merged: u, Description: tf.Unclassified.Desc,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Domain != entries[j].Domain {
			return entries[i].Domain < entries[j].Domain
		}
		return entries[i].Name < entries[j].Name
	})
	if err := st.ApplyTaxonomy(ctx, entries); err != nil {
		return stats, fmt.Errorf("写入分类体系失败: %w", err)
	}
	stats.Taxonomy = len(entries)
	log("分类体系：%d 个细分类，归入 %d 个顶层域", len(entries), len(tf.Domains))

	// 4) 读归类结果，按**题目原文的归一化文本**建映射（不碰 id）
	byNorm := map[string]string{}
	files, _ := filepath.Glob(filepath.Join(dir, "out", "batch-*.json"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return stats, err
		}
		var bf batchFile
		if err := json.Unmarshal(b, &bf); err != nil {
			return stats, fmt.Errorf("解析 %s 失败: %w", filepath.Base(f), err)
		}
		stats.Batches++
		for _, a := range bf.Assignments {
			if a.Cat == "" {
				continue
			}
			if !known[a.Cat] {
				stats.UnknownCat[a.Cat]++
			}
			if a.Cat == tf.Unclassified.Name {
				stats.NoCategory++
			}
			text := strings.TrimSpace(a.Text)
			if text == "" {
				// 没有 text 的结果**无法安全落地**：旧格式只有 id，而 id 会随重建重排，
				// 按 id 写入等于随机贴标签（实测匹配率 0/212）。
				// 宁可跳过并计数，也不要静默出错。
				stats.NoText++
				continue
			}
			n := Norm(text)
			if n == "" {
				n = text
			}
			byNorm[model.ClipRunes(n, model.MaxNormRunes)] = a.Cat
		}
	}
	if len(byNorm) == 0 {
		return stats, fmt.Errorf("没有读到任何可用的归类结果（%s/out/batch-*.json 缺少 text 字段？）", dir)
	}
	n, err := st.ApplyClassificationsByNorm(ctx, byNorm)
	if err != nil {
		return stats, fmt.Errorf("写入归类失败: %w", err)
	}
	stats.Assignments = n
	log("归类结果：%d 批 → 命中库内 %d 条问题（另有 %d 条因缺 text 被跳过）",
		stats.Batches, n, stats.NoText)
	return stats, nil
}
