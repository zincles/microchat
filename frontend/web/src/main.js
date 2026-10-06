// microchat web 前端 —— 与 TUI/TG 同一条 HTTP 契约（只调 /api/v1，不碰后端内部）。
// 零运行时依赖（fetch 直调；构建只用 vite）。契约见 AGENTS.md API 表。
// 拆分（Vue 式布局，无框架运行时）：api/client.js（call+端点函数）/ components/MessageBubble.js（气泡渲染+纯函数）/ main.js（接线）。
import { createApi } from "./api/client.js";
import { toastVue } from "./toast-vue.js";
import { mountComposer, composerClear, composerSubmit, composerSetStatus } from "./composer-vue.js";
import { mountTopBar, topBar } from "./topbar-vue.js";
import { mountSessionBar, sessionBar } from "./sessionbar-vue.js";
import { mountPayloadBar, payloadBar } from "./payloadbar-vue.js";
import { parseCommand, filterCommands } from "./utils/commands.js";
import { advanceCursor } from "./utils/cursor.js";
import {
  formatStatusLine,
  statusOverBudget,
  groupModelsByProvider,
  deletionPlanSummary,
} from "./utils/format.js";
import { vueMessages, vueStream } from "./messages-vue.js";

const API = localStorage.getItem("mc_api") || "http://127.0.0.1:8787/api/v1";
const TOKEN = localStorage.getItem("mc_token") || "";
const api = createApi({ base: API, token: TOKEN });
// 预演开关：localStorage.mc_preview —— "0" = 关，其余（含没写）= 开。
// 每拍现读：设置里改完即时生效，不用刷新（API/Token 那两格才要刷新）。
function previewOn() { return localStorage.getItem("mc_preview") !== "0"; }

// 发送键：localStorage.mc_send —— "button"（默认，仅按钮）/ "enter"（回车发）/ "shift-enter"。
// 每拍现读（设置里改完下一拍即生效，不用刷新）。
function sendMode() { return localStorage.getItem("mc_send") || "button"; }
// 发送键提示住 Composer.vue 的 hint computed（placeholder 跟着策略走）。
// 主题预设：localStorage.mc_theme —— ""（跟随系统）/ midnight / paper / wine / forest / breeze / breeze-dark。
// TG 客户端里打开（有 window.Telegram.WebApp）⇒ 不用预设，宿主主题说了算。
function applyTheme() {
  if (window.Telegram?.WebApp) { document.documentElement.removeAttribute("data-theme"); return; }
  const t = localStorage.getItem("mc_theme") || "";
  if (t) document.documentElement.setAttribute("data-theme", t);
  else document.documentElement.removeAttribute("data-theme");
}
applyTheme();

// 保留旧名：call() 照旧直调，boot()/send() 语义不变（扩展只加功能）。
async function call(method, path, body) {
  return api.call(method, path, body);
}

const el = {
  messages: document.getElementById("messages-vue"),
};

let sessionID = null;
let sessionInfo = {};
// toast：右上通知（TG 安卓味）—— Vue 版（Toast.vue 挂 #toast-vue，旧盒子空着）。
function toast(text, kind) {
  toastVue(text, kind);
}

function addMessage(who, text, cls, think, messageId) {
  // Vue 列：进 vueMessages（流式那条 _stream 占位由 pollTurn 维护）。
  const m = {
    who,
    role: cls === "user" ? "user" : "assistant",
    content: text,
    reasoning: typeof think === "string" ? think : think?.text,
    reasoningMs: think?.ms,
    messageId,
  };
  vueMessages.value.push(m);
  el.messages.scrollTop = el.messages.scrollHeight;
  return m;
}

// editMessageSave：行内保存 ⇒ PATCH ⇒ 整段重画（世界状态随之现演）。
async function editMessageSave(messageId, kind, value) {
  await api.editMessage(sessionID, messageId, kind === "reasoning" ? { reasoning: value } : { content: value });
  renderHistory(await api.listMessages(sessionID));
}

