package scraper

import (
	"regexp"
	"strings"
)

// 岗位分组：原始 job 串有 37 种写法，聚合后才适合做筛选维度。
const (
	JGAgent = "Agent 开发"
	JGFS    = "全栈开发"
	JGData  = "数据平台"
	JGInfra = "平台/Infra"
	JGAI    = "AI/算法"
	JGCpp   = "C++"
	JGGo    = "Go"
	JGOther = "其他"
)

var (
	reCpp = regexp.MustCompile(`(?i)c\+\+|cpp`)
	reGo  = regexp.MustCompile(`(?i)\bgo\b|golang`)
	reAI  = regexp.MustCompile(`(?i)ai|算法|大模型`)
)

// JobGroup 把岗位名归入分组。
func JobGroup(job string) string {
	j := strings.ToLower(job)
	switch {
	case strings.Contains(j, "agent"):
		return JGAgent
	case strings.Contains(job, "全栈"):
		return JGFS
	case strings.Contains(job, "数据"):
		return JGData
	case strings.Contains(j, "infra") || strings.Contains(job, "平台") || strings.Contains(job, "基础"):
		return JGInfra
	case reAI.MatchString(job):
		return JGAI
	case reCpp.MatchString(j):
		return JGCpp
	case reGo.MatchString(j):
		return JGGo
	default:
		return JGOther
	}
}

// RoundGroup 把轮次归入分组（复活赛保留原样，便于单列）。
func RoundGroup(round string) string {
	switch {
	case strings.Contains(strings.ToLower(round), "hr"):
		return "HR 面"
	case strings.Contains(round, "复活赛"):
		return "复活赛"
	case round == "一面", round == "二面":
		return round
	case round == "":
		return ""
	default:
		return "三面及以上"
	}
}
