package scraper

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ---------- 断路器 ----------
//
// 多进程并行抓取时，单个进程的退避救不了整体：如果站点开始软限流
// （内容页返回空壳），三个 worker 会各自「礼貌地」降速，
// 但合起来仍在持续敲门，结果可能把 IP 送进更硬的封禁。
//
// 断路器通过一个共享文件把「现在该停」这件事广播给所有进程：
// 任一 worker 判定站点在限流，就写入一个「冷却到 T」的时间戳，
// 其余 worker 在每次请求前读这个文件，到点之前一律等待。
//
// 用文件而不是内存，是因为 worker 是独立进程（也可能分布在不同终端）。
// 写入用 rename 保证原子性，避免读到半截 JSON。

// BreakerState 是断路器落盘的状态。
type BreakerState struct {
	Until  int64  `json:"until"`  // 冷却到这个时刻（unix 毫秒）
	Reason string `json:"reason"` // 触发原因，便于事后复盘
	By     string `json:"by"`     // 哪个分片触发的
	SetAt  int64  `json:"setAt"`  // 触发时刻
}

// Breaker 是基于文件的跨进程冷却开关。
type Breaker struct {
	Path string
	ID   string // 本进程标识，写进 BreakerState.By
}

// NewBreaker 构造断路器。path 为空表示禁用（单进程调试时方便）。
func NewBreaker(path, id string) *Breaker { return &Breaker{Path: path, ID: id} }

// Load 读取当前状态；文件不存在或损坏都当作「未触发」。
func (b *Breaker) Load() BreakerState {
	var st BreakerState
	if b == nil || b.Path == "" {
		return st
	}
	raw, err := os.ReadFile(b.Path)
	if err != nil {
		return st
	}
	_ = json.Unmarshal(raw, &st)
	return st
}

// Remaining 返回还需等待多久；未触发或已到期返回 0。
func (b *Breaker) Remaining() time.Duration {
	st := b.Load()
	d := time.Until(time.UnixMilli(st.Until))
	if d < 0 {
		return 0
	}
	return d
}

// Wait 阻塞到冷却结束。返回原因字符串（未冷却则为空）。
//
// 刻意在**每次请求前**调用：冷却期间可能被别的 worker 延长，
// 只检查一次会在延长后提前开闸。
func (b *Breaker) Wait(ctx context.Context) (string, error) {
	for {
		st := b.Load()
		d := time.Until(time.UnixMilli(st.Until))
		if d <= 0 {
			return "", nil
		}
		select {
		case <-ctx.Done():
			return st.Reason, ctx.Err()
		case <-time.After(d):
			// 醒来后重新读一次：期间可能被延长
		}
	}
}

// Trip 触发（或延长）冷却。取「更晚的那个截止时间」，不缩短已有的冷却。
func (b *Breaker) Trip(d time.Duration, reason string) error {
	if b == nil || b.Path == "" {
		return nil
	}
	now := time.Now()
	until := now.Add(d).UnixMilli()
	if cur := b.Load(); cur.Until > until {
		until = cur.Until // 已有更长的冷却，不缩短
	}
	st := BreakerState{Until: until, Reason: reason, By: b.ID, SetAt: now.UnixMilli()}

	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := b.Path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, b.Path) // rename 原子，避免读到半截
}

// Clear 清除冷却（例如人工确认站点已恢复）。
func (b *Breaker) Clear() error {
	if b == nil || b.Path == "" {
		return nil
	}
	err := os.Remove(b.Path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// BreakerPath 返回原始数据目录下的断路器文件路径。
func BreakerPath(rawDir string) string { return filepath.Join(rawDir, "_breaker.json") }

// ShardOf 判断某个 URL 是否属于第 i 个分片（共 n 片）。
//
// 用 URL 的哈希取模而不是「按下标切片」：sitemap 的顺序会变
// （牛客随时新增内容），按下标切会让同一个 URL 在不同轮次落到不同分片，
// 于是「某分片已抓完」这个判断失效。哈希取模对同一个 URL 恒定。
func ShardOf(u string, i, n int) bool {
	if n <= 1 {
		return true
	}
	sum := sha1.Sum([]byte(u))
	// 用 sha1 前 8 字节当整数：分布足够均匀，且不受 URL 长度影响
	var v uint64
	for _, b := range sum[:8] {
		v = v<<8 | uint64(b)
	}
	return int(v%uint64(n)) == i
}

// ShardLabel 生成用于日志的稳定标识。
func ShardLabel(i, n int) string { return fmt.Sprintf("%d/%d", i, n) }