// editMessageFlow：/edit 命令走它（无行内框 ⇒ 先取旧值当 prompt 初值，取消即收手）。
async function editMessageFlow(messageId, kind) {
  const messages = await api.listMessages(sessionID);
  const target = messages.find((m) => m.id === messageId);
  if (!target) { toast("那条消息已经不在了（多半被删了）：重进会话看看", "error"); return; }
  const old = kind === "reasoning" ? (target.reasoning ?? "") : target.content;
  const next = window.prompt(kind === "reasoning" ? "改思考（空=清掉）" : "改正文", old);
  if (next === null) return;
  await editMessageSave(messageId, kind, next);
}

function renderHistory(messages) {
  vueMessages.value = [];
  vueStream.value = { id: null, text: "" };
  for (const m of messages) {
    vueMessages.value.push({
      who: m.role === "user" ? "你" : "助手",
      role: m.role,
      content: m.content,
      reasoning: m.reasoning,
      reasoningMs: m.reasoning_ms,
      messageId: m.id,
      id: m.id,
    });
  }
  el.messages.scrollTop = el.messages.scrollHeight;
}
async function refreshStatusline(phase, elapsedMs) {
  try {
    const ctx = await api.context(sessionID).catch(() => null);
    composerSetStatus(formatStatusLine({
      ctx,
      phase,
      elapsedMs,
      provider: sessionInfo.provider,
      model: sessionInfo.model,
    }), statusOverBudget(ctx));
  } catch { /* 状态行失败不挡主流程 */ }
}

// renderPayload：右载荷栏（真请求 method/url/headers/体直放）。
// —— Vue 版：PayloadBar.vue 画 aside，经 payloadbar-vue 桥进来。
let lastPayloadWire = null;
function renderPayload(wire) {
  lastPayloadWire = wire ?? null;
  // 真请求直放（没有就放空话）。
  payloadBar.setWire(lastPayloadWire);
}
mountPayloadBar();

// renderSessions：左会话栏（当前高亮；× 关闭 → DELETE 会话；关的是当前 ⇒ 进剩下第一条）。
// —— Vue 版：SessionBar.vue 画 aside，事件经 sessionbar-vue 桥进来。
function renderSessions(sessions) {
  sessionBar.setSessions(sessions, sessionID);
}
mountSessionBar({
  onSelect: (id) => enterSession(id),
  onClose: async (id) => {
    // 关会话 = DELETE 整条（删完后端不代建 —— 关的是当前 ⇒ 进剩下第一条，没有就建一条）。
    await api.closeSession(id).catch((err) => alert(`关闭失败：${err.message}`));
    if (id === sessionID) {
      const rest = await api.listSessions();
      if (rest.length) await enterSession(rest[0].id);
      else await enterSession((await api.createSession({})).id);
    } else refreshSessionsBar();
  },
  onNew: async () => {
    await enterSession((await api.createSession({})).id);
  },
  onOpenSettings: () => window.__mcOpenSettings(),
});

async function refreshSessionsBar() {
  try {
    renderSessions(await api.listSessions());
  } catch { /* 栏失败不挡主流程 */ }
}

async function enterSession(id) {
  sessionID = id;
  const [sessions, messages] = await Promise.all([api.listSessions(), api.listMessages(id)]);
  const found = sessions.find((s) => s.id === id);
  sessionInfo = found ?? {};
  paintSessionHeader();
  // 原生下拉跟当前值走（选项按 provider 分 optgroup；换选项即 PATCH，不再弹 picker）。
  fillModelSelect();
  fillAgentSelect();
  renderSessions(sessions);
  renderHistory(messages);
  refreshStatusline("idle");
}

async function boot() {
  await api.health();
  const sessions = await api.listSessions();
  for (const s of sessions) {
    if (s.messages === 0 && !s.title) await api.deleteSession(s.id).catch(() => {});
  }
  await enterSession((await api.createSession({})).id);
}

// 发送：受理与生成分开（202 回执 + 轮询 status，见 AGENTS.md「一轮生成」）。
async function send(content) {
  const accepted = await api.sendMessage(sessionID, content);
  addMessage("你", content, "user");
  await pollTurn(accepted.turn.message_id);
}

// 重发历史：不落新消息，拿现有历史再跑一轮（尾 user 没回复 / 尾 assistant 想再要一版）。
async function resend() {
  const accepted = await api.resend(sessionID);
  await pollTurn(accepted.turn.message_id);
}

