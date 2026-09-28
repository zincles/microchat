//! microchat 前端（egui）。
//!
//! - 启动即尝试连接后端（默认 `http://127.0.0.1:8787`）；连不上就停在连接页，不进主界面；
//! - 设置分两处：**服务器设置**（后端持有，经 API 读写）与 **前端设置**（本机 JSON 文件）；
//! - 密码只存内存，不落盘；
//! - 会话与消息仍是本地占位：下一轮换成 API（等 providers 能说话之后）。

mod client;
mod fonts;
mod frontend_settings;
mod graph_node;

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
/// 空标题的显示兜底（真实标题由后端按 `config.chat.title_chars` 从首条消息生成）。
const NEW_TITLE: &str = "新对话";
/// 调试页关系图的 `GraphView` id 由 `GraphLayout::id()` 给出。
/// 关系图最多拉几个会话的消息（调试用，别把整库存拉进来）。
const GRAPH_CONVERSATION_LIMIT: usize = 3;

mod chat;
mod debug;
mod graph;
mod settings;
mod tasks;

pub(crate) fn run() -> eframe::Result {
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

/// 右键菜单动作，延后一帧交给 TextEdit 自己执行（见 `fn ui` 开头）。
#[derive(Clone, Copy, PartialEq)]
enum MenuAction {
    Cut,
    Copy,
    Paste,
    SelectAll,
}

/// 需要**延后一帧**执行的动作（下一帧在控件构建之前注入）。
///
/// 剪切/粘贴/全选都要靠控件自己改文本与光标：先把选区写回 `TextEditState`、再注入事件，
/// 控件就会照它自己的逻辑处理（这样也不必由我们直接改用户的 `String`，undo 状态也不打架）。
enum DeferredMenu {
    /// 文本框：`action` 只可能是剪切/粘贴/全选（复制是当场做的）。
    EditAction { id: egui::Id, action: MenuAction },
}

/// 右键菜单打开前记下的选区。
///
/// **为什么必须快照**：egui 的 TextEdit 在 `pointer.any_pressed()` 时就把选区折成单个光标
/// （`text_cursor_state.rs:79`，右键也算），丢焦点时还会再折一次（`builder.rs:559`）。
/// 等菜单弹出来再读选区，读到的永远是空的——这正是"右键剪切失效"的根因。
#[derive(Clone, Debug, PartialEq)]
struct SelectionSnapshot {
    id: egui::Id,
    start: usize,
    end: usize,
    text: String,
}

/// 按**字符**下标切片（egui 的光标是字符下标，不是字节）。
fn slice_chars(text: &str, start: usize, end: usize) -> String {
    text.chars()
        .skip(start)
        .take(end.saturating_sub(start))
        .collect()
}

/// 取当前选区（没有选区返回 `None`）。
fn selection_snapshot(ctx: &egui::Context, id: egui::Id, text: &str) -> Option<SelectionSnapshot> {
    let range = TextEditState::load(ctx, id)?.cursor.char_range()?;
    let (start, end) = if range.primary.index <= range.secondary.index {
        (range.primary.index, range.secondary.index)
    } else {
        (range.secondary.index, range.primary.index)
    };
    let (start, end): (usize, usize) = (start.into(), end.into());
    (start != end).then(|| SelectionSnapshot {
        id,
        start,
        end,
        text: slice_chars(text, start, end),
    })
}

/// 快照的维护策略。
///
/// 帧序（egui 0.36 实测，见 `snapshot_survives_the_real_frame_sequence`）：
/// - **右键按下**那一帧：`TextEditState` 里选区已被折成光标（`output.cursor_range` 慢一帧，别用它判断）；
/// - **右键松开**（= `secondary_clicked`，菜单同时弹出）那一帧：仍是折着的。
///
/// 因此：**只有非空选区才写入**，且右键交互期间一律不写（那时的"选区"是点出来的，不是用户选的）；
/// 耗时怎么显示：不到一秒给毫秒，之后给秒，超过一分钟给"1分23秒"。
/// 当前毫秒时间戳（前端这边只用来排轮询）。
fn now_ms_now() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis() as i64)
        .unwrap_or(0)
}

fn fmt_duration(ms: i64) -> String {
    match ms {
        ms if ms < 1000 => format!("{ms} ms"),
        ms if ms < 60_000 => format!("{:.1}s", ms as f64 / 1000.0),
        ms => format!("{}分{}秒", ms / 60_000, (ms % 60_000) / 1000),
    }
}

/// 清空要同时满足「有焦点 ＋ 鼠标没按着 ＋ 没在右键 ＋ 菜单没开」。
fn update_selection_snapshot(
    stored: &mut Option<SelectionSnapshot>,
    id: egui::Id,
    live: Option<SelectionSnapshot>,
    focused: bool,
    menu_open: bool,
    pointer_down: bool,
    secondary_clicked: bool,
) {
    if let Some(live) = live {
        if !pointer_down && !secondary_clicked {
            *stored = Some(live);
        }
        return;
    }
    if focused
        && !menu_open
        && !pointer_down
        && !secondary_clicked
        && stored.as_ref().is_some_and(|it| it.id == id)
    {
        *stored = None;
    }
}

/// 把快照里的选区**复原回 TextEdit**。
///
/// 菜单开着 / 右键交互期间每帧调一次：egui 在右键按下那一刻就把选区折成了单个光标，
/// 而用户需要看见自己即将剪掉的是哪一段（顺带免掉"选区闪一下"）。
fn restore_selection(ctx: &egui::Context, snapshot: &SelectionSnapshot) {
    let Some(mut state) = TextEditState::load(ctx, snapshot.id) else {
        return;
    };
    state.cursor.set_char_range(Some(CCursorRange::two(
        CCursor::new(snapshot.start),
        CCursor::new(snapshot.end),
    )));
    state.store(ctx, snapshot.id);
}

/// 给任意 `TextEdit` 挂上可用的右键菜单。**所有** TextEdit 都该调它一次。
///
/// 为什么必须这样（都是 egui 0.36 实测出来的）：
/// 1. **右键按下的那一帧**，TextEdit 就把选区折成了单个光标（`pointer.any_pressed`），
///    等到菜单弹出（松开那一帧）再读选区必然是空的 → 所以维护一份"上一帧的选区快照"；
/// 2. 右键交互期间与菜单开着时把选区**写回** `TextEditState`，否则高亮会闪一下；
/// 3. 复制当场做（写剪贴板）；剪切/粘贴/全选交给下一帧注入，让控件自己动手。
///
/// 返回需要延后执行的动作；调用方存起来，下一帧交给 [`App::ui`] 开头那段执行。
fn attach_edit_menu(
    ui: &egui::Ui,
    response: &egui::Response,
    text: &str,
    selection: &mut Option<SelectionSnapshot>,
    read_only: bool,
) -> Option<DeferredMenu> {
    let id = response.id;
    let live = selection_snapshot(ui.ctx(), id, text);
    let menu_open = response.context_menu_opened();
    let pointer_down = ui.input(|input| input.pointer.any_down());
    let secondary_clicked = response.clicked_by(egui::PointerButton::Secondary);

    let picked = edit_context_menu(
        response,
        selection.as_ref().filter(|it| it.id == id),
        read_only,
    );

    // 只有"这一帧没选动作"时才跑清空策略：菜单里点了剪切/粘贴时，快照还要留着给下一帧用。
    if picked.is_none() {
        update_selection_snapshot(
            selection,
            id,
            live,
            response.has_focus(),
            menu_open,
            pointer_down,
            secondary_clicked,
        );
    }

    if (menu_open || pointer_down || secondary_clicked)
        && let Some(snapshot) = selection.as_ref().filter(|it| it.id == id)
    {
        restore_selection(ui.ctx(), snapshot);
    }

    match picked? {
        // 复制：当场写剪贴板（用快照，不依赖控件）
        MenuAction::Copy => {
            if let Some(snapshot) = selection.take() {
                ui.ctx().copy_text(snapshot.text);
            }
            None
        }
        // 其余交给下一帧：控件自己会改文本/光标
        action => Some(DeferredMenu::EditAction { id, action }),
    }
}

/// 可选的文本框的右键菜单。egui 的 TextEdit **没有**内置菜单（Ctrl+C/V 才是原生路径），
/// 所以每个我们自己建的 TextEdit 都得挂一遍——不只是输入框。
///
/// 返回被选中的动作；真正执行延后一帧（见 `App::ui`），避免和本帧的输入处理抢。
fn edit_context_menu(
    response: &egui::Response,
    selection: Option<&SelectionSnapshot>,
    read_only: bool,
) -> Option<MenuAction> {
    let has_selection = selection.is_some();
    let mut picked = None;
    response.context_menu(|ui| {
        if ui
            .add_enabled(
                has_selection && !read_only,
                egui::Button::new("剪切"),
            )
            .clicked()
        {
            picked = Some(MenuAction::Cut);
            ui.close();
        }
        if ui
            .add_enabled(has_selection, egui::Button::new("复制"))
            .clicked()
        {
            picked = Some(MenuAction::Copy);
            ui.close();
        }
        if ui
            .add_enabled(!read_only, egui::Button::new("粘贴"))
            .clicked()
        {
            picked = Some(MenuAction::Paste);
            ui.close();
        }
        ui.separator();
        if ui.button("全选").clicked() {
            picked = Some(MenuAction::SelectAll);
            ui.close();
        }
    });
    picked
}

#[derive(Clone, Copy, PartialEq)]
enum View {
    Chat,
    Settings,
    Account,
    /// 后台任务一屏（生成 / 压缩 / 刷新模型……）。指示器点一下也来这儿。
    Tasks,
    Debug,
}

#[derive(Clone, Copy, PartialEq)]
enum SettingsTab {
    Connection,
    Agents,
    /// **能力（Ability）**：写死在代码里的固定流程（目前只有摘要器）。名字与说明来自代码，
    /// 这里只改它们的模板/渠道/模型 —— 与 Agent 并列、各管各的。
    Abilities,
    Models,
    Frontend,
    About,
}

