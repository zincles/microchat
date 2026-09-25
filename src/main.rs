//! microchat 前端（egui）。
//!
//! - 启动即尝试连接后端（默认 `http://127.0.0.1:8787`）；连不上就停在连接页，不进主界面；
//! - 设置分两处：**服务器设置**（后端持有，经 API 读写）与 **前端设置**（本机 JSON 文件）；
//! - 密码只存内存，不落盘；
//! - 会话与消息仍是本地占位：下一轮换成 API（等 providers 能说话之后）。

mod client;
mod fonts;
mod frontend_settings;

use std::collections::BTreeMap;

use eframe::egui::text::{CCursor, CCursorRange};
use eframe::egui::widgets::text_edit::TextEditState;
use eframe::egui::{self, Align, Color32, Layout, RichText, TextEdit};
use microchat::config::{Agent, AgentsConfig, ProviderKind};
use microchat::model::{Conversation as ApiConversation, Message as ApiMessage, Role as ApiRole};
use microchat::registry::ProviderView;
use microchat::server::{DebugState, RawFile};
use uuid::Uuid;

use client::{Client, Command, Event};
use frontend_settings::{FrontendSettings, Theme};

/// 后端默认地址（端口与 server 的默认配置一致）。
const DEFAULT_SERVER: &str = "http://127.0.0.1:8787";
const NEW_TITLE: &str = "新对话";
/// 本地占位会话的标题长度（真实标题由后端按 `config.chat.title_chars` 生成）。
const MOCK_TITLE_CHARS: usize = 32;
/// 调试页关系图的 `GraphView` id：布局状态存在 egui memory 里，靠它区分与重置。
const GRAPH_ID: &str = "debug_graph";
/// 关系图最多拉几个会话的消息（调试用，别把整库存拉进来）。
const GRAPH_CONVERSATION_LIMIT: usize = 3;

fn main() -> eframe::Result {
    let options = eframe::NativeOptions {
        viewport: egui::ViewportBuilder::default()
            .with_title("microchat")
            .with_inner_size([1100.0, 720.0])
            .with_min_inner_size([720.0, 480.0]),
        ..Default::default()
    };
    eframe::run_native(
        "microchat",
        options,
        Box::new(|cc| {
            match fonts::install_cjk(&cc.egui_ctx) {
                Ok(f) => eprintln!("字体: {} face {}", f.path.display(), f.index),
                Err(e) => eprintln!("字体装载失败: {e}"),
            }
            Ok(Box::new(App::new(cc.egui_ctx.clone())))
        }),
    )
}

#[derive(Clone, Copy, PartialEq)]
enum Role {
    User,
    Assistant,
}

struct Msg {
    role: Role,
    text: String,
}

struct Conversation {
    title: String,
    msgs: Vec<Msg>,
    draft: String,
}

fn blank_conversation() -> Conversation {
    Conversation {
        title: NEW_TITLE.to_owned(),
        msgs: Vec::new(),
        draft: String::new(),
    }
}

/// 右键菜单动作，延后一帧交给 TextEdit 自己执行（见 `fn ui` 开头）。
enum MenuAction {
    Cut,
    Copy,
    Paste,
    SelectAll,
}

#[derive(Clone, Copy, PartialEq)]
enum View {
    Chat,
    Settings,
    Account,
    Debug,
}

#[derive(Clone, Copy, PartialEq)]
enum SettingsTab {
    Server,
    Frontend,
    About,
}

impl SettingsTab {
    const ALL: [(Self, &'static str); 3] = [
        (Self::Server, "服务器设置"),
        (Self::Frontend, "前端设置"),
        (Self::About, "关于"),
    ];
}

/// 调试页的分页：与设置页同构（左导航 + 内容区），别把工具堆成一长条。
#[derive(Clone, Copy, PartialEq)]
enum DebugTab {
    State,
    Config,
    Log,
    Graph,
}

impl DebugTab {
    const ALL: [(Self, &'static str); 4] = [
        (Self::State, "后端状态"),
        (Self::Config, "原始配置"),
        (Self::Log, "请求日志"),
        (Self::Graph, "关系图"),
    ];
}

/// 与后端的连接状态。未 `Online` 时只显示连接页。
#[derive(PartialEq)]
enum Backend {
    Checking,
    Offline(String),
    Online,
}

struct App {
    client: Client,
    backend: Backend,
    /// 口令（= 后端 `config.jsonc` 的 `server.auth_token`），只存内存。
    token: Option<String>,
    password: String,
    /// 服务器侧数据（服务器设置页用）。
    providers: Option<Vec<ProviderView>>,
    agents: Option<AgentsConfig>,
    selected_agent: Option<String>,
    agent_name: String,
    agent_prompt: String,
    new_agent_id: String,
    new_agent_name: String,
    note: String,
    /// 调试页数据。
    debug_state: Option<DebugState>,
    debug_file: Option<RawFile>,
    debug_file_name: String,
    /// 调试页关系图：后端数据与投影出的图。
    graph_conversations: Vec<ApiConversation>,
    graph_messages: BTreeMap<Uuid, Vec<ApiMessage>>,
    graph: Option<egui_graphs::Graph>,
    /// provider 编辑草稿。
    editing_provider: Option<String>,
    provider_base_url: String,
    provider_headers: String,
    /// 编辑时的密钥输入：留空 = 不改。
    provider_api_key: String,
    /// 新建 provider 表单。
    new_provider_id: String,
    new_provider_kind: ProviderKind,
    new_provider_base_url: String,
    new_provider_api_key: String,
    new_provider_headers: String,
    /// 前端侧设置（**已应用**的值；编辑走 `frontend_draft`）。
    settings: FrontendSettings,
    /// 前端设置的草稿：改动先落这里，点「保存」才应用 + 落盘。
    frontend_draft: FrontendSettings,
    settings_dirty: bool,
    /// 本地占位会话。
    convs: Vec<Conversation>,
    current: usize,
    view: View,
    settings_tab: SettingsTab,
    debug_tab: DebugTab,
    preedit_active: bool,
    focus_pending: bool,
    composer_id: Option<egui::Id>,
    menu_action: Option<MenuAction>,
}

impl App {
    fn new(ctx: egui::Context) -> Self {
        let settings = FrontendSettings::load();
        ctx.set_theme(settings.theme.to_egui());
        ctx.set_zoom_factor(settings.zoom);

        let mut app = Self {
            client: Client::spawn(ctx),
            backend: Backend::Offline(String::new()),
            token: None,
            password: String::new(),
            providers: None,
            agents: None,
            selected_agent: None,
            agent_name: String::new(),
            agent_prompt: String::new(),
            new_agent_id: String::new(),
            new_agent_name: String::new(),
            note: String::new(),
            debug_state: None,
            debug_file: None,
            debug_file_name: String::new(),
            graph_conversations: Vec::new(),
            graph_messages: BTreeMap::new(),
            graph: None,
            editing_provider: None,
            provider_base_url: String::new(),
            provider_headers: String::new(),
            provider_api_key: String::new(),
            new_provider_id: String::new(),
            new_provider_kind: ProviderKind::OpenAiCompat,
            new_provider_base_url: String::new(),
            new_provider_api_key: String::new(),
            new_provider_headers: String::new(),
            frontend_draft: settings.clone(),
            settings,
            settings_dirty: false,
            convs: vec![blank_conversation()],
            current: 0,
            view: View::Chat,
            settings_tab: SettingsTab::Server,
            debug_tab: DebugTab::State,
            preedit_active: false,
            focus_pending: true,
            composer_id: None,
            menu_action: None,
        };
        app.connect();
        app
    }

