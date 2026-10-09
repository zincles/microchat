<script setup>
// App.vue —— 三栏骨架 + 全部接线（原 main.js + 九个桥，合一）。
// 组件直接挂模板，ref 直调实例方法，不再经 *-vue.js 桥 / mount.js。
import { ref, reactive, computed, onMounted, nextTick } from "vue";
import { createApi, advanceCursor } from "./api/client.js";
import { parseCommand, filterCommands } from "./utils/commands.js";
import {
  groupModelsByProvider,
  formatStatusLine,
  statusOverBudget,
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
  const t = localStorage.getItem("mc_theme") || "breeze-dark";
  document.documentElement.setAttribute("data-theme", t);
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

// —— 会话状态 ——
let sessionID = null;
const sessionInfo = reactive({});
const vueMessages = ref([]);
const vueStream = ref({ id: null, text: "", reasoning: "" });
const vueDisplayMode = ref("chat");
const settingsOpen = ref(false);
const sessionSettingsId = ref(null);
const lastPayloadWire = ref(null);

// —— 消息列 ——
function scrollBottom() {
  requestAnimationFrame(() => {
    const el = document.getElementById("messages-vue");
    if (el) el.scrollTop = el.scrollHeight;
  });
}
function addMessage(who, text, cls, think, messageId) {
  vueMessages.value.push({
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
}
function onBubbleRefresh() {
  resend().catch((err) => toast(`重发失败：${err.message}`, "error"));
}

// —— 顶栏 ——
function paintSessionHeader() {
  topbarRef.value?.setSession(sessionInfo);
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
    if (!cur && groups[0]?.items[0]) applyModelValue(groups[0].items[0].value);
  }).catch((err) => toast(`模型列表失败：${err.message}`, "error"));
}
async function applyModelValue(value) {
  const sep = value.indexOf("|||");
  if (sep < 0) return;
  Object.assign(sessionInfo, await api.patchSession(sessionID, {
    provider: value.slice(0, sep),
    model: value.slice(sep + 3),
  }));
  paintSessionHeader();
  refreshStatusline("idle");
}
function fillAgentSelect() {
  api.agents().then((data) => {
    const list = (data.agents ?? []).map((a) => ({ id: a.id, label: a.name }));
    topbarRef.value?.setAgents(list, sessionInfo.agent_id);
    if (!sessionInfo.agent_id && list[0]) applyAgentValue(list[0].id);
  }).catch((err) => toast(`Agent 列表失败：${err.message}`, "error"));
}
async function applyAgentValue(id) {
  if (!id) return;
  Object.assign(sessionInfo, await api.patchSession(sessionID, { agent_id: id }));
  paintSessionHeader();
  refreshDisplayMode();
  refreshStatusline("idle");
}
async function refreshDisplayMode() {
  try {
    const data = await api.agents();
    const a = (data.agents ?? []).find((x) => x.id === sessionInfo.agent_id);
    vueDisplayMode.value = a?.display_mode === "roleplay" ? "roleplay" : "chat";
  } catch { /* 拿不到就 chat 兜底 */ }
}
function applySideToggle(barID, gripID, key, hidden) {
  document.getElementById(barID)?.classList.toggle("collapsed", hidden);
  document.getElementById(gripID)?.classList.toggle("hidden-by-bar", hidden);
}

// —— 会话 ——
function toggleLeft() {
  const bar = document.getElementById("sessions-bar");
  const hidden = !bar?.classList.contains("collapsed");
  localStorage.setItem("mc_bar_left_hidden", hidden ? "1" : "0");
  applySideToggle("sessions-bar", "grip-left", "mc_bar_left_hidden", hidden);
}
function toggleRight() {
  const bar = document.getElementById("payload-bar");
  const hidden = !bar?.classList.contains("collapsed");
  localStorage.setItem("mc_bar_right_hidden", hidden ? "1" : "0");
  applySideToggle("payload-bar", "grip-right", "mc_bar_right_hidden", hidden);
}
async function closeSession(id) {
  const list = await api.listSessions().catch(() => []);
  const name = list.find((s) => s.id === id)?.title || "新会话";
  if (!(await overlaysRef.value.confirmAsk(`删除会话「${name}」？不可逆。`))) return;
  await api.closeSession(id).catch((err) => { toast(`关闭失败：${err.message}`, "error"); return; });
  if (id === sessionID) {
    const rest = await api.listSessions();
    if (rest.length) await enterSession(rest[0].id);
    else await enterSession((await api.createSession({})).id);
  } else refreshSessionsBar();
}
async function newSession() {
  try {
    await enterSession((await api.createSession({})).id);
  } catch (err) { toast(`新建失败：${err.message}`, "error"); }
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
  }
}
async function boot() {
  const health = await api.health();
  topbarRef.value?.setConn(`已连接 v${health.version ?? "?"}`);
  const sessions = await api.listSessions();
  for (const s of sessions) {
    if (s.messages === 0 && !s.title) await api.deleteSession(s.id).catch((err) => toast(`清空调会话失败：${err.message}`, "error"));
  }
  await enterSession((await api.createSession({})).id);
}

