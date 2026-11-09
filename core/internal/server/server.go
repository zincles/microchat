// Package server：HTTP 层。**接口形状照搬 AGENTS.md 那张表**（49 条），一条条搬。
//
// 全部挂在 /api/v1 下；错误体固定 {"error":{"code","message"}}；配了口令就全都要 Bearer。
package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"microchat/internal/chat"
	"microchat/internal/compact"
	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/providers"
	"microchat/internal/queue"
	"microchat/internal/reroll"
	"microchat/internal/store"
	"microchat/internal/task"

	"github.com/google/uuid"
)

type Server struct {
	store   *store.Store
	config  config.Config
	paths   config.Paths
	chat    *chat.Service
	compact *compact.Service
	rerolls *reroll.Service
	tasks   *task.Registry
	queues  *queue.Service // 队列任务（2026-11-01）：串行、一步失败即整体结束、可取消
	mux     *http.ServeMux
}

// New：把"一轮生成"(chat)、"压缩"(compact) 与"重摇"(reroll) 三层注进来 —— 它们才是干活的那层
// （server 只编排：解析请求、定错误码、写响应）。
func New(st *store.Store, cfg config.Config, paths config.Paths, chatService *chat.Service,
	compactService *compact.Service, rerollService *reroll.Service, tasks *task.Registry,
	queues *queue.Service) *Server {
	s := &Server{store: st, config: cfg, paths: paths, chat: chatService, compact: compactService,
		rerolls: rerollService, tasks: tasks, queues: queues, mux: http.NewServeMux()}
	// Go 1.22+ 的 ServeMux 原生支持 `GET /x/{id}` 这种模式 —— 连路由库都不需要。
	s.mux.HandleFunc("GET /api/v1/health", s.health)
	// 版本更新（2026-11-09）：左栏「更新」入口 —— 读当前提交、拉取并请求重启。
	s.mux.HandleFunc("GET /api/v1/update", s.updateStatus)
	s.mux.HandleFunc("POST /api/v1/update/pull", s.updatePull)
	s.mux.HandleFunc("POST /api/v1/update/rebase", s.updateRebase)
	// 鉴权（DEFINE「用户与鉴权」）：status/login/register 是**仅有的三处开放口**
	// （register 另有"还没有用户"的闸）；其余一切都要有效凭据。
	s.mux.HandleFunc("GET /api/v1/sessions", s.listSessions)
	s.mux.HandleFunc("POST /api/v1/sessions", s.createSession)
	s.mux.HandleFunc("POST /api/v1/sessions/import-st", s.importSt)
	s.mux.HandleFunc("POST /api/v1/sessions/import-json", s.importJSON)
	s.mux.HandleFunc("PATCH /api/v1/sessions/{session_id}", s.updateSession)
	s.mux.HandleFunc("DELETE /api/v1/sessions/{session_id}", s.deleteSession)
	// Copy：线性会话里的"分岔"（新 session + 消息与摘要一并复制）
	s.mux.HandleFunc("POST /api/v1/sessions/{session_id}/copy", s.copySession)
	// 对话记录导出（JSON）：**会话域**（owner 自己就能导）；导入在设置面（import-json，admin）。
	s.mux.HandleFunc("GET /api/v1/sessions/{session_id}/export", s.exportSession)
	// provider / 模型 / 渠道（registry 那一层）
	s.mux.HandleFunc("GET /api/v1/providers", s.listProviders)
	// 内建预设清单（字面段优先于 {id}，Go 的 ServeMux 自己保证）
	s.mux.HandleFunc("GET /api/v1/providers/presets", s.listProviderPresets)
	s.mux.HandleFunc("POST /api/v1/providers", s.createProvider)
	s.mux.HandleFunc("PATCH /api/v1/providers/{provider_id}", s.updateProvider)
	s.mux.HandleFunc("DELETE /api/v1/providers/{provider_id}", s.deleteProvider)
	s.mux.HandleFunc("POST /api/v1/providers/{provider_id}/refresh", s.refreshProvider)
	// 测试连通性：只拉一次 `/models`，**不落库、不改配置**（编辑器里的「测试连通性」）。
	s.mux.HandleFunc("POST /api/v1/providers/{provider_id}/test", s.testProvider)
	// 路由表刷新（models.dev 快照 → `model_route`）：设置 Provider 面板用（看缓存/手刷）。
	s.mux.HandleFunc("POST /api/v1/providers/{provider_id}/refresh-routes", s.refreshProviderRoutes)
	// 套餐余量（只读）：**只对 kind=opencode-go 有义**，别的 kind 400 说清楚（见 usage_api.go）
	s.mux.HandleFunc("GET /api/v1/providers/{provider_id}/usage", s.providerUsage)
	s.mux.HandleFunc("GET /api/v1/agents", s.listAgents)
	s.mux.HandleFunc("POST /api/v1/agents", s.createAgent)
	s.mux.HandleFunc("PATCH /api/v1/agents/{agent_id}", s.updateAgent)
	s.mux.HandleFunc("DELETE /api/v1/agents/{agent_id}", s.deleteAgent)
	// 提示词段（system prompt segment）：读口随 `GET /agents`（那份就是 AgentsConfig 全貌）；
	// 这里只有三个写口（设置面 = admin）。删除有守卫：还被引用 ⇒ 拒绝（列出引用者）。
	s.mux.HandleFunc("POST /api/v1/segments", s.createSegment)
	s.mux.HandleFunc("PATCH /api/v1/segments/{segment_id}", s.updateSegment)
	s.mux.HandleFunc("DELETE /api/v1/segments/{segment_id}", s.deleteSegment)
	// **界面偏好**（2026-11-01：住服务器，不再住浏览器 localStorage）
	// 酒馆（2026-11-06）：原始 JSON 存 BLOB，导出逐字节原样；解析是后续独立步骤。
	s.mux.HandleFunc("GET /api/v1/tavern/presets", s.listTavernPresets)
	s.mux.HandleFunc("POST /api/v1/tavern/presets", s.createTavernPreset)
	s.mux.HandleFunc("GET /api/v1/tavern/presets/{preset_id}", s.getTavernPreset)
	s.mux.HandleFunc("DELETE /api/v1/tavern/presets/{preset_id}", s.deleteTavernPreset)
	s.mux.HandleFunc("GET /api/v1/config/model-rules", s.getModelRules)
	s.mux.HandleFunc("PUT /api/v1/config/model-rules", s.putModelRules)
	s.mux.HandleFunc("GET /api/v1/config/prefs", s.getPrefs)
	s.mux.HandleFunc("PUT /api/v1/config/prefs", s.putPrefs)
	// 自定义背景图：原始字节进出（它是张图，不是 JSON）
	s.mux.HandleFunc("GET /api/v1/config/prefs/background", s.getBackground)
	s.mux.HandleFunc("PUT /api/v1/config/prefs/background", s.putBackground)
	s.mux.HandleFunc("DELETE /api/v1/config/prefs/background", s.deleteBackground)
	s.mux.HandleFunc("GET /api/v1/config/chat", s.getChatConfig)
	s.mux.HandleFunc("PUT /api/v1/config/chat", s.putChatConfig)
	// 新建会话的缺省三件（provider / model / agent）：`PUT /config/chat` 只管 chat 段，
	// 缺省另起一条（免得界面为改一个缺省去读整段 chat 再原样 PUT 回去 —— 两段各改各的）。
	s.mux.HandleFunc("GET /api/v1/config/defaults", s.getDefaults)
	s.mux.HandleFunc("PUT /api/v1/config/defaults", s.putDefaults)
	s.mux.HandleFunc("GET /api/v1/models", s.listModels)
	s.mux.HandleFunc("PATCH /api/v1/models", s.setModelOverride)
	// 任务面板：进程内的事实（与 `-debug tasks` 同一份 Board；轮询用，不进库）
	s.mux.HandleFunc("GET /api/v1/tasks", s.taskBoard)
	// 按停**一个**任务（2026-11-01 用户定：「任务菜单里，我希望有办法取消任务」）。
	// 按的是 **TaskID** —— 面板上只有那个 id，它够不到任务自己的 ctx，所以这条路是必需的。
	s.mux.HandleFunc("POST /api/v1/tasks/{task_id}/cancel", s.cancelTask)
	// 队列任务（2026-11-01 用户定）—— 排 / 取消，都是 admin（队列绕过逐会话归属检查）。
	s.mux.HandleFunc("POST /api/v1/tasks/queues", s.enqueueQueue)
	s.mux.HandleFunc("POST /api/v1/tasks/queues/{queue_id}/cancel", s.cancelQueue)

	// statelang：解析 + 计算（给外部工具用；不涉及会话、不落库）
	s.mux.HandleFunc("POST /api/v1/statelang", s.statelangParse)
	s.mux.HandleFunc("GET /api/v1/sessions/{session_id}/state", s.getSessionState)
	// (c) 预演：把待发那句追加进去之后真会发出去的东西 —— **只算不写**（同一个装配，重算状态）。
	// GET 那条（旧 (b) 逐项形状）已删：显式注册回 410 `gone`（不用框架默认 405 ——
	// 405 说"方法不对"，410 说"这东西没了"，后者才是真话）。
	s.mux.HandleFunc("POST /api/v1/sessions/{session_id}/outgoing", s.postSessionOutgoing)
	// 草稿预演：**还没有会话**（web 草稿态）时"这句发出去会是什么样" —— 临时种子会话走
	// `chat.PreviewWire`（同一支笔），一条会话都不建。id 给了就当 sessions.id 用（进请求头）。
	s.mux.HandleFunc("POST /api/v1/outgoing", s.postDraftOutgoing)
	// 生效的系统提示词（三级解析的**结果** + 来源）—— 调试用：客户端摆在消息区最上方
	s.mux.HandleFunc("GET /api/v1/sessions/{session_id}/prompt", s.getSessionPrompt)
	s.mux.HandleFunc("PATCH /api/v1/sessions/{session_id}/messages/{message_id}", s.editMessage)
	s.mux.HandleFunc("DELETE /api/v1/sessions/{session_id}/messages/{message_id}", s.deleteMessage)
	s.mux.HandleFunc("GET /api/v1/sessions/{session_id}/summaries", s.listSummaries)
	s.mux.HandleFunc("PATCH /api/v1/sessions/{session_id}/summaries/{summary_id}", s.editSummary)
	s.mux.HandleFunc("GET /api/v1/sessions/{session_id}/summaries/{summary_id}/undo-preview", s.previewUndoSummary)
	s.mux.HandleFunc("DELETE /api/v1/sessions/{session_id}/summaries/{summary_id}", s.undoSummary)
	// 删除预览：**只算不动**（安全 ⇒ GET）；DELETE 照同一份计算干
	s.mux.HandleFunc("GET /api/v1/sessions/{session_id}/messages/{message_id}/deletion-preview", s.deletionPreview)
	s.mux.HandleFunc("GET /api/v1/sessions/{session_id}/messages", s.listMessages)
	// 批量区间：消息列被摘要切开时一发拿全，别一个区间一个 RTT（2026-11-04）。
	s.mux.HandleFunc("POST /api/v1/sessions/{session_id}/messages/batch", s.listMessagesBatch)
	// 一轮生成：**受理与生成分开**（202 + 轮询）—— 状态 / 游标读 / 停止 / 上下文占用
	s.mux.HandleFunc("POST /api/v1/sessions/{session_id}/messages", s.sendMessage)
	// 重发历史：**不落新消息**，拿现有历史再跑一轮（尾 user 没回复 / 尾 assistant 想再要一版都行）
	s.mux.HandleFunc("POST /api/v1/sessions/{session_id}/resend", s.resendHistory)
	s.mux.HandleFunc("GET /api/v1/sessions/{session_id}/status", s.turnStatus)
	s.mux.HandleFunc("GET /api/v1/sessions/{session_id}/turn/text", s.turnText)
	s.mux.HandleFunc("GET /api/v1/sessions/{session_id}/turn/poll", s.turnPoll)
	s.mux.HandleFunc("GET /api/v1/sessions/{session_id}/turn/events", s.turnEvents)
	s.mux.HandleFunc("POST /api/v1/sessions/{session_id}/stop", s.stopTurn)
	s.mux.HandleFunc("GET /api/v1/sessions/{session_id}/context", s.sessionContext)
	s.mux.HandleFunc("POST /api/v1/sessions/{session_id}/compact", s.compactSession)
	// 压缩预览：**只算不动**（调同一套 strategySpan；算完压哪段、压完树长什么样）
	s.mux.HandleFunc("POST /api/v1/sessions/{session_id}/compact/preview", s.compactPreview)
	// 压缩载荷预演：**这一发真会发出去什么**（只算不动；与真压同一段拼装）
	s.mux.HandleFunc("POST /api/v1/sessions/{session_id}/compact/payload", s.compactPayload)
	s.mux.HandleFunc("POST /api/v1/sessions/{session_id}/compact/advanced/preview", s.advancedCompactPreview)
	s.mux.HandleFunc("POST /api/v1/sessions/{session_id}/compact/advanced", s.advancedCompactStart)
	s.mux.HandleFunc("GET /api/v1/sessions/{session_id}/compact/advanced/{job_id}", s.advancedCompactStatus)
	s.mux.HandleFunc("POST /api/v1/sessions/{session_id}/compact/advanced/{job_id}/cancel", s.advancedCompactCancel)
	// 重摇：**两个平行家族**（候选只在内存里；见 reroll_api.go）
	// 消息级：把尾条那条 assistant 回复重新摇几版，挑一版定下来
	s.mux.HandleFunc("POST /api/v1/sessions/{session_id}/reroll-message", s.rerollMessageEnter)
	s.mux.HandleFunc("GET /api/v1/sessions/{session_id}/reroll-message", s.rerollMessageState)
	s.mux.HandleFunc("POST /api/v1/sessions/{session_id}/reroll-message/switch", s.rerollMessageSwitch)
	s.mux.HandleFunc("DELETE /api/v1/sessions/{session_id}/reroll-message/{idx}", s.rerollMessageDelete)
	s.mux.HandleFunc("DELETE /api/v1/sessions/{session_id}/reroll-message", s.rerollMessageClear)
	// 摘要级：按一条已存在顶层摘要的原始材料重摇它的正文（`{"idx": N}` 上溯到根）
	s.mux.HandleFunc("POST /api/v1/sessions/{session_id}/reroll-summary", s.rerollSummaryEnter)
	s.mux.HandleFunc("GET /api/v1/sessions/{session_id}/reroll-summary", s.rerollSummaryState)
	s.mux.HandleFunc("POST /api/v1/sessions/{session_id}/reroll-summary/switch", s.rerollSummarySwitch)
	s.mux.HandleFunc("DELETE /api/v1/sessions/{session_id}/reroll-summary/{idx}", s.rerollSummaryDelete)
	s.mux.HandleFunc("DELETE /api/v1/sessions/{session_id}/reroll-summary", s.rerollSummaryClear)
	return s
}

