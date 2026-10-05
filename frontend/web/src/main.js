// microchat web 前端 —— 与 TUI/TG 同一条 HTTP 契约（只调 /api/v1，不碰后端内部）。
// 零运行时依赖（fetch 直调；构建只用 vite）。契约见 AGENTS.md API 表。
// 拆分（Vue 式布局，无框架运行时）：api/client.js（call+端点函数）/ components/MessageBubble.js（气泡渲染+纯函数）/ main.js（接线）。
import { createApi } from "./api/client.js";
import { parseCommand, filterCommands } from "./utils/commands.js";
import { advanceCursor } from "./utils/cursor.js";
import {
  renderMessage,
  formatStatusLine,
  statusOverBudget,
  groupModelsByProvider,
  SETTINGS_TABS,
  switchSettingsTab,
  snapshotSettings,
  formatRoutesOutcome,
  ABILITY_IDS,
  buildAbilitiesPatch,
} from "./components/MessageBubble.js";

const API = localStorage.getItem("mc_api") || "http://127.0.0.1:8787/api/v1";
const TOKEN = localStorage.getItem("mc_token") || "";
const api = createApi({ base: API, token: TOKEN });
// 预演开关：localStorage.mc_preview —— "0" = 关，其余（含没写）= 开。
// 每拍现读：设置里改完即时生效，不用刷新（API/Token 那两格才要刷新）。
function previewOn() { return localStorage.getItem("mc_preview") !== "0"; }

// 主题预设：localStorage.mc_theme —— ""（跟随系统）/ midnight / paper / wine / forest。
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
  conn: document.getElementById("conn"),
  session: document.getElementById("session"),
  messages: document.getElementById("messages"),
  form: document.getElementById("composer"),
  input: document.getElementById("input"),
  statusline: document.getElementById("statusline"),
  outgoingRaw: document.getElementById("outgoing-raw"),
  sessionList: document.getElementById("session-list"),
  sessionNew: document.getElementById("session-new"),
  palette: document.getElementById("palette"),
  paletteList: document.getElementById("palette-list"),
  picker: document.getElementById("picker"),
  pickerTitle: document.getElementById("picker-title"),
  pickerList: document.getElementById("picker-list"),
  confirmBox: document.getElementById("confirm-box"),
  confirmText: document.getElementById("confirm-text"),
  confirmYes: document.getElementById("confirm-yes"),
  confirmNo: document.getElementById("confirm-no"),
};

let sessionID = null;
let sessionInfo = {};
let palSel = 0;
let palItems = [];
let pickerItems = [];
let pickerSel = 0;
let pickerAction = null;

function addMessage(who, text, cls, think, messageId) {
  const node = renderMessage(document, {
    who,
    role: cls === "user" ? "user" : "assistant",
    content: text,
    reasoning: typeof think === "string" ? think : think?.text,
    reasoningMs: think?.ms,
    messageId,
    onEdit: messageId ? (mid, kind, value) => editMessageSave(mid, kind, value).catch((err) =>
      addMessage("系统", `改消息失败：${err.message}`, "assistant")) : undefined,
  });
  el.messages.appendChild(node);
  el.messages.scrollTop = el.messages.scrollHeight;
  return node;
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
  if (!target) { addMessage("系统", "那条消息已经不在了（多半被删了）：重进会话看看", "assistant"); return; }
  const old = kind === "reasoning" ? (target.reasoning ?? "") : target.content;
  const next = window.prompt(kind === "reasoning" ? "改思考（空=清掉）" : "改正文", old);
  if (next === null) return;
  await editMessageSave(messageId, kind, next);
}

function renderHistory(messages) {
  el.messages.innerHTML = "";
  for (const m of messages) {
    addMessage(
      m.role === "user" ? "你" : "助手",
      m.content,
      m.role,
      m.reasoning ? { text: m.reasoning, ms: m.reasoning_ms } : null,
      m.id,
    );
  }
}

async function refreshStatusline(phase, elapsedMs) {
  try {
    const ctx = await api.context(sessionID).catch(() => null);
    el.statusline.textContent = formatStatusLine({
      ctx,
      phase,
      elapsedMs,
      provider: sessionInfo.provider,
      model: sessionInfo.model,
    });
    el.statusline.classList.toggle("over", statusOverBudget(ctx));
  } catch { /* 状态行失败不挡主流程 */ }
}