// pollTurn：守着一轮跑完（生成中气泡 + 300ms 轮询 + 游标读增量 + 落库后对账）。
async function pollTurn(turnID) {
  const m = { who: "助手", role: "assistant", _stream: true, stream: true, content: "生成中…" };
  vueMessages.value.push(m);
  vueStream.value = { id: turnID, text: "" };
  let cur = { from: 0, thinkFrom: 0 };
  for (;;) {
    await new Promise((r) => setTimeout(r, 300));
    const st = await api.status(sessionID);
    const slice = await api.turnText(sessionID, cur.from, cur.thinkFrom);
    if (slice.text) vueStream.value.text += slice.text;
    cur = advanceCursor(cur, slice);
    refreshStatusline(st.phase, st.elapsed_ms);
    refreshSessionsBar();
    if (st.phase === "idle" || st.phase === "error") {
      if (st.phase === "error") vueStream.value.text = `失败：${st.error || "未知"}`;
      else if (!vueStream.value.text) {
        const all = await api.listMessages(sessionID);
        const last = all[all.length - 1];
        if (last && last.id === turnID) vueStream.value.text = last.content;
      }
      // 落库对账：整段重画（流式占位换成真消息，含 id 可编辑）。
      renderHistory(await api.listMessages(sessionID));
      refreshStatusline(st.phase, st.elapsed_ms);
      refreshSessionsBar();
      break;
    }
  }
}

// ---- 命令面板（输入 / 开头过滤，Tab/上下+回车）----
// —— Vue 版：Overlays.vue 画浮层三件，经 overlays-vue 桥进来。
import { mountOverlays, overlays } from "./overlays-vue.js";
const showPalette = (items) => overlays.showPalette(items);
const movePalette = (d) => overlays.movePalette(d);
const showPicker = (t, items, action) => overlays.showPicker(t, items, action);
const movePicker = (d) => overlays.movePicker(d);
const hidePicker = () => overlays.hidePicker();
const confirmAsk = (text) => overlays.confirmAsk(text);
mountOverlays({
  onRunCommand: (name) => runCommand(name, []),
  onPick: (action, it) => action(it),
});

