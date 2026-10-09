// tree.js —— 「压缩树」：把会话的压缩结构画成文件管理器那种树（谁压了谁、体积多大、压掉多少）。
//
// 数据全在现成的两个 GET 里（messages + summaries）——**后端零改动**。
// 口径与后端装配（`state.walk`）逐条对齐，面板不许撒谎：
//   1) 摘要只在**区间左端**被装配用上（指针悬空/区间缺失 ⇒ 那几条照原文发）；
//   2) 能升到**同左端**的最粗祖先（左端不同不许升）。
// 节点两类：message（叶子：原始文本）与 summary（目录：一段被压过的区间，可嵌目录 = 嵌套压缩）。
//
// 体积：rawChars = 子树原文总字数（du 式"子树和"，bar 按它归一）；
// tokens = **发出去时这份的估算大小**：message ≈ 估算（`ceil(字数/1.3)`，与后端
// `state.EstimateTokens` 同一把尺；模型级 tokenizer 覆盖估不到，所以标 ≈），
// summary = 落库的 `tokens`（压缩那一发的产出，本来就是估的）。
const RATIO = 1.3; // 与后端 DefaultTokenizerRatio 同值
const runes = (text) => [...(text ?? "")].length;
const approx = (text) => (runes(text) ? Math.max(1, Math.ceil(runes(text) / RATIO)) : 0);

// buildCompactTree(messages, summaries) → 顶层节点数组（按 idx 顺序的"森林"）。
// messages 需要 {id, idx, role, content}（idx ≤ 0 的合成项会被丢）；summaries 就是
// `GET /sessions/{id}/summaries` 的原样 [{id, parent_summary_id, source_kind,
// begin_message_id, end_message_id, blocks, tokens, dirty, …}]。
export function buildCompactTree(messages, summaries) {
  const msgs = (messages ?? []).filter((m) => m.idx > 0);
  const pos = new Map(msgs.map((m, i) => [m.id, i])); // message id → 数组下标
  const all = summaries ?? [];
  const byId = new Map(all.map((s) => [s.id, s]));
  const span = (s) => {
    if (!s) return null;
    const b = pos.get(s.begin_message_id);
    const e = pos.get(s.end_message_id);
    if (b === undefined || e === undefined || e < b) return null;
    return [b, e];
  };
  const kidsOf = new Map();
  for (const s of all) {
    if (s.parent_summary_id && byId.has(s.parent_summary_id)) {
      if (!kidsOf.has(s.parent_summary_id)) kidsOf.set(s.parent_summary_id, []);
      kidsOf.get(s.parent_summary_id).push(s);
    }
  }

  // used：装配时真正"跳"到的那些摘要 —— **逐字复刻 state.walk**：走到的位置若左端对齐就升到
  // 最粗同左端祖先、跳到它的右端之后（跳过的消息不再回看 —— 别对每条消息独立判断，那会把
  // 已被上层吞掉的孩子（s2）也标成"发它"）。
  const used = new Set();
  for (let i = 0; i < msgs.length; ) {
    const m = msgs[i];
    const s0 = m.summary_id ? byId.get(m.summary_id) : null;
    const sp = span(s0);
    if (s0 && sp && sp[0] === i) {
      let cur = s0;
      for (;;) {
        const p = cur.parent_summary_id ? byId.get(cur.parent_summary_id) : null;
        const ps = span(p);
        if (!p || !ps || ps[0] !== sp[0] || ps[1] < sp[1]) break;
        cur = p;
      }
      used.add(cur.id);
      const cs = span(cur);
      i = (cs ?? sp)[1] + 1;
      continue;
    }
    i++;
  }

  const msgNode = (m) => ({
    kind: "message", id: m.id, idx: m.idx, role: m.role,
    rawChars: runes(m.content), tokens: approx(m.content),
    dirty: false, used: false, children: [],
  });
  const sumNode = (s) => {
    const sp = span(s);
    const ordered = (kidsOf.get(s.id) ?? [])
      .slice()
      .sort((a, b) => ((span(a) ?? [0])[0]) - ((span(b) ?? [0])[0]));
    const kids = [];
    if (ordered.length) {
      for (const c of ordered) kids.push(sumNode(c));
    } else if (sp) {
      // 消息级（或数据缺孩子的兜底）：区间里的消息就是孩子。
      for (const m of msgs.slice(sp[0], sp[1] + 1)) kids.push(msgNode(m));
    }
    return {
      kind: "summary", id: s.id,
      fromIdx: sp ? msgs[sp[0]].idx : null, toIdx: sp ? msgs[sp[1]].idx : null,
      blocks: s.blocks ?? 0, sourceKind: s.source_kind,
      rawChars: kids.reduce((n, k) => n + k.rawChars, 0),
      tokens: s.tokens ?? 0,
      dirty: !!s.dirty, used: used.has(s.id), children: kids,
    };
  };

  const roots = [];
  let i = 0;
  while (i < msgs.length) {
    const m = msgs[i];
    // "根"= 没有（活着的）父的摘要，且左端就是这条消息 —— 它带着整段。
    const heads = all.filter((s) => s.begin_message_id === m.id && (!s.parent_summary_id || !byId.has(s.parent_summary_id)));
    if (heads.length) {
      let end = i;
      for (const s of heads) end = Math.max(end, (span(s) ?? [i])[1]);
      for (const s of heads) roots.push(sumNode(s));
      i = end + 1;
      continue;
    }
    roots.push(msgNode(m));
    i++;
  }
  return roots;
}

// treeTotals：树顶那行小账（原文多少字 / 发出去大约多少 tok）。
// **只数顶层**：每行的 rawChars 是"子树和"，从上往下加起来会把同一段原文数好几遍；
// 顶层节点本身就是对全部消息的一个划分。sent 同理（压过的部分发的是摘要那份，不再发原文）。
export function treeTotals(nodes) {
  let rawChars = 0;
  let sent = 0;
  for (const n of nodes ?? []) {
    rawChars += n.rawChars;
    sent += n.tokens;
  }
  return { rawChars, sent };
}
