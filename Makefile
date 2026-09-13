# nc-interview Makefile
#
# 配置来源：项目根目录的 .env（见 .env.example），或直接给环境变量。
#   NC_DB_USER / NC_DB_PASS / NC_DB_HOST / NC_DB_NAME   或完整 NC_DSN
#   NC_ADDR（默认 127.0.0.1:8787） / NC_COOKIE（仅抓取时需要）

BIN     := bin/nc
SCRAPE  := bin/nc-scrape
FETCH   := bin/nc-fetch
PARSE   := bin/nc-parse

.PHONY: help setup build run scrape import rebuild fmt vet test check clean \
        fetch fetch-dry fetch-check parse parse-only parse-dry all-sitemap llm-classify \
        test-db test-integration crawl llm-batches llm-sample llm-report

help: ## 显示帮助
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

setup: ## 建库建用户（会提示输入 MySQL root 密码）
	@mysql -u root -p < scripts/setup.sql
	@echo "完成。请把密码写入 .env：NC_DB_PASS=<你设的密码>"

build: ## 编译服务端与全部命令行工具
	@mkdir -p bin
	go build -o $(BIN) ./cmd/server
	go build -o $(SCRAPE) ./cmd/scrape
	go build -o $(FETCH) ./cmd/fetch
	go build -o $(PARSE) ./cmd/parse
	@echo "已生成 $(BIN) $(SCRAPE) $(FETCH) $(PARSE)"

run: ## 启动服务（默认 http://127.0.0.1:8787）
	go run ./cmd/server

scrape: ## 在线抓取个人主页时间线（需 NC_COOKIE）
	go run ./cmd/scrape -months 6

import: ## 从 data/posts.jsonl 导入（无需 Cookie）
	go run ./cmd/scrape -jsonl data/posts.jsonl

rebuild: ## 用库内帖子重建问题聚类（改了分类规则后跑；LLM 归类会按 norm 自动保留）
	go run ./cmd/scrape -rebuild

llm-apply: ## 导入 LLM 归类结果（读 data/llm/{taxonomy,merge}.json 与 out/）
	go run ./cmd/scrape -apply-llm data/llm

llm-stats: ## 分析 LLM 归类结果，输出合并决策依据
	python3 scripts/llm-stats.py data/llm

llm-batches: ## 从未归类的题生成批次文件（子代理读 batches/ 写 out/）
	go run ./cmd/llmbatch -dir data/llm

llm-sample: ## 生成分层抽样，用于让模型探索分类体系
	go run ./cmd/llmbatch -sample 300

llm-report: ## 只统计题库与归类覆盖情况
	go run ./cmd/llmbatch -report

# —— 全站面经：抓取（原始 HTML 落盘，可断点续抓、可重复解析） ——

fetch-dry: ## 只看本次会抓哪些 URL，不发请求
	go run ./cmd/fetch -limit 500 -dry

fetch-check: ## 复核已抓原始数据：删掉空壳页并让它们重新排队
	go run ./cmd/fetch -recheck

fetch: ## 抓取全站内容页（随机 1.5~4s/条 + 每 30 条长休息；Ctrl-C 可中断）
	go run ./cmd/fetch -limit 500

parse-dry: ## 试解析已抓原始数据，只统计不写库
	go run ./cmd/parse -dry -limit 20

parse: ## 解析已抓原始数据并写入库（含粗筛、切题、归类、清理空帖）
	go run ./cmd/parse -classify -prune

parse-only: ## 只解析入库，不重建聚类（数据量大时先入库，之后再单独 rebuild）
	go run ./cmd/parse

all-sitemap: fetch parse ## 抓一批再解析入库

llm-classify: ## 提示：LLM 归类为三步走，详见 docs/DEPLOYMENT.md 5.3
	@echo "① 探索:   make llm-sample        → data/llm/sample.json（分层抽样）"
	@echo "          让模型据此产出/扩充 data/llm/taxonomy.json"
	@echo "② 归类:   make llm-batches       → data/llm/batches/batch-NN.json"
	@echo "          子代理逐批归类，写 data/llm/out/batch-NN.json（⚠️ 每条必带 text）"
	@echo "③ 应用:   make llm-apply && make llm-stats"
	@echo "④ 合并:   编辑 data/llm/merge.json 后重跑 make llm-apply（不需再调模型）"

crawl: ## 常驻抓取（用法：make crawl SHARD=0/3 MINUTES=240）
	@mkdir -p bin && go build -o bin/nc-fetch ./cmd/fetch
	scripts/crawl.sh $(or $(SHARD),0/1) $(MINUTES)

fmt: ## 格式化
	gofmt -w .

vet: ## 静态检查
	go vet ./...

test-db: ## 建集成测试用的独立库（绝不碰生产库）
	@mysql -h127.0.0.1 -u root -p -e "CREATE DATABASE IF NOT EXISTS nc_interview_test \
		CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
	@echo "完成。跑集成测试请设置 NC_TEST_DSN，见 make test"

test: ## 跑测试（含集成测试，需要 NC_TEST_DSN；数据库相关用例无 DSN 时自动跳过）
	@if [ -n "$$NC_TEST_DSN" ]; then \
		go test ./...; \
	else \
		echo "提示：未设置 NC_TEST_DSN，将跳过需要 MySQL 的集成测试（那组用例专门抓 SQL 层 bug）"; \
		go test ./...; \
	fi

test-integration: ## 只跑需要 MySQL 的集成测试
	@test -n "$$NC_TEST_DSN" || (echo "请先导出 NC_TEST_DSN，见 docs/DEPLOYMENT.md"; exit 1)
	go test ./internal/store/ -v

check: fmt vet test build ## 一键：格式化 + 检查 + 测试 + 编译

clean: ## 清理编译产物（不动数据库，不动 data/raw）
	rm -rf bin