async function runCommand(name, args) {
  overlays.hidePalette();
  switch (name) {
    case "new": {
      const s = await api.createSession({});
      await enterSession(s.id);
      break;
    }
    case "resume": {
      const list = await api.listSessions();
      showPicker(
        "选会话（resume）",
        list.map((s) => ({ label: `${s.title || "(无标题)"} [${s.messages}条]`, value: s.id })),
        (it) => enterSession(it.value),
      );
      break;
    }
    case "model": {
      pickModel();
      break;
    }

    case "providers":
    case "agents": {
      const data = name === "providers" ? await api.providers() : await api.agents();
      const rows = name === "providers" ? data : (data.agents ?? data);
      showPicker(name, rows.map((r) => ({ label: r.name ?? r.id, value: r })), () => {});
      break;
    }
    case "compact": {
      const n = args[0] ? Number(args[0]) : 0;
      await api.compact(sessionID, Number.isFinite(n) && n > 0 ? n : undefined);
      toast(`压缩已受理${n ? `（${n} 块）` : ""}`, "ok");
      break;
    }
    case "reroll": {
      await api.rerollEnter(sessionID);
      const st = await api.rerollState(sessionID);
      showPicker(
        "重摇选版",
        (st.items ?? []).map((it, i) => ({ label: `#${i} ${String(it.preview ?? "").slice(0, 40)}`, value: i })),
        async (it) => {
          await api.rerollSwitch(sessionID, it.value);
          renderHistory(await api.listMessages(sessionID));
        },
      );
      break;
    }
    case "switch": {
      await api.rerollSwitch(sessionID, Number(args[0] ?? 0));
      renderHistory(await api.listMessages(sessionID));
      break;
    }
    case "stop": {
      await api.stop(sessionID);
      break;
    }
    case "resend": {
      await resend();
      break;
    }
    case "delete": {
      if (!(await confirmAsk(`删除整个会话 ${sessionID}？`))) break;
      await api.deleteSession(sessionID);
      const s = await api.createSession({});
      await enterSession(s.id);
      break;
    }
    case "cut": {
      const messages = await api.listMessages(sessionID);
      showPicker(
        "cut：选一条（删它及之后）",
        messages.filter((m) => m.idx > 0).map((m) => ({
          label: `#${m.idx} ${m.role} ${m.content.slice(0, 40)}`,
          value: m,
        })),
        async (it) => {
          const plan = await api.deletionPreview(sessionID, it.value.id);
          if (!(await confirmAsk(`${deletionPlanSummary(plan)}，确认删 #${it.value.idx} 及之后？`))) return;
          await api.deleteMessages(sessionID, it.value.id, plan.last_deleted_message_id);
          renderHistory(await api.listMessages(sessionID));
        },
      );
      break;
    }
    case "edit": {
      // /edit <idx> [思考]：改那条消息的正文（带"思考"改思考）；不给 idx ⇒ 弹 picker 选。
      const messages = await api.listMessages(sessionID);
      const wantThink = (args[0] ?? "").toLowerCase().startsWith("思") || (args[1] ?? "").toLowerCase().startsWith("思");
      const n = Number(wantThink && isNaN(Number(args[0])) ? args[1] : args[0]);
      const one = async (m) => editMessageFlow(m.id, wantThink ? "reasoning" : "content");
      if (Number.isInteger(n) && n > 0) {
        const m = messages.find((x) => x.idx === n);
        if (!m) { toast(`没有第 ${n} 条`, "error"); break; }
        await one(m);
        break;
      }
      showPicker(
        `edit：选一条（改${wantThink ? "思考" : "正文"}）`,
        messages.filter((m) => m.idx > 0).map((m) => ({
          label: `#${m.idx} ${m.role} ${(wantThink ? (m.reasoning ?? "") : m.content).slice(0, 40)}`,
          value: m,
        })),
        async (it) => { await one(it.value); },
      );
      break;
    }
    case "editsum": {
      // /editsum：picker 列出本会话摘要（listSummaries），选一条改 text。
      const items = await api.listSummaries(sessionID);
      if (!items.length) { toast("这条会话还没有摘要"); break; }
      showPicker(
        "editsum：选一条摘要（改它的正文）",
        items.map((s) => ({
          label: `${(s.text ?? "").slice(0, 40)}${s.dirty ? "（已过期）" : ""}`,
          value: s,
        })),
        async (it) => {
          const next = window.prompt("改摘要正文", it.value.text ?? "");
          if (next === null || !next.trim()) return;
          await api.editSummary(sessionID, it.value.id, next);
          toast("摘要已改", "ok");
        },
      );
      break;
    }
    default:
      toast(`未知命令 /${name}`);
  }
}

// pickModel / fillModelSelect / applyModelValue：顶部原生 <select> 换模型。
// 选项按 provider 分 optgroup（当前值选中；换选项即 PATCH 两格，不再弹 picker）。
function pickModel() {
  fillModelSelect();
}

function fillModelSelect() {
  api.models().then((models) => {
    const groups = groupModelsByProvider(models).map((g) => ({
      provider: g.provider,
      items: g.items.map((m) => ({ value: `${g.provider}|||${m.upstream_id}`, label: m.name ?? m.upstream_id })),
    }));
    const cur = sessionInfo.provider && sessionInfo.model ? `${sessionInfo.provider}|||${sessionInfo.model}` : "";
    topBar.setModels(groups, cur);
    if (!cur && groups[0]?.items[0]) applyModelValue(groups[0].items[0].value);
  }).catch((err) => toast(`模型列表失败：${err.message}`, "error"));
}

async function applyModelValue(value) {
  const sep = value.indexOf("|||");
  if (sep < 0) return;
  sessionInfo = await api.patchSession(sessionID, {
    provider: value.slice(0, sep),
    model: value.slice(sep + 3),
  });
  paintSessionHeader();
  refreshStatusline("idle");
}

// pickAgent / fillAgentSelect / applyAgentValue：顶部原生 <select> 换 Agent（只改当前会话）。
function pickAgent() {
  fillAgentSelect();
}

function fillAgentSelect() {
  api.agents().then((data) => {
    const list = (data.agents ?? []).map((a) => ({ id: a.id, label: a.name }));
    topBar.setAgents(list, sessionInfo.agent_id);
    if (!sessionInfo.agent_id && list[0]) applyAgentValue(list[0].id);
  }).catch((err) => toast(`Agent 列表失败：${err.message}`, "error"));
}

