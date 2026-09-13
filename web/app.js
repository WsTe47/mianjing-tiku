'use strict';
/* nc-interview 前端：全部数据来自 /api/*，不再内嵌数据。 */

const $ = (s, r = document) => r.querySelector(s);
const PAGE_SIZE = 60;

const TABS = [
  ['overview', '概览'],
  ['llm', '按知识点'],
  ['round', '按轮次'],
  ['job', '按岗位'],
  ['company', '按公司'],
  ['module', '按模块'],
  ['matrix', '公司×知识点'],
  ['study', '复习清单'],
  ['post', '面经原文'],
];

// 应用状态
const S = {
  chipExpand: {},
  tab: 'overview',
  filter: {
    round: '', job: '', company: '', category: '', topic: '',
    llmMerged: '', llmDomain: '', llmCat: '',
  },
  keyword: '',
  // range 是**全局**时间范围（按发布时间）。刻意不放进 S.filter：
  // 切页签时 S.filter 会被重置，而时间是全局的，应当跨页签保留。
  range: { from: '', to: '' },
  page: 1,
  total: 0,
  items: [],
  facets: null,
  meta: null,
  topics: null,
  taxonomy: null,
  loading: false,
};

// ---------- hash 状态同步（可分享/收藏筛选结果） ----------
const FILTER_KEYS = ['round', 'job', 'company', 'category', 'topic', 'llmMerged', 'llmDomain', 'llmCat', 'hasAnswer', 'officialOnly', 'includeNoise'];

function readHash() {
  const raw = decodeURIComponent(location.hash.slice(1));
  const [tab, qs] = raw.split('?');
  if (TABS.some(([k]) => k === tab)) S.tab = tab;
  if (qs) {
    const p = new URLSearchParams(qs);
    FILTER_KEYS.forEach((k) => { S.filter[k] = p.get(k) || ''; });
    S.keyword = p.get('q') || '';
    S.range = { from: p.get('from') || '', to: p.get('to') || '' };
  }
}

function writeHash() {
  const p = new URLSearchParams();
  FILTER_KEYS.forEach((k) => { if (S.filter[k]) p.set(k, S.filter[k]); });
  if (S.keyword) p.set('q', S.keyword);
  if (S.range.from) p.set('from', S.range.from);
  if (S.range.to) p.set('to', S.range.to);
  const qs = p.toString();
  history.replaceState(null, '', '#' + S.tab + (qs ? '?' + qs : ''));
}

readHash();

// rangeParams 返回当前全局时间范围，供所有接口调用展开。
//
// 时间范围是全局的：维度计数、矩阵、原文列表、顶部统计卡都要受它约束。
// 「筛了区间但某个榜还是全量」比不提供筛选更误导，所以这里统一注入。
function rangeParams() {
  return { from: S.range.from, to: S.range.to };
}

// ---------- HTTP ----------
async function api(path, params) {
  const u = new URL(path, location.origin);
  Object.entries(params || {}).forEach(([k, v]) => {
    if (v !== '' && v != null) u.searchParams.set(k, v);
  });
  const r = await fetch(u);
  if (!r.ok) {
    let msg = `HTTP ${r.status}`;
    try { msg = (await r.json()).error || msg; } catch (_) {}
    throw new Error(msg);
  }
  return r.json();
}

