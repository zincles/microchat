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
    // sendMessage：发一句话（202 受理 + 后台跑；预演调的不是这条，别混）。
    sendMessage: (id, content) => call("POST", `/sessions/${enc(id)}/messages`, { content }),
    // resend：重发历史（不落新消息，拿现有历史再跑一轮；空会话 400）。
    resend: (id) => call("POST", `/sessions/${enc(id)}/resend`),
    // editMessage：改任意一条消息的正文 / 思考（至少给一个；nil=不动、""=清掉）。
    editMessage: (sid, mid, body) => call("PATCH", `/sessions/${enc(sid)}/messages/${enc(mid)}`, body),
    // editSummary：手改任意一条摘要的正文（就地换 text；空 ⇒ 400）。
    editSummary: (sid, sumid, text) => call("PATCH", `/sessions/${enc(sid)}/summaries/${enc(sumid)}`, { text }),
    // listSummaries：这条会话的摘要列表（/editsum 的 picker 用；id + text + dirty 现成）。
    listSummaries: (sid) => call("GET", `/sessions/${enc(sid)}/summaries`),
    patchSession: (id, body) => call("PATCH", `/sessions/${enc(id)}`, body),
    deleteProvider: (id) => call("DELETE", `/providers/${enc(id)}`),
    status: (id) => call("GET", `/sessions/${enc(id)}/status`),
    turnText: (id, from, thinkFrom) =>
      call("GET", `/sessions/${enc(id)}/turn/text?from=${from}&think_from=${thinkFrom}`),
    context: (id) => call("GET", `/sessions/${enc(id)}/context`),
    // outgoingPreview：右载荷预演 —— 有字问 (c)（待发追尾），空字问现有历史（与空发 resend 同段）。
    // 空会话空字 ⇒ 后端 400（与 resend 同错），调用方吞掉保持空话。
    outgoingPreview: (id, content) =>
      call("POST", `/sessions/${enc(id)}/outgoing`, { content: content ?? "" }),
    compact: (id, blocks) =>
      call("POST", `/sessions/${enc(id)}/compact`, blocks ? { blocks } : {}),
    stop: (id) => call("POST", `/sessions/${enc(id)}/stop`, {}),
    rerollEnter: (id) => call("POST", `/sessions/${enc(id)}/reroll-message`, {}),
    rerollState: (id) => call("GET", `/sessions/${enc(id)}/reroll-message`),
    rerollSwitch: (id, idx) => call("POST", `/sessions/${enc(id)}/reroll-message/switch`, { idx }),
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
    patchProvider: (id, body) => call("PATCH", `/providers/${enc(id)}`, body),
    refreshProvider: (id) => call("POST", `/providers/${enc(id)}/refresh`),
    // refreshRoutes：刷路由表（models.dev 快照 → model_route 落库），回 {provider, models, error?}。
    refreshRoutes: (id) => call("POST", `/providers/${enc(id)}/refresh-routes`),
  };
}
