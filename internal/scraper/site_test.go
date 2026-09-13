package scraper

import "testing"

// TestIsContentURL 覆盖两种内容页形态与否定例。
//
// 这个判断决定了「原文 ↗」用哪个链接：/discuss/<id> 的帖子也有 uuid，
// 但按 uuid 拼成 /feed/main/detail/ 会落到空壳页（HTTP 200 但没有正文）。
func TestIsContentURL(t *testing.T) {
	yes := []string{
		"https://www.nowcoder.com/feed/main/detail/816d2b9f19c24d3885155d6d8b77834f",
		"https://www.nowcoder.com/discuss/922491544811098112",
		// 带跟踪参数的仍然指向内容页（regexp 故意不锚定尾部：
		// SitemapURLs 就是靠 FindString 从 sitemap 行里摘出规范链接、顺手丢掉 query）。
		// 调用方拼「原文」链接前会先 stripQuery。
		"https://www.nowcoder.com/discuss/922491544811098112?urlSource=sitemap",
	}
	no := []string{
		"",
		"https://www.nowcoder.com/users/125006155",
		"https://www.nowcoder.com/feed/main/detail/",
		"https://www.nowcoder.com/discuss/abc",
		"https://example.com/feed/main/detail/abc",
	}
	for _, u := range yes {
		if !IsContentURL(u) {
			t.Errorf("IsContentURL(%q) 应为 true", u)
		}
	}
	for _, u := range no {
		if IsContentURL(u) {
			t.Errorf("IsContentURL(%q) 应为 false", u)
		}
	}
}