// ---------- 工具 ----------
const esc = (s) => String(s ?? '').replace(/[&<>"]/g, (c) =>
  ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
const tagCls = (t) => 't-' + String(t).replace(/[^\w\u4e00-\u9fff]/g, '');
const lvCls = (n) => (n >= 5 ? 'n4' : n >= 3 ? 'n3' : n >= 2 ? 'n2' : 'n1');

function bars(el, facets, limit = 14, unit = '条') {
  const list = (facets || []).slice(0, limit);
  if (!list.length) { el.innerHTML = '<div class="empty">无数据</div>'; return; }
  const mx = list[0].count || 1;
  el.innerHTML = list.map((f) => `<div class="bar">
    <div class="nm">${esc(f.value)}</div>
    <div class="tr"><div class="fl" style="width:${Math.max(3, f.count / mx * 100)}%"></div></div>
    <div class="vl">${f.count}${unit}</div></div>`).join('');
}

// ---------- 概览 ----------
function renderOverview(v) {
  const m = S.meta || {}, f = S.facets || {};
  const real = m.realQuestions ?? m.questions;
  const noise = (m.questions ?? 0) - real;
  v.innerHTML = `
    <div class="stats">
      <div class="stat"><b>${m.interviews ?? '-'}</b><i>篇面经</i></div>
      <div class="stat"><b>${m.companies ?? '-'}</b><i>家公司</i></div>
      <div class="stat"><b>${real ?? '-'}</b><i>组有效问题</i></div>
      <div class="stat"><b>${m.withAnswer ?? 0}</b><i>组带参考答案</i></div>
    </div>
    <div class="note">数据来自牛客网<b>公开面经</b>，覆盖多家公司的多轮面试。
    下方按<b>涉及公司数</b>排序，比单纯计数更能反映普遍性。<br>
    <b>带「答案」标记</b>的题目来自牛客官方整理的结构化真题（题目 + 参考答案），
    是题库里质量最高的部分，可用列表上方的「只看有答案」筛出来系统刷。<br>
    模块已把<b>«项目经历»</b>与<b>«知识点»</b>分开，知识点再拆为<b>计算机基础（四大件）</b>与<b>后端工程</b>；
    <b>«开场/通用»</b>（自我介绍、团队介绍、反问）只是流程套话，<b>不计入</b>下方考察主题的统计。
    ${noise > 0 ? `<br><span class="dim">另有 ${noise} 组经模型判定为面后感/攻略/广告等非题目，默认不列出。</span>` : ''}</div>
    <div class="sec">考察方向（模型归类） · 按题目数</div><div id="b-domains"></div>
    <div class="sec">考察主题 TOP 12</div><div id="b-merged"></div>
    <div class="sec">面试轮次分布</div><div id="b-rounds"></div>
    <div class="sec">岗位方向分布</div><div id="b-jobs"></div>
    <div class="sec">被问最多的公司 · TOP 14</div><div id="b-cos"></div>
    <div class="sec">最高频问题 · TOP 20</div><div id="top"></div>`;
  // 概览用**模型归类**而不是规则主题：规则主题是为早期「单用户 Agent 方向」
  // 那批数据调的，放到全站数据上 55% 会落进「其他技术」，看不出东西。
  // 规则主题仍然保留在「按模块」页里，两套分类并存是刻意的。
  bars($('#b-domains'), f.llmDomain, 8, '条');
  bars($('#b-merged'), f.llmMerged, 12, '条');
  bars($('#b-rounds'), f.rounds, 9, '条');
  bars($('#b-jobs'), f.jobs, 9, '条');
  bars($('#b-cos'), f.companies, 14, '条');
  loadTop();
}

// 展开/收起：用事件委托绑在容器上。
// 之前是拼 HTML 字符串后逐个绑 onclick，实际从没绑上——所以点了没反应。
function bindExpand(container) {
  if (!container) return;
  container.onclick = (e) => {
    // 「加载完整答案」按钮：不切换展开状态
    const more = e.target.closest('[data-ans-full]');
    if (more) {
      e.stopPropagation();
      loadDetail(more.dataset.ansFull, more.closest('.q'));
      return;
    }
    // 措辞折叠：展开全部 / 收起 / 还有 N 篇——都只重画措辞块，不切换卡片开合
    const vAll = e.target.closest('[data-var]');
    if (vAll) {
      e.stopPropagation();
      varOpen.add(vAll.dataset.var);
      refreshVariants(vAll.closest('.q'));
      return;
    }
    const vLess = e.target.closest('[data-varless]');
    if (vLess) {
      e.stopPropagation();
      varOpen.delete(vLess.dataset.varless);
      refreshVariants(vLess.closest('.q'));
      return;
    }
    const vSec = e.target.closest('[data-varsec]');
    if (vSec) {
      e.stopPropagation();
      const qid = vSec.dataset.varsec;
      if (varSecOpen.has(qid)) varSecOpen.delete(qid); else varSecOpen.add(qid);
      refreshVariants(vSec.closest('.q'));
      return;
    }
    const aLess = e.target.closest('[data-ans-less]');
    if (aLess) {
      e.stopPropagation();
      const qid = aLess.dataset.ansLess;
      ansOpen.delete(qid);
      const card = aLess.closest('.q');
      const ab = card.querySelector(`[data-ans-box="${qid}"]`);
      if (ab && card._q) ab.innerHTML = answerBlockFull(card._q, qid);
      return;
    }
    const oAll = e.target.closest('[data-occ-all]');
    if (oAll) {
      e.stopPropagation();
      occOpen.add(oAll.dataset.occAll);
      refreshOcc(oAll.closest('.q'));
      return;
    }
    const oLess = e.target.closest('[data-occ-less]');
    if (oLess) {
      e.stopPropagation();
      occOpen.delete(oLess.dataset.occLess);
      refreshOcc(oLess.closest('.q'));
      return;
    }
    const aEx = e.target.closest('[data-ans-expand]');
    if (aEx) {
      e.stopPropagation();
      const qid = aEx.dataset.ansExpand;
      ansOpen.add(qid);
      const card = aEx.closest('.q');
      const ab = card.querySelector(`[data-ans-box="${qid}"]`);
      if (ab && card._q) ab.innerHTML = answerBlockFull(card._q, qid);
      return;
    }
    const vSrc = e.target.closest('[data-vsrc]');
    if (vSrc) {
      e.stopPropagation();
      vsrcOpen.add(vSrc.dataset.vsrc);
      refreshVariants(vSrc.closest('.q'));
      return;
    }
    const h = e.target.closest('.qh');
    if (!h || !container.contains(h)) return;
    const card = h.parentElement;
    const b = card.querySelector('.qb');
    if (b) b.classList.toggle('on');
    // 展开时才补拉完整出现记录（列表接口只带前几条，省掉近九成体积）
    if (b && b.classList.contains('on')) {
      const box = card.querySelector('[data-occ-box]');
      loadDetail(box ? box.dataset.occBox : null, card);
    }
  };
}

async function loadTop() {
  try {
    const d = await api('/api/questions', { size: 20, ...rangeParams() });
    const box = $('#top');
    // 注意不能写 map(qCard)：map 会把下标当作第二个实参传给 open，导致「除第一项外全部默认展开」
    box.innerHTML = d.items.map((q) => qCard(q)).join('');
    bindExpand(box);
  } catch (e) { $('#top').innerHTML = `<div class="err">${esc(e.message)}</div>`; }
}

// ---------- 问题卡片 ----------
// 标签跟随当前视图：在「按知识点」里显示模型判定的细分类，其他视图显示规则主题。
// 两套分类并存是刻意的——规则分类快而稳，模型分类准而细，可互相参照。
function tagOf(q) {
  if (S.tab === 'llm') return q.llmCat || q.llmMerged || '';
  return q.topic || '';
}

// answerBlock 渲染参考答案。
//
// 答案来自牛客官方结构化真题（ssrCommonData.experienceQuestionList），
// 只有约 4% 的帖子带这个列表，但每题都配参考答案——所以答案存在与否
// 是这道题质量的一个强信号，值得在折叠行上用一个标记显出来。
// 答案文本 → 段落。答案常是 Markdown 味道的分点，按行渲染即可。
function answerParas(text) {
  return text
    .split(/\n{1,}/)
    .map((l) => l.trim())
    .filter(Boolean)
    .map((l) => `<p>${esc(l)}</p>`)
    .join('');
}

// answerBlock 渲染参考答案。
//
// 列表接口里的 answer 是**截断预览**（400 字），完整内容要用 /api/questions/{id} 取。
// 这样一页 50 条的响应从 ~447KB 降到 ~170KB，而用户真正展开看的通常只有一两条。
function answerBlock(q) {
  if (!q.answer) return '';
  const truncated = (q.answerLen || 0) > [...q.answer].length;
  const more = truncated
    ? `<div class="ans-more" data-ans-full="${q.id}">完整答案 ${q.answerLen} 字 · 点击加载 ▾</div>`
    : '';
  return `<div class="ans" data-ans-box="${q.id}">
    <div class="lb ans-lb">参考答案 <span class="ans-src">牛客结构化真题</span></div>
    ${answerParas(q.answer)}
    ${more}
  </div>`;
}

// 展开时按需取完整答案，拿到后原地替换，不重绘整个列表。

// 出现记录的渲染。
//
// 列表接口只带回前几条（它占整页响应体积近九成），完整列表在展开时按 id 取回，
// 所以这里由调用方传入 (列表, 总数) 两个值。
function renderOcc(list, total, qid) {
  const all = qid && occOpen.has(qid);
  const shown = all ? list : list.slice(0, OCC_SHOW);
  const occ = shown.map((o) => `<div class="oc">
      <span class="co">${esc(o.company)}</span>
      <span class="meta">${esc(o.roundGroup || o.round)} · ${esc(o.jobGroup || o.job)} · ${esc(o.date)}</span>
      ${o.official ? '<span class="oc-off">真题</span>' : ''}
      <a href="${esc(o.url)}" target="_blank" rel="noopener noreferrer">原文 ↗</a>
    </div>`).join('');
  const hidden = list.length - shown.length;
  let foot = '';
  if (hidden > 0) {
    foot = `<button class="var-more" data-occ-all="${qid}">展开全部 ${list.length} 条出现记录 ▾</button>`;
  } else if (all && list.length > OCC_SHOW) {
    foot = `<button class="var-more" data-occ-less="${qid}">收起 ▴</button>`;
  }
  // 列表接口本来就只带前几条，此时 hidden<=0，不显示按钮
  const note = (!qid && total > list.length)
    ? `<div class="oc-more">另有 ${total - list.length} 条出现记录，展开后加载…</div>` : '';
  return occ + foot + note;
}

// 其他措辞（预览态）。列表接口只给一个扁平列表。
function renderVariantPreview(q) {
  const vs = q.variants || [];
  if (!vs.length) return '';
  const more = (q.variantN || vs.length) > vs.length
    ? `<div class="oc-more">另有 ${q.variantN - vs.length} 种措辞，展开后加载</div>` : '';
  return `<div class="lb">其他措辞</div>`
    + vs.map((x) => `<div class="var">${esc(x)}</div>`).join('') + more;
}

// 其他措辞（完整态）：每种说法后面列出**它出自哪几篇面经**，可直接点开原文。
//
// 数量分布极其偏斜：99.6% 的题不到 8 种措辞，但极少数题是病态的——
// 「自我介绍一下。」有 322 种措辞、出处加起来上千条，全铺开就是几百行。
// 所以默认只列前 VAR_SHOW 种、每种只列前 VSRC_SHOW 篇，其余按需展开。
const VAR_SHOW = 8;
// 参考答案默认只露两行——用 CSS line-clamp 裁，所以不论窗口多宽都正好两行；
// 这里只保证 DOM 里塞的字够撑满两行。94% 的题答案在 1,500~3,000 字，全铺开就是一屏。
const ANSWER_PREVIEW_RUNES = 400;
// 出现记录同样要折叠：高频题动辄几百条（「自我介绍一下。」有 584 条），
// 全铺开是 160KB / 2900 行——比措辞还夸张。
const OCC_SHOW = 12;
const VSRC_SHOW = 3;
const occOpen = new Set();
const ansOpen = new Set();     // 已展开完整答案的题目 id
const varOpen = new Set();     // 已展开「全部 N 种」的题目 id
const varSecOpen = new Set();  // 已展开「其他措辞」整个模块的题目 id
const vsrcOpen = new Set();    // 已展开全部出处的 "题目id:第几种"

function renderVariantSources(sources, occs, qid) {
  if (!sources || !sources.length) return '';
  const n = sources.length;
  // 模块整体默认收起：这是展开卡片里最长的一块，先只给一行标题，想看再点开。
  // 点开之后仍只列前 VAR_SHOW 种（每种再只列前 VSRC_SHOW 篇）。
  const secOpen = varSecOpen.has(qid);
  const head = `<button class="sec-toggle" data-varsec="${qid}">`
    + `<span>其他措辞 · 各自出自哪篇面经</span>`
    + `<span class="sec-n">${n} 种</span>`
    + `<span class="sec-arrow">${secOpen ? '▴ 收起' : '▾ 展开'}</span></button>`;
  if (!secOpen) return head;

  const byPost = {};
  (occs || []).forEach((o) => { byPost[o.postId] = o; });

  const allVar = varOpen.has(qid);
  const shown = allVar ? sources : sources.slice(0, VAR_SHOW);

  const items = shown.map((v, i) => {
    const refsAll = (v.postIds || []).filter((p) => byPost[p]);
    const key = qid + ':' + i;
    const refs = vsrcOpen.has(key) ? refsAll : refsAll.slice(0, VSRC_SHOW);
    const chips = refs.map((pid) => {
      const o = byPost[pid];
      const bits = [o.company, o.roundGroup || o.round, o.date].filter(Boolean).map(esc).join(' · ');
      return `<a class="vsrc" href="${esc(o.url)}" target="_blank" rel="noopener noreferrer">${bits} ↗</a>`;
    }).join('');
    const rest = refsAll.length - refs.length;
    const restBtn = rest > 0
      ? `<button class="vsrc-more" data-vsrc="${key}">还有 ${rest} 篇 ▾</button>` : '';
    const cnt = (v.postIds || []).length;
    // 出现次数是判断「这条措辞可不可信」的主要线索，所以单独做成醒目的角标
    return `<div class="var-item">
      <div class="var">${esc(v.text)}${cnt > 1 ? `<span class="var-n">×${cnt}</span>` : ''}</div>
      <div class="vsrc-list">${chips}${restBtn}</div>
    </div>`;
  }).join('');

  const hidden = n - shown.length;
  let foot = '';
  if (hidden > 0) {
    foot = `<button class="var-more" data-var="${qid}">展开全部 ${n} 种措辞 ▾</button>`;
  } else if (allVar && n > VAR_SHOW) {
    foot = `<button class="var-more" data-varless="${qid}">收起 ▴</button>`;
  }
  return head + items + foot;
}

function answerBlockFull(q, id) {
  const txt = q.answer || '';
  if (!txt) return '';
  const head = `<div class="lb ans-lb">参考答案 <span class="ans-src">牛客结构化真题</span></div>`;
  const runes = Array.from(txt);
  if (ansOpen.has(id)) {
    return head + answerParas(txt)
      + `<button class="var-more" data-ans-less="${id}">收起 ▴</button>`;
  }
  // 折叠态：塞 400 字再交给 CSS 裁成两行（猜字数的话，窄窗口会变四行、宽窗口一行半）
  const preview = runes.slice(0, ANSWER_PREVIEW_RUNES).join('').replace(/\s*\n+\s*/g, ' ').trim();
  return head
    + `<div class="ans-clamp">${esc(preview)}</div>`
    + `<button class="var-more" data-ans-expand="${id}">展开全文（共 ${runes.length} 字）▾</button>`;
}

function refreshOcc(card) {
  const q = card && card._q;
  if (!q) return;
  const box = card.querySelector('[data-occ-box]');
  if (box) box.innerHTML = renderOcc(q.occurrences || [], q.n, q.id);
}

// refreshVariants 只重画措辞块（折叠/展开时用），不动卡片其余部分。
function refreshVariants(card) {
  const q = card && card._q;
  if (!q) return;
  const vb = card.querySelector('[data-var-box]');
  if (vb) vb.innerHTML = renderVariantSources(q.variantSources, q.occurrences, q.id);
}

// loadDetail 展开卡片时把完整内容取回来。
//
// 列表接口为了体积只给预览（出现记录 6 条、措辞 12 条、答案 400 字），
// 所以这三样都在这里补齐，且只请求一次。
async function loadDetail(id, card) {
  if (!card || card.dataset.detailLoaded) return;
  card.dataset.detailLoaded = '1';
  try {
    const q = await api('/api/questions/' + id);
    card._q = q;   // 缓存下来，折叠/展开措辞时不必重新请求
    const occ = card.querySelector('[data-occ-box]');
    if (occ) occ.innerHTML = renderOcc(q.occurrences || [], q.n, id);
    const vb = card.querySelector('[data-var-box]');
    if (vb && q.variantSources) vb.innerHTML = renderVariantSources(q.variantSources, q.occurrences, id);
    const ab = card.querySelector(`[data-ans-box="${id}"]`);
    if (ab && q.answer) {
      ab.innerHTML = answerBlockFull(q, id);
      const btn = card.querySelector('[data-ans-full]');
      if (btn) btn.remove();
    }
  } catch (e) {
    delete card.dataset.detailLoaded;   // 失败保留预览，下次展开还能重试
  }
}

function qCard(q, open = false) {
  const tag = tagOf(q);
  const variants = `<div data-var-box="${q.id}">${renderVariantPreview(q)}</div>`;
  // officialN = 该题在官方结构化真题里出现的次数，>0 说明有权威出处
  const offBadge = q.officialN
    ? `<span class="badge-off" title="来自牛客结构化真题 ${q.officialN} 次">真题</span>`
    : '';
  const ansBadge = q.answer ? `<span class="badge-ans" title="有参考答案">答案</span>` : '';
  const shown = (q.occurrences || []).length;
  return `<div class="q">
    <div class="qh">
      <div class="n ${lvCls(q.n)}">${q.n}</div>
      <div class="qt">${esc(q.canonical)}${tag ? `<span class="tag ${tagCls(tag)}">${esc(tag)}</span>` : ''}${offBadge}${ansBadge}</div>
    </div>
    <div class="qb${open ? ' on' : ''}">
      ${answerBlock(q)}
      ${variants}
      <div class="lb">被问到的场合（公司 · 轮次 · 岗位 · 日期）</div>
      <div class="occ" data-occ-box="${q.id}"${q.n > shown ? ' data-occ-more="1"' : ''}>${renderOcc(q.occurrences || [], q.n)}</div>
    </div>
  </div>`;
}

// ---------- 列表视图 ----------
// 每个维度默认最多显示多少个 chip。
//
// 为什么必须封顶：抓全站后公司维度有 209 个取值、细分类有 82 个，
// 全部铺开会变成一堵几百个 chip 的墙（手机上要滑很久才能看到内容），
// 而且每个 chip 都带计数，DOM 也白白变重。
//
// 规则：
//   - 默认只显示前 CHIP_LIMIT 个（按计数降序，最有代表性的排在前面）
//   - 末尾给一个「更多」chip 展开全部
//   - **当前选中的值即使排在第 100 位也一定显示**，否则用户看不出自己在筛什么
const CHIP_LIMIT = 16;

function chipRow(items, cur, key, allLabel) {
  const mk = (val, label, cnt) =>
    `<div class="chip${cur === val ? ' on' : ''}" data-k="${key}" data-v="${esc(val)}">${esc(label)}${cnt != null ? `<span class="c">${cnt}</span>` : ''}</div>`;

  const all = items || [];
  const expanded = !!(S.chipExpand && S.chipExpand[key]);
  let shown = all;
  let moreChip = '';
  if (!expanded && all.length > CHIP_LIMIT) {
    shown = all.slice(0, CHIP_LIMIT);
    // 选中的值如果被截断在外面，补进来，避免「筛了但看不见」
    if (cur && !shown.some((f) => f.value === cur)) {
      const hit = all.find((f) => f.value === cur);
      if (hit) shown = shown.concat([hit]);
    }
    moreChip = `<div class="chip more-chip" data-more="${esc(key)}">展开全部 ${all.length} 项 ▾</div>`;
  } else if (expanded && all.length > CHIP_LIMIT) {
    moreChip = `<div class="chip more-chip" data-more="${esc(key)}">收起 ▴</div>`;
  }
  return mk('', allLabel, null) + shown.map((f) => mk(f.value, f.value, f.count)).join('') + moreChip;
}

async function renderList() {
  const v = $('#view');
  const f = S.facets || {};
  let chips = '';
  if (S.tab === 'round') chips = chipRow(f.rounds, S.filter.round, 'round', '全部轮次');
  else if (S.tab === 'job') chips = chipRow(f.jobs, S.filter.job, 'job', '全部岗位');
  else if (S.tab === 'company') chips = chipRow(f.companies, S.filter.company, 'company', '全部公司');
  else if (S.tab === 'llm') chips = chipRow(f.llmMerged, S.filter.llmMerged, 'llmMerged', '全部知识点');
  else chips = chipRow(f.categories, S.filter.category, 'category', '全部模块');

  // 二级筛选
  let topicRow = '';
  if (S.tab === 'module' && S.filter.category) {
    // 二级下钻用 **LLM 细分类**而不是规则主题。
    // 规则主题在这一页已经失效：「后端工程」模块下 75% 的题落进「其他技术」，
    // 下钻等于没下钻；同一批题的 LLM 细分类分布才是可用信息。
    let cats = [];
    try { cats = await api('/api/llmcats', { category: S.filter.category, ...rangeParams() }); } catch (_) {}
    S.llmCatsInCat = cats;
    topicRow = `<div class="chips" id="topics">${chipRow(cats, S.filter.llmCat, 'llmCat', '全部知识点')}</div>`;
  }
  if (S.tab === 'llm' && S.filter.llmMerged) {
    // 二级：该可用类下的明细。先给「顶层域」，再给「细分类」，便于逐层收窄
    const fine = (S.taxonomy || []).filter((t) => t.merged === S.filter.llmMerged);
    const domains = [...new Set(fine.map((t) => t.domain))]
      .map((d) => ({ value: d, count: f.llmDomain.find((x) => x.value === d)?.count || 0 }));
    topicRow = `<div class="chips" id="topics">${chipRow(domains, S.filter.llmDomain, 'llmDomain', '全部域')}</div>`;
    const cats = fine
      .filter((t) => !S.filter.llmDomain || t.domain === S.filter.llmDomain)
      .map((t) => ({ value: t.name, count: f.llmCat.find((x) => x.value === t.name)?.count || 0 }))
      .filter((c) => c.count > 0);
    if (cats.length) {
      topicRow += `<div class="chips" id="cats">${chipRow(cats, S.filter.llmCat, 'llmCat', '全部细分类')}</div>`;
    }
  }

  // 质量开关：有答案的题来自官方结构化真题，是这份题库里最值得先刷的部分
  const quality = `<div class="chips qfilter" id="qf">
    <div class="chip${S.filter.hasAnswer ? ' on' : ''}" data-k="hasAnswer" data-v="${S.filter.hasAnswer ? '' : '1'}">只看有答案</div>
    <div class="chip${S.filter.officialOnly ? ' on' : ''}" data-k="officialOnly" data-v="${S.filter.officialOnly ? '' : '1'}">只看官方真题</div>
    <div class="chip${S.filter.includeNoise ? ' on' : ''}" data-k="includeNoise" data-v="${S.filter.includeNoise ? '' : '1'}">显示未归类与非题目</div>
  </div>`;

  v.innerHTML = `<div class="chips" id="chips">${chips}</div>${topicRow}${quality}
    <input class="search" id="kw" placeholder="搜索问题关键词（如 协程 / MCP / LRU）" value="${esc(S.keyword)}">
    <div id="list"></div>`;

  $('#chips').onclick = onChip;
  $('#qf').onclick = onChip;
  ['#topics', '#cats'].forEach((sel) => { const e = $(sel); if (e) e.onclick = onChip; });
  const kw = $('#kw');
  let timer;
  kw.oninput = () => {
    clearTimeout(timer);
    timer = setTimeout(() => { S.keyword = kw.value.trim(); reloadList(); }, 260);
  };
  loadList(true);
}

function onChip(e) {
  // 「展开全部 / 收起」不是筛选条件，单独处理
  const more = e.target.closest('[data-more]');
  if (more) {
    const k = more.dataset.more;
    S.chipExpand = S.chipExpand || {};
    S.chipExpand[k] = !S.chipExpand[k];
    render(); // 只重画 chips，不重置筛选
    return;
  }
  const el = e.target.closest('.chip');
  if (!el) return;
  S.filter[el.dataset.k] = el.dataset.v;
  // 改变上级筛选时要清掉下级，否则会筛出空结果
  if (el.dataset.k === 'category') { S.filter.topic = ''; S.filter.llmCat = ''; }
  if (el.dataset.k === 'llmMerged') { S.filter.llmDomain = ''; S.filter.llmCat = ''; }
  if (el.dataset.k === 'llmDomain') S.filter.llmCat = '';
  reload();
}

// 换筛选维度：需要重画 chips（模块变化会改变主题列表）
function reload() { S.page = 1; writeHash(); render(); }
// 仅改关键词：只重取列表，不重建 chips
function reloadList() { S.page = 1; writeHash(); loadList(true); }

async function loadList(reset) {
  if (S.loading) return;
  S.loading = true;
  const box = $('#list');
  const btn = $('#more');
  if (btn) btn.disabled = true;
  if (reset) box.innerHTML = '<div class="empty">加载中…</div>';
  try {
    const d = await api('/api/questions', {
      ...S.filter, ...rangeParams(), q: S.keyword, page: S.page, size: PAGE_SIZE,
      // 打开「显示未归类与非题目」时必须同时 showAll，
      // 否则后端的「默认只看已核验」规则会把未归类的题继续挡掉。
      showAll: S.filter.includeNoise ? '1' : '',
    });
    S.total = d.total;
    // 同上：map(qCard) 会把下标当 open 传进去，必须包一层
    const html = d.items.map((q) => qCard(q)).join('');
    if (reset) box.innerHTML =
      `<div class="note">共 <b>${d.total}</b> 组问题 · 按出现次数降序</div>` + (html || '<div class="empty">没有匹配的问题</div>');
    else box.insertAdjacentHTML('beforeend', html);
    bindExpand(box);
    // 分页
    if (btn) btn.remove();
    if (S.page * PAGE_SIZE < d.total) {
      box.insertAdjacentHTML('beforeend',
        `<button class="more" id="more">加载更多（已显示 ${S.page * PAGE_SIZE} / ${d.total}）</button>`);
      $('#more').onclick = () => { S.page++; loadList(false); };
    }
  } catch (e) {
    box.innerHTML = `<div class="err">加载失败：${esc(e.message)}</div>`;
  } finally {
    S.loading = false;
  }
}

// ---------- 渲染调度 ----------
function render() {
  document.querySelectorAll('.tab').forEach((t) => t.classList.toggle('on', t.dataset.k === S.tab));
  syncRangeBar();
  if (S.tab === 'overview') renderOverview($('#view'));
  else if (S.tab === 'post') renderPosts();
  else if (S.tab === 'matrix') renderMatrix();
  else if (S.tab === 'study') renderStudy();
  else renderList();
}

// ---------- 复习清单 ----------
//
// 和其它视图的本质区别是**排序依据**：那些视图按出现次数排，而次数会被
// 「同一家公司刷很多帖」顶高。这里按复习优先级排：
//     公司覆盖面 × 3 + min(频次, 20) + (有参考答案 ? 4 : 0)
// 覆盖面权重最高——被 20 家公司各问一次，比被一家公司问 30 次更能说明「普遍要会」。
// 权重是刻意写成可解释的，不是调出来的黑箱。
//
// 清单给到 100 条并配打印样式：这个视图的实际用法就是「打印出来当复习提纲」。
const STUDY_SIZE = 100;

async function renderStudy() {
  const v = $('#view');
  v.innerHTML = '<div class="empty">加载中…</div>';
  try {
    const d = await api('/api/questions', { sort: 'priority', size: STUDY_SIZE, page: 1, ...rangeParams() });
    const rows = d.items.map((q, i) => `<div class="st-row">
        <div class="st-rank">${i + 1}</div>
        <div class="st-main">
          <div class="st-q">${esc(q.canonical)}${q.answer ? '<span class="badge-ans">答案</span>' : ''}</div>
          <div class="st-meta">${q.coN || 0} 家公司问过 · 共出现 ${q.n} 次${q.llmMerged ? ' · ' + esc(q.llmMerged) : ''}</div>
        </div>
      </div>`).join('');
    v.innerHTML = `<div class="note">按<b>复习优先级</b>排序：公司覆盖面 × 3 + 频次（上限 20）+ 有答案加 4。
      覆盖面权重最高，是为了避免「一家公司刷很多帖」把某道题顶上来。
      点「打印」会输出一份不带页头页签的干净清单。</div>
      <div class="st-bar"><button id="st-print" class="more">打印这份清单</button>
        <span class="dim">共 ${d.total} 组，按优先级取前 ${d.items.length} 组</span></div>
      <div class="st-list">${rows || '<div class="empty">没有可展示的题目</div>'}</div>`;
    const btn = $('#st-print');
    if (btn) btn.onclick = () => window.print();
  } catch (e) { v.innerHTML = `<div class="err">${esc(e.message)}</div>`; }
}

// ---------- 公司 × 知识点 矩阵 ----------
//
// 单维度视图回答不了「某家公司特别爱问什么」——按公司看只是一串名字，
// 按知识点看只是一串分类。矩阵把两个维度交叉起来，一眼能看出
// 「字节偏算法、美团偏业务」这类结构性差异。
//
// 单元格用**去重后的题目组数**而不是出现次数：一家刷了很多帖的公司在矩阵里
// 不该因为重复提问而虚高。
async function renderMatrix() {
  const v = $('#view');
  v.innerHTML = '<div class="empty">加载中…</div>';
  try {
    const m = await api('/api/matrix', { companies: 14, cats: 12, ...rangeParams() });
    if (!m.companies || !m.companies.length) {
      v.innerHTML = '<div class="empty">数据还不够，先生成矩阵。</div>';
      return;
    }
    const max = Math.max(1, ...m.cells.flat());
    const head = `<tr><th class="mx-co">公司 ＼ 知识点</th>`
      + m.cats.map((c) => `<th class="mx-cat">${esc(c)}</th>`).join('')
      + `<th class="mx-tot">合计</th></tr>`;
    const body = m.companies.map((co, i) => `<tr><th class="mx-co">${esc(co)}</th>`
      + m.cells[i].map((n, j) => `<td class="mx-c" data-co="${esc(co)}" data-cat="${esc(m.cats[j])}"`
          + ` style="--h:${(n / max * 0.8).toFixed(3)}"`
          + ` title="${esc(co)} × ${esc(m.cats[j])}：${n} 组题">${n || ''}</td>`).join('')
      + `<td class="mx-tot">${m.rowTotal[i]}</td></tr>`).join('');
    v.innerHTML = `<div class="mx-hint">格子 = 该公司在该知识点上被问到的<b>题目组数</b>`
      + `（同一道题被问多次只算 1）。<b>点格子</b>可直接跳到这一组合的问题列表；颜色越深题量越大。</div>`
      + `<div class="mx-wrap"><table class="mx">${head}${body}`
      + `<tr><th class="mx-co">合计</th>`
      + m.colTotal.map((n) => `<td class="mx-tot">${n}</td>`).join('')
      + `<td class="mx-tot"></td></tr></table></div>`;
    v.querySelectorAll('.mx-c').forEach((td) => {
      td.onclick = () => {
        if (!td.textContent.trim()) return;
        // 直接落到「按知识点」视图并带上两个筛选条件（nav() 会清空筛选，所以不能用它）
        S.filter = { round: '', job: '', company: td.dataset.co, category: '', topic: '',
                     llmMerged: td.dataset.cat, llmDomain: '', llmCat: '' };
        S.keyword = ''; S.page = 1; S.tab = 'llm';
        writeHash(); window.scrollTo({ top: 0 }); render();
      };
    });
  } catch (e) { v.innerHTML = `<div class="err">${esc(e.message)}</div>`; }
}

// ---------- 面经原文 ----------
//
// 前面几个页签都是「按题目看」——把 2 万条问题按维度切。但面经的价值不只在题目：
// 作者的完整叙述（面试流程、节奏、问到什么深度、怎么被追问）是题目列表切没了的。
// 所以单开一个页签，直接翻原文。
//
// 正文整篇返回即可：实测一页 20 篇约 43KB，比题目列表还轻
//（题目列表大头是出现记录，一篇帖子只有一份正文）。
function pCard(p) {
  const meta = [p.company, p.roundGroup || p.round, p.jobGroup, p.postedDate]
    .filter(Boolean).map((x) => esc(x)).join(' · ');
  const eng = [
    p.viewCnt ? `${p.viewCnt} 浏览` : '',
    p.likeCnt ? `${p.likeCnt} 赞` : '',
    p.commentCnt ? `${p.commentCnt} 评论` : '',
  ].filter(Boolean).join(' · ');
  const body = (p.content || '').trim();
  const paras = body
    ? body.split(/\n{1,}/).map((l) => l.trim()).filter(Boolean).map((l) => `<p>${esc(l)}</p>`).join('')
    : '<p class="dim">（这篇没有正文，可能只发了图片）</p>';
  return `<div class="post">
    <div class="ph">
      <div class="pt">${esc(p.title || '（无标题）')}</div>
      <div class="pm">${meta}${p.authorName ? ' · ' + esc(p.authorName) : ''}</div>
      ${eng ? `<div class="pe">${eng}</div>` : ''}
    </div>
    <div class="pb">
      ${paras}
      <div class="pl">
        <a href="${esc(p.url)}" target="_blank" rel="noopener noreferrer">查看原文 ↗</a>
        ${p.topicTag ? `<span class="tag">${esc(p.topicTag)}</span>` : ''}
      </div>
    </div>
  </div>`;
}

async function renderPosts() {
  const v = $('#view');
  const f = S.facets || {};
  const chips = chipRow(f.companies, S.filter.company, 'company', '全部公司');
  v.innerHTML = `<div class="chips" id="chips">${chips}</div>
    <div id="plist"></div>`;
  $('#chips').onclick = (e) => {
    const el = e.target.closest('.chip');
    if (!el) return;
    S.filter.company = el.dataset.v;
    S.page = 1;
    loadPosts(true);
  };
  loadPosts(true);
}

async function loadPosts(reset) {
  if (S.loading) return;
  S.loading = true;
  const box = $('#plist');
  if (reset) box.innerHTML = '<div class="empty">加载中…</div>';
  try {
    const d = await api('/api/posts', { company: S.filter.company, page: S.page, size: PAGE_SIZE, ...rangeParams() });
    S.postTotal = d.total;
    const html = d.items.map(pCard).join('');
    if (reset) {
      box.innerHTML = `<div class="note">共 <b>${d.total}</b> 篇面经原文 · 按发布时间降序</div>` +
        (html || '<div class="empty">没有匹配的面经</div>');
    } else {
      box.insertAdjacentHTML('beforeend', html);
    }
    if (S.page * PAGE_SIZE < d.total) {
      const old = box.querySelector('.more'); if (old) old.remove();
      box.insertAdjacentHTML('beforeend',
        `<button class="more" id="more">加载更多（已显示 ${Math.min(S.page * PAGE_SIZE, d.total)} / ${d.total}）</button>`);
      $('#more').onclick = () => { S.page++; loadPosts(false); };
    }
  } catch (e) {
    box.innerHTML = `<div class="err">加载失败：${esc(e.message)}</div>`;
  } finally {
    S.loading = false;
  }
}

function nav(k) {
  S.tab = k;
  S.filter = { round: '', job: '', company: '', category: '', topic: '' };
  S.keyword = '';
  S.page = 1;
  writeHash();
  window.scrollTo({ top: 0, behavior: 'smooth' });
  render();
}

// 浏览器前进/后退时同步状态
window.addEventListener('hashchange', () => {
  S.filter = { round: '', job: '', company: '', category: '', topic: '' };
  S.keyword = '';
  readHash();
  S.page = 1;
  render();
});

// loadMetaAndFacets 取「全局范围相关」的全部派生数据。
//
// 时间范围一变，这些数字全都得重算——所以集中在这里，供 boot 与时间栏复用。
async function loadMetaAndFacets() {
  const [meta, facets, taxonomy] = await Promise.all([
    api('/api/meta', rangeParams()), api('/api/facets', rangeParams()), api('/api/taxonomy'),
  ]);
  S.meta = meta; S.facets = facets; S.taxonomy = taxonomy;
  {
    const meta_ = meta;
    // 数据来源已不止一个用户，所以这里只讲规模与构成，不提具体个人
    const ansPart = meta_.withAnswer ? ` · <b>${meta.withAnswer}</b> 组带参考答案` : '';
    const noise = meta_.noiseQuestions ?? 0;
    const unlabeled = meta_.unlabeled ?? 0;
    const extra = [];
    if (noise) extra.push(`${noise} 组经模型判定为非题目`);
    if (unlabeled) extra.push(`${unlabeled} 组尚未归类`);
    const noisePart = extra.length
      ? `<span class="dim">（另有 ${extra.join('、')}，默认不列出）</span>`
      : '';
    $('#sub').innerHTML =
      `收录 <b>${meta_.interviews}</b> 篇面经 · <b>${meta_.companies}</b> 家公司<br>
       ${meta_.occurrences} 条题目记录 → 归并 <b>${meta_.realQuestions ?? meta_.questions}</b> 组有效问题${ansPart}${noisePart}<br>
       ${meta_.dateFrom} ~ ${meta_.dateTo}`;
    $('#rng').textContent = `${meta_.dateFrom} ~ ${meta_.dateTo}`;
  }
}

// reloadAll 在全局时间范围变化后重取所有派生数据并重绘。
async function reloadAll() {
  try {
    await loadMetaAndFacets();
  } catch (_) { /* 网络抖动时保留旧数字，总比空白好 */ }
  renderRangeBar();
  render();
}

// renderRangeBar 构建全局时间范围控件（只在启动时构建一次 DOM）。
function renderRangeBar() {
  const box = $('#rangebar');
  if (!box) return;
  const m = S.meta || {};
  const presets = [['全部', ''], ['近 7 天', 7], ['近 30 天', 30], ['近 90 天', 90]];
  box.innerHTML = `<span class="rb-label">发布时间</span>`
    + presets.map(([label, d]) => `<button class="rb-preset" data-days="${d}">${esc(label)}</button>`).join('')
    + `<input type="date" id="rb-from" class="rb-date" title="起始日期"`
    + ` min="${esc(m.globalFrom || m.dateFrom || '')}" max="${esc(m.globalTo || m.dateTo || '')}">`
    + `<span class="rb-sep">→</span>`
    + `<input type="date" id="rb-to" class="rb-date" title="结束日期"`
    + ` min="${esc(m.globalFrom || m.dateFrom || '')}" max="${esc(m.globalTo || m.dateTo || '')}">`
    + `<button class="rb-clear" id="rb-clear">清除</button>`
    + `<span class="rb-hint"></span>`;

  const apply = (from, to) => {
    S.range = { from: from || '', to: to || '' };
    S.page = 1;
    writeHash();
    reloadAll();
  };
  box.querySelectorAll('.rb-preset').forEach((b) => {
    b.onclick = () => {
      const d = b.dataset.days;
      if (d === '') return apply('', '');
      const to = new Date();
      const from = new Date();
      from.setDate(from.getDate() - Number(d) + 1);
      apply(iso(from), iso(to));
    };
  });
  const f = $('#rb-from');
  const t = $('#rb-to');
  if (f) f.onchange = () => apply(f.value, t.value);
  if (t) t.onchange = () => apply(f.value, t.value);
  const c = $('#rb-clear');
  if (c) c.onclick = () => apply('', '');
  syncRangeBar();
}

const iso = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;

// syncRangeBar 只同步「选中态」，不重建 DOM——否则用户正在改的日期输入会被打断。
function syncRangeBar() {
  const box = $('#rangebar');
  if (!box) return;
  const f = $('#rb-from');
  const t = $('#rb-to');
  if (f && document.activeElement !== f) f.value = S.range.from;
  if (t && document.activeElement !== t) t.value = S.range.to;
  const on = !!(S.range.from || S.range.to);
  box.querySelectorAll('.rb-preset').forEach((b) => {
    b.classList.toggle('on', b.dataset.days === '' ? !on : false);
  });
  const clr = $('#rb-clear');
  if (clr) clr.style.display = on ? '' : 'none';
  // 有筛选时把区间也写进提示，让「当前看的是哪一段」一眼可见
  const hint = box.querySelector('.rb-hint');
  if (hint) {
    const m = S.meta || {};
    const g = `${m.globalFrom || m.dateFrom || '-'} ~ ${m.globalTo || m.dateTo || '-'}`;
    hint.textContent = on
      ? `已限定：${S.range.from || '最早'} ~ ${S.range.to || '最新'}（全库 ${g}）`
      : `可选范围 ${g}`;
  }
}

async function boot() {
  $('#tabs').innerHTML = TABS.map(([k, l]) =>
    `<div class="tab" data-k="${k}">${l}</div>`).join('');
  $('#tabs').onclick = (e) => {
    const t = e.target.closest('.tab');
    if (t && t.dataset.k !== S.tab) nav(t.dataset.k);
  };
  try {
    await loadMetaAndFacets();
    renderRangeBar();
    render();
  } catch (e) {
    $('#view').innerHTML = `<div class="err">无法连接后端：${esc(e.message)}<br>
      请确认服务已启动（<code>make run</code>）。</div>`;
  }
}

boot();
