package importer

import (
	"encoding/json"
	"fmt"
	"os"
)

// partitionPath 是「大模型精确分区」表的路径。
//
// 分工：**代码做初步合并（候选生成），大模型做精确合并（判定）**。
// 代码的字符串规则（相同 / 包含 / 编辑距离）召回够但精度不足——
// 例如「微调与 Prompt 的区别」「Prompt Cache 是什么」「prompt 注入怎么解决」
// 会仅因为都含 prompt 就被聚成一簇，而代码侧**没有任何机制能把它拆开**。
//
// 所以流程改成：
//  1. 代码先把 n>=2 的簇（= 所有可能合错的地方）导出来当候选；
//  2. 大模型逐个簇做分区：把成员划成若干「真的在问同一件事」的子组，并起标题；
//  3. 本表按 norm 落地，重建时**按组 id 合并**，组间绝不串。
//
// 按 norm 存的原因和 llm_labels 一样：换了 id、重建聚类都不会丢。
const partitionPath = "data/llm/partition.json"

// Partition 是大模型给出的精确分区。
type Partition struct {
	// Group 把成员写法映射到组 id。键是 NormKeepCase（**保留大小写**），
	// 因为 Norm 会把 ReAct / React 折叠成同一个键。
	Group map[string]string
	// Canon 把组 id 映射到组标题原文。
	Canon map[string]string
	// Exact 用**成员原文**做键。
	//
	// 为什么 norm 不够：reNonWord 会把标点全去掉，于是「&和&&的区别」与
	// 「==和===的区别」归一化后都是「和区别」——两个完全不同的题在 norm 层
	// 就不可区分了。原文键能把它们分开；norm 键作为兜底（原文略有出入时仍能命中）。
	Exact map[string]string
	// Size 记录每个组的成员数，仅用于日志。
	Size map[string]int
}

// LoadPartition 读取分区表；文件不存在时返回 nil（行为等价于「没有分区」）。
func LoadPartition(path string) (*Partition, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f struct {
		Groups []struct {
			Canonical string   `json:"canonical"`
			Members   []string `json:"members"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("解析 %s: %w", path, err)
	}
	p := &Partition{
		Group: make(map[string]string),
		Exact: make(map[string]string),
		Canon: make(map[string]string),
		Size:  make(map[string]int),
	}
	for i, g := range f.Groups {
		id := fmt.Sprintf("p%05d", i)
		if g.Canonical == "" || len(g.Members) == 0 {
			continue
		}
		p.Canon[id] = g.Canonical
		for _, m := range g.Members {
			if _, dup := p.Exact[m]; !dup {
				p.Exact[m] = id
			}
			n := clip(NormKeepCase(m), maxNormRunes)
			if n == "" {
				continue
			}
			// 同一个 norm 出现在两个组里时以先出现的为准，并保证组标题自己也在组内
			if _, dup := p.Group[n]; !dup {
				p.Group[n] = id
				p.Size[id]++
			}
		}
	}
	return p, nil
}
