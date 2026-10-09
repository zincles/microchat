<script setup>
// App.vue —— 三栏骨架 + 全部接线（原 main.js + 九个桥，合一）。
// 组件直接挂模板，ref 直调实例方法，不再经 *-vue.js 桥 / mount.js。
import { ref, reactive, computed, onMounted, onUnmounted } from "vue";
import { createApi, advanceCursor } from "./api/client.js";
import { parseCommand, filterCommands } from "./utils/commands.js";
import { uuidv7 } from "./utils/ids.js";
import {
  formatStatusLine,
  statusOverBudget,
  groupModelsByProvider,
  deletionPlanSummary,
} from "./utils/format.js";
import TopBar from "./components/TopBar.vue";
import SessionBar from "./components/SessionBar.vue";
import PayloadBar from "./components/PayloadBar.vue";
import MessageBubble from "./components/MessageBubble.vue";
import Composer from "./components/Composer.vue";
import Toast from "./components/Toast.vue";
import SettingsModal from "./components/SettingsModal.vue";
import SessionSettings from "./components/SessionSettings.vue";
import Overlays from "./components/Overlays.vue";

const API = localStorage.getItem("mc_api") || "http://127.0.0.1:8787/api/v1";
const TOKEN = localStorage.getItem("mc_token") || "";
const api = createApi({ base: API, token: TOKEN });

function previewOn() { return localStorage.getItem("mc_preview") !== "0"; }
function sendMode() { return localStorage.getItem("mc_send") || "enter"; }
function applyTheme() {
  if (window.Telegram?.WebApp) { document.documentElement.removeAttribute("data-theme"); return; }
  // `??` 而不是 `||`：设过空串 = "跟随系统"（空串会被 `||` 吞掉变成默认主题，踩过）。
  const t = localStorage.getItem("mc_theme") ?? "deepseek";
  if (t) document.documentElement.setAttribute("data-theme", t);
  else document.documentElement.removeAttribute("data-theme");
}
applyTheme();

// —— 组件 refs（直调实例方法，替代桥） ——
const toastRef = ref(null);
const topbarRef = ref(null);
const sessionbarRef = ref(null);
const payloadbarRef = ref(null);
const composerRef = ref(null);
const overlaysRef = ref(null);
const toast = (t, k) => toastRef.value?.push(t, k);
// askConfirm：给设置/子编辑器用的全局确认框（与左栏删除同一个口 —— "不可逆的删除先摊开"）。
function askConfirm(text) {
  return overlaysRef.value?.confirmAsk(text) ?? Promise.resolve(false);
}

// —— 会话状态 ——
let sessionID = null;
const sessionInfo = reactive({});
// 草稿态（DeepSeek 网页版行为，2026-10-09 定）：＋/开机/`/new` 只开一张**客户端草稿**，库里没有行；
// 首条发送才 POST /sessions（带上草稿里选好的 provider/model/agent）。
// ⚠ HACK（刻意，全项目唯一的口子）：草稿**铸一枚 UUIDv7** 当将来那会话的 id —— 预演
// （POST /outgoing 临时会话）拿它进请求头，建会话时把同一个值交给 POST /sessions
// ⇒ 预演的那一发给真发的那一发逐字节一致。别扩散（消息/摘要的 id 仍是服务端铸）。
// 要会话的命令（compact/cut/reroll…）在草稿里明确拒绝（NEEDS_SESSION）。
let draft = false;
let draftPrefs = { provider: "", model: "", agent: "" };
let draftId = null;
const vueMessages = ref([]);
const vueStream = ref({ id: null, text: "", reasoning: "" });
const vueDisplayMode = ref("chat");
const settingsOpen = ref(false);
const sessionSettingsId = ref(null);

