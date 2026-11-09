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
export interface ChatSummary {
  id: string;
  parent_summary_id?: string;
  begin_idx?: number;
  end_idx?: number;
  [key: string]: unknown;
}

export type ChatSegment =
  | { kind: "msg"; idx: number }
  | { kind: "summary"; id: string; b: number; e: number; summary: ChatSummary };

export interface ChatMessage {
  id?: string;
  messageId?: string;
  idx?: number | null;
  role?: string;
  kind?: "chat" | "tool_run";
  status?: "done" | "canceled" | "failed" | "interrupted";
  tools_snapshot?: string[];
  tools?: {
    id?: string;
    source?: string;
    tool_name?: string;
    item_id?: string;
    queries?: string[];
    arguments?: string;
    result?: string;
    sources?: { type?: string; url: string; title?: string }[];
    status?: string;
  }[];
  who?: string;
  content?: string;
  reasoning?: string;
  reasoningMs?: number | null;
  usage?: Record<string, unknown> | null;
  stream?: boolean;
  noIdx?: boolean;
  tool?: { kind: string; state: string } | null;
  sources?: { type?: string; url: string; title?: string }[];
  [key: string]: unknown;
}

// toolSources：把一条消息的多个工具来源摊平并按 URL 去重（多次搜索常返回同一批站点）。
export function toolSources(tools?: {
  sources?: { type?: string; url: string; title?: string }[];
}[] | null): { type?: string; url: string; title?: string }[] {
  const seen = new Set<string>();
  const out: { type?: string; url: string; title?: string }[] = [];
  for (const tool of tools || []) {
    for (const source of tool.sources || []) {
      if (!source?.url || seen.has(source.url)) continue;
      seen.add(source.url);
      out.push(source);
    }
  }
  return out;
}

export interface ChatSummaryEntry {
  state: "loading" | "ready";
  rows: ChatRow[] | null;
}

export interface ChatRowMessage {
  kind: "msg";
  key: string;
  msg: ChatMessage;
  displayIdx: number | null;
}

export interface ChatRowSummary {
  kind: "summary";
  key: string;
  summary: ChatSummary;
  fromIdx: number;
  toIdx: number;
  rows: ChatRow[] | null;
  loading: boolean;
}

export type ChatRow = ChatRowMessage | ChatRowSummary;

const segStart = (seg: ChatSegment): number => (seg.kind === "summary" ? seg.b : seg.idx);
const segEnd = (seg: ChatSegment): number => (seg.kind === "summary" ? seg.e : seg.idx);

