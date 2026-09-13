package store

import (
	"context"
	"sort"

	"github.com/climber47/nc-interview/internal/model"
)

// Matrix 返回「公司 × 知识点」的交叉计数。
//
// 取前 topCo 家公司与前 topCat 个知识点（各自按总量降序），
// 单元格是**去重后的题目数**而不是出现次数：同一道题被同一家公司问 5 次只算 1，
// 否则一家刷了很多帖的公司会在矩阵里虚高。
func (s *Store) Matrix(ctx context.Context, topCo, topCat int, flt model.Filter) (model.Matrix, error) {
	if topCo <= 0 {
		topCo = 15
	}
	if topCat <= 0 {
		topCat = 12
	}
	q := `
		SELECT p.company, q.llm_merged, COUNT(DISTINCT q.id)
		FROM occurrences o
		JOIN posts p ON p.id = o.post_id
		JOIN questions q ON q.id = o.question_id
		WHERE p.company <> '' AND q.llm_merged <> ''`
	var args []any
	// 矩阵也要被全局时间范围限定，否则会出现「筛了 8 月，矩阵还是全量」的不一致
	if dc, da := dateCond(flt); dc != "" {
		q += " AND " + dc
		args = append(args, da...)
	}
	q += ` GROUP BY p.company, q.llm_merged`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return model.Matrix{}, err
	}
	defer rows.Close()

	type cell struct {
		co, cat string
		n       int
	}
	var cells []cell
	coTotal := map[string]int{}
	catTotal := map[string]int{}
	for rows.Next() {
		var c cell
		if err := rows.Scan(&c.co, &c.cat, &c.n); err != nil {
			return model.Matrix{}, err
		}
		cells = append(cells, c)
		coTotal[c.co] += c.n
		catTotal[c.cat] += c.n
	}
	if err := rows.Err(); err != nil {
		return model.Matrix{}, err
	}

	top := func(m map[string]int, n int) []string {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		// 数量降序；同数量按名字排，保证结果稳定可复现
		sort.Slice(keys, func(i, j int) bool {
			if m[keys[i]] != m[keys[j]] {
				return m[keys[i]] > m[keys[j]]
			}
			return keys[i] < keys[j]
		})
		if len(keys) > n {
			keys = keys[:n]
		}
		return keys
	}
	cos := top(coTotal, topCo)
	cats := top(catTotal, topCat)

	coIdx := make(map[string]int, len(cos))
	for i, c := range cos {
		coIdx[c] = i
	}
	catIdx := make(map[string]int, len(cats))
	for j, c := range cats {
		catIdx[c] = j
	}

	out := model.Matrix{
		Companies: cos,
		Cats:      cats,
		Cells:     make([][]int, len(cos)),
		RowTotal:  make([]int, len(cos)),
		ColTotal:  make([]int, len(cats)),
	}
	for i := range out.Cells {
		out.Cells[i] = make([]int, len(cats))
	}
	for _, c := range cells {
		i, ok := coIdx[c.co]
		if !ok {
			continue
		}
		j, ok := catIdx[c.cat]
		if !ok {
			continue
		}
		out.Cells[i][j] = c.n
		out.RowTotal[i] += c.n
		out.ColTotal[j] += c.n
	}
	return out, nil
}
