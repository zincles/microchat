// microchat web 前端 —— 与 TUI/TG 同一条 HTTP 契约（只调 /api/v1，不碰后端内部）。
// 零运行时依赖（fetch 直调；构建只用 vite）。契约见 AGENTS.md API 表。
// 拆分：api.js（call+端点函数）/ ui.js（渲染）/ main.js（接线）。
import { createApi, parseCommand, filterCommands, advanceCursor } from "./src/api.js";
import {
  renderMessage,
  renderOutgoingItem,
  formatStatusLine,
  statusOverBudget,
  groupModelsByProvider,
  deletionPlanSummary,
} from "./src/ui.js";

const API = localStorage.getItem("mc_api") || "http://127.0.0.1:8787/api/v1";
const TOKEN = localStorage.getItem("mc_token") || "";
const api = createApi({ base: API, token: TOKEN });

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
  outgoingBox: document.getElementById("outgoing-box"),
  outgoingHead: document.getElementById("outgoing-head"),
  outgoing: document.getElementById("outgoing"),
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

function addMessage(who, text, cls, think) {
  const node = renderMessage(document, {
    who,
    role: cls === "user" ? "user" : "assistant",
    content: text,
    reasoning: typeof think === "string" ? think : think?.text,
    reasoningMs: think?.ms,
  });
  el.messages.appendChild(node);
  el.messages.scrollTop = el.messages.scrollHeight;
  return node;
}

function renderHistory(messages) {
  el.messages.innerHTML = "";
  for (const m of messages) {
    addMessage(
      m.role === "user" ? "你" : "助手",
      m.content,
      m.role,
      m.reasoning ? { text: m.reasoning, ms: m.reasoning_ms } : null,
    );
  }
}

async function refreshStatusline(phase, elapsedMs) {
  try {
    const [ctx, out] = await Promise.all([
      api.context(sessionID).catch(() => null),
      api.outgoing(sessionID).catch(() => null),
    ]);
    el.statusline.textContent = formatStatusLine({
      ctx,
      phase,
      elapsedMs,
      provider: sessionInfo.provider,
      model: sessionInfo.model,
    });
    el.statusline.classList.toggle("over", statusOverBudget(ctx));
    if (out) {
      el.outgoingHead.textContent = `出站载荷（${out.length} 条）`;
      el.outgoing.innerHTML = "";
      for (const item of out) el.outgoing.appendChild(renderOutgoingItem(document, item));
    }
  } catch { /* 状态行失败不挡主流程 */ }
}

async function boot() {
  const health = await call("GET", "/health");
  el.conn.textContent = `已连接 v${health.version}`;
  const sessions = await call("GET", "/sessions");
  for (const s of sessions) {
    if (s.messages === 0 && !s.title) await call("DELETE", `/sessions/${s.id}`).catch(() => {});
  }
  const created = await call("POST", "/sessions", {});
  sessionID = created.id;
  sessionInfo = created;
  el.session.textContent = created.title || "新会话";
  const messages = await call("GET", `/sessions/${sessionID}/messages`);
  renderHistory(messages);
  refreshStatusline("idle");
}

