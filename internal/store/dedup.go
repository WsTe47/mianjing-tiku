package store

import (
	"context"
	"strings"
	"unicode"
)

// PostQuestionSet 是一篇帖子，以及它抽出来的问题簇 id 集合。
type PostQuestionSet struct {
	ID        int64
	Title     string
	Questions []int64
}

// NormalizeTitle 归一化标题：只保留字母、数字与汉字，其余（空白、标点、emoji）全部丢掉。
//
// 目的是让「美团内推美团内推码」与「美团内推 美团内推码」落进同一个 key，
// 从而在同题转帖之间做比较。
func NormalizeTitle(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// jaccard 计算两个问题簇集合的 Jaccard 相似度。
//
// 两个空集返回 0（而不是 1）：空帖由 PruneEmptyPosts 处理，
// 不该在这里被当成「完全一样」而互相删除。
func jaccard(a, b []int64) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	set := make(map[int64]struct{}, len(a))
	for _, x := range a {
		set[x] = struct{}{}
	}
	inter := 0
	for _, x := range b {
		if _, ok := set[x]; ok {
			inter++
		}
	}
	union := len(set) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// 跨标题洗稿的阈值，比同标题那条更严。
//
// 为什么更严：标题是转帖最省事的锚点，同标题几乎不会误判；跨标题只能靠题集
// 重合度，而**题集小的时候 Jaccard 完全不可靠**——两篇各 4 道题、恰好重合 4 道，
// Jaccard 就是 1.0，但它们毫无关系。
//
// 实测（3,603 篇 / 35,909 有效题）：不设最小题集时，异标题 J>=0.9 有 151 对，
// 其中 44 对两边都不带「内推」，抽样一看全是 4 题小帖误配
// （《【社招复盘】面试，是人类讲故事能力的巅峰炫技》≈《嵌入式经典百套大厂面试题总结》）。
// 加上「最小 8 题」后剩 109 对，其中 95 对含「内推」——其余是
// 《快手数据研发面经》这种同内容换措辞标题的内推模板洗稿。
const (
	crossTitleThreshold    = 0.9
	crossTitleMinQuestions = 8
)

// DuplicatePostIDs 返回「应当删掉」的重复帖子 id，保留每组里 id 最小的那篇。
//
// 两条判据，命中任意一条即算重复：
//  1. 归一化标题相同，且题目集 Jaccard >= threshold；
//  2. 跨标题：两边题目集都 >= crossTitleMinQuestions，
//     且 Jaccard >= crossTitleThreshold（专治换了标题的内推模板洗稿）。
//
// 输入必须按 id 升序，结果才稳定可复现。
//
// 为什么需要它：牛客上的内推广告会把同一份「模板面经题单」连同内推码一起
// 复制几十上百遍。这些转帖不是几十个人真的考过同一道题，但按出现次数统计时
// 会把模板里的每道题都刷成 57×/76× 这种整齐的假频次，直接污染「按频次排序」
// 这个核心功能。按题集去重只去掉重复计数，不丢任何一道题。
func DuplicatePostIDs(sets []PostQuestionSet, threshold float64) []int64 {
	dup := make(map[int64]bool)

	// ---- 第一遍：同归一化标题 ----
	byTitle := make(map[string][]int)
	var order []string
	for i, s := range sets {
		k := NormalizeTitle(s.Title)
		if k == "" {
			continue
		}
		if _, ok := byTitle[k]; !ok {
			order = append(order, k)
		}
		byTitle[k] = append(byTitle[k], i)
	}
	for _, k := range order {
		idx := byTitle[k]
		if len(idx) < 2 {
			continue
		}
		var kept []int
		for _, i := range idx {
			isDup := false
			for _, j := range kept {
				if jaccard(sets[i].Questions, sets[j].Questions) >= threshold {
					isDup = true
					break
				}
			}
			if isDup {
				dup[sets[i].ID] = true
			} else {
				kept = append(kept, i)
			}
		}
	}

	// ---- 第二遍：跨标题洗稿 ----
	// 倒排索引只放「已判定保留」的帖子，边扫边建：
	// 这样候选集是逐步长大的，而且天然保证「保留 id 更小的那篇」。
	inv := make(map[int64][]int)
	for i, s := range sets {
		if dup[s.ID] {
			continue
		}
		li := len(s.Questions)
		if li >= crossTitleMinQuestions {
			cand := make(map[int]int)
			for _, q := range s.Questions {
				for _, j := range inv[q] {
					cand[j]++
				}
			}
			for j, c := range cand {
				lj := len(sets[j].Questions)
				if lj < crossTitleMinQuestions {
					continue
				}
				// 必要条件（J>=0.9 ⇒ c >= 0.9*(li+lj)/1.9），用整数比较免浮点。
				// 注意不能用 c >= 0.9*max(li,lj)：那个条件过强，会漏掉大小不等的对。
				if c*19 < 9*(li+lj) {
					continue
				}
				if jaccard(s.Questions, sets[j].Questions) >= crossTitleThreshold {
					dup[s.ID] = true
					break
				}
			}
		}
		if dup[s.ID] {
			continue
		}
		for _, q := range s.Questions {
			inv[q] = append(inv[q], i)
		}
	}

	out := make([]int64, 0, len(dup))
	for _, s := range sets {
		if dup[s.ID] {
			out = append(out, s.ID)
		}
	}
	return out
}

// PostQuestionSets 按 id 升序读出每篇帖子及其问题簇集合。
func (s *Store) PostQuestionSets(ctx context.Context) ([]PostQuestionSet, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id, p.title, o.question_id
		FROM posts p
		LEFT JOIN occurrences o ON o.post_id = p.id
		ORDER BY p.id, o.question_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PostQuestionSet
	for rows.Next() {
		var id int64
		var title string
		var qid *int64
		if err := rows.Scan(&id, &title, &qid); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].ID != id {
			out = append(out, PostQuestionSet{ID: id, Title: title})
		}
		if qid != nil {
			out[len(out)-1].Questions = append(out[len(out)-1].Questions, *qid)
		}
	}
	return out, rows.Err()
}

