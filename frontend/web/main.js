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
  SETTINGS_TABS,
  switchSettingsTab,
  snapshotSettings,
  formatRoutesOutcome,
  ABILITY_IDS,
  buildAbilitiesPatch,
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
  outgoing: document.getElementById("outgoing"),
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
    if (out) renderPayload(out, null);
  } catch { /* 状态行失败不挡主流程 */ }
}

// renderPayload：右载荷栏（(b) 已定历史；预演时后面跟一条 pending 待发项）。
// 空态"输入框打字即预演"（补在 outgoing 容器内，不另起容器）。
function renderPayload(items, pendingText) {
  el.outgoing.innerHTML = "";
  if (!(items ?? []).length && pendingText == null) {
    const d = document.createElement("div");
    d.className = "empty";
    d.textContent = "输入框打字即预演";
    el.outgoing.appendChild(d);
    return;
  }
  for (const item of items ?? []) el.outgoing.appendChild(renderOutgoingItem(document, item));
  if (pendingText != null) {
    el.outgoing.appendChild(
      renderOutgoingItem(document, { type: "message", pending: true, content: pendingText }),
    );
  }
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
  el.session.textContent = sessionInfo.title || "新会话";
  renderSessions(sessions);
  renderHistory(messages);
  refreshStatusline("idle");
}

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

// ---- 设置 modal（齿轮开全屏四 tab；不保存不写 —— 打开 snapshot，关丢弃）----
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

  // 客户端区：localStorage.mc_api/mc_token 两格（改完提示刷新页面）。
  document.getElementById("cli-save").onclick = () => {
    localStorage.setItem("mc_api", document.getElementById("cli-api").value.trim() || "http://127.0.0.1:8787/api/v1");
    localStorage.setItem("mc_token", document.getElementById("cli-token").value);
    document.getElementById("cli-status").textContent = "已保存，刷新页面生效";
  };

  // Agent 区：左列表+右详情（name/system_prompt/三能力checkbox/provider-model-prompt 三覆盖格/保存整段 PATCH/新建/删除/设默认）。
  let curAgent = null;
  let curDefault = "";
  const abilityInputs = {};
  const abBox = document.getElementById("agent-abilities");
  abBox.innerHTML = "";
  for (const id of ABILITY_IDS) {
    const wrap = document.createElement("div");
    wrap.className = "ability";
    wrap.innerHTML = `<label><input type="checkbox" checked /> ${id}</label> `;
    const mk = (key, ph) => {
      const inp = document.createElement("input");
      inp.placeholder = `${id} ${key}（空=默认）`;
      inp.title = ph;
      wrap.appendChild(inp);
      return inp;
    };
    abilityInputs[id] = {
      enabled: wrap.querySelector("input[type=checkbox]"),
      provider: mk("provider", "留空=用会话自己的"),
      model: mk("model", "留空=用会话自己的"),
      prompt: mk("prompt", "留空=用代码默认模板"),
    };
    abBox.appendChild(wrap);
  }
  const loadAgents = async () => {
    const data = await api.agents();
    curDefault = data.default_agent ?? "";
    const box = document.getElementById("agent-list");
    box.innerHTML = "";
    for (const a of data.agents ?? []) {
      const d = document.createElement("div");
      d.className = "picker-item" + (curAgent?.id === a.id ? " sel" : "");
      d.textContent = `${a.name}${a.id === curDefault ? "（默认）" : ""}`;
      d.title = a.id;
      d.onclick = () => {
        curAgent = a;
        document.getElementById("agent-detail").classList.remove("hidden");
        document.getElementById("agent-title").textContent = `${a.name} / ${a.id}${a.id === curDefault ? "（默认）" : ""}`;
        document.getElementById("agent-prompt").value = a.system_prompt ?? "";
        const ab = a.abilities ?? {};
        for (const id of ABILITY_IDS) {
          const one = ab[id] ?? {};
          abilityInputs[id].enabled.checked = one.enabled !== false;
          abilityInputs[id].provider.value = one.provider ?? "";
          abilityInputs[id].model.value = one.model ?? "";
          abilityInputs[id].prompt.value = one.prompt ?? "";
        }
      };
      box.appendChild(d);
    }
  };
  document.getElementById("agent-add").onclick = async () => {
    const name = document.getElementById("new-agent-name").value.trim();
    if (!name) return;
    await api.createAgent({ name, system_prompt: "" });
    loadAgents().catch(() => {});
  };
  document.getElementById("agent-save").onclick = async () => {
    if (!curAgent) return;
    const rows = {};
    for (const id of ABILITY_IDS) {
      rows[id] = {
        enabled: abilityInputs[id].enabled.checked,
        provider: abilityInputs[id].provider.value,
        model: abilityInputs[id].model.value,
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
    for (const p of list ?? []) {
      const d = document.createElement("div");
      d.className = "provider-item";
      const info = document.createElement("span");
      info.textContent = `${p.id}｜${p.vendor ?? "?"}｜${p.protocol ?? "?"}｜${p.has_key ? "有key" : "无key"}｜${p.models?.length ?? 0}模型`;
      d.appendChild(info);
      const mkBtn = (label, fn) => {
        const b = document.createElement("button");
        b.type = "button";
        b.textContent = label;
        b.onclick = fn;
        d.appendChild(b);
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
      });
      box.appendChild(d);
    }
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
    if (!sessionID || !text.trim() || text.startsWith("/")) {
      refreshStatusline("idle").catch(() => {});
      return;
    }
    try {
      const out = await api.outgoingPreview(sessionID, text);
      if (out == null) return; // 空守卫回 null ⇒ 不刷，等下一拍
      renderPayload(out, text);
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

wireSettings();

boot().catch((err) => {
  el.conn.textContent = `连不上（${err.message}；API 地址存在 localStorage.mc_api）`;
});
