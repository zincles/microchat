// microchat web 前端 —— 与 TUI/TG 同一条 HTTP 契约（只调 /api/v1，不碰后端内部）。
//
// 依赖：零运行时依赖（fetch 直调；构建只用 vite）。契约见 AGENTS.md API 表。
const API = localStorage.getItem("mc_api") || "http://127.0.0.1:8787/api/v1";
const TOKEN = localStorage.getItem("mc_token") || "";

async function call(method, path, body) {
  const res = await fetch(API + path, {
    method,
    headers: {
      "Content-Type": "application/json",
      ...(TOKEN ? { Authorization: `Bearer ${TOKEN}` } : {}),
    },
    ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
  });
  if (res.status === 204) return null;
  const data = await res.json();
  if (!res.ok) throw new Error(data?.error?.message ?? `HTTP ${res.status}`);
  return data;
}

const el = {
  conn: document.getElementById("conn"),
  session: document.getElementById("session"),
  messages: document.getElementById("messages"),
  form: document.getElementById("composer"),
  input: document.getElementById("input"),
};

let sessionID = null;

function addMessage(who, text, cls, think) {
  const div = document.createElement("div");
  div.className = `msg ${cls}`;
  const head = document.createElement("div");
  head.className = "who";
  head.textContent = who;
  div.appendChild(head);
  if (think) {
    const t = document.createElement("div");
    t.className = "think";
    t.textContent = `思考：${think}`;
    div.appendChild(t);
  }
  const body = document.createElement("div");
  body.className = "text";
  body.textContent = text;
  div.appendChild(body);
  el.messages.appendChild(div);
  el.messages.scrollTop = el.messages.scrollHeight;
  return div;
}

async function boot() {
  // 健康检查：连上才往下走（探针也过鉴权，见 AGENTS.md）。
  const health = await call("GET", "/health");
  el.conn.textContent = `已连接 v${health.version}`;
  // 启动编排照 TUI 的口径：清掉【0 条消息且无标题】的会话，再建一条真的。
  const sessions = await call("GET", "/sessions");
  for (const s of sessions) {
    if (s.messages === 0 && !s.title) await call("DELETE", `/sessions/${s.id}`).catch(() => {});
  }
  const created = await call("POST", "/sessions", {});
  sessionID = created.id;
  el.session.textContent = created.title || "新会话";
  const messages = await call("GET", `/sessions/${sessionID}/messages`);
  for (const m of messages) {
    addMessage(m.role === "user" ? "你" : "助手", m.content, m.role, m.reasoning);
  }
}

// 发送：受理与生成分开（202 回执 + 轮询 status，见 AGENTS.md「一轮生成」）。
async function send(content) {
  const accepted = await call("POST", `/sessions/${sessionID}/messages`, { content });
  addMessage("你", content, "user");
  const bubble = addMessage("助手", "生成中…", "assistant");
  const textEl = bubble.querySelector(".text");
  const turnID = accepted.turn.message_id;
  let from = 0, thinkFrom = 0;
  for (;;) {
    await new Promise((r) => setTimeout(r, 300));
    const st = await call("GET", `/sessions/${sessionID}/status`);
    const slice = await call("GET", `/sessions/${sessionID}/turn/text?from=${from}&think_from=${thinkFrom}`);
    if (slice.text) textEl.textContent = (textEl.textContent === "生成中…" ? "" : textEl.textContent) + slice.text;
    from = slice.next;
    thinkFrom = slice.think_next;
    if (st.phase === "idle" || st.phase === "error") {
      if (st.phase === "error") textEl.textContent = `失败：${st.error || "未知"}`;
      else if (!textEl.textContent || textEl.textContent === "生成中…") {
        const all = await call("GET", `/sessions/${sessionID}/messages`);
        const last = all[all.length - 1];
        if (last && last.id === turnID) textEl.textContent = last.content;
      }
      break;
    }
  }
}

el.form.addEventListener("submit", (e) => {
  e.preventDefault();
  const text = el.input.value.trim();
  if (!text) return;
  el.input.value = "";
  send(text).catch((err) => addMessage("系统", `发送失败：${err.message}`, "assistant"));
});

boot().catch((err) => {
  el.conn.textContent = `连不上（${err.message}；API 地址存在 localStorage.mc_api）`;
});