// computeSegments(total, summaries)：把整条会话切成有序段（旧→新）。
// 顶层摘要要求端点序号齐且落界内（悬空/缺 ⇒ 不折，那段照旧按消息段走）；后端给的总数快照
// 可能偏小 ⇒ span 尾巴按 total 夹住（宁短勿越界）。
export function computeSegments(total: number, summaries?: ChatSummary[] | null): ChatSegment[] {
  const tops = (summaries ?? [])
    .filter((s) => !s.parent_summary_id)
    .filter((s) => (s.begin_idx ?? 0) > 0 && (s.end_idx ?? 0) > 0 && (s.end_idx ?? 0) >= (s.begin_idx ?? 0))
    .map((s) => ({ s, b: s.begin_idx as number, e: s.end_idx as number }))
    .sort((x, y) => x.b - y.b || y.e - x.e); // 同起点先取长的（后面 find 第一条）
  const segments: ChatSegment[] = [];
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
export function windowStart(segments: ChatSegment[], n: number): number {
  if (!segments.length) return 1;
  return segStart(segments[Math.max(0, segments.length - n)]);
}

// prevWindowStart：比 from 更老的一页（n 段）的起点；已在最老 ⇒ 原样返回。
export function prevWindowStart(segments: ChatSegment[], from: number, n: number): number {
  const at = segments.findIndex((seg) => segStart(seg) === from);
  if (at <= 0) return from;
  return segStart(segments[Math.max(0, at - n)]);
}

// alignWindowFrom：结构变了（压缩把几段并成一张卡、删除砍了尾巴）后，把窗口起点对回段边界：
// 落在某段里 ⇒ 提到该段起点；越过末尾（窗口不存在了）⇒ 回到最后一页。
export function alignWindowFrom(segments: ChatSegment[], from: number, n: number): number {
  if (!segments.length) return 1;
  if (from <= segStart(segments[0])) return segStart(segments[0]);
  for (const seg of segments) {
    if (from >= segStart(seg) && from <= segEnd(seg)) return segStart(seg);
  }
  return windowStart(segments, n);
}

// olderCount：窗口之外还有几段（0 ⇒ 已到最老，不挂"加载更多"）。
export function olderCount(segments: ChatSegment[], from: number): number {
  let n = 0;
  for (const seg of segments) if (segStart(seg) < from) n++;
  return n;
}

// runsToFetch：窗口 [from..to] 里**消息段**的 idx 连续区间 —— 摘要段整段跳过（一个字节都不取）。
// to = Infinity ⇒ 末段开区间（尾巴用 `?from_idx=` 无终点拉，吸收总数快照的滞后）。
export function runsToFetch(segments: ChatSegment[], from: number, to: number): Array<[number, number]> {
  const runs: Array<[number, number]> = [];
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

// seededMissingRuns：窗口要的那几段里，**第一拍带回来的尾部消息还没覆盖**的部分。
//
// 高延迟下的省 RTT（2026-11-05）：取数时把"最后 windowLimit 条消息"与摘要、总数**并行**
// 取回 —— 没有摘要横跨窗口时，那几条正好就是窗口要的全部 ⇒ 第二拍不用发。
// 只有窗口比"最后 N 条"更老（摘要把窗口切成很大的段、或加载更多）才需要补一发。
// seeded 必须按 idx 升序；b === Infinity 表示"到末尾"，而 seeded 本来就盖住了末尾。
export function seededMissingRuns(
  runs: Array<[number, number]>,
  seeded?: Array<{ idx: number }> | null,
): Array<[number, number]> {
  if (!seeded?.length) return runs;
  const first = seeded[0].idx;
  const last = seeded[seeded.length - 1].idx;
  const missing: Array<[number, number]> = [];
  for (const [a, b] of runs) {
    const end = Math.min(b === Infinity ? last : b, first - 1);
    if (a <= end) missing.push([a, end]);
  }
  return missing;
}


// buildChatRows({segments, windowFrom, messages, summaryMsgs})：渲染行（旧→新）。
//   messages：已取回的消息（展示形状：who/idx/content/…；含窗口外的与库外的尾巴，都按需过滤）；
//   summaryMsgs：Map<摘要id, {state:"loading"|"ready", rows}> —— 展开才有的那份。
// 编号：真消息用它的 idx；库外的（乐观句、生成中那一发）按后缀顺延；明确无号（noIdx）给 null。
export function buildChatRows({
  segments,
  windowFrom,
  messages,
  summaryMsgs,
}: {
  segments?: ChatSegment[] | null;
  windowFrom: number;
  messages?: ChatMessage[] | null;
  summaryMsgs?: Map<string, ChatSummaryEntry> | null;
}): ChatRow[] {
  const list = messages ?? [];
  const byIdx = new Map<number, ChatMessage>();
  for (const m of list) if (typeof m.idx === "number" && m.idx > 0) byIdx.set(m.idx, m);

  const rows: ChatRow[] = [];
  const covered = new Set<number>();
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
  const extras: ChatMessage[] = [];
  for (const m of list) {
    const idx = typeof m.idx === "number" ? m.idx : 0;
    if (idx > 0) {
      if (idx < windowFrom || covered.has(idx)) continue;
      extras.push(m);
    } else {
      extras.push(m);
    }
  }
  extras.sort((a, b) => (a.idx ?? Infinity) - (b.idx ?? Infinity)); // 无 idx 的排最后（稳定序保持原顺序）
  for (const m of extras) {
    if (m.noIdx) rows.push({ kind: "msg", key: m.id ?? "err", msg: m, displayIdx: null });
    else if (typeof m.idx === "number" && m.idx > 0) {
      last = Math.max(last, m.idx);
      rows.push({ kind: "msg", key: m.id ?? `m${m.idx}`, msg: m, displayIdx: m.idx });
    }
    else rows.push({ kind: "msg", key: m.id ?? "tmp", msg: m, displayIdx: ++last });
  }
  return rows;
}

// —— 气泡正文的三个来源（2026-10-17 用户定）————————————————————————
//   · 一轮的合成流（还没落库那条）
//   · **正在摇的那条**：摇的过程中气泡立刻切走（空白起步，随候选流长字）—— 旧正文一直挂到第一帧
//     才被换掉"有点丑"（用户原话）。空白**不等于**丢内容：旧版就是候选 #1（后端的 [原文, 新候选]），
//     切回去、或者摇失败，都照旧回来。
//   · 库内正文。
export interface RerollStateLike {
  active?: boolean;
  running?: boolean;
  target_message_id?: string;
}

export function rerollTargetLive(
  msg: ChatMessage | null | undefined,
  {
    startingId = null,
    state = null,
  }: { startingId?: string | null; state?: RerollStateLike | null } = {},
): boolean {
  if (!msg?.messageId) return false;
  if (startingId === msg.messageId) return true; // 刚落手、状态还没回来那一小段
  return !!state?.active && !!state?.running && state.target_message_id === msg.messageId;
}

// latestPromptFor：最新一条 assistant 对应的那条 user（A-B-C-D-E-F ⇒ E）。
//
// 只认"最新 assistant 之前最近的一条 user"：它才是这条回复的 prompt。没有 assistant、
// 或它前面没有 user（例如开场白）⇒ null。消息 id 用 messageId 优先（toRow 形状）/ id（接口原形）。
export function latestPromptFor(messages?: ChatMessage[] | null): string | null {
  const list = messages ?? [];
  const idOf = (m?: ChatMessage | null): string | null => m?.messageId ?? m?.id ?? null;
  let lastAI = -1;
  for (let i = list.length - 1; i >= 0; i--) {
    if (list[i]?.role === "assistant" && idOf(list[i])) { lastAI = i; break; }
  }
  if (lastAI < 0) return null;
  for (let i = lastAI - 1; i >= 0; i--) {
    if (list[i]?.role === "user" && idOf(list[i])) return idOf(list[i]);
  }
  return null;
}

// editRerollTarget：编辑这条消息后，能不能"保存并重摇"，以及重摇谁。
//
// 重摇只认尾条 assistant（后端同一条闸），所以只有两种编辑位置合法：
//   · 尾条 assistant 自己；
//   · 紧挨着它前面的那条 user（例如 user2 后面只剩 AI2）。
// user1 后面还跟着多条消息 ⇒ 不行；没有尾条 assistant ⇒ 不行。
// 返回目标消息 id（string）或 null；纯函数，App 只读它。
export function editRerollTarget(messages?: ChatMessage[] | null, messageId?: string | null): string | null {
  const list = messages ?? [];
  const idOf = (m?: ChatMessage | null): string | null => m?.messageId ?? m?.id ?? null;
  const index = list.findIndex((m) => idOf(m) === messageId);
  if (index < 0) return null;
  const message = list[index];
  const next = list[index + 1] ?? null;
  if (message?.role === "assistant" && index === list.length - 1) return idOf(message);
  if (message?.role === "user" && next?.role === "assistant" && index + 1 === list.length - 1) return idOf(next);
  return null;
}

// bubbleText：这条消息在 `field`（正文 content / 思考 reasoning）上该显示什么。
// 顺序：合成流 > 正在摇的那条 > 库内字段。**正在摇时不给回落**（就是要空白起步）。
export function bubbleText(
  msg: ChatMessage | null | undefined,
  {
    field = "content",
    live = false,
    streamText = null,
    previewText = null,
  }: {
    field?: "content" | "reasoning";
    live?: boolean;
    streamText?: string | null;
    previewText?: string | null;
  } = {},
): string {
  if (msg?.stream) return streamText ?? "";
  if (live) return previewText ?? "";
  return msg?.[field] ?? "";
}
