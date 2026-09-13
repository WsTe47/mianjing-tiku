#!/usr/bin/env python3
"""
survey.py —— 统计 data/raw/ 里已抓样本的各项特征，用于决定抓取/清洗策略。

回答的问题：
  1. 产出率漏斗：抓多少条才能出一条面经？
  2. 噪声构成：题解 / 内推广告 / 打卡动态 / 闲聊 各占多少？
  3. 时间分布：抓到的帖子集中在哪些月份？（用于判断 sitemap 是否近似时间序）
  4. experienceQuestionList 命中率：有多少帖子带**结构化官方真题**？
     如果命中率不低，它比正文正则抽取质量更高，值得单独接一条管线。
  5. entityType 分布：能不能靠它在解析侧更早过滤？

用法：
  python3 scripts/survey.py [data/raw] [--limit N] [--sample-kw 关键词]
"""
import argparse
import gzip
import json
import os
import re
import sys
from collections import Counter

STATE = "window.__INITIAL_STATE__="


def extract_state(html: bytes):
    """花括号配对扫描，跳过字符串字面量。与 Go 侧 extractStateJSON 同逻辑。"""
    i = html.find(STATE.encode())
    if i < 0:
        return None
    start = i + len(STATE)
    if start >= len(html) or html[start:start + 1] != b"{":
        return None
    depth, in_str, esc = 0, False, False
    for p in range(start, len(html)):
        c = html[p:p + 1]
        if in_str:
            if esc:
                esc = False
            elif c == b"\\":
                esc = True
            elif c == b'"':
                in_str = False
            continue
        if c == b'"':
            in_str = True
        elif c == b"{":
            depth += 1
        elif c == b"}":
            depth -= 1
            if depth == 0:
                return html[start:p + 1]
    return None


def load_manifest(raw_dir):
    path = os.path.join(raw_dir, "_manifest.jsonl")
    out = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                out.append(json.loads(line))
            except json.JSONDecodeError:
                continue  # 可能正在被写
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("raw", nargs="?", default="data/raw")
    ap.add_argument("--limit", type=int, default=0)
    ap.add_argument("--sample-kw", default="", help="打印正文含该关键词的样本")
    args = ap.parse_args()

    entries = load_manifest(args.raw)
    if args.limit:
        entries = entries[: args.limit]
    print(f"清单共 {len(entries)} 条\n")

    entity = Counter()
    months = Counter()
    kinds = Counter()
    stats = Counter()
    kind_stat = {"/feed/": Counter(), "/discuss/": Counter()}
    eq_hits = []          # 带结构化真题的帖子
    sampled = []

    for e in entries:
        path = os.path.join(args.raw, e["hash"] + ".html.gz")
        try:
            with gzip.open(path, "rb") as f:
                html = f.read()
        except OSError:
            stats["文件缺失"] += 1
            continue

        state = extract_state(html)
        if state is None:
            stats["无 __INITIAL_STATE__"] += 1
            kinds["空壳/非内容页"] += 1
            continue
        try:
            root = json.loads(state)
        except json.JSONDecodeError:
            stats["JSON 截断"] += 1
            continue

        pref = root.get("prefetchData") or {}
        ssr = (pref.get("2") or {}).get("ssrCommonData") or {}
        cd = ssr.get("contentData") or {}
        if not cd:
            stats["无 contentData"] += 1
            kinds["非内容页"] += 1
            continue

        stats["解析成功"] += 1
        url_kind = "/feed/" if "/feed/" in e["url"] else "/discuss/"
        kind_stat[url_kind]["total"] += 1
        content = re.sub(r"<[^>]+>", "", cd.get("content") or "")
        title = (cd.get("title") or "").strip()
        entity[cd.get("entityType")] += 1

        ts = cd.get("createdAt") or cd.get("editTime")
        if ts:
            import datetime
            months[datetime.datetime.fromtimestamp(ts / 1000).strftime("%Y-%m")] += 1

        # 噪声分类（粗略，仅用于看构成）
        blob = title + "\n" + content
        if not content and not title:
            kinds_this = "空内容"
        elif re.search(r"内推码|内推入职奖励|招聘详情|热招岗位|宣讲会", blob) and \
                len(re.findall(r"面经|面试|一面|二面|面试官", blob)) < 2:
            kinds_this = "内推/招聘广告"
        elif title.startswith("题解") or "题解 |" in title:
            kinds_this = "题解"
        elif re.search(r"面经|面试|一面|二面|三面|笔试|机试|面试官", blob):
            kinds_this = "疑似面经"
        else:
            kinds_this = "其他闲聊"
        kinds[kinds_this] += 1

        if kinds_this in ("疑似面经",):
            kind_stat[url_kind]["interview"] += 1
        if kinds_this == "内推/招聘广告":
            kind_stat[url_kind]["ad"] += 1

        eq = ssr.get("experienceQuestionList") or []
        if eq:
            eq_hits.append((title, len(eq), eq[:3]))

        if args.sample_kw and args.sample_kw in blob:
            sampled.append((title, content[:200], len(eq)))

    print("=== 解析 ===")
    for k, v in stats.most_common():
        print(f"  {k:22} {v}")

    print("\n=== 内容构成（粗分类）===")
    tot = sum(kinds.values()) or 1
    for k, v in kinds.most_common():
        print(f"  {k:22} {v:6}  {v / tot * 100:5.1f}%")

    print("\n=== entityType ===")
    for k, v in entity.most_common():
        print(f"  {str(k):22} {v}")

    print("\n=== 时间分布（按月）===")
    for k, v in sorted(months.items()):
        print(f"  {k}  {v}")

    print("\n=== 按 URL 类型的产出率 ===")
    for k in ("/feed/", "/discuss/"):
        c = kind_stat[k]
        t = c["total"] or 1
        print(f"  {k:10} 抓取 {c['total']:5}  疑似面经 {c['interview']:4} ({c['interview']/t*100:4.1f}%)"
              f"  内推广告 {c['ad']:4} ({c['ad']/t*100:4.1f}%)")

    print(f"\n=== experienceQuestionList 命中 ===")
    print(f"  带结构化真题的帖子: {len(eq_hits)}/{sum(entity.values())}")
    for title, n, head in eq_hits[:8]:
        print(f"  - [{n} 题] {title[:40]}")
        for h in head:
            print(f"      {str(h)[:120]}")

    if sampled:
        print(f"\n=== 关键词样本 ===")
        for title, body, n in sampled[:10]:
            print(f"  - {title[:44]} (真题 {n})")
            print(f"      {body[:160]}")


if __name__ == "__main__":
    sys.exit(main())


# ---------- 追加分析：URL 类型产出率 + sitemap 时间序 ----------
# 这两件事直接决定抓取策略，所以单独做成可调用的函数。
