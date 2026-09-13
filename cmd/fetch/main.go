// Command fetch 从牛客 sitemap 抓取公开内容页并落盘。
//
// 设计要点（都是为了避免被 WAF 拦、以及保证可中断可续）：
//   - 免登录：公开详情页完整 SSR，不需要 Cookie
//   - 随机频率：固定间隔本身就是最容易被识别的爬虫特征
//   - 会话保持：Cookie Jar 携带 acw_tc（阿里云 WAF token）
//   - 指数退避 + 冷却：连续失败时主动降速
//   - 断点续抓：按 URL 的 sha1 落盘，重跑自动跳过已抓
//   - 原始响应落盘：解析规则会变，原始数据不该因此重抓
//
// 用法：
//
//	fetch -limit 500                 抓 500 条（默认随机 1.5~4s/条）
//	fetch -limit 500 -type feed      只抓 /feed/ 类型
//	fetch -dry                       只列出将要抓的 URL，不发请求
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/climber47/nc-interview/internal/scraper"
)

func main() {
	var (
		limit   = flag.Int("limit", 500, "本次最多抓多少条（已抓过的不计入）")
		minD    = flag.Duration("min", 1500*time.Millisecond, "请求间隔下限")
		maxD    = flag.Duration("max", 4*time.Second, "请求间隔上限（与下限之间随机）")
		rawDir  = flag.String("raw", "data/raw", "原始响应落盘目录")
		smDir   = flag.String("sitemap", "data/sitemap", "sitemap 缓存目录")
		kind    = flag.String("type", "all", "抓哪类：feed | discuss | all")
		dry     = flag.Bool("dry", false, "只列出待抓 URL，不发请求")
		timeout = flag.Duration("timeout", 0, "整体超时（0 表示不限，靠 Ctrl-C 停止）")

		longEvery = flag.Int("long-every", 30, "每 N 次请求插入一段长休息（0 关闭）")
		longMin   = flag.Duration("long-min", 12*time.Second, "长休息下限")
		longMax   = flag.Duration("long-max", 40*time.Second, "长休息上限")
		shuffle   = flag.Bool("shuffle", true, "打乱抓取顺序（避免每次重跑都从同一段前缀开始）")
		tries     = flag.Int("tries", 3, "单条 URL 的就地重试次数（针对空壳页/限流/5xx）")
		recheck   = flag.Bool("recheck", false, "复核已落盘数据，删除空壳页并让它们重新排队，然后退出")
		shard     = flag.String("shard", "0/1", "分片，形如 i/N：只抓哈希取模属于第 i 片（共 N 片）的 URL")
		noBreaker = flag.Bool("no-breaker", false, "禁用跨进程断路器（单进程调试时用）")
		shellRate = flag.Float64("shell-rate", 0.6, "窗口内空壳率超过该比例就触发全体冷却")
		coolDown  = flag.Duration("cooldown", 60*time.Second, "首次冷却时长（之后逐次翻倍，10 分钟封顶）")
		shellWait = flag.Duration("shell-wait", 1500*time.Millisecond, "空壳页重试前的等待基准（实际为 1~2 倍随机）")
	)
	flag.Parse()

	if *maxD < *minD {
		log.Fatalf("-max (%s) 不能小于 -min (%s)", *maxD, *minD)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if *timeout > 0 {
		var c context.CancelFunc
		ctx, c = context.WithTimeout(ctx, *timeout)
		defer c()
	}
	// Ctrl-C 优雅停止：已抓的都在盘上，重跑即可续
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		fmt.Println("\n收到中断信号，正在收尾（已抓内容已落盘，可直接重跑续抓）…")
		cancel()
	}()

	f, err := scraper.NewFetcher(*minD, *maxD)
	if err != nil {
		log.Fatalf("初始化失败: %v", err)
	}
	store, err := scraper.OpenRawStore(*rawDir)
	if err != nil {
		log.Fatalf("打开落盘目录失败: %v", err)
	}

	fmt.Printf("已有 %d 条抓取记录（%s）\n", store.Count(), *rawDir)
	f.Validate = scraper.LooksLikeContentPage

	// 分片参数
	shardIdx, shardNum, err := parseShard(*shard)
	if err != nil {
		log.Fatalf("解析 -shard 失败: %v", err)
	}
	me := scraper.ShardLabel(shardIdx, shardNum)
	consecOK := 0 // 连续成功次数，用于回落冷却等级

	// 跨进程断路器：任一 worker 发现软限流，全体冷却，避免把 IP 打封
	br := scraper.NewBreaker("", me)
	if !*noBreaker {
		br = scraper.NewBreaker(scraper.BreakerPath(*rawDir), me)
	}
	if d := br.Remaining(); d > 0 {
		st := br.Load()
		fmt.Printf("[分片 %s] 断路器生效中，还需冷却 %s（由 %s 触发：%s）\n",
			me, d.Round(time.Second), st.By, st.Reason)
	}

	if *recheck {
		kept, dropped, err := store.Recheck()
		if err != nil {
			log.Fatalf("复核失败: %v", err)
		}
		fmt.Printf("复核完成：保留 %d 条，删除空壳/损坏 %d 条（这些 URL 已重新排队）\n", kept, dropped)
		return
	}
	fmt.Println("正在准备 sitemap…")
	urls, err := scraper.SitemapURLs(ctx, f.Client, *smDir, f.UA, log.Printf)
	if err != nil {
		log.Fatalf("获取 sitemap 失败: %v", err)
	}

	// 按类型过滤 + 按分片过滤
	var todo []string
	for _, u := range urls {
		switch *kind {
		case "feed":
			if !strings.Contains(u, "/feed/") {
				continue
			}
		case "discuss":
			if !strings.Contains(u, "/discuss/") {
				continue
			}
		}
		if !scraper.ShardOf(u, shardIdx, shardNum) {
			continue
		}
		if !store.Has(u) {
			todo = append(todo, u)
		}
	}
	fmt.Printf("[分片 %s] sitemap 共 %d 条内容 URL，本分片待抓 %d 条\n", me, len(urls), len(todo))
	if *dry {
		n := *limit
		if n > len(todo) {
			n = len(todo)
		}
		for _, u := range todo[:n] {
			fmt.Println("  " + u)
		}
		fmt.Printf("（-dry 模式，未发请求；按当前参数本次会抓 %d 条）\n", n)
		return
	}
	if len(todo) == 0 {
		fmt.Println("没有待抓内容，全部已抓取。")
		return
	}

	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	if *shuffle {
		rnd.Shuffle(len(todo), func(i, j int) { todo[i], todo[j] = todo[j], todo[i] })
	}

	f.LongEvery, f.LongMin, f.LongMax = *longEvery, *longMin, *longMax
	scraper.OnLongPause = func(n int, d time.Duration) {
		fmt.Printf("  —— 已完成 %d 次请求，长休息 %s ——\n", n, d.Round(time.Second))
	}

	start := time.Now()
	ok, fail, skipped, trips, escalation := 0, 0, 0, 0, 0
	var win shellWindow

	for i, u := range todo {
		if ok >= *limit {
			break
		}
		select {
		case <-ctx.Done():
			goto done
		default:
		}

		// 断路器：站点在软限流时全体停下来，而不是各自「礼貌地」继续敲门
		if reason, err := br.Wait(ctx); err != nil {
			goto done
		} else if reason != "" {
			fmt.Printf("  [分片 %s] 冷却结束，继续（原因：%s）\n", me, reason)
		}

		// 随机频率：间隔在 [min, max] 间抖动
		if err := f.Sleep(ctx); err != nil {
			break
		}

		body, status, finalErr, retryable := fetchWithRetry(ctx, f, u, *tries, *shellWait)
		now := time.Now().UnixMilli()
		if finalErr != nil {
			fail++
			// 4xx（非 403）不重试也不落盘：内容可能已删除
			mark := "可重试"
			if !retryable {
				mark = "跳过"
				skipped++
			}
			fmt.Printf("  [%d/%d] %s %v（%s）\n", i+1, len(todo), shortURL(u), finalErr, mark)

			// 空壳率过高 = 撞上了「不服务」的时间窗。
			//
			// 实测的空壳模型（见 docs/DEPLOYMENT.md 第十一节）：
			// 站点会在「正常服务」和「只发空壳」两种状态之间切换，坏窗口持续几秒到几分钟。
			// 坏窗口里**所有** URL 都是空壳——曾经同一个 URL 连打 5 次全是空壳，
			// 12 秒后同样连打 6 次又全是内容页。所以这既不是限流（跟频率无关），
			// 也不是单条 URL 坏了（跟 URL 无关），而是时间窗口。
			//
			// 结论：坏窗口里快速重试是白费请求，正确做法是**停下来等**。
			// 冷却时间逐次翻倍（60s → 10min 封顶），窗口恢复正常后自动收敛。
			consecOK = 0
			if isShellErr(finalErr) {
				win.add(true)
			} else {
				win.add(false)
			}
			if n, rate := win.stats(); n >= winSize && rate >= *shellRate {
				d := *coolDown << escalation
				if d <= 0 || d > maxCoolDown {
					d = maxCoolDown
				}
				if err := br.Trip(d, fmt.Sprintf("最近 %d 次请求空壳率 %.0f%%", n, rate*100)); err != nil {
					log.Printf("写断路器失败: %v", err)
				} else {
					trips++
					escalation++
					fmt.Printf("  ⚠最近 %d 次请求空壳率 %.0f%%，全体冷却 %s（第 %d 次，所有分片暂停）\n",
						n, rate*100, d.Round(time.Second), trips)
				}
				win.reset()
			}
			continue
		}

		if err := store.Save(u, status, body, now); err != nil {
			log.Printf("落盘失败 %s: %v", shortURL(u), err)
			continue
		}
		ok++
		// 连续拿到内容页说明坏窗口过去了，冷却等级回落，避免长期停在高位
		if consecOK++; consecOK >= winSize {
			escalation = 0
		}
		// 每 10 条报一次进度，避免刷屏
		if ok%10 == 0 || ok == *limit {
			el := time.Since(start).Seconds()
			rate := float64(ok) / el
			left := float64(*limit-ok) / rate
			// 空壳率是判断「站点是不是在压我们」的核心指标，必须直接可见。
			// 只看「失败」会漏掉它——空壳重试成功的不计入失败，但它们是真实成本。
			shN, shRate := win.stats()
			fmt.Printf("  [%s] 已抓 %d/%d（失败 %d，跳过 %d，窗口空壳率 %.0f%%/%d）  平均 %.2f 条/秒  单请求 %.2fs  预计剩余 %s\n",
				me, ok, *limit, fail, skipped, shRate*100, shN, rate,
				f.AvgRequestTime().Seconds(),
				(time.Duration(left) * time.Second).Round(time.Second))
		}
	}

done:
	el := time.Since(start)
	fmt.Printf("\n[分片 %s] 本次结束：成功 %d 条，失败 %d，跳过 %d，触发冷却 %d 次，用时 %s\n",
		me, ok, fail, skipped, trips, el.Round(time.Second))
	fmt.Printf("累计已抓 %d 条，原始响应在 %s\n", store.Count(), *rawDir)
	if ok < *limit {
		fmt.Printf("（未达上限 %d 条：待抓队列可能已空或被中断；重跑本命令即可继续）\n", *limit)
	}
	fmt.Println("下一步：make parse   —— 把原始响应解析入库")
}