// renderPayload：右载荷栏（真请求 method/url/headers/体直放）。
let lastPayloadWire = null;
function renderPayload(wire) {
  lastPayloadWire = wire ?? null;
  // 真请求直放（没有就放空话）。
  el.outgoingRaw.textContent =
    lastPayloadWire ? JSON.stringify(lastPayloadWire, null, 2) : "(还没有预演：输入框打字即问 (c))";
}

// renderSessions：左会话栏（当前高亮；× 关闭 → DELETE 会话；关的是当前 ⇒ 进剩下第一条）。
// 空态"还没有会话"（new 按钮已在 bar-head，空态只提示）。
function renderSessions(sessions) {
  el.sessionList.innerHTML = "";
  if (!(sessions ?? []).length) {
    const d = document.createElement("div");
    d.className = "empty";
    d.textContent = "还没有会话";
    el.sessionList.appendChild(d);
    return;
  }
  for (const s of sessions) {
    const row = document.createElement("div");
    row.className = "session-item" + (s.id === sessionID ? " sel" : "");
    const title = document.createElement("span");
    title.className = "title";
    title.textContent = s.title || "新会话";
    const close = document.createElement("button");
    close.className = "close";
    close.type = "button";
    close.title = "关闭会话";
    close.textContent = "×";
    close.addEventListener("click", async (e) => {
      e.stopPropagation();
      // 关会话 = DELETE 整条（删完后端不代建 —— 关的是当前 ⇒ 进剩下第一条，没有就建一条）。
      await api.closeSession(s.id).catch((err) => alert(`关闭失败：${err.message}`));
      if (s.id === sessionID) {
        const rest = await api.listSessions();
        if (rest.length) await enterSession(rest[0].id);
        else await enterSession((await api.createSession({})).id);
      } else refreshSessionsBar();
    });
    row.append(title, close);
    row.addEventListener("click", () => enterSession(s.id));
    el.sessionList.appendChild(row);
  }
}

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
  fillModelSelect(document.getElementById("session-model"));
  fillAgentSelect(document.getElementById("session-agent"));
  renderSessions(sessions);
  renderHistory(messages);
  refreshStatusline("idle");
}

document.getElementById("session-model").addEventListener("change", (e) => {
  if (!sessionID || !e.target.value) return;
  applyModelValue(e.target.value).catch((err) => addMessage("系统", `换模型失败：${err.message}`, "assistant"));
});
document.getElementById("session-agent").addEventListener("change", (e) => {
  if (!sessionID || !e.target.value) return;
  applyAgentValue(e.target.value).catch((err) => addMessage("系统", `换 Agent 失败：${err.message}`, "assistant"));
});

async function boot() {
  const health = await api.health();
  el.conn.textContent = `已连接 v${health.version}`;
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
  const bubble = addMessage("助手", "生成中…", "assistant");
  const textEl = bubble.querySelector(".text");
  const turnID = accepted.turn.message_id;
  let cur = { from: 0, thinkFrom: 0 };
  for (;;) {
    await new Promise((r) => setTimeout(r, 300));
    const st = await api.status(sessionID);
    const slice = await api.turnText(sessionID, cur.from, cur.thinkFrom);
    if (slice.text) textEl.textContent = (textEl.textContent === "生成中…" ? "" : textEl.textContent) + slice.text;
    cur = advanceCursor(cur, slice);
    refreshStatusline(st.phase, st.elapsed_ms);
    refreshSessionsBar();
    if (st.phase === "idle" || st.phase === "error") {
      if (st.phase === "error") textEl.textContent = `失败：${st.error || "未知"}`;
      else if (!textEl.textContent || textEl.textContent === "生成中…") {
        const all = await api.listMessages(sessionID);
        const last = all[all.length - 1];
        if (last && last.id === turnID) textEl.textContent = last.content;
      }
      refreshStatusline(st.phase, st.elapsed_ms);
      refreshSessionsBar();
      break;
    }
  }
}

// ---- 命令面板（输入 / 开头过滤，Tab/上下+回车）----
function showPalette(items) {
  palItems = items;
  palSel = 0;
  el.paletteList.innerHTML = "";
  items.forEach((name, i) => {
    const d = document.createElement("div");
    d.className = "palette-item" + (i === 0 ? " sel" : "");
    d.textContent = "/" + name;
    d.onclick = () => runCommand(name, []);
    el.paletteList.appendChild(d);
  });
  el.palette.classList.toggle("hidden", items.length === 0);
}

