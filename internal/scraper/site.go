package scraper

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------- sitemap ----------

const (
	SitemapIndex1 = "https://www.nowcoder.com/sitemap1.txt"
	SitemapIndex2 = "https://www.nowcoder.com/sitemap2.txt"
)

var reContentURL = regexp.MustCompile(`^https://www\.nowcoder\.com/(feed/main/detail/[0-9a-f]+|discuss/\d+)`)

// IsContentURL 判断一个 URL 是不是牛客的内容页形态。
func IsContentURL(u string) bool { return reContentURL.MatchString(u) }

// SitemapURLs 拉取（或读缓存）sitemap，返回去重后的内容页 URL。
//
// 牛客在 robots.txt 里主动发布了这些 sitemap，是它认可的爬虫入口。
func SitemapURLs(ctx context.Context, client *http.Client, cacheDir, ua string, log func(string, ...any)) ([]string, error) {
	if log == nil {
		log = func(string, ...any) {}
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, u := range []string{SitemapIndex1, SitemapIndex2} {
		cache := filepath.Join(cacheDir, filepath.Base(u))
		body, err := os.ReadFile(cache)
		if err != nil {
			body, err = fetchOnce(ctx, client, u, ua)
			if err != nil {
				return nil, fmt.Errorf("下载 %s 失败: %w", u, err)
			}
			if err := os.WriteFile(cache, body, 0o644); err != nil {
				return nil, err
			}
			log("已下载并缓存 %s（%d 字节）", filepath.Base(u), len(body))
		}
		n := 0
		for _, line := range strings.Split(string(body), "\n") {
			line = strings.TrimSpace(line)
			if m := reContentURL.FindString(line); m != "" && !seen[m] {
				seen[m] = true
				out = append(out, m)
				n++
			}
		}
		log("%s 贡献 %d 条内容 URL", filepath.Base(u), n)
	}
	sort.Strings(out)
	return out, nil
}

// ---------- 原始响应落盘（断点续抓的基础）----------

// RawStore 以「URL 的 sha1」为键保存 gzip 压缩的原始响应，并维护一份 append-only 清单。
//
// 落盘而非只存解析结果：解析规则会变，原始响应不该因此重抓。
type RawStore struct {
	Dir      string
	manifest string
	mu       sync.Mutex
	done     map[string]bool
}

// ManifestEntry 是清单里的一条。
type ManifestEntry struct {
	Hash      string `json:"hash"`
	URL       string `json:"url"`
	Status    int    `json:"status"`
	Bytes     int    `json:"bytes"`
	FetchedAt int64  `json:"fetchedAt"`
}

// OpenRawStore 打开（或创建）目录并载入已有清单。
func OpenRawStore(dir string) (*RawStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	r := &RawStore{Dir: dir, manifest: filepath.Join(dir, "_manifest.jsonl"), done: map[string]bool{}}
	b, err := os.ReadFile(r.manifest)
	if err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if line = strings.TrimSpace(line); line == "" {
				continue
			}
			var e ManifestEntry
			if json.Unmarshal([]byte(line), &e) == nil && e.Hash != "" {
				r.done[e.Hash] = true
			}
		}
	}
	return r, nil
}

// Hash 把 URL 映射为稳定文件名。
func Hash(u string) string {
	h := sha1.Sum([]byte(u))
	return hex.EncodeToString(h[:])[:20]
}

// Has 报告该 URL 是否已经抓过。
func (r *RawStore) Has(u string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.done[Hash(u)]
}

// Count 返回已抓条数。
func (r *RawStore) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.done)
}

// LooksLikeContentPage 判断响应正文是不是「真的内容页」。
//
// 背景：牛客的内容页至少有三种「不是内容」的返回，且 HTTP 状态都是 200：
//
//	① 约 6KB 的骨架 HTML（只有 <head>，完全没有 SSR 数据）
//	② 约 82KB 的兜底页，contentData 存在但是**空对象 {}**（实测 5 条，内容全同）
//	③ 正常情况下也可能有正文为空的内容页（打卡动态）——这类是**合法**的，要留下
//
// 早先只检查正文里是否出现 "contentData" 这个子串，于是 ② 被当成有效内容页落盘，
// 然后在解析阶段变成一条 title/content/uuid 全空、日期 1970 的脏数据。
//
// 所以判据必须是**语义的**：真的解出 contentData，且它带有身份标识（uuid 或 id）。
// 代价是每个响应多一次 JSON 解析（约 1~2ms），相对 500ms 的网络耗时可以忽略。
func LooksLikeContentPage(body []byte) bool {
	state, err := extractStateJSON(body)
	if err != nil {
		return false
	}
	var root map[string]any
	if err := json.Unmarshal(state, &root); err != nil {
		return false
	}
	cd := digMap(root, "prefetchData", "2", "ssrCommonData", "contentData")
	if cd == nil {
		return false
	}
	// 空对象 {} 是没有身份标识的，属于变体 ②
	return strOf(cd["uuid"]) != "" || int64Of(cd["id"]) != 0
}

