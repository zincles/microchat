//! 前端与后端之间的唯一通道：命令进、事件出。
//!
//! UI 线程绝不做网络 IO。一个常驻线程跑 blocking reqwest，事件经 channel 回到 UI，
//! 并在投递后 `request_repaint()` 唤醒界面——界面只负责每帧 `try_recv`。

use std::collections::VecDeque;
use std::sync::mpsc::{self, Receiver, Sender};
use std::sync::{Arc, Mutex};

use eframe::egui;
use microchat::config::AgentsConfig;
use microchat::registry::ProviderView;
use microchat::server::{DebugState, RawFile};

pub enum Command {
    /// 连通性 + 口令检查（`GET /health`）。
    Check {
        base: String,
        token: Option<String>,
    },
    ListProviders {
        base: String,
        token: Option<String>,
    },
    ListConversations {
        base: String,
        token: Option<String>,
    },
    ListMessages {
        base: String,
        token: Option<String>,
        conversation: uuid::Uuid,
    },
    /// 拉某会话的变量：全局 + 本会话操作日志 + 生效值。
    ListVariables {
        base: String,
        token: Option<String>,
        conversation: uuid::Uuid,
    },
    /// 编辑一条消息。正文是存档，后端会按新正文**重算**它产生的变量操作。
    EditMessage {
        base: String,
        token: Option<String>,
        conversation: uuid::Uuid,
        message: uuid::Uuid,
        content: String,
    },
    /// 删除一条消息。它正文里写下的状态操作，后端会在同一事务里一起清掉。
    DeleteMessage {
        base: String,
        token: Option<String>,
        conversation: uuid::Uuid,
        message: uuid::Uuid,
    },
    CreateConversation {
        base: String,
        token: Option<String>,
    },
    DeleteConversation {
        base: String,
        token: Option<String>,
        conversation: uuid::Uuid,
    },
    SendMessage {
        base: String,
        token: Option<String>,
        conversation: uuid::Uuid,
        content: String,
    },
    UpdateConversation {
        base: String,
        token: Option<String>,
        conversation: uuid::Uuid,
        /// 只给要改的字段；`None` = 不动。
        provider: Option<String>,
        model: Option<String>,
        agent_id: Option<String>,
    },
    RefreshProvider {
        base: String,
        token: Option<String>,
        provider: String,
    },
    UpdateProvider {
        base: String,
        token: Option<String>,
        id: String,
        base_url: String,
        headers: std::collections::BTreeMap<String, String>,
        /// `None` = 不动；`Some("")` = 清除密钥；其它 = 设置。
        api_key: Option<String>,
    },
    CreateProvider {
        base: String,
        token: Option<String>,
        id: String,
        kind: microchat::config::ProviderKind,
        base_url: String,
        headers: std::collections::BTreeMap<String, String>,
        api_key: Option<String>,
    },
    ListAgents {
        base: String,
        token: Option<String>,
    },
    SaveAgent {
        base: String,
        token: Option<String>,
        id: String,
        name: String,
        system_prompt: String,
    },
    /// 删掉某条消息的**所有兄弟**（连同各自子树）——界面上那个「删除全部」。
    DeleteSiblings {
        base: String,
        token: Option<String>,
        conversation: uuid::Uuid,
        message: uuid::Uuid,
    },
    /// 切到某条消息所在的分支（落到它，再顺着最新的孩子走到末端）。
    SetLeaf {
        base: String,
        token: Option<String>,
        conversation: uuid::Uuid,
        leaf: uuid::Uuid,
    },
    /// 每条消息"在同龄兄弟里排第几、一共几条"。
    ListBranches {
        base: String,
        token: Option<String>,
        conversation: uuid::Uuid,
    },
    /// 重新发送：尾条是助手就长出一个兄弟，是用户消息就照它重发。
    Resend {
        base: String,
        token: Option<String>,
        conversation: uuid::Uuid,
    },
    /// 把某个 agent 设为 `agents.jsonc` 的 `default_agent`。
    MakeDefaultAgent {
        base: String,
        token: Option<String>,
        id: String,
    },
    CreateAgent {
        base: String,
        token: Option<String>,
        name: String,
        system_prompt: String,
    },
    DeleteAgent {
        base: String,
        token: Option<String>,
        id: String,
    },
    DebugState {
        base: String,
        token: Option<String>,
    },
    DebugFile {
        base: String,
        token: Option<String>,
        name: String,
    },
}