// Handler：路由外面**套两层**，顺序至关重要 ——
//
//	CORS( 鉴权( mux ) )
//
// **CORS 必须是最外层**：浏览器发带 JSON 的 POST 前先发预检 `OPTIONS`，而预检**不带** `Authorization`
// ⇒ 要是先撞鉴权就是 401、预检失败、正式请求根本发不出去。所以 OPTIONS 在 CORS 这一层就被接管，
// 永远进不到鉴权，也进不到路由（405 那条口径不受影响：只有 `OPTIONS` 被我们截下）。
// 反过来 CORS 头要盖住**每一个**响应（200 / 401 / 404 / 405 / 错误体）⇒ CORS 必须在最外层。
// Handler：CORS 包一层就够了 —— 2026-11-01 起**没有鉴权**（只听 127.0.0.1，见 AGENTS.md）。
func (s *Server) Handler() http.Handler { return withCORS(s.mux) }

// withCORS：给每个响应加 CORS 头，并把预检 `OPTIONS` 就地回成 204（无响应体）。
//
// `Access-Control-Allow-Origin` 有 `Origin` 就**回显**它、否则 `*`；回显时依赖请求头 ⇒ 一并加 `Vary: Origin`。
// `Access-Control-Expose-Headers: Allow`：405 的 `allow:` 头不在 CORS 安全名单里，不回显浏览器读不到。
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		if origin := r.Header.Get("Origin"); origin != "" {
			header.Set("Access-Control-Allow-Origin", origin)
		} else {
			header.Set("Access-Control-Allow-Origin", "*")
		}
		header.Add("Vary", "Origin")
		header.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
		if requested := r.Header.Get("Access-Control-Request-Headers"); requested != "" {
			header.Set("Access-Control-Allow-Headers", requested)
		} else {
			header.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		}
		header.Set("Access-Control-Max-Age", "600")
		header.Set("Access-Control-Expose-Headers", "Allow")
		// 预检在这里终结：204、无体，绝不落到鉴权/路由。
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// health：探针（Rust 版回 {"status","version"}）。
// health：版本号与 Rust 版**刻意对齐** —— 这个端口要取代它，形状必须一模一样。
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": "0.1.0"})
}