impl SettingsTab {
    /// 各自独立成页：连接、Agent、模型与渠道各管各的，
    /// 别把三件事塞进同一页——找东西要滚半天，改 A 时也看不见 B 的状态。
    const ALL: [(Self, &'static str); 6] = [
        (Self::Connection, "连接"),
        (Self::Agents, "Agent"),
        (Self::Abilities, "能力"),
        (Self::Models, "模型与渠道"),
        (Self::Frontend, "前端设置"),
        (Self::About, "关于"),
    ];
}

/// 调试页的分页：与设置页同构（左导航 + 内容区），别把工具堆成一长条。
#[derive(Clone, Copy, PartialEq)]
enum DebugTab {
    State,
    Payload,
    Config,
    Log,
    Graph,
}

impl DebugTab {
    const ALL: [(Self, &'static str); 5] = [
        (Self::State, "后端状态"),
        (Self::Payload, "最近发送载荷"),
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
    /// 口令（= 后端 `config.json` 的 `server.auth_token`），只存内存。
    token: Option<String>,
    password: String,
    /// 服务器侧数据（服务器设置页用）。
    providers: Option<Vec<ProviderView>>,
    agents: Option<AgentsConfig>,
    selected_agent: Option<String>,
    /// 选中 agent 的 id（可改：保存时若变了就是**重命名**）。
    agent_id: String,
    agent_name: String,
    agent_prompt: String,
    new_agent_name: String,
    note: String,
    /// 调试页数据。
    /// 调试页「最近发送载荷」：后端内存里那一份（重启就没了）。
    debug_payload: Option<serde_json::Value>,
    debug_state: Option<DebugState>,
    debug_file: Option<RawFile>,
    debug_file_name: String,
    /// 调试页关系图：后端数据与投影出的图。
    graph: Option<BlockGraph>,
    /// 关系图：自己排布时算出的画布包围盒（"适配视图"用）。
    graph_bounds: egui::Rect,
    /// 重建后请求适配一次（每帧 fit 会与手动缩放/拖动打架，所以只做一次）。
    graph_needs_fit: bool,
    /// 会话与消息都来自后端（连上后拉取，不再有本地占位）。
    /// 会话带 `turn`：这一轮在不在跑，左栏据此标"生成中"。
    conversations: Vec<microchat::server::ConversationView>,
    messages: BTreeMap<Uuid, Vec<ApiMessage>>,
    /// 每条消息在同龄兄弟里的位置（切分支用）。按会话存。
    branches: BTreeMap<Uuid, BTreeMap<Uuid, microchat::store::BranchInfo>>,
    /// 当前会话的变量视图（全局 + 本会话操作日志 + 生效值），由后端算好。
    variables: Option<microchat::world::WorldStateView>,
    /// 上面那份变量属于哪个会话（切换时先清空，避免串台）。
    variables_for: Option<Uuid>,
    /// 后台任务一屏（`GET /tasks`）：左栏那行指示器与「任务」页共用这一份。
    task_board: Option<microchat::task::TaskBoard>,
    /// 下一次去问任务表的时间（毫秒时间戳）。
    task_poll_at: Option<i64>,
    /// 能力清单（摘要器…）：名字与说明来自代码（= 能做什么），模板/渠道/模型可覆盖（= 怎么用）。
    abilities: Option<Vec<microchat::server::AbilityView>>,
    /// 能力的编辑草稿，键 = 它的 id：`(模板, 渠道, 模型)`。
    ability_drafts: BTreeMap<String, (String, String, String)>,
    /// 「压缩 N 个块」那个输入框的草稿。
    compact_blocks: String,
    /// 正在等结果的那条会话（点了压缩之后；轮询 `/status` 直到 `compact` 不再 running）。
    compact_ask: Option<Uuid>,
    /// 每条会话的摘要（服务端现算的成员数与首尾），关系图与摘要面板用。
    summaries: BTreeMap<Uuid, Vec<microchat::registry::SummaryView>>,
    /// 当前会话的上下文占用（估算 + 上一轮上游实测），由后端算好。
    context_usage: Option<microchat::world::ContextUsage>,
    /// 上面那份占用属于哪个会话（切换时先清空，避免串台）。
    context_usage_for: Option<Uuid>,
    /// 服务端 `config.json` 的 `chat` 段（模型上下文 / 压缩阈值 / 标题字数）。
    chat_config: Option<microchat::config::ChatConfig>,
    /// 上面那段的编辑草稿（保存时才解析；空串 = 清空该项）。
    chat_model_context: String,
    chat_trigger: String,
    /// 每个模型"上下文覆盖值"的编辑草稿，键 = `(provider, upstream_id)`。
    model_context_drafts: BTreeMap<(String, String), String>,
    /// 正在编辑的消息：`(消息 id, 草稿)`；`None` = 都在只读态。
    editing: Option<(Uuid, String)>,
    /// 每个会话各自的输入草稿：切会话不丢字。
    drafts: BTreeMap<Uuid, String>,
    current: Option<Uuid>,
    /// 正在发送中的会话（POST 还没回来：这期间按钮也是禁用的）。
    pending_send: Option<Uuid>,
    /// 这一轮**生成**的状态：`(会话, 状态)`。发送被受理后由轮询驱动，
    /// `pending` / `streaming` 期间每 300ms 拉一次 `/status`——非流式下 `pending` 会一直
    /// 持续到整段回复回来，所以"在不在生成"必须由状态说了算，不能看 POST 回没回。
    turn: Option<(Uuid, microchat::turn::TurnStatus)>,
    /// 正在等「获取模型」回来的 provider：用来在列表刷新回来时说一句"拿到了多少个"
    /// （列表事件本身看不出是"谁触发的"）。
    refreshing: Option<String>,
    /// 这一轮**流式**已经收到的正文（只服务动画）与它的游标。
    /// 真消息落库后由 `/messages` 接管，这里就清空——它是"假的"，不进树、不参与计算。
    streamed: String,
    stream_cursor: usize,
    /// **思考**流（推理型模型先吐的那段）与它自己的游标。同样只服务动画。
    thinking: String,
    think_cursor: usize,
    /// 底栏常驻那行：连接状态（`已连接后端 v0.1.0`）。和 `note`（一次性动作结果）分开，
    /// 后者会被下一条消息顶掉，前者不该消失。
    connected: String,
    /// 下一次轮询的时刻（`turn` 还在跑时才有意义）。
    next_poll: Option<std::time::Instant>,
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
    view: View,
    settings_tab: SettingsTab,
    debug_tab: DebugTab,
    preedit_active: bool,
    focus_pending: bool,
    composer_id: Option<egui::Id>,
    /// 下一帧要执行的菜单动作（剪切/粘贴/全选靠控件自己动手，不能当场做）。
    deferred: Option<DeferredMenu>,
    /// 右键菜单要用的选区快照（见 `SelectionSnapshot`）。
    menu_selection: Option<SelectionSnapshot>,
    /// 会话列表最上面那块系统提示词是否展开（默认只露几行：它常常是整篇世界观）。
    system_prompt_open: bool,
}

impl App {
    pub(super) fn new(ctx: egui::Context) -> Self {
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
            agent_id: String::new(),
            agent_name: String::new(),
            agent_prompt: String::new(),
            new_agent_name: String::new(),
            note: String::new(),
            debug_state: None,
            debug_payload: None,
            debug_file: None,
            debug_file_name: String::new(),
            graph: None,
            graph_bounds: egui::Rect::NOTHING,
            graph_needs_fit: false,
            conversations: Vec::new(),
            messages: BTreeMap::new(),
            variables: None,
            context_usage: None,
            context_usage_for: None,
            task_board: None,
            task_poll_at: None,
            abilities: None,
            ability_drafts: BTreeMap::new(),
            compact_blocks: "1".to_owned(),
            compact_ask: None,
            summaries: BTreeMap::new(),
            chat_config: None,
            chat_model_context: String::new(),
            chat_trigger: String::new(),
            model_context_drafts: BTreeMap::new(),
            variables_for: None,
            editing: None,
            drafts: BTreeMap::new(),
            current: None,
            pending_send: None,
            turn: None,
            refreshing: None,
            connected: String::new(),
            streamed: String::new(),
            stream_cursor: 0,
            thinking: String::new(),
            think_cursor: 0,
            next_poll: None,
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
            view: View::Chat,
            settings_tab: SettingsTab::Connection,
            debug_tab: DebugTab::State,
            preedit_active: false,
            focus_pending: true,
            composer_id: None,
            deferred: None,
            menu_selection: None,
            branches: BTreeMap::new(),
            system_prompt_open: false,
        };
        app.connect();
        app
    }
}

impl App {
    pub(super) fn connect(&mut self) {
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
}

impl App {
    /// **服务端侧数据的唯一取数入口**：连上后端时、点 ⟳ 时、进设置页时都走它。
    ///
    /// 别再在别处另写一份"要拉哪些" —— 曾经 `refresh_all` 与它各写一份，结果新加的两样
    /// （`chat` 段、能力清单）只进了 ⟳ 那条路，设置页就一直"加载中…"。
    pub(super) fn load_server_data(&mut self) {
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        self.client.send(Command::ListProviders {
            base: base.clone(),
            token: token.clone(),
        });
        self.client.send(Command::ListAgents {
            base: base.clone(),
            token: token.clone(),
        });
        self.client.send(Command::ListConversations {
            base: base.clone(),
            token: token.clone(),
        });
        // 服务端 `chat` 段（模型上下文 / 压缩阈值）：设置页那两个框的数据源
        self.client.send(Command::GetChatConfig {
            base: base.clone(),
            token: token.clone(),
        });
        // 后台任务一屏（指示器与「任务」页共用）
        self.task_poll_at = Some(now_ms_now() + 2_000);
        self.client.send(Command::ListTasks {
            base: base.clone(),
            token: token.clone(),
        });
        // 能力清单（名字/说明来自代码 + 现在生效的覆盖）
        self.client.send(Command::ListAbilities { base, token });
    }
}

impl App {
    /// 走进某个设置页时顺手拉一次：设置页要的数据都在这儿，进来就是新的。
    pub(super) fn enter_settings_tab(&mut self, tab: SettingsTab) {
        match tab {
            SettingsTab::Agents
            | SettingsTab::Abilities
            | SettingsTab::Models
            | SettingsTab::Connection => self.load_server_data(),
            SettingsTab::Frontend | SettingsTab::About => {}
        }
    }
}

impl App {
    pub(super) fn fetch_conversations(&mut self) {
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        self.client.send(Command::ListConversations { base, token });
    }
}

impl App {
    pub(super) fn fetch_messages(&mut self, conversation: Uuid) {
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        self.client.send(Command::ListMessages {
            base,
            token,
            conversation,
        });
        // 变量跟着消息一起拉：消息里的状态块正是变量的来源，两者同时变化。
        self.fetch_conversation_state(conversation);
    }
}

impl App {
    /// 只拉变量（每次回复后单独调一次就够，不必重拉整段历史）。
    /// 把所有面板要的东西重新从服务器拉一遍。
    ///
    /// 服务端就在本机、请求都是毫秒级，所以这里不做增量、也不做节流：点一下就全量对齐，
    /// 免得界面上出现"某个角落还是旧数据"这种只有刷新才能解释的状态。
    pub(super) fn refresh_all(&mut self) {
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        self.client.send(Command::Check {
            base: base.clone(),
            token: token.clone(),
        });
        self.load_server_data();
        if let Some(conversation) = self.current {
            self.fetch_messages(conversation);
            self.fetch_conversation_state(conversation);
            self.fetch_branches(conversation);
        }
        self.note = "已向服务器同步".to_owned();
    }
}

impl App {
    /// 拉"每条消息在同龄兄弟里排第几"（界面上的「‹ 2/3 ›」）。
    pub(super) fn fetch_branches(&mut self, conversation: Uuid) {
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        self.client.send(Command::ListBranches {
            base,
            token,
            conversation,
        });
    }
}

impl App {
    /// 拉当前会话的一批"即时状态"：变量、上下文占用、**摘要列表**。
    ///
    /// 它们该在**同样的时机**变（切会话、发送之后、编辑之后），分批拉只是多几次往返。
    pub(super) fn fetch_conversation_state(&mut self, conversation: Uuid) {
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        self.client.send(Command::ListWorldState {
            base,
            token,
            conversation,
        });
        self.fetch_context(conversation);
        self.fetch_summaries(conversation);
    }
}

impl App {
    /// 拉某会话的摘要列表（关系图与摘要面板的数据源）。
    pub(super) fn fetch_summaries(&mut self, conversation: Uuid) {
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        self.client.send(Command::ListSummaries {
            base,
            token,
            conversation,
        });
    }
}

impl App {
    /// 拉当前会话的上下文占用（`/context`：只有数字，没有正文）。
    pub(super) fn fetch_context(&mut self, conversation: Uuid) {
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        self.client.send(Command::ContextUsage {
            base,
            token,
            conversation,
        });
    }
}

impl App {
    pub(super) fn create_conversation(&mut self) {
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        self.client.send(Command::CreateConversation { base, token });
    }
}

impl App {
    pub(super) fn delete_conversation(&mut self, conversation: Uuid) {
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        self.client.send(Command::DeleteConversation {
            base,
            token,
            conversation,
        });
    }
}

impl App {
    /// 渠道显示名：配置里的 `name` 优先，缺省用 id。
    ///
    /// **不带类型**：`openai-compat` / `dummy` 这类是设置页要关心的事，
    /// 聊天窗口上方只留"谁家的哪个模型"。
    /// 未选、或已不在配置里（providers.json 被改过）都说明白。
    pub(super) fn provider_label(&self, provider: &str) -> String {
        if provider.is_empty() {
            return "未选择（走兜底）".to_owned();
        }
        match self
            .providers
            .as_ref()
            .and_then(|list| list.iter().find(|p| p.id == provider))
        {
            Some(view) => view.name.clone().unwrap_or_else(|| view.id.clone()),
            None => format!("{provider}（已不在配置里）"),
        }
    }
}

impl App {
    /// 合并后的选择器标题：`渠道 / 模型`（渠道带类型，模型带显示名）。
    pub(super) fn model_picker_label(&self, provider: &str, model: &str) -> String {
        format!(
            "{} / {}",
            self.provider_label(provider),
            self.model_label(provider, model)
        )
    }
}

impl App {
    /// 模型显示名：优先取注册表里的显示名（dummy 会显示成「Dummy（测试用空模型）」）。
    pub(super) fn model_label(&self, provider: &str, model: &str) -> String {
        if model.is_empty() {
            return "未选择（回复走兜底）".to_owned();
        }
        let resolved = self
            .providers
            .as_ref()
            .and_then(|list| list.iter().find(|p| p.id == provider))
            .and_then(|p| p.models.iter().find(|m| m.upstream_id == model))
            .map(|m| m.name.clone());
        resolved.unwrap_or_else(|| format!("{provider}/{model}"))
    }
}

impl App {
    /// Agent 显示名：优先用 `agents.json` 里的 name，找不到就原样显示 id。
    pub(super) fn agent_label(&self, agent_id: &str) -> String {
        if agent_id.is_empty() {
            return "未指定".to_owned();
        }
        self.agents
            .as_ref()
            .and_then(|config| config.get(agent_id))
            .map(|agent| {
                if agent.name.is_empty() {
                    agent.id.clone()
                } else {
                    agent.name.clone()
                }
            })
            .unwrap_or_else(|| agent_id.to_owned())
    }
}

impl App {
    /// 一次把渠道与模型都定下来：下拉里选的那一项就是答案，不用替用户猜模型落点。
    /// 后端本来就同时收这两个字段，所以一条 PATCH 就够。
    pub(super) fn switch_model_choice(&mut self, conversation: Uuid, provider: String, model: String) {
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        self.client.send(Command::UpdateConversation {
            base,
            token,
            conversation,
            provider: Some(provider),
            model: Some(model),
            agent_id: None,
        });
    }
}

impl App {
    /// 渠道与模型合成一个下拉：每一项是「某个渠道下的某个模型」，选中即同时定下两者。
    ///
    /// 原来是"渠道 + 模型"两层联动：先选渠道、模型再按渠道过滤，换渠道时还得替用户
    /// 猜一个模型落点（同名保留、否则第一个）。上游可能几十上百个模型，所以下拉里
    /// 自带滚动，别让弹层长到屏幕外。
    pub(super) fn model_picker(&mut self, ui: &mut egui::Ui, conversation: Uuid, provider: &str, model: &str) {
        let mut picked: Option<(String, String)> = None;
        egui::ComboBox::from_id_salt("chat_model")
            .selected_text(RichText::new(self.model_picker_label(provider, model)).monospace())
            .width(360.0)
            .show_ui(ui, |ui| {
                egui::ScrollArea::vertical()
                    .max_height(360.0)
                    .show(ui, |ui| {
                        let rows = flat_model_rows(self.providers.as_deref().unwrap_or(&[]));
                        if rows.is_empty() {
                            ui.label(
                                RichText::new("还没有模型：去「设置 → 服务器设置」点「获取模型」").weak(),
                            );
                        }
                        for (label, provider_id, model_id) in rows {
                            let selected = provider_id == provider && model_id == model;
                            if ui.selectable_label(selected, label).clicked() {
                                picked = Some((provider_id, model_id));
                            }
                        }
                    });
            });
        if let Some((provider, model)) = picked {
            self.switch_model_choice(conversation, provider, model);
        }
    }
}

impl App {
    /// 换 Agent：提示词在请求组装时按 `agent_id` 解析——改 `agents.json` 会影响所有用它的会话。
    pub(super) fn switch_agent(&mut self, conversation: Uuid, agent_id: String) {
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        self.client.send(Command::UpdateConversation {
            base,
            token,
            conversation,
            provider: None,
            model: None,
            agent_id: Some(agent_id),
        });
    }
}

impl App {
    /// 切到某个会话：记住选中并拉它的消息（每次拉，保证看到最新）。
    pub(super) fn open_conversation(&mut self, conversation: Uuid) {
        self.current = Some(conversation);
        self.focus_pending = true;
        self.view = View::Chat;
        self.fetch_messages(conversation);
        self.fetch_branches(conversation);
    }
}

impl App {
    /// 发送当前草稿。发送期间禁用按钮；失败**保留草稿**，不吞用户打的字。
    pub(super) fn send_message(&mut self) {
        let Some(conversation) = self.current else {
            return;
        };
        let Some(text) = self
            .drafts
            .get(&conversation)
            .map(|draft| draft.trim().to_owned())
            .filter(|text| !text.is_empty())
        else {
            return;
        };
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        self.pending_send = Some(conversation);
        self.note.clear();
        self.client.send(Command::SendMessage {
            base,
            token,
            conversation,
            content: text,
        });
    }
}

impl App {
    /// 这一轮还在跑就按点轮询它的状态；跑完了交给事件处理去对齐界面。
    ///
    /// 非流式下后端会一直停在 `pending`（等整段回复），所以"在生成"这件事只有状态
    /// 说得清——POST 那个连接在后台线程里挂着，界面看不见它。流式落地后同一段代码
    /// 会在 `streaming` 期间持续跑，只是那时可以顺带把增量取回来。
    /// 任务指示器的节奏：每两秒问一次 `/tasks`（就这一个端点，很轻）。
    ///
    /// 与 `poll_turn` 分开：那个是"这一轮流到哪了"（300ms、只在忙时跑），
    /// 这个是"后台有哪些活在跑"（常开、两秒一次）。
    pub(super) fn poll_tasks(&mut self, ctx: &egui::Context) {
        const TASK_POLL: std::time::Duration = std::time::Duration::from_millis(2_000);
        if self.task_poll_at.is_some_and(|at| now_ms_now() >= at) {
            self.task_poll_at = None;
            let (base, token) = (self.settings.server_address.clone(), self.token.clone());
            self.client.send(Command::ListTasks { base, token });
        }
        if self.task_poll_at.is_none() {
            self.task_poll_at = Some(now_ms_now() + 2_000);
        }
        ctx.request_repaint_after(TASK_POLL);
    }

