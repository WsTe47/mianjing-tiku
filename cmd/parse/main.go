// Command parse 把 fetch 落盘的原始响应解析入库。
//
// 与抓取分离的理由：解析规则（提取路径、粗筛阈值）会反复调整，
// 而原始响应已经落盘，改规则重跑解析即可，不必重抓。
//
// 用法：
//
//	parse                     解析全部已落盘响应，粗筛后入库
//	parse -no-filter          不做粗筛，全部入库（用于校准阈值）
//	parse -limit 100          只处理前 100 条
//	parse -classify           入库后顺带重建问题聚类
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/climber47/nc-interview/internal/config"
	"github.com/climber47/nc-interview/internal/importer"
	"github.com/climber47/nc-interview/internal/model"
	"github.com/climber47/nc-interview/internal/scraper"
	"github.com/climber47/nc-interview/internal/store"
)

func main() {
	var (
		rawDir   = flag.String("raw", "data/raw", "原始响应目录（fetch 的产物）")
		noFilter = flag.Bool("no-filter", false, "不做粗筛，全部入库")
		limit    = flag.Int("limit", 0, "最多处理多少条（0 表示不限）")
		classify = flag.Bool("classify", false, "入库后重建问题聚类")
		prune    = flag.Bool("prune", false, "重建聚类后，删除一条题都没抽出来的帖子（需要 -classify）")
		dedup    = flag.Bool("dedup", true, "删除「同标题 + 题目集高度重合」的转帖，避免转帖刷高假频次（需要 -classify）")
		dthumb   = flag.Float64("dedup-thresh", 0.8, "转帖判定的题目集 Jaccard 相似度阈值")
		dry      = flag.Bool("dry", false, "只统计，不写库")
		sample   = flag.Int("sample", 0, "打印前 N 条被粗筛掉的标题，用于校准阈值")
	)
	flag.Parse()

	cfg := config.Load()
	st, err := store.Open(cfg.DSN)
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}
	defer st.Close()

	ctx := context.Background()

	raw, err := scraper.OpenRawStore(*rawDir)
	if err != nil {
		log.Fatalf("打开原始响应目录失败: %v", err)
	}
	// 宽松标题解析的词典：库里已有的公司名。随抓取自动扩充。
	knownCompanies, err := st.Companies(ctx)
	if err != nil {
		log.Fatalf("读取公司词典失败: %v", err)
	}
	entries, err := raw.Entries()
	if err != nil {
		log.Fatalf("读取清单失败: %v", err)
	}
	if len(entries) == 0 {
		log.Fatalf("%s 里没有抓取记录，先运行 make fetch", *rawDir)
	}
	fmt.Printf("清单共 %d 条原始响应\n", len(entries))

	var (
		total, okPost, dropped, badPage, tooOld int
		offPosts, offQuestions                  int
		loadFail, parseFail, noUUID             int
		droppedSample                           []string
		failSample                              []string
	)
	start := time.Now()

	for i, e := range entries {
		if *limit > 0 && i >= *limit {
			break
		}
		body, err := raw.Load(e.URL)
		if err != nil {
			// 读文件失败与解析失败要分开统计：前者通常是「与抓取并行」的写读竞争
			// （Save 已改原子 rename），后者才是页面结构或内容的问题。
			// 混在一个计数器里会让「哪一类在变多」看不出来。
			badPage++
			loadFail++
			if len(failSample) < 8 {
				failSample = append(failSample, fmt.Sprintf("读取失败 %s: %v", shortURL(e.URL), err))
			}
			continue
		}
		p, err := scraper.ExtractContent(body)
		if err != nil {
			badPage++
			parseFail++
			if len(failSample) < 8 {
				failSample = append(failSample, fmt.Sprintf("解析失败 %s: %v", shortURL(e.URL), err))
			}
			continue
		}
		total++

		// 少数内容页的 contentData 没有 uuid（entityType 与常规动态不同）。
		// 以前这类直接跳过，等于白抓；其实**抓取时用的 URL 本身就是规范链接**，
		// 拿它兜底即可，信息一点不少。
		// 规范原文链接优先用**抓取时用的那个 URL**。
		//
		// ⚠️ 这里踩过一个坑：原来一律按 uuid 拼 /feed/main/detail/<uuid>，
		// 但 /discuss/<id> 的帖子**同样带 uuid**，而它在 feed 路径下是一个空壳页——
		// HTTP 200、也有 contentData，唯独没有正文。实测 1,271 篇（36%）的
		// 「原文 ↗」因此跳到空白页。抓取时用的 URL 才是规范链接。
		pageURL := stripQuery(e.URL)
		if !scraper.IsContentURL(pageURL) {
			// 抓取用的 URL 不是内容页形态（例如旧的用户时间线路径），退回按 uuid 拼
			pageURL = "https://www.nowcoder.com/feed/main/detail/" + p.UUID
		}
		if p.UUID == "" {
			noUUID++
			if p.ID == 0 {
				p.ID = hashID(p.UUID + pageURL) // 保证 posts.id 非空且稳定
			}
		}
		if p.CreatedAt == 0 {
			tooOld++
		}

		if !*noFilter && !scraper.LooksLikeInterview(p) {
			dropped++
			if len(droppedSample) < *sample {
				droppedSample = append(droppedSample, fmt.Sprintf("[%d分] %s", scraper.InterviewScore(p), p.Title))
			}
			continue
		}

		// 结构化真题列表落库（JSON）。这是最高质量的题源，见 migration 004。
		officialQ := ""
		if len(p.ExperienceQuestions) > 0 {
			if b, err := json.Marshal(p.ExperienceQuestions); err == nil {
				officialQ = string(b)
			}
		}

		co, job, round := scraper.ParseTitleLoose(p.Title, knownCompanies)
		if round == "" {
			// 标题没写轮次时用正文兜底（只在正文恰好只提到一种轮次时才采用）
			round = scraper.RoundFromContent(p.Content)
		}
		post := model.Post{
			ID: p.ID, UUID: p.UUID, Title: p.Title,
			URL:        pageURL,
			Company:    co,
			Job:        job,
			JobGroup:   scraper.JobGroup(job),
			Round:      round,
			RoundGroup: scraper.RoundGroup(round),
			Content:    p.Content,
			PostedAt:   p.CreatedAt,
			PostedDate: time.UnixMilli(p.CreatedAt).Format("2006-01-02"),
			ScrapedAt:  time.Now().UnixMilli(),
			AuthorID:   p.AuthorID,
			AuthorName: p.AuthorName,
			Source:     "sitemap",
			EntityType: p.EntityType,
			LikeCnt:    p.LikeCnt,
			CommentCnt: p.CommentCnt,
			ViewCnt:    p.ViewCnt,
			TopicTag:   p.TopicTag,
			OfficialQ:  officialQ,
		}
		if len(p.ExperienceQuestions) > 0 {
			offPosts++
			offQuestions += len(p.ExperienceQuestions)
		}
		if *dry {
			okPost++
			continue
		}
		if err := st.UpsertPost(ctx, post); err != nil {
			log.Printf("写入失败 %d: %v", p.ID, err)
			continue
		}
		okPost++

		if okPost%200 == 0 {
			fmt.Printf("  已入库 %d 条…\n", okPost)
		}
	}

	// 补全历史帖的标题派生字段。
	// 宽松解析上线前入库的帖子 company/round 还是空的，而这两个字段是
	// 「按公司/按轮次筛选」的全部依据；重跑解析只会覆盖新帖，
	// 所以这里对已有行做一次幂等回填。
	if !*dry {
		if fixed, err := backfillMeta(ctx, st, log.Printf); err != nil {
			log.Printf("补全标题字段失败: %v", err)
		} else if fixed > 0 {
			fmt.Printf("  补全标题字段 %d 篇（公司/轮次/岗位）\n", fixed)
		}
	}

	fmt.Printf("\n解析结果（用时 %s）\n", time.Since(start).Round(time.Second))
	fmt.Printf("  解析成功      %d 条\n", total)
	fmt.Printf("  通过粗筛入库  %d 条\n", okPost)
	fmt.Printf("  粗筛丢弃      %d 条\n", dropped)
	fmt.Printf("  页面解析失败  %d 条（读取失败 %d / 内容解析失败 %d / 缺 uuid %d）\n",
		badPage, loadFail, parseFail, noUUID)
	if len(failSample) > 0 {
		fmt.Println("\n失败样例：")
		for _, f := range failSample {
			fmt.Println("  - " + f)
		}
	}
	if offPosts > 0 {
		fmt.Printf("  其中带结构化真题 %d 条帖子 / %d 道题（含参考答案）\n", offPosts, offQuestions)
	}
	if tooOld > 0 {
		fmt.Printf("  时间为 0 的    %d 条（已入库，日期会是 1970）\n", tooOld)
	}
	if len(droppedSample) > 0 {
		fmt.Println("\n被粗筛丢弃的样例（用于校准阈值）：")
		for _, s := range droppedSample {
			fmt.Printf("  - %s\n", trunc(s, 60))
		}
	}

	m, err := st.Meta(ctx, model.Filter{})
	if err == nil {
		fmt.Printf("\n库内现状：%d 篇帖子（%d 篇面试记录 / %d 家公司）\n", m.Posts, m.Interviews, m.Companies)
	}
	if *classify && !*dry {
		fmt.Println("\n重建问题聚类…")
		stt, err := importer.ImportAll(ctx, st, func(f string, a ...any) { log.Printf(f, a...) })
		if err != nil {
			log.Fatalf("重建聚类失败: %v", err)
		}
		fmt.Printf("  问题行 %d 条 → 聚类 %d 组\n", stt.RawLines, stt.Questions)

		if *dedup {
			// 转帖闸门：内推广告会把同一份模板题单复制几十上百遍，
			// 不处理的话模板里的每道题都会被刷成 57×/76× 这种整齐的假频次。
			n, occs, byTitle, err := st.DeduplicatePostsVerbose(ctx, *dthumb)
			if err != nil {
				log.Fatalf("去除转帖失败: %v", err)
			}
			fmt.Printf("  去除转帖 %d 篇（%d 个标题组，清掉 %d 条出现记录）\n", n, byTitle, occs)
		}
		if *prune {
			// 最后一道噪声闸门：一条题都抽不出来的帖子不是面经。
			n, orphan, err := st.PruneEmptyPosts(ctx)
			if err != nil {
				log.Fatalf("清理空帖失败: %v", err)
			}
			fmt.Printf("  清理空帖 %d 篇（顺带清掉 %d 条孤立出现记录）\n", n, orphan)
		}
		fmt.Println("提示：新数据还需要跑 LLM 归类（make llm-classify）")
	} else if *prune || *dedup {
		fmt.Println("提示：-prune / -dedup 需要配合 -classify 使用（要先重建出 occurrences 才能判断空帖与转帖）")
	}
}

