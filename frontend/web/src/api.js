// microchat web API 层 —— 与 TUI/TG 同一条 HTTP 契约（只调 /api/v1）。
// 零运行时依赖：fetch 直调。提一个 createApi 工厂，方便测试注入 fetch。
export function createApi({ base, token = "", fetchFn = fetch }) {
  async function call(method, path, body) {
    const res = await fetchFn(base + path, {
      method,
      headers: {
        "Content-Type": "application/json",
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
      ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
    });
    if (res.status === 204) return null;
    const data = await res.json();
    if (!res.ok) throw new Error(data?.error?.message ?? `HTTP ${res.status}`);
    return data;
  }

  const enc = encodeURIComponent;
  return {
    call,
    health: () => call("GET", "/health"),
    listSessions: () => call("GET", "/sessions"),
    createSession: (body = {}) => call("POST", "/sessions", body),
    deleteSession: (id) => call("DELETE", `/sessions/${enc(id)}`),
    // closeSession：关会话 = DELETE 整条（删完后端不代建 —— 进哪条由调用方定）。
    closeSession: (id) => call("DELETE", `/sessions/${enc(id)}`),
    listMessages: (id) => call("GET", `/sessions/${enc(id)}/messages`),
    patchSession: (id, body) => call("PATCH", `/sessions/${enc(id)}`, body),
    status: (id) => call("GET", `/sessions/${enc(id)}/status`),
    turnText: (id, from, thinkFrom) =>
      call("GET", `/sessions/${enc(id)}/turn/text?from=${from}&think_from=${thinkFrom}`),
    context: (id) => call("GET", `/sessions/${enc(id)}/context`),
    outgoing: (id) => call("GET", `/sessions/${enc(id)}/outgoing`),
    // outgoingPreview：右载荷预演 —— 待发那句问 (c)（只算不写）。
    // 空/空白不发包（后端 400 也无意义）⇒ 直接回 null，调用方不刷右栏。
    outgoingPreview: (id, content) => {
      if (!String(content ?? "").trim()) return Promise.resolve(null);
      return call("POST", `/sessions/${enc(id)}/outgoing`, { content });
    },
    compact: (id, blocks) =>
      call("POST", `/sessions/${enc(id)}/compact`, blocks ? { blocks } : {}),
    stop: (id) => call("POST", `/sessions/${enc(id)}/stop`, {}),
    rerollEnter: (id) => call("POST", `/sessions/${enc(id)}/reroll-message`, {}),
    rerollState: (id) => call("GET", `/sessions/${enc(id)}/reroll-message`),
    rerollSwitch: (id, idx) => call("POST", `/sessions/${enc(id)}/reroll-message/switch`, { idx }),
    rerollDelete: (id, idx) => call("DELETE", `/sessions/${enc(id)}/reroll-message/${idx}`),
    rerollClear: (id) => call("DELETE", `/sessions/${enc(id)}/reroll-message`),
    rerollSummaryEnter: (id, idx) => call("POST", `/sessions/${enc(id)}/reroll-summary`, { idx }),
    rerollSummaryState: (id) => call("GET", `/sessions/${enc(id)}/reroll-summary`),
    rerollSummarySwitch: (id, idx) =>
      call("POST", `/sessions/${enc(id)}/reroll-summary/switch`, { idx }),
    deletionPreview: (sid, mid) =>
      call("GET", `/sessions/${enc(sid)}/messages/${enc(mid)}/deletion-preview`),
    deleteMessages: (sid, mid, lastDeletedMessageID) =>
      call("DELETE", `/sessions/${enc(sid)}/messages/${enc(mid)}`, {
        last_deleted_message_id: lastDeletedMessageID,
      }),
    getChat: () => call("GET", "/config/chat"),
    putChat: (body) => call("PUT", "/config/chat", body),
    // models：跨 provider 拍平（`{provider, upstream_id, name}` —— 下拉选项就打它）。
    models: () => call("GET", "/models"),
    agents: () => call("GET", "/agents"),
    createAgent: (body) => call("POST", "/agents", body),
    patchAgent: (id, body) => call("PATCH", `/agents/${enc(id)}`, body),
    deleteAgent: (id) => call("DELETE", `/agents/${enc(id)}`),
    getDefaults: () => call("GET", "/config/defaults"),
    putDefaults: (body) => call("PUT", "/config/defaults", body),
    providers: () => call("GET", "/providers"),
    providerPresets: () => call("GET", "/providers/presets"),
    createProvider: (body) => call("POST", "/providers", body),
    refreshProvider: (id) => call("POST", `/providers/${enc(id)}/refresh`),
    // refreshRoutes：刷路由表（models.dev 快照 → model_route 落库），回 {provider, models, error?}。
    refreshRoutes: (id) => call("POST", `/providers/${enc(id)}/refresh-routes`),
  };
}

// 命令解析："/name arg1 arg2" → {name, args}；非 / 开头 → null。
export function parseCommand(input) {
  if (!input.startsWith("/")) return null;
  const parts = input.slice(1).trim().split(/\s+/).filter(Boolean);
  if (!parts.length) return { name: "", args: [] };
  return { name: parts[0], args: parts.slice(1) };
}

export const COMMANDS = [
  "resume",
  "model",
  "providers",
  "agents",
  "compact",
  "reroll",
  "switch",
  "stop",
  "new",
  "delete",
  "cut",
];

// 前缀过滤（/ 开头输入用；空名回全部）。
export function filterCommands(name) {
  const q = (name ?? "").toLowerCase();
  return COMMANDS.filter((c) => c.startsWith(q));
}

// 游标推进：next/think_next 只增不减；回退的包直接丢弃。
export function advanceCursor(state, slice) {
  if (slice.next < state.from || slice.think_next < state.thinkFrom) return { ...state, stale: true };
  return { from: slice.next, thinkFrom: slice.think_next, stale: false };
}

export function buildTurnTextPath(sessionID, from, thinkFrom) {
  return `/sessions/${encodeURIComponent(sessionID)}/turn/text?from=${from}&think_from=${thinkFrom}`;
}