pub enum Event {
    /// `Ok` = 后端版本号。
    Checked(Result<String, String>),
    Providers(Result<Vec<ProviderView>, String>),
    Conversations(Result<Vec<microchat::model::Conversation>, String>),
    Messages {
        conversation: uuid::Uuid,
        result: Result<Vec<microchat::model::Message>, String>,
    },
    Variables {
        conversation: uuid::Uuid,
        result: Result<microchat::vars::VariableView, String>,
    },
    MessageEdited {
        conversation: uuid::Uuid,
        result: Result<microchat::model::Message, String>,
    },
    MessageDeleted {
        conversation: uuid::Uuid,
        message: uuid::Uuid,
        /// 后端回 `{ "deleted": n }`：删的是整棵子树，条数得让界面说清楚。
        result: Result<serde_json::Value, String>,
    },
    ConversationCreated(Result<microchat::model::Conversation, String>),
    ConversationDeleted {
        conversation: uuid::Uuid,
        result: Result<(), String>,
    },
    Turn {
        conversation: uuid::Uuid,
        result: Result<microchat::server::ChatTurn, String>,
    },
    /// 重新发送的结果：树上多了一条兄弟，界面应当重新拉消息与兄弟信息。
    Resent {
        conversation: uuid::Uuid,
        result: Result<microchat::server::ChatTurn, String>,
    },
    /// 切分支的结果：路径变了，界面应当重新拉消息。
    LeafSwitched {
        conversation: uuid::Uuid,
        result: Result<microchat::model::Conversation, String>,
    },
    Branches {
        conversation: uuid::Uuid,
        result: Result<std::collections::BTreeMap<uuid::Uuid, microchat::store::BranchInfo>, String>,
    },
    ConversationUpdated(Result<microchat::model::Conversation, String>),
    Agents(Result<AgentsConfig, String>),
    /// 写操作结果；UI 收到 `Ok` 后重新拉取列表。
    AgentWritten(Result<(), String>),
    ProviderWritten(Result<(), String>),
    DebugState(Result<DebugState, String>),
    DebugFile(Result<RawFile, String>),
}

pub struct Client {
    tx: Sender<Command>,
    rx: Receiver<Event>,
    /// 请求日志（环形，给调试页看）。
    log: Arc<Mutex<VecDeque<String>>>,
}

const LOG_CAP: usize = 100;

impl Client {
    pub fn spawn(ctx: egui::Context) -> Self {
        let (tx, cmd_rx) = mpsc::channel::<Command>();
        let (evt_tx, rx) = mpsc::channel::<Event>();
        let log = Arc::new(Mutex::new(VecDeque::new()));
        let worker_log = Arc::clone(&log);

        std::thread::Builder::new()
            .name("microchat-api".to_owned())
            .spawn(move || {
                let http = match reqwest::blocking::Client::builder().build() {
                    Ok(http) => http,
                    Err(e) => {
                        let _ = evt_tx.send(Event::Checked(Err(format!("HTTP 客户端初始化失败: {e}"))));
                        return;
                    }
                };
                while let Ok(command) = cmd_rx.recv() {
                    let label = command.label();
                    let started = std::time::Instant::now();
                    let event = handle(&http, command);
                    if let Ok(mut log) = worker_log.lock() {
                        log.push_back(format!(
                            "{label} → {} ({} ms)",
                            summarize(&event),
                            started.elapsed().as_millis()
                        ));
                        while log.len() > LOG_CAP {
                            log.pop_front();
                        }
                    }
                    if evt_tx.send(event).is_err() {
                        break; // UI 已退出
                    }
                    ctx.request_repaint();
                }
            })
            .expect("无法启动 API 线程");

        Self { tx, rx, log }
    }

