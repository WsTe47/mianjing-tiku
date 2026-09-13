package importer

import (
	"math/rand"
	"testing"
)

// TestSimilarLengthBoundHolds 验证聚类里那条长度剪枝是**等价**的。
//
// 剪枝依据：Similar = 1 - lev/max(la,lb)，而 lev >= |la-lb|，
// 所以 Similar >= T 必然有 |la-lb| <= (1-T)*max(la,lb)。
// 长度差超出这个界的两条不可能相似，直接跳过不必付 Levenshtein 的代价。
//
// 这条性质如果哪天不成立（比如 Similar 改成别的距离），热路径会开始悄悄漏掉
// 本该合并的簇——那正是本项目踩过的「不报错但结果错」的类型，所以要钉住。
func TestSimilarLengthBoundHolds(t *testing.T) {
	rnd := rand.New(rand.NewSource(7))
	alphabet := []rune("abcdefg进程线程锁缓存索引事务隔离级别网络协议")
	randStr := func(n int) string {
		r := make([]rune, n)
		for i := range r {
			r[i] = alphabet[rnd.Intn(len(alphabet))]
		}
		return string(r)
	}

	checked, pruned := 0, 0
	for i := 0; i < 4000; i++ {
		a := randStr(4 + rnd.Intn(30))
		var b string
		if rnd.Intn(2) == 0 {
			b = randStr(4 + rnd.Intn(30))
		} else {
			// 一半用 a 的近似变体，保证能造出「确实相似」的样本
			r := []rune(a)
			for k := 0; k < 1+rnd.Intn(3); k++ {
				r[rnd.Intn(len(r))] = alphabet[rnd.Intn(len(alphabet))]
			}
			b = string(r)
		}
		sim := Similar(a, b)
		la, lb := len([]rune(a)), len([]rune(b))
		mx := la
		if lb > mx {
			mx = lb
		}
		slack := int((1 - similarThreshold) * float64(mx))
		diff := la - lb
		if diff < 0 {
			diff = -diff
		}
		if sim >= similarThreshold {
			checked++
			if diff > slack {
				t.Fatalf("剪枝不成立：a=%q b=%q Sim=%.3f 但 |la-lb|=%d > slack=%d",
					a, b, sim, diff, slack)
			}
		} else if diff > slack {
			pruned++
		}
	}
	if checked < 100 {
		t.Fatalf("相似样本太少（%d），这条测试没验证到东西", checked)
	}
	t.Logf("相似样本 %d 条全部满足长度界；另有 %d 条被剪枝", checked, pruned)
}

// BenchmarkClusterRealistic 用接近真实分布的规模压聚类热路径。
//
// 优化前这里是「行数 × 簇数」次 []rune 转换 + 几乎每对都跑 Levenshtein，
// 全量重建（10 万行 / 6 万簇）跑一小时都进不到写库阶段。
func BenchmarkClusterRealistic(b *testing.B) {
	rnd := rand.New(rand.NewSource(11))
	alphabet := []rune("abcdefg进程线程锁缓存索引事务隔离级别网络协议微服务分布式")
	var rows []row
	for i := 0; i < 20000; i++ {
		n := 6 + rnd.Intn(24)
		r := make([]rune, n)
		for k := range r {
			r[k] = alphabet[rnd.Intn(len(alphabet))]
		}
		rows = append(rows, row{text: string(r), postID: int64(i)})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Cluster(rows)
	}
}

// TestSimilarAtLeastMatchesSimilar 是这次性能优化的安全性证明：
// 新的带状 + 提前退出实现必须与原实现**逐例一致**。
//
// 这类改动的危险在于「快是快了，但边界上偶尔判错」——那会悄悄改变聚类结果，
// 且不会报错。所以这里拿大量随机样本（含相似、不相似、等长、差很多长度的）
// 直接对拍。
func TestSimilarAtLeastMatchesSimilar(t *testing.T) {
	rnd := rand.New(rand.NewSource(23))
	alphabet := []rune("abcdefg进程线程锁缓存索引事务隔离级别网络协议微服务分布式")
	randStr := func(n int) string {
		r := make([]rune, n)
		for i := range r {
			r[i] = alphabet[rnd.Intn(len(alphabet))]
		}
		return string(r)
	}
	for i := 0; i < 6000; i++ {
		a := randStr(1 + rnd.Intn(30))
		var b string
		switch rnd.Intn(4) {
		case 0:
			b = randStr(1 + rnd.Intn(30))
		case 1: // 近似变体
			r := []rune(a)
			for k := 0; k < rnd.Intn(4); k++ {
				r[rnd.Intn(len(r))] = alphabet[rnd.Intn(len(alphabet))]
			}
			b = string(r)
		case 2: // 前缀/子串
			r := []rune(a)
			b = string(r[:1+rnd.Intn(len(r))])
		default: // 等长但内容不同
			b = randStr(len([]rune(a)))
		}
		want := Similar(a, b) >= similarThreshold
		got := similarAtLeast(a, b, similarThreshold)
		if want != got {
			t.Fatalf("判定不一致：a=%q b=%q Similar=%.4f（期望 %v，实际 %v）",
				a, b, Similar(a, b), want, got)
		}
	}
}