// listSessions：列表项 = 会话 + 这一轮的状态 + **这条会话有几条消息**（左栏据此标"生成中"，
// 客户端的启动编排据此一眼认出"空会话" —— 不必逐条会话再拉一次消息）。
//
// 状态是**进程内**的事实：后端一重启就全是 idle —— 那是诚实的（没有生成在跑）。
func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.store.ListSessions()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	counts, err := s.store.MessageCounts()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	views := make([]model.SessionView, 0, len(sessions))
	for _, session := range sessions {
		views = append(views, model.SessionView{
			Session: session, Messages: counts[session.ID], Turn: s.chat.StatusView(session.ID),
		})
	}
	writeJSON(w, http.StatusOK, views)
}

// listMessages：这条会话的消息，**按 id 升序**（线性会话 ⇒ 就是整条会话）。
//
// 三个查询参数（**都不给 = 全部**，语义不变）：
//
//	?from_idx=5&to_idx=10   闭区间（**含两端**）；缺的那端补默认（起点 ⇒ 1、终点 ⇒ 末尾）
//	?last=20                取**尾部** 20 条
//
// **参数错 ⇒ 400**：`last` 与 `from_idx`/`to_idx` 混用、非正整数、区间反了（`from_idx > to_idx`）。
// **越界不是错**：要 5..99 而只有 3 条 ⇒ 给现有的那几条（分页的常见语义）；起点在末尾之后 ⇒ 空数组。
// 响应**仍是数组**（没换成对象）—— 免得破坏现有客户端 ✗。
//
// 每条都带序号 `idx`（**派生**、不落库；`0` 是合成的系统提示词那一格，消息从 1 起）——
// 见 `model.Message.Idx` 与 `DEFINE.md` 的「各种 id」。
func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	window, ok := messageWindow(w, r)
	if !ok {
		return
	}
	messages, err := s.store.ListMessagesWindow(r.PathValue("session_id"), window)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, messages)
}