    fn connect(&mut self) {
        self.backend = Backend::Checking;
        self.note.clear();
        self.token = (!self.password.is_empty()).then(|| self.password.clone());
        self.settings_dirty = true;
        // 连接页改的是"已应用"的设置；草稿要同步，否则前端设置里一保存又把地址改回去了
        self.frontend_draft.server_address = self.settings.server_address.clone();
        self.frontend_draft.username = self.settings.username.clone();
        self.client.send(Command::Check {
            base: self.settings.server_address.clone(),
            token: self.token.clone(),
        });
    }

    fn load_server_data(&mut self) {
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        self.client.send(Command::ListProviders {
            base: base.clone(),
            token: token.clone(),
        });
        self.client.send(Command::ListAgents { base, token });
    }

    fn pump_events(&mut self) {
        while let Some(event) = self.client.try_recv() {
            match event {
                Event::Checked(Ok(version)) => {
                    self.backend = Backend::Online;
                    self.note = format!("已连接后端 v{version}");
                    self.load_server_data();
                }
                Event::Checked(Err(message)) => self.backend = Backend::Offline(message),
                Event::Providers(Ok(list)) => self.providers = Some(list),
                Event::Providers(Err(message)) => self.note = message,
                Event::Conversations(Ok(list)) => {
                    self.graph_conversations = list;
                    self.graph_messages.clear();
                    let (base, token) = (self.settings.server_address.clone(), self.token.clone());
                    for conversation in self
                        .graph_conversations
                        .iter()
                        .take(GRAPH_CONVERSATION_LIMIT)
                    {
                        self.client.send(Command::ListMessages {
                            base: base.clone(),
                            token: token.clone(),
                            conversation: conversation.id,
                        });
                    }
                    self.rebuild_graph();
                }
                Event::Conversations(Err(message)) => self.note = message,
                Event::Messages {
                    conversation,
                    result,
                } => match result {
                    Ok(messages) => {
                        self.graph_messages.insert(conversation, messages);
                        self.rebuild_graph();
                    }
                    Err(message) => self.note = message,
                },
                Event::Agents(Ok(config)) => {
                    self.agents = Some(config);
                    let still_there = self
                        .selected_agent
                        .as_ref()
                        .is_some_and(|id| self.agents.as_ref().is_some_and(|a| a.get(id).is_some()));
                    if !still_there {
                        self.selected_agent = self
                            .agents
                            .as_ref()
                            .and_then(|a| a.agents.first().map(|agent| agent.id.clone()));
                    }
                    self.load_selected_agent();
                }
                Event::Agents(Err(message)) => self.note = message,
                Event::ProviderWritten(Ok(())) => {
                    self.note = "provider 已保存".to_owned();
                    self.editing_provider = None;
                    self.new_provider_id.clear();
                    self.new_provider_base_url.clear();
                    self.new_provider_api_key.clear();
                    self.new_provider_headers.clear();
                    self.provider_api_key.clear();
                    let (base, token) = (self.settings.server_address.clone(), self.token.clone());
                    self.client.send(Command::ListProviders { base, token });
                }
                Event::ProviderWritten(Err(message)) => self.note = message,
                Event::AgentWritten(Ok(())) => {
                    self.note = "已保存".to_owned();
                    let (base, token) = (self.settings.server_address.clone(), self.token.clone());
                    self.client.send(Command::ListAgents { base, token });
                }
                Event::AgentWritten(Err(message)) => self.note = message,
                Event::DebugState(Ok(state)) => self.debug_state = Some(state),
                Event::DebugState(Err(message)) => self.note = message,
                Event::DebugFile(Ok(file)) => self.debug_file = Some(file),
                Event::DebugFile(Err(message)) => self.note = message,
            }
        }
    }

    fn load_selected_agent(&mut self) {
        let (name, prompt) = match self
            .selected_agent
            .as_ref()
            .and_then(|id| self.agents.as_ref()?.get(id))
        {
            Some(agent) => (agent.name.clone(), agent.system_prompt.clone()),
            None => (String::new(), String::new()),
        };
        self.agent_name = name;
        self.agent_prompt = prompt;
    }

    /// 前端设置草稿有没有改动（与已应用的状态逐字段比较）。
    fn frontend_dirty(&self) -> bool {
        self.frontend_draft != self.settings
    }

    /// 应用并保存前端设置。主题与缩放都在**这一刻**才生效——拖动中应用会让界面在手底下变形。
    fn save_frontend_settings(&mut self, ctx: &egui::Context) {
        self.settings = self.frontend_draft.clone();
        ctx.set_theme(self.settings.theme.to_egui());
        ctx.set_zoom_factor(self.settings.zoom);
        self.settings_dirty = true;
        self.note = "前端设置已保存".to_owned();
    }

    /// 服务器设置里有没有未保存的改动。
    fn server_dirty(&self) -> bool {
        self.agent_draft_id().is_some() || self.provider_draft_id().is_some()
    }

    /// 有改动的 agent id（保存与提示共用同一份判定）。
    fn agent_draft_id(&self) -> Option<String> {
        let id = self.selected_agent.as_ref()?;
        let agent = self.agents.as_ref()?.get(id)?;
        agent_draft_differs(agent, &self.agent_name, &self.agent_prompt).then(|| id.clone())
    }

    /// 有改动的 provider id。密钥只看"有没有填新的"——旧密钥读不回来。
    fn provider_draft_id(&self) -> Option<String> {
        let id = self.editing_provider.as_ref()?;
        let provider = self.providers.as_ref()?.iter().find(|p| &p.id == id)?;
        let headers = parse_header_lines(&self.provider_headers);
        provider_draft_differs(
            provider,
            self.provider_base_url.trim(),
            &headers,
            !self.provider_api_key.is_empty(),
        )
        .then(|| id.clone())
    }

    /// 页脚「保存」：把面板里所有待提交的草稿一次写回后端。
    fn save_server_settings(&mut self) {
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        if let Some(id) = self.agent_draft_id() {
            self.client.send(Command::SaveAgent {
                base: base.clone(),
                token: token.clone(),
                id,
                name: self.agent_name.clone(),
                system_prompt: self.agent_prompt.clone(),
            });
        }
        if let Some(id) = self.provider_draft_id() {
            self.client.send(Command::UpdateProvider {
                base,
                token,
                id,
                base_url: self.provider_base_url.trim().to_owned(),
                headers: parse_header_lines(&self.provider_headers),
                api_key: (!self.provider_api_key.is_empty()).then(|| self.provider_api_key.clone()),
            });
        }
    }