    pub fn send(&self, command: Command) {
        let _ = self.tx.send(command);
    }

    /// 每帧调一次，非阻塞。
    pub fn try_recv(&self) -> Option<Event> {
        self.rx.try_recv().ok()
    }

    /// 请求日志快照（新的在后）。
    pub fn log_snapshot(&self) -> Vec<String> {
        self.log
            .lock()
            .map(|log| log.iter().cloned().collect())
            .unwrap_or_default()
    }
}

impl Command {
    /// 日志里显示的一行简述。
    fn label(&self) -> String {
        match self {
            Self::Check { .. } => "GET /health".to_owned(),
            Self::ListProviders { .. } => "GET /providers".to_owned(),
            Self::ListConversations { .. } => "GET /conversations".to_owned(),
            Self::ListMessages { conversation, .. } => {
                format!("GET /conversations/{}/messages", &conversation.to_string()[..8])
            }
            Self::ListVariables { conversation, .. } => {
                format!("GET /conversations/{}/variables", &conversation.to_string()[..8])
            }
            Self::EditMessage {
                conversation,
                message,
                ..
            } => format!(
                "PATCH /conversations/{}/messages/{}",
                &conversation.to_string()[..8],
                &message.to_string()[..8]
            ),
            Self::DeleteMessage {
                conversation,
                message,
                ..
            } => format!(
                "DELETE /conversations/{}/messages/{}",
                &conversation.to_string()[..8],
                &message.to_string()[..8]
            ),
            Self::CreateConversation { .. } => "POST /conversations".to_owned(),
            Self::DeleteConversation { conversation, .. } => {
                format!("DELETE /conversations/{}", &conversation.to_string()[..8])
            }
            Self::SendMessage { conversation, .. } => {
                format!("POST /conversations/{}/messages", &conversation.to_string()[..8])
            }
            Self::UpdateConversation { conversation, .. } => {
                format!("PATCH /conversations/{}", &conversation.to_string()[..8])
            }
            Self::RefreshProvider { provider, .. } => {
                format!("POST /providers/{provider}/refresh")
            }
            Self::UpdateProvider { id, .. } => format!("PATCH /providers/{id}"),
            Self::CreateProvider { id, .. } => format!("POST /providers ({id})"),
            Self::ListAgents { .. } => "GET /agents".to_owned(),
            Self::SaveAgent { id, .. } => format!("PATCH /agents/{id}"),
            Self::CreateAgent { name, .. } => format!("POST /agents ({name})"),
            Self::MakeDefaultAgent { id, .. } => format!("PATCH /agents/{id} (设为默认)"),
            Self::DeleteSiblings { message, .. } => {
                format!("DELETE /messages/{}/siblings", &message.to_string()[..8])
            }
            Self::SetLeaf { leaf, .. } => format!("PATCH /conversations (切到 {})", &leaf.to_string()[..8]),
            Self::ListBranches { conversation, .. } => {
                format!("GET /conversations/{}/branches", &conversation.to_string()[..8])
            }
            Self::Resend { conversation, .. } => {
                format!("POST /conversations/{}/resend", &conversation.to_string()[..8])
            }
            Self::DeleteAgent { id, .. } => format!("DELETE /agents/{id}"),
            Self::DebugState { .. } => "GET /debug/state".to_owned(),
            Self::DebugFile { name, .. } => format!("GET /debug/file/{name}"),
        }
    }
}