// MessageRangesReq：`{"ranges":[[1,5],[8,10]]}`；`to=0` 表示到末尾。
// 区间是 1-based **消息序号**，与 `/messages?from_idx=&to_idx=` 同一把尺。
type MessageRangesReq struct {
	Ranges [][2]int `json:"ranges"`
}

// listMessagesBatch：一次取多个消息区间（远程/高延迟下把 N 个 GET 压成一发）。
// 所有区间先验完再动库；返回仍是按 id 升序、去重后的数组。
func (s *Server) listMessagesBatch(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	var req MessageRangesReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Ranges) == 0 {
		writeJSON(w, http.StatusOK, []model.Message{})
		return
	}
	for _, span := range req.Ranges {
		if span[0] < 1 || span[1] < 0 || (span[1] > 0 && span[1] < span[0]) {
			writeError(w, http.StatusUnprocessableEntity, "invalid", "ranges 要是 [from,to] 且 from >= 1、to 为 0（到末尾）或 >= from")
			return
		}
	}
	messages, err := s.store.ListMessageRanges(r.PathValue("session_id"), req.Ranges)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, messages)
}

// messageWindow：把三个查询参数解析 + 校验成窗口（**参数层的错一律 400**，错误体固定）。// messageWindow：把三个查询参数解析 + 校验成窗口（**参数层的错一律 400**，错误体固定）。
//
// `last` 与 `from_idx`/`to_idx` **不许混用**：一个说"取尾部几条"、一个说"取第几条到第几条"，
// 同时给就是调用方没想清（宁可说清，也不猜谁赢）。
func messageWindow(w http.ResponseWriter, r *http.Request) (store.MessageWindow, bool) {
	query := r.URL.Query()
	last, hasLast, ok := queryIndex(w, query, "last")
	if !ok {
		return store.MessageWindow{}, false
	}
	from, hasFrom, ok := queryIndex(w, query, "from_idx")
	if !ok {
		return store.MessageWindow{}, false
	}
	to, hasTo, ok := queryIndex(w, query, "to_idx")
	if !ok {
		return store.MessageWindow{}, false
	}
	if hasLast && (hasFrom || hasTo) {
		writeError(w, http.StatusBadRequest, "invalid", "last 不能与 from_idx/to_idx 混用")
		return store.MessageWindow{}, false
	}
	if hasFrom && hasTo && from > to {
		writeError(w, http.StatusBadRequest, "invalid", "from_idx 不能大于 to_idx")
		return store.MessageWindow{}, false
	}
	return store.MessageWindow{Last: last, FromIdx: from, ToIdx: to}, true
}

