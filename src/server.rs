//! HTTP API：前后端分离契约（前端只调 WebAPI，可整体换实现）。
//!
//! - 路由统一挂 `/api/v1`；错误体固定 `{"error":{"code","message"}}`，`code` 是稳定枚举，
//!   客户端按 `code` 分支而不是匹配文案；
//! - `config.server.auth_token` 为 `Some` 时全局要求 `Authorization: Bearer <token>`；
//! - CORS 全开：egui 之外将来接 Web/Flutter 客户端零配置。

use std::collections::BTreeMap;
use std::sync::{Arc, Mutex, MutexGuard};

use axum::{
    extract::{Path, State},
    http::{HeaderMap, StatusCode},
    middleware::{self, Next},
    response::{IntoResponse, Response},
    routing::{delete, get, patch, post},
    Json, Router,
};
use serde::{Deserialize, Serialize};
use tower_http::cors::{Any, CorsLayer};
use uuid::Uuid;

use crate::chat;
use crate::config::{
    self, Agent, AgentsConfig, Config, Paths, ProviderConfig, ProviderKind, ProvidersConfig,
};
use crate::model::{title_from, Conversation, DiscoveredModel, Message, Role};
use crate::providers;
use crate::registry::{self, ProviderView};
use crate::store::{self, Store};
use crate::turn::{Phase, TurnRegistry, TurnStatus};
use crate::vars;

#[derive(Clone)]
pub struct AppState {
    store: Arc<Mutex<Store>>,
    auth_token: Option<String>,
    title_chars: usize,
    /// 每个会话"这一轮在不在跑"。**进程内的事实，不进库**——后端一重启就没有生成在跑，
    /// 状态回到 `idle` 是诚实的；库里只会留下占位消息，启动时清掉。
    turns: Arc<TurnRegistry>,
    /// 新建会话的默认 provider / model（来自 `config.json` 的 `defaults`）。
    default_provider: String,
    default_model: String,
    /// `providers.json`（含密钥）/ `agents.json` / `config.json` 的位置：这些文件
    /// **每次请求现读**，手改了文件立刻生效，无需重启。
    paths: Paths,
}

impl AppState {
    pub fn new(store: Store, config: &Config, paths: Paths) -> Self {
        Self {
            store: Arc::new(Mutex::new(store)),
            auth_token: config.server.auth_token.clone(),
            title_chars: config.chat.title_chars,
            turns: Arc::new(TurnRegistry::new()),
            default_provider: config.defaults.provider.clone(),
            default_model: config.defaults.model.clone(),
            paths,
        }
    }

    fn lock(&self) -> Result<MutexGuard<'_, Store>, ApiError> {
        self.store
            .lock()
            .map_err(|_| ApiError::internal("存储锁中毒"))
    }
}

pub fn router(state: AppState) -> Router {
    let api = Router::new()
        .route(
            "/conversations",
            get(list_conversations).post(create_conversation),
        )
        .route(
            "/conversations/{id}",
            patch(update_conversation).delete(delete_conversation),
        )
        .route(
            "/conversations/{id}/messages",
            get(list_messages).post(send_message),
        )
        .route("/conversations/{id}/resend", post(resend_message))
        .route("/conversations/{id}/status", get(get_turn_status))
        .route("/conversations/{id}/stop", post(stop_turn))
        .route("/conversations/{id}/branches", get(get_branches))
        .route(
            "/conversations/{id}/messages/{message_id}",
            patch(edit_message).delete(delete_message),
        )
        .route(
            "/conversations/{id}/messages/{message_id}/siblings",
            delete(delete_siblings),
        )
        .route(
            "/conversations/{id}/variables",
            get(get_conversation_variables),
        )
        .route(
            "/conversations/{id}/outgoing",
            get(get_conversation_outgoing),
        )
        .route("/health", get(health))
        .route("/providers", get(list_providers).post(create_provider))
        .route("/models", get(list_all_models))
        .route("/models/probe", post(probe_models))
        .route(
            "/providers/{id}",
            patch(update_provider).delete(delete_provider),
        )
        .route("/providers/{id}/refresh", post(refresh_provider))
        .route("/agents", get(list_agents).post(create_agent))
        .route("/agents/{id}", patch(update_agent).delete(delete_agent))
        .route("/debug/state", get(debug_state))
        .route("/debug/file/{name}", get(debug_file))
        .route_layer(middleware::from_fn_with_state(state.clone(), require_bearer))
        .with_state(state);

    Router::new()
        .nest("/api/v1", api)
        .layer(CorsLayer::new().allow_origin(Any).allow_methods(Any).allow_headers(Any))
}

#[derive(Deserialize)]
pub struct CreateConversationReq {
    /// 省略时用 `config.json` 的 `defaults.provider` / `defaults.model`（默认落到 dummy）。
    #[serde(default)]
    pub provider: Option<String>,
    #[serde(default)]
    pub model: Option<String>,
    /// 省略时用 `agents.json` 的 `default_agent`。
    #[serde(default)]
    pub agent_id: Option<String>,
    /// 会话级 system prompt。**留空就是没覆盖**：用 agent 的（变量底子也跟着用它的）。
    #[serde(default)]
    pub system_prompt: String,
}

#[derive(Deserialize)]
pub struct UpdateConversationReq {
    pub title: Option<String>,
    /// 把"当前尾巴"切到某条消息上（换分支）。那条必须属于这个会话。
    pub current_leaf: Option<Uuid>,
    /// 换模型：`provider` 与 `model` 一起给（模型是 provider 下的 id）。
    pub provider: Option<String>,
    pub model: Option<String>,
    pub agent_id: Option<String>,
}

#[derive(Deserialize)]
pub struct SendReq {
    pub content: String,
}

async fn list_conversations(
    State(state): State<AppState>,
) -> Result<Json<Vec<ConversationView>>, ApiError> {
    let conversations = state.lock()?.list_conversations()?;
    Ok(Json(
        conversations
            .into_iter()
            .map(|conversation| ConversationView {
                turn: state.turns.status(conversation.id),
                conversation,
            })
            .collect(),
    ))
}

async fn create_conversation(
    State(state): State<AppState>,
    Json(req): Json<CreateConversationReq>,
) -> Result<(StatusCode, Json<Conversation>), ApiError> {
    let provider = req
        .provider
        .unwrap_or_else(|| state.default_provider.clone());
    let model = req.model.unwrap_or_else(|| state.default_model.clone());
    let mut store = state.lock()?;
    let mut conv = store.create_conversation(&provider, &model, &req.system_prompt)?;

    // agent 缺省 → 取 `agents.json` 的 `default_agent`。
    // 不这么做的话新会话永远带着内置的 `default`，agent 的提示词与它写的变量底子都用不上。
    let agent_id = match req.agent_id {
        Some(id) if !id.trim().is_empty() => id.trim().to_owned(),
        _ => {
            let agents: AgentsConfig = crate::config::load_json(&state.paths.agents_json())?;
            agents
                .default_agent()
                .map(|agent| agent.id.clone())
                .unwrap_or_else(|| crate::model::DEFAULT_AGENT_ID.to_owned())
        }
    };
    if agent_id != conv.agent_id {
        store.set_conversation_agent(conv.id, &agent_id)?;
        conv.agent_id = agent_id;
    }
    Ok((StatusCode::CREATED, Json(conv)))
}

/// 改会话的标题 / 模型 / Agent。三者都可省略，只改给出来的那些。
async fn update_conversation(
    State(state): State<AppState>,
    Path(id): Path<Uuid>,
    Json(req): Json<UpdateConversationReq>,
) -> Result<Json<Conversation>, ApiError> {
    let mut store = state.lock()?;

    if let Some(title) = req.title {
        store.update_title(id, &title)?;
    }
    if req.provider.is_some() || req.model.is_some() {
        let conversation = store.get_conversation(id)?.ok_or_else(ApiError::not_found)?;
        let provider = req.provider.unwrap_or(conversation.provider);
        let model = req.model.unwrap_or(conversation.model);
        store.set_conversation_model(id, &provider, &model)?;
    }
    if let Some(agent_id) = req.agent_id {
        store.set_conversation_agent(id, &agent_id)?;
    }
    if let Some(leaf) = req.current_leaf {
        // 界面上点「‹ 2/3 ›」：切到**当前尾巴的兄弟**（那条候选回复），再顺着最新的孩子走到末端。
        // 不允许切到别的旧消息——那等于把对话倒回去，变量（世界状态）会跟着倒。
        store.switch_leaf_to_sibling(id, leaf)?;
    }

    Ok(Json(
        store.get_conversation(id)?.ok_or_else(ApiError::not_found)?,
    ))
}

async fn delete_conversation(
    State(state): State<AppState>,
    Path(id): Path<Uuid>,
) -> Result<StatusCode, ApiError> {
    state.lock()?.delete_conversation(id)?;
    Ok(StatusCode::NO_CONTENT)
}

async fn list_messages(
    State(state): State<AppState>,
    Path(id): Path<Uuid>,
) -> Result<Json<Vec<Message>>, ApiError> {
    Ok(Json(state.lock()?.list_messages(id)?))
}

/// 一条会话的视图：会话本体 + 它当前这一轮的状态。
///
/// `#[serde(flatten)]` 是给兼容性留的：老客户端按 `Conversation` 解析会**忽略**多出来的
/// `turn`（serde 默认忽略未知字段），新客户端多读一个字段——两边不用迁就对方。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ConversationView {
    #[serde(flatten)]
    pub conversation: Conversation,
    pub turn: TurnStatus,
}

impl std::ops::Deref for ConversationView {
    type Target = Conversation;
    fn deref(&self) -> &Self::Target {
        &self.conversation
    }
}

/// 「发送」与「重新发送」的受理回执：**不含回复正文**。
///
/// 生成在后台跑（`tokio::spawn`），客户端拿 `turn.message_id` 指认那条正在生成的助手
/// 消息，再按 `turn.phase` 轮询 `/status` 到 `idle`（或 `error`）。
/// 这样非流式与流式**共用同一套前端逻辑**：流式不过是"同一个消息 id 的正文在变长"。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TurnAccepted {
    /// 刚落下的用户消息。重发时**不带**（重发触发的是已有的那条）。
    #[serde(skip_serializing_if = "Option::is_none")]
    pub user: Option<Message>,
    /// 选中的对话后端名（`dummy` / `fallback` / `openai-completion`）。
    pub backend: String,
    /// 受理那一刻的状态（`pending`）。
    pub turn: TurnStatus,
}

/// 一轮对话：落用户消息 → 选对话后端 → **把生成丢给后台**。
///
/// 顺序是刻意的：**用户消息先落库**。若后端失败（没配模型、上游挂了），用户的话仍在
/// 库里，重发或换模型不会丢上下文。
/// 已经在生成中的会话再发 → 409（**不排队**：排队会让"按了发送却什么都没发生"难以解释）。
async fn send_message(
    State(state): State<AppState>,
    Path(id): Path<Uuid>,
    Json(req): Json<SendReq>,
) -> Result<(StatusCode, Json<TurnAccepted>), ApiError> {
    let (user, parent) = {
        let mut store = state.lock()?;
        let conversation = store.get_conversation(id)?.ok_or_else(ApiError::not_found)?;
        if state.turns.status(id).phase.is_busy() {
            return Err(ApiError::conflict("这个会话还在生成中，等它跑完或先按停止"));
        }
        // 接在**当前尾巴**下面；没有尾巴（空会话）就是第一条。
        let user = store.insert_message(id, Role::User, &req.content, conversation.current_leaf)?;
        if conversation.title.is_empty() {
            store.update_title(id, &title_from(&req.content, state.title_chars))?;
        }
        (user.clone(), user.id)
    };

    let (backend, turn) = start_turn(&state, id, parent).await?;
    Ok((
        StatusCode::ACCEPTED,
        Json(TurnAccepted {
            user: Some(user),
            backend,
            turn,
        }),
    ))
}

/// 重新发送：**最后一条是助手就再长一个兄弟**（对这条回复不满意，旧的留在树上）；
/// **最后一条是用户消息就照着它重发**（比如上回上游报错，回复没落下来）。
///
/// 两条路都走 `start_turn`——和正常发消息是同一条生成路径，所以重发出来的东西
/// 与第一次相比没有任何"特殊待遇"。
async fn resend_message(
    State(state): State<AppState>,
    Path(id): Path<Uuid>,
) -> Result<(StatusCode, Json<TurnAccepted>), ApiError> {
    let parent = {
        let store = state.lock()?;
        if state.turns.status(id).phase.is_busy() {
            return Err(ApiError::conflict("这个会话还在生成中，等它跑完或先按停止"));
        }
        let messages = store.list_messages(id)?;
        match messages.last() {
            None => return Err(ApiError::bad_request("这个会话还没有消息可重发")),
            // 尾条是助手 → 新回复挂在它的**父亲**下面，两条成为兄弟；旧的留着，
            // 界面上的「‹ 2/3 ›」能切回去。（以前是先把尾巴退回去，现在由占位消息
            // 自己把尾巴挪到新位置，路径一步不动。）
            Some(last) if last.role == Role::Assistant => last
                .parent_id
                .ok_or_else(|| ApiError::bad_request("这条回复没有上文，没法重发"))?,
            // 尾条本来就是用户消息（上回报错、或刚删掉回复）→ 直接照着它生成。
            Some(last) => last.id,
        }
    };

    let (backend, turn) = start_turn(&state, id, parent).await?;
    Ok((
        StatusCode::ACCEPTED,
        Json(TurnAccepted {
            user: None,
            backend,
            turn,
        }),
    ))
}

