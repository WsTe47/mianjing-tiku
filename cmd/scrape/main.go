// Command scrape 抓取牛客动态并导入数据库。
//
// 三种用法：
//
//	scrape                     在线抓取（需 NC_COOKIE 环境变量）
//	scrape -jsonl data/posts.jsonl   从本地 JSONL 导入（无需 Cookie，用于离线/重复导入）
//	scrape -rebuild            只用库里已有的帖子正文重建问题聚类（改了分类规则后跑这个）
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/climber47/nc-interview/internal/config"
	"github.com/climber47/nc-interview/internal/importer"
	"github.com/climber47/nc-interview/internal/model"
	"github.com/climber47/nc-interview/internal/scraper"
	"github.com/climber47/nc-interview/internal/store"
)

func main() {
	var (
		jsonl    = flag.String("jsonl", "", "从本地 JSONL 文件导入，而不是在线抓取")
		rebuild  = flag.Bool("rebuild", false, "只用库内帖子重建问题聚类")
		months   = flag.Int("months", 6, "在线抓取时回溯的月数")
		dsn      = flag.String("dsn", "", "MySQL DSN（默认取 NC_DSN 或分项环境变量）")
		applyLLM = flag.String("apply-llm", "", "导入 LLM 归类结果，传入含 taxonomy.json / merge.json / out/ 的目录")
	)
	flag.Parse()

	cfg := config.Load()
	if *dsn != "" {
		cfg.DSN = *dsn
	}
	st, err := store.Open(cfg.DSN)
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	logf := func(f string, a ...any) { log.Printf(f, a...) }
	logf("数据库: %s", cfg.RedactedDSN())

	switch {
	case *applyLLM != "":
		stt, err := importer.LoadLLM(ctx, st, *applyLLM, logf)
		if err != nil {
			log.Fatalf("导入 LLM 归类失败: %v", err)
		}
		fmt.Printf("\nLLM 归类导入：细分类 %d 个｜合并为 %d 个可用类｜%d 批 / %d 条归位\n",
			stt.Taxonomy, stt.Merged, stt.Batches, stt.Assignments)
		if stt.NoCategory > 0 {
			fmt.Printf("             其中判定为「非题目」%d 条\n", stt.NoCategory)
		}
		if len(stt.UnknownCat) > 0 {
			fmt.Println("             ⚠️ 模型返回了体系外的分类名：")
			for k, v := range stt.UnknownCat {
				fmt.Printf("                %s (%d)\n", k, v)
			}
		}
		if len(stt.Unmerged) > 0 {
			fmt.Printf("             未被任何合并类覆盖的细分类 %d 个（将各自成为可用类）\n", len(stt.Unmerged))
		}
		return

	case *rebuild:
		stt, err := importer.ImportAll(ctx, st, logf)
		if err != nil {
			log.Fatalf("重建失败: %v", err)
		}
		report(stt)

	case *jsonl != "":
		posts, err := readJSONL(*jsonl)
		if err != nil {
			log.Fatalf("读取 %s 失败: %v", *jsonl, err)
		}
		logf("从 %s 读入 %d 篇帖子", *jsonl, len(posts))
		stt, err := importer.Import(ctx, st, posts, logf)
		if err != nil {
			log.Fatalf("导入失败: %v", err)
		}
		report(stt)

	default:
		if cfg.Cookie == "" {
			log.Fatal("在线抓取需要设置 NC_COOKIE 环境变量（浏览器里复制牛客的 Cookie 头）")
		}
		cutoff := time.Now().AddDate(0, -*months, 0).UnixMilli()
		sc := scraper.New(cfg.UserID, cfg.Cookie)
		sc.AuthorName = cfg.AuthorName
		logf("开始抓取用户 %d，回溯 %d 个月（截至 %s）",
			cfg.UserID, *months, time.UnixMilli(cutoff).Format("2006-01-02"))

		posts, err := sc.FetchAll(ctx, cutoff, func(page, got int, oldest string) {
			logf("  第 %2d 页：累计 %3d 篇，本页最早 %s", page, got, oldest)
		})
		if err != nil {
			log.Fatalf("抓取失败: %v", err)
		}
		logf("抓取完成，共 %d 篇（已去重）", len(posts))

		stt, err := importer.Import(ctx, st, posts, logf)
		if err != nil {
			log.Fatalf("导入失败: %v", err)
		}
		report(stt)
	}

	m, err := st.Meta(ctx, model.Filter{})
	if err == nil {
		fmt.Printf("\n库内现状：%d 篇帖子（%d 篇面试记录，%d 家公司）\n", m.Posts, m.Interviews, m.Companies)
		fmt.Printf("          问题 %d 组 / 出现记录 %d 条，日期 %s ~ %s\n",
			m.Questions, m.Occurrence, m.DateFrom, m.DateTo)
	}
}

// readJSONL 读取抓取产物（字段名兼容 Python 版本的 camelCase）。
func readJSONL(path string) ([]model.Post, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []model.Post
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var raw struct {
			ID           int64  `json:"id"`
			UUID         string `json:"uuid"`
			Title        string `json:"title"`
			Content      string `json:"content"`
			CreatedAt    int64  `json:"createdAt"`
			Date         string `json:"date"`
			InterviewExp string `json:"interviewExp"`
		}
		if err := json.Unmarshal(line, &raw); err != nil {
			continue
		}
		if raw.UUID == "" || raw.CreatedAt == 0 {
			continue
		}
		co, job, round := scraper.ParseTitle(raw.Title)
		if raw.Date == "" {
			raw.Date = time.UnixMilli(raw.CreatedAt).Format("2006-01-02")
		}
		out = append(out, model.Post{
			ID: raw.ID, UUID: raw.UUID, Title: raw.Title, URL: scraper.PostURL(raw.UUID),
			Company: co, Job: job, JobGroup: scraper.JobGroup(job),
			Round: round, RoundGroup: scraper.RoundGroup(round),
			Content:  raw.Content,
			PostedAt: raw.CreatedAt, PostedDate: raw.Date,
			InterviewExp: raw.InterviewExp, ScrapedAt: time.Now().UnixMilli(),
			Source: "user",
		})
	}
	return out, sc.Err()
}

func report(s importer.Stats) {
	fmt.Printf("\n导入结果：帖子 %d 篇｜问题行 %d 条（跳过备注 %d）｜聚类 %d 组\n",
		s.Posts, s.RawLines, s.Skipped, s.Questions)
}