// queryIndex：读一个**序号型**查询参数：没给 ⇒ (0, false, true)；给了但不是正整数 ⇒ 400。
func queryIndex(w http.ResponseWriter, query url.Values, name string) (value int, given bool, ok bool) {
	if !query.Has(name) {
		return 0, false, true
	}
	parsed, err := strconv.Atoi(trimSpace(query.Get(name)))
	if err != nil || parsed <= 0 {
		writeError(w, http.StatusBadRequest, "invalid", name+" 要是正整数")
		return 0, false, false
	}
	return parsed, true, true
}

// CreateSessionReq：Rust 版的 `#[serde(default)]` ⇒ 全都可以省略。
//
// `title` **真生效**（写进 `sessions.title`）—— 它是个合理的契约字段：建一条**已命名**的会话
// （如"重建后的新库"）不必再多发一次 `PATCH`。省略 / `""` ⇒ 标题空着（等首条用户消息自动起名）。
// ⚠ 在补上这一格之前，`{"title": …}` 是**被静默丢掉**的（`decodeJSON` 不认未知字段）——
// 静默忽略比报错难查得多（这条踩过）。
//
// `id`（2026-10-09 加）：**客户端铸好的 UUIDv7** —— 草稿态（web 前端）用它把"预演的那一发"
// 与"真发的那一发"钉成同一件事（`sessions.id` 会进请求头 `x-opencode-session` 那类，
// 预演的头上已经是它）。省略 ⇒ 服务端现铸（一切旧客户端照旧）。已有同 id ⇒ **409**。
//
// ⚠ **HACK（刻意，别扩散）**：这是全项目**唯一**允许客户端铸 id 的口子 —— 动机只有草稿预演这一条
// （会话还不存在时，预演的请求头上就得有那枚 id；不这样就得给后端发明"草稿"这个实体，更重）。
// 消息 / 摘要 / agent 的 id 一律服务端铸，别拿这个口子去学。代价就是下面两条校验路：
// 形状不对 ⇒ 400，撞车 ⇒ 409。
type CreateSessionReq struct {
	ID           *string `json:"id"`
	Title        *string `json:"title"`
	Provider     *string `json:"provider"`
	Model        *string `json:"model"`
	AgentID      *string `json:"agent_id"`
	SystemPrompt string  `json:"system_prompt"`
	// ThinkingLevel：统一七档（off/minimal/low/medium/high/xhigh/max）；空 = 跟随模型默认。
	ThinkingLevel *string `json:"thinking_level"`
	// NativeToolsDisabled：规则派生的 native tool 里手动关掉的 id；nil = 默认全开。
	NativeToolsDisabled *[]string `json:"native_tools_disabled"`
}

// UpdateSessionReq：只改给出来的那些。
//
// `system_prompt` 与其它字段**同一个语义**（2026-09-30 改）：指针 ⇒ 分得出"没给"与"给了空串"。
//   - `nil`（没这个键 / `null`）= **不动**；
//   - `""` = **清掉会话级覆盖**（空串 = 没覆盖 ⇒ 底子算全局，回落到 agent 的）；
//   - 非空 = 写进 `sessions.system_prompt`。
//
// ⚠ 它**不是**收下不用的字段：`GET /sessions/{session_id}/prompt` 的 `source` 与
// `/outgoing` 里第一条 system 都会跟着变（由 `chat.EffectiveSystemPrompt` 一处解析）。
type UpdateSessionReq struct {
	Title        *string `json:"title"`
	Provider     *string `json:"provider"`
	Model        *string `json:"model"`
	AgentID      *string `json:"agent_id"`
	SystemPrompt *string `json:"system_prompt"`
	// ThinkingLevel：nil = 不动；"" = 清掉覆盖（跟随模型默认）；非空 = 统一七档之一。
	ThinkingLevel *string `json:"thinking_level"`
	// NativeToolsDisabled：nil = 不动；[] = 全部恢复；非空 = 关闭这些 native tool。
	NativeToolsDisabled *[]string `json:"native_tools_disabled"`
	// RequestOverrides：nil = 不动；{} = 清空；对象 = 整份替换。允许覆盖任何 key。
	RequestOverrides *map[string]json.RawMessage `json:"request_overrides"`
}

// normalizeUUIDv7：校验 + 规范化（小写带横线）。`sessions.id` 的 v7 形状由这一处守
// （客户端铸的 id 从 `POST /sessions` 进来 —— 不守的话"会话 id 恒为 UUIDv7"这条就悄悄破了）。
func normalizeUUIDv7(raw string) (string, bool) {
	parsed, err := uuid.Parse(raw)
	if err != nil || parsed.Version() != 7 {
		return "", false
	}
	return parsed.String(), true
}