    fn persist_settings(&mut self) {
        if !self.settings_dirty {
            return;
        }
        self.settings_dirty = false;
        if let Err(e) = self.settings.save() {
            self.note = format!("前端设置保存失败: {e}");
        }
    }

    /// 连接页：未连上后端时唯一能看到的东西。
    fn connect_screen(&mut self, ui: &mut egui::Ui) {
        ui.vertical_centered(|ui| {
            ui.add_space(48.0);
            ui.heading("microchat");
            ui.label(RichText::new("需要连接后端才能使用").weak());
            ui.add_space(24.0);
        });

        ui.vertical_centered(|ui| {
            ui.allocate_ui_with_layout(
                egui::vec2(420.0, 180.0),
                Layout::top_down(Align::Min),
                |ui| {
                    egui::Grid::new("connect_form")
                        .num_columns(2)
                        .spacing([12.0, 10.0])
                        .show(ui, |ui| {
                            ui.label("服务器地址");
                            if ui
                                .add(
                                    TextEdit::singleline(&mut self.settings.server_address)
                                        .desired_width(280.0),
                                )
                                .changed()
                            {
                                self.settings_dirty = true;
                            }
                            ui.end_row();

                            ui.label("用户名");
                            ui.add(
                                TextEdit::singleline(&mut self.settings.username)
                                    .desired_width(280.0),
                            );
                            ui.end_row();

                            ui.label("密码");
                            ui.add(
                                TextEdit::singleline(&mut self.password)
                                    .password(true)
                                    .desired_width(280.0),
                            );
                            ui.end_row();
                        });

                    ui.add_space(12.0);
                    let checking = self.backend == Backend::Checking;
                    let label = if checking { "连接中…" } else { "连接" };
                    if ui
                        .add_enabled(
                            !checking,
                            egui::Button::new(label).min_size(egui::vec2(96.0, 28.0)),
                        )
                        .clicked()
                    {
                        self.connect();
                    }
                },
            );
        });

        ui.vertical_centered(|ui| {
            ui.add_space(8.0);
            match &self.backend {
                Backend::Checking => {
                    ui.spinner();
                }
                Backend::Offline(message) if !message.is_empty() => {
                    ui.colored_label(Color32::from_rgb(0xd0, 0x4a, 0x4a), message);
                    ui.label(
                        RichText::new(
                            "确认后端已启动：cargo run --bin server（默认 127.0.0.1:8787）",
                        )
                        .weak(),
                    );
                }
                _ => {}
            }
        });
    }

    /// 服务器设置：连接信息 + agents（可写）+ 模型（可刷新）。
    fn server_settings(&mut self, ui: &mut egui::Ui) {
        ui.label(RichText::new("连接").strong());
        ui.horizontal(|ui| {
            ui.label(RichText::new(&self.settings.server_address).monospace());
            ui.label(
                RichText::new(if self.token.is_some() {
                    "（已带口令）"
                } else {
                    "（无口令）"
                })
                .weak(),
            );
            if ui.button("断开").clicked() {
                self.backend = Backend::Offline("已断开".to_owned());
                self.providers = None;
                self.agents = None;
                self.selected_agent = None;
            }
        });
        ui.separator();

        ui.label(RichText::new("Agents（预设）").strong());
        ui.label(
            RichText::new("改动会写回后端的 config/agents.jsonc；该文件会被整体重写，注释会丢")
                .weak(),
        );

        let mut select: Option<String> = None;
        let mut delete: Option<String> = None;
        match &self.agents {
            None => {
                ui.label(RichText::new("加载中…").weak());
            }
            Some(config) => {
                if config.agents.is_empty() {
                    ui.label(RichText::new("还没有 agent，下面新建一个").weak());
                }
                for agent in &config.agents {
                    ui.horizontal(|ui| {
                        let selected = self.selected_agent.as_deref() == Some(agent.id.as_str());
                        let label = if agent.name.is_empty() {
                            agent.id.clone()
                        } else {
                            format!("{}（{}）", agent.name, agent.id)
                        };
                        if ui.selectable_label(selected, label).clicked() {
                            select = Some(agent.id.clone());
                        }
                        ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                            if close_button(ui).clicked() {
                                delete = Some(agent.id.clone());
                            }
                        });
                    });
                }
            }
        }
        if let Some(id) = select {
            self.selected_agent = Some(id);
            self.load_selected_agent();
        }
        if let Some(id) = delete {
            let (base, token) = (self.settings.server_address.clone(), self.token.clone());
            self.client.send(Command::DeleteAgent { base, token, id });
        }

        if self.selected_agent.is_some() {
            ui.add_space(6.0);
            egui::Grid::new("agent_editor")
                .num_columns(2)
                .spacing([12.0, 8.0])
                .show(ui, |ui| {
                    ui.label("名称");
                    ui.add(TextEdit::singleline(&mut self.agent_name).desired_width(280.0));
                    ui.end_row();

                    ui.label("系统提示词");
                    ui.add(
                        TextEdit::multiline(&mut self.agent_prompt)
                            .desired_rows(6)
                            .desired_width(420.0),
                    );
                    ui.end_row();
                });
            ui.label(RichText::new("改完点右下角「保存」提交").weak());
        }

        ui.add_space(8.0);
        ui.separator();
        ui.label(RichText::new("新建 agent").strong());
        ui.horizontal(|ui| {
            ui.label("id");
            ui.add(TextEdit::singleline(&mut self.new_agent_id).desired_width(120.0));
            ui.label("名称");
            ui.add(TextEdit::singleline(&mut self.new_agent_name).desired_width(160.0));
            let ready = !self.new_agent_id.trim().is_empty();
            if ui.add_enabled(ready, egui::Button::new("新建")).clicked() {
                let (base, token) = (self.settings.server_address.clone(), self.token.clone());
                self.client.send(Command::CreateAgent {
                    base,
                    token,
                    id: self.new_agent_id.trim().to_owned(),
                    name: self.new_agent_name.trim().to_owned(),
                    system_prompt: String::new(),
                });
                self.new_agent_id.clear();
                self.new_agent_name.clear();
            }
        });