function movePalette(delta) {
  if (!palItems.length) return;
  palSel = (palSel + delta + palItems.length) % palItems.length;
  [...el.paletteList.children].forEach((c, i) => c.classList.toggle("sel", i === palSel));
}

function showPicker(title, items, action) {
  pickerItems = items;
  pickerSel = 0;
  pickerAction = action;
  el.pickerTitle.textContent = title;
  el.pickerList.innerHTML = "";
  items.forEach((it, i) => {
    const d = document.createElement("div");
    d.className = "picker-item" + (i === 0 ? " sel" : "");
    d.textContent = it.label;
    d.onclick = () => { hidePicker(); action(it); };
    el.pickerList.appendChild(d);
  });
  el.picker.classList.remove("hidden");
}

function movePicker(delta) {
  if (!pickerItems.length) return;
  pickerSel = (pickerSel + delta + pickerItems.length) % pickerItems.length;
  [...el.pickerList.children].forEach((c, i) => c.classList.toggle("sel", i === pickerSel));
}

function hidePicker() {
  el.picker.classList.add("hidden");
  pickerAction = null;
}

function confirmAsk(text) {
  return new Promise((resolve) => {
    el.confirmText.textContent = text;
    el.confirmBox.classList.remove("hidden");
    const done = (v) => {
      el.confirmBox.classList.add("hidden");
      el.confirmYes.onclick = el.confirmNo.onclick = null;
      resolve(v);
    };
    el.confirmYes.onclick = () => done(true);
    el.confirmNo.onclick = () => done(false);
  });
}


async function runCommand(name, args) {
  el.palette.classList.add("hidden");
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
      addMessage("系统", `压缩已受理${n ? `（${n} 块）` : ""}`, "assistant");
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
        if (!m) { addMessage("系统", `没有第 ${n} 条`, "assistant"); break; }
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
      if (!items.length) { addMessage("系统", "这条会话还没有摘要", "assistant"); break; }
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
          addMessage("系统", "摘要已改", "assistant");
        },
      );
      break;
    }
    default:
      addMessage("系统", `未知命令 /${name}`, "assistant");
  }
}

// pickModel / fillModelSelect / applyModelValue：顶部原生 <select> 换模型。
// 选项按 provider 分 optgroup（当前值选中；换选项即 PATCH 两格，不再弹 picker）。
function pickModel() {
  fillModelSelect(document.getElementById("session-model"));
}

function fillModelSelect(sel) {
  api.models().then((models) => {
    sel.innerHTML = "";
    const cur = sessionInfo.provider && sessionInfo.model ? `${sessionInfo.provider}|||${sessionInfo.model}` : "";
    for (const g of groupModelsByProvider(models)) {
      const og = document.createElement("optgroup");
      og.label = g.provider;
      for (const m of g.items) {
        const o = document.createElement("option");
        o.value = `${g.provider}|||${m.upstream_id}`;
        o.textContent = m.name ?? m.upstream_id;
        og.appendChild(o);
      }
      sel.appendChild(og);
    }
    if ([...sel.options].some((o) => o.value === cur)) sel.value = cur;
    else if (!cur && sel.options[0]) applyModelValue(sel.options[0].value);
  }).catch((err) => addMessage("系统", `模型列表失败：${err.message}`, "assistant"));
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
  fillAgentSelect(document.getElementById("session-agent"));
}

function fillAgentSelect(sel) {
  api.agents().then((data) => {
    sel.innerHTML = "";
    for (const a of data.agents ?? []) {
      const o = document.createElement("option");
      o.value = a.id;
      o.textContent = a.name;
      sel.appendChild(o);
    }
    if ([...sel.options].some((o) => o.value === sessionInfo.agent_id)) sel.value = sessionInfo.agent_id;
    else if (!sessionInfo.agent_id && sel.options[0]) applyAgentValue(sel.options[0].value);
  }).catch((err) => addMessage("系统", `Agent 列表失败：${err.message}`, "assistant"));
}

async function applyAgentValue(id) {
  if (!id) return;
  sessionInfo = await api.patchSession(sessionID, { agent_id: id });
  paintSessionHeader();
  refreshStatusline("idle");
}