/// 开一轮生成：先把这条回复的 id 定下来（UUIDv7，本地就能算）并登记状态，
/// 再把真正的工作丢给后台任务。**开跑之前不写库**——库里只放完整的回复。
async fn start_turn(
    state: &AppState,
    id: Uuid,
    parent: Uuid,
) -> Result<(String, TurnStatus), ApiError> {
    let providers = ProvidersConfig::load(&state.paths.providers_json())?;
    let backend = {
        let store = state.lock()?;
        let conversation = store.get_conversation(id)?.ok_or_else(ApiError::not_found)?;
        chat::Backend::select(&conversation, &providers)
            .name()
            .to_owned()
    };

    // id 先发出去（客户端拿它指认"正在生成的那条"），落库时用同一个。
    let message = Uuid::now_v7();
    // `begin` 是唯一的闸门：同一会话只放行一轮。上面那个 busy 检查只是为了少写一条
    // 用户消息，真正的并发防线在这里。
    let token = state
        .turns
        .begin(id, message)
        .map_err(|_| ApiError::conflict("这个会话还在生成中，等它跑完或先按停止"))?;
    let handle = tokio::spawn(run_turn(state.clone(), id, parent, message, token));
    state.turns.attach(id, token, handle);

    Ok((backend, state.turns.status(id)))
}

/// 真去生成一段回复：取料（锁里）→ 走上游（锁外）→ 返回正文。
///
/// 组装出站消息用的是**唯一那条路径**：[`vars::build_outgoing`]（历史剔除状态块、
/// 注入当前变量表、跳过空正文）。这里不另写一份拼装。
async fn generate(state: &AppState, id: Uuid) -> Result<String, ApiError> {
    let providers = ProvidersConfig::load(&state.paths.providers_json())?;

    // 取料（会话、历史、出站消息）都在锁里，**等上游之前把锁放掉**。
    // 密钥不用单独取：它就在 provider 自己身上（`providers.json`）。
    let (conversation, outgoing) = {
        let store = state.lock()?;
        let conversation = store.get_conversation(id)?.ok_or_else(ApiError::not_found)?;
        let messages = store.list_messages(id)?;
        let (system_prompt, source) = effective_system_prompt(state, &conversation)?;
        let effective =
            vars::VariableView::from_sources(id, &system_prompt, source, &messages).effective;
        let outgoing = vars::build_outgoing(&system_prompt, &messages, &effective);
        (conversation, outgoing)
    };

    let reply = chat::complete(&conversation, &outgoing, &providers).await?;
    Ok(reply)
}

/// 后台那半程：生成完**才**把回复插进库（用受理时发出去的那个 id 与当时捕获的上文）。
///
/// 全程**不持锁跨 await**：取料在锁里，等上游在锁外，落库再进锁。
/// 收尾前核对令牌——被"停止"顶掉的那一轮结果直接丢掉，库里什么痕迹都不会留下。
async fn run_turn(state: AppState, id: Uuid, parent: Uuid, message: Uuid, token: u64) {
    let reply = match generate(&state, id).await {
        Ok(reply) => reply,
        Err(error) => {
            if state
                .turns
                .finish(id, token, Phase::Error, 0, Some(error.message.clone()))
                .is_err()
            {
                // 已经被停止：错就不必再报给前端了（它是上一轮的事）
                eprintln!("生成失败（这一轮已被停止）: {}", error.message);
            }
            return;
        }
    };

    if !state.turns.is_mine(id, token) {
        // 这一轮已经被停掉：库里没有它的痕迹，丢掉就完了
        return;
    }
    let chars = reply.chars().count();
    let stored = match state.lock() {
        Ok(mut store) => store
            .insert_message_with_id(id, message, Role::Assistant, &reply, Some(parent))
            .map(|_| ())
            .map_err(ApiError::from),
        Err(_) => Err(ApiError::internal("存储锁中毒")),
    };
    match stored {
        Ok(()) => {
            let _ = state.turns.finish(id, token, Phase::Idle, chars, None);
        }
        Err(error) => {
            let _ = state
                .turns
                .finish(id, token, Phase::Error, 0, Some(error.message.clone()));
        }
    }
}

/// 这个会话此刻的状态。前端按它转圈/放行：`idle` 之外都别让用户再发。
async fn get_turn_status(
    State(state): State<AppState>,
    Path(id): Path<Uuid>,
) -> Result<Json<TurnStatus>, ApiError> {
    if state.lock()?.get_conversation(id)?.is_none() {
        return Err(ApiError::not_found());
    }
    Ok(Json(state.turns.status(id)))
}

/// 停止这一轮。**幂等**：没在跑也回 200（`stopped: false`）。
///
/// 摘登记（旧任务回来时令牌对不上，写不进去）→ `abort` 后台任务（别让上游白跑完）。
/// **库里没有要收拾的东西**：这条回复在拿到整段之前不存在，任务被丢掉就没了。
async fn stop_turn(
    State(state): State<AppState>,
    Path(id): Path<Uuid>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let Some(handle) = state.turns.stop(id) else {
        return Ok(Json(serde_json::json!({ "stopped": false })));
    };
    if let Some(handle) = handle {
        handle.abort();
    }
    Ok(Json(serde_json::json!({ "stopped": true })))
}

/// 编辑一条消息的请求体。
#[derive(Deserialize)]
pub struct EditMessageReq {
    pub content: String,
}

/// 编辑一条消息（你和助手的都能改）。
///
/// 正文是**存档**：改它等于改写那一回合的记录，所以它产生的变量操作按新正文整体重算
/// （同一事务里换掉），否则世界状态会和存档自相矛盾。
async fn edit_message(
    State(state): State<AppState>,
    Path((id, message_id)): Path<(Uuid, Uuid)>,
    Json(req): Json<EditMessageReq>,
) -> Result<Json<Message>, ApiError> {
    let mut store = state.lock()?;
    Ok(Json(store.update_message(id, message_id, &req.content)?))
}

/// 删除某一句。跨会话删（`id` 与消息不匹配）给 404——和编辑同一个口径。
///
/// 顺序是插入顺序（`rowid`），删中间一句只是少一行。变量表不落库，所以这里也不用清什么：
/// 正文没了，重演时它的 `<state>` 自然不在了。
/// 删掉某条消息的**所有兄弟**（连同各自子树）：站在某条回复上点「删除全部」时走这里。
/// 只留下它们的上文（通常是那句问话），接着就能重新生成。
async fn delete_siblings(
    State(state): State<AppState>,
    Path((id, message_id)): Path<(Uuid, Uuid)>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let deleted = state.lock()?.delete_siblings(id, message_id)?;
    Ok(Json(serde_json::json!({ "deleted": deleted })))
}

async fn delete_message(
    State(state): State<AppState>,
    Path((id, message_id)): Path<(Uuid, Uuid)>,
) -> Result<Json<serde_json::Value>, ApiError> {
    // 删的是**整棵子树**（中间那条下面有分支时一起没），所以把条数回给前端，
    // 让界面能说清"删掉了 N 条"，而不是只报一个"成功"。
    let deleted = state.lock()?.delete_message(id, message_id)?;
    Ok(Json(serde_json::json!({ "deleted": deleted })))
}

/// 生效的 system prompt：会话级覆盖优先，否则用 agent 的（内置默认也算）。
///
/// **这就是真会发给模型的那份**。变量底子、出站消息、界面显示都用它，
/// 免得"界面上写的提示词"和"实际发出去的"成了两份东西。
/// 返回值第二项是它的来源：agent 的提示词写的底子所有会话共享（算全局），
/// 会话自己写的只属于这条会话。
fn effective_system_prompt(
    state: &AppState,
    conversation: &Conversation,
) -> Result<(String, vars::PromptSource), ApiError> {
    let own = conversation.system_prompt.trim();
    if !own.is_empty() {
        return Ok((own.to_owned(), vars::PromptSource::Conversation));
    }
    let agents: AgentsConfig = crate::config::load_json(&state.paths.agents_json())?;
    let prompt = agents
        .resolve(&conversation.agent_id)
        .map(|agent| agent.system_prompt)
        .unwrap_or_default();
    Ok((prompt, vars::PromptSource::Agent))
}

/// 每条消息"在同龄兄弟里排第几、一共几条"——界面上的「< 2/3 >」用它。
async fn get_branches(
    State(state): State<AppState>,
    Path(id): Path<Uuid>,
) -> Result<Json<std::collections::BTreeMap<Uuid, crate::store::BranchInfo>>, ApiError> {
    Ok(Json(state.lock()?.branch_info(id)?))
}

/// 某会话的变量：全局打底 + 顺着正文重演出来的本会话改动 + 生效值。
async fn get_conversation_variables(
    State(state): State<AppState>,
    Path(id): Path<Uuid>,
) -> Result<Json<vars::VariableView>, ApiError> {
    let store = state.lock()?;
    let conversation = store.get_conversation(id)?.ok_or_else(ApiError::not_found)?;
    let messages = store.list_messages(id)?;
    let (system_prompt, source) = effective_system_prompt(&state, &conversation)?;
    Ok(Json(vars::VariableView::from_sources(
        id,
        &system_prompt,
        source,
        &messages,
    )))
}

/// **下次会发出去的东西**：变量表已注入、历史里的状态块已剔除。
///
/// 这是"看不见提示词就没法调试"的解药，也是真模型后端接进来之前的验收面。
async fn get_conversation_outgoing(
    State(state): State<AppState>,
    Path(id): Path<Uuid>,
) -> Result<Json<Vec<vars::Outgoing>>, ApiError> {
    let store = state.lock()?;
    let conversation = store.get_conversation(id)?.ok_or_else(ApiError::not_found)?;
    let messages = store.list_messages(id)?;
    let (system_prompt, source) = effective_system_prompt(&state, &conversation)?;
    let effective =
        vars::VariableView::from_sources(id, &system_prompt, source, &messages).effective;
    Ok(Json(vars::build_outgoing(
        &system_prompt,
        &messages,
        &effective,
    )))
}

/// 连通性探针：前端启动时用它判断"后端在不在、口令对不对"。
/// 同样过鉴权——401 与"连不上"是两种不同的失败，前端要能区分。
async fn health() -> Json<serde_json::Value> {
    Json(serde_json::json!({
        "status": "ok",
        "version": env!("CARGO_PKG_VERSION"),
    }))
}

/// 跨 provider 的**扁平模型列表**：给"只选一个模型"的下拉用。
/// 每项都带 `provider` 与显示名（provider 名可选，缺省回退到 id 由客户端做）。
async fn list_all_models(
    State(state): State<AppState>,
) -> Result<Json<Vec<registry::ModelListItem>>, ApiError> {
    let config = ProvidersConfig::load(&state.paths.providers_json())?;
    let store = state.lock()?;
    Ok(Json(registry::model_list(&registry::views(&store, &config)?)))
}

/// provider × 模型 的合并视图（配置来自 `providers.json`，模型来自数据库）。
async fn list_providers(
    State(state): State<AppState>,
) -> Result<Json<Vec<ProviderView>>, ApiError> {
    let config = ProvidersConfig::load(&state.paths.providers_json())?;
    let store = state.lock()?;
    Ok(Json(registry::views(&store, &config)?))
}

/// 新建 provider。`kind` 决定要哪些字段：`dummy` 不该有 `base_url`，`openai-compat` 必须有。
/// `api_key` 就写在**这个 provider 自己身上**（`providers.json` 本来就是含密钥的文件），
/// 回给客户端的是**不含密钥**的视图。
async fn create_provider(
    State(state): State<AppState>,
    Json(req): Json<CreateProviderReq>,
) -> Result<(StatusCode, Json<ProviderView>), ApiError> {
    let id = req.id.trim().to_owned();
    if id.is_empty() || id.chars().any(char::is_whitespace) {
        return Err(ApiError::bad_request("provider id 不能为空、不能含空白字符"));
    }

    let path = state.paths.providers_json();
    let mut config = ProvidersConfig::load(&path)?;
    if config.get(&id).is_some() {
        return Err(ApiError::conflict("同名 provider 已存在"));
    }

    let provider = ProviderConfig {
        id,
        name: clean_name(req.name),
        kind: req.kind,
        base_url: req.base_url.trim().to_owned(),
        headers: req.headers,
        api_key: req.api_key.unwrap_or_default().trim().to_owned(),
        ..Default::default()
    };
    let created = provider.id.clone();
    config.providers.push(provider);
    if let Err(e) = config.validate() {
        return Err(ApiError::bad_request(&e.to_string()));
    }

    config.save(&path)?;
    Ok((
        StatusCode::CREATED,
        Json(one_view(&state, &config, &created)?),
    ))
}

/// 某个 provider 的对外视图（**不含密钥**）：写完配置要回话时用它。
fn one_view(
    state: &AppState,
    config: &ProvidersConfig,
    id: &str,
) -> Result<ProviderView, ApiError> {
    let store = state.lock()?;
    registry::views(&store, config)?
        .into_iter()
        .find(|view| view.id == id)
        .ok_or_else(ApiError::not_found)
}

/// 去上游拉 `/models` 并落库。HTTP 在**锁外**完成，避免持锁跨 await。
async fn refresh_provider(
    State(state): State<AppState>,
    Path(id): Path<String>,
) -> Result<Json<ProviderView>, ApiError> {
    let config = ProvidersConfig::load(&state.paths.providers_json())?;
    let provider = config.get(&id).ok_or_else(ApiError::not_found)?;
    match provider.kind {
        ProviderKind::Dummy => {
            return Err(ApiError::bad_request("dummy provider 没有上游可拉取"));
        }
        ProviderKind::OpenAiCompat => {}
    }
    let client = providers::Client::new(provider)?;
    let discovered = client.list_models().await?;

    let mut store = state.lock()?;
    store.apply_discovery(&id, &discovered)?;
    drop(discovered);
    drop(store);

    Ok(Json(one_view(&state, &config, &id)?))
}