        ui.add_space(8.0);
        ui.separator();
        ui.label(RichText::new("模型").strong());
        ui.label(
            RichText::new("「获取模型」会从上游拉取可用模型；成功后本 provider 的模型列表＝上游当前那份")
                .weak(),
        );
        match &self.providers {
            None => {
                ui.label(RichText::new("加载中…").weak());
            }
            Some(list) if list.is_empty() => {
                ui.label(RichText::new("还没有 provider，下面新建一个").weak());
            }
            Some(list) => {
                let mut refresh: Option<String> = None;
                let mut edit: Option<(String, String, BTreeMap<String, String>)> = None;
                for provider in list {
                    ui.horizontal(|ui| {
                        ui.label(RichText::new(&provider.id).strong());
                        ui.label(RichText::new(kind_label(provider.kind)).weak());
                        ui.label(
                            RichText::new(format!("{} 个模型", provider.models.len())).weak(),
                        );
                        let is_upstream = provider.kind == ProviderKind::OpenAiCompat;
                        if ui
                            .add_enabled(is_upstream, egui::Button::new("获取模型"))
                            .clicked()
                        {
                            refresh = Some(provider.id.clone());
                        }
                        if ui.button("编辑").clicked() {
                            edit = Some((
                                provider.id.clone(),
                                provider.base_url.clone(),
                                provider.headers.clone(),
                            ));
                        }
                    });
                    if provider.kind == ProviderKind::OpenAiCompat {
                        ui.horizontal(|ui| {
                            ui.label(RichText::new(&provider.base_url).weak().monospace());
                            ui.label(
                                RichText::new(if provider.has_key {
                                    "已配密钥"
                                } else {
                                    "未配密钥"
                                })
                                .weak(),
                            );
                        });
                    }
                    for model in &provider.models {
                        ui.label(
                            RichText::new(format!("· {}   {}", model.name, model.upstream_id)).weak(),
                        );
                    }
                }
                if let Some(provider) = refresh {
                    let (base, token) = (self.settings.server_address.clone(), self.token.clone());
                    self.client.send(Command::RefreshProvider {
                        base,
                        token,
                        provider,
                    });
                }
                if let Some((id, base_url, headers)) = edit {
                    self.editing_provider = Some(id);
                    self.provider_base_url = base_url;
                    self.provider_headers = header_lines(&headers);
                    self.provider_api_key.clear();
                }
            }
        }

        ui.add_space(8.0);
        ui.separator();
        ui.label(RichText::new("新建 provider").strong());
        ui.horizontal(|ui| {
            ui.label("id");
            ui.add(TextEdit::singleline(&mut self.new_provider_id).desired_width(120.0));
            ui.label("接口类型");
            for kind in [ProviderKind::OpenAiCompat, ProviderKind::Dummy] {
                ui.selectable_value(&mut self.new_provider_kind, kind, kind_label(kind));
            }
        });

        // 按类型显示待填参数
        match self.new_provider_kind {
            ProviderKind::OpenAiCompat => {
                ui.horizontal(|ui| {
                    ui.label("base_url");
                    ui.add(
                        TextEdit::singleline(&mut self.new_provider_base_url)
                            .desired_width(300.0)
                            .hint_text("https://…/v1"),
                    );
                });
                ui.horizontal(|ui| {
                    ui.label("api_key");
                    ui.add(
                        TextEdit::singleline(&mut self.new_provider_api_key)
                            .password(true)
                            .desired_width(300.0)
                            .hint_text("写入后端 config/secrets.json，不回显"),
                    );
                });
                ui.horizontal(|ui| {
                    ui.label("headers");
                    ui.add(
                        TextEdit::multiline(&mut self.new_provider_headers)
                            .desired_rows(2)
                            .desired_width(300.0)
                            .hint_text("每行一个：Header: 值（可空）"),
                    );
                });
            }
            ProviderKind::Dummy => {
                ui.label(
                    RichText::new("dummy 类型没有上游，也不需要 base_url；对话会直接走兜底回复。")
                        .weak(),
                );
            }
        }

        ui.horizontal(|ui| {
            let ready = !self.new_provider_id.trim().is_empty();
            if ui.add_enabled(ready, egui::Button::new("创建")).clicked() {
                let (base, token) = (self.settings.server_address.clone(), self.token.clone());
                let api_key = (!self.new_provider_api_key.is_empty())
                    .then(|| self.new_provider_api_key.clone());
                self.client.send(Command::CreateProvider {
                    base,
                    token,
                    id: self.new_provider_id.trim().to_owned(),
                    kind: self.new_provider_kind,
                    base_url: self.new_provider_base_url.trim().to_owned(),
                    headers: parse_header_lines(&self.new_provider_headers),
                    api_key,
                });
            }
            ui.label(RichText::new(&self.note).weak());
        });

        if let Some(id) = self.editing_provider.clone() {
            ui.add_space(8.0);
            ui.separator();
            ui.label(RichText::new(format!("编辑 provider：{id}")).strong());
            ui.label(
                RichText::new(
                    "id 不可改（历史会话与密钥都按它引用）。保存会重写 providers.jsonc，注释会丢；密钥写入 config/secrets.json",
                )
                .weak(),
            );
            egui::Grid::new("provider_editor")
                .num_columns(2)
                .spacing([12.0, 8.0])
                .show(ui, |ui| {
                    ui.label("base_url");
                    ui.add(TextEdit::singleline(&mut self.provider_base_url).desired_width(320.0));
                    ui.end_row();

                    ui.label("headers");
                    ui.add(
                        TextEdit::multiline(&mut self.provider_headers)
                            .desired_rows(3)
                            .desired_width(320.0)
                            .hint_text("每行一个：Header: 值"),
                    );
                    ui.end_row();

                    ui.label("密钥");
                    ui.horizontal(|ui| {
                        ui.add(
                            TextEdit::singleline(&mut self.provider_api_key)
                                .password(true)
                                .desired_width(240.0)
                                .hint_text("留空 = 不改"),
                        );
                        if ui.small_button("清除").clicked() {
                            let (base, token) =
                                (self.settings.server_address.clone(), self.token.clone());
                            self.client.send(Command::UpdateProvider {
                                base,
                                token,
                                id: id.clone(),
                                base_url: self.provider_base_url.trim().to_owned(),
                                headers: parse_header_lines(&self.provider_headers),
                                api_key: Some(String::new()),
                            });
                        }
                    });
                    ui.end_row();
                });
            ui.horizontal(|ui| {
                if ui.button("取消").clicked() {
                    self.editing_provider = None;
                }
                ui.label(RichText::new("改完点右下角「保存」提交").weak());
            });
        }

        if !self.note.is_empty() {
            ui.add_space(8.0);
            ui.label(RichText::new(&self.note).weak());
        }
    }

