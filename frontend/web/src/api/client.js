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

// normalizeApiBase：把用户输入的地址收拾成标准 API 基址 ——
// 去空白/尾斜杠；没写协议补 http://；没写 /api/v1 后缀就补上（"只填到端口"也能用）。
export function normalizeApiBase(v) {
  let s = (v ?? "").trim().replace(/\/+$/, "");
  if (!s) return "";
  if (!/^https?:\/\//i.test(s)) s = `http://${s}`;
  if (!/\/api\/v1$/.test(s)) s += "/api/v1";
  return s;
}

// probeHealth：启动自检 —— "这个地址后面是不是一个活着的 microchat 后端"。
//
// 2026-11-01：鉴权砍了，原来的 `/auth/status` 也没了 ⇒ 换成 `/health`（它本来就要鉴权，现在不用了）。
// 两道闸照旧（都是踩过的坑）：地址先 `normalizeApiBase`（裸地址会被 fetch 当相对路径、
// 被 vite 的 200 HTML 骗过）；回体必须是 JSON 且 `status:"ok"`（挡住"随便一个 web 服务回 200"）。
export async function probeHealth(base, { fetchFn = fetch } = {}) {
  const res = await fetchFn(`${normalizeApiBase(base)}/health`);
  let data = null;
  try { data = await res.json(); } catch { /* 非 JSON 体 */ }
  if (!res.ok) throw httpError(data, res);
  if (!data || data.status !== "ok") throw new Error("目标回的不是 microchat 后端（/health 没有 status:ok）");
  return data; // {status, version}
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

  // callBytes：**原始字节** PUT（背景图专用）—— 体是一张图的字节流，
  // 走 JSON.stringify 会把它毁掉。`Content-Type` 交给浏览器按 blob 的类型给。
  async function callBytes(method, path, blob) {
    const res = await fetchFn(base + path, {
      method,
      headers: { "Content-Type": blob.type || "application/octet-stream" },
      body: blob,
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
    listSessions: () => call("GET", "/sessions"),
    createSession: (body = {}) => call("POST", "/sessions", body),
    // deleteSession：删整条会话（后端不代建 —— 删完进哪条由调用方定）。
    deleteSession: (id) => call("DELETE", `/sessions/${enc(id)}`),
    listMessages: (id) => call("GET", `/sessions/${enc(id)}/messages`),
    // 消息窗口用（2026-10-10）：闭区间 / 只给起点（到末尾）/ 取尾 N 条 —— 都走 /messages 现成参数。
    listMessagesRange: (id, fromIdx, toIdx) =>
      call("GET", `/sessions/${enc(id)}/messages?from_idx=${fromIdx}&to_idx=${toIdx}`),
    listMessagesTail: (id, fromIdx) =>
      call("GET", `/sessions/${enc(id)}/messages?from_idx=${fromIdx}`),
    listMessagesLast: (id, n) => call("GET", `/sessions/${enc(id)}/messages?last=${n}`),
    // sendMessage：发一句话（202 受理 + 后台跑；预演调的不是这条，别混）。
    sendMessage: (id, content) => call("POST", `/sessions/${enc(id)}/messages`, { content }),
    // resend：重发历史（不落新消息，拿现有历史再跑一轮；空会话 400）。
    resend: (id) => call("POST", `/sessions/${enc(id)}/resend`),
    // editMessage：改任意一条消息的正文 / 思考（至少给一个；nil=不动、""=清掉）。
    editMessage: (sid, mid, body) => call("PATCH", `/sessions/${enc(sid)}/messages/${enc(mid)}`, body),
    // editSummary：手改任意一条摘要的正文（就地换 text；空 ⇒ 400）。
    editSummary: (sid, sumid, text) => call("PATCH", `/sessions/${enc(sid)}/summaries/${enc(sumid)}`, { text }),
    // listSummaries：这条会话的摘要列表（/editsum 的 picker 用；id + text + dirty 现成）。
    // 2026-11-01 起还带两格**派生**字段：`level`（层级）与 `undoable`（现在能不能撤销）。
    listSummaries: (sid) => call("GET", `/sessions/${enc(sid)}/summaries`),
    // undoSummary：**撤销一次压缩**。只允许撤合法的（顶层 + 每层最近那次）——
    // 判定在后端（`compact.PlanUndo`），不合法回 409 并带一句人话，界面请原样显示。
    undoSummary: (sid, sumid) => call("DELETE", `/sessions/${enc(sid)}/summaries/${enc(sumid)}`),
    patchSession: (id, body) => call("PATCH", `/sessions/${enc(id)}`, body),
    // copySession：**创建副本**（2026-11-01 补的界面 —— 后端 `POST /copy` 早就有，web 一直没入口）。
    // 新会话 + 消息与摘要一并复制（摘要新 id、指针重映射）；**世界状态不复制**（它本来就从正文现演）。
    copySession: (id) => call("POST", `/sessions/${enc(id)}/copy`),
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
    // compactPayload：压缩那一发**真会发出去什么**（只算不发）—— 与真压同一段拼装，
    // 所以它返回的就是 `LastPayload` 形状（和 outgoingPreview 同一个渲染器能画）。
    compactPayload: (id, blocks) =>
      call("POST", `/sessions/${enc(id)}/compact/payload`, blocks ? { blocks } : {}),
    stop: (id) => call("POST", `/sessions/${enc(id)}/stop`, {}),
    rerollEnter: (id) => call("POST", `/sessions/${enc(id)}/reroll-message`, {}),
    rerollState: (id) => call("GET", `/sessions/${enc(id)}/reroll-message`),
    rerollSwitch: (id, idx) => call("POST", `/sessions/${enc(id)}/reroll-message/switch`, { idx }),
    // 摘要重摇家族（形状一一对应）：enter 的 idx 是 **1-based 消息序号**（上溯到根摘要）。
    rerollSummaryEnter: (id, idx) => call("POST", `/sessions/${enc(id)}/reroll-summary`, { idx }),
    rerollSummaryState: (id) => call("GET", `/sessions/${enc(id)}/reroll-summary`),
    rerollSummarySwitch: (id, idx) => call("POST", `/sessions/${enc(id)}/reroll-summary/switch`, { idx }),
    deletionPreview: (sid, mid) =>
      call("GET", `/sessions/${enc(sid)}/messages/${enc(mid)}/deletion-preview`),
    deleteMessages: (sid, mid, lastDeletedMessageID) =>
      call("DELETE", `/sessions/${enc(sid)}/messages/${enc(mid)}`, {
        last_deleted_message_id: lastDeletedMessageID,
      }),
    // tasks：进程内任务面板（后台有多少在跑、都是什么）。
    tasks: () => call("GET", "/tasks"),
    // enqueueQueue：把一串原子任务排成**一个队列**（2026-11-01）—— 串行跑，一步失败整个结束。
    // body 两种形状二选一：`{steps:[{session_id,blocks}…]}` 或 `{session_id,blocks,times}`。
    enqueueQueue: (body) => call("POST", "/tasks/queues", body),
    // cancelQueue：随时取消；在飞的那一发会被一起掐断（压缩要整段才落库 ⇒ 不留半条）。
    cancelQueue: (queueId) => call("POST", `/tasks/queues/${enc(queueId)}/cancel`),
    // cancelTask：按停**一个**任务（2026-11-01）。回 `{canceled}` —— **false 不是错误**
    // （没这个 id / 已经收尾 / 它压根不能停，三种都是"没什么可停的"）。
    cancelTask: (id) => call("POST", `/tasks/${enc(id)}/cancel`),

    // —— 界面偏好（2026-11-01 起住服务器，不再住浏览器 localStorage）——
    getPrefs: () => call("GET", "/config/prefs"),
    // putPrefs：**整份**替换（偏好是一组独立开关，客户端手里就有全量；补丁语义只会多出歧义）。
    putPrefs: (prefs) => call("PUT", "/config/prefs", prefs),
    // 自定义背景图：原始字节进出。`backgroundUrl` 拼的是**给 CSS 用**的地址
    // （带 `v=` 破缓存 —— 图换了但路径不变，只有版本号能让浏览器重取）。
    backgroundUrl: (version) => `${base}/config/prefs/background${version ? `?v=${enc(version)}` : ""}`,
    uploadBackground: (blob) => callBytes("PUT", "/config/prefs/background", blob),
    deleteBackground: () => call("DELETE", "/config/prefs/background"),

    getChat: () => call("GET", "/config/chat"),
    putChat: (body) => call("PUT", "/config/chat", body),
    // models：跨 provider 拍平（`{provider, upstream_id, name}` —— 下拉选项就打它）。
    models: () => call("GET", "/models"),
    agents: () => call("GET", "/agents"),
    // 对话记录导出/导入（JSON，2026-10-11）：只带 role+content+标题（不含 system prompt / agent）。
    // 导出 = 会话域（owner 自己就能导）；导入 = 设置面（admin，与 ST 导入一致）。
    exportSession: (id) => call("GET", `/sessions/${enc(id)}/export`),
    importJson: (doc) => call("POST", "/sessions/import-json", doc),
    // 提示词段（system prompt segment）：读口随 `GET /agents`（段库就在那份里）；写口三个（admin）。
    createSegment: (body) => call("POST", "/segments", body),
    patchSegment: (id, body) => call("PATCH", `/segments/${enc(id)}`, body),
    deleteSegment: (id) => call("DELETE", `/segments/${enc(id)}`),
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