// backfillMeta 用宽松规则重算所有帖子的 company/job/round。
//
// 幂等且**自愈**：对全部帖子重算，解析规则修好后旧的错误值会被纠正
// （只处理空字段的写法做不到这一点）。
func backfillMeta(ctx context.Context, st *store.Store, logf func(string, ...any)) (int, error) {
	known, err := st.Companies(ctx)
	if err != nil {
		return 0, err
	}
	titles, err := st.AllPostTitles(ctx)
	if err != nil {
		return 0, err
	}
	contents, err := st.AllPostContents(ctx)
	if err != nil {
		return 0, err
	}
	fixed := 0
	for id, title := range titles {
		co, job, round := scraper.ParseTitleLoose(title, known)
		if round == "" {
			round = scraper.RoundFromContent(contents[id])
		}
		if err := st.UpdatePostMeta(ctx, id, co, job,
			scraper.JobGroup(job), round, scraper.RoundGroup(round)); err != nil {
			logf("补全 %d 失败: %v", id, err)
			continue
		}
		fixed++
		// 新学到的公司名立刻加入词典，同批次后面的帖也能用上
		if co != "" {
			known = append(known, co)
		}
	}
	return fixed, nil
}

// stripQuery 去掉 URL 的查询串：?urlSource=sitemap 只是爬虫来源标记，
// 存进库里会污染原文链接。
func stripQuery(u string) string {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		return u[:i]
	}
	return u
}

// hashID 由字符串生成一个稳定的正整数 ID（FNV-1a）。
// 用于极少数没有 contentId 的页面，保证 posts.id 非空且同一页面每次结果一致。
func hashID(s string) int64 {
	const offset, prime = 14695981039346656037, 1099511628211
	var h uint64 = offset
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime
	}
	return int64(h & 0x7fffffffffffffff)
}

func shortURL(u string) string {
	return trunc(strings.TrimPrefix(u, "https://www.nowcoder.com"), 50)
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