// isShellErr 判断错误是不是「200 但返回空壳页」。这是软限流的主要信号，
// 也是唯一值得触发全体冷却的错误——网络抖动和 5xx 由各自的退避处理就够了。
func isShellErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "空壳页")
}

// ---------- 空壳率滑动窗口 ----------
//
// 为什么不用「连续 N 次」：空壳是概率性的（实测 25%~50%）。
// 若单次空壳概率是 p，连续 3 次的概率是 p³——p=0.5 时高达 12.5%，
// 也就是说正常波动下也会频繁误判成「被限流」。
// 按窗口内的比例判断，才能把「偶发」和「持续」分开。

const winSize = 16

// maxCoolDown 是冷却时长的上限。坏窗口实测持续几秒到几分钟，
// 所以封顶设在 10 分钟；再长不如直接收工等人来看。
const maxCoolDown = 10 * time.Minute

type shellWindow struct {
	buf  [winSize]bool
	n    int
	next int
}

func (w *shellWindow) add(shell bool) {
	w.buf[w.next] = shell
	w.next = (w.next + 1) % winSize
	if w.n < winSize {
		w.n++
	}
}

func (w *shellWindow) reset() { w.n, w.next = 0, 0 }

// stats 返回窗口内的样本数与空壳比例。
func (w *shellWindow) stats() (int, float64) {
	if w.n == 0 {
		return 0, 0
	}
	c := 0
	for i := 0; i < w.n; i++ {
		if w.buf[i] {
			c++
		}
	}
	return w.n, float64(c) / float64(w.n)
}