// paintSessionHeader：顶部 `会话名 · 模型 · Agent` 三段（改完当场重画，不等轮询）。
function paintSessionHeader() {
  const who = [sessionInfo.provider, sessionInfo.model].filter(Boolean).join("/") || "还没选模型";
  el.session.textContent = `${sessionInfo.title || "新会话"} · ${who} · ${sessionInfo.agent_id ?? "?"}`;
}
let settingsTab = "server";
let settingsSnap = null;
function showSettingsTab(name) {
  settingsTab = switchSettingsTab(settingsTab, name);
  for (const t of SETTINGS_TABS) {
    document.getElementById(`tab-${t}`)?.classList.toggle("sel", t === settingsTab);
    document.getElementById(`panel-${t}`)?.classList.toggle("hidden", t !== settingsTab);
  }
}
function readServerForm() {
  return {
    title_chars: document.getElementById("chat-title-chars").value,
    model_context_tokens: document.getElementById("chat-model-ctx").value,
    compact_blocks: document.getElementById("chat-compact-blocks").value,
    compact_trigger_tokens: document.getElementById("chat-compact-trigger").value,
    provider: document.getElementById("def-provider").value,
    model: document.getElementById("def-model").value,
    agent: document.getElementById("def-agent").value,
  };
}
function wireSettings() {
  const modal = document.getElementById("settings");
  // 客户端两格预填（localStorage 现值，开 modal 就读；关丢弃 ⇒ 不保存不写）。
  const loadClient = () => {
    document.getElementById("cli-api").value = localStorage.getItem("mc_api") || "http://127.0.0.1:8787/api/v1";
    document.getElementById("cli-token").value = localStorage.getItem("mc_token") || "";
    document.getElementById("cli-preview").checked = previewOn();
    document.getElementById("cli-theme").value = localStorage.getItem("mc_theme") || "";
  };
  const open = async () => {
    settingsSnap = snapshotSettings(readServerForm());
    modal.classList.remove("hidden");
    showSettingsTab(settingsTab);
    loadClient();
    loadChat().catch(() => {});
    loadDefaults().catch(() => {});
    loadAgents().catch(() => {});
    loadProviders().catch(() => {});
  };
  const close = () => modal.classList.add("hidden"); // 关丢弃：不回写、不 PUT
  document.getElementById("settings-open").onclick = open;
  document.getElementById("settings-close").onclick = close;
  // 全屏切换（localStorage.mc_settings_full 记住；Agent 宽表用）。
  const box = modal.querySelector(".modal-box");
  if (localStorage.getItem("mc_settings_full") === "1") box?.classList.add("full");
  document.getElementById("settings-full").onclick = () => {
    const on = box?.classList.toggle("full") ?? false;
    localStorage.setItem("mc_settings_full", on ? "1" : "0");
  };
  modal.addEventListener("click", (e) => { if (e.target === modal) close(); });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && !modal.classList.contains("hidden")) close();
  });
  for (const t of SETTINGS_TABS) {
    document.getElementById(`tab-${t}`).onclick = () => showSettingsTab(t);
  }

  // 服务端区：chat 四数 → GET+PUT /config/chat；defaults 三格 → GET+PUT /config/defaults。
  const loadChat = async () => {
    const c = await api.getChat();
    document.getElementById("chat-title-chars").value = c.title_chars ?? "";
    document.getElementById("chat-model-ctx").value = c.model_context_tokens ?? "";
    document.getElementById("chat-compact-blocks").value = c.compact_blocks ?? "";
    document.getElementById("chat-compact-trigger").value = c.compact_trigger_tokens ?? "";
  };
  document.getElementById("chat-save").onclick = async () => {
    const num = (id) => {
      const v = document.getElementById(id).value.trim();
      return v === "" ? undefined : Number(v);
    };
    const body = {};
    const t = num("chat-title-chars"); if (t !== undefined) body.title_chars = t;
    const m = num("chat-model-ctx"); if (m !== undefined) body.model_context_tokens = m;
    const b = num("chat-compact-blocks"); if (b !== undefined) body.compact_blocks = b;
    const trig = document.getElementById("chat-compact-trigger").value.trim();
    // compact_trigger_tokens 空=默认（后端 *int：不给=默认；给数=阈值）—— 用当前值补齐整段 PUT。
    const cur = await api.getChat().catch(() => ({}));
    const full = { ...cur, ...body };
    if (trig === "") delete full.compact_trigger_tokens;
    else full.compact_trigger_tokens = Number(trig);
    try {
      await api.putChat(full);
      document.getElementById("chat-status").textContent = "已保存";
    } catch (err) { document.getElementById("chat-status").textContent = `保存失败：${err.message}`; }
  };
  const loadDefaults = async () => {
    const d = await api.getDefaults();
    document.getElementById("def-provider").value = d.provider ?? "";
    document.getElementById("def-model").value = d.model ?? "";
    document.getElementById("def-agent").value = d.agent ?? "";
  };
  document.getElementById("def-save").onclick = async () => {
    try {
      await api.putDefaults({
        provider: document.getElementById("def-provider").value,
        model: document.getElementById("def-model").value,
        agent: document.getElementById("def-agent").value,
      });
      document.getElementById("def-status").textContent = "已保存";
    } catch (err) { document.getElementById("def-status").textContent = `保存失败：${err.message}`; }
  };

  // 客户端区：mc_api/mc_token 改完刷新生效；mc_preview/mc_theme 即时生效。
  document.getElementById("cli-save").onclick = () => {
    localStorage.setItem("mc_api", document.getElementById("cli-api").value.trim() || "http://127.0.0.1:8787/api/v1");
    localStorage.setItem("mc_token", document.getElementById("cli-token").value);
    localStorage.setItem("mc_preview", document.getElementById("cli-preview").checked ? "1" : "0");
    localStorage.setItem("mc_theme", document.getElementById("cli-theme").value);
    applyTheme(); // 主题即时生效（不刷新）
    document.getElementById("cli-status").textContent = "已保存（API/Token 刷新页面生效，预演与主题即时生效）";
  };

  // Agent 区：左列表+右详情（name/system_prompt/三能力checkbox/provider-model-prompt 三覆盖格/保存整段 PATCH/新建/删除/设默认）。
  let curAgent = null;
  let curDefault = "";
  const abilityInputs = {};
  const abBox = document.getElementById("agent-abilities");
  abBox.innerHTML = "";
  for (const id of ABILITY_IDS) {
    const block = document.createElement("fieldset");
    block.className = "ability-block";
    const legend = document.createElement("legend");
    const check = document.createElement("input");
    check.type = "checkbox";
    check.checked = true;
    legend.append(check, document.createTextNode(` ${id}`));
    block.appendChild(legend);
    // Model 一格：`{provider}/{model}` 下拉（空=跟会话走；选项打 GET /models 拍平）。
    const modelRow = document.createElement("label");
    modelRow.className = "field";
    const modelLabel = document.createElement("span");
    modelLabel.textContent = "Model";
    const modelSel = document.createElement("select");
    modelRow.append(modelLabel, modelSel);
    block.appendChild(modelRow);
    // 提示词覆盖独占一行（大段文本框跟右边挤没法看）。
    const promptRow = document.createElement("label");
    promptRow.className = "field-block";
    const promptLabel = document.createElement("span");
    promptLabel.textContent = "提示词覆盖（空=默认模板）";
    const promptBox = document.createElement("textarea");
    promptBox.rows = 3;
    promptRow.append(promptLabel, promptBox);
    block.appendChild(promptRow);
    abilityInputs[id] = { enabled: check, modelSel, prompt: promptBox };
    abBox.appendChild(block);
  }
  const loadAgents = async () => {
    const data = await api.agents();
    curDefault = data.default_agent ?? "";
    const box = document.getElementById("agent-list");
    box.innerHTML = "";
    for (const a of data.agents ?? []) {
      const d = document.createElement("button");
      d.type = "button";
      d.className = "agent-item" + (curAgent?.id === a.id ? " sel" : "");
      d.title = a.id;
      const name = document.createElement("span");
      name.textContent = `${a.name}${a.id === curDefault ? "（默认）" : ""}`;
      const sub = document.createElement("span");
      sub.className = "sub";
      sub.textContent = a.id;
      d.append(name, sub);
      d.onclick = () => {
        curAgent = a;
        document.getElementById("agent-detail").classList.remove("hidden");
        document.getElementById("agent-title").textContent = `${a.name} / ${a.id}${a.id === curDefault ? "（默认）" : ""}`;
        document.getElementById("agent-prompt").value = a.system_prompt ?? "";
        const ab = a.abilities ?? {};
        for (const id of ABILITY_IDS) {
          const one = ab[id] ?? {};
          abilityInputs[id].enabled.checked = one.enabled !== false;
          // 存量 `{provider,model}` 回填成下拉值（`provider/model`；散着填的老数据拼回去）。
          const both = one.provider && one.model ? `${one.provider}/${one.model}` : "";
          abilityInputs[id].modelSel.value = [...abilityInputs[id].modelSel.options].some((o) => o.value === both) ? both : "";
          abilityInputs[id].prompt.value = one.prompt ?? "";
        }
      };
      box.appendChild(d);
    }
    // Model 下拉选项：GET /models 拍平（`{provider}/{model}`；空=跟会话走）。
    try {
      const models = await api.models();
      for (const id of ABILITY_IDS) {
        const sel = abilityInputs[id].modelSel;
        sel.innerHTML = "";
        const empty = document.createElement("option");
        empty.value = "";
        empty.textContent = "跟会话走";
        sel.appendChild(empty);
        for (const m of models ?? []) {
          const o = document.createElement("option");
          o.value = `${m.provider}/${m.upstream_id}`;
          o.textContent = `${m.provider}/${m.name ?? m.upstream_id}`;
          sel.appendChild(o);
        }
      }
    } catch { /* 模型列表拿不到就不填下拉（空=跟会话走照旧） */ }
  };
  document.getElementById("agent-save").onclick = async () => {
    if (!curAgent) return;
    const rows = {};
    for (const id of ABILITY_IDS) {
      // 下拉值拆回 `{provider,model}`（空=跟会话走 ⇒ 两格都不发）。
      const both = abilityInputs[id].modelSel.value;
      const slash = both.indexOf("/");
      rows[id] = {
        enabled: abilityInputs[id].enabled.checked,
        provider: slash < 0 ? "" : both.slice(0, slash),
        model: slash < 0 ? "" : both.slice(slash + 1),
        prompt: abilityInputs[id].prompt.value,
      };
    }
    try {
      curAgent = await api.patchAgent(curAgent.id, {
        system_prompt: document.getElementById("agent-prompt").value,
        abilities: buildAbilitiesPatch(rows),
      });
      document.getElementById("agent-status").textContent = "已保存";
      loadAgents().catch(() => {});
    } catch (err) { document.getElementById("agent-status").textContent = `保存失败：${err.message}`; }
  };
  document.getElementById("agent-default").onclick = async () => {
    if (!curAgent) return;
    await api.patchAgent(curAgent.id, { make_default: true });
    loadAgents().catch(() => {});
  };
  document.getElementById("agent-del").onclick = async () => {
    if (curAgent && (await confirmAsk(`删除 agent ${curAgent.name}？`))) {
      await api.deleteAgent(curAgent.id);
      curAgent = null;
      document.getElementById("agent-detail").classList.add("hidden");
      loadAgents().catch(() => {});
    }
  };

  // Provider 区：列表（vendor/protocol/有无key/模型数）+ 新建三格外加 key + 每条三按钮 + models.dev 缓存行。
  const PROTOCOLS = ["openai-chat-completion", "openai-response", "anthropic-messages", "gemini-generate-content", "systemone"];
  const loadProviders = async () => {
    const [list, presets] = await Promise.all([api.providers(), api.providerPresets()]);
    const vsel = document.getElementById("new-provider-vendor");
    vsel.innerHTML = "";
    for (const p of presets ?? []) {
      const o = document.createElement("option");
      o.value = p.vendor;
      o.textContent = `${p.name}（${p.vendor}）`;
      vsel.appendChild(o);
    }
    const psel = document.getElementById("new-provider-protocol");
    psel.innerHTML = "";
    const blank = document.createElement("option");
    blank.value = "";
    blank.textContent = "protocol（空=默认）";
    psel.appendChild(blank);
    for (const pr of PROTOCOLS) {
      const o = document.createElement("option");
      o.value = pr;
      o.textContent = pr;
      psel.appendChild(o);
    }
    const box = document.getElementById("provider-list");
    box.innerHTML = "";
    const table = document.createElement("table");
    table.className = "plist";
    const head = document.createElement("tr");
    for (const h of ["渠道", "vendor", "protocol", "密钥", "模型", "操作"]) {
      const th = document.createElement("th");
      th.textContent = h;
      head.appendChild(th);
    }
    table.appendChild(head);
    for (const p of list ?? []) {
      const tr = document.createElement("tr");
      const cell = (text) => {
        const td = document.createElement("td");
        td.textContent = text;
        tr.appendChild(td);
        return td;
      };
      cell(p.id);
      cell(p.vendor ?? "?");
      cell(p.protocol ?? "?");
      cell(p.has_key ? "有" : "无");
      cell(String(p.models?.length ?? 0));
      const ops = document.createElement("td");
      ops.className = "ops";
      const mkBtn = (label, fn, danger) => {
        const b = document.createElement("button");
        b.type = "button";
        b.className = "link-btn" + (danger ? " danger" : "");
        b.textContent = label;
        b.onclick = fn;
        ops.appendChild(b);
        return b;
      };
      mkBtn("刷新模型", async () => {
        try {
          await api.refreshProvider(p.id);
          document.getElementById("routes-status").textContent = `${p.id}：模型已刷新`;
          loadProviders().catch(() => {});
        } catch (err) { document.getElementById("routes-status").textContent = `${p.id}：刷新失败（${err.message}）`; }
      });
      mkBtn("刷路由", async () => {
        try {
          const out = await api.refreshRoutes(p.id);
          // 刷路由回 models 数即缓存新鲜度，不另开接口。
          document.getElementById("routes-status").textContent = `路由缓存：${formatRoutesOutcome(out)}`;
        } catch (err) { document.getElementById("routes-status").textContent = `路由缓存：${p.id}：刷新失败（${err.message}）`; }
      });
      mkBtn("删除", async () => {
        if (await confirmAsk(`删除渠道 ${p.id}？`)) {
          await api.deleteProvider(p.id);
          loadProviders().catch(() => {});
        }
      }, true);
      tr.appendChild(ops);
      table.appendChild(tr);
    }
    box.appendChild(table);
  };
  document.getElementById("provider-add").onclick = async () => {
    const id = document.getElementById("new-provider-id").value.trim();
    if (!id) return;
    const protocol = document.getElementById("new-provider-protocol").value || undefined;
    await api.createProvider({
      id,
      vendor: document.getElementById("new-provider-vendor").value,
      ...(protocol ? { protocol } : {}),
      api_key: document.getElementById("new-provider-key").value || undefined,
    });
    loadProviders().catch(() => {});
  };
}

