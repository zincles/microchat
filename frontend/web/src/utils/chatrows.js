// chatrows.js —— 消息列的**窗口与渲染行**（分页 + 摘要折叠 + 编号）。
//
// 模型：消息列**不是**全量原文 —— 只挂一个**窗口**（[windowFrom..尾]，单位是"段"）：
//   - 段 = 一条消息（未压缩），或一张**顶层摘要卡**（没爹的那层；金字塔只露最外）；
//   - 摘要段**只带摘要自己**：被它盖住的原文一个字节都不取 —— 用户按下"查看未压缩文本"
//     才去现拉（这一层是"浏览器不被撑炸"的关键）；
//   - 更老的历史在窗口外：`加载更多历史` 往回一页（一页 N 段 —— 见 App 的 WINDOW_SEGMENTS）。
// 本文件只管**算**：该拉哪些区间（runsToFetch）、该画哪些行（buildChatRows）；
// 取数、滚动锚、缓存都在 App / SummaryCard。
//
// 段的两端用 **begin_idx/end_idx（消息序号）** —— 接口现发（store 派生），
// 前端手里没有全文也能切段（这正是分页的意义）。
const segStart = (seg) => (seg.kind === "summary" ? seg.b : seg.idx);
const segEnd = (seg) => (seg.kind === "summary" ? seg.e : seg.idx);

// computeSegments(total, summaries)：把整条会话切成有序段（旧→新）。
// 顶层摘要要求端点序号齐且落界内（悬空/缺 ⇒ 不折，那段照旧按消息段走）；后端给的总数快照
// 可能偏小 ⇒ span 尾巴按 total 夹住（宁短勿越界）。
export function computeSegments(total, summaries) {
  const tops = (summaries ?? [])
    .filter((s) => !s.parent_summary_id)
    .filter((s) => s.begin_idx > 0 && s.end_idx > 0 && s.end_idx >= s.begin_idx)
    .map((s) => ({ s, b: s.begin_idx, e: s.end_idx }))
    .sort((x, y) => x.b - y.b || y.e - x.e); // 同起点先取长的（后面 find 第一条）
  const segments = [];
  let covered = 0;
  for (const t of tops) {
    if (t.b <= covered || t.b > total) continue; // 重叠/越界：跳过（数据异常时先来的赢）
    for (let i = covered + 1; i < t.b; i++) segments.push({ kind: "msg", idx: i });
    const e = Math.min(t.e, total);
    segments.push({ kind: "summary", id: t.s.id, b: t.b, e, summary: t.s });
    covered = e;
  }
  for (let i = covered + 1; i <= total; i++) segments.push({ kind: "msg", idx: i });
  return segments;
}

// windowStart：从尾部起取 n 段 ⇒ 窗口起点（idx）；段不够 = 1。
export function windowStart(segments, n) {
  if (!segments.length) return 1;
  return segStart(segments[Math.max(0, segments.length - n)]);
}

// prevWindowStart：比 from 更老的一页（n 段）的起点；已在最老 ⇒ 原样返回。
export function prevWindowStart(segments, from, n) {
  const at = segments.findIndex((seg) => segStart(seg) === from);
  if (at <= 0) return from;
  return segStart(segments[Math.max(0, at - n)]);
}

// alignWindowFrom：结构变了（压缩把几段并成一张卡、删除砍了尾巴）后，把窗口起点对回段边界：
// 落在某段里 ⇒ 提到该段起点；越过末尾（窗口不存在了）⇒ 回到最后一页。
export function alignWindowFrom(segments, from, n) {
  if (!segments.length) return 1;
  if (from <= segStart(segments[0])) return segStart(segments[0]);
  for (const seg of segments) {
    if (from >= segStart(seg) && from <= segEnd(seg)) return segStart(seg);
  }
  return windowStart(segments, n);
}

// olderCount：窗口之外还有几段（0 ⇒ 已到最老，不挂"加载更多"）。
export function olderCount(segments, from) {
  let n = 0;
  for (const seg of segments) if (segStart(seg) < from) n++;
  return n;
}

// runsToFetch：窗口 [from..to] 里**消息段**的 idx 连续区间 —— 摘要段整段跳过（一个字节都不取）。
// to = Infinity ⇒ 末段开区间（尾巴用 `?from_idx=` 无终点拉，吸收总数快照的滞后）。
export function runsToFetch(segments, from, to) {
  const runs = [];
  for (const seg of segments) {
    const s = segStart(seg);
    if (s < from) continue;
    if (to !== Infinity && s > to) break;
    if (seg.kind === "summary") continue;
    const last = runs[runs.length - 1];
    if (last && last[1] + 1 === s) last[1] = s;
    else runs.push([s, s]);
  }
  if (to === Infinity && runs.length) {
    const lastSeg = [...segments].reverse().find((seg) => segStart(seg) >= from);
    if (lastSeg && lastSeg.kind === "msg") runs[runs.length - 1][1] = Infinity;
  }
  return runs;
}

// buildChatRows({segments, windowFrom, messages, summaryMsgs})：渲染行（旧→新）。
//   messages：已取回的消息（展示形状：who/idx/content/…；含窗口外的与库外的尾巴，都按需过滤）；
//   summaryMsgs：Map<摘要id, {state:"loading"|"ready", rows}> —— 展开才有的那份。
// 编号：真消息用它的 idx；库外的（乐观句、生成中那一发）按后缀顺延；明确无号（noIdx）给 null。
export function buildChatRows({ segments, windowFrom, messages, summaryMsgs }) {
  const list = messages ?? [];
  const byIdx = new Map();
  for (const m of list) if (m.idx > 0) byIdx.set(m.idx, m);

  const rows = [];
  const covered = new Set();
  let last = Math.max(0, windowFrom - 1);
  for (const seg of segments ?? []) {
    if (segEnd(seg) < windowFrom) continue; // 窗口外的老段
    if (seg.kind === "summary") {
      for (let i = seg.b; i <= seg.e; i++) covered.add(i);
      const entry = summaryMsgs?.get(seg.id);
      rows.push({
        kind: "summary", key: `sum-${seg.id}`, summary: seg.summary,
        fromIdx: seg.b, toIdx: seg.e,
        rows: entry?.rows ?? null,
        loading: entry?.state === "loading",
      });
    } else {
      covered.add(seg.idx);
      const m = byIdx.get(seg.idx);
      if (m) rows.push({ kind: "msg", key: m.id ?? `m${seg.idx}`, msg: m, displayIdx: seg.idx });
    }
    last = Math.max(last, segEnd(seg));
  }

  // 尾巴 extras：布局还不认识的消息（总数快照滞后时的新消息）、乐观句、生成中那一发。
  const extras = [];
  for (const m of list) {
    if (m.idx > 0) {
      if (m.idx < windowFrom || covered.has(m.idx)) continue;
      extras.push(m);
    } else {
      extras.push(m);
    }
  }
  extras.sort((a, b) => (a.idx ?? Infinity) - (b.idx ?? Infinity)); // 无 idx 的排最后（稳定序保持原顺序）
  for (const m of extras) {
    if (m.noIdx) rows.push({ kind: "msg", key: m.id ?? "err", msg: m, displayIdx: null });
    else if (m.idx > 0) { last = Math.max(last, m.idx); rows.push({ kind: "msg", key: m.id ?? `m${m.idx}`, msg: m, displayIdx: m.idx }); }
    else rows.push({ kind: "msg", key: m.id ?? "tmp", msg: m, displayIdx: ++last });
  }
  return rows;
}