fn summarize(event: &Event) -> String {
    match event {
        Event::Checked(Ok(version)) => format!("OK v{version}"),
        Event::Checked(Err(message)) => format!("失败: {message}"),
        Event::Providers(Ok(list)) => format!("OK {} 个 provider", list.len()),
        Event::Providers(Err(message)) => format!("失败: {message}"),
        Event::Conversations(Ok(list)) => format!("OK {} 个会话", list.len()),
        Event::Conversations(Err(message)) => format!("失败: {message}"),
        Event::Messages { result, .. } => match result {
            Ok(list) => format!("OK {} 条消息", list.len()),
            Err(message) => format!("失败: {message}"),
        },
        Event::Variables { result, .. } => match result {
            Ok(view) => format!("OK {} 个生效变量", view.effective.len()),
            Err(message) => format!("失败: {message}"),
        },
        Event::LeafSwitched { result, .. } => match result {
            Ok(_) => "OK 已切分支".to_owned(),
            Err(message) => format!("失败: {message}"),
        },
        Event::Branches { result, .. } => match result {
            Ok(map) => format!("OK {} 条消息带分支信息", map.len()),
            Err(message) => format!("失败: {message}"),
        },
        Event::Resent { result, .. } => match result {
            Ok(turn) => format!("OK 重新生成（{}）", turn.backend),
            Err(message) => format!("失败: {message}"),
        },
        Event::MessageDeleted { result, .. } => match result {
            Ok(value) => format!("OK 已删除 {} 条", value["deleted"]),
            Err(message) => format!("失败: {message}"),
        },
        Event::MessageEdited { result, .. } => match result {
            Ok(_) => "OK".to_owned(),
            Err(message) => format!("失败: {message}"),
        },
        Event::ConversationCreated(Ok(conversation)) => {
            format!("OK 会话 {}", &conversation.id.to_string()[..8])
        }
        Event::ConversationCreated(Err(message)) => format!("失败: {message}"),
        Event::ConversationDeleted { result, .. } => match result {
            Ok(()) => "OK".to_owned(),
            Err(message) => format!("失败: {message}"),
        },
        Event::Turn { result, .. } => match result {
            Ok(turn) => format!("OK 后端 {}", turn.backend),
            Err(message) => format!("失败: {message}"),
        },
        Event::ConversationUpdated(Ok(_)) => "OK".to_owned(),
        Event::ConversationUpdated(Err(message)) => format!("失败: {message}"),
        Event::Agents(Ok(config)) => format!("OK {} 个 agent", config.agents.len()),
        Event::Agents(Err(message)) => format!("失败: {message}"),
        Event::AgentWritten(Ok(())) => "OK".to_owned(),
        Event::AgentWritten(Err(message)) => format!("失败: {message}"),
        Event::ProviderWritten(Ok(())) => "OK".to_owned(),
        Event::ProviderWritten(Err(message)) => format!("失败: {message}"),
        Event::DebugState(Ok(state)) => {
            format!("OK 会话 {} 条", state.counts.conversations)
        }
        Event::DebugState(Err(message)) => format!("失败: {message}"),
        Event::DebugFile(Ok(file)) => format!("OK {} 字节", file.text.len()),
        Event::DebugFile(Err(message)) => format!("失败: {message}"),
    }
}