// newSessionSeed：草稿/新会话的"种子会话" —— **没进库的一行**。
// provider/model 省略 ⇒ config defaults（**每次现读**：`PUT /config/defaults` 改完当场生效）；
// agent 空白 ⇒ `agents.json` 的 default_agent。
// **建会话（createSession）与草稿预演（postDraftOutgoing）共用这一处** —— 否则
// "预演说走 A 渠道、真建出来却是 B"这类漂移没有一处能对账。
func (s *Server) newSessionSeed(id string, provider, modelID, agentID *string) (model.Session, error) {
	cfg, err := config.LoadConfig(s.paths)
	if err != nil {
		return model.Session{}, err
	}
	p, m := cfg.Defaults.Provider, cfg.Defaults.Model
	if provider != nil {
		p = *provider
	}
	if modelID != nil {
		m = *modelID
	}
	a := ""
	if agentID != nil {
		a = strings.TrimSpace(*agentID)
	}
	if a == "" {
		a = s.defaultAgentID()
	}
	return model.Session{ID: id, Provider: p, Model: m, AgentID: a}, nil
}

// normalizeThinkingLevel：统一档的唯一校验处。空串合法（跟随模型默认）；
// 非空必须在该模型的能力集合里（providers.ThinkingLevels）——否则别写进库，界面也别摆这个选项。
func normalizeThinkingLevel(model, raw string) (string, bool) {
	level := strings.ToLower(strings.TrimSpace(raw))
	if level == "" {
		return "", true
	}
	for _, allowed := range providers.ThinkingLevels(model) {
		if level == allowed {
			return level, true
		}
	}
	return "", false
}

// thinkingError：422 文案点名"这个模型能收什么"，别只说"非法"。
func thinkingError(model, level string) string {
	return "模型 " + model + " 不支持思考档位 " + strconv.Quote(level) +
		"（可用：" + strings.Join(providers.ThinkingLevels(model), " / ") + "）"
}

// createSession：省略字段时取 config.json 的 defaults；
// agent 缺省 → `agents.json` 的 default_agent（**不然新会话永远带着内置 default**，
// agent 的提示词与世界状态底子对任何新会话都不生效 —— 这条踩过）。
// `id` 给了就用给的（草稿铸的 —— 预演头上已经是它）：先校形状、再看撞没撞（409）。
func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var req CreateSessionReq
	if !decodeJSON(w, r, &req) {
		return
	}
	id := ""
	if req.ID != nil && strings.TrimSpace(*req.ID) != "" {
		normalized, ok := normalizeUUIDv7(strings.TrimSpace(*req.ID))
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid", "id 要是 UUIDv7（草稿铸的那个）")
			return
		}
		id = normalized
		existing, err := s.store.GetSession(id)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if existing != nil {
			writeError(w, http.StatusConflict, "conflict", "这个 id 已经有会话了")
			return
		}
	}
	// 缺省每次现读（`PUT /config/defaults` 改完**当场生效** —— 启动快照会把它冻住，
	// 那正是"改完缺省、新会话还是 dummy"那类 bug 的来源）。口径与草稿预演同一处。
	seed, err := s.newSessionSeed(id, req.Provider, req.Model, req.AgentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if req.ThinkingLevel != nil {
		level, ok := normalizeThinkingLevel(seed.Model, *req.ThinkingLevel)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "invalid", thinkingError(seed.Model, *req.ThinkingLevel))
			return
		}
		seed.ThinkingLevel = level
	}
	title := ""
	if req.Title != nil {
		title = *req.Title
	}
	session, err := s.store.CreateSessionWithID(id, seed.Provider, seed.Model, req.SystemPrompt, title)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if seed.AgentID != session.AgentID {
		if err := s.store.SetSessionAgent(session.ID, seed.AgentID); err != nil {
			writeStoreError(w, err)
			return
		}
		session.AgentID = seed.AgentID
	}
	if seed.ThinkingLevel != "" {
		if err := s.store.SetSessionThinking(session.ID, seed.ThinkingLevel); err != nil {
			_ = s.store.DeleteSession(session.ID)
			writeStoreError(w, err)
			return
		}
		session.ThinkingLevel = seed.ThinkingLevel
	}
	if req.NativeToolsDisabled != nil {
		disabled := append([]string{}, (*req.NativeToolsDisabled)...)
		if err := s.store.SetSessionNativeToolsDisabled(session.ID, disabled); err != nil {
			_ = s.store.DeleteSession(session.ID)
			writeStoreError(w, err)
			return
		}
		session.NativeToolsDisabled = disabled
	}
	// 首段对话（`agent.first_message`，2026-10-11 用户定）：新会话把它插成**第一条 assistant 消息**
	// （idx 1）。插失败就把刚建的会话删掉（草稿铸的 id 还能重试），不留半截会话。
	// 导入（自己带消息表）与 Copy（复制既有会话）**都不走这儿** —— 不会被这条打扰。
	if first := s.loadAgents().FirstMessageOf(session.AgentID); first != "" {
		greeting := model.Message{
			ID:        uuid.Must(uuid.NewV7()).String(),
			SessionID: session.ID,
			Role:      model.RoleAssistant,
			Content:   first,
		}
		if _, err := s.store.InsertMessage(greeting); err != nil {
			_ = s.store.DeleteSession(session.ID)
			writeStoreError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, session)
}

// loadAgents：读 `agents.json`（读不到就算空的 —— 文件可以缺失）。
//
// 走 `config.LoadAgents`，不自己 `json.Unmarshal`：那条路上还挂着"退役的能力要摘掉"
// （2026-10-28 起 `judge`）—— 自己解析一遍，就等于只有启动时才摘、问一次接口又冒回来。
func (s *Server) loadAgents() config.AgentsConfig {
	agents, err := config.LoadAgents(s.paths)
	if err != nil {
		return config.AgentsConfig{}
	}
	return agents
}