async function applyAgentValue(id) {
  if (!id) return;
  sessionInfo = await api.patchSession(sessionID, { agent_id: id });
  paintSessionHeader();
  refreshStatusline("idle");
}
// paintSessionHeader：顶部 `会话名 · 模型 · Agent` 三段（改完当场重画，不等轮询）。
function paintSessionHeader() {
  topBar.setSession(sessionInfo);
}
import { mountSettings } from "./settings-vue.js";
const settingsCtl = mountSettings({ onNotify: (t, k) => toast(t, k) });
window.__mcOpenSettings = () => settingsCtl.open();

let previewTimer = 0;
// 底栏自增高：1 行起，最多 6 行（CSS max-height 兜底出滚动）；发完/进会话复位。
// —— Vue 版：Composer.vue 挂 #composer-vue，事件经 composer-vue 桥进来。
mountComposer({
  onInputText(v) {
    if (v.startsWith("/")) {
      const cmd = parseCommand(v);
      showPalette(filterCommands(cmd ? cmd.name : ""));
    } else {
      overlays.hidePalette();
    }
    // 右载荷预演（300ms 防抖）：输入框里的字当待发那句问 (c) —— 只算不写。
    // 空输入 / 命令行不预演（退回 (b)）；400 按"没东西可预演"吞掉，不糊右栏。
    clearTimeout(previewTimer);
    previewTimer = setTimeout(async () => {
      const text = v;
      // 预演总闸：关了就不发包（右栏留旧话，不糊也不闪）。
      if (!previewOn()) return;
      if (!sessionID || !text.trim() || text.startsWith("/")) {
        refreshStatusline("idle").catch(() => {});
        return;
      }
      try {
        const out = await api.outgoingPreview(sessionID, text);
        if (out == null) return; // 空守卫回 null ⇒ 不刷，等下一拍
        // (c) 就是真请求：直接放 raw 档（pretty 已删，不再问 (b) 糊）。
        renderPayload(out);
      } catch { /* 空/错就不刷，等下一拍 */ }
    }, 300);
  },
  onKeydown(e, value) {
    const pickerOpen = overlays.isPickerOpen();
    const palOpen = overlays.isPaletteOpen();
    if (e.key === "ArrowDown" || (e.key === "Tab" && !e.shiftKey)) {
      if (palOpen || pickerOpen) {
        e.preventDefault();
        if (pickerOpen) movePicker(1);
        else { movePalette(1); }
      }
    } else if (e.key === "ArrowUp" || (e.key === "Tab" && e.shiftKey)) {
      if (palOpen || pickerOpen) {
        e.preventDefault();
        if (pickerOpen) movePicker(-1);
        else movePalette(-1);
      }
    } else if (e.key === "Enter" && (palOpen || pickerOpen) && value.startsWith("/")) {
      // picker 有选中动作时优先执行 picker
      if (pickerOpen && overlays.pickerAction()) {
        e.preventDefault();
        const it = overlays.pickerSelected();
        const pa = overlays.pickerAction();
        hidePicker();
        if (it && pa) pa(it);
      } else if (palOpen && overlays.palSelected()) {
        e.preventDefault();
        const cmd = parseCommand(value);
        composerClear();
        runCommand(overlays.palSelected(), cmd ? cmd.args : []).catch((err) =>
          toast(`命令失败：${err.message}`, "error"),
        );
      }
    } else if (e.key === "Escape") {
      overlays.hidePalette();
      hidePicker();
    } else if (e.key === "Enter" && !e.ctrlKey && !e.metaKey) {
      // 发送键策略（命令面板没开时才到这儿）：enter ⇒ 裸回车发；shift-enter ⇒ Shift+回车发；button ⇒ 都不发。
      const mode = sendMode();
      const want = mode === "enter" ? !e.shiftKey : mode === "shift-enter" ? e.shiftKey : false;
      if (want) {
        e.preventDefault();
        composerSubmit();
      }
    }
  },
  onSubmit(text) {
    overlays.hidePalette();
    // 空输入 ⇒ 重发历史（不落新消息，拿现有历史再跑一轮；空会话后端 400 说清）。
    if (!text) {
      resend().catch((err) => toast(`重发失败：${err.message}`, "error"));
      return;
    }
    const cmd = parseCommand(text);
    if (cmd) {
      runCommand(cmd.name, cmd.args).catch((err) => toast(`命令失败：${err.message}`, "error"));
      return;
    }
    send(text).catch((err) => toast(`发送失败：${err.message}`, "error"));
  },
});