// —— 消息列 ——
// tmpKey：只给"还没进库"的气泡（乐观用户消息 / 流式占位）当 v-for key —— 不能共用 'stream'（重键）。
let tmpSeq = 0;
function tmpKey() { return `tmp-${++tmpSeq}`; }
function scrollBottom() {
  requestAnimationFrame(() => {
    const el = document.getElementById("messages-vue");
    if (el) el.scrollTop = el.scrollHeight;
  });
}
function addMessage(who, text, cls, think, messageId) {
  vueMessages.value.push({
    id: tmpKey(),
    who,
    role: cls === "user" ? "user" : "assistant",
    content: text,
    reasoning: typeof think === "string" ? think : think?.text,
    reasoningMs: think?.ms,
    messageId,
  });
  scrollBottom();
}
async function editMessageSave(messageId, kind, value) {
  await api.editMessage(sessionID, messageId, kind === "reasoning" ? { reasoning: value } : { content: value });
  renderHistory(await api.listMessages(sessionID));
  refreshStateView();
}
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
  vueStream.value = { id: null, text: "", reasoning: "" };
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
  scrollBottom();
}
const lastMsgId = computed(() => [...vueMessages.value].reverse().find((m) => m.messageId)?.messageId ?? null);
function onBubbleSave(mid, kind, value) {
  editMessageSave(mid, kind, value).catch((err) => toast(`改消息失败：${err.message}`, "error"));
}
async function onBubbleDelete(mid, isLast) {
  if (!mid) return;
  const plan = await api.deletionPreview(sessionID, mid).catch((err) => {
    toast(`删除预览失败：${err.message}`, "error");
    return null;
  });
  if (!plan) return;
  const suffix = isLast
    ? "确认删除这条？"
    : `这不是最后一条：删它会连同之后 ${plan.deleted_message_ids.length - 1} 条一起删，确认？`;
  if (!(await overlaysRef.value.confirmAsk(`${deletionPlanSummary(plan)}，${suffix}`))) return;
  await api.deleteMessages(sessionID, mid, plan.last_deleted_message_id)
    .catch((err) => toast(`删除失败：${err.message}`, "error"));
  renderHistory(await api.listMessages(sessionID));
  refreshStateView();
}
function onBubbleRefresh() {
  resend().catch((err) => toast(`重发失败：${err.message}`, "error"));
}

// —— 顶栏 ——
function paintSessionHeader() {
  topbarRef.value?.setSession(sessionInfo, draft);
  document.title = sessionInfo.title ? `${sessionInfo.title} - MicroChat` : "MicroChat";
}
async function fillModelSelect() {
  api.models().then((models) => {
    const groups = groupModelsByProvider(models).map((g) => ({
      provider: g.provider,
      items: g.items.map((m) => ({ value: `${g.provider}|||${m.upstream_id}`, label: m.name ?? m.upstream_id })),
    }));
    const cur = sessionInfo.provider && sessionInfo.model ? `${sessionInfo.provider}|||${sessionInfo.model}` : "";
    topbarRef.value?.setModels(groups, cur);
  }).catch((err) => toast(`模型列表失败：${err.message}`, "error"));
}
async function applyModelValue(value) {
  const sep = value.indexOf("|||");
  if (sep < 0) return;
  const provider = value.slice(0, sep), model = value.slice(sep + 3);
  if (draft) {
    // 草稿：改动只活在客户端，建会话时随 POST /sessions 一起带上。
    draftPrefs.provider = provider;
    draftPrefs.model = model;
    Object.assign(sessionInfo, { provider, model });
    paintSessionHeader();
    refreshStatusline("idle");
    refreshPreview(); // 面板不许停在旧渠道那一发上
    return;
  }
  Object.assign(sessionInfo, await api.patchSession(sessionID, { provider, model }));
  paintSessionHeader();
  refreshStatusline("idle");
  refreshPreview();
}
function fillAgentSelect() {
  api.agents().then((data) => {
    const list = (data.agents ?? []).map((a) => ({ id: a.id, label: a.name }));
    topbarRef.value?.setAgents(list, sessionInfo.agent_id);
    // 没 agent 时别猜 list[0]：后端认的是 default_agent。
    if (!sessionInfo.agent_id && list.length) applyAgentValue(data.default_agent || list[0].id);
  }).catch((err) => toast(`Agent 列表失败：${err.message}`, "error"));
}
async function applyAgentValue(id) {
  if (!id) return;
  if (draft) {
    draftPrefs.agent = id;
    Object.assign(sessionInfo, { agent_id: id });
    paintSessionHeader();
    refreshDisplayMode();
    refreshStatusline("idle");
    refreshPreview();
    return;
  }
  Object.assign(sessionInfo, await api.patchSession(sessionID, { agent_id: id }));
  paintSessionHeader();
  refreshDisplayMode();
  refreshStatusline("idle");
  refreshPreview();
}
async function refreshDisplayMode() {
  try {
    const data = await api.agents();
    const a = (data.agents ?? []).find((x) => x.id === sessionInfo.agent_id);
    vueDisplayMode.value = a?.display_mode === "roleplay" ? "roleplay" : "chat";
  } catch { /* 拿不到就 chat 兜底 */ }
}
function applySideToggle(barID, hidden) {
  document.getElementById(barID)?.classList.toggle("collapsed", hidden);
}