#[derive(Deserialize)]
pub struct ProbeReq {
    pub base_url: String,
    /// 一次性使用：既不落盘也不回显。
    #[serde(default)]
    pub api_key: Option<String>,
    #[serde(default)]
    pub headers: BTreeMap<String, String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProbeResult {
    pub models: Vec<DiscoveredModel>,
}

/// 用 **url + apikey** 直接拉模型列表，**不落库**——"先探测，再决定保不保存"。
async fn probe_models(Json(req): Json<ProbeReq>) -> Result<Json<ProbeResult>, ApiError> {
    if !req.base_url.starts_with("http://") && !req.base_url.starts_with("https://") {
        return Err(ApiError::bad_request(
            "base_url 必须以 http:// 或 https:// 开头",
        ));
    }
    let client = providers::Client::from_parts(&req.base_url, req.api_key, req.headers)?;
    let models = client.list_models().await?;
    Ok(Json(ProbeResult { models }))
}

#[derive(Deserialize)]
pub struct CreateProviderReq {
    pub id: String,
    /// 显示名（可选）：前端下拉里显示它，缺省回退到 `id`。
    #[serde(default)]
    pub name: Option<String>,
    #[serde(default)]
    pub kind: ProviderKind,
    #[serde(default)]
    pub base_url: String,
    #[serde(default)]
    pub headers: BTreeMap<String, String>,
    /// 写进 `providers.json` 里这个 provider 的 `api_key`；接口从此不回显它。
    #[serde(default)]
    pub api_key: Option<String>,
}

#[derive(Deserialize)]
pub struct UpdateProviderReq {
    pub base_url: Option<String>,
    /// `None` = 不动；`Some("")` = 清掉显示名；其它 = 设置。
    pub name: Option<String>,
    pub headers: Option<BTreeMap<String, String>>,
    /// `None` = 不动；`Some("")` = 删除密钥；其它 = 设置。
    pub api_key: Option<String>,
}

/// 显示名：空白与空串都当"没配"（`Some("")` 是"清掉"的写法）。
fn clean_name(name: Option<String>) -> Option<String> {
    name.map(|name| name.trim().to_owned())
        .filter(|name| !name.is_empty())
}

/// 删除一个 provider：从 `providers.json` 摘掉、清掉它的密钥、忘掉已发现的模型。
///
/// 历史会话里仍留着它的名字——那由 `chat::Backend::select` 兜底成 fallback 话术，
/// 不是错误，所以这里不做任何"正在被使用"的阻拦。
async fn delete_provider(
    State(state): State<AppState>,
    Path(id): Path<String>,
) -> Result<StatusCode, ApiError> {
    let path = state.paths.providers_json();
    let mut config = ProvidersConfig::load(&path)?;
    let before = config.providers.len();
    config.providers.retain(|provider| provider.id != id);
    if config.providers.len() == before {
        return Err(ApiError::not_found());
    }
    // 删掉不需要再 validate：移除不可能制造重复 id 或空 base_url。
    // 密钥就在这条记录自己身上，跟着一起没了——没有第二处要清。
    config.save(&path)?;
    state.lock()?.forget_provider(&id)?;

    // `config.json` 里若正拿它当默认 provider，顺手挪到还活着的第一个——
    // 否则新建会话会带着一个幽灵 provider 出门（前端只看得到"未配置模型"）。
    let config_path = state.paths.config_json();
    let mut app = crate::config::Config::load(&config_path)?;
    if app.defaults.provider == id {
        app.defaults.provider = config
            .providers
            .first()
            .map(|provider| provider.id.clone())
            .unwrap_or_default();
        app.defaults.model.clear(); // 换了 provider，旧 model id 未必还存在
        crate::config::write_json_pretty(&config_path, &app)?;
    }
    Ok(StatusCode::NO_CONTENT)
}

/// 编辑已有 provider 的连接信息（含密钥：`None` = 不动，`Some("")` = 清掉）。
/// `id` 是主键**不可改**：历史会话与 `config.json` 的默认值都按它引用。
/// 校验失败给 400（不是 500）——是调用方输入的问题。
async fn update_provider(
    State(state): State<AppState>,
    Path(id): Path<String>,
    Json(req): Json<UpdateProviderReq>,
) -> Result<Json<ProviderView>, ApiError> {
    let path = state.paths.providers_json();
    let mut config = ProvidersConfig::load(&path)?;

    let provider = config
        .providers
        .iter_mut()
        .find(|provider| provider.id == id)
        .ok_or_else(ApiError::not_found)?;
    if let Some(base_url) = req.base_url {
        provider.base_url = base_url.trim().to_owned();
    }
    if let Some(headers) = req.headers {
        provider.headers = headers;
    }
    if let Some(name) = req.name {
        provider.name = clean_name(Some(name));
    }
    if let Some(api_key) = req.api_key {
        // 三态：没给 = 上面根本没进来（不动）；空串 = 清除；其它 = 设置
        provider.api_key = api_key.trim().to_owned();
    }

    if let Err(e) = config.validate() {
        return Err(ApiError::bad_request(&e.to_string()));
    }
    config.save(&path)?;

    Ok(Json(one_view(&state, &config, &id)?))
}

#[derive(Deserialize)]
pub struct CreateAgentReq {
    /// 名称是**唯一的人类句柄**——id 由后端生成，不接受指定。
    pub name: String,
    #[serde(default)]
    pub system_prompt: String,
}

#[derive(Deserialize)]
pub struct UpdateAgentReq {
    pub name: Option<String>,
    pub system_prompt: Option<String>,
    /// 把它设为 `agents.json` 的 `default_agent`（新建会话默认用它）。
    #[serde(default)]
    pub make_default: bool,
    /// 改 id = **重命名**：连同 `default_agent` 与所有会话的引用一起搬（空串 = 不改）。
    pub new_id: Option<String>,
}

async fn list_agents(State(state): State<AppState>) -> Result<Json<AgentsConfig>, ApiError> {
    let config = AgentsConfig::load(&state.paths.agents_json())?;
    // 对外给"生效列表"：含内置默认 agent（文件里没有 default 时补上）
    Ok(Json(AgentsConfig {
        agents: config.effective(),
        ..config
    }))
}

/// 新建 agent 预设。`agents.json` 是唯一会被程序写入的用户文件，且只在被调用时写。
async fn create_agent(
    State(state): State<AppState>,
    Json(req): Json<CreateAgentReq>,
) -> Result<(StatusCode, Json<Agent>), ApiError> {
    let name = req.name.trim().to_owned();
    if name.is_empty() {
        // id 不可见之后，名称就是它唯一的句柄：没名字的 agent 在界面上没法认。
        return Err(ApiError::bad_request("agent 名称不能为空"));
    }

    let path = state.paths.agents_json();
    let mut config = AgentsConfig::load(&path)?;
    // id 由后端生成（UUIDv7，和会话/消息同一套）：不透明、不可变、不会撞车。
    // 想手写 id 的老路仍然通——直接写进 `agents.json`，加载器照收（历史数据不受影响）。
    let agent = Agent {
        id: Uuid::now_v7().to_string(),
        name,
        system_prompt: req.system_prompt,
        ..Default::default()
    };
    config.agents.push(agent.clone());
    if let Err(e) = config.validate() {
        return Err(ApiError::bad_request(&e.to_string()));
    }
    config.save(&path)?;
    Ok((StatusCode::CREATED, Json(agent)))
}

async fn update_agent(
    State(state): State<AppState>,
    Path(id): Path<String>,
    Json(req): Json<UpdateAgentReq>,
) -> Result<Json<Agent>, ApiError> {
    let path = state.paths.agents_json();
    let mut config = AgentsConfig::load(&path)?;

    // 定位：文件里没有这个 id、但能 resolve 出来（内置默认 agent）→ 先把它落地到文件再改。
    if config.get(&id).is_none() {
        if let Some(builtin) = config.resolve(&id) {
            config.agents.insert(0, builtin);
        }
    }
    if config.get(&id).is_none() {
        return Err(ApiError::not_found());
    }

    // 改 id = **重命名**：库里的会话引用先搬，再动文件。
    //
    // 顺序是刻意的——库改了、文件写失败，还能再跑一次（那时旧 id 仍在文件里）；
    // 反过来先把旧 id 从文件里抹掉，就再也找不到它，会话会永久指向一个不存在的 agent
    // （而 `resolve()` 找不到只是静默回空提示词，不报错——那是最难查的一类问题）。
    let mut current = id.clone();
    if let Some(new_id) = req.new_id {
        let new_id = new_id.trim().to_owned();
        if !new_id.is_empty() && new_id != current {
            if config.get(&new_id).is_some() {
                return Err(ApiError::conflict("已有同名 agent"));
            }
            state.lock()?.rename_agent_references(&current, &new_id)?;
            let agent = config
                .agents
                .iter_mut()
                .find(|agent| agent.id == current)
                .ok_or_else(ApiError::not_found)?;
            agent.id = new_id.clone();
            if config.default_agent == current {
                config.default_agent = new_id.clone();
            }
            current = new_id;
        }
    }

    let agent = config
        .agents
        .iter_mut()
        .find(|agent| agent.id == current)
        .ok_or_else(ApiError::not_found)?;
    if let Some(name) = req.name {
        agent.name = name;
    }
    if let Some(prompt) = req.system_prompt {
        agent.system_prompt = prompt;
    }
    if req.make_default {
        config.default_agent = current.clone();
    }
    let updated = agent.clone();
    config.validate()?;
    config.save(&path)?;
    Ok(Json(updated))
}


async fn delete_agent(
    State(state): State<AppState>,
    Path(id): Path<String>,
) -> Result<StatusCode, ApiError> {
    let path = state.paths.agents_json();
    let mut config = AgentsConfig::load(&path)?;
    let before = config.agents.len();
    config.agents.retain(|agent| agent.id != id);
    if config.agents.len() == before {
        // 内置默认 agent 不在文件里，删了也会立刻回来——明确拒绝，别让用户以为删掉了
        if config.resolve(&id).is_some() {
            return Err(ApiError::bad_request("内置默认 agent 不能删除"));
        }
        return Err(ApiError::not_found());
    }
    if config.default_agent == id {
        config.default_agent = config
            .agents
            .first()
            .map(|agent| agent.id.clone())
            .unwrap_or_default();
    }
    config.save(&path)?;
    Ok(StatusCode::NO_CONTENT)
}

/// 调试页用：后端自述状态。结构上就没有密钥字段。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DebugState {
    pub version: String,
    pub auth_enabled: bool,
    pub config_dir: String,
    pub data_dir: String,
    pub db_path: String,
    pub counts: store::StoreStats,
    pub chat_title_chars: usize,
    pub default_provider: String,
    pub default_model: String,
    pub default_agent: String,
    pub providers_configured: usize,
    pub agents_configured: usize,
}

/// 原始配置文件内容（仅白名单内的几个）。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RawFile {
    pub name: String,
    pub text: String,
    pub modified_ms: Option<i64>,
}

/// 白名单：只有这三个文件能被调试页读到；`providers.json` 里的密钥在吐出去之前会被打码。
const DEBUG_FILES: [&str; 3] = ["config.json", "providers.json", "agents.json"];

async fn debug_state(State(state): State<AppState>) -> Result<Json<DebugState>, ApiError> {
    let config = Config::load(&state.paths.config_json())?;
    let providers = ProvidersConfig::load(&state.paths.providers_json())?;
    let agents = AgentsConfig::load(&state.paths.agents_json())?;
    let counts = state.lock()?.stats()?;

    Ok(Json(DebugState {
        version: env!("CARGO_PKG_VERSION").to_owned(),
        auth_enabled: state.auth_token.is_some(),
        config_dir: state.paths.config_dir.display().to_string(),
        data_dir: state.paths.data_dir.display().to_string(),
        db_path: state.paths.database().display().to_string(),
        counts,
        chat_title_chars: config.chat.title_chars,
        default_provider: config.defaults.provider.clone(),
        default_model: config.defaults.model.clone(),
        default_agent: config.defaults.agent.clone(),
        providers_configured: providers.providers.len(),
        agents_configured: agents.agents.len(),
    }))
}

/// 调试页读文件：`providers.json` 里的 `api_key` **先抹掉**。
///
/// 密钥和 provider 住在一起，而这个接口是"把文件原样吐出来"——不打码就等于把凭据
/// 递给任何能访问这个后端的人（默认没配鉴权，CORS 又是全开）。
fn redact_api_keys(text: &str) -> String {
    let Ok(mut value) = serde_json::from_str::<serde_json::Value>(text) else {
        // 解析不了（手写写坏了）就整份不打：调试页看不见内容，总比漏出密钥强
        return "（文件不是合法 JSON，内容已隐去）".to_owned();
    };
    if let Some(providers) = value.get_mut("providers").and_then(|v| v.as_array_mut()) {
        for provider in providers {
            let has_key = provider
                .get("api_key")
                .and_then(|key| key.as_str())
                .is_some_and(|key| !key.is_empty());
            if has_key {
                provider["api_key"] = serde_json::Value::String("***".to_owned());
            }
        }
    }
    serde_json::to_string_pretty(&value).unwrap_or_else(|_| "（打码失败，内容已隐去）".to_owned())
}

