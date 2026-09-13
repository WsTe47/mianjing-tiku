// Package api 提供 HTTP 接口与静态前端。
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/climber47/nc-interview/internal/importer"
	"github.com/climber47/nc-interview/internal/model"
	"github.com/climber47/nc-interview/internal/store"
)

// Server 持有依赖。
type Server struct {
	st        *store.Store
	webDir    string
	corsAllow string
}

// New 构造 Server。
func New(st *store.Store, webDir, corsAllow string) *Server {
	return &Server{st: st, webDir: webDir, corsAllow: corsAllow}
}

// Routes 注册全部路由（Go 1.22+ 的 method+pattern 路由）。
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/meta", s.meta)
	mux.HandleFunc("GET /api/facets", s.facets)
	mux.HandleFunc("GET /api/topics", s.topics)
	mux.HandleFunc("GET /api/llmcats", s.llmCats)
	mux.HandleFunc("GET /api/matrix", s.matrix)
	mux.HandleFunc("GET /api/taxonomy", s.taxonomy)
	mux.HandleFunc("GET /api/questions", s.questions)
	mux.HandleFunc("GET /api/questions/{id}", s.question)
	mux.HandleFunc("GET /api/posts", s.posts)
	mux.HandleFunc("GET /api/posts/{id}", s.post)
	mux.HandleFunc("POST /api/reimport", s.reimport)
	mux.HandleFunc("GET /", s.static)
	return s.withCORS(mux)
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.corsAllow != "" {
			w.Header().Set("Access-Control-Allow-Origin", s.corsAllow)
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) meta(w http.ResponseWriter, r *http.Request) {
	m, err := s.st.Meta(r.Context(), rangeFilter(r.URL.Query()))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// rangeFilter 从查询串里取全局时间范围（from / to，YYYY-MM-DD 闭区间）。
//
// 时间范围是**全局**的：维度计数、矩阵、原文列表都要受它约束，
// 否则「筛了 8 月，公司榜还是全量」这种自相矛盾比不筛选更误导。
func rangeFilter(q url.Values) model.Filter {
	return model.Filter{From: q.Get("from"), To: q.Get("to")}
}

func (s *Server) facets(w http.ResponseWriter, r *http.Request) {
	f, err := s.st.Facets(r.Context(), rangeFilter(r.URL.Query()))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// topics 返回指定模块下的主题取值（?category=Agent%2FAI）。
// 前端据此只显示与该模块相关的主题。
func (s *Server) topics(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.TopicFacets(r.Context(), r.URL.Query().Get("category"), rangeFilter(r.URL.Query()))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if list == nil {
		list = []model.Facet{}
	}
	writeJSON(w, http.StatusOK, list)
}

// llmCats 返回某模块内的 LLM 细分类分布（「按模块」页的二级下钻）。
func (s *Server) llmCats(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.LLMCatFacets(r.Context(), r.URL.Query().Get("category"), rangeFilter(r.URL.Query()))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if list == nil {
		list = []model.Facet{}
	}
	writeJSON(w, http.StatusOK, list)
}

// matrix 返回「公司 × 知识点」的交叉计数。
func (s *Server) matrix(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	m, err := s.st.Matrix(r.Context(),
		atoiDef(q.Get("companies"), 15), atoiDef(q.Get("cats"), 12), rangeFilter(q))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// cleanVariantSources 把「其他措辞」清洗成适合展示的样子。
//
// 变体来自 occurrences.raw_text（原文），带编号前缀与不可见字符；
// canonical 是清洗过的。不清洗的话同一道题在两处长得不一样。
// 清洗后可能有两条变成同一条，所以按文本合并、把 postIds 并起来。
func cleanVariantSources(vs []model.VariantSource) []model.VariantSource {
	out := make([]model.VariantSource, 0, len(vs))
	idx := make(map[string]int, len(vs))
	for _, v := range vs {
		t := importer.CleanDisplayText(v.Text)
		if t == "" {
			continue
		}
		if i, ok := idx[t]; ok {
			out[i].PostIDs = append(out[i].PostIDs, v.PostIDs...)
			continue
		}
		idx[t] = len(out)
		out = append(out, model.VariantSource{Text: t, PostIDs: append([]int64(nil), v.PostIDs...)})
	}
	// 合并后重新按出处数量降序（后端原本已排好，合并会打乱）
	sort.SliceStable(out, func(a, b int) bool { return len(out[a].PostIDs) > len(out[b].PostIDs) })
	return out
}

// truthy 把查询参数当布尔解析："" / "0" / "false" 为假，其余为真。
func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "0", "false", "no":
		return false
	}
	return true
}

func atoiDef(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// taxonomy 返回 LLM 分类体系（三层：合并类 / 顶层域 / 细分类）。
func (s *Server) taxonomy(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.Taxonomy(r.Context())
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if list == nil {
		list = []model.TaxonomyEntry{}
	}
	writeJSON(w, http.StatusOK, list)
}

// questions 支持按轮次/岗位/公司/模块/主题/LLM 归类/关键词筛选，频次在筛选范围内重算。
//
//	GET /api/questions?round=一面&llmMerged=Agent%20工程&q=协程&page=1&size=50
func (s *Server) questions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := model.Filter{
		Round:     q.Get("round"),
		Job:       q.Get("job"),
		Company:   q.Get("company"),
		Category:  q.Get("category"),
		Topic:     q.Get("topic"),
		LLMMerged: q.Get("llmMerged"),
		LLMDomain: q.Get("llmDomain"),
		LLMCat:    q.Get("llmCat"),
		Keyword:   q.Get("q"),
		Sort:      q.Get("sort"),
		From:      q.Get("from"),
		To:        q.Get("to"),
		// 布尔筛选按「参数存在且不为 0/false」判定，这样 ?hasAnswer=1 与 ?hasAnswer= 都可写
		HasAnswer:    truthy(q.Get("hasAnswer")),
		OfficialOnly: truthy(q.Get("officialOnly")),
		IncludeNoise: truthy(q.Get("includeNoise")),
		Page:         atoiDef(q.Get("page"), 1),
		Size:         atoiDef(q.Get("size"), 50),
	}
	// 默认只展示已核验（有 LLM 判定）的题目，避免归类跟不上抓取时
	// 列表里混进未过滤的噪声。库里还没有任何标注时（全新部署）退回全部展示。
	if !truthy(q.Get("showAll")) && !f.IncludeNoise {
		has, err := s.st.HasLabels(r.Context())
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		f.OnlyLabeled = has
	}
	list, total, err := s.st.QueryQuestions(r.Context(), f)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if list == nil {
		list = []model.Question{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total": total,
		"page":  f.Page,
		"size":  f.Size,
		"items": list,
	})
}

func (s *Server) question(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, "id 必须是整数")
		return
	}
	q, err := s.st.Question(r.Context(), id)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if q == nil {
		fail(w, 404, "问题不存在")
		return
	}
	// 展示层清洗：canonical 已由 stripLeadLabel 处理过，变体是原文透出，
	// 不清洗会让同一道题在「标题」与「其他措辞」里长得不一样。
	q.VariantSources = cleanVariantSources(q.VariantSources)
	writeJSON(w, http.StatusOK, q)
}

