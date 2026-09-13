// Command llmbatch 生成 / 检查 LLM 归类用的批次文件。
//
// 这是「三步解耦」里的第 ①②步之间的桥：
//
//	① 抽样探索分类体系  →  llmbatch -sample 300  →  data/llm/sample.json
//	② 分批归类          →  llmbatch -dir data/llm -size 150  →  batches/batch-NN.json
//	                        （子代理读批次文件、写 out/batch-NN.json）
//	③ 应用与合并        →  make llm-apply
//
// 为什么批次文件里**必须带 text**：归类的键是题目原文，不是数据库 id。
// id 会随 `make rebuild`（TRUNCATE + 重建）整体重排，用 id 落地会静默贴错标签——
// 项目里真发生过：1478 条标注被贴到无关题目上，且没有任何报错。
//
// 用法：
//
//	llmbatch -dir data/llm             生成待归类批次（只含尚未归类的题）
//	llmbatch -dir data/llm -all        重新生成全部题目的批次
//	llmbatch -sample 300               生成分类体系探索用的分层抽样
//	llmbatch -report                   只统计，不写文件
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/climber47/nc-interview/internal/config"
	"github.com/climber47/nc-interview/internal/model"
	"github.com/climber47/nc-interview/internal/store"
)

// batchItem 是喂给归类子代理的一条题目。
type batchItem struct {
	ID       int64  `json:"id"` // 仅供人工核对；落地时按 text 匹配
	Text     string `json:"text"`
	Cur      string `json:"cur"`      // 规则分类的模块，给模型当参考
	CurTopic string `json:"curTopic"` // 规则分类的主题
	N        int    `json:"n"`        // 出现次数，用于分层抽样
}

type batchOut struct {
	Batch int         `json:"batch"`
	Items []batchItem `json:"items"`
}

func main() {
	var (
		dir     = flag.String("dir", "data/llm", "输出目录")
		size    = flag.Int("size", 150, "每批条数")
		sample  = flag.Int("sample", 0, "生成分类体系探索用的分层抽样（取值即样本量）")
		all     = flag.Bool("all", false, "不限于未归类的题，取全部")
		report  = flag.Bool("report", false, "只统计，不写文件")
		testDSN = flag.String("dsn", "", "覆盖 DSN（默认读 .env）")
	)
	flag.Parse()

	cfg := config.Load()
	dsn := cfg.DSN
	if *testDSN != "" {
		dsn = *testDSN
	}
	st, err := store.Open(dsn)
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	labels, applied, err := st.LabelStats(ctx)
	if err != nil {
		log.Fatalf("读取标注统计失败: %v", err)
	}
	m, err := st.Meta(ctx, model.Filter{})
	if err != nil {
		log.Fatalf("读取库内统计失败: %v", err)
	}
	fmt.Printf("题库：%d 组问题，其中已含 LLM 归类 %d 组（标注表累计 %d 条）\n",
		m.Questions, applied, labels)

	qs, err := collect(ctx, st, *all)
	if err != nil {
		log.Fatalf("取题目失败: %v", err)
	}
	fmt.Printf("本次待处理 %d 组\n", len(qs))
	if *report {
		byCat := map[string]int{}
		for _, q := range qs {
			byCat[q.Cur]++
		}
		keys := make([]string, 0, len(byCat))
		for k := range byCat {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return byCat[keys[i]] > byCat[keys[j]] })
		fmt.Println("按规则模块分布：")
		for _, k := range keys {
			fmt.Printf("  %-12s %d\n", k, byCat[k])
		}
		return
	}

	if err := os.MkdirAll(*dir, 0o755); err != nil {
		log.Fatalf("建目录失败: %v", err)
	}

	if *sample > 0 {
		items := stratified(qs, *sample)
		path := filepath.Join(*dir, "sample.json")
		if err := writeJSON(path, items); err != nil {
			log.Fatalf("写 sample.json 失败: %v", err)
		}
		fmt.Printf("已写 %s（%d 条，按模块分层抽样）\n", path, len(items))
		return
	}

	bdir := filepath.Join(*dir, "batches")
	if err := os.RemoveAll(bdir); err != nil {
		log.Fatalf("清理旧批次失败: %v", err)
	}
	if err := os.MkdirAll(bdir, 0o755); err != nil {
		log.Fatalf("建批次目录失败: %v", err)
	}
	// 批号必须**接着已有结果**往下排。
	//
	// 否则每跑一次增量都会重新从 batch-01 开始，把上一次的 out/batch-01.json 覆盖掉，
	// 而 out/ 是归类结果的唯一载体——被覆盖就等于那批标注白跑了。
	// 项目里已经踩过一次：生成增量批次时把 22..27 编成了 01..06。
	start := nextBatchNumber(filepath.Join(*dir, "out"))
	if start > 1 {
		fmt.Printf("已有结果到 batch-%02d，本次从 batch-%02d 开始编号\n", start-1, start)
	}

	n := start - 1
	for i := 0; i < len(qs); i += *size {
		end := i + *size
		if end > len(qs) {
			end = len(qs)
		}
		n++
		b := batchOut{Batch: n, Items: qs[i:end]}
		path := filepath.Join(bdir, fmt.Sprintf("batch-%02d.json", n))
		if err := writeJSON(path, b); err != nil {
			log.Fatalf("写 %s 失败: %v", path, err)
		}
	}
	fmt.Printf("已写 %d 个批次到 %s（batch-%02d ~ batch-%02d，每批 %d 条）\n",
		n-(start-1), bdir, start, n, *size)
	fmt.Println("下一步：让子代理读 batches/batch-NN.json，按题目原文归类，")
	fmt.Println("        输出 out/batch-NN.json：{\"batch\":N,\"assignments\":[{\"id\":..,\"text\":\"原文\",\"cat\":\"细分类名\"}]}")
	fmt.Println("        ⚠️ text 必填——归属按 text 匹配，缺 text 的结果会被拒绝导入。")
}