fn handle(http: &reqwest::blocking::Client, command: Command) -> Event {
    match command {
        Command::Check { base, token } => {
            let result = check(http, &base, token.as_deref());
            Event::Checked(result)
        }
        Command::ListProviders { base, token } => Event::Providers(get(
            http,
            &base,
            token.as_deref(),
            "/api/v1/providers",
        )),
        Command::ListConversations { base, token } => Event::Conversations(get(
            http,
            &base,
            token.as_deref(),
            "/api/v1/conversations",
        )),
        Command::ListMessages {
            base,
            token,
            conversation,
        } => Event::Messages {
            conversation,
            result: get(
                http,
                &base,
                token.as_deref(),
                &format!("/api/v1/conversations/{conversation}/messages"),
            ),
        },
        Command::ListVariables {
            base,
            token,
            conversation,
        } => Event::Variables {
            conversation,
            result: get(
                http,
                &base,
                token.as_deref(),
                &format!("/api/v1/conversations/{conversation}/variables"),
            ),
        },
        Command::EditMessage {
            base,
            token,
            conversation,
            message,
            content,
        } => Event::MessageEdited {
            conversation,
            result: write_json(
                http,
                reqwest::Method::PATCH,
                &format!(
                    "{}/api/v1/conversations/{conversation}/messages/{message}",
                    base.trim_end_matches('/')
                ),
                token.as_deref(),
                serde_json::json!({ "content": content }),
            ),
        },
        Command::DeleteMessage {
            base,
            token,
            conversation,
            message,
        } => Event::MessageDeleted {
            conversation,
            message,
            result: write_json(
                http,
                reqwest::Method::DELETE,
                &format!(
                    "{}/api/v1/conversations/{conversation}/messages/{message}",
                    base.trim_end_matches('/')
                ),
                token.as_deref(),
                serde_json::json!({}),
            ),
        },
        Command::CreateConversation { base, token } => {
            // 不带 provider/model：让后端按 config.jsonc 的 defaults 决定（默认落到 dummy）
            Event::ConversationCreated(write_json(
                http,
                reqwest::Method::POST,
                &format!("{}/api/v1/conversations", base.trim_end_matches('/')),
                token.as_deref(),
                serde_json::json!({ "system_prompt": "" }),
            ))
        }
        Command::DeleteConversation {
            base,
            token,
            conversation,
        } => Event::ConversationDeleted {
            conversation,
            result: write(
                http,
                reqwest::Method::DELETE,
                &format!(
                    "{}/api/v1/conversations/{conversation}",
                    base.trim_end_matches('/')
                ),
                token.as_deref(),
                None,
            ),
        },
        Command::SendMessage {
            base,
            token,
            conversation,
            content,
        } => Event::Turn {
            conversation,
            result: write_json(
                http,
                reqwest::Method::POST,
                &format!(
                    "{}/api/v1/conversations/{conversation}/messages",
                    base.trim_end_matches('/')
                ),
                token.as_deref(),
                serde_json::json!({ "content": content }),
            ),
        },
        Command::DeleteSiblings {
            base,
            token,
            conversation,
            message,
        } => Event::MessageDeleted {
            conversation,
            message,
            result: write_json(
                http,
                reqwest::Method::DELETE,
                &format!(
                    "{}/api/v1/conversations/{conversation}/messages/{message}/siblings",
                    base.trim_end_matches('/')
                ),
                token.as_deref(),
                serde_json::json!({}),
            ),
        },
        Command::SetLeaf {
            base,
            token,
            conversation,
            leaf,
        } => Event::LeafSwitched {
            conversation,
            result: write_json(
                http,
                reqwest::Method::PATCH,
                &format!(
                    "{}/api/v1/conversations/{conversation}",
                    base.trim_end_matches('/')
                ),
                token.as_deref(),
                serde_json::json!({ "current_leaf": leaf }),
            ),
        },
        Command::ListBranches {
            base,
            token,
            conversation,
        } => Event::Branches {
            conversation,
            result: write_json(
                http,
                reqwest::Method::GET,
                &format!(
                    "{}/api/v1/conversations/{conversation}/branches",
                    base.trim_end_matches('/')
                ),
                token.as_deref(),
                serde_json::json!({}),
            ),
        },
        Command::Resend {
            base,
            token,
            conversation,
        } => Event::Resent {
            conversation,
            result: write_json(
                http,
                reqwest::Method::POST,
                &format!(
                    "{}/api/v1/conversations/{conversation}/resend",
                    base.trim_end_matches('/')
                ),
                token.as_deref(),
                serde_json::json!({}),
            ),
        },
        Command::UpdateConversation {
            base,
            token,
            conversation,
            provider,
            model,
            agent_id,
        } => {
            // 只把要改的字段放进 body：服务端按 Option 语义处理，没给的不动
            let mut body = serde_json::Map::new();
            if let Some(provider) = provider {
                body.insert("provider".to_owned(), provider.into());
            }
            if let Some(model) = model {
                body.insert("model".to_owned(), model.into());
            }
            if let Some(agent_id) = agent_id {
                body.insert("agent_id".to_owned(), agent_id.into());
            }
            Event::ConversationUpdated(write_json(
                http,
                reqwest::Method::PATCH,
                &format!(
                    "{}/api/v1/conversations/{conversation}",
                    base.trim_end_matches('/')
                ),
                token.as_deref(),
                serde_json::Value::Object(body),
            ))
        }
        Command::RefreshProvider {
            base,
            token,
            provider,
        } => Event::Providers(get(
            http,
            &base,
            token.as_deref(),
            &format!("/api/v1/providers/{}/refresh", encode_segment(&provider)),
        )),
        Command::ListAgents { base, token } => {
            Event::Agents(get(http, &base, token.as_deref(), "/api/v1/agents"))
        }
        Command::SaveAgent {
            base,
            token,
            id,
            name,
            system_prompt,
        } => {
            let body = serde_json::json!({ "name": name, "system_prompt": system_prompt });
            let result = write(
                http,
                reqwest::Method::PATCH,
                &format!("{}/api/v1/agents/{}", base.trim_end_matches('/'), encode_segment(&id)),
                token.as_deref(),
                Some(body),
            );
            Event::AgentWritten(result)
        }
        Command::MakeDefaultAgent { base, token, id } => {
            let result = write(
                http,
                reqwest::Method::PATCH,
                &format!(
                    "{}/api/v1/agents/{}",
                    base.trim_end_matches('/'),
                    encode_segment(&id)
                ),
                token.as_deref(),
                Some(serde_json::json!({ "make_default": true })),
            );
            Event::AgentWritten(result)
        }
        Command::CreateAgent {
            base,
            token,
            name,
            system_prompt,
        } => {
            let body = serde_json::json!({ "name": name, "system_prompt": system_prompt });
            let result = write(
                http,
                reqwest::Method::POST,
                &format!("{}/api/v1/agents", base.trim_end_matches('/')),
                token.as_deref(),
                Some(body),
            );
            Event::AgentWritten(result)
        }
        Command::DeleteAgent { base, token, id } => {
            let result = write(
                http,
                reqwest::Method::DELETE,
                &format!("{}/api/v1/agents/{}", base.trim_end_matches('/'), encode_segment(&id)),
                token.as_deref(),
                None,
            );
            Event::AgentWritten(result)
        }
        Command::UpdateProvider {
            base,
            token,
            id,
            base_url,
            headers,
            api_key,
        } => {
            let body =
                serde_json::json!({ "base_url": base_url, "headers": headers, "api_key": api_key });
            let result = write(
                http,
                reqwest::Method::PATCH,
                &format!(
                    "{}/api/v1/providers/{}",
                    base.trim_end_matches('/'),
                    encode_segment(&id)
                ),
                token.as_deref(),
                Some(body),
            );
            Event::ProviderWritten(result)
        }
        Command::CreateProvider {
            base,
            token,
            id,
            kind,
            base_url,
            headers,
            api_key,
        } => {
            let body = serde_json::json!({
                "id": id,
                "kind": kind,
                "base_url": base_url,
                "headers": headers,
                "api_key": api_key,
            });
            let result = write(
                http,
                reqwest::Method::POST,
                &format!("{}/api/v1/providers", base.trim_end_matches('/')),
                token.as_deref(),
                Some(body),
            );
            Event::ProviderWritten(result)
        }
        Command::DebugState { base, token } => {
            Event::DebugState(get(http, &base, token.as_deref(), "/api/v1/debug/state"))
        }
        Command::DebugFile { base, token, name } => Event::DebugFile(get(
            http,
            &base,
            token.as_deref(),
            &format!("/api/v1/debug/file/{}", encode_segment(&name)),
        )),
    }
}