// 拖拽调栏宽：grip 按住 ⇒ 跟随横移 ⇒ 松手落 localStorage（mc_bar_left/right，px）。
// 夹紧 160..480（左）/ 240..600（右）；窄屏 grip 藏了 ⇒ 这段自然歇着。
function wireResizers() {
  const pairs = [
    ["grip-left", "sessions-bar", "mc_bar_left", 160, 480],
    ["grip-right", "payload-bar", "mc_bar_right", 240, 600],
  ];
  for (const [gripID, barID, key, min, max] of pairs) {
    const grip = document.getElementById(gripID);
    const bar = document.getElementById(barID);
    if (!grip || !bar) continue;
    const saved = Number(localStorage.getItem(key));
    if (Number.isFinite(saved) && saved >= min && saved <= max) bar.style.width = `${saved}px`;
    grip.addEventListener("pointerdown", (e) => {
      e.preventDefault();
      grip.classList.add("active");
      grip.setPointerCapture(e.pointerId);
      const startX = e.clientX;
      const startW = bar.getBoundingClientRect().width;
      const left = barID === "sessions-bar";
      const move = (ev) => {
        const dx = ev.clientX - startX;
        const w = Math.min(max, Math.max(min, startW + (left ? dx : -dx)));
        bar.style.width = `${w}px`;
      };
      const up = (ev) => {
        grip.classList.remove("active");
        grip.removeEventListener("pointermove", move);
        grip.removeEventListener("pointerup", up);
        const w = Math.round(bar.getBoundingClientRect().width);
        localStorage.setItem(key, String(Math.min(max, Math.max(min, w))));
      };
      grip.addEventListener("pointermove", move);
      grip.addEventListener("pointerup", up);
    });
  }
}
wireResizers();

// 侧栏显隐：顶栏 SVG 按钮切换（Lucide panel-left/right，见 components/icons.js）。
// 状态记 localStorage（mc_bar_left_hidden / mc_bar_right_hidden）；藏栏连相邻 grip 一起藏。
// —— Vue 版：TopBar.vue 画 header，显隐按钮经 topbar-vue 桥进来。
function applySideToggle(barID, gripID, key, hidden) {
  const bar = document.getElementById(barID);
  const grip = document.getElementById(gripID);
  bar?.classList.toggle("collapsed", hidden);
  grip?.classList.toggle("hidden-by-bar", hidden);
}
applySideToggle("sessions-bar", "grip-left", "mc_bar_left_hidden", localStorage.getItem("mc_bar_left_hidden") === "1");
applySideToggle("payload-bar", "grip-right", "mc_bar_right_hidden", localStorage.getItem("mc_bar_right_hidden") === "1");
mountTopBar({
  onToggleLeft: () => {
    const bar = document.getElementById("sessions-bar");
    const hidden = !bar.classList.contains("collapsed");
    localStorage.setItem("mc_bar_left_hidden", hidden ? "1" : "0");
    applySideToggle("sessions-bar", "grip-left", "mc_bar_left_hidden", hidden);
  },
  onToggleRight: () => {
    const bar = document.getElementById("payload-bar");
    const hidden = !bar.classList.contains("collapsed");
    localStorage.setItem("mc_bar_right_hidden", hidden ? "1" : "0");
    applySideToggle("payload-bar", "grip-right", "mc_bar_right_hidden", hidden);
  },
  onSelectModel: (value) => applyModelValue(value).catch((err) => toast(`换模型失败：${err.message}`, "error")),
  onSelectAgent: (id) => applyAgentValue(id).catch((err) => toast(`换 Agent 失败：${err.message}`, "error")),
});

// Vue 气泡的行内保存 ⇒ 同 editMessageSave（PATCH + 整段重画）。
window.addEventListener("mc-edit-save", (e) => {
  const { mid, kind, value } = e.detail ?? {};
  editMessageSave(mid, kind, value).catch((err) => toast(`改消息失败：${err.message}`, "error"));
});

boot().catch((err) => {
  topBar.setConn(`连不上（${err.message}；API 地址存在 localStorage.mc_api）`);
});