// DeletePosts 删除给定帖子及其出现记录，返回（删掉的帖子数, 删掉的记录数）。
func (s *Store) DeletePosts(ctx context.Context, ids []int64) (int64, int64, error) {
	if len(ids) == 0 {
		return 0, 0, nil
	}
	var occs int64
	const chunk = 500
	for start := 0; start < len(ids); start += chunk {
		end := start + chunk
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		ph := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		args := make([]any, len(batch))
		for i, id := range batch {
			args[i] = id
		}
		r, err := s.db.ExecContext(ctx,
			`DELETE FROM occurrences WHERE post_id IN (`+ph+`)`, args...)
		if err != nil {
			return 0, 0, err
		}
		n, _ := r.RowsAffected()
		occs += n
	}
	var posts int64
	for start := 0; start < len(ids); start += chunk {
		end := start + chunk
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		ph := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		args := make([]any, len(batch))
		for i, id := range batch {
			args[i] = id
		}
		r, err := s.db.ExecContext(ctx,
			`DELETE FROM posts WHERE id IN (`+ph+`)`, args...)
		if err != nil {
			return 0, occs, err
		}
		n, _ := r.RowsAffected()
		posts += n
	}
	return posts, occs, nil
}

// RecomputeQuestionCounts 依据 occurrences 重算 questions.n，并删掉不再出现的簇。
func (s *Store) RecomputeQuestionCounts(ctx context.Context) (int64, error) {
	if err := s.RecomputeCounts(ctx); err != nil {
		return 0, err
	}
	r, err := s.db.ExecContext(ctx, `DELETE FROM questions WHERE n = 0`)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// DeduplicatePosts 删掉「同标题 + 题目集高度重合」的转帖，返回（删帖数, 删记录数, 错误）。
func (s *Store) DeduplicatePosts(ctx context.Context, threshold float64) (int64, int64, error) {
	posts, occs, _, err := s.DeduplicatePostsVerbose(ctx, threshold)
	return posts, occs, err
}

// DeduplicatePostsVerbose 同 DeduplicatePosts，但额外返回命中的标题组数。
func (s *Store) DeduplicatePostsVerbose(ctx context.Context, threshold float64) (int64, int64, int, error) {
	sets, err := s.PostQuestionSets(ctx)
	if err != nil {
		return 0, 0, 0, err
	}
	dups := DuplicatePostIDs(sets, threshold)
	if len(dups) == 0 {
		return 0, 0, 0, nil
	}
	dupSet := make(map[int64]struct{}, len(dups))
	for _, id := range dups {
		dupSet[id] = struct{}{}
	}
	titles := make(map[string]struct{})
	for _, s := range sets {
		if _, ok := dupSet[s.ID]; ok {
			titles[NormalizeTitle(s.Title)] = struct{}{}
		}
	}

	posts, occs, err := s.DeletePosts(ctx, dups)
	if err != nil {
		return posts, occs, len(titles), err
	}
	if _, err := s.RecomputeQuestionCounts(ctx); err != nil {
		return posts, occs, len(titles), err
	}
	return posts, occs, len(titles), nil
}
