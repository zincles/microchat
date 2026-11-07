// microchat web API 层 —— 与 TUI/TG 同一条 HTTP 契约（只调 /api/v1）。
// 零运行时依赖：fetch 直调。提一个 createApi 工厂，方便测试注入 fetch。
//
// 2026-11-06：从 client.js 迁到 TypeScript（阶段 2）。响应形状全在 ./types.ts；
// 这里只做 URL/方法/解析，不掺业务判断。
import type {
  Accepted,
  AdvancedJob,
  AdvancedPreview,
  AdvancedRequest,
  Agent,
  AgentsConfig,
  ApiError,
  Background,
  CancelTaskResult,
  ChatConfig,
  CompactStatus,
  ContextUsage,
  CreateAgentRequest,
  CreateProviderRequest,
  CreateSegmentRequest,
  CreateSessionRequest,
  DefaultsConfig,
  DeletionPlan,
  DraftOutgoingRequest,
  EditMessageRequest,
  EnqueueQueueRequest,
  ExportDocument,
  HealthResponse,
  ImportJsonResult,
  ImportStResult,
  LastPayload,
  MergedStream,
  Message,
  ModelListItem,
  ModelView,
  SetModelRequest,
  PayloadPreview,
  Prefs,
  PrefsView,
  PresetInfo,
  PreviewResult,
  ProviderView,
  Queue,
  RefreshRoutesResult,
  RerollAccepted,
  RerollState,
  Segment,
  Session,
  SessionStatus,
  SessionView,
  StateView,
  StopResult,
  StreamAccumulator,
  StreamCursor,
  StreamSlice,
  Summary,
  TavernPreset,
  TaskBoard,
  TestProviderRequest,
  TestProviderResult,
  TurnPoll,
  UndoPreview,
  UpdateAgentRequest,
  UpdateProviderRequest,
  UpdateSegmentRequest,
  UpdateSessionRequest,
} from "./types.ts";

export const DEFAULT_API_URL = "http://127.0.0.1:8787/api/v1";

export type FetchLike = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

// mergeStreamSlice：把一次游标读并进（正文/思考）累积器 —— 流式轮询的唯一合并规则。
// `slice.next < cur.from` = 游标回退（后端缓冲被新一轮重置）⇒ **清零重建**：
// 返回空累积 + 游标归零，下一拍从 0 读回新缓冲的全部内容（不是"接着往后追加"——
// 那样会把旧一轮的尾巴接在新一轮前面，且永远错过新缓冲开头）。
// 正常情况：追加本段、游标推进到 slice.next/think_next。
export function mergeStreamSlice(
  acc: StreamAccumulator,
  cur: StreamCursor,
  slice: StreamSlice,
): MergedStream {
  const reset = slice.next < cur.from || slice.think_next < cur.thinkFrom;
  if (reset) return { text: "", reasoning: "", cur: { from: 0, thinkFrom: 0 }, reset: true };
  // **同一个游标读两次**（网络重试 / 迟到的重复响应）不许把同一段再追加一遍。
  if (slice.next === cur.from && slice.think_next === cur.thinkFrom) {
    return { text: acc.text, reasoning: acc.reasoning, cur, reset: false };
  }
  return {
    text: acc.text + (slice.text ?? ""),
    reasoning: acc.reasoning + (slice.thinking ?? ""),
    cur: { from: slice.next, thinkFrom: slice.think_next },
    reset: false,
  };
}

// readJson：2xx/错误体都当 JSON 读；读不出来（如框架 405 的 text/plain）不炸成 SyntaxError ——
// 错误面给 null（由 httpError 回退 `HTTP <status>`），成功面照旧原样抛（成功体必是 JSON，不许瞒）。
async function readJson(res: Response): Promise<unknown> {
  try {
    return await res.json();
  } catch (e) {
    if (res.ok) throw e;
    return null;
  }
}