async fn debug_file(
    State(state): State<AppState>,
    Path(name): Path<String>,
) -> Result<Json<RawFile>, ApiError> {
    if !DEBUG_FILES.contains(&name.as_str()) {
        return Err(ApiError::bad_request(
            "只允许读取 config.json / providers.json / agents.json",
        ));
    }
    let path = state.paths.config_dir.join(&name);
    let raw = std::fs::read_to_string(&path).unwrap_or_default();
    let text = if name == "providers.json" {
        redact_api_keys(&raw)
    } else {
        raw
    };
    let modified_ms = std::fs::metadata(&path)
        .and_then(|meta| meta.modified())
        .ok()
        .and_then(|time| time.duration_since(std::time::UNIX_EPOCH).ok())
        .map(|d| d.as_millis() as i64);
    Ok(Json(RawFile {
        name,
        text,
        modified_ms,
    }))
}

async fn require_bearer(
    State(state): State<AppState>,
    headers: HeaderMap,
    request: axum::extract::Request,
    next: Next,
) -> Response {
    if let Some(expected) = &state.auth_token {
        let supplied = headers
            .get(axum::http::header::AUTHORIZATION)
            .and_then(|v| v.to_str().ok())
            .and_then(|v| v.strip_prefix("Bearer "));
        if supplied != Some(expected.as_str()) {
            return ApiError::unauthorized().into_response();
        }
    }
    next.run(request).await
}

pub struct ApiError {
    status: StatusCode,
    code: &'static str,
    message: String,
}

impl ApiError {
    fn not_found() -> Self {
        Self {
            status: StatusCode::NOT_FOUND,
            code: "not_found",
            message: "会话不存在".to_owned(),
        }
    }

    fn bad_request(message: &str) -> Self {
        Self {
            status: StatusCode::BAD_REQUEST,
            code: "invalid",
            message: message.to_owned(),
        }
    }

    fn unauthorized() -> Self {
        Self {
            status: StatusCode::UNAUTHORIZED,
            code: "unauthorized",
            message: "缺少或无效的 Bearer token".to_owned(),
        }
    }

    fn conflict(message: &str) -> Self {
        Self {
            status: StatusCode::CONFLICT,
            code: "conflict",
            message: message.to_owned(),
        }
    }

    fn internal(message: &str) -> Self {
        Self {
            status: StatusCode::INTERNAL_SERVER_ERROR,
            code: "internal",
            message: message.to_owned(),
        }
    }
}

impl From<store::Error> for ApiError {
    fn from(e: store::Error) -> Self {
        match e {
            store::Error::NotFound => Self::not_found(),
            store::Error::Invalid(message) => Self::bad_request(&message),
            store::Error::Db(e) => {
                eprintln!("store error: {e}");
                Self::internal("存储错误")
            }
        }
    }
}

impl From<chat::Error> for ApiError {
    /// 502：错在上游，不是调用方。上游的原话要一路带到前端——"上游失败"这种话没用。
    fn from(e: chat::Error) -> Self {
        Self {
            status: StatusCode::BAD_GATEWAY,
            code: "upstream",
            message: e.to_string(),
        }
    }
}

impl From<config::Error> for ApiError {
    fn from(e: config::Error) -> Self {
        eprintln!("config error: {e}");
        Self {
            status: StatusCode::INTERNAL_SERVER_ERROR,
            code: "config",
            message: e.to_string(),
        }
    }
}

impl From<providers::Error> for ApiError {
    fn from(e: providers::Error) -> Self {
        Self {
            status: StatusCode::BAD_GATEWAY,
            code: "upstream",
            message: e.to_string(),
        }
    }
}

impl IntoResponse for ApiError {
    fn into_response(self) -> Response {
        let body = Json(serde_json::json!({
            "error": { "code": self.code, "message": self.message }
        }));
        (self.status, body).into_response()
    }
}

#[cfg(test)]
mod tests {
    use axum::body::Body;
    use axum::http::Request;
    use http_body_util::BodyExt;
    use std::path::PathBuf;
    use tower::ServiceExt;

    use super::*;
    use crate::config::Config;

    fn app(auth_token: Option<&str>) -> Router {
        let mut config = Config::default();
        config.server.auth_token = auth_token.map(str::to_owned);
        let paths = Paths {
            config_dir: PathBuf::from("/tmp/microchat-test-no-files"),
            data_dir: PathBuf::from("/tmp/microchat-test-no-files"),
        };
        router(AppState::new(
            Store::open_in_memory().unwrap(),
            &config,
            paths,
        ))
    }

    /// 临时配置目录（内含 providers/agents 文件）+ 路由。
    fn app_with_files(files: &[(&str, &str)]) -> (Router, PathBuf) {
        app_with_env(None, files)
    }

    /// 同上，但可带鉴权口令。
    fn app_with_env(auth_token: Option<&str>, files: &[(&str, &str)]) -> (Router, PathBuf) {
        let dir = std::env::temp_dir().join(format!("microchat-api-{}", Uuid::now_v7()));
        std::fs::create_dir_all(&dir).unwrap();
        for (name, content) in files {
            std::fs::write(dir.join(name), content).unwrap();
        }
        let paths = Paths {
            config_dir: dir.clone(),
            data_dir: dir.clone(),
        };
        let mut config = Config::default();
        config.server.auth_token = auth_token.map(str::to_owned);
        let app = router(AppState::new(
            Store::open_in_memory().unwrap(),
            &config,
            paths,
        ));
        (app, dir)
    }

    /// 起一个假上游，返回其 `/v1` 地址。
    async fn fake_upstream(status: StatusCode, body: &'static str) -> String {
        let app = Router::new().route("/v1/models", get(move || async move { (status, body) }));
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        tokio::spawn(async move {
            axum::serve(listener, app).await.unwrap();
        });
        format!("http://{addr}/v1")
    }

    /// 起一个"会拖一会儿再回答"的假上游——用来观察 `pending`、409 与「停止」。
    async fn slow_upstream(delay: std::time::Duration, reply: &'static str) -> String {
        let app = Router::new().route(
            "/v1/chat/completions",
            post(move || async move {
                tokio::time::sleep(delay).await;
                Json(serde_json::json!({
                    "choices": [ { "message": { "role": "assistant", "content": reply } } ]
                }))
            }),
        );
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        tokio::spawn(async move {
            axum::serve(listener, app).await.unwrap();
        });
        format!("http://{addr}/v1")
    }

    async fn send(app: &Router, req: Request<Body>) -> (StatusCode, String) {
        let resp = app.clone().oneshot(req).await.unwrap();
        let status = resp.status();
        let body = resp.into_body().collect().await.unwrap().to_bytes();
        (status, String::from_utf8_lossy(&body).into_owned())
    }

    fn json_req(method: &str, uri: &str, body: &str) -> Request<Body> {
        Request::builder()
            .method(method)
            .uri(uri)
            .header("content-type", "application/json")
            .body(Body::from(body.to_owned()))
            .unwrap()
    }

    fn get_req(uri: &str) -> Request<Body> {
        Request::builder().uri(uri).body(Body::empty()).unwrap()
    }

    async fn create(app: &Router, body: &str) -> Conversation {
        let (status, created) = send(app, json_req("POST", "/api/v1/conversations", body)).await;
        assert_eq!(status, StatusCode::CREATED);
        serde_json::from_str(&created).unwrap()
    }

    /// 从 `…/conversations/<uuid>/…` 里抠出会话 id。
    fn conversation_of(uri: &str) -> Uuid {
        uri.split('/')
            .find_map(|part| Uuid::parse_str(part).ok())
            .expect("uri 里该有会话 id")
    }

    /// 轮询到这一轮不再是 `pending` / `streaming`。dummy 与 fallback 是瞬间回的，
    /// 真上游才需要等——这里给 10 秒余量。
    async fn settled(app: &Router, conversation: Uuid) -> TurnStatus {
        let uri = format!("/api/v1/conversations/{conversation}/status");
        for _ in 0..1000 {
            let (status, body) = send(app, get_req(&uri)).await;
            assert_eq!(status, StatusCode::OK);
            let turn: TurnStatus = serde_json::from_str(&body).unwrap();
            if !turn.phase.is_busy() {
                return turn;
            }
            tokio::time::sleep(std::time::Duration::from_millis(10)).await;
        }
        panic!("这一轮 10 秒还没落地");
    }

    async fn messages(app: &Router, conversation: Uuid) -> Vec<Message> {
        let uri = format!("/api/v1/conversations/{conversation}/messages");
        let (status, body) = send(app, get_req(&uri)).await;
        assert_eq!(status, StatusCode::OK);
        serde_json::from_str(&body).unwrap()
    }

    /// 测试口径的"一轮对话"。
    #[derive(Debug, Clone)]
    struct ChatTurn {
        user: Message,
        assistant: Message,
        backend: String,
    }

    /// 发一条（`uri` 给 `/messages` 或 `/resend`）并**等这一轮落定**，把最后两条消息
    /// 折算回"一轮对话"的样子。
    ///
    /// 接口本身是"202 受理 + 后台生成 + 轮询状态"——回复不再随 POST 一起回来。
    /// 上游失败会让这里 panic（终态不是 `idle`）；要断言失败就自己用 `settled()`。
    async fn turn(app: &Router, uri: &str, body: &str) -> ChatTurn {
        let conversation = conversation_of(uri);
        let (code, text) = send(app, json_req("POST", uri, body)).await;
        assert_eq!(code, StatusCode::ACCEPTED, "受理该是 202：{text}");
        let accepted: TurnAccepted = serde_json::from_str(&text).unwrap();
        let status = settled(app, conversation).await;
        assert_eq!(status.phase, Phase::Idle, "这一轮没跑成：{status:?}");
        let messages = messages(app, conversation).await;
        assert!(messages.len() >= 2, "至少该有问与答");
        let assistant = messages.last().cloned().unwrap();
        let user = messages[messages.len() - 2].clone();
        ChatTurn {
            user,
            assistant,
            backend: accepted.backend,
        }
    }

    #[tokio::test]
    async fn create_and_list_conversation_roundtrip() {
        let app = app(None);
        let conv = create(
            &app,
            r#"{"provider":"openrouter","model":"deepseek/deepseek-v4-flash","system_prompt":"你是助手"}"#,
        )
        .await;
        assert_eq!(conv.provider, "openrouter");
        assert_eq!(conv.system_prompt, "你是助手");

        let (status, body) = send(&app, get_req("/api/v1/conversations")).await;
        assert_eq!(status, StatusCode::OK);
        let list: Vec<Conversation> = serde_json::from_str(&body).unwrap();
        assert_eq!(list.len(), 1);
        assert_eq!(list[0].id, conv.id);
    }