// —— 会话 ——
function toggleLeft() {
  const bar = document.getElementById("sessions-bar");
  const hidden = !bar?.classList.contains("collapsed");
  localStorage.setItem("mc_bar_left_hidden", hidden ? "1" : "0");
  applySideToggle("sessions-bar", hidden);
}
function toggleRight() {
  const bar = document.getElementById("payload-bar");
  const hidden = !bar?.classList.contains("collapsed");
  localStorage.setItem("mc_bar_right_hidden", hidden ? "1" : "0");
  applySideToggle("payload-bar", hidden);
}
async function closeSession(id) {
  const list = await api.listSessions().catch(() => []);
  const name = list.find((s) => s.id === id)?.title || "新会话";
  if (!(await overlaysRef.value.confirmAsk(`删除会话「${name}」？不可逆。`))) return;
  try {
    await api.closeSession(id);
  } catch (err) {
    toast(`关闭失败：${err.message}`, "error");
    return; // 删失败就别切会话（catch 回调里的 return 不是这个函数的 return）
  }
  if (id === sessionID) {
    closeSettings(); // 被删的是当前会话 ⇒ 接下来要去草稿：设置视图让位
    await startDraft(); // 删掉当前会话 ⇒ 回到草稿（不再预建空会话 —— 那正是被拿掉的旧行为）
  } else refreshSessionsBar();
}
async function startDraft() {
  draft = true;
  sessionID = null;
  draftId = uuidv7(); // 这张草稿将来那会话的 id（预演与建会话都用它）
  const d = await api.getDefaults().catch(() => ({}));
  draftPrefs = { provider: d.provider ?? "", model: d.model ?? "", agent: d.agent ?? "" };
  Object.assign(sessionInfo, {
    title: "", provider: draftPrefs.provider, model: draftPrefs.model,
    agent_id: draftPrefs.agent, system_prompt: "",
  });
  vueMessages.value = [];
  vueStream.value = { id: null, text: "", reasoning: "" };
  paintSessionHeader();
  refreshDisplayMode();
  fillModelSelect();
  fillAgentSelect();
  payloadbarRef.value?.setWire(null);
  payloadbarRef.value?.setTables({});
  refreshStatusline("idle");
  refreshSessionsBar();
  refreshPreview(); // 输入框里若还留着字（＋ 时不清输入框），预演立刻跟上
}
function newSession() {
  closeSettings(); // 侧栏动作 = 把人带回对话区（设置视图让位）
  startDraft().catch((err) => toast(`新建失败：${err.message}`, "error"));
}
function renderSessions(sessions) {
  sessionbarRef.value?.setSessions(sessions, sessionID);
}
async function refreshSessionsBar() {
  try {
    renderSessions(await api.listSessions());
  } catch (err) { toast(`刷会话栏失败：${err.message}`, "error"); }
}
async function enterSession(id) {
  draft = false;
  sessionID = id;
  const [sessions, messages] = await Promise.all([api.listSessions(), api.listMessages(id)]);
  Object.assign(sessionInfo, sessions.find((s) => s.id === id) ?? {});
  paintSessionHeader();
  refreshDisplayMode();
  fillModelSelect();
  fillAgentSelect();
  renderSessions(sessions);
  renderHistory(messages);
  refreshStatusline("idle");
  refreshStateView(id);
  if (previewOn() && messages.length) {
    api.outgoingPreview(id, "").then((w) => payloadbarRef.value?.setWire(w))
      .catch((err) => toast(`预演失败：${err.message}`, "error"));
  } else {
    payloadbarRef.value?.setWire(null); // 别把上一条会话的载荷挂在空会话上
  }
}
async function boot() {
  const health = await api.health();
  connText = `已连接 v${health.version ?? "?"}`;
  // 不再"删空会话 + 预建真会话"：web 走草稿态，库里不产生空行（旧编排的两步一起拿掉）。
  await startDraft();
}

