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
    routing::{get, patch, post},
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

#[derive(Clone)]
pub struct AppState {
    store: Arc<Mutex<Store>>,
    auth_token: Option<String>,
    title_chars: usize,
    /// `providers.jsonc` / `agents.jsonc` / `secrets.json` 的位置：这些文件**每次请求现读**，
    /// 手改了文件立刻生效，无需重启。
    paths: Paths,
}

impl AppState {
    pub fn new(store: Store, config: &Config, paths: Paths) -> Self {
        Self {
            store: Arc::new(Mutex::new(store)),
            auth_token: config.server.auth_token.clone(),
            title_chars: config.chat.title_chars,
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
            patch(rename_conversation).delete(delete_conversation),
        )
        .route(
            "/conversations/{id}/messages",
            get(list_messages).post(send_message),
        )
        .route("/health", get(health))
        .route("/providers", get(list_providers).post(create_provider))
        .route("/models/probe", post(probe_models))
        .route("/providers/{id}", patch(update_provider))
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
    pub provider: String,
    pub model: String,
    #[serde(default)]
    pub system_prompt: String,
}

#[derive(Deserialize)]
pub struct RenameReq {
    pub title: String,
}

#[derive(Deserialize)]
pub struct SendReq {
    pub content: String,
}

async fn list_conversations(
    State(state): State<AppState>,
) -> Result<Json<Vec<Conversation>>, ApiError> {
    Ok(Json(state.lock()?.list_conversations()?))
}

async fn create_conversation(
    State(state): State<AppState>,
    Json(req): Json<CreateConversationReq>,
) -> Result<(StatusCode, Json<Conversation>), ApiError> {
    let conv = state
        .lock()?
        .create_conversation(&req.provider, &req.model, &req.system_prompt)?;
    Ok((StatusCode::CREATED, Json(conv)))
}

async fn rename_conversation(
    State(state): State<AppState>,
    Path(id): Path<Uuid>,
    Json(req): Json<RenameReq>,
) -> Result<Json<Conversation>, ApiError> {
    let mut store = state.lock()?;
    store.update_title(id, &req.title)?;
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

/// 一轮对话的结果：连"是谁回的"一并告诉客户端，省得它猜。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ChatTurn {
    pub user: Message,
    pub assistant: Message,
    pub backend: String,
}

/// 一轮对话：落用户消息 → 选对话后端 → 落助手回复。
///
/// 顺序是刻意的：**用户消息先落库**。若后端失败（例如尚未实现的 `openai-completion`
/// 回 501），用户的话仍在库里，重发或换模型不会丢上下文。
/// 流式（SSE）版本随 `openai-completion` 落地时替换本接口的响应形态。
async fn send_message(
    State(state): State<AppState>,
    Path(id): Path<Uuid>,
    Json(req): Json<SendReq>,
) -> Result<(StatusCode, Json<ChatTurn>), ApiError> {
    let providers = ProvidersConfig::load(&state.paths.providers_jsonc())?;
    let mut store = state.lock()?;

    let user = store.insert_message(id, Role::User, &req.content)?;
    let conversation = store.get_conversation(id)?.ok_or_else(ApiError::not_found)?;
    if conversation.title.is_empty() {
        store.update_title(id, &title_from(&req.content, state.title_chars))?;
    }

    let backend = chat::Backend::select(&conversation, &providers);
    let reply = backend.reply(&conversation, &req.content)?;
    let assistant = store.insert_message(id, Role::Assistant, &reply)?;

    Ok((
        StatusCode::CREATED,
        Json(ChatTurn {
            user,
            assistant,
            backend: backend.name().to_owned(),
        }),
    ))
}

/// 连通性探针：前端启动时用它判断"后端在不在、口令对不对"。
/// 同样过鉴权——401 与"连不上"是两种不同的失败，前端要能区分。
async fn health() -> Json<serde_json::Value> {
    Json(serde_json::json!({
        "status": "ok",
        "version": env!("CARGO_PKG_VERSION"),
    }))
}

/// provider × 模型 的合并视图（配置来自 `providers.jsonc`，模型来自数据库）。
async fn list_providers(
    State(state): State<AppState>,
) -> Result<Json<Vec<ProviderView>>, ApiError> {
    let config = ProvidersConfig::load(&state.paths.providers_jsonc())?;
    let secrets = config::load_secrets(&state.paths.secrets_json())?;
    let store = state.lock()?;
    Ok(Json(registry::views(&store, &config, &secrets)?))
}

/// 新建 provider。`kind` 决定要哪些字段：`dummy` 不该有 `base_url`，`openai-compat` 必须有。
/// `api_key` 写进 `secrets.json`，不进 `providers.jsonc`、不回显。
async fn create_provider(
    State(state): State<AppState>,
    Json(req): Json<CreateProviderReq>,
) -> Result<(StatusCode, Json<ProviderConfig>), ApiError> {
    let id = req.id.trim().to_owned();
    if id.is_empty() || id.chars().any(char::is_whitespace) {
        return Err(ApiError::bad_request("provider id 不能为空、不能含空白字符"));
    }

    let path = state.paths.providers_jsonc();
    let mut config = ProvidersConfig::load(&path)?;
    if config.get(&id).is_some() {
        return Err(ApiError::conflict("同名 provider 已存在"));
    }

    let provider = ProviderConfig {
        id,
        kind: req.kind,
        base_url: req.base_url.trim().to_owned(),
        headers: req.headers,
        ..Default::default()
    };
    config.providers.push(provider.clone());
    if let Err(e) = config.validate() {
        return Err(ApiError::bad_request(&e.to_string()));
    }

    config.save(&path)?;
    write_secret(&state, &provider.id, req.api_key)?;
    Ok((StatusCode::CREATED, Json(provider)))
}

/// 写 / 删 `secrets.json` 里某个 provider 的密钥；`None` 或空串 = 删除。
fn write_secret(state: &AppState, provider: &str, api_key: Option<String>) -> Result<(), ApiError> {
    let path = state.paths.secrets_json();
    let mut secrets = config::load_secrets(&path)?;
    match api_key {
        Some(key) if !key.is_empty() => {
            secrets.insert(provider.to_owned(), key);
        }
        _ => {
            secrets.remove(provider);
        }
    }
    config::save_secrets(&path, &secrets)?;
    Ok(())
}

/// 去上游拉 `/models` 并落库。HTTP 在**锁外**完成，避免持锁跨 await。
async fn refresh_provider(
    State(state): State<AppState>,
    Path(id): Path<String>,
) -> Result<Json<ProviderView>, ApiError> {
    let config = ProvidersConfig::load(&state.paths.providers_jsonc())?;
    let provider = config.get(&id).ok_or_else(ApiError::not_found)?;
    match provider.kind {
        ProviderKind::Dummy => {
            return Err(ApiError::bad_request("dummy provider 没有上游可拉取"));
        }
        ProviderKind::OpenAiCompat => {}
    }
    let secrets = config::load_secrets(&state.paths.secrets_json())?;
    let client = providers::Client::new(provider, secrets.get(&id).cloned())?;
    let discovered = client.list_models().await?;

    let mut store = state.lock()?;
    store.apply_discovery(&id, &discovered)?;
    drop(discovered);

    let refreshed = registry::views(&*store, &config, &secrets)?
        .into_iter()
        .find(|view| view.id == id);
    refreshed.map(Json).ok_or_else(ApiError::not_found)
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
    #[serde(default)]
    pub kind: ProviderKind,
    #[serde(default)]
    pub base_url: String,
    #[serde(default)]
    pub headers: BTreeMap<String, String>,
    /// 一次性传给后端写进 `secrets.json`；不落 `providers.jsonc`、不回显。
    #[serde(default)]
    pub api_key: Option<String>,
}

#[derive(Deserialize)]
pub struct UpdateProviderReq {
    pub base_url: Option<String>,
    pub headers: Option<BTreeMap<String, String>>,
    /// `None` = 不动；`Some("")` = 删除密钥；其它 = 设置。
    pub api_key: Option<String>,
}

/// 编辑已有 provider 的连接信息。`id` 是主键**不可改**：历史会话、`secrets.json`、
/// 默认配置都按它引用。校验失败给 400（不是 500）——是调用方输入的问题。
async fn update_provider(
    State(state): State<AppState>,
    Path(id): Path<String>,
    Json(req): Json<UpdateProviderReq>,
) -> Result<Json<ProviderConfig>, ApiError> {
    let path = state.paths.providers_jsonc();
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

    if let Err(e) = config.validate() {
        return Err(ApiError::bad_request(&e.to_string()));
    }
    config.save(&path)?;

    if let Some(api_key) = req.api_key {
        write_secret(&state, &id, Some(api_key))?;
    }

    config
        .get(&id)
        .cloned()
        .map(Json)
        .ok_or_else(ApiError::not_found)
}

#[derive(Deserialize)]
pub struct CreateAgentReq {
    pub id: String,
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub system_prompt: String,
}

#[derive(Deserialize)]
pub struct UpdateAgentReq {
    pub name: Option<String>,
    pub system_prompt: Option<String>,
}

async fn list_agents(State(state): State<AppState>) -> Result<Json<AgentsConfig>, ApiError> {
    Ok(Json(AgentsConfig::load(&state.paths.agents_jsonc())?))
}

/// 新建 agent 预设。`agents.jsonc` 是唯一会被程序写入的用户文件，且只在被调用时写。
async fn create_agent(
    State(state): State<AppState>,
    Json(req): Json<CreateAgentReq>,
) -> Result<(StatusCode, Json<Agent>), ApiError> {
    if req.id.trim().is_empty() || req.id.chars().any(char::is_whitespace) {
        return Err(ApiError::bad_request("agent id 不能为空、不能含空白字符"));
    }

    let path = state.paths.agents_jsonc();
    let mut config = AgentsConfig::load(&path)?;
    if config.get(&req.id).is_some() {
        return Err(ApiError::conflict("同名 agent 已存在"));
    }
    let agent = Agent {
        id: req.id,
        name: req.name,
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
    let path = state.paths.agents_jsonc();
    let mut config = AgentsConfig::load(&path)?;
    let agent = config
        .agents
        .iter_mut()
        .find(|agent| agent.id == id)
        .ok_or_else(ApiError::not_found)?;
    if let Some(name) = req.name {
        agent.name = name;
    }
    if let Some(prompt) = req.system_prompt {
        agent.system_prompt = prompt;
    }
    let updated = agent.clone();
    config.save(&path)?;
    Ok(Json(updated))
}

async fn delete_agent(
    State(state): State<AppState>,
    Path(id): Path<String>,
) -> Result<StatusCode, ApiError> {
    let path = state.paths.agents_jsonc();
    let mut config = AgentsConfig::load(&path)?;
    let before = config.agents.len();
    config.agents.retain(|agent| agent.id != id);
    if config.agents.len() == before {
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

/// 白名单：`secrets.json` **永远**不在其中。
const DEBUG_FILES: [&str; 3] = ["config.jsonc", "providers.jsonc", "agents.jsonc"];

async fn debug_state(State(state): State<AppState>) -> Result<Json<DebugState>, ApiError> {
    let config = Config::load(&state.paths.config_jsonc())?;
    let providers = ProvidersConfig::load(&state.paths.providers_jsonc())?;
    let agents = AgentsConfig::load(&state.paths.agents_jsonc())?;
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

async fn debug_file(
    State(state): State<AppState>,
    Path(name): Path<String>,
) -> Result<Json<RawFile>, ApiError> {
    if !DEBUG_FILES.contains(&name.as_str()) {
        return Err(ApiError::bad_request(
            "只允许读取 config.jsonc / providers.jsonc / agents.jsonc",
        ));
    }
    let path = state.paths.config_dir.join(&name);
    let text = std::fs::read_to_string(&path).unwrap_or_default();
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

    fn not_implemented(name: &str) -> Self {
        Self {
            status: StatusCode::NOT_IMPLEMENTED,
            code: "not_implemented",
            message: format!("{name} 对话后端尚未实现"),
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
    fn from(e: chat::Error) -> Self {
        match e {
            chat::Error::NotImplemented(name) => Self::not_implemented(name),
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

    /// 临时配置目录（内含 providers/agents/secrets 文件）+ 路由。
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

        let (status, body) = send(&app, json_req("POST", &uri, r#"{"content":"第一句话"}"#)).await;
        assert_eq!(status, StatusCode::CREATED);
        let turn: ChatTurn = serde_json::from_str(&body).unwrap();
        assert_eq!(turn.user.role, Role::User);
        assert_eq!(turn.user.content, "第一句话");
        assert_eq!(turn.backend, "dummy", "providers.jsonc 里没有 x，应回落到 dummy");
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
    async fn configured_model_reports_unimplemented_backend() {
        let (app, dir) = app_with_files(&[(
            "providers.jsonc",
            r#"{ "providers": [ { "id": "local", "base_url": "http://127.0.0.1:9/v1" } ] }"#,
        )]);
        let conv = create(&app, r#"{"provider":"local","model":"m"}"#).await;
        let uri = format!("/api/v1/conversations/{}/messages", conv.id);

        let (status, body) = send(&app, json_req("POST", &uri, r#"{"content":"你好"}"#)).await;
        assert_eq!(status, StatusCode::NOT_IMPLEMENTED);
        let v: serde_json::Value = serde_json::from_str(&body).unwrap();
        assert_eq!(v["error"]["code"], "not_implemented");
        assert!(v["error"]["message"].as_str().unwrap().contains("openai-completion"));

        // 失败不该吞掉用户的话
        let (_, body) = send(&app, get_req(&uri)).await;
        let msgs: Vec<Message> = serde_json::from_str(&body).unwrap();
        assert_eq!(msgs.len(), 1);
        assert_eq!(msgs[0].content, "你好");

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
    async fn providers_view_reads_config_and_never_leaks_secrets() {
        let (app, dir) = app_with_files(&[
            (
                "providers.jsonc",
                r#"{
                    // 注释也要能解析
                    "providers": [ { "id": "local", "base_url": "http://127.0.0.1:9/v1", } ],
                }"#,
            ),
            ("secrets.json", r#"{"local":"sk-secret-value"}"#),
        ]);

        let (status, body) = send(&app, get_req("/api/v1/providers")).await;
        assert_eq!(status, StatusCode::OK);
        let views: Vec<ProviderView> = serde_json::from_str(&body).unwrap();
        assert_eq!(views.len(), 1);
        assert_eq!(views[0].id, "local");
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
            "providers.jsonc",
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
            "providers.jsonc",
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
            "agents.jsonc",
            r#"{ "default_agent": "default", "agents": [] }"#,
        )]);
        // 「跑团」的 percent-encoding：URI 只允许 ASCII
        let uri = "/api/v1/agents/%E8%B7%91%E5%9B%A2";

        let (status, body) = send(
            &app,
            json_req(
                "POST",
                "/api/v1/agents",
                r#"{"id":"跑团","name":"跑团 GM","system_prompt":"你是 GM"}"#,
            ),
        )
        .await;
        assert_eq!(status, StatusCode::CREATED);
        let agent: Agent = serde_json::from_str(&body).unwrap();
        assert_eq!(agent.id, "跑团");

        let (status, _) = send(&app, json_req("POST", "/api/v1/agents", r#"{"id":"跑团"}"#)).await;
        assert_eq!(status, StatusCode::CONFLICT, "重复 id 应 409");

        let (status, body) = send(
            &app,
            json_req("PATCH", uri, r#"{"system_prompt":"你是严格的 GM"}"#),
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        let updated: Agent = serde_json::from_str(&body).unwrap();
        assert_eq!(updated.system_prompt, "你是严格的 GM");
        assert_eq!(updated.name, "跑团 GM", "未提供的字段保持不变");

        let on_disk = std::fs::read_to_string(dir.join("agents.jsonc")).unwrap();
        assert!(on_disk.contains("你是严格的 GM"), "改动要落盘");

        let (status, _) = send(&app, json_req("DELETE", uri, "")).await;
        assert_eq!(status, StatusCode::NO_CONTENT);
        let (status, _) = send(&app, json_req("DELETE", uri, "")).await;
        assert_eq!(status, StatusCode::NOT_FOUND);

        let _ = std::fs::remove_dir_all(&dir);
    }

    #[tokio::test]
    async fn update_provider_edits_existing_only() {
        let (app, dir) = app_with_files(&[(
            "providers.jsonc",
            r#"{
                // 手写注释
                "providers": [ { "id": "local", "base_url": "http://127.0.0.1:9/v1" } ],
            }"#,
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

        let on_disk = std::fs::read_to_string(dir.join("providers.jsonc")).unwrap();
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

    #[tokio::test]
    async fn create_provider_stores_key_separately_and_never_echoes_it() {
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
        assert!(!body.contains("sk-secret"), "响应不回显密钥");
        let created: ProviderConfig = serde_json::from_str(&body).unwrap();
        assert_eq!(created.kind, ProviderKind::OpenAiCompat);

        // providers.jsonc 只有连接信息
        let providers = std::fs::read_to_string(dir.join("providers.jsonc")).unwrap();
        assert!(providers.contains("11434"));
        assert!(!providers.contains("sk-secret"), "密钥不该进 providers.jsonc");

        // secrets.json 有密钥且权限收紧
        let secrets_path = dir.join("secrets.json");
        let secrets = std::fs::read_to_string(&secrets_path).unwrap();
        assert!(secrets.contains("sk-secret"));
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = std::fs::metadata(&secrets_path)
                .unwrap()
                .permissions()
                .mode()
                & 0o777;
            assert_eq!(mode, 0o600, "密钥文件权限应为 0600");
        }

        // 视图只说"配了没有"
        let (_, body) = send(&app, get_req("/api/v1/providers")).await;
        let views: Vec<ProviderView> = serde_json::from_str(&body).unwrap();
        assert!(views[0].has_key);
        assert_eq!(views[0].kind, ProviderKind::OpenAiCompat);
        assert!(!body.contains("sk-secret"));

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
            &[("providers.jsonc", r#"{ "providers": [] }"#)],
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
        let (app, dir) = app_with_files(&[
            ("providers.jsonc", r#"{ "providers": [] }"#),
            ("secrets.json", r#"{"x":"sk-secret-value"}"#),
        ]);

        // 计数随数据变化
        create(&app, r#"{"provider":"local","model":"m"}"#).await;
        let (_, body) = send(&app, get_req("/api/v1/debug/state")).await;
        let v: serde_json::Value = serde_json::from_str(&body).unwrap();
        assert_eq!(v["counts"]["conversations"], 1);
        assert_eq!(v["counts"]["messages"], 0);

        // 白名单内可读
        let (status, body) = send(&app, get_req("/api/v1/debug/file/providers.jsonc")).await;
        assert_eq!(status, StatusCode::OK);
        let v: serde_json::Value = serde_json::from_str(&body).unwrap();
        assert!(v["text"].as_str().unwrap().contains("providers"));
        assert!(v["modified_ms"].is_i64());

        // 密钥文件与未知文件一律拒绝，且不回显内容
        let (status, body) = send(&app, get_req("/api/v1/debug/file/secrets.json")).await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        assert!(!body.contains("sk-secret-value"));

        let (status, _) = send(&app, get_req("/api/v1/debug/file/nope.jsonc")).await;
        assert_eq!(status, StatusCode::BAD_REQUEST);

        let _ = std::fs::remove_dir_all(&dir);
    }
}