    /// 前端设置：只存本机；**改的是草稿**，点右下角「保存」才应用。
    fn frontend_settings(&mut self, ui: &mut egui::Ui) {
        ui.label(RichText::new("以下设置只存在本机（frontend.jsonc），不上传后端").weak());
        ui.label(
            RichText::new("改动先落在草稿里，点右下角「保存」才生效——主题与缩放在保存那一刻应用")
                .weak(),
        );
        ui.add_space(6.0);
        egui::Grid::new("frontend_form")
            .num_columns(2)
            .spacing([16.0, 10.0])
            .show(ui, |ui| {
                ui.label("主题");
                ui.horizontal(|ui| {
                    for theme in [Theme::System, Theme::Light, Theme::Dark] {
                        ui.selectable_value(&mut self.frontend_draft.theme, theme, theme.label());
                    }
                });
                ui.end_row();

                ui.label("界面缩放");
                ui.add(
                    egui::Slider::new(&mut self.frontend_draft.zoom, 0.8..=1.5).fixed_decimals(2),
                );
                ui.end_row();

                ui.label("发送");
                ui.checkbox(&mut self.frontend_draft.enter_sends, "Enter 发送消息");
                ui.end_row();

                ui.label("滚动");
                ui.checkbox(&mut self.frontend_draft.stick_to_bottom, "新消息自动滚到底部");
                ui.end_row();

                ui.label("服务器地址");
                ui.add(
                    TextEdit::singleline(&mut self.frontend_draft.server_address)
                        .desired_width(280.0),
                );
                ui.end_row();

                ui.label("用户名");
                ui.add(
                    TextEdit::singleline(&mut self.frontend_draft.username).desired_width(280.0),
                );
                ui.end_row();
            });
    }

    fn about(&mut self, ui: &mut egui::Ui) {
        egui::Grid::new("about").num_columns(2).spacing([16.0, 8.0]).show(ui, |ui| {
            ui.label("版本");
            ui.label(format!("microchat {}", env!("CARGO_PKG_VERSION")));
            ui.end_row();

            ui.label("后端");
            ui.label(RichText::new(&self.settings.server_address).monospace());
            ui.end_row();

            ui.label("连接");
            ui.label(match &self.backend {
                Backend::Online => "已连接",
                Backend::Checking => "连接中",
                Backend::Offline(_) => "未连接",
            });
            ui.end_row();

            ui.label("前端设置");
            ui.label(RichText::new(FrontendSettings::path().display().to_string()).monospace());
            ui.end_row();

            ui.label("域名");
            ui.label(RichText::new("会话仍为本地占位，尚未接后端存储").weak());
            ui.end_row();
        });
    }

    fn fetch_debug(&mut self) {
        if self.debug_file_name.is_empty() {
            self.debug_file_name = "config.jsonc".to_owned();
        }
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        self.client.send(Command::DebugState { base, token });
        self.fetch_debug_file();
        self.fetch_graph();
    }

    fn fetch_debug_file(&mut self) {
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        let name = self.debug_file_name.clone();
        self.client.send(Command::DebugFile { base, token, name });
    }

    fn fetch_debug_state(&mut self) {
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        self.client.send(Command::DebugState { base, token });
    }

    fn fetch_graph(&mut self) {
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        self.client.send(Command::ListConversations { base, token });
    }

    /// 把后端数据投影成关系图：会话是中枢节点，消息按时间串成链。
    /// `messages.parent_id` 还没落地（Phase 3），所以这里只能是线性链——真分支图等那次迁移之后再画。
    fn rebuild_graph(&mut self) {
        let mut graph = egui_graphs::Graph::new();
        for conversation in &self.graph_conversations {
            let title = if conversation.title.is_empty() {
                format!("会话 {}", &conversation.id.to_string()[..8])
            } else {
                conversation.title.clone()
            };
            let mut previous = graph.add_node_with_label((), title);
            for (index, message) in self
                .graph_messages
                .get(&conversation.id)
                .into_iter()
                .flatten()
                .enumerate()
            {
                let who = match message.role {
                    ApiRole::User => "你",
                    ApiRole::Assistant => "助手",
                };
                let excerpt: String = message.content.chars().take(12).collect();
                let node = graph
                    .add_node_with_label((), format!("{} {}·{}", index + 1, who, excerpt));
                graph.add_edge(previous, node, ());
                previous = node;
            }
        }
        self.graph = Some(graph);
    }

