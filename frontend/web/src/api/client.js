// microchat web API 层 —— 与 TUI/TG 同一条 HTTP 契约（只调 /api/v1）。
// 零运行时依赖：fetch 直调。提一个 createApi 工厂，方便测试注入 fetch。
export const DEFAULT_API_URL = "http://127.0.0.1:8787/api/v1";

// mergeStreamSlice：把一次游标读并进（正文/思考）累积器 —— 流式轮询的唯一合并规则。
// `slice.next < cur.from` = 游标回退（后端缓冲被新一轮重置）⇒ **清零重建**：
// 返回空累积 + 游标归零，下一拍从 0 读回新缓冲的全部内容（不是"接着往后追加"——
// 那样会把旧一轮的尾巴接在新一轮前面，且永远错过新缓冲开头）。
// 正常情况：追加本段、游标推进到 slice.next/think_next。
export function mergeStreamSlice(acc, cur, slice) {
  const reset = slice.next < cur.from || slice.think_next < cur.thinkFrom;
  if (reset) return { text: "", reasoning: "", cur: { from: 0, thinkFrom: 0 }, reset: true };
  return {
    text: acc.text + (slice.text ?? ""),
    reasoning: acc.reasoning + (slice.thinking ?? ""),
    cur: { from: slice.next, thinkFrom: slice.think_next },
    reset: false,
  };
}

// readJson：2xx/错误体都当 JSON 读；读不出来（如框架 405 的 text/plain）不炸成 SyntaxError ——
// 错误面给 null（由 httpError 回退 `HTTP <status>`），成功面照旧原样抛（成功体必是 JSON，不许瞒）。
async function readJson(res) {
  try {
    return await res.json();
  } catch (e) {
    if (res.ok) throw e;
    return null;
  }
}

// httpError：非 2xx 的统一定形 —— message = 后端文案，**code 原样带上**（调用方按 code 分支，
// 别猜文案；没错误体时 code=null、message 回退 `HTTP <status>`）。
function httpError(data, res) {
  const e = new Error(data?.error?.message ?? `HTTP ${res.status}`);
  e.code = data?.error?.code ?? null;
  e.status = res.status;
  return e;
}

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
    const data = await readJson(res);
    if (!res.ok) throw httpError(data, res);
    return data;
  }

  // callText：原文 POST（JSONL 导入口：体不是 JSON，不能走 JSON.stringify）。
  async function callText(method, path, text) {
    const res = await fetchFn(base + path, {
      method,
      headers: {
        "Content-Type": "text/plain",
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
      body: text,
    });
    const data = await readJson(res);
    if (!res.ok) throw httpError(data, res);
    return data;
  }

  const enc = encodeURIComponent;
  return {
    call,
    health: () => call("GET", "/health"),
    listSessions: () => call("GET", "/sessions"),
    createSession: (body = {}) => call("POST", "/sessions", body),
    // deleteSession：删整条会话（后端不代建 —— 删完进哪条由调用方定）。
    deleteSession: (id) => call("DELETE", `/sessions/${enc(id)}`),
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
    sessionState: (id) => call("GET", `/sessions/${enc(id)}/state`),
    // outgoingPreview：右载荷预演 —— 有字问 (c)（待发追尾），空字问现有历史（与空发 resend 同段）。
    // 空会话空字 ⇒ 后端 400（与 resend 同错），调用方吞掉保持空话。
    outgoingPreview: (id, content) =>
      call("POST", `/sessions/${enc(id)}/outgoing`, { content: content ?? "" }),
    // draftOutgoing：草稿预演（**还没有会话**）—— 临时会话，不落库；id 是客户端铸的
    // 草稿会话 id（建会话时交给 POST /sessions 同一个值 ⇒ 预演头 = 真发头）。
    draftOutgoing: (body) => call("POST", "/outgoing", body),
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
    // importSt：ST JSONL 原文导入（体不是 JSON，走 callText；title 走 ?title=）。
    importSt: (text, title) =>
      callText("POST", `/sessions/import-st${title ? `?title=${enc(title)}` : ""}`, text),
    providerPresets: () => call("GET", "/providers/presets"),
    createProvider: (body) => call("POST", "/providers", body),
    patchProvider: (id, body) => call("PATCH", `/providers/${enc(id)}`, body),
    // refreshProvider：拉该渠道的 /models 并落库（只写发现列，用户列不动）。
    refreshProvider: (id) => call("POST", `/providers/${enc(id)}/refresh`),
    // refreshRoutes：刷路由表（models.dev 快照 → model_route 落库），回 {provider, models, error?}。
    refreshRoutes: (id) => call("POST", `/providers/${enc(id)}/refresh-routes`),
  };
}