fn check(
    http: &reqwest::blocking::Client,
    base: &str,
    token: Option<&str>,
) -> Result<String, String> {
    let value: serde_json::Value = get(http, base, token, "/api/v1/health")?;
    Ok(value["version"].as_str().unwrap_or("unknown").to_owned())
}

/// GET 并解析 JSON。
fn get<T: serde::de::DeserializeOwned>(
    http: &reqwest::blocking::Client,
    base: &str,
    token: Option<&str>,
    path: &str,
) -> Result<T, String> {
    let url = format!("{}{path}", base.trim_end_matches('/'));
    let mut request = http.get(url);
    if let Some(token) = token {
        request = request.bearer_auth(token);
    }
    decode(request.send())
}

/// 写操作并解析响应体（用于"创建后拿回对象"这类接口）。
fn write_json<T: serde::de::DeserializeOwned>(
    http: &reqwest::blocking::Client,
    method: reqwest::Method,
    url: &str,
    token: Option<&str>,
    body: serde_json::Value,
) -> Result<T, String> {
    let mut request = http.request(method, url).json(&body);
    if let Some(token) = token {
        request = request.bearer_auth(token);
    }
    decode(request.send())
}

/// 写操作：只要 2xx 就算成功（响应体可为空）。
fn write(
    http: &reqwest::blocking::Client,
    method: reqwest::Method,
    url: &str,
    token: Option<&str>,
    body: Option<serde_json::Value>,
) -> Result<(), String> {
    let mut request = http.request(method, url);
    if let Some(token) = token {
        request = request.bearer_auth(token);
    }
    if let Some(body) = body {
        request = request.json(&body);
    }
    let response = request.send().map_err(connect_error)?;
    let status = response.status();
    if status.is_success() {
        return Ok(());
    }
    let text = response.text().unwrap_or_default();
    Err(describe_error(status.as_u16(), &text))
}