    /// 调试页：与设置页同构——左导航 + 分页，别把工具堆成一长条。
    /// 关系图独立成页还顺手解决滚轮语义之争（图里滚轮＝缩放，不再和页面滚动抢）。
    fn debug_view(&mut self, ui: &mut egui::Ui) {
        ui.horizontal(|ui| {
            ui.heading("调试");
            ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                let refresh = match self.debug_tab {
                    DebugTab::State => Some("刷新状态"),
                    DebugTab::Config => Some("重新读取"),
                    DebugTab::Log => None,
                    DebugTab::Graph => Some("重新拉取"),
                };
                if let Some(label) = refresh {
                    if ui.button(label).clicked() {
                        match self.debug_tab {
                            DebugTab::State => self.fetch_debug_state(),
                            DebugTab::Config => self.fetch_debug_file(),
                            DebugTab::Graph => self.fetch_graph(),
                            DebugTab::Log => {}
                        }
                    }
                }
            });
        });
        ui.separator();
        if !self.note.is_empty() {
            ui.label(RichText::new(&self.note).weak());
        }

        egui::Panel::left("debug_nav")
            .resizable(false)
            .default_size(150.0)
            .show(ui, |ui| {
                ui.add_space(6.0);
                for (tab, label) in DebugTab::ALL {
                    ui.selectable_value(&mut self.debug_tab, tab, label);
                }
            });

        match self.debug_tab {
            DebugTab::State => self.debug_state_tab(ui),
            DebugTab::Config => self.debug_config_tab(ui),
            DebugTab::Log => self.debug_log_tab(ui),
            DebugTab::Graph => self.debug_graph_tab(ui),
        }
    }

    fn debug_state_tab(&mut self, ui: &mut egui::Ui) {
        egui::ScrollArea::vertical()
            .id_salt("debug_state_scroll")
            .auto_shrink([false, false])
            .show(ui, |ui| match &self.debug_state {
                None => {
                    ui.label(RichText::new("加载中…（后端若刚重启，点右上角「刷新状态」）").weak());
                }
                Some(state) => {
                    let rows: [(&str, String); 9] = [
                        ("版本", state.version.clone()),
                        (
                            "鉴权",
                            if state.auth_enabled {
                                "已启用"
                            } else {
                                "未启用"
                            }
                            .to_owned(),
                        ),
                        ("配置目录", state.config_dir.clone()),
                        ("数据目录", state.data_dir.clone()),
                        ("数据库", state.db_path.clone()),
                        (
                            "会话 / 消息 / 模型",
                            format!(
                                "{} / {} / {}",
                                state.counts.conversations,
                                state.counts.messages,
                                state.counts.models
                            ),
                        ),
                        ("标题字数", state.chat_title_chars.to_string()),
                        (
                            "默认 provider / model / agent",
                            format!(
                                "{} / {} / {}",
                                state.default_provider, state.default_model, state.default_agent
                            ),
                        ),
                        (
                            "已配置 providers / agents",
                            format!("{} / {}", state.providers_configured, state.agents_configured),
                        ),
                    ];
                    egui::Grid::new("debug_state")
                        .num_columns(2)
                        .spacing([16.0, 6.0])
                        .show(ui, |ui| {
                            for (key, value) in rows {
                                ui.label(key);
                                ui.label(RichText::new(value).monospace());
                                ui.end_row();
                            }
                        });
                }
            });
    }

    fn debug_config_tab(&mut self, ui: &mut egui::Ui) {
        ui.label(
            RichText::new("只读白名单：config / providers / agents；密钥文件永不出现在这里").weak(),
        );
        let mut pick: Option<&'static str> = None;
        ui.horizontal(|ui| {
            for name in ["config.jsonc", "providers.jsonc", "agents.jsonc"] {
                if ui
                    .selectable_label(self.debug_file_name == name, name)
                    .clicked()
                {
                    pick = Some(name);
                }
            }
        });
        if let Some(name) = pick {
            self.debug_file_name = name.to_owned();
            self.fetch_debug_file();
        }

        match &self.debug_file {
            None => {
                ui.label(RichText::new("选一个文件查看").weak());
            }
            Some(file) => {
                ui.label(
                    RichText::new(format!(
                        "{}  ·  {} 字符",
                        file.name,
                        file.text.chars().count()
                    ))
                    .weak(),
                );
                egui::ScrollArea::vertical()
                    .id_salt("debug_raw_file")
                    .auto_shrink([false, false])
                    .show(ui, |ui| {
                        ui.add(egui::Label::new(RichText::new(&file.text).monospace()).wrap());
                    });
            }
        }
    }

    /// 请求日志纯本地，不需要刷新按钮。
    fn debug_log_tab(&mut self, ui: &mut egui::Ui) {
        let log = self.client.log_snapshot();
        if log.is_empty() {
            ui.label(RichText::new("（暂无）").weak());
            return;
        }
        egui::ScrollArea::vertical()
            .id_salt("debug_log")
            .auto_shrink([false, false])
            .stick_to_bottom(true)
            .show(ui, |ui| {
                for line in log {
                    ui.monospace(line);
                }
            });
    }

    /// 关系图占满整页：滚轮在这里只可能是缩放。
    fn debug_graph_tab(&mut self, ui: &mut egui::Ui) {
        ui.horizontal(|ui| {
            if ui
                .add_enabled(self.graph.is_some(), egui::Button::new("重新布局"))
                .clicked()
            {
                egui_graphs::reset::<egui_graphs::FruchtermanReingoldState>(
                    ui,
                    Some(GRAPH_ID.to_owned()),
                );
            }
            ui.label(
                RichText::new(
                    "节点＝会话与消息，边＝会话→首条、消息→下一条（父指针未落地，暂为线性链）；节点可拖动、滚轮缩放、拖空白平移",
                )
                .weak(),
            );
        });

        match &mut self.graph {
            None => {
                ui.label(RichText::new("点右上角「重新拉取」载入").weak());
            }
            Some(graph) => {
                let interactions = egui_graphs::SettingsInteraction::new()
                    .with_dragging_enabled(true)
                    .with_node_selection_enabled(true);
                let navigation = egui_graphs::SettingsNavigation::new()
                    .with_zoom_and_pan_enabled(true)
                    .with_fit_to_screen_enabled(false);
                ui.allocate_ui(ui.available_size(), |ui| {
                    egui_graphs::GraphView::<
                        egui_graphs::FruchtermanReingoldState,
                        egui_graphs::LayoutForceDirected<egui_graphs::FruchtermanReingold>,
                    >::new()
                    .with_interactions(&interactions)
                    .with_navigations(&navigation)
                    .with_id(Some(GRAPH_ID.to_owned()))
                    .show(ui, graph);
                });
            }
        }
    }

    fn sidebar(&mut self, ui: &mut egui::Ui) {
        let mut switch_to: Option<usize> = None;
        let mut delete: Option<usize> = None;
        let mut new_conv = false;

        let mut open_debug = false;
        egui::Panel::bottom("account_bar").show(ui, |ui| {
            ui.add_space(4.0);
            ui.separator();
            for (label, view) in [
                ("账户", View::Account),
                ("设置", View::Settings),
                ("调试", View::Debug),
            ] {
                if ui.selectable_label(self.view == view, label).clicked() {
                    self.view = view;
                    open_debug = view == View::Debug;
                }
            }
            ui.add_space(4.0);
        });
        if open_debug {
            self.fetch_debug();
        }

        ui.add_space(8.0);
        if ui.button("＋ 新建对话").clicked() {
            new_conv = true;
        }
        ui.add_space(8.0);
        ui.separator();
        egui::ScrollArea::vertical()
            .id_salt("conversation_list")
            .auto_shrink([false, false])
            .show(ui, |ui| {
                for (i, conv) in self.convs.iter().enumerate() {
                    ui.horizontal(|ui| {
                        if ui.selectable_label(i == self.current, &conv.title).clicked() {
                            switch_to = Some(i);
                        }
                        ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                            if close_button(ui).clicked() {
                                delete = Some(i);
                            }
                        });
                    });
                }
            });

        if new_conv {
            self.convs.insert(0, blank_conversation());
            self.current = 0;
            self.view = View::Chat;
            self.focus_pending = true;
        } else if let Some(i) = delete {
            self.convs.remove(i);
            if self.convs.is_empty() {
                self.convs.push(blank_conversation());
                self.current = 0;
            } else if self.current >= i {
                self.current = self.current.saturating_sub(1).min(self.convs.len() - 1);
            }
            self.focus_pending = true;
        } else if let Some(i) = switch_to {
            self.current = i;
            self.view = View::Chat;
            self.focus_pending = true;
        }
    }

    fn send(&mut self) {
        let conv = &mut self.convs[self.current];
        let text = conv.draft.trim().to_owned();
        if text.is_empty() {
            return;
        }
        if conv.title == NEW_TITLE {
            conv.title = text.chars().take(MOCK_TITLE_CHARS).collect();
        }
        conv.msgs.push(Msg {
            role: Role::User,
            text: text.clone(),
        });
        // 回声代替真实模型：对话接后端在 providers 能说话之后。
        conv.msgs.push(Msg {
            role: Role::Assistant,
            text: format!("（回声）{text}"),
        });
        conv.draft.clear();
    }

    fn chat_view(&mut self, ui: &mut egui::Ui, composing: bool, enter: bool) {
        egui::Panel::bottom("composer").show(ui, |ui| {
            ui.add_space(8.0);
            ui.horizontal(|ui| {
                let hint = if self.settings.enter_sends {
                    "输入消息…  Enter 发送 / Shift+Enter 换行"
                } else {
                    "输入消息…  Shift+Enter 换行"
                };
                let edit = ui.add_sized(
                    [ui.available_width() - 88.0, 88.0],
                    TextEdit::multiline(&mut self.convs[self.current].draft)
                        .desired_rows(3)
                        .hint_text(hint),
                );
                self.composer_id = Some(edit.id);
                if self.focus_pending {
                    edit.request_focus();
                    self.focus_pending = false;
                }
                if edit.has_focus() && self.preedit_active {
                    ui.ctx().request_repaint();
                }

                // egui 的 TextEdit 没有内置右键菜单（Ctrl+C/V 才是原生路径），这里补上。
                edit.context_menu(|ui| {
                    let has_sel = TextEditState::load(ui.ctx(), edit.id)
                        .and_then(|s| s.cursor.char_range())
                        .is_some_and(|r| r.primary != r.secondary);

                    if ui.add_enabled(has_sel, egui::Button::new("剪切")).clicked() {
                        self.menu_action = Some(MenuAction::Cut);
                        edit.request_focus();
                        ui.close();
                    }
                    if ui.add_enabled(has_sel, egui::Button::new("复制")).clicked() {
                        self.menu_action = Some(MenuAction::Copy);
                        edit.request_focus();
                        ui.close();
                    }
                    if ui.button("粘贴").clicked() {
                        self.menu_action = Some(MenuAction::Paste);
                        edit.request_focus();
                        ui.close();
                    }
                    ui.separator();
                    if ui.button("全选").clicked() {
                        self.menu_action = Some(MenuAction::SelectAll);
                        edit.request_focus();
                        ui.close();
                    }
                });

                let has_text = !self.convs[self.current].draft.trim().is_empty();
                let send = ui.add_enabled(has_text, egui::Button::new("发送"));
                if send.clicked() || (enter && self.settings.enter_sends && !composing && has_text)
                {
                    self.send();
                } else if enter && self.settings.enter_sends && !composing {
                    // multiline 的 Enter 会顺带插入换行，空输入时把它吃掉。
                    self.convs[self.current].draft.clear();
                }
            });
            ui.add_space(8.0);
        });

        let stick = self.settings.stick_to_bottom;
        let msgs = &self.convs[self.current].msgs;
        if msgs.is_empty() {
            ui.centered_and_justified(|ui| {
                ui.label(RichText::new("输入消息开始对话").weak());
            });
            return;
        }
        egui::ScrollArea::vertical()
            .id_salt("chat_messages")
            .auto_shrink([false, false])
            .stick_to_bottom(stick)
            .show(ui, |ui| {
                for msg in msgs {
                    let (label, color) = match msg.role {
                        Role::User => ("你", Color32::from_rgb(0x2f, 0x6f, 0xe0)),
                        Role::Assistant => ("助手", Color32::from_rgb(0x2f, 0x9e, 0x44)),
                    };
                    ui.horizontal(|ui| {
                        ui.colored_label(color, RichText::new(label).strong());
                        ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                            let text = msg.text.clone();
                            if ui.small_button("复制").clicked() {
                                ui.output_mut(|o| {
                                    o.commands.push(egui::OutputCommand::CopyText(text))
                                });
                            }
                        });
                    });
                    ui.add(egui::Label::new(RichText::new(&msg.text)).wrap());
                    ui.add_space(10.0);
                }
            });
    }
}