// parseShard 解析 `i/N` 形式的分片参数。
func parseShard(s string) (idx, num int, err error) {
	parts := strings.SplitN(strings.TrimSpace(s), "/", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("格式应为 i/N，收到 %q", s)
	}
	if num, err = strconv.Atoi(parts[1]); err != nil || num < 1 {
		return 0, 0, fmt.Errorf("分片总数无效: %q", parts[1])
	}
	if idx, err = strconv.Atoi(parts[0]); err != nil || idx < 0 || idx >= num {
		return 0, 0, fmt.Errorf("分片序号无效（应在 0~%d）: %q", num-1, parts[0])
	}
	return idx, num, nil
}

// fetchWithRetry 抓一条 URL，对「可重试」的失败（空壳页 / 限流 / 5xx / 网络抖动）
// 就地重试若干次。空壳页是瞬时的，实测同一 URL 隔几秒即可恢复，
// 就地重试比留到下一轮整体重跑省时间。
func fetchWithRetry(ctx context.Context, f *scraper.Fetcher, u string, tries int, shellWait time.Duration) (
	body []byte, status int, err error, retryable bool) {

	if tries < 1 {
		tries = 1
	}
	for attempt := 1; ; attempt++ {
		b, st, retry, gerr := f.Get(ctx, u)
		if gerr == nil {
			return b, st, nil, false
		}
		retryable, status = retry, st
		if !retry || attempt >= tries {
			return b, st, gerr, retry
		}
		// 空壳页是 SSR 缓存未命中，隔一两秒再来就好；
		// 其他失败（5xx/网络）已经在 Get 里退避过了，这里只补一小拍。
		wait := shellWait
		if !isShellErr(gerr) {
			wait = 0
		}
		if wait > 0 {
			d := wait + time.Duration(rand.Int63n(int64(wait)))
			select {
			case <-ctx.Done():
				return nil, st, gerr, retry
			case <-time.After(d):
			}
		}
	}
}

func shortURL(u string) string {
	u = strings.TrimPrefix(u, "https://www.nowcoder.com")
	if len(u) > 46 {
		u = u[:46] + "…"
	}
	return u
}