// 发送：受理与生成分开（202 回执 + 轮询 status，见 AGENTS.md「一轮生成」）。
async function send(content) {
  const accepted = await call("POST", `/sessions/${sessionID}/messages`, { content });
  addMessage("你", content, "user");
  const bubble = addMessage("助手", "生成中…", "assistant");
  const textEl = bubble.querySelector(".text");
  const turnID = accepted.turn.message_id;
  let cur = { from: 0, thinkFrom: 0 };
  for (;;) {
    await new Promise((r) => setTimeout(r, 300));
    const st = await call("GET", `/sessions/${sessionID}/status`);
    const slice = await call(
      "GET",
      `/sessions/${sessionID}/turn/text?from=${cur.from}&think_from=${cur.thinkFrom}`,
    );
    if (slice.text) textEl.textContent = (textEl.textContent === "生成中…" ? "" : textEl.textContent) + slice.text;
    cur = advanceCursor(cur, slice);
    refreshStatusline(st.phase, st.elapsed_ms);
    if (st.phase === "idle" || st.phase === "error") {
      if (st.phase === "error") textEl.textContent = `失败：${st.error || "未知"}`;
      else if (!textEl.textContent || textEl.textContent === "生成中…") {
        const all = await call("GET", `/sessions/${sessionID}/messages`);
        const last = all[all.length - 1];
        if (last && last.id === turnID) textEl.textContent = last.content;
      }
      refreshStatusline(st.phase, st.elapsed_ms);
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

async function enterSession(id) {
  sessionID = id;
  const messages = await api.listMessages(id);
  renderHistory(messages);
  const list = await api.listSessions();
  const s = list.find((x) => x.id === id);
  if (s) {
    sessionInfo = s;
    el.session.textContent = s.title || "新会话";
  }
  refreshStatusline("idle");
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
      const models = await api.models();
      const items = [];
      for (const g of groupModelsByProvider(models)) {
        for (const m of g.items) items.push({ label: `${g.provider} / ${m.name ?? m.upstream_id}`, value: m });
      }
      showPicker("选模型", items, async (it) => {
        sessionInfo = await api.patchSession(sessionID, {
          provider: it.value.provider,
          model: it.value.upstream_id,
        });
        refreshStatusline("idle");
      });
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
    default:
      addMessage("系统", `未知命令 /${name}`, "assistant");
  }
}

// ---- 管理抽屉三件（每件一个 <details>，复用 call()）----
function wireManage() {
  document.getElementById("manage-toggle").onclick = () =>
    document.getElementById("manage").classList.toggle("hidden");

  const loadProviders = async () => {
    const [list, presets] = await Promise.all([api.providers(), api.providerPresets()]);
    const sel = document.getElementById("preset-select");
    sel.innerHTML = "";
    for (const p of presets ?? []) {
      const o = document.createElement("option");
      o.value = p.vendor;
      o.textContent = `${p.name}（${p.vendor}）`;
      sel.appendChild(o);
    }
    const box = document.getElementById("provider-list");
    box.innerHTML = "";
    for (const p of list ?? []) {
      const d = document.createElement("div");
      d.textContent = `${p.id}${p.has_key ? "（有key）" : ""}`;
      d.dataset.pid = p.id;
      d.onclick = () => { document.getElementById("new-provider-id").value = p.id; };
      box.appendChild(d);
    }
  };
  document.getElementById("provider-add").onclick = async () => {
    await api.createProvider({
      id: document.getElementById("new-provider-id").value.trim(),
      vendor: document.getElementById("preset-select").value,
      api_key: document.getElementById("new-provider-key").value || undefined,
    });
    loadProviders().catch(() => {});
  };
  document.getElementById("provider-del").onclick = async () => {
    const id = document.getElementById("new-provider-id").value.trim();
    if (id && (await confirmAsk(`删除渠道 ${id}？`))) {
      await api.deleteProvider(id);
      loadProviders().catch(() => {});
    }
  };

  let curAgent = null;
  const loadAgents = async () => {
    const data = await api.agents();
    const box = document.getElementById("agent-list");
    box.innerHTML = "";
    for (const a of data.agents ?? []) {
      const d = document.createElement("div");
      d.className = "picker-item";
      d.textContent = `${a.name}（${a.id}）`;
      d.onclick = () => {
        curAgent = a;
        document.getElementById("agent-detail").classList.remove("hidden");
        document.getElementById("agent-title").textContent = `${a.name} / ${a.id}`;
        document.getElementById("agent-prompt").value = a.system_prompt ?? "";
        const ab = a.abilities ?? {};
        document.getElementById("ab-title").checked = ab.title?.enabled !== false;
        document.getElementById("ab-compact").checked = ab.compact?.enabled !== false;
        document.getElementById("ab-judge").checked = ab.judge?.enabled !== false;
      };
      box.appendChild(d);
    }
  };
  document.getElementById("agent-add").onclick = async () => {
    const name = document.getElementById("new-agent-name").value.trim();
    if (name) {
      await api.createAgent({ name, system_prompt: "" });
      loadAgents().catch(() => {});
    }
  };
  document.getElementById("agent-save").onclick = async () => {
    if (!curAgent) return;
    const ab = (on) => ({ enabled: !!on });
    await api.patchAgent(curAgent.id, {
      system_prompt: document.getElementById("agent-prompt").value,
      abilities: {
        title: ab(document.getElementById("ab-title").checked),
        compact: ab(document.getElementById("ab-compact").checked),
        judge: ab(document.getElementById("ab-judge").checked),
      },
    });
  };
  document.getElementById("agent-del").onclick = async () => {
    if (curAgent && (await confirmAsk(`删除 agent ${curAgent.name}？`))) {
      await api.deleteAgent(curAgent.id);
      curAgent = null;
      document.getElementById("agent-detail").classList.add("hidden");
      loadAgents().catch(() => {});
    }
  };

  const loadDefaults = async () => {
    const d = await api.getDefaults();
    document.getElementById("def-provider").value = d.provider ?? "";
    document.getElementById("def-model").value = d.model ?? "";
    document.getElementById("def-agent").value = d.agent ?? "";
  };
  document.getElementById("def-save").onclick = async () => {
    await api.putDefaults({
      provider: document.getElementById("def-provider").value,
      model: document.getElementById("def-model").value,
      agent: document.getElementById("def-agent").value,
    });
    document.getElementById("def-status").textContent = "已保存";
  };
  document.getElementById("manage-toggle").addEventListener("click", () => {
    loadProviders().catch(() => {});
    loadAgents().catch(() => {});
    loadDefaults().catch(() => {});
  }, { once: true });
}

el.input.addEventListener("input", () => {
  const v = el.input.value;
  if (v.startsWith("/")) {
    const cmd = parseCommand(v);
    showPalette(filterCommands(cmd ? cmd.name : ""));
  } else {
    el.palette.classList.add("hidden");
  }
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

wireManage();

boot().catch((err) => {
  el.conn.textContent = `连不上（${err.message}；API 地址存在 localStorage.mc_api）`;
});