// —— 发送 / 轮询 ——
async function send(content) {
  const accepted = await api.sendMessage(sessionID, content);
  addMessage("你", content, "user");
  await pollTurn(accepted.turn.message_id);
}
async function resend() {
  const accepted = await api.resend(sessionID);
  await pollTurn(accepted.turn.message_id);
}
async function pollTurn(turnID) {
  const m = { who: "助手", role: "assistant", _stream: true, stream: true, content: "", reasoning: "" };
  vueMessages.value.push(m);
  vueStream.value = { id: turnID, text: "", reasoning: "" };
  composerRef.value?.setRunning(true);
  let cur = { from: 0, thinkFrom: 0 };
  try {
    for (;;) {
      await new Promise((r) => setTimeout(r, 300));
      const st = await api.status(sessionID);
      const slice = await api.turnText(sessionID, cur.from, cur.thinkFrom);
      if (slice.thinking) {
        vueStream.value.reasoning = (vueStream.value.reasoning ?? "") + slice.thinking;
        m.reasoning = vueStream.value.reasoning;
      }
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
        renderHistory(await api.listMessages(sessionID));
        refreshStatusline(st.phase, st.elapsed_ms);
        refreshSessionsBar();
        refreshStateView();
        if (previewOn()) {
          api.outgoingPreview(sessionID, "").then((w) => payloadbarRef.value?.setWire(w))
            .catch((err) => toast(`预演失败：${err.message}`, "error"));
        }
        break;
      }
    }
  } finally {
    composerRef.value?.setRunning(false);
  }
}
function refreshStatusline(phase, elapsedMs) {
  api.context(sessionID).catch(() => null).then((ctx) => {
    composerRef.value?.setStatus(formatStatusLine({
      ctx, phase, elapsedMs, provider: sessionInfo.provider, model: sessionInfo.model,
    }), statusOverBudget(ctx));
  }).catch((err) => toast(`状态行失败：${err.message}`, "error"));
}
function refreshStateView(id) {
  api.sessionState(id ?? sessionID).then((v) => payloadbarRef.value?.setTables(v?.tables)).catch(() => {});
}