impl eframe::App for App {
    fn ui(&mut self, ui: &mut egui::Ui, _frame: &mut eframe::Frame) {
        self.pump_events();

        // 右键菜单动作延后一帧执行：注入必须发生在本帧 TextEdit 构建之前。
        if let Some(action) = self.menu_action.take() {
            match action {
                MenuAction::Copy => ui.ctx().input_mut(|i| i.events.push(egui::Event::Copy)),
                MenuAction::Cut => ui.ctx().input_mut(|i| i.events.push(egui::Event::Cut)),
                MenuAction::Paste => {
                    ui.ctx().send_viewport_cmd(egui::ViewportCommand::RequestPaste);
                }
                MenuAction::SelectAll => {
                    if let Some(id) = self.composer_id {
                        if let Some(mut state) = TextEditState::load(ui.ctx(), id) {
                            let end = self.convs[self.current].draft.chars().count();
                            state.cursor.set_char_range(Some(CCursorRange::two(
                                CCursor::new(0usize),
                                CCursor::new(end),
                            )));
                            state.store(ui.ctx(), id);
                        }
                    }
                }
            }
            self.focus_pending = true;
        }

        // IME 合成状态：空 `Preedit` 是"合成已结束"，只有非空 preedit 或本帧 Commit 才算合成中。
        let composing_before = self.preedit_active;
        let mut composing_now = false;
        for event in ui.ctx().input(|i| i.events.clone()) {
            let egui::Event::Ime(ime) = event else { continue };
            match &ime {
                egui::ImeEvent::Preedit { text, .. } => {
                    self.preedit_active = !text.is_empty();
                    composing_now |= self.preedit_active;
                }
                egui::ImeEvent::Commit(_) => {
                    self.preedit_active = false;
                    composing_now = true;
                }
                _ => {}
            }
        }
        let composing = composing_before || composing_now;
        let enter = ui
            .ctx()
            .input(|i| i.key_pressed(egui::Key::Enter) && !i.modifiers.shift);

        // 未连上后端：只给连接页。
        if self.backend != Backend::Online {
            egui::CentralPanel::default().show(ui, |ui| self.connect_screen(ui));
            if enter && self.backend != Backend::Checking {
                self.connect();
            }
            self.persist_settings();
            return;
        }

        egui::Panel::left("sidebar")
            .resizable(true)
            .default_size(260.0)
            .show(ui, |ui| self.sidebar(ui));

        egui::CentralPanel::default().show(ui, |ui| match self.view {
            View::Chat => self.chat_view(ui, composing, enter),
            View::Debug => self.debug_view(ui),
            View::Account => {
                ui.heading("账户");
                ui.separator();
                ui.add_space(6.0);
                let total_msgs: usize = self.convs.iter().map(|c| c.msgs.len()).sum();
                egui::Grid::new("account_form")
                    .num_columns(2)
                    .spacing([16.0, 10.0])
                    .show(ui, |ui| {
                        ui.label("显示名称");
                        if ui
                            .add(
                                TextEdit::singleline(&mut self.settings.username)
                                    .desired_width(240.0),
                            )
                            .changed()
                        {
                            // 账户页没有保存按钮：这里改的是已应用值，草稿跟着同步，
                            // 免得之后在前端设置里一保存又把它顶回去
                            self.settings_dirty = true;
                            self.frontend_draft.username = self.settings.username.clone();
                        }
                        ui.end_row();

                        ui.label("身份验证");
                        ui.label(if self.token.is_some() {
                            "已带口令"
                        } else {
                            "无口令（本地模式）"
                        });
                        ui.end_row();

                        ui.label("会话数");
                        ui.label(self.convs.len().to_string());
                        ui.end_row();

                        ui.label("消息数");
                        ui.label(total_msgs.to_string());
                        ui.end_row();
                    });
                ui.add_space(10.0);
                ui.label(RichText::new("会话与账户仍是本地占位，未接后端存储。").weak());
            }
            View::Settings => {
                ui.heading("设置");
                ui.separator();
                egui::Panel::left("settings_nav")
                    .resizable(false)
                    .default_size(150.0)
                    .show(ui, |ui| {
                        ui.add_space(6.0);
                        for (tab, label) in SettingsTab::ALL {
                            ui.selectable_value(&mut self.settings_tab, tab, label);
                        }
                    });
                // 与面板并列的固定页脚：内容多少都一样，它永远在右下角。
                // 两个设置页都走"手动保存才生效"——前端设置尤其如此：拖动缩放条时若立即应用，
                // 界面会在手底下变形，滑块根本拖不准。
                let footer = match self.settings_tab {
                    SettingsTab::Server => Some(("提交 Agents 与 provider 的改动", true)),
                    SettingsTab::Frontend => Some(("应用并保存到本机 frontend.jsonc", false)),
                    SettingsTab::About => None,
                };
                if let Some((hint, is_server)) = footer {
                    egui::Panel::bottom("settings_footer").show(ui, |ui| {
                        ui.add_space(6.0);
                        ui.separator();
                        ui.horizontal(|ui| {
                            ui.label(RichText::new(&self.note).weak());
                            let dirty = if is_server {
                                self.server_dirty()
                            } else {
                                self.frontend_dirty()
                            };
                            ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                                if ui
                                    .add_enabled(dirty, egui::Button::new("保存"))
                                    .on_hover_text(hint)
                                    .clicked()
                                {
                                    if is_server {
                                        self.save_server_settings();
                                    } else {
                                        self.save_frontend_settings(ui.ctx());
                                    }
                                }
                                if !is_server
                                    && ui
                                        .add_enabled(dirty, egui::Button::new("还原"))
                                        .on_hover_text("丢弃草稿，回到已保存的值")
                                        .clicked()
                                {
                                    self.frontend_draft = self.settings.clone();
                                }
                                if !dirty {
                                    ui.label(RichText::new("没有未保存的改动").weak());
                                }
                            });
                        });
                        ui.add_space(6.0);
                    });
                }

                egui::ScrollArea::vertical()
                    .id_salt("settings_content")
                    .auto_shrink([false, false])
                    .show(ui, |ui| {
                        ui.add_space(6.0);
                        match self.settings_tab {
                            SettingsTab::Server => self.server_settings(ui),
                            SettingsTab::Frontend => self.frontend_settings(ui),
                            SettingsTab::About => self.about(ui),
                        }
                    });
            }
        });

        self.persist_settings();
    }
}