    #[tokio::test]
    async fn send_message_persists_both_sides_and_autotitles() {
        let app = app(None);
        let conv = create(&app, r#"{"provider":"x","model":"y"}"#).await;
        let uri = format!("/api/v1/conversations/{}/messages", conv.id);

        let turn = turn(&app, &uri, r#"{"content":"第一句话"}"#).await;
        assert_eq!(turn.user.role, Role::User);
        assert_eq!(turn.user.content, "第一句话");
        assert_eq!(turn.backend, "fallback", "providers.json 里没有 x，应回落到 fallback");
        assert_eq!(turn.assistant.role, Role::Assistant);
        assert_eq!(turn.assistant.content, "未配置模型");

        let (status, body) = send(&app, get_req(&uri)).await;
        assert_eq!(status, StatusCode::OK);
        let msgs: Vec<Message> = serde_json::from_str(&body).unwrap();
        assert_eq!(msgs.len(), 2, "用户消息与助手回复都要在库里");
        assert_eq!(msgs[0].content, "第一句话");
        assert_eq!(msgs[1].content, "未配置模型");

        // 标题自动生成 = 首条用户消息（title_chars 范围内原样）
        let (status, body) = send(&app, get_req("/api/v1/conversations")).await;
        assert_eq!(status, StatusCode::OK);
        let list: Vec<Conversation> = serde_json::from_str(&body).unwrap();
        assert_eq!(list[0].title, "第一句话");
    }

    #[tokio::test]
    async fn create_conversation_uses_config_defaults_when_omitted() {
        let app = app(None);
        let conv = create(&app, "{}").await;
        assert_eq!(conv.provider, "dummy", "省略时默认落到 dummy");
        assert_eq!(conv.model, crate::chat::DUMMY_MODEL_ID);
    }

    #[tokio::test]
    async fn dummy_model_replies_with_the_test_sentence() {
        let (app, dir) = app_with_files(&[(
            "providers.json",
            r#"{ "providers": [ { "id": "dummy", "kind": "dummy" } ] }"#,
        )]);
        let conv = create(&app, "{}").await;
        let uri = format!("/api/v1/conversations/{}/messages", conv.id);

        let turn = turn(&app, &uri, r#"{"content":"在吗"}"#).await;
        assert_eq!(turn.backend, "dummy");
        assert_eq!(turn.assistant.content, "（测试用空模型）");

        let _ = std::fs::remove_dir_all(&dir);
    }

    #[tokio::test]
    async fn state_block_becomes_variables_but_stays_in_the_archive() {
        // 底子住在 system prompt 里（和正文同一套 `<state>` 语法）。放在 agent 的提示词上，
        // 用同一 agent 的会话共享它 —— 所以它算"全局"。
        let (app, dir) = app_with_env(
            None,
            &[(
                "agents.json",
                r#"{ "agents": [ { "id": "default", "name": "默认助手",
                     "system_prompt": "你是客栈老板。\n<state>set 季节 = 初冬</state>" } ] }"#,
            )],
        );
        // 不写会话级提示词 → 用 agent 的
        let conv = create(&app, r#"{"provider":"x","model":"y"}"#).await;
        let uri = format!("/api/v1/conversations/{}/messages", conv.id);
        // 老写法 `setglobal` 仍留在正文里：它现在只报警告，不再往全局写东西。
        let content = "我要住店\n<state>\nset HP = 12\nsetglobal 季节 = 初冬\n</state>";
        let body = serde_json::json!({ "content": content }).to_string();
        let (status, _) = send(&app, json_req("POST", &uri, &body)).await;
        assert_eq!(status, StatusCode::ACCEPTED, "受理是 202：生成在后台跑");
        // 等这一轮落定再检查派生物（变量、出站），否则是在跟后台赛跑
        let _ = settled(&app, conv.id).await;

        // 1) 变量 = 提示词里的底子（全局）+ 正文现演出来的本会话改动
        let vars_uri = format!("/api/v1/conversations/{}/variables", conv.id);
        let (status, body) = send(&app, get_req(&vars_uri)).await;
        assert_eq!(status, StatusCode::OK);
        let view: crate::vars::VariableView = serde_json::from_str(&body).unwrap();
        assert_eq!(view.global.len(), 1, "底子来自 agent 的提示词");
        assert_eq!(view.global[0].key, "季节");
        assert!(view.global[0].message_id.is_none(), "底子不来自某条消息");
        assert_eq!(view.session.len(), 1, "只有 set HP —— setglobal 已废弃");
        assert!(view.session[0].message_id.is_some(), "能追到是哪句写的");
        assert_eq!(view.effective.get("HP").map(String::as_str), Some("12"));
        assert_eq!(view.effective.get("季节").map(String::as_str), Some("初冬"));

        // 会话自己写了提示词（覆盖 agent 的）→ 底子换成它写的，而且只算本会话
        let own = create(
            &app,
            r#"{"provider":"x","model":"y","system_prompt":"<state>set 季节 = 盛夏</state>你是掌柜。"}"#,
        )
        .await;
        let (_, body) = send(
            &app,
            get_req(&format!("/api/v1/conversations/{}/variables", own.id)),
        )
        .await;
        let view: crate::vars::VariableView = serde_json::from_str(&body).unwrap();
        assert!(view.global.is_empty(), "会话自带的提示词不产生全局行");
        assert_eq!(view.effective.get("季节").map(String::as_str), Some("盛夏"));

        // 2) 存档：消息正文里标签**原样保留**
        let (_, body) = send(&app, get_req(&uri)).await;
        let messages: Vec<Message> = serde_json::from_str(&body).unwrap();
        assert!(
            messages[0].content.contains("<state>"),
            "标签是存档的一部分：{}",
            messages[0].content
        );

        // 3) 出站：历史剔除标签，系统提示词后面挂当前变量表
        let outgoing_uri = format!("/api/v1/conversations/{}/outgoing", conv.id);
        let (status, body) = send(&app, get_req(&outgoing_uri)).await;
        assert_eq!(status, StatusCode::OK);
        let outgoing: Vec<crate::vars::Outgoing> = serde_json::from_str(&body).unwrap();
        assert_eq!(outgoing[0].role, crate::vars::OutgoingRole::System);
        assert!(outgoing[0].content.starts_with("你是客栈老板。"));
        assert!(
            !outgoing[0].content.contains("<state>"),
            "提示词里的状态块也要剔除，别把标签喂给模型"
        );
        assert!(outgoing[0].content.contains("当前变量:"));
        assert!(outgoing[0].content.contains("HP = 12"));
        assert_eq!(outgoing[1].content, "我要住店");
        assert!(
            !outgoing.iter().any(|item| item.content.contains("<state>")),
            "任何一条都不该带着标签发出去"
        );
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[tokio::test]
    async fn models_endpoint_is_flat_and_respects_auth() {
        let (app, dir) = app_with_env(
            Some("s3cret"),
            &[(
                "providers.json",
                r#"{ "providers": [ { "id": "dummy", "kind": "dummy", "name": "假模型" } ] }"#,
            )],
        );

        let (status, _) = send(&app, get_req("/api/v1/models")).await;
        assert_eq!(status, StatusCode::UNAUTHORIZED, "没带口令应当 401");

        let authed = Request::builder()
            .uri("/api/v1/models")
            .header("authorization", "Bearer s3cret")
            .body(Body::empty())
            .unwrap();
        let (status, body) = send(&app, authed).await;
        assert_eq!(status, StatusCode::OK);
        let list: Vec<crate::registry::ModelListItem> = serde_json::from_str(&body).unwrap();
        assert_eq!(list.len(), 1, "dummy 的虚拟模型也在扁平列表里");
        assert_eq!(list[0].provider, "dummy");
        assert_eq!(list[0].provider_name.as_deref(), Some("假模型"));
        assert_eq!(list[0].upstream_id, crate::chat::DUMMY_MODEL_ID);
        assert_eq!(list[0].name, "Dummy（测试用空模型）");

        let _ = std::fs::remove_dir_all(&dir);
    }

    /// 删中间一句：剩下的顺序不变（靠 rowid），那句写下的状态操作也不再生效。
    /// 重新发送 = 接一个**兄弟**：旧回复留着，切回去就能看见。
    #[tokio::test]
    async fn resend_branches_a_sibling_and_switching_leaf_rewrites_the_path() {
        let app = app(None);
        let conv = create(&app, r#"{"provider":"dummy","model":"dummy"}"#).await;
        let uri = format!("/api/v1/conversations/{}/messages", conv.id);
        let resend = format!("/api/v1/conversations/{}/resend", conv.id);
        let branches = format!("/api/v1/conversations/{}/branches", conv.id);
        let patches = format!("/api/v1/conversations/{}", conv.id);

        let first = turn(&app, &uri, r#"{"content":"在吗"}"#).await;

        let second = turn(&app, &resend, "").await;
        assert_eq!(second.user.id, first.user.id, "用户那条不该被动");
        assert_ne!(second.assistant.id, first.assistant.id);
        assert_eq!(
            second.assistant.parent_id,
            Some(first.user.id),
            "新回复挂在同一条用户消息下面 —— 旧回复的兄弟"
        );

        // 当前路径仍是两条（走的是新那条），但两条回复都在树上
        let (_, body) = send(&app, get_req(&uri)).await;
        let path: Vec<Message> = serde_json::from_str(&body).unwrap();
        assert_eq!(path.len(), 2);
        assert_eq!(path[1].id, second.assistant.id);

        // 兄弟信息：那条用户消息下面有两条回复，当前走第 2 条
        let (_, body) = send(&app, get_req(&branches)).await;
        let info: BTreeMap<Uuid, crate::store::BranchInfo> = serde_json::from_str(&body).unwrap();
        let branch = &info[&second.assistant.id];
        assert_eq!((branch.index, branch.total), (2, 2), "当前是第 2/2 条");
        assert!(branch.siblings.contains(&first.assistant.id), "旧回复还在兄弟里");

        // 切回旧的：路径换成那条旧回复
        let body = serde_json::json!({ "current_leaf": first.assistant.id }).to_string();
        let (status, _) = send(&app, json_req("PATCH", &patches, &body)).await;
        assert_eq!(status, StatusCode::OK);
        let (_, body) = send(&app, get_req(&uri)).await;
        let path: Vec<Message> = serde_json::from_str(&body).unwrap();
        assert_eq!(path[1].id, first.assistant.id, "切回旧回复了");
        let (_, body) = send(&app, get_req(&branches)).await;
        let info: BTreeMap<Uuid, crate::store::BranchInfo> = serde_json::from_str(&body).unwrap();
        assert_eq!(info[&first.assistant.id].index, 1, "现在轮到第 1 条");

        // 空会话重发 → 400
        let empty = create(&app, r#"{"provider":"dummy","model":"dummy"}"#).await;
        let (status, _) = send(
            &app,
            json_req("POST", &format!("/api/v1/conversations/{}/resend", empty.id), ""),
        )
        .await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
    }

    /// 只允许在**最新那句**上切分支：切到旧消息 = 把对话倒回去（变量也会倒）。
    #[tokio::test]
    async fn switching_leaf_is_only_allowed_between_siblings() {
        let app = app(None);
        let conv = create(&app, r#"{"provider":"dummy","model":"dummy"}"#).await;
        let uri = format!("/api/v1/conversations/{}/messages", conv.id);
        let resend = format!("/api/v1/conversations/{}/resend", conv.id);
        let patches = format!("/api/v1/conversations/{}", conv.id);

        let first = turn(&app, &uri, r#"{"content":"第一句"}"#).await;
        let second = turn(&app, &resend, "").await;
        // 再往下说一句，让上面那对回复变成"旧消息"
        let third = turn(&app, &uri, r#"{"content":"第二句"}"#).await;
        // 尾巴上再重发一次：它和 third.assistant 是兄弟，现在切它俩是允许的
        let fourth = turn(&app, &resend, "").await;

        let switch = |leaf: Uuid| serde_json::json!({ "current_leaf": leaf }).to_string();

        // 尾巴的兄弟 → 允许
        let (status, _) = send(&app, json_req("PATCH", &patches, &switch(third.assistant.id))).await;
        assert_eq!(status, StatusCode::OK);
        let (_, body) = send(&app, get_req(&uri)).await;
        let path: Vec<Message> = serde_json::from_str(&body).unwrap();
        assert_eq!(path.last().unwrap().id, third.assistant.id);
        assert_eq!(path.len(), 4, "路径长度不变（只是换了一条候选）");
        let _ = fourth;

        // 旧消息（第一句的回复）→ 400
        let (status, body) = send(&app, json_req("PATCH", &patches, &switch(first.assistant.id))).await;
        assert_eq!(status, StatusCode::BAD_REQUEST, "旧消息上不给切分支：{body}");
        let (status, _) = send(&app, json_req("PATCH", &patches, &switch(second.assistant.id))).await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
    }

    /// 「删除全部」：某个上文下的几条回复一次清光（连各自子树），只留下那句问话。
    #[tokio::test]
    async fn deleting_all_siblings_leaves_only_the_question() {
        let app = app(None);
        let conv = create(&app, r#"{"provider":"dummy","model":"dummy"}"#).await;
        let uri = format!("/api/v1/conversations/{}/messages", conv.id);
        let resend = format!("/api/v1/conversations/{}/resend", conv.id);

        let first = turn(&app, &uri, r#"{"content":"你是谁？"}"#).await;
        let second = turn(&app, &resend, "").await;

        let (status, body) = send(
            &app,
            Request::builder()
                .method("DELETE")
                .uri(format!("{uri}/{}/siblings", second.assistant.id))
                .body(Body::empty())
                .unwrap(),
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(
            serde_json::from_str::<serde_json::Value>(&body).unwrap()["deleted"],
            2,
            "两条回复一起清"
        );

        let (_, body) = send(&app, get_req(&uri)).await;
        let path: Vec<Message> = serde_json::from_str(&body).unwrap();
        assert_eq!(path.len(), 1, "只剩那句问话");
        assert_eq!(path[0].id, first.user.id);
        assert_eq!(path[0].role, crate::model::Role::User);

        // 清完就能直接重发（leaf 落在问话上）
        let again = turn(&app, &resend, "").await;
        assert_eq!(again.assistant.parent_id, Some(first.user.id));
    }

    /// 删掉**当前那条**回复时，路径该退到它**还活着的最新兄弟**上，
    /// 而不是塌回到用户那句话（那样看起来就像"几个分支一起没了"）。
    #[tokio::test]
    async fn deleting_the_current_reply_falls_back_to_its_newest_sibling() {
        let app = app(None);
        let conv = create(&app, r#"{"provider":"dummy","model":"dummy"}"#).await;
        let uri = format!("/api/v1/conversations/{}/messages", conv.id);
        let resend = format!("/api/v1/conversations/{}/resend", conv.id);
        let branches = format!("/api/v1/conversations/{}/branches", conv.id);

        let first = turn(&app, &uri, r#"{"content":"你是谁？"}"#).await;
        // 再点两次「重新发送」→ 三条兄弟回复 1/2/3
        let second = turn(&app, &resend, "").await;
        let third = turn(&app, &resend, "").await;

        let (_, body) = send(&app, get_req(&branches)).await;
        let info: BTreeMap<Uuid, crate::store::BranchInfo> = serde_json::from_str(&body).unwrap();
        assert_eq!((info[&third.assistant.id].index, info[&third.assistant.id].total), (3, 3));

        // 在"我是3"上按删除
        let (status, body) = send(
            &app,
            Request::builder()
                .method("DELETE")
                .uri(format!("{uri}/{}", third.assistant.id))
                .body(Body::empty())
                .unwrap(),
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(
            serde_json::from_str::<serde_json::Value>(&body).unwrap()["deleted"],
            1,
            "只该删掉那一条回复"
        );

        // 路径应当落在"我是2"上；另外两条回复仍在树上
        let (_, body) = send(&app, get_req(&uri)).await;
        let path: Vec<Message> = serde_json::from_str(&body).unwrap();
        assert_eq!(path.len(), 2, "问句 + 还活着的那条回复");
        assert_eq!(path[1].id, second.assistant.id, "退到最新的兄弟上");

        let (_, body) = send(&app, get_req(&branches)).await;
        let info: BTreeMap<Uuid, crate::store::BranchInfo> = serde_json::from_str(&body).unwrap();
        assert_eq!(info[&second.assistant.id].total, 2, "树上是 2/2：1 和 2 都还在");
        assert!(info[&second.assistant.id].siblings.contains(&first.assistant.id));
    }

    /// 删中间那条 = 连它整棵子树一起没；leaf 退回父亲；变量只按当前路径算。
    #[tokio::test]
    async fn deleting_a_subtree_reports_the_count_and_variables_follow_the_path() {
        let app = app(None);
        let conv = create(&app, r#"{"provider":"dummy","model":"dummy"}"#).await;
        let uri = format!("/api/v1/conversations/{}/messages", conv.id);
        let resend = format!("/api/v1/conversations/{}/resend", conv.id);

        let first = turn(&app, &uri, r#"{"content":"<state>set HP = 12</state>开场"}"#).await;
        // 再来一条兄弟回复：树上现在有 1 条用户 + 2 条回复
        let _second = turn(&app, &resend, "").await;

        let (status, body) = send(
            &app,
            Request::builder()
                .method("DELETE")
                .uri(format!("{uri}/{}", first.user.id))
                .body(Body::empty())
                .unwrap(),
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(
            serde_json::from_str::<serde_json::Value>(&body).unwrap()["deleted"],
            3,
            "1 条用户消息 + 它的 2 条回复，一起走"
        );

        let (_, body) = send(&app, get_req(&uri)).await;
        assert!(serde_json::from_str::<Vec<Message>>(&body).unwrap().is_empty());

        // 变量只按当前路径算：那句 <state> 不在了，HP 也跟着没了
        let (_, body) = send(
            &app,
            get_req(&format!("/api/v1/conversations/{}/variables", conv.id)),
        )
        .await;
        let view: crate::vars::VariableView = serde_json::from_str(&body).unwrap();
        assert!(!view.effective.contains_key("HP"), "{:?}", view.effective);
    }

    #[tokio::test]
    async fn deleting_a_message_drops_its_state_ops_and_keeps_order() {
        let app = app(None);
        let conv = create(&app, r#"{"provider":"x","model":"y"}"#).await;
        let uri = format!("/api/v1/conversations/{}/messages", conv.id);

        // 两句用户话，状态写在**第二句**上：删它只会带走它自己的子树
        // （第一句也要等它跑完——同一会话不许两轮并行，忙的时候发是 409）
        let _first = turn(&app, &uri, r#"{"content":"开场白"}"#).await;
        let second = turn(&app, &uri, r#"{"content":"<state>set HP = 12</state>继续往里走"}"#).await;

        let vars_uri = format!("/api/v1/conversations/{}/variables", conv.id);
        let (_, body) = send(&app, get_req(&vars_uri)).await;
        let before: serde_json::Value = serde_json::from_str(&body).unwrap();
        assert_eq!(before["effective"]["HP"], "12", "先确认这条状态确实生效了");

        let delete = |message: Uuid| {
            Request::builder()
                .method("DELETE")
                .uri(format!("{uri}/{message}"))
                .body(Body::empty())
                .unwrap()
        };
        let (status, body) = send(&app, delete(second.user.id)).await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(
            serde_json::from_str::<serde_json::Value>(&body).unwrap()["deleted"],
            2,
            "第二句用户消息 + 它的回复，一起走"
        );

        // 剩下的路径：第一对还在；leaf 退回到被删那条的父亲上
        let (_, body) = send(&app, get_req(&uri)).await;
        let msgs: Vec<crate::model::Message> = serde_json::from_str(&body).unwrap();
        assert_eq!(msgs.len(), 2, "前面那对没被牵连");
        assert_eq!(msgs[0].content, "开场白");
        assert_eq!(msgs[1].role, crate::model::Role::Assistant);

        // 被删那句写下的状态跟着走（变量只按当前路径算）
        let (_, body) = send(&app, get_req(&vars_uri)).await;
        let after: serde_json::Value = serde_json::from_str(&body).unwrap();
        assert!(
            after["effective"].get("HP").is_none(),
            "被删那句写下的 HP 不该再生效：{after}"
        );

        // 再删一次 → 404（跨会话删也走这条）
        let (status, _) = send(&app, delete(second.user.id)).await;
        assert_eq!(status, StatusCode::NOT_FOUND);
    }

    /// 「设为默认」：写进 `agents.json` 的 `default_agent`，之后新建会话用它。
    #[tokio::test]
    async fn an_agent_can_be_made_the_default() {
        let (app, dir) = app_with_files(&[(
            "agents.json",
            r#"{ "default_agent": "", "agents": [] }"#,
        )]);
        let (_, body) = send(&app, json_req("POST", "/api/v1/agents", r#"{"name":"跑团 GM"}"#)).await;
        let agent: Agent = serde_json::from_str(&body).unwrap();

        let (status, _) = send(
            &app,
            json_req(
                "PATCH",
                &format!("/api/v1/agents/{}", agent.id),
                r#"{"make_default":true}"#,
            ),
        )
        .await;
        assert_eq!(status, StatusCode::OK);

        let (_, body) = send(&app, get_req("/api/v1/agents")).await;
        let config: AgentsConfig = serde_json::from_str(&body).unwrap();
        assert_eq!(config.default_agent, agent.id, "默认 agent 要落到文件里");

        // 新建会话就跟着用它
        let conv = create(&app, r#"{"provider":"dummy","model":"dummy"}"#).await;
        assert_eq!(conv.agent_id, agent.id);

        let _ = std::fs::remove_dir_all(&dir);
    }

    /// 改 agent 的 id = 重命名：会话引用和 default_agent 一起搬，冲突要拦住。
    #[tokio::test]
    async fn renaming_an_agent_moves_every_reference() {
        let (app, dir) = app_with_env(
            None,
            &[(
                "agents.json",
                r#"{ "default_agent": "ze",
                     "agents": [ { "id": "ze", "name": "测试助手", "system_prompt": "旧提示词" } ] }"#,
            )],
        );
        let conv = create(
            &app,
            r#"{"provider":"dummy","model":"dummy","agent_id":"ze"}"#,
        )
        .await;
        assert_eq!(conv.agent_id, "ze");

        let (status, body) = send(
            &app,
            json_req("PATCH", "/api/v1/agents/ze", r#"{"new_id":"跑团","name":"跑团主持人"}"#),
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        let agent: Agent = serde_json::from_str(&body).unwrap();
        assert_eq!(agent.id, "跑团");
        assert_eq!(agent.name, "跑团主持人");

        // 会话引用跟着搬（软引用，只能由这一处维护）
        let (_, body) = send(&app, get_req("/api/v1/conversations")).await;
        let list: Vec<Conversation> = serde_json::from_str(&body).unwrap();
        assert_eq!(list[0].agent_id, "跑团", "会话引用要跟着搬");

        // default_agent 跟着搬
        let (_, body) = send(&app, get_req("/api/v1/agents")).await;
        let config: AgentsConfig = serde_json::from_str(&body).unwrap();
        assert_eq!(config.default_agent, "跑团");

        // 旧 id 不存在了
        let (status, _) = send(
            &app,
            json_req("PATCH", "/api/v1/agents/ze", r#"{"name":"x"}"#),
        )
        .await;
        assert_eq!(status, StatusCode::NOT_FOUND);

        // 改到已存在的 id → 409
        let (status, body) = send(
            &app,
            json_req("POST", "/api/v1/agents", r#"{"name":"另一个"}"#),
        )
        .await;
        assert_eq!(status, StatusCode::CREATED);
        let other: Agent = serde_json::from_str(&body).unwrap();
        let body = serde_json::json!({ "new_id": other.id }).to_string();
        let (status, _) = send(
            &app,
            json_req("PATCH", "/api/v1/agents/跑团", &body),
        )
        .await;
        assert_eq!(status, StatusCode::CONFLICT, "撞到已存在的 id 要 409");

        let _ = std::fs::remove_dir_all(&dir);
    }

    #[tokio::test]
    async fn variables_endpoint_rejects_unknown_conversation() {
        let app = app(None);
        let uri = format!("/api/v1/conversations/{}/variables", Uuid::now_v7());
        let (status, _) = send(&app, get_req(&uri)).await;
        assert_eq!(status, StatusCode::NOT_FOUND);
    }

    #[tokio::test]
    async fn editing_a_message_rewrites_its_state_block() {
        let app = app(None);
        let conv = create(&app, r#"{"provider":"x","model":"y"}"#).await;
        let uri = format!("/api/v1/conversations/{}/messages", conv.id);
        let body = serde_json::json!({ "content": "开始\n<state>\nset HP = 12\n</state>" }).to_string();
        let turn = turn(&app, &uri, &body).await;

        // 改成正 3：旧操作要被**换掉**，不是叠加
        let edit_uri = format!("/api/v1/conversations/{}/messages/{}", conv.id, turn.user.id);
        let edited = "改口\n<state>\nset HP = 3\n</state>";
        let body = serde_json::json!({ "content": edited }).to_string();
        let (status, response) = send(&app, json_req("PATCH", &edit_uri, &body)).await;
        assert_eq!(status, StatusCode::OK);
        let updated: Message = serde_json::from_str(&response).unwrap();
        assert_eq!(updated.content, edited);
        assert_eq!(updated.id, turn.user.id);

        let vars_uri = format!("/api/v1/conversations/{}/variables", conv.id);
        let (_, response) = send(&app, get_req(&vars_uri)).await;
        let view: crate::vars::VariableView = serde_json::from_str(&response).unwrap();
        assert_eq!(view.session.len(), 1, "旧操作要被换掉，不是叠加");
        assert_eq!(view.effective.get("HP").map(String::as_str), Some("3"));

        // 出站用新正文，且标签仍被剔除
        let outgoing_uri = format!("/api/v1/conversations/{}/outgoing", conv.id);
        let (_, response) = send(&app, get_req(&outgoing_uri)).await;
        let outgoing: Vec<crate::vars::Outgoing> = serde_json::from_str(&response).unwrap();
        assert_eq!(outgoing[1].content, "改口");
    }

    /// 编造一个不存在/不属于该会话的消息 id → 404，不能跨会话改。
    #[tokio::test]
    async fn editing_rejects_foreign_or_unknown_messages() {
        let app = app(None);
        let first = create(&app, r#"{"provider":"x","model":"y"}"#).await;
        let second = create(&app, r#"{"provider":"x","model":"y"}"#).await;
        let uri = format!("/api/v1/conversations/{}/messages", first.id);
        let turn = turn(&app, &uri, r#"{"content":"原话"}"#).await;

        let body = serde_json::json!({ "content": "被篡改" }).to_string();
        let foreign = format!(
            "/api/v1/conversations/{}/messages/{}",
            second.id, turn.user.id
        );
        let (status, _) = send(&app, json_req("PATCH", &foreign, &body)).await;
        assert_eq!(status, StatusCode::NOT_FOUND);

        let unknown = format!("/api/v1/conversations/{}/messages/{}", first.id, Uuid::now_v7());
        let (status, _) = send(&app, json_req("PATCH", &unknown, &body)).await;
        assert_eq!(status, StatusCode::NOT_FOUND);

        // 原消息没被动过
        let (_, response) = send(&app, get_req(&uri)).await;
        let messages: Vec<Message> = serde_json::from_str(&response).unwrap();
        assert_eq!(messages[0].content, "原话");
    }

    /// 配了真模型但上游连不上：**受理是 202，失败落在这一轮的状态里**（原话带着）。
    /// 绝不假装有人在说话，也不留半条占位空消息。
    #[tokio::test]
    async fn unreachable_upstream_lands_in_the_error_state() {
        let (app, dir) = app_with_env(
            None,
            &[(
                "providers.json",
                r#"{ "providers": [ { "id": "local", "kind": "openai-compat", "base_url": "http://127.0.0.1:1/v1" } ] }"#,
            )],
        );
        let conv = create(&app, r#"{"provider":"local","model":"m"}"#).await;
        let uri = format!("/api/v1/conversations/{}/messages", conv.id);

        let (status, _) = send(&app, json_req("POST", &uri, r#"{"content":"在吗"}"#)).await;
        assert_eq!(status, StatusCode::ACCEPTED, "受理先于生成：失败由状态报");

        let turn = settled(&app, conv.id).await;
        assert_eq!(turn.phase, Phase::Error, "上游连不上 → 这一轮的结局是失败");
        let message = turn.error.clone().unwrap_or_default();
        assert!(
            message.contains("上游"),
            "要把上游的原因带出来：{message}"
        );
        assert_eq!(turn.message_id, None, "失败后不该再指着某条消息");

        // 助手消息（占位那条）不该留下
        let msgs = messages(&app, conv.id).await;
        assert_eq!(msgs.len(), 1, "只有用户那条");
        let _ = std::fs::remove_dir_all(&dir);
    }

    /// 生成期间：状态是 `pending`（带 `message_id`），会话列表里也看得见；
    /// 此时再发 → 409（**不排队**），且不会多留一条用户消息。
    #[tokio::test]
    async fn a_running_turn_is_pending_and_blocks_a_second_send() {
        let base = slow_upstream(std::time::Duration::from_millis(400), "慢回复").await;
        let providers = format!(
            r#"{{ "providers": [ {{ "id": "slow", "kind": "openai-compat", "base_url": "{base}" }} ] }}"#
        );
        let (app, dir) = app_with_files(&[("providers.json", &providers)]);
        let conv = create(&app, r#"{"provider":"slow","model":"m"}"#).await;
        let uri = format!("/api/v1/conversations/{}/messages", conv.id);

        let (status, body) = send(&app, json_req("POST", &uri, r#"{"content":"在吗"}"#)).await;
        assert_eq!(status, StatusCode::ACCEPTED);
        let accepted: TurnAccepted = serde_json::from_str(&body).unwrap();
        assert_eq!(accepted.turn.phase, Phase::Pending, "受理那一刻还没有输出");
        assert_eq!(accepted.backend, "openai-completion");
        let placeholder = accepted.turn.message_id.expect("受理时就有占位消息 id");

        let status_uri = format!("/api/v1/conversations/{}/status", conv.id);
        let (_, body) = send(&app, get_req(&status_uri)).await;
        let running: TurnStatus = serde_json::from_str(&body).unwrap();
        assert_eq!(running.phase, Phase::Pending);
        assert_eq!(running.message_id, Some(placeholder));

        let (_, body) = send(&app, get_req("/api/v1/conversations")).await;
        let list: Vec<ConversationView> = serde_json::from_str(&body).unwrap();
        assert!(list[0].turn.phase.is_busy(), "列表里带着这一轮的状态");

        // 生成期间**库里什么都没有**：这条回复要等整段拿到才落库
        let mid = messages(&app, conv.id).await;
        assert_eq!(mid.len(), 1, "只有用户那条，生成中的回复还不在库里");
        assert_eq!(mid[0].content, "在吗");

        let (status, body) = send(&app, json_req("POST", &uri, r#"{"content":"喂"}"#)).await;
        assert_eq!(status, StatusCode::CONFLICT, "忙的时候再发该给 409：{body}");

        let turn = settled(&app, conv.id).await;
        assert_eq!(turn.phase, Phase::Idle);
        assert_eq!(turn.chars, "慢回复".chars().count());
        let msgs = messages(&app, conv.id).await;
        assert_eq!(msgs.len(), 2, "被 409 挡下的那条不许留下");
        assert_eq!(msgs[1].id, placeholder, "回复填进的就是那条占位消息");

        let _ = std::fs::remove_dir_all(&dir);
    }

    /// 「停止」：摘登记 + 删掉还空着的占位消息；用户那句留着（接着可以重发）。
    /// 幂等——没在跑时再按一次也是 200。
    #[tokio::test]
    async fn stop_drops_the_placeholder_and_keeps_the_user_message() {
        let base = slow_upstream(std::time::Duration::from_secs(30), "来不及了").await;
        let providers = format!(
            r#"{{ "providers": [ {{ "id": "slow", "kind": "openai-compat", "base_url": "{base}" }} ] }}"#
        );
        let (app, dir) = app_with_files(&[("providers.json", &providers)]);
        let conv = create(&app, r#"{"provider":"slow","model":"m"}"#).await;
        let uri = format!("/api/v1/conversations/{}/messages", conv.id);
        let stop_uri = format!("/api/v1/conversations/{}/stop", conv.id);

        let (status, _) = send(&app, json_req("POST", &uri, r#"{"content":"在吗"}"#)).await;
        assert_eq!(status, StatusCode::ACCEPTED);

        let (status, body) = send(&app, json_req("POST", &stop_uri, "")).await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(
            serde_json::from_str::<serde_json::Value>(&body).unwrap()["stopped"],
            true
        );

        let turn = settled(&app, conv.id).await;
        assert_eq!(turn.phase, Phase::Idle, "停完就是空闲");
        let msgs = messages(&app, conv.id).await;
        assert_eq!(msgs.len(), 1, "占位消息被收拾掉，用户那句还在");
        assert_eq!(msgs[0].content, "在吗");

        let (status, body) = send(&app, json_req("POST", &stop_uri, "")).await;
        assert_eq!(status, StatusCode::OK, "没在跑也是 200");
        assert_eq!(
            serde_json::from_str::<serde_json::Value>(&body).unwrap()["stopped"],
            false
        );

        let _ = std::fs::remove_dir_all(&dir);
    }

    /// provider 的建、改名、删一条链走通；删的时候密钥与派生数据一起清。
    #[tokio::test]
    async fn provider_can_be_created_renamed_and_deleted() {
        const TOKEN: &str = "tok";
        let (app, dir) = app_with_env(
            Some(TOKEN),
            &[("providers.json", r#"{ "providers": [] }"#)],
        );
        let authed = |method: &str, uri: &str, body: &str| {
            let mut builder = Request::builder()
                .method(method)
                .uri(uri)
                .header("authorization", format!("Bearer {TOKEN}"));
            if !body.is_empty() {
                builder = builder.header("content-type", "application/json");
            }
            builder.body(Body::from(body.to_owned())).unwrap()
        };

        let (status, body) = send(
            &app,
            authed(
                "POST",
                "/api/v1/providers",
                r#"{"id":"open","kind":"openai-compat","base_url":"http://127.0.0.1:1/v1","name":"我的上游","api_key":"sk-x"}"#,
            ),
        )
        .await;
        assert_eq!(status, StatusCode::CREATED);
        assert_eq!(
            serde_json::from_str::<serde_json::Value>(&body).unwrap()["name"],
            "我的上游"
        );

        // 只改显示名：base_url 不许被顺手清掉
        let (status, body) = send(
            &app,
            authed("PATCH", "/api/v1/providers/open", r#"{"name":"改名了"}"#),
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        let patched: serde_json::Value = serde_json::from_str(&body).unwrap();
        assert_eq!(patched["name"], "改名了");
        assert_eq!(patched["base_url"], "http://127.0.0.1:1/v1");

        let (_, body) = send(&app, authed("GET", "/api/v1/providers", "")).await;
        let list: Vec<serde_json::Value> = serde_json::from_str(&body).unwrap();
        assert_eq!(list.len(), 1);
        assert_eq!(list[0]["has_key"], true);

        let (status, _) = send(&app, authed("DELETE", "/api/v1/providers/open", "")).await;
        assert_eq!(status, StatusCode::NO_CONTENT);
        let (status, _) = send(&app, authed("DELETE", "/api/v1/providers/open", "")).await;
        assert_eq!(status, StatusCode::NOT_FOUND, "再删一次应当 404");
        let (_, body) = send(&app, authed("GET", "/api/v1/providers", "")).await;
        let list: Vec<serde_json::Value> = serde_json::from_str(&body).unwrap();
        assert!(list.is_empty());

        // 密钥确实清了：同名重建不该自带 key
        send(
            &app,
            authed(
                "POST",
                "/api/v1/providers",
                r#"{"id":"open","kind":"openai-compat","base_url":"http://127.0.0.1:1/v1"}"#,
            ),
        )
        .await;
        let (_, body) = send(&app, authed("GET", "/api/v1/providers", "")).await;
        let list: Vec<serde_json::Value> = serde_json::from_str(&body).unwrap();
        assert_eq!(list[0]["has_key"], false, "删 provider 要连密钥一起清掉");
        assert_eq!(list[0]["name"], serde_json::Value::Null);

        let _ = std::fs::remove_dir_all(&dir);
    }

    #[tokio::test]
    async fn conversation_model_and_agent_can_be_switched() {
        let app = app(None);
        let conv = create(&app, "{}").await;
        assert_eq!(conv.provider, "dummy");
        let uri = format!("/api/v1/conversations/{}", conv.id);

        let (status, body) = send(
            &app,
            json_req("PATCH", &uri, r#"{"provider":"local","model":"some-model"}"#),
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        let updated: Conversation = serde_json::from_str(&body).unwrap();
        assert_eq!(
            (updated.provider.as_str(), updated.model.as_str()),
            ("local", "some-model")
        );

        let (status, body) = send(&app, json_req("PATCH", &uri, r#"{"agent_id":"跑团"}"#)).await;
        assert_eq!(status, StatusCode::OK);
        let updated: Conversation = serde_json::from_str(&body).unwrap();
        assert_eq!(updated.agent_id, "跑团");

        // 省略的字段不该被动
        let (status, body) = send(&app, json_req("PATCH", &uri, r#"{"title":"改个名"}"#)).await;
        assert_eq!(status, StatusCode::OK);
        let updated: Conversation = serde_json::from_str(&body).unwrap();
        assert_eq!(updated.title, "改个名");
        assert_eq!(updated.provider, "local");
        assert_eq!(updated.agent_id, "跑团");
    }

    #[tokio::test]
    async fn builtin_default_agent_is_listed_editable_but_not_deletable() {
        let (app, dir) = app_with_files(&[(
            "agents.json",
            r#"{ "default_agent": "default", "agents": [] }"#,
        )]);

        let (status, body) = send(&app, get_req("/api/v1/agents")).await;
        assert_eq!(status, StatusCode::OK);
        let config: AgentsConfig = serde_json::from_str(&body).unwrap();
        assert_eq!(config.agents.len(), 1, "文件为空时补上内置默认");
        let builtin = config.get("default").expect("内置默认 agent 应在列表里");
        assert_eq!(builtin.system_prompt, "You are a helpful assistant.");

        // 内置的删不掉
        let (status, _) = send(&app, json_req("DELETE", "/api/v1/agents/default", "")).await;
        assert_eq!(status, StatusCode::BAD_REQUEST);

        // 但能改：改它会把内置"落地"进文件
        let (status, body) = send(
            &app,
            json_req(
                "PATCH",
                "/api/v1/agents/default",
                r#"{"system_prompt":"You are a terse assistant."}"#,
            ),
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        let updated: Agent = serde_json::from_str(&body).unwrap();
        assert_eq!(updated.system_prompt, "You are a terse assistant.");
        let on_disk = std::fs::read_to_string(dir.join("agents.json")).unwrap();
        assert!(on_disk.contains("You are a terse assistant."), "编辑内置 agent 要落地");

        // 落地之后它已是普通 agent，可以删
        let (status, _) = send(&app, json_req("DELETE", "/api/v1/agents/default", "")).await;
        assert_eq!(status, StatusCode::NO_CONTENT);

        let _ = std::fs::remove_dir_all(&dir);
    }

    #[tokio::test]
    async fn rename_conversation_updates_title() {
        let app = app(None);
        let conv = create(&app, r#"{"provider":"x","model":"y"}"#).await;
        let uri = format!("/api/v1/conversations/{}", conv.id);

        let (status, body) = send(&app, json_req("PATCH", &uri, r#"{"title":"自定义标题"}"#)).await;
        assert_eq!(status, StatusCode::OK);
        let updated: Conversation = serde_json::from_str(&body).unwrap();
        assert_eq!(updated.title, "自定义标题");
    }

    #[tokio::test]
    async fn delete_conversation_is_idempotent_not_found() {
        let app = app(None);
        let conv = create(&app, r#"{"provider":"x","model":"y"}"#).await;
        let uri = format!("/api/v1/conversations/{}", conv.id);

        let (status, _) = send(&app, json_req("DELETE", &uri, "")).await;
        assert_eq!(status, StatusCode::NO_CONTENT);

        let (status, _) = send(&app, json_req("DELETE", &uri, "")).await;
        assert_eq!(status, StatusCode::NOT_FOUND);
    }

    #[tokio::test]
    async fn bearer_auth_enforced_when_configured() {
        let app = app(Some("s3cret"));

        let (status, _) = send(&app, get_req("/api/v1/conversations")).await;
        assert_eq!(status, StatusCode::UNAUTHORIZED);

        let req = Request::builder()
            .uri("/api/v1/conversations")
            .header("authorization", "Bearer s3cret")
            .body(Body::empty())
            .unwrap();
        let (status, _) = send(&app, req).await;
        assert_eq!(status, StatusCode::OK);
    }

    #[tokio::test]
    async fn errors_use_stable_code_body() {
        let app = app(None);
        let uri = format!("/api/v1/conversations/{}/messages", Uuid::now_v7());
        let (status, body) = send(&app, get_req(&uri)).await;

        assert_eq!(status, StatusCode::NOT_FOUND);
        let v: serde_json::Value = serde_json::from_str(&body).unwrap();
        assert_eq!(v["error"]["code"], "not_found");
    }

    #[tokio::test]
    async fn health_reports_version_and_respects_auth() {
        let plain = app(None);
        let (status, body) = send(&plain, get_req("/api/v1/health")).await;
        assert_eq!(status, StatusCode::OK);
        let v: serde_json::Value = serde_json::from_str(&body).unwrap();
        assert_eq!(v["status"], "ok");
        assert!(v["version"].is_string());

        let guarded = app(Some("s3cret"));
        let (status, _) = send(&guarded, get_req("/api/v1/health")).await;
        assert_eq!(status, StatusCode::UNAUTHORIZED, "探针也要过鉴权");
    }

    #[tokio::test]
    async fn providers_view_reads_config_and_never_leaks_the_key() {
        let (app, dir) = app_with_files(&[(
            "providers.json",
            r#"{ "providers": [ { "id": "local", "base_url": "http://127.0.0.1:9/v1",
                 "api_key": "sk-secret-value" } ] }"#,
        )]);

        let (status, body) = send(&app, get_req("/api/v1/providers")).await;
        assert_eq!(status, StatusCode::OK);
        let views: Vec<ProviderView> = serde_json::from_str(&body).unwrap();
        assert_eq!(views.len(), 1);
        assert_eq!(views[0].id, "local");
        assert!(views[0].has_key, "密钥就在配置里，视图只说配了没有");
        assert!(views[0].models.is_empty(), "未刷新时没有模型");
        assert!(!body.contains("sk-secret-value"), "响应绝不能带出密钥");

        let _ = std::fs::remove_dir_all(&dir);
    }

    #[tokio::test]
    async fn refresh_discovers_models_and_persists_them() {
        let upstream = fake_upstream(
            StatusCode::OK,
            r#"{"data":[{"id":"deepseek/deepseek-v4-flash","name":"DeepSeek V4 Flash"}]}"#,
        )
        .await;
        let (app, dir) = app_with_files(&[(
            "providers.json",
            &format!(r#"{{ "providers": [ {{ "id": "local", "base_url": "{upstream}" }} ] }}"#),
        )]);

        let (status, body) =
            send(&app, json_req("POST", "/api/v1/providers/local/refresh", "")).await;
        assert_eq!(status, StatusCode::OK);
        let view: ProviderView = serde_json::from_str(&body).unwrap();
        assert!(view.last_refresh_at.is_some());
        assert_eq!(view.models.len(), 1);
        assert_eq!(view.models[0].name, "DeepSeek V4 Flash");

        // 落库了：重新读取仍在
        let (_, body) = send(&app, get_req("/api/v1/providers")).await;
        let views: Vec<ProviderView> = serde_json::from_str(&body).unwrap();
        assert_eq!(views[0].models.len(), 1);

        let _ = std::fs::remove_dir_all(&dir);
    }

    #[tokio::test]
    async fn refresh_reports_upstream_failure_and_unknown_provider() {
        let upstream = fake_upstream(StatusCode::INTERNAL_SERVER_ERROR, "").await;
        let (app, dir) = app_with_files(&[(
            "providers.json",
            &format!(r#"{{ "providers": [ {{ "id": "local", "base_url": "{upstream}" }} ] }}"#),
        )]);

        let (status, body) =
            send(&app, json_req("POST", "/api/v1/providers/local/refresh", "")).await;
        assert_eq!(status, StatusCode::BAD_GATEWAY);
        let v: serde_json::Value = serde_json::from_str(&body).unwrap();
        assert_eq!(v["error"]["code"], "upstream");

        let (status, _) = send(&app, json_req("POST", "/api/v1/providers/nope/refresh", "")).await;
        assert_eq!(status, StatusCode::NOT_FOUND);

        let _ = std::fs::remove_dir_all(&dir);
    }

    #[tokio::test]
    async fn agent_crud_roundtrip_writes_config_file() {
        let (app, dir) = app_with_files(&[(
            "agents.json",
            r#"{ "default_agent": "default", "agents": [] }"#,
        )]);
        // 只给名称：id 由后端生成（UUIDv7）——不再由调用方指定
        let (status, body) = send(
            &app,
            json_req(
                "POST",
                "/api/v1/agents",
                r#"{"name":"跑团 GM","system_prompt":"你是 GM"}"#,
            ),
        )
        .await;
        assert_eq!(status, StatusCode::CREATED);
        let agent: Agent = serde_json::from_str(&body).unwrap();
        assert!(
            uuid::Uuid::parse_str(&agent.id).is_ok(),
            "id 应当是生成的 UUID：{}",
            agent.id
        );
        assert_eq!(agent.name, "跑团 GM");

        // id 不可见之后，名称是唯一句柄：没名字不给建
        let (status, _) = send(&app, json_req("POST", "/api/v1/agents", r#"{"name":"   "}"#)).await;
        assert_eq!(status, StatusCode::BAD_REQUEST);

        let uri = format!("/api/v1/agents/{}", agent.id);

        let (status, body) = send(
            &app,
            json_req("PATCH", &uri, r#"{"system_prompt":"你是严格的 GM"}"#),
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        let updated: Agent = serde_json::from_str(&body).unwrap();
        assert_eq!(updated.system_prompt, "你是严格的 GM");
        assert_eq!(updated.name, "跑团 GM", "未提供的字段保持不变");

        let on_disk = std::fs::read_to_string(dir.join("agents.json")).unwrap();
        assert!(on_disk.contains("你是严格的 GM"), "改动要落盘");

        let (status, _) = send(&app, json_req("DELETE", &uri, "")).await;
        assert_eq!(status, StatusCode::NO_CONTENT);
        let (status, _) = send(&app, json_req("DELETE", &uri, "")).await;
        assert_eq!(status, StatusCode::NOT_FOUND);

        let _ = std::fs::remove_dir_all(&dir);
    }

    #[tokio::test]
    async fn update_provider_edits_existing_only() {
        let (app, dir) = app_with_files(&[(
            "providers.json",
            r#"{ "providers": [ { "id": "local", "base_url": "http://127.0.0.1:9/v1" } ] }"#,
        )]);

        let (status, body) = send(
            &app,
            json_req(
                "PATCH",
                "/api/v1/providers/local",
                r#"{"base_url":"http://127.0.0.1:11434/v1","headers":{"X-Title":"microchat"}}"#,
            ),
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        let updated: ProviderConfig = serde_json::from_str(&body).unwrap();
        assert_eq!(updated.base_url, "http://127.0.0.1:11434/v1");
        assert_eq!(updated.headers["X-Title"], "microchat");

        let on_disk = std::fs::read_to_string(dir.join("providers.json")).unwrap();
        assert!(on_disk.contains("11434"), "改动要落盘");
        assert!(on_disk.contains("X-Title"));

        // 坏 base_url → 400（调用方输入问题，不是 500）
        let (status, body) = send(
            &app,
            json_req(
                "PATCH",
                "/api/v1/providers/local",
                r#"{"base_url":"127.0.0.1:11434"}"#,
            ),
        )
        .await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        assert_eq!(
            serde_json::from_str::<serde_json::Value>(&body).unwrap()["error"]["code"],
            "invalid"
        );

        // 不存在的 provider → 404
        let (status, _) = send(
            &app,
            json_req(
                "PATCH",
                "/api/v1/providers/nope",
                r#"{"base_url":"http://x/v1"}"#,
            ),
        )
        .await;
        assert_eq!(status, StatusCode::NOT_FOUND);

        let _ = std::fs::remove_dir_all(&dir);
    }

    /// 密钥和 provider 住在一起（`providers.json`）：写进去、权限收紧、**不回显**，
    /// 连调试页读那个文件也要先打码。
    #[tokio::test]
    async fn create_provider_keeps_the_key_with_the_provider_and_never_echoes_it() {
        let (app, dir) = app_with_files(&[]);

        let (status, body) = send(
            &app,
            json_req(
                "POST",
                "/api/v1/providers",
                r#"{"id":"local","kind":"openai-compat","base_url":"http://127.0.0.1:11434/v1","api_key":"sk-secret"}"#,
            ),
        )
        .await;
        assert_eq!(status, StatusCode::CREATED);
        assert!(!body.contains("sk-secret"), "响应不回显密钥：{body}");
        let created: ProviderView = serde_json::from_str(&body).unwrap();
        assert_eq!(created.id, "local");
        assert!(created.has_key, "只说配了没有");

        // providers.json 里带着密钥（就住这一处），权限收紧到 0600
        let providers_path = dir.join("providers.json");
        let providers = std::fs::read_to_string(&providers_path).unwrap();
        assert!(providers.contains("11434"));
        assert!(providers.contains("sk-secret"), "密钥和 provider 住一起");
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = std::fs::metadata(&providers_path)
                .unwrap()
                .permissions()
                .mode()
                & 0o777;
            assert_eq!(mode, 0o600, "含密钥的文件权限应为 0600");
        }
        assert!(
            !dir.join("secrets.json").exists(),
            "不再有第二个放密钥的文件"
        );

        // 视图只说"配了没有"，接口不回显
        let (_, body) = send(&app, get_req("/api/v1/providers")).await;
        let views: Vec<ProviderView> = serde_json::from_str(&body).unwrap();
        assert!(views[0].has_key);
        assert_eq!(views[0].kind, ProviderKind::OpenAiCompat);
        assert!(!body.contains("sk-secret"));

        // 调试页能读到这个文件（那是它的用处），但密钥要被打码
        let (status, body) = send(&app, get_req("/api/v1/debug/file/providers.json")).await;
        assert_eq!(status, StatusCode::OK);
        let raw: RawFile = serde_json::from_str(&body).unwrap();
        assert!(raw.text.contains("11434"), "别的字段照旧看得见");
        assert!(!raw.text.contains("sk-secret"), "调试页不许漏密钥：{}", raw.text);
        assert!(raw.text.contains("***"), "打码后要看得见'这儿原本有东西'");

        // 重复 id → 409
        let (status, _) = send(
            &app,
            json_req(
                "POST",
                "/api/v1/providers",
                r#"{"id":"local","kind":"openai-compat","base_url":"http://127.0.0.1:1/v1"}"#,
            ),
        )
        .await;
        assert_eq!(status, StatusCode::CONFLICT);

        let _ = std::fs::remove_dir_all(&dir);
    }

    #[tokio::test]
    async fn dummy_provider_needs_no_url_and_cannot_be_refreshed() {
        let (app, dir) = app_with_files(&[]);

        let (status, _) = send(
            &app,
            json_req("POST", "/api/v1/providers", r#"{"id":"offline","kind":"dummy"}"#),
        )
        .await;
        assert_eq!(status, StatusCode::CREATED);

        // dummy 填了 base_url → 400
        let (status, _) = send(
            &app,
            json_req(
                "POST",
                "/api/v1/providers",
                r#"{"id":"bad","kind":"dummy","base_url":"http://x/v1"}"#,
            ),
        )
        .await;
        assert_eq!(status, StatusCode::BAD_REQUEST);

        // dummy 没有上游可拉
        let (status, body) =
            send(&app, json_req("POST", "/api/v1/providers/offline/refresh", "")).await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        assert!(body.contains("dummy"));

        let _ = std::fs::remove_dir_all(&dir);
    }

    #[tokio::test]
    async fn probe_fetches_models_without_persisting() {
        let upstream = fake_upstream(
            StatusCode::OK,
            r#"{"data":[
                {"id":"local-model","name":"Local Model","context_length":8192,
                 "supported_parameters":["temperature","top_p"],
                 "default_parameters":{"temperature":1.0}}
            ]}"#,
        )
        .await;

        let (app, dir) = app_with_files(&[]);
        let body = format!(r#"{{ "base_url": "{upstream}", "api_key": "sk-test" }}"#);
        let (status, body) = send(&app, json_req("POST", "/api/v1/models/probe", &body)).await;
        assert_eq!(status, StatusCode::OK);

        let result: ProbeResult = serde_json::from_str(&body).unwrap();
        assert_eq!(result.models.len(), 1);
        assert_eq!(result.models[0].upstream_id, "local-model");
        assert_eq!(result.models[0].upstream_name.as_deref(), Some("Local Model"));
        assert_eq!(result.models[0].supported_parameters, ["temperature", "top_p"]);
        assert_eq!(result.models[0].default_parameters.as_ref().unwrap()["temperature"], 1.0);

        // 探测不落库
        let (_, state) = send(&app, get_req("/api/v1/debug/state")).await;
        assert_eq!(
            serde_json::from_str::<serde_json::Value>(&state).unwrap()["counts"]["models"],
            0,
            "probe 不该写库"
        );

        // 缺 scheme → 400
        let (status, _) = send(
            &app,
            json_req("POST", "/api/v1/models/probe", r#"{"base_url":"ftp://x/v1"}"#),
        )
        .await;
        assert_eq!(status, StatusCode::BAD_REQUEST);

        let _ = std::fs::remove_dir_all(&dir);
    }

    #[tokio::test]
    async fn debug_state_is_readable_and_hides_secrets() {
        let (app, dir) = app_with_env(
            Some("s3cret"),
            &[("providers.json", r#"{ "providers": [] }"#)],
        );

        let req = Request::builder()
            .uri("/api/v1/debug/state")
            .header("authorization", "Bearer s3cret")
            .body(Body::empty())
            .unwrap();
        let (status, body) = send(&app, req).await;
        assert_eq!(status, StatusCode::OK);

        let v: serde_json::Value = serde_json::from_str(&body).unwrap();
        assert_eq!(v["auth_enabled"], true);
        assert!(v["version"].is_string());
        assert!(v["db_path"].is_string());
        assert!(!body.contains("s3cret"), "调试状态绝不能带出密钥");

        let _ = std::fs::remove_dir_all(&dir);
    }

    #[tokio::test]
    async fn debug_reports_counts_and_only_whitelisted_files() {
        let (app, dir) = app_with_files(&[(
            "providers.json",
            r#"{ "providers": [ { "id": "local", "base_url": "http://127.0.0.1:9/v1",
                 "api_key": "sk-secret-value" } ] }"#,
        )]);

        // 计数随数据变化
        create(&app, r#"{"provider":"local","model":"m"}"#).await;
        let (_, body) = send(&app, get_req("/api/v1/debug/state")).await;
        let v: serde_json::Value = serde_json::from_str(&body).unwrap();
        assert_eq!(v["counts"]["conversations"], 1);
        assert_eq!(v["counts"]["messages"], 0);

        // 白名单内可读——但**密钥要打码**（这个接口会把文件原样吐出来）
        let (status, body) = send(&app, get_req("/api/v1/debug/file/providers.json")).await;
        assert_eq!(status, StatusCode::OK);
        let v: serde_json::Value = serde_json::from_str(&body).unwrap();
        let text = v["text"].as_str().unwrap();
        assert!(text.contains("providers"), "其余内容照旧看得见");
        assert!(text.contains("***"), "密钥位置留下打码痕迹");
        assert!(!body.contains("sk-secret-value"), "调试页不许漏密钥：{body}");
        assert!(v["modified_ms"].is_i64());

        // 未知文件一律拒绝
        let (status, _) = send(&app, get_req("/api/v1/debug/file/nope.json")).await;
        assert_eq!(status, StatusCode::BAD_REQUEST);

        let _ = std::fs::remove_dir_all(&dir);
    }
}
