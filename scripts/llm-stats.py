#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""分析 LLM 归类结果，为「合并成可用类」提供依据。

用法：
    python3 scripts/llm-stats.py [data/llm]

输出：
  1. 覆盖率校验（10 批是否齐全、id 是否与批次文件一一对应）
  2. 44 个细分类的命中条数（降序）——合并决策的主要依据
  3. 「非题目」「其他」的具体内容
  4. 顶层域的分布
"""
import json
import sys
import glob
import os
import collections

root = sys.argv[1] if len(sys.argv) > 1 else 'data/llm'
tax = json.load(open(os.path.join(root, 'taxonomy.json'), encoding='utf-8'))
fine2domain = {}
for dom in tax['domains']:
    for c in dom['categories']:
        fine2domain[c['name']] = dom['name']
nonq = tax.get('unclassified', {}).get('name', '非题目')

# ---- 1. 覆盖校验 ----
batches = sorted(glob.glob(os.path.join(root, 'batches', 'batch-*.json')))
outs = {os.path.basename(f): json.load(open(f, encoding='utf-8'))
        for f in sorted(glob.glob(os.path.join(root, 'out', 'batch-*.json')))}
print('=' * 68)
print('一、覆盖率校验')
print('=' * 68)
missing, mismatch = [], []
total_assigned = 0
for bf in batches:
    name = os.path.basename(bf)
    src = json.load(open(bf, encoding='utf-8'))
    src_ids = [q['id'] for q in src['questions']]
    if name not in outs:
        missing.append(name)
        continue
    got = outs[name]['assignments']
    got_ids = [a['id'] for a in got]
    if sorted(got_ids) != sorted(src_ids):
        mismatch.append(f"{name}: 输入 {len(src_ids)} 条 / 输出 {len(got_ids)} 条")
    total_assigned += len(got)
print(f"  批次文件 {len(batches)} 个，产出 {len(outs)} 个")
print(f"  已完成 {len(outs)}/{len(batches)} 批，累计归类 {total_assigned} 条")
if missing:
    print(f"  ⏳ 未完成: {', '.join(missing)}")
if mismatch:
    print("  ❌ id 不匹配:")
    for m in mismatch:
        print('     ' + m)
if not missing and not mismatch:
    print("  ✅ 全部批次齐全且 id 一一对应")

# ---- 2. 细分类命中统计 ----
count = collections.Counter()
other_qs = collections.defaultdict(list)
src_text = {}
for bf in batches:
    for q in json.load(open(bf, encoding='utf-8'))['questions']:
        src_text[q['id']] = q['text']
for name, d in outs.items():
    for a in d['assignments']:
        count[a['cat']] += 1
        if a['cat'] in ('其他', nonq):
            other_qs[a['cat']].append(src_text.get(a['id'], '?'))

print()
print('=' * 68)
print('二、细分类命中条数（合并决策依据）')
print('=' * 68)
by_domain = collections.defaultdict(list)
for k, v in count.items():
    if k in ('其他', nonq):
        continue
    by_domain[fine2domain.get(k, '?未知域')].append((k, v))
for dom in sorted(by_domain, key=lambda d: -sum(v for _, v in by_domain[d])):
    items = sorted(by_domain[dom], key=lambda x: -x[1])
    print(f"\n【{dom}】{sum(v for _, v in items)} 条 / {len(items)} 类")
    for k, v in items:
        flag = '  ← 偏小，考虑并入' if v <= 4 else ''
        print(f"   {v:4}  {k}{flag}")

print()
print('=' * 68)
print('三、需要看原文的两类')
print('=' * 68)
for cat in ('其他', nonq):
    qs = other_qs.get(cat, [])
    print(f"\n【{cat}】{len(qs)} 条")
    for q in qs:
        print(f"   - {q[:76]}")

# ---- 4. 域分布 ----
print()
print('=' * 68)
print('四、顶层域分布')
print('=' * 68)
dc = collections.Counter()
for k, v in count.items():
    dc['非知识点' if k == nonq else fine2domain.get(k, '?未知域')] += v
for d, v in dc.most_common():
    print(f"   {v:4}  {d}")
print(f"\n   合计 {sum(dc.values())} 条")
print(f"   未使用任何分类的问题: {count.get('其他', 0)} 条（其他）")