// —— 发送 / 轮询 ——
async function send(content) {
  const sid = sessionID;
  const accepted = await api.sendMessage(sid, content);
  addMessage("你", content, "user");
  await pollTurn(sid, accepted.turn.message_id);
}
async function resend() {
  const sid = sessionID;
  const accepted = await api.resend(sid);
  await pollTurn(sid, accepted.turn.message_id);
}
async function pollTurn(sid, turnID) {
  const m = { who: "助手", role: "assistant", _stream: true, stream: true, content: "", reasoning: "", id: tmpKey() };
  vueMessages.value.push(m);
  vueStream.value = { id: turnID, text: "", reasoning: "" };
  composerRef.value?.setRunning(true);
  let cur = { from: 0, thinkFrom: 0 };
  try {
    for (;;) {
      await new Promise((r) => setTimeout(r, 300));
      if (sessionID !== sid) return; // 会话切走了：这一轮不再往界面上写（库里照旧落）
      const st = await api.status(sid);
      const slice = await api.turnText(sid, cur.from, cur.thinkFrom);
      if (slice.thinking) {
        vueStream.value.reasoning = (vueStream.value.reasoning ?? "") + slice.thinking;
        m.reasoning = vueStream.value.reasoning;
      }
      if (slice.text) vueStream.value.text += slice.text;
      cur = advanceCursor(cur, slice);
      refreshStatusline(st.phase, st.elapsed_ms);
      refreshSessionsBar();
      if (st.phase === "idle" || st.phase === "error") {
        const failed = st.phase === "error";
        renderHistory(await api.listMessages(sid));
        if (failed) {
          // 失败合成一条气泡摆在文末（renderHistory 会清 vueStream，所以必须摆在它后面）+ toast 兜底。
          const errText = `失败：${st.error || "未知"}`;
          vueMessages.value.push({ who: "助手", role: "assistant", content: errText, id: tmpKey() });
          scrollBottom();
          toast(errText, "error");
        }
        refreshStatusline(st.phase, st.elapsed_ms);
        refreshSessionsBar();
        refreshStateView(sid);
        if (previewOn()) {
          api.outgoingPreview(sid, "").then((w) => payloadbarRef.value?.setWire(w))
            .catch((err) => toast(`预演失败：${err.message}`, "error"));
        }
        break;
      }
    }
  } finally {
    composerRef.value?.setRunning(false);
  }
}
let connText = "未连接";
function refreshStatusline(phase, elapsedMs) {
  if (draft || !sessionID) {
    // 草稿：没有会话可问占用 —— 状态行只报"新对话 + 将来会用的模型"。
    const who = [sessionInfo.provider, sessionInfo.model].filter(Boolean).join("/") || "未选模型";
    composerRef.value?.setStatus(`${connText}｜新对话｜${who}｜${phase ?? "idle"}`, false);
    return;
  }
  api.context(sessionID).catch(() => null).then((ctx) => {
    composerRef.value?.setStatus(`${connText}｜` + formatStatusLine({
      ctx, phase, elapsedMs, provider: sessionInfo.provider, model: sessionInfo.model,
    }), statusOverBudget(ctx));
  }).catch((err) => toast(`状态行失败：${err.message}`, "error"));
}
function refreshStateView(id) {
  if (draft || !(id ?? sessionID)) return; // 草稿：没有会话可问状态（右栏保持空话）
  api.sessionState(id ?? sessionID).then((v) => payloadbarRef.value?.setTables(v?.tables)).catch(() => {});
}