// normalizeApiBase：把用户输入的地址收拾成标准 API 基址 ——
// 去空白/尾斜杠；没写协议补 http://；没写 /api/v1 后缀就补上（"只填到端口"也能用）。
export function normalizeApiBase(v?: string | null): string {
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
export async function probeHealth(
  base: string,
  { fetchFn = fetch, timeout = 8000 }: { fetchFn?: FetchLike; timeout?: number } = {},
): Promise<HealthResponse> {
  // 8 秒上限：黑洞地址（DROP / 路由不可达）上 fetch 可能挂很久，启动不该被它按住。
  const opts: RequestInit = {};
  if (typeof AbortSignal !== "undefined" && typeof AbortSignal.timeout === "function") {
    opts.signal = AbortSignal.timeout(timeout);
  }
  const res = await fetchFn(`${normalizeApiBase(base)}/health`, opts);
  let data: unknown = null;
  try {
    data = await res.json();
  } catch {
    /* 非 JSON 体 */
  }
  if (!res.ok) throw httpError(data, res);
  if (
    data === null ||
    typeof data !== "object" ||
    !("status" in data) ||
    data.status !== "ok"
  ) {
    throw new Error("目标回的不是 microchat 后端（/health 没有 status:ok）");
  }
  return data as unknown as HealthResponse; // {status, version}
}

// httpError：非 2xx 的统一定形 —— message = 后端文案，**code 原样带上**（调用方按 code 分支，
// 别猜文案；没错误体时 code=null、message 回退 `HTTP <status>`）。
function httpError(data: unknown, res: Response): ApiError {
  const record: Record<string, unknown> =
    data !== null && typeof data === "object" ? (data as Record<string, unknown>) : {};
  const rawError = record.error;
  const error: Record<string, unknown> =
    rawError !== null && typeof rawError === "object"
      ? (rawError as Record<string, unknown>)
      : {};
  const message = typeof error.message === "string" ? error.message : `HTTP ${res.status}`;
  const e = new Error(message) as ApiError;
  e.code = typeof error.code === "string" ? error.code : null;
  e.status = res.status;
  return e;
}

export interface CreateApiOptions {
  base: string;
  token?: string;
  fetchFn?: FetchLike;
}

export function createApi({ base, token = "", fetchFn = fetch }: CreateApiOptions) {
  // GET 的超时与一次网络重试（2026-11-05，高延迟/弱网）：
  //   · 本地/反代都可能在链路上"挂住"而不是报错 —— 不设上限时，进会话会永远停在骨架；
  //   · GET 是幂等的，网络层失败（不是超时）重试一次很划算；超时**不**重试（那等于把等待翻倍）。
  // POST/其它方法可能触发上游长活（刷新模型、导入），不套这个上限。
  const GET_TIMEOUT_MS = 20000;

  function callInit(method: string, body: unknown, opts: RequestInit): RequestInit {
    return {
      method,
      headers: {
        "Content-Type": "application/json",
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
      ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
      ...opts,
    };
  }

  async function call<T = unknown>(
    method: string,
    path: string,
    body?: unknown,
    opts: RequestInit = {},
  ): Promise<T> {
    const init = callInit(method, body, opts);
    if (
      method === "GET" &&
      !init.signal &&
      typeof AbortSignal !== "undefined" &&
      typeof AbortSignal.timeout === "function"
    ) {
      init.signal = AbortSignal.timeout(GET_TIMEOUT_MS);
    }
    let res: Response;
    try {
      res = await fetchFn(base + path, init);
    } catch (err) {
      const aborted =
        typeof err === "object" && err !== null && "name" in err && err.name === "AbortError";
      if (method !== "GET" || aborted) throw err;
      await new Promise((resolve) => setTimeout(resolve, 300));
      res = await fetchFn(base + path, init);
    }
    if (res.status === 204) return null as T;
    const data = await readJson(res);
    if (!res.ok) throw httpError(data, res);
    return data as T;
  }

  // callBytes：**原始字节** PUT（背景图专用）—— 体是一张图的字节流，
  // 走 JSON.stringify 会把它毁掉。`Content-Type` 交给浏览器按 blob 的类型给。
  async function callBytes<T>(method: string, path: string, blob: Blob): Promise<T> {
    const res = await fetchFn(base + path, {
      method,
      headers: { "Content-Type": blob.type || "application/octet-stream" },
      body: blob,
    });
    if (res.status === 204) return null as T;
    const data = await readJson(res);
    if (!res.ok) throw httpError(data, res);
    return data as T;
  }

  // callText：原文 POST（JSONL 导入口：体不是 JSON，不能走 JSON.stringify）。
  async function callText<T>(method: string, path: string, text: string): Promise<T> {
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
    return data as T;
  }

  const enc = encodeURIComponent;
  return {
    call,
    listSessions: () => call<SessionView[]>("GET", "/sessions"),
    createSession: (body: CreateSessionRequest = {}) => call<Session>("POST", "/sessions", body),
    // deleteSession：删整条会话（后端不代建 —— 删完进哪条由调用方定）。
    deleteSession: (id: string) => call<null>("DELETE", `/sessions/${enc(id)}`),
    listMessages: (id: string) => call<Message[]>("GET", `/sessions/${enc(id)}/messages`),
    // 消息窗口用（2026-10-10）：闭区间 / 只给起点（到末尾）/ 取尾 N 条 —— 都走 /messages 现成参数。
    listMessagesRange: (id: string, fromIdx: number, toIdx: number) =>
      call<Message[]>("GET", `/sessions/${enc(id)}/messages?from_idx=${fromIdx}&to_idx=${toIdx}`),
    listMessagesTail: (id: string, fromIdx: number) =>
      call<Message[]>("GET", `/sessions/${enc(id)}/messages?from_idx=${fromIdx}`),
    listMessagesLast: (id: string, n: number) =>
      call<Message[]>("GET", `/sessions/${enc(id)}/messages?last=${n}`),
    // messageRanges：一次取多个闭区间（to=0 = 到末尾）；把摘要切开的几段压成一发。
    messageRanges: (id: string, ranges: Array<[number, number]>) =>
      call<Message[]>("POST", `/sessions/${enc(id)}/messages/batch`, { ranges }),
    // sendMessage：发一句话（202 受理 + 后台跑；预演调的不是这条，别混）。
    sendMessage: (id: string, content: string) =>
      call<Accepted>("POST", `/sessions/${enc(id)}/messages`, { content }),
    // resend：重发历史（不落新消息，拿现有历史再跑一轮；空会话 400）。
    resend: (id: string) => call<Accepted>("POST", `/sessions/${enc(id)}/resend`),
    // editMessage：改任意一条消息的正文 / 思考（至少给一个；nil=不动、""=清掉）。
    editMessage: (sid: string, mid: string, body: EditMessageRequest) =>
      call<Message>("PATCH", `/sessions/${enc(sid)}/messages/${enc(mid)}`, body),
    // editSummary：手改任意一条摘要的正文（就地换 text；空 ⇒ 400）。
    editSummary: (sid: string, sumid: string, text: string) =>
      call<Summary>("PATCH", `/sessions/${enc(sid)}/summaries/${enc(sumid)}`, { text }),
    // listSummaries：这条会话的摘要列表（/editsum 的 picker 用；id + text + dirty 现成）。
    // 2026-11-01 起还带两格**派生**字段：`level`（层级）与 `undoable`（现在能不能撤销）。
    listSummaries: (sid: string) =>
      call<Summary[]>("GET", `/sessions/${enc(sid)}/summaries`),
    // undoPreview：撤销前先问"会连带删哪些"（更早的同层压缩会级联）。
    undoPreview: (sid: string, sumid: string) =>
      call<UndoPreview>("GET", `/sessions/${enc(sid)}/summaries/${enc(sumid)}/undo-preview`),
    // undoSummary：**撤销一次压缩**。顶层都可撤；更早的会级联同层更晚的。
    undoSummary: (sid: string, sumid: string) =>
      call<null>("DELETE", `/sessions/${enc(sid)}/summaries/${enc(sumid)}`),
    patchSession: (id: string, body: UpdateSessionRequest) =>
      call<Session>("PATCH", `/sessions/${enc(id)}`, body),
    // copySession：**创建副本**（入口在左栏会话的「选项」浮板，2026-11-04 从会话设置面板挪过来）。
    // 新会话 + 消息与摘要一并复制（摘要新 id、指针重映射）；**世界状态不复制**（它本来就从正文现演）。
    copySession: (id: string) => call<Session>("POST", `/sessions/${enc(id)}/copy`),
    deleteProvider: (id: string) => call<null>("DELETE", `/providers/${enc(id)}`),
    // 酒馆：原始 JSON 只存字节，列表/删除走 JSON；上传/下载走原始 body（不 JSON 重写）。
    listTavernPresets: () => call<TavernPreset[]>("GET", "/tavern/presets"),
    uploadTavernPreset: (file: File) => {
      const name = file.name || "preset.json";
      return callBytes<TavernPreset>("POST", `/tavern/presets?name=${enc(name)}`, file);
    },
    deleteTavernPreset: (id: string) => call<null>("DELETE", `/tavern/presets/${enc(id)}`),
    downloadTavernPreset: async (id: string): Promise<Blob> => {
      const res = await fetchFn(`${base}/tavern/presets/${enc(id)}`);
      if (!res.ok) {
        const data = await readJson(res);
        throw httpError(data, res);
      }
      return res.blob();
    },
    status: (id: string) => call<SessionStatus>("GET", `/sessions/${enc(id)}/status`),
    turnText: (id: string, from: number, thinkFrom: number) =>
      call<StreamSlice>("GET", `/sessions/${enc(id)}/turn/text?from=${from}&think_from=${thinkFrom}`),
    // turnPoll：status + 增量一发拿（2026-11-03，远程 500–1000ms 时少一个完整 RTT）。
    turnPoll: (id: string, from: number, thinkFrom: number) =>
      call<TurnPoll>("GET", `/sessions/${enc(id)}/turn/poll?from=${from}&think_from=${thinkFrom}`),
    // turnEventsUrl：SSE 端点（原生 EventSource 用；不能带自定义头，所以这里是完整 URL）。
    turnEventsUrl: (id: string, from: number, thinkFrom: number) =>
      `${base}/sessions/${enc(id)}/turn/events?from=${from}&think_from=${thinkFrom}`,
    context: (id: string) => call<ContextUsage>("GET", `/sessions/${enc(id)}/context`),
    sessionState: (id: string) => call<StateView>("GET", `/sessions/${enc(id)}/state`),
    // outgoingPreview：右载荷预演 —— 有字问 (c)（待发追尾），空字问现有历史（与空发 resend 同段）。
    // 空会话空字 ⇒ 后端 400（与 resend 同错），调用方吞掉保持空话。
    outgoingPreview: (id: string, content?: string) =>
      call<LastPayload>("POST", `/sessions/${enc(id)}/outgoing`, { content: content ?? "" }),
    // draftOutgoing：草稿预演（**还没有会话**）—— 临时会话，不落库；id 是客户端铸的
    // 草稿会话 id（建会话时交给 POST /sessions 同一个值 ⇒ 预演头 = 真发头）。
    draftOutgoing: (body: DraftOutgoingRequest) => call<LastPayload>("POST", "/outgoing", body),
    compact: (id: string, blocks?: number) =>
      call<CompactStatus>("POST", `/sessions/${enc(id)}/compact`, blocks ? { blocks } : {}),
    // compactPayload：压缩那一发**真会发出去什么**（只算不发）—— 与真压同一段拼装，
    // 所以它返回的就是 `LastPayload` 形状（和 outgoingPreview 同一个渲染器能画）。
    compactPayload: (id: string, blocks?: number) =>
      call<PayloadPreview>("POST", `/sessions/${enc(id)}/compact/payload`, blocks ? { blocks } : {}),
    // 高级压缩 / 修洞：预览与运行共用 AdvancedRequest；repair=true 时扫空洞。
    advancedCompactPreview: (id: string, body: AdvancedRequest) =>
      call<AdvancedPreview>("POST", `/sessions/${enc(id)}/compact/advanced/preview`, body),
    advancedCompact: (id: string, body: AdvancedRequest) =>
      call<AdvancedJob>("POST", `/sessions/${enc(id)}/compact/advanced`, body),
    advancedCompactStatus: (id: string, jobId: string) =>
      call<AdvancedJob>("GET", `/sessions/${enc(id)}/compact/advanced/${enc(jobId)}`),
    advancedCompactCancel: (id: string, jobId: string) =>
      call<AdvancedJob>("POST", `/sessions/${enc(id)}/compact/advanced/${enc(jobId)}/cancel`, {}),
    stop: (id: string) => call<StopResult>("POST", `/sessions/${enc(id)}/stop`, {}),
    rerollEnter: (id: string) =>
      call<RerollAccepted>("POST", `/sessions/${enc(id)}/reroll-message`, {}),
    rerollState: (id: string) => call<RerollState>("GET", `/sessions/${enc(id)}/reroll-message`),
    rerollSwitch: (id: string, idx: number) =>
      call<RerollState>("POST", `/sessions/${enc(id)}/reroll-message/switch`, { idx }),
    // 摘要重摇家族（形状一一对应）：enter 的 idx 是 **1-based 消息序号**（上溯到根摘要）。
    rerollSummaryEnter: (id: string, idx: number) =>
      call<RerollAccepted>("POST", `/sessions/${enc(id)}/reroll-summary`, { idx }),
    rerollSummaryState: (id: string) =>
      call<RerollState>("GET", `/sessions/${enc(id)}/reroll-summary`),
    rerollSummarySwitch: (id: string, idx: number) =>
      call<RerollState>("POST", `/sessions/${enc(id)}/reroll-summary/switch`, { idx }),
    deletionPreview: (sid: string, mid: string) =>
      call<DeletionPlan>("GET", `/sessions/${enc(sid)}/messages/${enc(mid)}/deletion-preview`),
    deleteMessages: (sid: string, mid: string, lastDeletedMessageID: string) =>
      call<DeletionPlan>("DELETE", `/sessions/${enc(sid)}/messages/${enc(mid)}`, {
        last_deleted_message_id: lastDeletedMessageID,
      }),
    // tasks：进程内任务面板（后台有多少在跑、都是什么）。
    tasks: () => call<TaskBoard>("GET", "/tasks"),
    // enqueueQueue：把一串原子任务排成**一个队列**（2026-11-01）—— 串行跑，一步失败整个结束。
    // body 两种形状二选一：`{steps:[{session_id,blocks}…]}` 或 `{session_id,blocks,times}`。
    enqueueQueue: (body: EnqueueQueueRequest) => call<Queue>("POST", "/tasks/queues", body),
    // cancelQueue：随时取消；在飞的那一发会被一起掐断（压缩要整段才落库 ⇒ 不留半条）。
    cancelQueue: (queueId: string) =>
      call<Queue>("POST", `/tasks/queues/${enc(queueId)}/cancel`),
    // cancelTask：按停**一个**任务（2026-11-01）。回 `{canceled}` —— **false 不是错误**
    // （没这个 id / 已经收尾 / 它压根不能停，三种都是"没什么可停的"）。
    cancelTask: (id: string) => call<CancelTaskResult>("POST", `/tasks/${enc(id)}/cancel`),

    // —— 界面偏好（2026-11-01 起住服务器，不再住浏览器 localStorage）——
    getPrefs: () => call<PrefsView>("GET", "/config/prefs"),
    // putPrefs：**整份**替换（偏好是一组独立开关，客户端手里就有全量；补丁语义只会多出歧义）。
    putPrefs: (prefs: Prefs, opts?: RequestInit) =>
      call<PrefsView>("PUT", "/config/prefs", prefs, opts),
    // 自定义背景图：原始字节进出。`backgroundUrl` 拼的是**给 CSS 用**的地址
    // （带 `v=` 破缓存 —— 图换了但路径不变，只有版本号能让浏览器重取）。
    backgroundUrl: (version?: string) =>
      `${base}/config/prefs/background${version ? `?v=${enc(version)}` : ""}`,
    uploadBackground: (blob: Blob) => callBytes<Background>("PUT", "/config/prefs/background", blob),
    deleteBackground: () => call<null>("DELETE", "/config/prefs/background"),

    getChat: () => call<ChatConfig>("GET", "/config/chat"),
    putChat: (body: ChatConfig) => call<ChatConfig>("PUT", "/config/chat", body),
    // models：跨 provider 拍平（`{provider, upstream_id, name}` —— 下拉选项就打它）。
    models: () => call<ModelListItem[]>("GET", "/models"),
    // 模型用户列（显示名 / 上下文覆盖 / 参数）。刷新模型时后端永不动这三格。
    setModel: (body: SetModelRequest) => call<ModelView>("PATCH", "/models", body),
    agents: () => call<AgentsConfig>("GET", "/agents"),
    // 对话记录导出/导入（JSON，2026-10-11）：只带 role+content+标题（不含 system prompt / agent）。
    // 导出 = 会话域（owner 自己就能导）；导入 = 设置面（admin，与 ST 导入一致）。
    exportSession: (id: string) => call<ExportDocument>("GET", `/sessions/${enc(id)}/export`),
    importJson: (doc: unknown) => call<ImportJsonResult>("POST", "/sessions/import-json", doc),
    // 提示词段（system prompt segment）：读口随 `GET /agents`（段库就在那份里）；写口三个（admin）。
    createSegment: (body: CreateSegmentRequest) => call<Segment>("POST", "/segments", body),
    patchSegment: (id: string, body: UpdateSegmentRequest) =>
      call<Segment>("PATCH", `/segments/${enc(id)}`, body),
    deleteSegment: (id: string) => call<null>("DELETE", `/segments/${enc(id)}`),
    createAgent: (body: CreateAgentRequest) => call<Agent>("POST", "/agents", body),
    patchAgent: (id: string, body: UpdateAgentRequest) =>
      call<Agent>("PATCH", `/agents/${enc(id)}`, body),
    deleteAgent: (id: string) => call<null>("DELETE", `/agents/${enc(id)}`),
    getDefaults: () => call<DefaultsConfig>("GET", "/config/defaults"),
    putDefaults: (body: Partial<DefaultsConfig>) =>
      call<DefaultsConfig>("PUT", "/config/defaults", body),
    providers: () => call<ProviderView[]>("GET", "/providers"),
    // importSt：ST JSONL 原文导入（体不是 JSON，走 callText；title 走 ?title=）。
    importSt: (text: string, title?: string) =>
      callText<ImportStResult>(
        "POST",
        `/sessions/import-st${title ? `?title=${enc(title)}` : ""}`,
        text,
      ),
    providerPresets: () => call<PresetInfo[]>("GET", "/providers/presets"),
    createProvider: (body: CreateProviderRequest) =>
      call<ProviderView>("POST", "/providers", body),
    patchProvider: (id: string, body: UpdateProviderRequest) =>
      call<ProviderView>("PATCH", `/providers/${enc(id)}`, body),
    // refreshProvider：拉该渠道的 /models 并落库（只写发现列，用户列不动）。
    refreshProvider: (id: string) =>
      call<ProviderView>("POST", `/providers/${enc(id)}/refresh`),
    // testProvider：只拉一次 /models 测连通性（不落库、不保存）；body 可带表单覆盖值。
    testProvider: (id: string, body: TestProviderRequest = {}) =>
      call<TestProviderResult>("POST", `/providers/${enc(id)}/test`, body),
    // refreshRoutes：刷路由表（models.dev 快照 → model_route 落库），回 {provider, models, error?}。
    refreshRoutes: (id: string) =>
      call<RefreshRoutesResult>("POST", `/providers/${enc(id)}/refresh-routes`),
  };
}

