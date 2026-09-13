#!/usr/bin/env bash
#
# crawl.sh —— 常驻抓取 runner
#
# 为什么需要它：抓全站是「挂几小时到几天」的事，中间难免遇到
# 网络抖动、进程被杀、站点长时间限流。一个裸的 fetch 进程死掉之后
# 就没人管了；而重跑的成本极低（已抓的按 URL 哈希跳过）。
# 所以这里用最简单的办法换鲁棒性：**死循环 + 重跑**。
#
# 两个终止条件：
#   1. fetch 报告「没有待抓内容」——本分片真的抓完了
#   2. 超过 -b 指定的总时长预算——避免无人值守时无限跑下去
#
# 用法：
#   scripts/crawl.sh 0 3 480          # 分片 0/3，跑 480 分钟
#   SCRIPT_DRY=1 scripts/crawl.sh 0 3 1   # 只打印命令行，不真跑
#
set -uo pipefail

SHARD="${1:-0}"
TOTAL="${2:-3}"
BUDGET_MIN="${3:-480}"

MIN_DELAY="${MIN_DELAY:-2s}"
MAX_DELAY="${MAX_DELAY:-5s}"
LONG_EVERY="${LONG_EVERY:-25}"
LONG_MIN="${LONG_MIN:-10s}"
LONG_MAX="${LONG_MAX:-35s}"
LIMIT="${LIMIT:-100000}"

BIN="${BIN:-./bin/nc-fetch}"
RAW="${RAW:-data/raw}"
LOGDIR="${LOGDIR:-data/logs}"
RESTART_WAIT="${RESTART_WAIT:-60}"   # 崩溃后等多久再拉起（给断路器一点时间）

mkdir -p "$LOGDIR"
LOG="$LOGDIR/shard-${SHARD}-of-${TOTAL}.log"
mkdir -p "$RAW"

ARGS=(-limit "$LIMIT" -shard "$SHARD/$TOTAL"
      -min "$MIN_DELAY" -max "$MAX_DELAY"
      -long-every "$LONG_EVERY" -long-min "$LONG_MIN" -long-max "$LONG_MAX"
      -raw "$RAW")

if [ -n "${SCRIPT_DRY:-}" ]; then
  echo "$BIN ${ARGS[*]}"
  exit 0
fi

if [ ! -x "$BIN" ]; then
  echo "找不到可执行文件 $BIN，先跑 make build" >&2
  exit 1
fi

deadline=$(( $(date +%s) + BUDGET_MIN * 60 ))
round=0

echo "=== 分片 $SHARD/$TOTAL 启动 $(date '+%F %T')，预算 ${BUDGET_MIN} 分钟，日志 $LOG ===" | tee -a "$LOG"

while :; do
  round=$(( round + 1 ))
  now=$(date +%s)
  if [ "$now" -ge "$deadline" ]; then
    echo "=== [分片 $SHARD] 到达时间预算，停止 $(date '+%F %T') ===" | tee -a "$LOG"
    break
  fi

  remain_min=$(( (deadline - now) / 60 ))
  echo "--- [分片 $SHARD] 第 $round 轮开始 $(date '+%F %T')，剩余预算 ${remain_min} 分钟 ---" | tee -a "$LOG"

  # tee 保证进度可见，同时留一份日志给「是否抓完」判断
  "$BIN" "${ARGS[@]}" 2>&1 | tee -a "$LOG"
  rc=${PIPESTATUS[0]}

  # 抓完就收工。注意顺序：先判「抓完」再判「出错」，
  # 因为正常结束也返回 0，而抓完的提示只在正常结束时打印。
  if tail -n 20 "$LOG" | grep -q "没有待抓内容"; then
    echo "=== [分片 $SHARD] 已抓完 $(date '+%F %T') ===" | tee -a "$LOG"
    break
  fi

  # 注意 ${rc} 必须加花括号：写成 "$rc，" 时，在某些 locale 下 bash 会把
  # 中文逗号的首字节当成变量名的一部分，于是报 "rc?: unbound variable"
  # （set -u 下每次轮次结束都刷一行错误，虽然不影响运行但很吵）。
  echo "--- [分片 $SHARD] 第 $round 轮结束 rc=${rc}，${RESTART_WAIT}s 后重试 ---" | tee -a "$LOG"
  sleep "$RESTART_WAIT"
done