/// agent 草稿与已加载的是否不同。
fn agent_draft_differs(agent: &Agent, name: &str, system_prompt: &str) -> bool {
    agent.name != name || agent.system_prompt != system_prompt
}

/// provider 草稿与已加载的是否不同；密钥只看"有没有填新的"。
fn provider_draft_differs(
    provider: &ProviderView,
    base_url: &str,
    headers: &BTreeMap<String, String>,
    api_key_filled: bool,
) -> bool {
    provider.base_url != base_url || &provider.headers != headers || api_key_filled
}

/// 画出来的关闭按钮——**不依赖字体覆盖**。
///
/// 抄 egui 自己 `Window` 关闭钮的做法（`window.rs` 里的 `close_button`）：两条对角线 + hover 反馈。
/// 用文字画 ✕ 会踩字体坑：Noto Sans CJK 不含这类符号字形，渲染出来就是白方框（口）。
fn close_button(ui: &mut egui::Ui) -> egui::Response {
    let (rect, response) = ui.allocate_exact_size(egui::vec2(18.0, 18.0), egui::Sense::click());
    response.widget_info(|| {
        egui::WidgetInfo::labeled(egui::WidgetType::Button, ui.is_enabled(), "删除")
    });
    let visuals = ui.style().interact(&response);
    let rect = rect.shrink(4.0).expand(visuals.expansion);
    let painter = ui.painter();
    painter.line_segment([rect.left_top(), rect.right_bottom()], visuals.fg_stroke);
    painter.line_segment([rect.right_top(), rect.left_bottom()], visuals.fg_stroke);
    response.on_hover_text("删除")
}

fn kind_label(kind: ProviderKind) -> &'static str {
    match kind {
        ProviderKind::OpenAiCompat => "openai-compat",
        ProviderKind::Dummy => "dummy",
    }
}

/// `Header: 值` 每行一条 → 映射；空行与 `//` 注释行忽略。
fn parse_header_lines(text: &str) -> BTreeMap<String, String> {
    let mut headers = BTreeMap::new();
    for line in text.lines() {
        let line = line.trim();
        if line.is_empty() || line.starts_with("//") {
            continue;
        }
        if let Some((key, value)) = line.split_once(':') {
            let key = key.trim();
            if !key.is_empty() {
                headers.insert(key.to_owned(), value.trim().to_owned());
            }
        }
    }
    headers
}

fn header_lines(headers: &BTreeMap<String, String>) -> String {
    headers
        .iter()
        .map(|(key, value)| format!("{key}: {value}"))
        .collect::<Vec<_>>()
        .join("\n")
}

#[cfg(test)]
mod tests {
    use std::collections::BTreeMap;

    use microchat::config::{Agent, ProviderKind};
    use microchat::registry::ProviderView;

    use super::{
        agent_draft_differs, header_lines, parse_header_lines, provider_draft_differs,
    };

    #[test]
    fn drafts_are_compared_against_loaded_state() {
        let agent = Agent {
            id: "a".to_owned(),
            name: "名字".to_owned(),
            system_prompt: "提示".to_owned(),
            ..Default::default()
        };
        assert!(!agent_draft_differs(&agent, "名字", "提示"));
        assert!(agent_draft_differs(&agent, "改名", "提示"));
        assert!(agent_draft_differs(&agent, "名字", "新提示"));

        let provider = ProviderView {
            id: "local".to_owned(),
            kind: ProviderKind::OpenAiCompat,
            base_url: "http://a/v1".to_owned(),
            headers: BTreeMap::new(),
            has_key: true,
            last_refresh_at: None,
            models: Vec::new(),
        };
        assert!(!provider_draft_differs(&provider, "http://a/v1", &BTreeMap::new(), false));
        assert!(provider_draft_differs(&provider, "http://b/v1", &BTreeMap::new(), false));
        assert!(
            provider_draft_differs(&provider, "http://a/v1", &BTreeMap::new(), true),
            "填了新密钥就算有改动"
        );
    }

    #[test]
    fn header_lines_roundtrip_ignores_blank_and_comment_lines() {
        let parsed = parse_header_lines(
            "X-Title: microchat\n\n// 注释\nHTTP-Referer:http://a.example\n坏行没有冒号\n",
        );
        assert_eq!(parsed.len(), 2);
        assert_eq!(parsed["X-Title"], "microchat");
        assert_eq!(parsed["HTTP-Referer"], "http://a.example");
        assert_eq!(
            header_lines(&parsed),
            "HTTP-Referer: http://a.example\nX-Title: microchat"
        );
    }
}