let previewTimer = 0;
el.input.addEventListener("input", () => {
  const v = el.input.value;
  if (v.startsWith("/")) {
    const cmd = parseCommand(v);
    showPalette(filterCommands(cmd ? cmd.name : ""));
  } else {
    el.palette.classList.add("hidden");
  }
  // 右载荷预演（300ms 防抖）：输入框里的字当待发那句问 (c) —— 只算不写。
  // 空输入 / 命令行不预演（退回 (b)）；400 按"没东西可预演"吞掉，不糊右栏。
  clearTimeout(previewTimer);
  previewTimer = setTimeout(async () => {
    const text = el.input.value;
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
});

el.input.addEventListener("keydown", (e) => {
  const pickerOpen = !el.picker.classList.contains("hidden");
  const palOpen = !el.palette.classList.contains("hidden");
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
  } else if (e.key === "Enter" && (palOpen || pickerOpen) && el.input.value.startsWith("/")) {
    // picker 有选中动作时优先执行 picker
    if (pickerOpen && pickerAction) {
      e.preventDefault();
      const it = pickerItems[pickerSel];
      hidePicker();
      if (it) pickerAction(it);
    } else if (palOpen && palItems[palSel]) {
      e.preventDefault();
      const cmd = parseCommand(el.input.value);
      el.input.value = "";
      runCommand(palItems[palSel], cmd ? cmd.args : []).catch((err) =>
        addMessage("系统", `命令失败：${err.message}`, "assistant"),
      );
    }
  } else if (e.key === "Escape") {
    el.palette.classList.add("hidden");
    hidePicker();
  }
});

el.form.addEventListener("submit", (e) => {
  e.preventDefault();
  const text = el.input.value.trim();
  if (!text) return;
  el.input.value = "";
  el.palette.classList.add("hidden");
  const cmd = parseCommand(text);
  if (cmd) {
    runCommand(cmd.name, cmd.args).catch((err) => addMessage("系统", `命令失败：${err.message}`, "assistant"));
    return;
  }
  send(text).catch((err) => addMessage("系统", `发送失败：${err.message}`, "assistant"));
});

// 左栏＋按钮：＋新建（建完就进）；发完/收尾两栏都刷（标题是 idle 前写好的）。
el.sessionNew.addEventListener("click", async () => {
  await enterSession((await api.createSession({})).id);
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

wireSettings();

boot().catch((err) => {
  el.conn.textContent = `连不上（${err.message}；API 地址存在 localStorage.mc_api）`;
});
