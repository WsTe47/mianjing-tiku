// Package scraper 负责从牛客拉取指定用户的动态。
//
// 关键事实（均为实测结论）：
//   - 列表接口在 gw-c.nowcoder.com，参数是 pageNo（不是 page），主机与 www 不同；
//   - 需要登录 Cookie 才能看到动态列表；
//   - 列表响应里就带完整正文（records[].momentData.content），无需逐篇抓详情页；
//   - 原文链接格式为 /feed/main/detail/<moment.uuid>，用 contentId 拼会落到 SPA 空壳。
package scraper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/climber47/nc-interview/internal/model"
)

// DefaultUA 是默认 User-Agent。
const DefaultUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36"

// ListAPI 是动态列表接口模板，%d 为 pageNo。
const ListAPI = "https://gw-c.nowcoder.com/api/sparta/user/content/my-content?pageNo=%d&userId=%d"

// PostURL 由 moment uuid 生成原文链接。
func PostURL(uuid string) string {
	return "https://www.nowcoder.com/feed/main/detail/" + uuid
}

// Scraper 抓取指定用户的动态。
type Scraper struct {
	UserID     int64
	AuthorName string // 可选，用于给抓到的帖子标记作者
	Cookie     string
	Client     *http.Client
	UA         string
	Delay      time.Duration // 每页间隔，礼貌抓取
	MaxPage    int
}

// New 构造抓取器。
func New(userID int64, cookie string) *Scraper {
	return &Scraper{
		UserID:  userID,
		Cookie:  cookie,
		Client:  &http.Client{Timeout: 30 * time.Second},
		UA:      DefaultUA,
		Delay:   1200 * time.Millisecond,
		MaxPage: 60,
	}
}

type listResp struct {
	Success bool `json:"success"`
	Data    struct {
		Current   int `json:"current"`
		Total     int `json:"total"`
		TotalPage int `json:"totalPage"`
		Records   []struct {
			ContentID  json.Number `json:"contentId"`
			MomentData struct {
				ID          json.Number `json:"id"`
				UUID        string      `json:"uuid"`
				Title       string      `json:"title"`
				Content     string      `json:"content"`
				CreatedAt   int64       `json:"createdAt"`
				IP4Location string      `json:"ip4Location"`
			} `json:"momentData"`
			InterviewExp struct {
				Message string `json:"message"`
			} `json:"interviewExp"`
		} `json:"records"`
	} `json:"data"`
}

// FetchPage 拉取一页并转换为 Post 列表。
func (s *Scraper) FetchPage(ctx context.Context, page int) ([]model.Post, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf(ListAPI, page, s.UserID), nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", s.UA)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("Referer", fmt.Sprintf("https://www.nowcoder.com/users/%d", s.UserID))
	if s.Cookie != "" {
		req.Header.Set("Cookie", s.Cookie)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("请求第 %d 页失败: %w", page, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("第 %d 页返回 HTTP %d（Cookie 可能已失效）", page, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, 0, err
	}
	var lr listResp
	if err := json.Unmarshal(body, &lr); err != nil {
		return nil, 0, fmt.Errorf("解析第 %d 页失败: %w", page, err)
	}
	if !lr.Success {
		return nil, 0, fmt.Errorf("第 %d 页接口返回 success=false", page)
	}
	now := time.Now().UnixMilli()
	posts := make([]model.Post, 0, len(lr.Data.Records))
	for _, r := range lr.Data.Records {
		m := r.MomentData
		if m.UUID == "" {
			continue
		}
		id, _ := m.ID.Int64()
		if id == 0 {
			id, _ = r.ContentID.Int64()
		}
		title := strings.TrimSpace(m.Title)
		co, job, round := ParseTitle(title)
		posts = append(posts, model.Post{
			ID:           id,
			UUID:         m.UUID,
			Title:        title,
			URL:          PostURL(m.UUID),
			Company:      co,
			Job:          job,
			JobGroup:     JobGroup(job),
			Round:        round,
			RoundGroup:   RoundGroup(round),
			Content:      strings.TrimSpace(m.Content),
			PostedAt:     m.CreatedAt,
			PostedDate:   time.UnixMilli(m.CreatedAt).Format("2006-01-02"),
			InterviewExp: strings.TrimSpace(r.InterviewExp.Message),
			ScrapedAt:    now,
			AuthorID:     s.UserID,
			AuthorName:   s.AuthorName,
			Source:       "user",
		})
	}
	return posts, lr.Data.TotalPage, nil
}

var titleRe = regexp.MustCompile(`^(.+?)\s+(.+?)\s+(一面|二面|三面|四面|hr面|HR面|复活赛 一面|复活赛 二面)$`)

// ParseTitle 从「公司 岗位 轮次」形式的标题里拆出三个字段；不匹配则返回空串。
func ParseTitle(title string) (company, job, round string) {
	m := titleRe.FindStringSubmatch(strings.TrimSpace(title))
	if m == nil {
		return "", "", ""
	}
	return strings.TrimSpace(m[1]), strings.TrimSpace(m[2]), m[3]
}

// FetchAll 翻页抓取，直到越过 cutoff（毫秒时间戳）或达到页数上限。
// onPage 可选，用于打印进度。
func (s *Scraper) FetchAll(ctx context.Context, cutoff int64, onPage func(page, got int, oldest string)) ([]model.Post, error) {
	var all []model.Post
	seen := map[int64]bool{}
	for page := 1; page <= s.MaxPage; page++ {
		posts, totalPage, err := s.FetchPage(ctx, page)
		if err != nil {
			if page == 1 {
				return nil, err
			}
			break // 中途失败保留已抓到的部分
		}
		oldest := int64(1 << 62)
		for _, p := range posts {
			if !seen[p.ID] {
				seen[p.ID] = true
				all = append(all, p)
			}
			if p.PostedAt > 0 && p.PostedAt < oldest {
				oldest = p.PostedAt
			}
		}
		if onPage != nil {
			od := ""
			if oldest < (1 << 61) {
				od = time.UnixMilli(oldest).Format("2006-01-02")
			}
			onPage(page, len(all), od)
		}
		if oldest < cutoff {
			break
		}
		if totalPage > 0 && page >= totalPage {
			break
		}
		select {
		case <-ctx.Done():
			return all, ctx.Err()
		case <-time.After(s.Delay):
		}
	}
	return all, nil
}
