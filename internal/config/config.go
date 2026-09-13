// Package config 集中管理运行参数，全部可用环境变量覆盖。
//
// 凭据只从环境或 .env 读取，绝不写进代码或提交到仓库（见 .gitignore）。
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config 是服务配置。
type Config struct {
	Addr       string // 监听地址
	DSN        string // MySQL DSN
	WebDir     string // 前端静态目录
	UserID     int64  // 个人时间线抓取的默认目标用户（仅是种子数据，不代表题库来源）
	AuthorName string // 目标牛客用户昵称（仅用于标记来源）
	Cookie     string // 抓取用 Cookie（仅从环境读）
	CORSAllow  string // 允许的跨域来源，开发时用
}

// Load 读取配置。会先加载当前目录的 .env（若存在），环境变量优先。
func Load() Config {
	loadDotEnv(".env")

	c := Config{
		Addr:      env("NC_ADDR", "127.0.0.1:8787"),
		DSN:       buildDSN(),
		WebDir:    env("NC_WEB", "web"),
		CORSAllow: env("NC_CORS", "*"),
	}
	if v := os.Getenv("NC_USER_ID"); v != "" {
		c.UserID, _ = strconv.ParseInt(v, 10, 64)
	}
	if c.UserID == 0 {
		// 默认种子用户。全站面经走 cmd/fetch（sitemap），与这个 ID 无关。
		c.UserID = 125006155
	}
	// 昵称只用于在库里标记「这条来自谁」；拿不到就不写，别污染数据。
	c.AuthorName = os.Getenv("NC_AUTHOR_NAME")
	if c.AuthorName == "" && c.UserID == 125006155 {
		c.AuthorName = "赛文X" // 只用于给这批种子帖标注来源
	}
	c.Cookie = os.Getenv("NC_COOKIE")
	return c
}

// buildDSN 优先用完整 NC_DSN；否则用分项拼装。
//
// parseTime=false：posted_date 用 DATE_FORMAT 显式转字符串，避免 time.Time 的时区换算。
func buildDSN() string {
	if d := os.Getenv("NC_DSN"); d != "" {
		return d
	}
	user := env("NC_DB_USER", "nc_app")
	pass := os.Getenv("NC_DB_PASS")
	host := env("NC_DB_HOST", "127.0.0.1:3306")
	name := env("NC_DB_NAME", "nc_interview")
	return fmt.Sprintf("%s:%s@tcp(%s)/%s?charset=utf8mb4&collation=utf8mb4_unicode_ci&parseTime=false&loc=Local&timeout=5s",
		user, pass, host, name)
}

// Redacted 返回隐去密码的 DSN，用于日志输出。
func (c Config) RedactedDSN() string {
	at := strings.Index(c.DSN, "@")
	colon := strings.Index(c.DSN, ":")
	if at < 0 || colon < 0 || colon > at {
		return c.DSN
	}
	return c.DSN[:colon+1] + "****" + c.DSN[at:]
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// loadDotEnv 极简 .env 解析：KEY=VALUE，支持 # 注释与可选引号。
// 已存在的环境变量不覆盖（环境优先）。
func loadDotEnv(path string) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		if _, exists := os.LookupEnv(k); !exists {
			_ = os.Setenv(k, v)
		}
	}
}