// —— 命令 ——
function liftOverlays() {
  // 输入框自身就是 #composer（组件根）——"#composer-vue" 那层包装早没了，别再查它。
  const form = document.querySelector("#composer");
  if (form) overlaysRef.value?.lift(Math.max(0, form.getBoundingClientRect().height - 60));
}
// 输入框高度是异步变的（autosize 在 nextTick 里长高）⇒ 量一次不够，盯住它本身。
let composerRO = null;
// 要真会话的命令：草稿里明确拒绝（主语料不在，做了也是空转）。
const NEEDS_SESSION = new Set(["compact", "reroll", "switch", "stop", "resend", "delete", "cut", "edit", "editsum"]);
async function runCommand(name, args) {
  overlaysRef.value?.hidePalette();
  if (draft && NEEDS_SESSION.has(name)) {
    toast(`这是新对话：先发一句话再用 /${name}`, "error");
    return;
  }
  switch (name) {
    case "new": {
      if (draft) { toast("已经在一张新对话里了"); break; }
      await startDraft();
      break;
    }
    case "resume": {
      const list = await api.listSessions();
      overlaysRef.value?.showPicker(
        "选会话（resume）",
        list.map((s) => ({ label: `${s.title || "(无标题)"} [${s.messages}条]`, value: s.id })),
        (it) => enterSession(it.value),
      );
      break;
    }
    case "model": {
      fillModelSelect();
      break;
    }
    case "providers":
    case "agents": {
      const data = name === "providers" ? await api.providers() : await api.agents();
      const rows = name === "providers" ? data : (data.agents ?? data);
      overlaysRef.value?.showPicker(name, rows.map((r) => ({ label: r.name ?? r.id, value: r })), () => {});
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
      overlaysRef.value?.showPicker(
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
      if (!(await overlaysRef.value.confirmAsk(`删除整个会话 ${sessionID}？`))) break;
      await api.deleteSession(sessionID);
      await startDraft();
      break;
    }
    case "cut": {
      const messages = await api.listMessages(sessionID);
      overlaysRef.value?.showPicker(
        "cut：选一条（删它及之后）",
        messages.filter((m) => m.idx > 0).map((m) => ({
          label: `#${m.idx} ${m.role} ${m.content.slice(0, 40)}`,
          value: m,
        })),
        async (it) => {
          const plan = await api.deletionPreview(sessionID, it.value.id);
          if (!(await overlaysRef.value.confirmAsk(`${deletionPlanSummary(plan)}，确认删 #${it.value.idx} 及之后？`))) return;
          await api.deleteMessages(sessionID, it.value.id, plan.last_deleted_message_id);
          renderHistory(await api.listMessages(sessionID));
          refreshStateView();
        },
      );
      break;
    }
    case "edit": {
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
      overlaysRef.value?.showPicker(
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
      const items = await api.listSummaries(sessionID);
      if (!items.length) { toast("这条会话还没有摘要"); break; }
      overlaysRef.value?.showPicker(
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

// —— 预演（右载荷栏）——
// 两条路同一支笔：会话里走 `POST /sessions/{id}/outgoing`；草稿走 `POST /outgoing`（临时会话，
// 带上草稿铸的 id —— 头上那个会话键就是将来真会话的）。previewSeq 丢弃迟到的那一版。
let previewTimer = 0;
let previewSeq = 0;
async function previewPayload(v) {
  const seq = ++previewSeq;
  try {
    const out = draft
      ? await api.draftOutgoing({
        id: draftId, provider: draftPrefs.provider, model: draftPrefs.model,
        agent_id: draftPrefs.agent, content: v,
      })
      : await api.outgoingPreview(sessionID, v);
    if (seq !== previewSeq) return; // 有更新的一发在路上：旧的丢掉
    payloadbarRef.value?.setWire(out);
  } catch (err) {
    if (seq !== previewSeq) return;
    if (!draft && String(err.message ?? "").includes("400")) return; // 空会话问历史 ⇒ 保持空话
    toast(`预演失败：${err.message}`, "error");
  }
}
// refreshPreview：拿输入框里现有的字**立刻**重跑（改模型/Agent、回草稿之后用）——空字/命令不跑。
function refreshPreview() {
  if (!previewOn()) return;
  const v = composerRef.value?.text ?? "";
  if (!v.trim() || v.startsWith("/")) return;
  if (draft ? !draftId : !sessionID) return;
  previewPayload(v);
}
function onComposerInput(v) {
  liftOverlays();
  if (v.startsWith("/")) {
    const cmd = parseCommand(v);
    overlaysRef.value?.showPalette(filterCommands(cmd ? cmd.name : ""));
  } else {
    overlaysRef.value?.hidePalette();
  }
  clearTimeout(previewTimer);
  previewTimer = setTimeout(() => {
    if (!previewOn()) return;
    if (v.startsWith("/") || (!draft && !sessionID)) {
      refreshStatusline("idle").catch(() => {});
      return;
    }
    if (draft && !v.trim()) {
      payloadbarRef.value?.setWire(null); // 草稿清空 ⇒ 右栏回到占位文案
      return;
    }
    previewPayload(v);
  }, 300);
}
function onComposerKeydown(e, value) {
  const pickerOpen = overlaysRef.value?.isPickerOpen();
  const palOpen = overlaysRef.value?.isPaletteOpen();
  if (e.key === "ArrowDown" || (e.key === "Tab" && !e.shiftKey)) {
    if (palOpen || pickerOpen) {
      e.preventDefault();
      if (pickerOpen) overlaysRef.value?.movePicker(1);
      else overlaysRef.value?.movePalette(1);
    }
  } else if (e.key === "ArrowUp" || (e.key === "Tab" && e.shiftKey)) {
    if (palOpen || pickerOpen) {
      e.preventDefault();
      if (pickerOpen) overlaysRef.value?.movePicker(-1);
      else overlaysRef.value?.movePalette(-1);
    }
  } else if (e.key === "Enter" && (palOpen || pickerOpen) && !e.shiftKey && !e.ctrlKey && !e.metaKey && !e.altKey) {
    // 只有裸回车归浮层（修饰回车一律换行，与发送键口径一致 —— 不查修饰键的话
    // Shift+回车会把高亮命令直接执行掉）。
    if (pickerOpen && overlaysRef.value?.pickerAction()) {
      e.preventDefault();
      const it = overlaysRef.value?.pickerSelected();
      const pa = overlaysRef.value?.pickerAction();
      overlaysRef.value?.hidePicker();
      if (it && pa) pa(it);
    } else if (palOpen && value.startsWith("/") && overlaysRef.value?.palSelected()) {
      e.preventDefault();
      const cmd = parseCommand(value);
      composerRef.value?.clear();
      liftOverlays();
      runCommand(overlaysRef.value.palSelected(), cmd ? cmd.args : []).catch((err) =>
        toast(`命令失败：${err.message}`, "error"),
      );
    }
  } else if (e.key === "Escape") {
    overlaysRef.value?.hidePalette();
    overlaysRef.value?.hidePicker();
  } else if (e.key === "Enter") {
    const mode = sendMode();
    // 三档：enter = 裸回车发送（任何修饰都换行）；shift-enter = Shift+回车发送；button = 仅按钮。
    const want = mode === "enter" ? !(e.shiftKey || e.ctrlKey || e.metaKey || e.altKey)
      : mode === "shift-enter" ? (e.shiftKey && !e.ctrlKey && !e.metaKey)
      : false;
    if (want) {
      e.preventDefault();
      composerRef.value?.submit();
    }
  }
}
// sendFromDraft：草稿的第一句 —— 建会话（带草稿里选的 model/agent）→ 受理 → 轮询。
// 只有"受理"失败才回滚（删掉刚建的空会话、退回草稿）；轮询掉线不回滚（消息已入库）。
async function sendFromDraft(text) {
  const body = {};
  if (draftId) body.id = draftId; // 预演头上那个 id —— 真会话必须落同一个
  if (draftPrefs.provider) body.provider = draftPrefs.provider;
  if (draftPrefs.model) body.model = draftPrefs.model;
  if (draftPrefs.agent) body.agent_id = draftPrefs.agent;
  const s = await api.createSession(body);
  draft = false;
  sessionID = s.id;
  Object.assign(sessionInfo, s);
  paintSessionHeader();
  refreshSessionsBar();
  let accepted;
  try {
    accepted = await api.sendMessage(s.id, text);
  } catch (err) {
    await api.deleteSession(s.id).catch(() => {});
    await startDraft();
    throw err;
  }
  addMessage("你", text, "user");
  await pollTurn(s.id, accepted.turn.message_id);
}
function stopCurrent() {
  if (!sessionID) return; // 草稿没有会话可停
  api.stop(sessionID).catch((err) => toast(`停止失败：${err.message}`, "error"));
}
function onComposerSubmit(text) {
  liftOverlays();
  overlaysRef.value?.hidePalette();
  if (!text) {
    if (draft) { toast("新对话还没有可续写的历史：先写一句"); return; }
    resend().catch((err) => toast(`重发失败：${err.message}`, "error"));
    return;
  }
  const cmd = parseCommand(text);
  if (cmd) {
    runCommand(cmd.name, cmd.args).catch((err) => toast(`命令失败：${err.message}`, "error"));
    return;
  }
  if (draft) {
    sendFromDraft(text).catch((err) => toast(`发送失败：${err.message}`, "error"));
    return;
  }
  send(text).catch((err) => toast(`发送失败：${err.message}`, "error"));
}

// —— 设置视图（与对话同级，2026-10-10） ——
// 窄屏打开设置时**自动收起左栏**（返回时还原）：手机上别让会话条占着 30vh 只当背景。
// 用 matchMedia 判窄屏；只点类不写 localStorage（那是用户手动开关的持久化，别混淆）。
let settingsAutoCollapsed = false;
function openSettings() {
  settingsOpen.value = true;
  if (window.matchMedia?.("(max-width: 700px)")?.matches) {
    const bar = document.getElementById("sessions-bar");
    settingsAutoCollapsed = !!bar && !bar.classList.contains("collapsed");
    if (settingsAutoCollapsed) applySideToggle("sessions-bar", true);
  }
}
function closeSettings() {
  settingsOpen.value = false;
  if (settingsAutoCollapsed) {
    applySideToggle("sessions-bar", false);
    settingsAutoCollapsed = false;
  }
}
// —— 拖拽调宽 ——
onMounted(() => {
  applySideToggle("sessions-bar", localStorage.getItem("mc_bar_left_hidden") === "1");
  applySideToggle("payload-bar", localStorage.getItem("mc_bar_right_hidden") === "1");
  if (typeof ResizeObserver !== "undefined" && !composerRO) {
    composerRO = new ResizeObserver(() => liftOverlays());
    const form = document.querySelector("#composer");
    if (form) composerRO.observe(form);
  }
  boot().catch((err) => {
    connText = "未连接";
    refreshStatusline("idle");
    toast(`启动失败：${err.message}`, "error");
  });
});
onUnmounted(() => {
  composerRO?.disconnect();
  composerRO = null;
});
</script>

<template>
  <div id="app">
    <div id="columns">
      <SessionBar
        ref="sessionbarRef"
        @select="(id) => { closeSettings(); enterSession(id).catch((err) => toast(`进会话失败：${err.message}`, 'error')); }"
        @settings="(id) => { sessionSettingsId = id; }"
        @close="closeSession"
        @new="newSession"
        @open-settings="openSettings"
      />
      <div id="content">
      <section id="chat-col" v-show="!settingsOpen">
    <TopBar
      ref="topbarRef"
      @toggle-left="toggleLeft"
      @toggle-right="toggleRight"
      @select-model="(v) => applyModelValue(v).catch((err) => toast(`换模型失败：${err.message}`, 'error'))"
      @select-agent="(id) => applyAgentValue(id).catch((err) => toast(`换 Agent 失败：${err.message}`, 'error'))"
    />
        <main id="messages-vue">
          <MessageBubble
            v-for="m in vueMessages"
            :key="m.id ?? 'stream'"
            :who="m.who"
            :role="m.role"
            :content="m.stream ? vueStream.text : m.content"
            :reasoning="m.stream ? (vueStream.reasoning ?? m.reasoning) : m.reasoning"
            :reasoning-ms="m.reasoningMs"
            :message-id="m.messageId"
            :is-last="!!m.messageId && m.messageId === lastMsgId"
            :display-mode="vueDisplayMode"
            @save="onBubbleSave"
            @delete="onBubbleDelete"
            @refresh="onBubbleRefresh"
          />
        </main>
    <Composer
      ref="composerRef"
      @input-text="onComposerInput"
      @keydown="onComposerKeydown"
      @submit="onComposerSubmit"
      @stop="stopCurrent"
      @menu="() => overlaysRef?.showPalette(filterCommands(''))"
    />
      </section>
      <PayloadBar v-show="!settingsOpen" ref="payloadbarRef" />
      <SettingsModal
        v-if="settingsOpen"
        :ask="askConfirm"
        :esc-blocked="!!sessionSettingsId"
        @close="closeSettings"
        @toggle-left="toggleLeft"
        @sessions-changed="refreshSessionsBar"
        @goto-session="(id) => { closeSettings(); enterSession(id).catch((err) => toast(`进会话失败：${err.message}`, 'error')); }"
      />
      </div>
    </div>
    <Toast ref="toastRef" />
    <!-- Overlays 挂根层（不在聊天视图里）：设置视图开着时"确认"也要能弹（fixed 定位照旧）。 -->
    <Overlays ref="overlaysRef" @run-command="(n) => runCommand(n, []).catch((err) => toast(`命令失败：${err.message}`, 'error'))" @pick="(a, it) => Promise.resolve(a(it)).catch((err) => toast(`操作失败：${err.message}`, 'error'))" />
    <SessionSettings
      v-if="sessionSettingsId"
      :session-id="sessionSettingsId"
      @close="sessionSettingsId = null"
      @renamed="() => { refreshSessionsBar(); if (sessionSettingsId === sessionID) enterSession(sessionID).catch(() => {}); sessionSettingsId = null; }"
    />
  </div>
</template>