    pub(super) fn poll_turn(&mut self, ctx: &egui::Context) {
        const POLL: std::time::Duration = std::time::Duration::from_millis(300);
        let Some((conversation, status)) = self.turn.clone() else {
            self.next_poll = None;
            return;
        };
        if !status.phase.is_busy() && self.compact_ask.is_none() {
            self.next_poll = None;
            return;
        }
        let now = std::time::Instant::now();
        if let Some(at) = self.next_poll {
            if now < at {
                // 还没到点：让界面到点自己醒（egui 没有输入就睡）
                ctx.request_repaint_after(at - now);
                return;
            }
        }
        self.next_poll = Some(now + POLL);
        ctx.request_repaint_after(POLL);
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        self.client.send(Command::ConversationStatus {
            base: base.clone(),
            token: token.clone(),
            conversation,
        });
        // 已经在出字了：顺带把新那一段取回来（游标读，不消费）。
        // `pending` 时跳过——那时候还没有字。
        if status.phase == microchat::turn::Phase::Streaming {
            self.client.send(Command::TurnText {
                base,
                token,
                conversation,
                from: self.stream_cursor,
                think_from: self.think_cursor,
            });
        }
    }
}

impl App {
    pub(super) fn pump_events(&mut self) {
        while let Some(event) = self.client.try_recv() {
            match event {
                Event::Checked(Ok(version)) => {
                    self.backend = Backend::Online;
                    // 连接状态常驻底栏；动作结果走 note（会被下一条顶掉）
                    self.connected = format!("已连接后端 v{version}");
                    self.note.clear();
                    self.load_server_data();
                }
                Event::Checked(Err(message)) => self.backend = Backend::Offline(message),
                Event::Providers(Ok(list)) => {
                    // 「获取模型」刚回来：说一声拿到多少个（普通列一遍时这里什么都不说）
                    if let Some(provider) = self.refreshing.take() {
                        let count = list
                            .iter()
                            .find(|view| view.id == provider)
                            .map(|view| view.models.len())
                            .unwrap_or(0);
                        self.note = format!("已获取 {count} 个模型（{provider}）");
                    }
                    self.providers = Some(list);
                }
                Event::Providers(Err(message)) => {
                    // 失败也要说清楚：message 里带着状态码与后端给的 code
                    self.refreshing = None;
                    self.note = message;
                }
                Event::ProviderModelsCleared { provider, result } => match result {
                    Ok(value) => {
                        let count = value["deleted"].as_u64().unwrap_or(0);
                        self.note = format!("已删除 {count} 个模型（{provider}）");
                        let (base, token) =
                            (self.settings.server_address.clone(), self.token.clone());
                        self.client.send(Command::ListProviders { base, token });
                    }
                    Err(message) => self.note = message,
                },
                Event::Conversations(Ok(list)) => {
                    self.conversations = list;
                    // 选中项失效（首次连接 / 刚被删）→ 落到第一个
                    let still_there = self
                        .current
                        .is_some_and(|id| self.conversations.iter().any(|c| c.id == id));
                    if !still_there {
                        self.current = self.conversations.first().map(|c| c.id);
                        if let Some(id) = self.current {
                            // 换了会话就得把这一整套都拉一遍：只拉消息的话，
                            // 变量面板与「‹ 2/3 ›」会挂着上一个会话的东西。
                            self.fetch_messages(id);
                            self.fetch_conversation_state(id);
                            self.fetch_branches(id);
                        }
                    }
                    self.rebuild_graph();
                }
                Event::Conversations(Err(message)) => self.note = message,
                Event::ConversationCreated(Ok(conversation)) => {
                    self.messages.insert(conversation.id, Vec::new());
                    self.drafts.insert(conversation.id, String::new());
                    self.current = Some(conversation.id);
                    self.view = View::Chat;
                    self.focus_pending = true;
                    self.fetch_conversations();
                }
                Event::ConversationCreated(Err(message)) => self.note = message,
                Event::ConversationDeleted {
                    conversation,
                    result,
                } => match result {
                    Ok(()) => {
                        self.conversations.retain(|c| c.id != conversation);
                        self.messages.remove(&conversation);
                        self.drafts.remove(&conversation);
                        if self.current == Some(conversation) {
                            self.current = self.conversations.first().map(|c| c.id);
                            if let Some(id) = self.current {
                                self.fetch_messages(id);
                            }
                        }
                        self.rebuild_graph();
                    }
                    Err(message) => self.note = message,
                },
                Event::Messages {
                    conversation,
                    result,
                } => match result {
                    Ok(messages) => {
                        self.messages.insert(conversation, messages);
                        self.rebuild_graph();
                    }
                    Err(message) => self.note = message,
                },
                Event::WorldState {
                    conversation,
                    result,
                } => match result {
                    Ok(view) => {
                        self.variables = Some(view);
                        self.variables_for = Some(conversation);
                    }
                    Err(message) => self.note = message,
                },
                Event::ContextUsage {
                    conversation,
                    result,
                } => match result {
                    Ok(usage) => {
                        self.context_usage = Some(usage);
                        self.context_usage_for = Some(conversation);
                    }
                    Err(message) => self.note = message,
                },
                Event::Tasks(Ok(board)) => {
                    self.task_board = Some(board);
                    // 两秒后再看一次：这是一个"活着"的面板，但它只轮询这一个端点。
                    self.task_poll_at = Some(now_ms_now() + 2_000);
                }
                Event::Tasks(Err(message)) => self.note = message,
                Event::Abilities(Ok(list)) => {
                    // 草稿只在**第一次**填：别把正在编辑的内容冲掉
                    for ability in &list {
                        self.ability_drafts.entry(ability.id.clone()).or_insert_with(|| {
                            (
                                ability.system_prompt.clone(),
                                ability.provider.clone().unwrap_or_default(),
                                ability.model.clone().unwrap_or_default(),
                            )
                        });
                    }
                    self.abilities = Some(list);
                }
                Event::Abilities(Err(message)) => self.note = message,
                Event::AbilitiesSaved(Ok(list)) => {
                    // 保存回执：草稿跟着回到"已保存"状态（含服务端归一的空模板）
                    for ability in &list {
                        self.ability_drafts.insert(
                            ability.id.clone(),
                            (
                                ability.system_prompt.clone(),
                                ability.provider.clone().unwrap_or_default(),
                                ability.model.clone().unwrap_or_default(),
                            ),
                        );
                    }
                    self.note = "能力覆盖已保存".to_owned();
                    self.abilities = Some(list);
                }
                Event::AbilitiesSaved(Err(message)) => self.note = message,
                Event::CompactionAccepted {
                    conversation,
                    result,
                } => match result {
                    Ok(status) => {
                        self.note = format!("压缩中…（{} 个块）", status.blocks);
                        self.compact_ask = Some(conversation);
                        self.next_poll = None; // 下一帧立刻开始轮询
                    }
                    Err(message) => self.note = message,
                },
                Event::Summaries {
                    conversation,
                    result,
                } => match result {
                    Ok(list) => {
                        self.summaries.insert(conversation, list);
                        self.rebuild_graph();
                    }
                    Err(message) => self.note = message,
                },
                Event::ConversationArchived(Ok(receipt)) => {
                    self.note = format!(
                        "已归档 {} 条消息 → {}",
                        receipt["messages"], receipt["path"]
                    );
                }
                Event::ConversationArchived(Err(message)) => self.note = message,
                Event::ConversationForked(Ok(copy)) => {
                    self.note = format!("已复制出新会话：{}", copy.title);
                    // 选中副本并刷新列表（副本是全新的，消息要现拉）
                    self.current = Some(copy.id);
                    self.fetch_messages(copy.id);
                    self.fetch_conversation_state(copy.id);
                    self.fetch_branches(copy.id);
                    let (base, token) =
                        (self.settings.server_address.clone(), self.token.clone());
                    self.client.send(Command::ListConversations { base, token });
                }
                Event::ConversationForked(Err(message)) => self.note = message,
                Event::ChatConfig(Ok(chat)) => {
                    self.chat_model_context = chat.model_context_tokens.to_string();
                    self.chat_trigger = chat
                        .compact_trigger_tokens
                        .map(|value| value.to_string())
                        .unwrap_or_default();
                    self.chat_config = Some(chat);
                }
                Event::ChatConfig(Err(message)) => self.note = message,
                Event::ModelContextSaved {
                    provider,
                    upstream_id,
                    result,
                } => match result {
                    Ok(view) => {
                        self.model_context_drafts
                            .remove(&(provider.clone(), upstream_id.clone()));
                        self.note = match view.context_override {
                            Some(value) => {
                                format!("{provider}/{upstream_id} 的上下文覆盖已设为 {value}")
                            }
                            None => format!("{provider}/{upstream_id} 的上下文覆盖已清空"),
                        };
                        // 列表要跟着更新（那一行显示的正是这个覆盖值）
                        let (base, token) =
                            (self.settings.server_address.clone(), self.token.clone());
                        self.client.send(Command::ListProviders { base, token });
                    }
                    Err(message) => self.note = message,
                },
                Event::MessageEdited {
                    conversation,
                    result,
                } => match result {
                    Ok(updated) => {
                        if let Some(slot) = self
                            .messages
                            .get_mut(&conversation)
                            .and_then(|list| list.iter_mut().find(|item| item.id == updated.id))
                        {
                            *slot = updated;
                        }
                        self.note = "消息已更新".to_owned();
                        // 正文里的状态块可能被改过 → 变量要重算（后端已重算，这里重拉）
                        self.fetch_conversation_state(conversation);
                    }
                    Err(message) => self.note = message,
                },
                Event::MessageDeleted {
                    conversation,
                    message,
                    result,
                } => match result {
                    Ok(value) => {
                        if let Some(list) = self.messages.get_mut(&conversation) {
                            list.retain(|item| item.id != message);
                        }
                        // 删的是整棵子树：条数要说清楚，别只说"成功"
                        let count = value["deleted"].as_u64().unwrap_or(1);
                        self.note = format!("已删除 {count} 条");
                        // 树上少了东西：变量与兄弟信息都要重拉（leaf 也可能退回去了）
                        self.fetch_messages(conversation);
                        self.fetch_conversation_state(conversation);
                        self.fetch_branches(conversation);
                    }
                    Err(err) => self.note = err,
                },
                Event::ConversationUpdated(Ok(updated)) => {
                    if let Some(slot) = self.conversations.iter_mut().find(|c| c.id == updated.id) {
                        // 列表里的是 `ConversationView`（会话 + 这一轮的状态），只换会话本体
                        slot.conversation = updated;
                    }
                    self.note = "会话设置已更新".to_owned();
                }
                Event::ConversationUpdated(Err(message)) => self.note = message,
                Event::Turn {
                    conversation,
                    result,
                } => {
                    self.pending_send = None;
                    match result {
                        Ok(accepted) => {
                            self.drafts.insert(conversation, String::new());
                            self.note = format!("{} 生成中…", accepted.backend);
                            // 新的一轮：动画缓冲区清零，游标回到 0
                            self.streamed.clear();
                            self.stream_cursor = 0;
                            self.thinking.clear();
                            self.think_cursor = 0;
                            // 回复在后台长出来：把状态接过来，靠轮询等它落地。
                            // 用户消息与占位消息都已经在树上了，拉一次就齐。
                            self.turn = Some((conversation, accepted.turn));
                            self.next_poll = None;
                            self.fetch_messages(conversation);
                            self.fetch_conversations();
                        }
                        // 409 = 这个会话还在生成（可能是另一个窗口开的），话得说清楚
                        Err(message) => self.note = message,
                    }
                }
                Event::Resent {
                    conversation,
                    result,
                } => {
                    self.pending_send = None;
                    match result {
                        Ok(accepted) => {
                            self.note = format!("{} 重新生成中…", accepted.backend);
                            self.turn = Some((conversation, accepted.turn));
                            self.next_poll = None;
                            // 树上多了一条兄弟：路径与兄弟数都可能变，直接重拉
                            self.fetch_messages(conversation);
                            self.fetch_branches(conversation);
                        }
                        Err(message) => self.note = message,
                    }
                }
                Event::TurnStatus {
                    conversation,
                    result,
                } => match result {
                    Ok(status) => {
                        let finished = !status.phase.is_busy();
                        let phase = status.phase;
                        let chars = status.chars;
                        let error = status.error.clone();
                        let compact_done = (self.compact_ask == Some(conversation))
                            .then(|| status.compact.clone())
                            .flatten()
                            .filter(|compact| !compact.is_running());
                        self.turn = Some((conversation, status));
                        if let Some(compact) = compact_done {
                            self.compact_ask = None;
                            self.next_poll = None;
                            // 压缩落地：摘要多了、出站会变、关系图要重画
                            self.fetch_summaries(conversation);
                            self.fetch_conversation_state(conversation);
                            self.note = match compact.error {
                                Some(error) => format!("压缩失败：{error}"),
                                None => format!("已把 {} 个块收进一条摘要", compact.compacted),
                            };
                        }
                        if finished {
                            self.next_poll = None;
                            // 流结束：动画缓冲退场，真消息由 /messages 接管（同一个 id）
                            self.streamed.clear();
                            self.stream_cursor = 0;
                            self.thinking.clear();
                            self.think_cursor = 0;
                            // 回复落地（或失败）：正文、变量、兄弟数、列表都变过
                            self.fetch_messages(conversation);
                            self.fetch_conversation_state(conversation);
                            self.fetch_branches(conversation);
                            self.fetch_conversations();
                            self.note = match phase {
                                microchat::turn::Phase::Error => format!(
                                    "生成失败：{}（点「重新发送」可以再试）",
                                    error.unwrap_or_else(|| "原因见后端日志".to_owned())
                                ),
                                _ => format!("回复完成：{chars} 字"),
                            };
                        }
                    }
                    Err(message) => {
                        // 拉不到状态（会话被删、连接断了）：别继续每 300ms 敲一次
                        self.note = message;
                        self.turn = None;
                        self.next_poll = None;
                    }
                },
                Event::TurnText {
                    conversation,
                    from,
                    think_from,
                    result,
                } => {
                    let _ = conversation;
                    if let Ok(slice) = result {
                        // 只收游标对得上的那一段：重复或乱序的丢掉
                        if from == self.stream_cursor {
                            self.streamed.push_str(&slice.text);
                            self.stream_cursor = slice.next;
                        }
                        if think_from == self.think_cursor {
                            self.thinking.push_str(&slice.thinking);
                            self.think_cursor = slice.think_next;
                        }
                    }
                    // 拉不到不值得打扰用户：最终那条消息由 /messages 接管
                }
                Event::Stopped {
                    conversation,
                    result,
                } => match result {
                    Ok(value) => {
                        self.note = if value["stopped"].as_bool().unwrap_or(false) {
                            "已停止生成".to_owned()
                        } else {
                            "没有正在生成的回复".to_owned()
                        };
                        self.turn = None;
                        self.next_poll = None;
                        self.fetch_messages(conversation);
                        self.fetch_conversations();
                    }
                    Err(message) => self.note = message,
                },
                Event::LeafSwitched {
                    conversation,
                    result,
                } => match result {
                    Ok(_) => {
                        self.note = "已切到那条分支".to_owned();
                        // 路径换了：消息、变量、兄弟信息全都要重拉。
                        // 变量表是后端**沿当前路径现演**的（没有派生表），所以这一次重拉
                        // 就是"重新计算"本身——切到哪条分支，世界状态就是那条分支的样子。
                        self.fetch_messages(conversation);
                        self.fetch_conversation_state(conversation);
                        self.fetch_branches(conversation);
                    }
                    Err(message) => self.note = message,
                },
                Event::Branches {
                    conversation,
                    result,
                } => match result {
                    Ok(map) => {
                        self.branches.insert(conversation, map);
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
                Event::AgentSaved(Ok(agent)) => {
                    // 改了 id 的话，选中项要跟着挪到新 id 上，否则下一帧编辑器就"找不到人"了
                    self.selected_agent = Some(agent.id.clone());
                    self.load_selected_agent();
                    self.note = "已保存".to_owned();
                    let (base, token) = (self.settings.server_address.clone(), self.token.clone());
                    self.client.send(Command::ListAgents { base, token });
                    if let Some(conversation) = self.current {
                        self.fetch_messages(conversation);
                        self.fetch_conversations();
                    }
                }
                Event::AgentSaved(Err(message)) => self.note = message,
                Event::AgentWritten(Ok(())) => {
                    self.note = "已保存".to_owned();
                    let (base, token) = (self.settings.server_address.clone(), self.token.clone());
                    self.client.send(Command::ListAgents { base, token });
                }
                Event::AgentWritten(Err(message)) => self.note = message,
                Event::DebugState(Ok(state)) => self.debug_state = Some(state),
                Event::DebugState(Err(message)) => self.note = message,
                Event::DebugPayload(Ok(payload)) => self.debug_payload = Some(payload),
                Event::DebugPayload(Err(message)) => self.note = message,
                Event::DebugFile(Ok(file)) => self.debug_file = Some(file),
                Event::DebugFile(Err(message)) => self.note = message,
            }
        }
    }
}

impl App {
    pub(super) fn load_selected_agent(&mut self) {
        let (id, name, prompt) = match self
            .selected_agent
            .as_ref()
            .and_then(|id| self.agents.as_ref()?.get(id))
        {
            Some(agent) => (
                agent.id.clone(),
                agent.name.clone(),
                agent.system_prompt.clone(),
            ),
            None => (String::new(), String::new(), String::new()),
        };
        self.agent_id = id;
        self.agent_name = name;
        self.agent_prompt = prompt;
    }
}

impl App {
    /// 前端设置草稿有没有改动（与已应用的状态逐字段比较）。
    pub(super) fn frontend_dirty(&self) -> bool {
        self.frontend_draft != self.settings
    }
}

impl App {
    /// 应用并保存前端设置。主题与缩放都在**这一刻**才生效——拖动中应用会让界面在手底下变形。
    pub(super) fn save_frontend_settings(&mut self, ctx: &egui::Context) {
        self.settings = self.frontend_draft.clone();
        ctx.set_theme(self.settings.theme.to_egui());
        ctx.set_zoom_factor(self.settings.zoom);
        self.settings_dirty = true;
        self.note = "前端设置已保存".to_owned();
    }
}

impl App {
    /// 服务器设置里有没有未保存的改动。
    pub(super) fn server_dirty(&self) -> bool {
        self.agent_draft_id().is_some()
            || self.provider_draft_id().is_some()
            || self.abilities_dirty()
            || self.chat_config_dirty()
            || self.model_context_changes().map_or(true, |changes| !changes.is_empty())
    }
}

impl App {
    /// 能力的草稿与已保存值不同吗（模板 / 渠道 / 模型）。
    pub(super) fn abilities_dirty(&self) -> bool {
        let Some(abilities) = self.abilities.as_ref() else {
            return false;
        };
        abilities.iter().any(|ability| {
            self.ability_drafts
                .get(&ability.id)
                .is_some_and(|(prompt, provider, model)| {
                    prompt != &ability.system_prompt
                        || provider != ability.provider.as_deref().unwrap_or_default()
                        || model != ability.model.as_deref().unwrap_or_default()
                })
        })
    }
}

impl App {
    /// `chat` 段草稿与已保存值不同吗（模型上下文 / 摘要触发阈值）。
    ///
    /// **解析不了也算"有改动"**：让页脚「保存」可点，点了才告诉他错在哪 ——
    /// 一个填错的框把整页锁死是最烦人的。
    pub(super) fn chat_config_dirty(&self) -> bool {
        let Some(saved) = self.chat_config.as_ref() else {
            return false; // 还没读回来：别拿空草稿去覆盖服务器
        };
        match parse_chat_drafts(&self.chat_model_context, &self.chat_trigger) {
            Ok((model_context_tokens, compact_trigger_tokens)) => {
                model_context_tokens != saved.model_context_tokens
                    || compact_trigger_tokens != saved.compact_trigger_tokens
            }
            Err(_) => true,
        }
    }
}

impl App {
    /// 覆盖草稿里与**已保存值**不同的那些：`(provider, upstream_id, 新值)`。
    ///
    /// 框里留空 = 不覆盖（`None`）—— 与"清掉覆盖"是同一件事，不需要额外按钮。
    /// `Err` = 有填错的（保存时原样报出来，不猜他想填什么）。
    pub(super) fn model_context_changes(&self) -> Result<Vec<(String, String, Option<i64>)>, String> {
        let Some(providers) = self.providers.as_ref() else {
            return Ok(Vec::new());
        };
        let mut changes = Vec::new();
        for provider in providers {
            for model in &provider.models {
                let key = (provider.id.clone(), model.upstream_id.clone());
                let Some(draft) = self.model_context_drafts.get(&key) else {
                    continue;
                };
                let trimmed = draft.trim();
                let parsed = if trimmed.is_empty() {
                    None
                } else {
                    Some(
                        trimmed
                            .parse::<i64>()
                            .ok()
                            .filter(|value| *value > 0)
                            .ok_or_else(|| format!("{} 的覆盖值要填正数", model.upstream_id))?,
                    )
                };
                if parsed != model.context_override {
                    changes.push((key.0, key.1, parsed));
                }
            }
        }
        Ok(changes)
    }
}

impl App {
    /// 有改动的 agent id（保存与提示共用同一份判定）。
    pub(super) fn agent_draft_id(&self) -> Option<String> {
        let id = self.selected_agent.as_ref()?;
        let agent = self.agents.as_ref()?.get(id)?;
        agent_draft_differs(agent, &self.agent_id, &self.agent_name, &self.agent_prompt)
            .then(|| id.clone())
    }
}

impl App {
    /// 有改动的 provider id。密钥只看"有没有填新的"——旧密钥读不回来。
    pub(super) fn provider_draft_id(&self) -> Option<String> {
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
}

impl App {
    /// 页脚「保存」：把面板里所有待提交的草稿一次写回后端。
    pub(super) fn save_server_settings(&mut self) {
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        if self.abilities_dirty() {
            let Some(abilities) = self.abilities.as_ref() else {
                return;
            };
            let mut config = microchat::config::AbilitiesConfig::default();
            for ability in abilities {
                let Some((prompt, provider, model)) = self.ability_drafts.get(&ability.id) else {
                    continue;
                };
                let (template, provider, model) = (
                    prompt.trim().to_owned(),
                    provider.trim().to_owned(),
                    model.trim().to_owned(),
                );
                // 与内置一字不差 ⇒ 写**空模板**（文件干净，"这就是内置"也一目了然）
                let system_prompt = if template == ability.builtin_prompt.trim() {
                    String::new()
                } else {
                    template
                };
                config.abilities.insert(
                    ability.id.clone(),
                    microchat::config::AbilityOverride {
                        system_prompt,
                        provider: (!provider.is_empty()).then_some(provider),
                        model: (!model.is_empty()).then_some(model),
                        params: ability.params.clone(),
                        ..Default::default()
                    },
                );
            }
            self.client.send(Command::SaveAbilities {
                base: base.clone(),
                token: token.clone(),
                config,
            });
        }
        if self.chat_config_dirty() {
            match parse_chat_drafts(&self.chat_model_context, &self.chat_trigger) {
                Ok((model_context_tokens, compact_trigger_tokens)) => {
                    let title_chars = self
                        .chat_config
                        .as_ref()
                        .map(|chat| chat.title_chars)
                        .unwrap_or(32);
                    self.client.send(Command::SaveChatConfig {
                        base: base.clone(),
                        token: token.clone(),
                        chat: microchat::config::ChatConfig {
                            title_chars,
                            model_context_tokens,
                            compact_trigger_tokens,
                        },
                    });
                }
                Err(message) => self.note = message,
            }
        }
        match self.model_context_changes() {
            Ok(changes) => {
                for (provider, upstream_id, context_override) in changes {
                    self.client.send(Command::SetModelContext {
                        base: base.clone(),
                        token: token.clone(),
                        provider,
                        upstream_id,
                        context_override,
                    });
                }
            }
            Err(message) => self.note = message,
        }
        if let Some(id) = self.agent_draft_id() {
            self.client.send(Command::SaveAgent {
                base: base.clone(),
                token: token.clone(),
                id,
                // 只有真改了才带 new_id（等于重命名：后端会把引用一起搬）
                new_id: (self.agent_id != self.selected_agent.clone().unwrap_or_default())
                    .then(|| self.agent_id.clone()),
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
}

impl App {
    pub(super) fn persist_settings(&mut self) {
        if !self.settings_dirty {
            return;
        }
        self.settings_dirty = false;
        if let Err(e) = self.settings.save() {
            self.note = format!("前端设置保存失败: {e}");
        }
    }
}

impl App {
    /// 真正会发给模型的系统提示词：会话级覆盖优先，否则用 agent 的（内置默认也算）。
    pub(super) fn effective_system_prompt(&self, conversation: &ApiConversation) -> Option<String> {
        let agent_prompt = self
            .agents
            .as_ref()
            .and_then(|config| config.resolve(&conversation.agent_id))
            .map(|agent| agent.system_prompt);
        resolve_system_prompt(&conversation.system_prompt, agent_prompt.as_deref())
    }
}

impl eframe::App for App {
    fn ui(&mut self, ui: &mut egui::Ui, _frame: &mut eframe::Frame) {
        self.pump_events();
        self.poll_turn(ui.ctx());
        self.poll_tasks(ui.ctx());

        // 右键菜单动作延后一帧执行：注入必须发生在本帧 TextEdit 构建之前。
        //
        // 剪切/粘贴/全选都走"先把选区写回 TextEditState，再注入事件"这条路——
        // 控件自己会照它的逻辑改文本与光标（我们不去直接改用户的 String）。
        if let Some(deferred) = self.deferred.take() {
            match deferred {
                DeferredMenu::EditAction { id, action } => {
                    ui.ctx().memory_mut(|memory| memory.request_focus(id));
                    match action {
                        MenuAction::Cut => {
                            if let Some(snapshot) = self
                                .menu_selection
                                .as_ref()
                                .filter(|snapshot| snapshot.id == id)
                            {
                                restore_selection(ui.ctx(), snapshot);
                            }
                            self.menu_selection = None;
                            ui.ctx().input_mut(|input| input.events.push(egui::Event::Cut));
                        }
                        MenuAction::Paste => {
                            ui.ctx().send_viewport_cmd(egui::ViewportCommand::RequestPaste);
                        }
                        MenuAction::SelectAll => {
                            // 用 egui 原生的 Ctrl+A：不用知道正文长度
                            ui.ctx().input_mut(|input| {
                                input.events.push(egui::Event::Key {
                                    key: egui::Key::A,
                                    physical_key: None,
                                    pressed: true,
                                    repeat: false,
                                    modifiers: egui::Modifiers::COMMAND,
                                })
                            });
                        }
                        MenuAction::Copy => {}
                    }
                }
            }
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
            .min_size(160.0)
            .show(ui, |ui| self.sidebar(ui));

        egui::CentralPanel::default().show(ui, |ui| match self.view {
            View::Chat => self.chat_view(ui, composing, enter),
            View::Tasks => self.tasks_view(ui),
            View::Debug => self.debug_view(ui),
            View::Account => {
                ui.heading("账户");
                ui.separator();
                ui.add_space(6.0);
                let loaded_msgs: usize = self.messages.values().map(Vec::len).sum();
                egui::Grid::new("account_form")
                    .num_columns(2)
                    .spacing([16.0, 10.0])
                    .show(ui, |ui| {
                        ui.label("显示名称");
                        let edit_response = ui.add(
                            TextEdit::singleline(&mut self.settings.username)
                                .desired_width(240.0),
                        );
                        if let Some(deferred) = attach_edit_menu(
                            ui,
                            &edit_response,
                            &self.settings.username,
                            &mut self.menu_selection,
                            false,
                        ) {
                            self.deferred = Some(deferred);
                        }
                        if edit_response.changed() {
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
                        ui.label(self.conversations.len().to_string());
                        ui.end_row();

                        ui.label("已加载消息");
                        ui.label(format!("{loaded_msgs}（仅已打开的会话）"));
                        ui.end_row();
                    });
                ui.add_space(10.0);
                ui.label(RichText::new("会话与消息都存在后端的 data/microchat.db 里。").weak());
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
                            if ui
                                .selectable_value(&mut self.settings_tab, tab, label)
                                .clicked()
                            {
                                self.enter_settings_tab(tab);
                            }
                        }
                    });
                // 与面板并列的固定页脚：内容多少都一样，它永远在右下角。
                // 两个设置页都走"手动保存才生效"——前端设置尤其如此：拖动缩放条时若立即应用，
                // 界面会在手底下变形，滑块根本拖不准。
                let footer = match self.settings_tab {
                    SettingsTab::Agents => Some(("提交 Agent 的改动", true)),
                    SettingsTab::Abilities => Some(("提交工具的改动", true)),
                    SettingsTab::Models => Some(("提交 provider 与模型的改动", true)),
                    SettingsTab::Frontend => Some(("应用并保存到本机 frontend.json", false)),
                    // 连接页只有只读信息与「断开」；关于页没有可保存的东西。
                    SettingsTab::Connection | SettingsTab::About => None,
                };
                if let Some((hint, is_server)) = footer {
                    egui::Panel::bottom("settings_footer").show(ui, |ui| {
                        ui.add_space(6.0);
                        ui.separator();
                        ui.horizontal(|ui| {
                            // 常驻的连接状态 + 最近一次动作的结果（两者互不顶替）
                            ui.label(RichText::new(&self.connected).weak());
                            if !self.note.is_empty() {
                                ui.label(RichText::new("·").weak());
                                ui.label(RichText::new(&self.note).weak());
                            }
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
                            SettingsTab::Connection => self.settings_connection(ui),
                            SettingsTab::Agents => self.settings_agents(ui),
                            SettingsTab::Abilities => self.settings_abilities(ui),
                            SettingsTab::Models => self.settings_models(ui),
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
fn agent_draft_differs(agent: &Agent, id: &str, name: &str, system_prompt: &str) -> bool {
    agent.id != id || agent.name != name || agent.system_prompt != system_prompt
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

/// 真正会发给模型的系统提示词：会话级覆盖优先，否则用 agent 的。
/// 系统提示词默认露几行、多少字符（先到先算）。
const SYSTEM_PROMPT_PREVIEW_LINES: usize = 4;

const SYSTEM_PROMPT_PREVIEW_CHARS: usize = 400;

/// 摘要：最多 `LINES` 行、`CHARS` 个字符（按字符数截，不是字节——中文一个字三字节）。
/// 有省略就在末尾说还剩多少行，别让人以为提示词就这么短。
/// 解析「模型上下文」「摘要触发阈值」两个草稿框（判定与保存共用同一份规则）。
///
/// 触发阈值留空 = `None`（用预算 × 0.8）；模型上下文必须填正数。
fn parse_chat_drafts(model_context: &str, trigger: &str) -> Result<(usize, Option<usize>), String> {
    let model_context_tokens = model_context
        .trim()
        .parse::<usize>()
        .ok()
        .filter(|value| *value > 0)
        .ok_or_else(|| "「模型上下文」要填正数".to_owned())?;
    let compact_trigger_tokens = if trigger.trim().is_empty() {
        None
    } else {
        Some(
            trigger
                .trim()
                .parse::<usize>()
                .ok()
                .filter(|value| *value > 0)
                .ok_or_else(|| "「摘要触发阈值」要填正数，或留空".to_owned())?,
        )
    };
    Ok((model_context_tokens, compact_trigger_tokens))
}

/// token 数的缩写：`12.3k` / `1.0M`。**只此一处** —— 顶栏那行与设置页共用。
fn short_tokens(tokens: i64) -> String {
    if tokens >= 1_000_000 {
        format!("{:.1}M", tokens as f64 / 1_000_000.0)
    } else if tokens >= 1000 {
        format!("{:.1}k", tokens as f64 / 1000.0)
    } else {
        tokens.to_string()
    }
}

fn summarize_lines(text: &str) -> String {
    let lines: Vec<&str> = text.lines().collect();
    let kept = lines.len().min(SYSTEM_PROMPT_PREVIEW_LINES);
    let mut out = lines[..kept].join("\n");
    let chars = out.chars().count();
    if chars > SYSTEM_PROMPT_PREVIEW_CHARS {
        out = out.chars().take(SYSTEM_PROMPT_PREVIEW_CHARS).collect();
        out.push('…');
    }
    let dropped = lines.len().saturating_sub(kept);
    if dropped > 0 {
        out.push_str(&format!("\n…（还有 {dropped} 行）"));
    }
    // 只在真的截过时才补省略号
    if dropped == 0 && chars <= SYSTEM_PROMPT_PREVIEW_CHARS {
        return text.to_owned();
    }
    out
}

fn resolve_system_prompt(conversation_prompt: &str, agent_prompt: Option<&str>) -> Option<String> {
    let conversation_prompt = conversation_prompt.trim();
    if !conversation_prompt.is_empty() {
        return Some(conversation_prompt.to_owned());
    }
    agent_prompt
        .map(str::trim)
        .filter(|prompt| !prompt.is_empty())
        .map(str::to_owned)
}

/// 一个下拉要列的全部条目：`(显示文案, provider id, 上游模型 id)`，按文案排序。
///
/// 文案是「渠道 / 模型」——渠道优先用 `providers.json` 里的 `name`，缺省回退 id；
/// 模型名直接用后端算好的 `name`（三级回退在后端做，前端不再实现一遍）。
fn flat_model_rows(providers: &[ProviderView]) -> Vec<(String, String, String)> {
    let mut rows: Vec<(String, String, String)> = providers
        .iter()
        .flat_map(|view| {
            let provider_name = view.name.clone().unwrap_or_else(|| view.id.clone());
            view.models.iter().map(move |model| {
                (
                    format!("{provider_name} / {}", model.name),
                    view.id.clone(),
                    model.upstream_id.clone(),
                )
            })
        })
        .collect();
    rows.sort_by(|a, b| a.0.cmp(&b.0));
    rows
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

    use super::{SelectionSnapshot, slice_chars, update_selection_snapshot};
    use super::graph::{COLUMN_GAP, column_width, stacked_centers};
    use microchat::config::{Agent, ProviderKind};
    use microchat::registry::ProviderView;

    use super::{
        agent_draft_differs, flat_model_rows, header_lines, parse_header_lines,
        provider_draft_differs, resolve_system_prompt, summarize_lines,
        SYSTEM_PROMPT_PREVIEW_CHARS,
    };

    #[test]
    fn drafts_are_compared_against_loaded_state() {
        let agent = Agent {
            id: "a".to_owned(),
            name: "名字".to_owned(),
            system_prompt: "提示".to_owned(),
            ..Default::default()
        };
        assert!(!agent_draft_differs(&agent, "a", "名字", "提示"));
        assert!(agent_draft_differs(&agent, "改名", "名字", "提示"));
        assert!(agent_draft_differs(&agent, "a", "名字", "新提示"));
        assert!(
            agent_draft_differs(&agent, "另一个", "名字", "提示"),
            "改 id 也算改动（保存时会走重命名）"
        );

        let provider = ProviderView {
            id: "local".to_owned(),
            name: None,
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
    fn stacked_blocks_keep_apart() {
        // 列内自上而下：块高 26、间隔 26 → 中心 13、65、117
        let heights = [26.0_f32, 26.0, 26.0];
        let gap = 26.0;
        let centers = stacked_centers(&heights, gap);
        assert_eq!(centers, vec![13.0, 65.0, 117.0]);
        for index in 1..centers.len() {
            let needed = (heights[index - 1] + heights[index]) / 2.0 + gap;
            assert!(
                centers[index] - centers[index - 1] >= needed,
                "块 {} 与 {} 挨太近: {centers:?}",
                index - 1,
                index
            );
        }
        assert!(stacked_centers(&[], gap).is_empty());
    }

    #[test]
    fn conversation_columns_do_not_overlap() {
        // 两个会话＝两列，列宽取各列最宽的块；列中心距必须容得下两块半宽 + COLUMN_GAP
        let first = [
            "RPG·雨夜客栈",
            "system · 你是一个乐于助人的助手",
            "1 你·我想去镇上买点干粮",
        ];
        let second = ["新会话", "system · 简洁点", "1 你·在吗"];
        let widths = [first, second].map(|labels| {
            let sizes: Vec<_> = labels
                .iter()
                .map(|label| super::graph_node::block_size(label))
                .collect();
            column_width(&sizes)
        });

        let centers = stacked_centers(&widths, COLUMN_GAP);
        assert!(centers[1] > centers[0], "列必须从左往右递增: {centers:?}");
        let apart = centers[1] - centers[0];
        let needed = (widths[0] + widths[1]) / 2.0 + COLUMN_GAP;
        assert!(apart >= needed - 0.01, "两列重叠：相距 {apart} < {needed}");

        // 列宽取最宽的块，而不是第一块（否则窄标题会让整列错位）
        assert!(widths[0] >= super::graph_node::block_size(first[1]).x);
    }

    #[test]
    fn selection_math_is_by_chars_not_bytes() {
        let text = "你好HP=12世界";
        assert_eq!(slice_chars(text, 2, 7), "HP=12");
        assert_eq!(slice_chars(text, 0, 2), "你好");
        assert_eq!(slice_chars(text, 5, 99), "12世界", "越界按到结尾算");
        assert_eq!(slice_chars(text, 9, 9), "");
    }



    /// 回归两条（都来自真实踩坑）：
    /// 1. 会话标题必须**可截断**，否则侧栏被撑到标题那么宽、分隔线往左拖不动
    ///    （实测：不截断锁死 359，截断后 120 ✓）；
    /// 2. 选中底色必须画在标题**之前**——后画会盖住文字（选中项标题会消失）。
    #[test]
    fn sidebar_rows_truncate_and_paint_background_first() {
        use eframe::egui::{Align, Event, Layout, PointerButton, Pos2, Rect, Sense, Shape, vec2};

        let ctx = eframe::egui::Context::default();
        let title = "一个非常非常非常非常非常非常长的会话标题会撑开侧栏".to_owned();
        let input = |events: Vec<Event>| {
            let mut raw = eframe::egui::RawInput::default();
            raw.screen_rect = Some(Rect::from_min_size(Pos2::ZERO, vec2(1200.0, 800.0)));
            raw.events = events;
            raw
        };
        let pointer = |pos: Pos2, pressed: bool| Event::PointerButton {
            pos,
            button: PointerButton::Primary,
            pressed,
            modifiers: Default::default(),
        };

        let run = |events: Vec<Event>| {
            let mut width = 0.0f32;
            let mut row_rect = Rect::ZERO;
            let mut row_clicked = false;
            let mut selection_fill = eframe::egui::Color32::TRANSPARENT;
            let mut out = ctx.run_ui(input(events), |ctx| {
                let panel = eframe::egui::Panel::left("sidebar")
                    .resizable(true)
                    .default_size(260.0)
                    .min_size(160.0)
                    .show(ctx, |ui| {
                        selection_fill = ui.visuals().selection.bg_fill;
                        eframe::egui::ScrollArea::vertical()
                            .id_salt("conversation_list")
                            .auto_shrink([false, false])
                            .show(ui, |ui| {
                                for index in 0..3 {
                                    ui.horizontal(|ui| {
                                        let room = 26.0;
                                        let size = vec2(
                                            (ui.available_width() - room).max(40.0),
                                            ui.spacing().interact_size.y,
                                        );
                                        let (rect, row) =
                                            ui.allocate_exact_size(size, Sense::click());
                                        if index == 0 {
                                            row_rect = rect;
                                            row_clicked |= row.clicked();
                                        }
                                        if index == 0 {
                                            // 选中项：底色先画，文字后放
                                            ui.painter().rect_filled(
                                                rect,
                                                eframe::egui::CornerRadius::same(4),
                                                selection_fill,
                                            );
                                        }
                                        ui.put(
                                            rect.shrink2(vec2(4.0, 0.0)),
                                            eframe::egui::Label::new(&title)
                                                .truncate()
                                                .selectable(false),
                                        );
                                        ui.with_layout(
                                            Layout::right_to_left(Align::Center),
                                            |ui| {
                                                let _ = ui.small_button("✕");
                                            },
                                        );
                                    });
                                }
                            });
                    });
                width = panel.response.rect.width();
            });

            // 画出顺序：底色矩形必须排在它这行的文字之前
            let mut fill_at = None;
            let mut text_after = false;
            for (index, clipped) in out.shapes.iter().enumerate() {
                match &clipped.shape {
                    Shape::Rect(rect) if rect.fill == selection_fill => fill_at = Some(index),
                    Shape::Text(_) if fill_at.is_some() => text_after = true,
                    _ => {}
                }
            }
            out.textures_delta.clear();
            (width, fill_at.is_some(), text_after, row_rect, row_clicked)
        };

        let (start_width, has_fill, _, row_rect, _) = run(vec![]);
        assert!(has_fill, "选中项应当画了底色");
        let (_, _, text_after, _, _) = run(vec![]);
        assert!(text_after, "底色之后必须有文字——否则底色盖住了标题");

        // 点一下这一行：必须能触发（Label 默认 selectable，会盖在占位矩形上把点击吃掉）
        let center = row_rect.center();
        run(vec![Event::PointerMoved(center)]);
        run(vec![pointer(center, true)]);
        let (_, _, _, _, clicked) = run(vec![pointer(center, false)]);
        assert!(clicked, "点这一行应当能切换会话");

        let handle = Pos2::new(start_width, 400.0);
        run(vec![Event::PointerMoved(handle)]);
        run(vec![pointer(handle, true)]);
        run(vec![Event::PointerMoved(Pos2::new(120.0, 400.0))]);
        let (dragged, _, _, _, _) = run(vec![Event::PointerMoved(Pos2::new(120.0, 400.0))]);
        let (released, _, _, _, _) = run(vec![pointer(Pos2::new(120.0, 400.0), false)]);

        assert!(dragged < start_width, "往左拖应当变窄：{start_width} → {dragged}");
        assert!(released < 200.0, "松开后应当停在窄宽度：{released}");
    }

    /// 只读正文（消息）也必须能"选中 → 右键复制"。
    ///
    /// 实现用的是 egui 讨论区给的招：**绑到不可变 `&str` 的 TextEdit**，看起来就是一段文字，
    /// 但选区可读、Ctrl+C 原生可用、还能走和可编辑框同一套右键菜单。
    /// （`Label` 的选区存在私有字段里，取不到——所以不能用它。）
    #[test]
    fn readonly_text_is_selectable_copyable_and_uneditable() {
        use eframe::egui::{Event, Key, Modifiers, OutputCommand, PointerButton, Pos2, Rect, TextEdit, vec2};

        const BODY: &str = "第一行文字很长很长很长很长很长\n第二行也不短";
        let ctx = eframe::egui::Context::default();
        let mut current = BODY.to_owned();
        let input = |events: Vec<Event>| {
            let mut raw = eframe::egui::RawInput::default();
            raw.screen_rect = Some(Rect::from_min_size(Pos2::ZERO, vec2(600.0, 400.0)));
            raw.events = events;
            raw
        };
        let pointer = |pos: Pos2, button: PointerButton, pressed: bool| Event::PointerButton {
            pos,
            button,
            pressed,
            modifiers: Default::default(),
        };

        let run = |current: &mut String, events: Vec<Event>| {
            let mut observed: (Option<(usize, usize)>, Rect, Vec<String>) =
                (None, Rect::ZERO, Vec::new());
            let mut out = ctx.run_ui(input(events), |ctx| {
                eframe::egui::CentralPanel::default().show(ctx, |ui| {
                    ui.set_width(400.0);
                    let mut readonly = current.as_str();
                    let response = ui.add(
                        TextEdit::multiline(&mut readonly)
                            .frame(eframe::egui::Frame::NONE)
                            .desired_width(f32::INFINITY),
                    );
                    let range = super::TextEditState::load(ui.ctx(), response.id)
                        .and_then(|state| state.cursor.char_range())
                        .map(|range| {
                            let a: usize = range.primary.index.into();
                            let b: usize = range.secondary.index.into();
                            (a.min(b), a.max(b))
                        });
                    observed = (range, response.rect, Vec::new());
                });
            });
            let copies: Vec<String> = out
                .platform_output
                .commands
                .iter()
                .filter_map(|command| match command {
                    OutputCommand::CopyText(text) => Some(text.clone()),
                    _ => None,
                })
                .collect();
            out.textures_delta.clear();
            (observed.0, observed.1, copies)
        };

        let (_, rect, _) = run(&mut current, vec![]);

        // 拖选一段
        run(&mut current, vec![Event::PointerMoved(rect.left_center() + vec2(2.0, 4.0))]);
        run(
            &mut current,
            vec![pointer(rect.left_center() + vec2(2.0, 4.0), PointerButton::Primary, true)],
        );
        run(
            &mut current,
            vec![Event::PointerMoved(rect.left_center() + vec2(120.0, 4.0))],
        );
        let (range, _, _) = run(
            &mut current,
            vec![pointer(
                rect.left_center() + vec2(120.0, 4.0),
                PointerButton::Primary,
                false,
            )],
        );
        let (start, end) = range.expect("只读文本也应当能选中");
        assert!(start < end, "选区应当非空: {start}..{end}");

        // 注入 Copy（右键菜单的复制走的同一条路）→ 剪贴板拿到选中的那一段
        let (_, _, copies) = run(&mut current, vec![Event::Copy]);
        let copied = copies.first().expect("应当往剪贴板写一段").clone();
        assert!(!copied.is_empty(), "复制的内容不该是空的");
        assert!(
            BODY.contains(&copied),
            "复制的应当是正文的一部分: {copied:?}"
        );

        // Ctrl+A 再复制 → 拿到整段正文
        run(
            &mut current,
            vec![Event::Key {
                key: Key::A,
                physical_key: None,
                pressed: true,
                repeat: false,
                modifiers: Modifiers::COMMAND,
            }],
        );
        let (_, _, copies) = run(&mut current, vec![Event::Copy]);
        assert_eq!(copies.first().map(String::as_str), Some(BODY));

        // 只读：打字不进正文
        run(&mut current, vec![Event::Text("注入的文字".to_owned())]);
        assert_eq!(current, BODY, "只读文本不该被输入篡改");
    }

    /// 本 bug 的回归测试：用**真的** TextEdit 跑帧，自动找出"哪一帧选区被折掉"，
    /// 再验证快照策略在那一帧保住了选区、且剪切范围仍能切出正确的文本。
    ///
    /// egui 0.36 实测帧序（`TextEditState` 口径）：
    /// - 拖选后：选区 0..11 在；
    /// - 右键**按下**那一帧：已被折成 11..11（光标落到点击处）；
    /// - 右键**松开**（= click，菜单同时弹出）那一帧：仍然是 11..11。
    ///
    /// 也就是说 **选区在按下的那一帧就没了**，而菜单要到松开才弹——所以快照必须在
    /// 按下之前就存在（每帧刷新），并且按下/松开这两帧都不能被"清空"逻辑误伤。
    #[test]
    fn snapshot_survives_the_real_frame_sequence() {
        use eframe::egui::{Event, PointerButton, Pos2, Rect, TextEdit, vec2};

        const INITIAL: &str = "hello world";
        let ctx = eframe::egui::Context::default();
        let mut text = INITIAL.to_owned();
        let input = |events: Vec<Event>| {
            let mut input = eframe::egui::RawInput::default();
            input.screen_rect = Some(Rect::from_min_size(Pos2::ZERO, vec2(400.0, 200.0)));
            input.events = events;
            input
        };
        let pointer = |pos: Pos2, button: PointerButton, pressed: bool| Event::PointerButton {
            pos,
            button,
            pressed,
            modifiers: Default::default(),
        };

        // 每帧返回：TextEdit 报出的选区、Response 的 id、矩形、是否刚刚右键点击
        let mut run_frame = |events: Vec<Event>| {
            let mut observed: (
                Option<(usize, usize)>,
                eframe::egui::Id,
                Rect,
                bool,
                bool,
            ) = (None, eframe::egui::Id::NULL, Rect::ZERO, false, false);
            let mut output = ctx.run_ui(input(events), |ctx| {
                eframe::egui::CentralPanel::default().show(ctx, |ui| {
                    let response = ui.add(TextEdit::singleline(&mut text));
                    let range = super::TextEditState::load(ui.ctx(), response.id)
                        .and_then(|state| state.cursor.char_range())
                        .map(|range| {
                            let start: usize = range.primary.index.into();
                            let end: usize = range.secondary.index.into();
                            (start.min(end), start.max(end))
                        });
                    observed = (
                        range,
                        response.id,
                        response.rect,
                        response.has_focus(),
                        response.clicked_by(PointerButton::Secondary),
                    );
                });
            });
            output.textures_delta.clear();
            observed
        };

        let text_len = INITIAL.chars().count();
        let (_, _, rect, ..) = run_frame(vec![]);

        // 拖选：按下 → 移到末尾 → 松开
        run_frame(vec![Event::PointerMoved(rect.left_center() + vec2(2.0, 0.0))]);
        run_frame(vec![pointer(rect.left_center() + vec2(2.0, 0.0), PointerButton::Primary, true)]);
        run_frame(vec![Event::PointerMoved(rect.right_center() - vec2(2.0, 0.0))]);
        let (range, ..) = run_frame(vec![pointer(
            rect.right_center() - vec2(2.0, 0.0),
            PointerButton::Primary,
            false,
        )]);
        let selected = range.expect("拖选之后应当有选区");
        assert_eq!(selected, (0, text_len), "应当整段选中: {selected:?}");

        // 应用快照策略（和线上同一条策略）
        let mut stored = None;
        let (range, id, _, focused, secondary) = run_frame(vec![]);
        update_selection_snapshot(
            &mut stored,
            id,
            range.map(|(start, end)| SelectionSnapshot {
                id,
                start,
                end,
                text: slice_chars(INITIAL, start, end),
            }),
            focused,
            false,
            false,
            secondary,
        );
        assert!(stored.is_some(), "有选区时应当存下快照");

        // ★ 右键按下：egui 会按点击位置改写光标（实测这一帧 state 变成 11..11）。
        // 不变量：无论 egui 报什么，快照都必须还是用户当初选的那段。
        let (range, id, _, focused, secondary) = run_frame(vec![pointer(
            rect.center(),
            PointerButton::Secondary,
            true,
        )]);
        println!("按下那一帧 state 报: {range:?}");
        update_selection_snapshot(
            &mut stored,
            id,
            range.map(|(start, end)| SelectionSnapshot {
                id,
                start,
                end,
                text: slice_chars(INITIAL, start, end),
            }),
            focused,
            false,
            true,
            secondary,
        );
        assert!(stored.is_some());

        // ★★ 右键松开：这一帧 egui 会把选区折成光标——快照必须活下来
        let (range, id, _, focused, secondary) = run_frame(vec![pointer(
            rect.center(),
            PointerButton::Secondary,
            false,
        )]);
        println!("松开那一帧 state 报: {range:?}（secondary_clicked={secondary}）");
        assert!(secondary, "松开那一帧 clicked_by(Secondary) 应当为真（菜单在这一帧弹出）");
        update_selection_snapshot(
            &mut stored,
            id,
            range.map(|(start, end)| SelectionSnapshot {
                id,
                start,
                end,
                text: slice_chars(INITIAL, start, end),
            }),
            focused,
            false,
            false,
            secondary,
        );
        let snapshot = stored.expect("松开那一帧快照必须活下来——剪切就靠它");

        // 快照里的文本与范围，正是用户当初选中的那一段
        assert_eq!((snapshot.start, snapshot.end), (0, text_len));
        assert_eq!(snapshot.text, INITIAL);

        // 快照给出的区间正是"要被切掉的那段"（真正的删除由控件自己执行）
        assert_eq!(slice_chars(INITIAL, snapshot.start, snapshot.end), INITIAL);
    }

    #[test]
    fn system_prompt_preview_stops_early_and_says_what_is_left() {
        let short = "第一行\n第二行";
        assert_eq!(summarize_lines(short), short, "没超就原样给");

        let long: String = (1..=9)
            .map(|i| format!("第 {i} 行"))
            .collect::<Vec<_>>()
            .join("\n");
        let out = summarize_lines(&long);
        assert!(out.starts_with("第 1 行\n第 2 行"));
        assert!(!out.contains("第 5 行"), "超过 4 行的不进预览");
        assert!(out.ends_with("（还有 5 行）"), "要说清还剩多少：{out}");

        let one_long_line = "字".repeat(900);
        let out = summarize_lines(&one_long_line);
        assert_eq!(out.chars().filter(|c| *c == '字').count(), SYSTEM_PROMPT_PREVIEW_CHARS);
        assert!(out.ends_with('…'), "按字符截断，别把中文切坏");
    }

    #[test]
    fn system_prompt_prefers_conversation_override() {
        assert_eq!(resolve_system_prompt("会话覆盖", Some("agent 的")), Some("会话覆盖".to_owned()));
        assert_eq!(resolve_system_prompt("  ", Some("agent 的")), Some("agent 的".to_owned()));
        assert_eq!(resolve_system_prompt("", None), None, "两边都空就没有系统提示词");
        assert_eq!(resolve_system_prompt("", Some("   ")), None);
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

    #[test]
    fn flat_model_rows_flatten_fall_back_and_sort() {
        use microchat::registry::{ModelView, ProviderView};
        use std::collections::BTreeMap;

        let view = |id: &str, name: Option<&str>, models: &[&str]| ProviderView {
            id: id.to_owned(),
            name: name.map(str::to_owned),
            kind: ProviderKind::OpenAiCompat,
            base_url: "http://a/v1".to_owned(),
            headers: BTreeMap::new(),
            has_key: false,
            last_refresh_at: None,
            models: models
                .iter()
                .map(|m| ModelView {
                    upstream_id: (*m).to_owned(),
                    name: (*m).to_owned(),
                    display_name: None,
                    upstream_name: None,
                    owned_by: None,
                    context_length: None,
                    max_output: None,
                    context_override: None,
                    params: serde_json::Value::Null,
                    upstream_params: serde_json::Value::Null,
                })
                .collect(),
        };

        let rows = flat_model_rows(&[
            view("z", Some("Z 家"), &["b-model", "a-model"]),
            view("no-name", None, &["m"]),
        ]);
        assert_eq!(rows.len(), 3);
        // 按文案排序（中文在前、英文在后，就按字节序）
        assert_eq!(rows[0].0, "Z 家 / a-model");
        assert_eq!(rows[1].0, "Z 家 / b-model");
        assert_eq!(rows[2].0, "no-name / m", "没配 name 就用 id");
        // 每行都得带上能回填的 id
        assert_eq!(rows[0].1, "z");
        assert_eq!(rows[0].2, "a-model");
        assert!(flat_model_rows(&[]).is_empty());
    }

}

/// 布局方式：块的位置**由我们自己算**（见 `rebuild_graph`）——
/// 一个会话一列、列内**从上到下**排块（消息顺序＝向下），会话之间左右并排。
/// 所以这里的布局是个**空操作**。
/// 点云（力导向）已撤掉：它把"顺序"这个主要信息揉没了。

type BlockGraph = egui_graphs::Graph<
    (),
    (),
    petgraph::Directed,
    petgraph::graph::DefaultIx,
    graph_node::BlockNodeShape,
    egui_graphs::DefaultEdgeShape,
>;

type BlockGraphView = egui_graphs::GraphView<LayoutStaticState, LayoutStatic>;

/// **不做任何事**的布局：位置由我们自己排（见 `rebuild_graph`）。
///
/// 为什么不用 crate 自带的：
/// 1. `LayoutRandom` 的文档写着"Does not override existing locations"，实现却是把每个节点
///    随机撒进 250×250 的方框（`layouts/random/layout.rs`）——那正是"全挤在一起"的元凶；
/// 2. 分层布局按自己的间距假设排布，而这些块比它的假设宽得多，一样会叠在一起。
#[derive(Debug, Default)]
struct LayoutStatic {
    state: LayoutStaticState,
}

/// 空布局不需要任何状态，但 `LayoutState` 要求可持久化（存 egui memory）。
#[derive(Debug, Default, Clone, serde::Serialize, serde::Deserialize)]
struct LayoutStaticState;

impl egui_graphs::LayoutState for LayoutStaticState {}

impl egui_graphs::Layout<LayoutStaticState> for LayoutStatic {
    fn from_state(state: LayoutStaticState) -> impl egui_graphs::Layout<LayoutStaticState> {
        Self { state }
    }

    fn next<N, E, Ty, Ix, Dn, De>(
        &mut self,
        _graph: &mut egui_graphs::Graph<N, E, Ty, Ix, Dn, De>,
        _ui: &egui::Ui,
    ) where
        N: Clone,
        E: Clone,
        Ty: petgraph::EdgeType,
        Ix: petgraph::stable_graph::IndexType,
        Dn: egui_graphs::DisplayNode<N, E, Ty, Ix>,
        De: egui_graphs::DisplayEdge<N, E, Ty, Ix, Dn>,
    {
        // 位置是我们摆的，谁也别动
    }

    fn state(&self) -> LayoutStaticState {
        self.state.clone()
    }
}