// collect 取待处理的题目：默认只取尚未归类的。
func collect(ctx context.Context, st *store.Store, all bool) ([]batchItem, error) {
	f := model.Filter{Size: 500, Page: 1}
	var out []batchItem
	for {
		list, total, err := st.QueryQuestions(ctx, f)
		if err != nil {
			return nil, err
		}
		for _, q := range list {
			if !all && q.LLMCat != "" {
				continue
			}
			out = append(out, batchItem{
				ID: q.ID, Text: q.Canonical, Cur: q.Category, CurTopic: q.Topic, N: q.N,
			})
		}
		if f.Page*500 >= total {
			break
		}
		f.Page++
	}
	return out, nil
}

// stratified 按规则模块分层抽样，保证小众模块也有代表。
//
// 不分层的话，样本会被「后端工程」这种大模块淹没，
// 模型探索出的分类体系就只覆盖大头，尾部模块全落到「其他」。
func stratified(qs []batchItem, n int) []batchItem {
	groups := map[string][]batchItem{}
	for _, q := range qs {
		groups[q.Cur] = append(groups[q.Cur], q)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	rnd := rand.New(rand.NewSource(20260913)) // 固定种子，抽样可复现
	per := n / max(1, len(keys))
	var out []batchItem
	for _, k := range keys {
		g := groups[k]
		rnd.Shuffle(len(g), func(i, j int) { g[i], g[j] = g[j], g[i] })
		take := per
		if take > len(g) {
			take = len(g)
		}
		out = append(out, g[:take]...)
	}
	// 还有余量就从最大的组继续补，凑够 n
	if len(out) < n {
		used := map[int64]bool{}
		for _, q := range out {
			used[q.ID] = true
		}
		for _, q := range qs {
			if len(out) >= n {
				break
			}
			if !used[q.ID] {
				out = append(out, q)
				used[q.ID] = true
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// nextBatchNumber 扫描 outDir 里已有的 batch-NN.json，返回下一个可用编号。
//
// 同时也看 batches/：上次生成了批次但子代理还没产出结果时，
// 单看 out/ 会算出重复编号，把待处理的批次文件覆盖掉。
func nextBatchNumber(outDir string) int {
	maxN := 0
	for _, d := range []string{outDir, filepath.Dir(outDir) + "/batches"} {
		files, _ := filepath.Glob(filepath.Join(d, "batch-*.json"))
		for _, f := range files {
			base := strings.TrimSuffix(filepath.Base(f), ".json")
			var n int
			if _, err := fmt.Sscanf(base, "batch-%d", &n); err == nil && n > maxN {
				maxN = n
			}
		}
	}
	return maxN + 1
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