// Save 写入一条响应并追加清单。
//
// 写入是「先写临时文件再 rename」而不是直接 os.Create：解析进程（make parse）
// 常年与抓取进程并行跑，直接写目标文件会让读者看到**写了一半的 gzip**
// （表现为 parse 报若干条「解析失败」，重跑就好——这种偶发错误最难查）。
// rename 在同一文件系统内是原子的，读者要么看到旧文件、要么看到完整的新文件。
func (r *RawStore) Save(u string, status int, body []byte, fetchedAt int64) error {
	h := Hash(u)
	final := filepath.Join(r.Dir, h+".html.gz")

	tmp, err := os.CreateTemp(r.Dir, h+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	zw := gzip.NewWriter(tmp)
	if _, err := zw.Write(body); err != nil {
		zw.Close()
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := zw.Close(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, final); err != nil {
		os.Remove(tmpName)
		return err
	}

	line, _ := json.Marshal(ManifestEntry{Hash: h, URL: u, Status: status, Bytes: len(body), FetchedAt: fetchedAt})
	r.mu.Lock()
	defer r.mu.Unlock()
	mf, err := os.OpenFile(r.manifest, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer mf.Close()
	if _, err := mf.Write(append(line, '\n')); err != nil {
		return err
	}
	r.done[h] = true
	return nil
}

// Load 读回一条响应正文。
func (r *RawStore) Load(u string) ([]byte, error) {
	f, err := os.Open(filepath.Join(r.Dir, Hash(u)+".html.gz"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

// Entries 返回清单全部条目（按 URL 排序，便于稳定遍历）。
func (r *RawStore) Entries() ([]ManifestEntry, error) {
	b, err := os.ReadFile(r.manifest)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []ManifestEntry
	for _, line := range strings.Split(string(b), "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		var e ManifestEntry
		if json.Unmarshal([]byte(line), &e) == nil && e.Hash != "" {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	return out, nil
}

// Recheck 复核已落盘的原始响应，删掉不合格的（默认判据 LooksLikeContentPage），
// 并重写清单，使这些 URL 在下次抓取时重新排队。
//
// 用途：抓取阶段还没识别空壳页之前落下的脏数据，不必手工 rm。
func (r *RawStore) Recheck() (kept, dropped int, err error) {
	ents, err := r.Entries()
	if err != nil {
		return 0, 0, err
	}
	tmp, err := os.CreateTemp(r.Dir, "_manifest-*.jsonl")
	if err != nil {
		return 0, 0, err
	}
	defer os.Remove(tmp.Name())

	w := bufio.NewWriter(tmp)
	fresh := map[string]bool{}
	for _, e := range ents {
		body, lerr := r.Load(e.URL)
		if lerr != nil || !LooksLikeContentPage(body) {
			os.Remove(filepath.Join(r.Dir, e.Hash+".html.gz"))
			dropped++
			continue
		}
		line, _ := json.Marshal(e)
		w.Write(append(line, '\n'))
		fresh[e.Hash] = true
		kept++
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return 0, 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, 0, err
	}
	if err := os.Rename(tmp.Name(), r.manifest); err != nil {
		return 0, 0, err
	}
	os.Chmod(r.manifest, 0o644)
	r.mu.Lock()
	r.done = fresh
	r.mu.Unlock()
	return kept, dropped, nil
}

// ---------- 抓取器 ----------

// Fetcher 以随机频率抓取，并在异常时指数退避。
//
// 频率随机的理由：固定间隔本身就是最容易识别的爬虫特征。
type Fetcher struct {
	Client   *http.Client
	UA       string
	MinDelay time.Duration
	MaxDelay time.Duration
	rnd      *rand.Rand

	ConsecFail int           // 连续失败次数
	MaxBackoff time.Duration // 退避上限
	Cooldown   time.Duration // 连续失败过多时的冷却时长
	FailLimit  int           // 触发冷却的连续失败阈值

	// Validate 判定 200 响应的正文是否可用；nil 表示不做校验。
	// 返回 false 时按可重试失败处理（见 Get）。
	Validate func(body []byte) bool

	// 观测：累计请求耗时与次数，用于区分「我们等太久」和「服务端慢」。
	// 这两个数字决定了该加延迟还是该加并发，靠猜很容易调错方向。
	totalReq time.Duration
	reqCount int

	// 长休息：每 LongEvery 次请求额外插入一段 [LongMin, LongMax] 的停顿。
	// 目的不是礼貌，而是打散「几百次等间隔请求」这种一眼假的时间序列。
	LongEvery int
	LongMin   time.Duration
	LongMax   time.Duration
	seen      int
}

// OnLongPause 在进入长休息前回调（用于打印进度），可为 nil。
var OnLongPause func(n int, d time.Duration)

// NewFetcher 构造抓取器，带 Cookie Jar 以保持 acw_tc（阿里云 WAF token）。
func NewFetcher(minDelay, maxDelay time.Duration) (*Fetcher, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &Fetcher{
		Client: &http.Client{
			Timeout: 30 * time.Second,
			Jar:     jar, // 复用会话，避免每次重新握手（acw_tc 需要携带）
		},
		UA:         DefaultUA,
		MinDelay:   minDelay,
		MaxDelay:   maxDelay,
		rnd:        rand.New(rand.NewSource(time.Now().UnixNano())),
		MaxBackoff: 5 * time.Minute,
		Cooldown:   10 * time.Minute,
		FailLimit:  5,
		LongEvery:  30,
		LongMin:    12 * time.Second,
		LongMax:    40 * time.Second,
	}, nil
}

// Sleep 在 [MinDelay, MaxDelay] 间随机等待——固定节奏是最明显的爬虫特征。
// 每 LongEvery 次请求会额外插入一段长休息，避免长时间等间隔访问。
func (f *Fetcher) Sleep(ctx context.Context) error {
	d := f.MinDelay
	if f.MaxDelay > f.MinDelay {
		d += time.Duration(f.rnd.Int63n(int64(f.MaxDelay - f.MinDelay)))
	}
	f.seen++
	if f.LongEvery > 0 && f.seen%f.LongEvery == 0 && f.LongMax > 0 {
		extra := f.LongMin
		if f.LongMax > f.LongMin {
			extra += time.Duration(f.rnd.Int63n(int64(f.LongMax - f.LongMin)))
		}
		if OnLongPause != nil {
			OnLongPause(f.seen, extra)
		}
		d += extra
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// Get 抓一个 URL。成功返回正文；失败按连续失败次数指数退避。
//
// 返回的 retry 为 true 表示「可稍后重试」（网络错误/5xx），false 表示「不该重试」（4xx）。
func (f *Fetcher) Get(ctx context.Context, u string) (body []byte, status int, retry bool, err error) {
	t0 := time.Now()
	defer func() {
		f.totalReq += time.Since(t0)
		f.reqCount++
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, false, err
	}
	req.Header.Set("User-Agent", f.UA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("Referer", "https://www.nowcoder.com/")

	resp, err := f.Client.Do(req)
	if err != nil {
		f.backoff(ctx)
		return nil, 0, true, err
	}
	defer resp.Body.Close()
	status = resp.StatusCode
	body, err = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		f.backoff(ctx)
		return nil, status, true, err
	}

	switch {
	case status == http.StatusOK:
		// 200 不等于拿到了内容：牛客会间歇性返回空壳页。
		//
		// 实测这不是限流，而是 SSR 缓存未命中：
		//   - 空壳响应 0.1~0.2s 就返回，内容页要 0.6~0.7s（快说明是「没渲染」而非「被拦」）
		//   - 完全静默 2 分钟后、零并发的慢速探测（5s 间隔）仍有 25% 是空壳
		//   - 同一个 URL 立刻重试基本就能拿到内容页
		//
		// 所以这里**不做退避**——退避是给「站点在拒绝你」用的，
		// 而空壳只是「这次没渲染」。惩罚性等待会把吞吐拖垮 10 倍
		//（早先 ConsecFail=1 → 10s 退避，导致单请求均耗时被抬到 4.5~8s）。
		// 由调用方用很短的间隔重试；真正的持续限流交给断路器的「空壳率」判定。
		if f.Validate != nil && !f.Validate(body) {
			return body, status, true, fmt.Errorf("HTTP 200 但返回空壳页（%d 字节，缺 SSR 数据）", len(body))
		}
		f.ConsecFail = 0
		return body, status, false, nil
	case status == http.StatusTooManyRequests || status >= 500:
		// 限流或服务端错误：退避后重试是合理的
		f.backoff(ctx)
		return body, status, true, fmt.Errorf("HTTP %d", status)
	case status == http.StatusForbidden:
		// 403 往往是 WAF 拦截，退避后仍可能恢复，但更可能是节奏问题
		f.backoff(ctx)
		return body, status, true, fmt.Errorf("HTTP 403（可能被 WAF 拦截）")
	default:
		return body, status, false, fmt.Errorf("HTTP %d", status)
	}
}

// AvgRequestTime 返回平均单次请求耗时（含退避与重试，用于诊断瓶颈）。
func (f *Fetcher) AvgRequestTime() time.Duration {
	if f.reqCount == 0 {
		return 0
	}
	return f.totalReq / time.Duration(f.reqCount)
}

// backoff 指数退避；连续失败过多则进入冷却。
func (f *Fetcher) backoff(ctx context.Context) {
	f.ConsecFail++
	if f.ConsecFail >= f.FailLimit {
		select {
		case <-ctx.Done():
		case <-time.After(f.Cooldown):
		}
		f.ConsecFail = 0
		return
	}
	d := time.Duration(1<<uint(f.ConsecFail)) * 5 * time.Second
	if d > f.MaxBackoff {
		d = f.MaxBackoff
	}
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func fetchOnce(ctx context.Context, client *http.Client, u, ua string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}

// ParseURL 从 sitemap 路径还原完整 URL（清单里存的是无 scheme 的相对路径。
// 保留此函数以便将来清单格式变化时兼容）。
func ParseURL(path string) string {
	if u, err := url.Parse(path); err == nil && u.Scheme != "" {
		return path
	}
	return "https://www.nowcoder.com/" + strings.TrimPrefix(path, "/")
}