func (s *Server) posts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := atoiDef(q.Get("page"), 1)
	size := atoiDef(q.Get("size"), 50)
	// 默认只列「至少有一道未被判为非题目的题」的帖子，把纯内推广告挡在「面经原文」之外。
	// 未归类的题不算噪声，所以全新部署（库里还没标注）时这条自然全部通过，不会空列表。
	onlyQ := q.Get("all") != "1"
	list, total, err := s.st.Posts(r.Context(), q.Get("company"), q.Get("round"),
		q.Get("withContent") == "1", onlyQ, page, size, rangeFilter(q))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if list == nil {
		list = []model.Post{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": total, "page": page, "size": size, "items": list})
}

func (s *Server) post(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, "id 必须是整数")
		return
	}
	p, err := s.st.Post(r.Context(), id)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if p == nil {
		fail(w, 404, "帖子不存在")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// reimport 用当前分类/聚类规则，从库里已有的帖子正文重建问题集合（无需重新抓取）。
func (s *Server) reimport(w http.ResponseWriter, r *http.Request) {
	st, err := importer.ImportAll(r.Context(), s.st, nil)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// static 提供前端静态文件；未知路径回落到 index.html。
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		fail(w, 404, "接口不存在")
		return
	}
	clean := filepath.Clean("/" + r.URL.Path)
	full := filepath.Join(s.webDir, clean)
	if fi, err := os.Stat(full); err == nil && !fi.IsDir() {
		http.ServeFile(w, r, full)
		return
	}
	idx := filepath.Join(s.webDir, "index.html")
	if _, err := os.Stat(idx); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fail(w, 404, "前端未构建：缺少 "+idx)
			return
		}
		fail(w, 500, err.Error())
		return
	}
	http.ServeFile(w, r, idx)
}