fn decode<T: serde::de::DeserializeOwned>(
    response: Result<reqwest::blocking::Response, reqwest::Error>,
) -> Result<T, String> {
    let response = response.map_err(connect_error)?;
    let status = response.status();
    let text = response
        .text()
        .map_err(|e| format!("读取响应失败: {e}"))?;
    if !status.is_success() {
        return Err(describe_error(status.as_u16(), &text));
    }
    serde_json::from_str(&text).map_err(|e| format!("响应格式不符合预期: {e}"))
}

fn connect_error(e: reqwest::Error) -> String {
    format!("无法连接后端: {e}")
}

/// 把后端的 `{"error":{"code","message"}}` 变成人话。
fn describe_error(status: u16, body: &str) -> String {
    let parsed = serde_json::from_str::<serde_json::Value>(body).ok();
    let code = parsed.as_ref().and_then(|v| v["error"]["code"].as_str());
    let message = parsed.as_ref().and_then(|v| v["error"]["message"].as_str());
    match (code, message) {
        (Some(code), Some(message)) => format!("HTTP {status}（{code}）: {message}"),
        _ => format!("HTTP {status}"),
    }
}

/// 路径段 percent-encoding：agent id 可能是中文。
fn encode_segment(segment: &str) -> String {
    let mut out = String::with_capacity(segment.len());
    for byte in segment.bytes() {
        match byte {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'.' | b'_' | b'~' => {
                out.push(byte as char)
            }
            other => out.push_str(&format!("%{other:02X}")),
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use std::time::{Duration, Instant};

    use axum::http::{HeaderMap, StatusCode};
    use axum::{routing::get, Router};

    use super::*;

    /// 起一个假后端；`token` 为 `Some` 时校验 `Authorization: Bearer`。
    fn fake_backend(token: Option<&'static str>) -> String {
        let app = Router::new()
            .route(
                "/api/v1/health",
                get(move |headers: HeaderMap| async move {
                    if let Some(expected) = token {
                        let supplied = headers
                            .get("authorization")
                            .and_then(|v| v.to_str().ok())
                            .and_then(|v| v.strip_prefix("Bearer "));
                        if supplied != Some(expected) {
                            return (
                                StatusCode::UNAUTHORIZED,
                                r#"{"error":{"code":"unauthorized","message":"缺少或无效的 Bearer token"}}"#,
                            );
                        }
                    }
                    (StatusCode::OK, r#"{"status":"ok","version":"test"}"#)
                }),
            )
            // provider 编辑：在服务端校验前端发来的 body，不符就回 422
            .route(
                "/api/v1/providers/{id}",
                axum::routing::patch(
                    |axum::Json(body): axum::Json<serde_json::Value>| async move {
                        let ok = body["base_url"] == "http://127.0.0.1:11434/v1"
                            && body["headers"]["X-Title"] == "microchat";
                        if ok {
                            StatusCode::OK
                        } else {
                            StatusCode::UNPROCESSABLE_ENTITY
                        }
                    },
                ),
            );

        // 同步测试里不能把阻塞 socket 交给 tokio：在 runtime 内绑定，用 channel 递出地址。
        let (addr_tx, addr_rx) = mpsc::channel();
        std::thread::spawn(move || {
            let runtime = tokio::runtime::Builder::new_current_thread()
                .enable_all()
                .build()
                .unwrap();
            runtime.block_on(async move {
                let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
                addr_tx.send(listener.local_addr().unwrap()).unwrap();
                axum::serve(listener, app).await.unwrap();
            });
        });
        let addr = addr_rx.recv_timeout(Duration::from_secs(5)).unwrap();
        format!("http://{addr}")
    }

    fn next_event(client: &Client) -> Event {
        let deadline = Instant::now() + Duration::from_secs(5);
        loop {
            if let Some(event) = client.try_recv() {
                return event;
            }
            assert!(Instant::now() < deadline, "等不到事件");
            std::thread::sleep(Duration::from_millis(10));
        }
    }

    #[test]
    fn check_distinguishes_reachable_unauthorized_and_dead() {
        let client = Client::spawn(egui::Context::default());

        // 通则
        let base = fake_backend(None);
        client.send(Command::Check {
            base: base.clone(),
            token: None,
        });
        match next_event(&client) {
            Event::Checked(Ok(version)) => assert_eq!(version, "test"),
            other => panic!("期望连接成功，得到 {:?}", describe(&other)),
        }

        // 请求日志留痕（调试页用）
        let log = client.log_snapshot();
        assert!(
            log.iter().any(|line| line.contains("health")),
            "日志应有 health 记录: {log:?}"
        );

        // 口令错 → 401，且错误信息用后端给的 code
        let guarded = fake_backend(Some("good"));
        client.send(Command::Check {
            base: guarded.clone(),
            token: Some("wrong".to_owned()),
        });
        match next_event(&client) {
            Event::Checked(Err(message)) => {
                assert!(message.contains("401"), "应带上状态码: {message}");
                assert!(message.contains("unauthorized"), "应带上后端 code: {message}");
            }
            other => panic!("期望鉴权失败，得到 {:?}", describe(&other)),
        }

        // 端口关闭 → 连接错误
        client.send(Command::Check {
            base: "http://127.0.0.1:1".to_owned(),
            token: None,
        });
        match next_event(&client) {
            Event::Checked(Err(message)) => assert!(message.contains("无法连接"), "{message}"),
            other => panic!("期望连接失败，得到 {:?}", describe(&other)),
        }
    }

    #[test]
    fn update_provider_sends_only_connection_fields() {
        let client = Client::spawn(egui::Context::default());
        client.send(Command::UpdateProvider {
            base: fake_backend(None),
            token: None,
            id: "local".to_owned(),
            base_url: "http://127.0.0.1:11434/v1".to_owned(),
            headers: [("X-Title".to_owned(), "microchat".to_owned())]
                .into_iter()
                .collect(),
            api_key: None,
        });

        match next_event(&client) {
            Event::ProviderWritten(Ok(())) => {}
            other => panic!("期望保存成功，得到 {:?}", describe(&other)),
        }
    }

    fn describe(event: &Event) -> &'static str {
        match event {
            Event::Checked(_) => "Checked",
            Event::Providers(_) => "Providers",
            Event::Conversations(_) => "Conversations",
            Event::Messages { .. } => "Messages",
            Event::Variables { .. } => "Variables",
            Event::MessageEdited { .. } => "MessageEdited",
            Event::MessageDeleted { .. } => "MessageDeleted",
            Event::ConversationCreated(_) => "ConversationCreated",
            Event::ConversationDeleted { .. } => "ConversationDeleted",
            Event::Turn { .. } => "Turn",
            Event::Resent { .. } => "Resent",
            Event::LeafSwitched { .. } => "LeafSwitched",
            Event::Branches { .. } => "Branches",
            Event::ConversationUpdated(_) => "ConversationUpdated",
            Event::Agents(_) => "Agents",
            Event::AgentWritten(_) => "AgentWritten",
            Event::ProviderWritten(_) => "ProviderWritten",
            Event::DebugState(_) => "DebugState",
            Event::DebugFile(_) => "DebugFile",
        }
    }
}