// —— 命令 ——
function liftOverlays() {
  const form = document.querySelector("#composer-vue #composer");
  if (form) overlaysRef.value?.lift(Math.max(0, form.getBoundingClientRect().height - 60));
}
async function runCommand(name, args) {
  overlaysRef.value?.hidePalette();
  switch (name) {
    case "new": {
      const s = await api.createSession({});
      await enterSession(s.id);
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
      const s = await api.createSession({});
      await enterSession(s.id);
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

// —— 输入框事件 ——
let previewTimer = 0;
function onComposerInput(v) {
  liftOverlays();
  if (v.startsWith("/")) {
    const cmd = parseCommand(v);
    overlaysRef.value?.showPalette(filterCommands(cmd ? cmd.name : ""));
  } else {
    overlaysRef.value?.hidePalette();
  }
  clearTimeout(previewTimer);
  previewTimer = setTimeout(async () => {
    if (!previewOn()) return;
    if (!sessionID || v.startsWith("/")) {
      refreshStatusline("idle").catch(() => {});
      return;
    }
    try {
      const out = await api.outgoingPreview(sessionID, v);
      payloadbarRef.value?.setWire(out);
      lastPayloadWire.value = out;
    } catch (err) { if (!String(err.message ?? "").includes("400")) toast(`预演失败：${err.message}`, "error"); }
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
  } else if (e.key === "Enter" && (palOpen || pickerOpen) && value.startsWith("/")) {
    if (pickerOpen && overlaysRef.value?.pickerAction()) {
      e.preventDefault();
      const it = overlaysRef.value?.pickerSelected();
      const pa = overlaysRef.value?.pickerAction();
      overlaysRef.value?.hidePicker();
      if (it && pa) pa(it);
    } else if (palOpen && overlaysRef.value?.palSelected()) {
      e.preventDefault();
      const cmd = parseCommand(value);
      composerRef.value?.clear();
      runCommand(overlaysRef.value.palSelected(), cmd ? cmd.args : []).catch((err) =>
        toast(`命令失败：${err.message}`, "error"),
      );
    }
  } else if (e.key === "Escape") {
    overlaysRef.value?.hidePalette();
    overlaysRef.value?.hidePicker();
  } else if (e.key === "Enter" && !e.ctrlKey && !e.metaKey) {
    const mode = sendMode();
    const want = mode === "enter" ? !e.shiftKey : mode === "shift-enter" ? e.shiftKey : false;
    if (want) {
      e.preventDefault();
      composerRef.value?.submit();
    }
  }
}
function onComposerSubmit(text) {
  overlaysRef.value?.hidePalette();
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
}

// —— 拖拽调宽 ——
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
      const up = () => {
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

onMounted(() => {
  wireResizers();
  applySideToggle("sessions-bar", "grip-left", "mc_bar_left_hidden", localStorage.getItem("mc_bar_left_hidden") === "1");
  applySideToggle("payload-bar", "grip-right", "mc_bar_right_hidden", localStorage.getItem("mc_bar_right_hidden") === "1");
  boot().catch((err) => {
    topbarRef.value?.setConn(`连不上（${err.message}；API 地址存在 localStorage.mc_api）`);
    toast(`启动失败：${err.message}`, "error");
  });
});
</script>

<template>
  <div id="app">
    <TopBar
      ref="topbarRef"
      @toggle-left="toggleLeft"
      @toggle-right="toggleRight"
      @select-model="(v) => applyModelValue(v).catch((err) => toast(`换模型失败：${err.message}`, 'error'))"
      @select-agent="(id) => applyAgentValue(id).catch((err) => toast(`换 Agent 失败：${err.message}`, 'error'))"
    />
    <div id="columns">
      <SessionBar
        ref="sessionbarRef"
        @select="(id) => enterSession(id).catch((err) => toast(`进会话失败：${err.message}`, 'error'))"
        @settings="(id) => { sessionSettingsId = id; }"
        @close="closeSession"
        @new="newSession"
        @open-settings="() => { settingsOpen = true; }"
      />
      <div id="grip-left" class="grip" title="拖拽调左栏宽"></div>
      <section id="chat-col">
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
        <Overlays ref="overlaysRef" @run-command="(n) => runCommand(n, []).catch((err) => toast(`命令失败：${err.message}`, 'error'))" @pick="(a, it) => Promise.resolve(a(it)).catch((err) => toast(`操作失败：${err.message}`, 'error'))" />
        <Composer
          ref="composerRef"
          @input-text="onComposerInput"
          @keydown="onComposerKeydown"
          @submit="onComposerSubmit"
          @stop="() => api.stop(sessionID).catch((err) => toast(`停止失败：${err.message}`, 'error'))"
          @menu="() => overlaysRef?.showPalette(filterCommands(''))"
        />
      </section>
      <div id="grip-right" class="grip" title="拖拽调右栏宽"></div>
      <PayloadBar ref="payloadbarRef" />
    </div>
    <Toast ref="toastRef" />
    <SettingsModal
      v-if="settingsOpen"
      @close="settingsOpen = false"
      @notify="toast"
      @sessions-changed="refreshSessionsBar"
      @goto-session="(id) => { settingsOpen = false; enterSession(id).catch((err) => toast(`进会话失败：${err.message}`, 'error')); }"
    />
    <SessionSettings
      v-if="sessionSettingsId"
      :session-id="sessionSettingsId"
      @close="sessionSettingsId = null"
      @notify="toast"
      @renamed="() => { refreshSessionsBar(); if (sessionSettingsId === sessionID) enterSession(sessionID).catch(() => {}); sessionSettingsId = null; }"
    />
  </div>
</template>