// defaultAgentID：`agents.json` 的 default_agent（空串/缺失都算没配 ⇒ 内置默认）。
func (s *Server) defaultAgentID() string {
	if id := strings.TrimSpace(s.loadAgents().DefaultAgent); id != "" {
		return id
	}
	return model.DefaultAgentID()
}

// findAgent / agentExists / agentName：按 id 在**生效**列表里找（含内置默认；空 id 找不到）。
// 找的是"本机这一份"，导出的元数据（agent_id / agent_name）靠它说话。
func (s *Server) findAgent(id string) (config.Agent, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return config.Agent{}, false
	}
	for _, agent := range s.loadAgents().Effective() {
		if agent.ID == id {
			return agent, true
		}
	}
	return config.Agent{}, false
}

func (s *Server) agentExists(id string) bool {
	_, ok := s.findAgent(id)
	return ok
}

// agentName：id ⇒ 名字（找不到 ⇒ 空串；导出里 `agent_name` 只为"本机没有也能说清是谁"）。
func (s *Server) agentName(id string) string {
	agent, _ := s.findAgent(id)
	return agent.Name
}

func (s *Server) updateSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("session_id")
	if !s.ownsSession(w, r, id) {
		return
	}
	var req UpdateSessionReq
	if !decodeJSON(w, r, &req) {
		return
	}
	// 先把要校验的字段一次读完、验完，再落任何写 —— 否则"标题改了、档位非法"会留下半截更新。
	var session *model.Session
	if req.ThinkingLevel != nil || req.Provider != nil || req.Model != nil {
		current, err := s.store.GetSession(id)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if current == nil {
			writeError(w, http.StatusNotFound, "not_found", "会话不存在")
			return
		}
		session = current
	}
	var thinking *string
	if req.ThinkingLevel != nil {
		targetModel := session.Model
		if req.Model != nil {
			targetModel = *req.Model
		}
		level, ok := normalizeThinkingLevel(targetModel, *req.ThinkingLevel)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "invalid", thinkingError(targetModel, *req.ThinkingLevel))
			return
		}
		thinking = &level
	}
	if req.Title != nil {
		if err := s.store.UpdateTitle(id, *req.Title); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	if req.Provider != nil || req.Model != nil {
		provider, modelID := session.Provider, session.Model
		if req.Provider != nil {
			provider = *req.Provider
		}
		if req.Model != nil {
			modelID = *req.Model
		}
		if err := s.store.SetSessionModel(id, provider, modelID); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	if req.AgentID != nil {
		if err := s.store.SetSessionAgent(id, *req.AgentID); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	if req.SystemPrompt != nil {
		// 空串 = 清掉覆盖（落库就是空串 ⇒ 解析时回落到 agent 的 ⇒ 底子算全局）
		if err := s.store.SetSessionSystemPrompt(id, *req.SystemPrompt); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	if thinking != nil {
		if err := s.store.SetSessionThinking(id, *thinking); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	if req.NativeToolsDisabled != nil {
		disabled := append([]string{}, (*req.NativeToolsDisabled)...)
		if err := s.store.SetSessionNativeToolsDisabled(id, disabled); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	if req.RequestOverrides != nil {
		overrides := map[string]json.RawMessage{}
		for key, value := range *req.RequestOverrides {
			overrides[key] = value
		}
		if err := s.store.SetSessionRequestOverrides(id, overrides); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	updated, err := s.store.GetSession(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if updated == nil {
		writeError(w, http.StatusNotFound, "not_found", "会话不存在")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	if !s.ownsSession(w, r, r.PathValue("session_id")) {
		return
	}
	if err := s.store.DeleteSession(r.PathValue("session_id")); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// decodeJSON：请求体**语法**不合法 ⇒ 400（对齐 axum 的 400）。
//
// 注意不含"缺字段"：那是**语义**错，axum 给 422 ⇒ 各处理器自己判（见 missingField）。
func decodeJSON(w http.ResponseWriter, r *http.Request, into any) bool {
	if err := json.NewDecoder(r.Body).Decode(into); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "请求体不是合法的 JSON")
		return false
	}
	return true
}

// missingField：必填字段缺失 ⇒ **422** —— 与 axum 的 JsonRejection 状态码一致。
// 错误体仍用本项目契约里的 JSON（axum 那串纯文本不合自家契约，这里刻意不照抄）。
func missingField(w http.ResponseWriter, field string) {
	writeError(w, http.StatusUnprocessableEntity, "invalid", "缺 "+field)
}

// writeStoreError：把 store 的两类错误映射成固定的错误体（客户端按 code 分支）。
func writeStoreError(w http.ResponseWriter, err error) {
	var invalid store.InvalidError
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "会话不存在")
	case errors.As(err, &invalid):
		writeError(w, http.StatusBadRequest, "invalid", invalid.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}

// copySession：**Copy 一条会话** —— 线性会话里"分岔"就是它（不叫 fork）。
//
// 新 session（新 id）+ 消息与摘要一并复制（摘要新 id、消息的 `summary_id` 重映射）；
// 标题 / 渠道 / 模型 / agent / 提示词带着；**世界状态不复制**（它本来就现演）。
func (s *Server) copySession(w http.ResponseWriter, r *http.Request) {
	if !s.ownsSession(w, r, r.PathValue("session_id")) {
		return
	}
	copied, err := s.store.CopySession(r.PathValue("session_id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, copied)
}

// importSt：从 SillyTavern JSONL 建会话并按序落库（设置面板"已有对话管理器"的导入口")。
//
// 体是 JSONL 原文（按行解，见 `store.ParseSTLines`）。建会话走与 `createSession` 同一套缺省
// （provider / model / agent）；标题给了就用（`?title=`），没给 ⇒ 首条 user 起。
// 回 `{"session", "messages", "skipped"}`（201）。空文件 / 一条可落的都没有 ⇒ 400。
func (s *Server) importSt(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "读请求体失败："+err.Error())
		return
	}
	parsed, skipped := store.ParseSTLines(raw)
	if len(parsed) == 0 {
		writeError(w, http.StatusBadRequest, "invalid", "文件里没有可导入的消息")
		return
	}
	title := ""
	if value := r.URL.Query().Get("title"); strings.TrimSpace(value) != "" {
		title = strings.TrimSpace(value)
	}
	cfg, err := config.LoadConfig(s.paths)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	session, err := s.store.CreateSession(cfg.Defaults.Provider, cfg.Defaults.Model, "", title)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if agentID := s.defaultAgentID(); agentID != "" && agentID != session.AgentID {
		if err := s.store.SetSessionAgent(session.ID, agentID); err != nil {
			writeStoreError(w, err)
			return
		}
		session.AgentID = agentID
	}
	if err := s.store.ImportMessages(session.ID, parsed); err != nil {
		writeStoreError(w, err)
		return
	}
	if title == "" {
		for _, item := range parsed {
			if item.IsUser && strings.TrimSpace(item.Content) != "" {
				_, _ = s.store.SetTitleIfEmpty(session.ID, model.TitleFrom(item.Content, cfg.Chat.TitleChars))
				break
			}
		}
	}
	sessionFresh, err := s.store.GetSession(session.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"session": sessionFresh, "messages": len(parsed), "skipped": skipped,
	})
}

// writeMessageError：消息级路由的 404 文案 —— "会话不存在"会误导（多半是那条消息没了）。
func writeMessageError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "这条会话里没有这条消息")
		return
	}
	writeStoreError(w, err)
}

// EditMessageReq：正文 / 思考**至少给一个**（两个都不给 ⇒ 422）—— nil = 不动，"" = 清掉。
type EditMessageReq struct {
	Content   *string `json:"content"`
	Reasoning *string `json:"reasoning"`
}

// editMessage：改**任意**一条消息的正文 / 思考 = 重写存档（世界状态随之现演；摘要只标 `dirty`、**不级联**）。
func (s *Server) editMessage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	var req EditMessageReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Content == nil && req.Reasoning == nil {
		missingField(w, "content / reasoning（至少给一个）")
		return
	}
	message, err := s.store.UpdateMessage(r.PathValue("session_id"), r.PathValue("message_id"),
		store.MessageEdit{Content: req.Content, Reasoning: req.Reasoning})
	if err != nil {
		writeMessageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, message)
}

// DeleteMessageReq：删消息的请求体 —— 只带回**预览里的最后一条**。
//
// 预览之后、执行之前有人往尾巴上追加 ⇒ 照旧计划删会**多删**且静默 ⇒ 核对不符就 409（重新预览）。
type DeleteMessageReq struct {
	LastDeletedMessageID *string `json:"last_deleted_message_id"`
}

// deletionPreview：删除预览 —— **只算不动**（安全 ⇒ GET）。
//
// 与 DELETE 走**同一段计算**（`store.DeletionPlan`）⇒ 预览不会与真删漂移。
func (s *Server) deletionPreview(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	plan, err := s.store.DeletionPlan(r.PathValue("session_id"), r.PathValue("message_id"))
	if err != nil {
		writeMessageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// deleteMessage：删**这条及之后的全部消息**，级联见 DEFINE.md 的"删除的级联规则"：
// 【目标及其之后的全部消息】+【覆盖区间与之相交的全部摘要】+【这些摘要的全部祖先】，
// 再把幸存者里指向死摘要的指针置空。
func (s *Server) deleteMessage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	var req DeleteMessageReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.LastDeletedMessageID == nil {
		missingField(w, "last_deleted_message_id")
		return
	}
	sessionID, messageID := r.PathValue("session_id"), r.PathValue("message_id")
	plan, err := s.store.DeletionPlan(sessionID, messageID)
	if err != nil {
		writeMessageError(w, err)
		return
	}
	if *req.LastDeletedMessageID != plan.LastDeletedMessageID {
		writeError(w, http.StatusConflict, "conflict",
			"要删的这一段变了（会话末尾已经不是预览时的那条）：请重新取一次删除预览")
		return
	}
	if err := s.store.ApplyDeletion(plan); err != nil {
		writeStoreError(w, err)
		return
	}
	// 回的就是刚执行的那份计划（与预览同一个形状：真删了什么，一眼对得上）
	writeJSON(w, http.StatusOK, plan)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	// 默认会把 < > & 转义成 \u003c（Go 的老毛病）。消息正文里全是 LaTeX（\(i<j\)），
	// Rust 版是原样输出的 ⇒ **必须关掉**，否则两边字节不一致、用户也会看到 \u003c。
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(body); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	// Encode 会补一个 \n，axum 不补 —— 直接对齐字节（对账闸才有意义）
	payload := bytes.TrimRight(buffer.Bytes(), "\n")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message}})
}

// trimSpace：收在一处（免得 import 到处飞）。
func trimSpace(text string) string { return strings.TrimSpace(text) }
