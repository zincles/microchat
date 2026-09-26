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

/// 关系图 `GraphView` 的 id（元数据/布局状态都存在 egui memory 里）。
const GRAPH_ID: &str = "debug_graph";
/// 同一列里相邻块之间的垂直间隔；会话（列）之间的水平间隔（画布坐标）。
const STACK_GAP: f32 = 26.0;
const COLUMN_GAP: f32 = 48.0;
/// 滚轮换算成"点"的比例：一行 50 点、一页 400 点（不同后端给的单位不一样）。
const POINTS_PER_LINE: f32 = 50.0;
const POINTS_PER_PAGE: f32 = 400.0;

/// 关系图里的节点类型——颜色按它区分（与聊天界面里「你/助手」的配色一致）。
#[derive(Clone, Copy, PartialEq)]
enum GraphNodeKind {
    /// 会话：一棵树的根
    Conversation,
    /// 系统提示词（第一句）
    System,
    User,
    Assistant,
}

impl GraphNodeKind {
    fn color(self) -> Color32 {
        match self {
            Self::Conversation => Color32::from_rgb(0xe0, 0x9f, 0x2f),
            Self::System => Color32::from_rgb(0x8a, 0x8a, 0x8a),
            Self::User => Color32::from_rgb(0x2f, 0x6f, 0xe0),
            Self::Assistant => Color32::from_rgb(0x2f, 0x9e, 0x44),
        }
    }
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
    graph: Option<BlockGraph>,
    /// 关系图：自己排布时算出的画布包围盒（"适配视图"用）。
    graph_bounds: egui::Rect,
    /// 重建后请求适配一次（每帧 fit 会与手动缩放/拖动打架，所以只做一次）。
    graph_needs_fit: bool,
    /// 会话与消息都来自后端（连上后拉取，不再有本地占位）。
    conversations: Vec<ApiConversation>,
    messages: BTreeMap<Uuid, Vec<ApiMessage>>,
    /// 当前会话的变量视图（全局 + 本会话操作日志 + 生效值），由后端算好。
    variables: Option<microchat::vars::VariableView>,
    /// 上面那份变量属于哪个会话（切换时先清空，避免串台）。
    variables_for: Option<Uuid>,
    /// 正在编辑的消息：`(消息 id, 草稿)`；`None` = 都在只读态。
    editing: Option<(Uuid, String)>,
    /// 每个会话各自的输入草稿：切会话不丢字。
    drafts: BTreeMap<Uuid, String>,
    current: Option<Uuid>,
    /// 正在发送中的会话（发送期间禁用发送按钮，避免重复提交）。
    pending_send: Option<Uuid>,
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
            graph: None,
            graph_bounds: egui::Rect::NOTHING,
            graph_needs_fit: false,
            conversations: Vec::new(),
            messages: BTreeMap::new(),
            variables: None,
            variables_for: None,
            editing: None,
            drafts: BTreeMap::new(),
            current: None,
            pending_send: None,
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
            settings_tab: SettingsTab::Server,
            debug_tab: DebugTab::State,
            preedit_active: false,
            focus_pending: true,
            composer_id: None,
            deferred: None,
            menu_selection: None,
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
        self.client.send(Command::ListAgents {
            base: base.clone(),
            token: token.clone(),
        });
        self.client.send(Command::ListConversations { base, token });
    }

    fn fetch_conversations(&mut self) {
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        self.client.send(Command::ListConversations { base, token });
    }

    fn fetch_messages(&mut self, conversation: Uuid) {
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        self.client.send(Command::ListMessages {
            base,
            token,
            conversation,
        });
        // 变量跟着消息一起拉：消息里的状态块正是变量的来源，两者同时变化。
        self.fetch_variables(conversation);
    }

    /// 只拉变量（每次回复后单独调一次就够，不必重拉整段历史）。
    fn fetch_variables(&mut self, conversation: Uuid) {
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        self.client.send(Command::ListVariables {
            base,
            token,
            conversation,
        });
    }

    fn create_conversation(&mut self) {
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        self.client.send(Command::CreateConversation { base, token });
    }

    fn delete_conversation(&mut self, conversation: Uuid) {
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        self.client.send(Command::DeleteConversation {
            base,
            token,
            conversation,
        });
    }

    /// 渠道显示名：id + 类型；未选或已不在配置里时说明白。
    fn provider_label(&self, provider: &str) -> String {
        if provider.is_empty() {
            return "未选择（走兜底）".to_owned();
        }
        match self
            .providers
            .as_ref()
            .and_then(|list| list.iter().find(|p| p.id == provider))
            .map(|p| kind_label(p.kind))
        {
            Some(kind) => format!("{provider} · {kind}"),
            None => format!("{provider}（已不在配置里）"),
        }
    }

    /// 模型显示名：优先取注册表里的显示名（dummy 会显示成「Dummy（测试用空模型）」）。
    fn model_label(&self, provider: &str, model: &str) -> String {
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

    /// Agent 显示名：优先用 `agents.jsonc` 里的 name，找不到就原样显示 id。
    fn agent_label(&self, agent_id: &str) -> String {
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

    /// 换渠道：连带把模型落到新渠道里。同名模型保留，否则取它的第一个；渠道没模型就清空。
    fn switch_provider(&mut self, conversation: Uuid, provider: String) {
        let models: Vec<String> = self
            .providers
            .as_ref()
            .and_then(|list| list.iter().find(|p| p.id == provider))
            .map(|p| p.models.iter().map(|m| m.upstream_id.clone()).collect())
            .unwrap_or_default();
        let current_model = self
            .conversations
            .iter()
            .find(|c| c.id == conversation)
            .map(|c| c.model.clone())
            .unwrap_or_default();
        let model = model_after_provider_switch(&models, &current_model);

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

    /// 换模型：只改模型，渠道保持（模型只在渠道内唯一）。
    fn switch_model(&mut self, conversation: Uuid, model: String) {
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        self.client.send(Command::UpdateConversation {
            base,
            token,
            conversation,
            provider: None,
            model: Some(model),
            agent_id: None,
        });
    }

    /// 换 Agent：提示词在请求组装时按 `agent_id` 解析——改 `agents.jsonc` 会影响所有用它的会话。
    fn switch_agent(&mut self, conversation: Uuid, agent_id: String) {
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

    /// 切到某个会话：记住选中并拉它的消息（每次拉，保证看到最新）。
    fn open_conversation(&mut self, conversation: Uuid) {
        self.current = Some(conversation);
        self.focus_pending = true;
        self.view = View::Chat;
        self.fetch_messages(conversation);
    }

    /// 发送当前草稿。发送期间禁用按钮；失败**保留草稿**，不吞用户打的字。
    fn send_message(&mut self) {
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
                    self.conversations = list;
                    // 选中项失效（首次连接 / 刚被删）→ 落到第一个
                    let still_there = self
                        .current
                        .is_some_and(|id| self.conversations.iter().any(|c| c.id == id));
                    if !still_there {
                        self.current = self.conversations.first().map(|c| c.id);
                        if let Some(id) = self.current {
                            self.fetch_messages(id);
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
                Event::Variables {
                    conversation,
                    result,
                } => match result {
                    Ok(view) => {
                        self.variables = Some(view);
                        self.variables_for = Some(conversation);
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
                        self.fetch_variables(conversation);
                    }
                    Err(message) => self.note = message,
                },
                Event::ConversationUpdated(Ok(updated)) => {
                    if let Some(slot) = self.conversations.iter_mut().find(|c| c.id == updated.id) {
                        *slot = updated;
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
                        Ok(turn) => {
                            let entry = self.messages.entry(conversation).or_default();
                            entry.push(turn.user);
                            entry.push(turn.assistant);
                            self.drafts.insert(conversation, String::new());
                            self.note = format!("{} 回复", turn.backend);
                            // 回复里可能带状态块 → 变量变了，面板要跟上
                            self.fetch_variables(conversation);
                            // 首条消息会生成标题，刷新列表才看得到
                            self.fetch_conversations();
                        }
                        Err(message) => self.note = message,
                    }
                }
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
                            let edit_response = ui.add(
                                TextEdit::singleline(&mut self.settings.server_address)
                                    .desired_width(280.0),
                            );
                            if let Some(deferred) = attach_edit_menu(
                                ui,
                                &edit_response,
                                &self.settings.server_address,
                                &mut self.menu_selection,
                                false,
                            ) {
                                self.deferred = Some(deferred);
                            }
                            if edit_response.changed() {
                                self.settings_dirty = true;
                            }
                            ui.end_row();

                            ui.label("用户名");
                            let edit_response = ui.add(
                                TextEdit::singleline(&mut self.settings.username)
                                    .desired_width(280.0),
                            );
                            if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.settings.username, &mut self.menu_selection, false) {
                                self.deferred = Some(deferred);
                            };
                            ui.end_row();

                            ui.label("密码");
                            let edit_response = ui.add(
                                TextEdit::singleline(&mut self.password)
                                    .password(true)
                                    .desired_width(280.0),
                            );
                            if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.password, &mut self.menu_selection, false) {
                                self.deferred = Some(deferred);
                            };
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
                    let edit_response = ui.add(TextEdit::singleline(&mut self.agent_name).desired_width(280.0));
                    if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.agent_name, &mut self.menu_selection, false) {
                        self.deferred = Some(deferred);
                    };
                    ui.end_row();

                    ui.label("系统提示词");
                    let edit_response = ui.add(
                        TextEdit::multiline(&mut self.agent_prompt)
                            .desired_rows(6)
                            .desired_width(420.0),
                    );
                    if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.agent_prompt, &mut self.menu_selection, false) {
                        self.deferred = Some(deferred);
                    };
                    ui.end_row();
                });
            ui.label(RichText::new("改完点右下角「保存」提交").weak());
        }

        ui.add_space(8.0);
        ui.separator();
        ui.label(RichText::new("新建 agent").strong());
        ui.horizontal(|ui| {
            ui.label("id");
            let edit_response = ui.add(TextEdit::singleline(&mut self.new_agent_id).desired_width(120.0));
            if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.new_agent_id, &mut self.menu_selection, false) {
                self.deferred = Some(deferred);
            };
            ui.label("名称");
            let edit_response = ui.add(TextEdit::singleline(&mut self.new_agent_name).desired_width(160.0));
            if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.new_agent_name, &mut self.menu_selection, false) {
                self.deferred = Some(deferred);
            };
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
            let edit_response = ui.add(TextEdit::singleline(&mut self.new_provider_id).desired_width(120.0));
            if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.new_provider_id, &mut self.menu_selection, false) {
                self.deferred = Some(deferred);
            };
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
                    let edit_response = ui.add(
                        TextEdit::singleline(&mut self.new_provider_base_url)
                            .desired_width(300.0)
                            .hint_text("https://…/v1"),
                    );
                    if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.new_provider_base_url, &mut self.menu_selection, false) {
                        self.deferred = Some(deferred);
                    };
                });
                ui.horizontal(|ui| {
                    ui.label("api_key");
                    let edit_response = ui.add(
                        TextEdit::singleline(&mut self.new_provider_api_key)
                            .password(true)
                            .desired_width(300.0)
                            .hint_text("写入后端 config/secrets.json，不回显"),
                    );
                    if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.new_provider_api_key, &mut self.menu_selection, false) {
                        self.deferred = Some(deferred);
                    };
                });
                ui.horizontal(|ui| {
                    ui.label("headers");
                    let edit_response = ui.add(
                        TextEdit::multiline(&mut self.new_provider_headers)
                            .desired_rows(2)
                            .desired_width(300.0)
                            .hint_text("每行一个：Header: 值（可空）"),
                    );
                    if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.new_provider_headers, &mut self.menu_selection, false) {
                        self.deferred = Some(deferred);
                    };
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
                    let edit_response = ui.add(TextEdit::singleline(&mut self.provider_base_url).desired_width(320.0));
                    if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.provider_base_url, &mut self.menu_selection, false) {
                        self.deferred = Some(deferred);
                    };
                    ui.end_row();

                    ui.label("headers");
                    let edit_response = ui.add(
                        TextEdit::multiline(&mut self.provider_headers)
                            .desired_rows(3)
                            .desired_width(320.0)
                            .hint_text("每行一个：Header: 值"),
                    );
                    if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.provider_headers, &mut self.menu_selection, false) {
                        self.deferred = Some(deferred);
                    };
                    ui.end_row();

                    ui.label("密钥");
                    ui.horizontal(|ui| {
                        let edit_response = ui.add(
                            TextEdit::singleline(&mut self.provider_api_key)
                                .password(true)
                                .desired_width(240.0)
                                .hint_text("留空 = 不改"),
                        );
                        if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.provider_api_key, &mut self.menu_selection, false) {
                            self.deferred = Some(deferred);
                        };
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
                let edit_response = ui.add(
                    TextEdit::singleline(&mut self.frontend_draft.server_address)
                        .desired_width(280.0),
                );
                if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.frontend_draft.server_address, &mut self.menu_selection, false) {
                    self.deferred = Some(deferred);
                };
                ui.end_row();

                ui.label("用户名");
                let edit_response = ui.add(
                    TextEdit::singleline(&mut self.frontend_draft.username).desired_width(280.0),
                );
                if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.frontend_draft.username, &mut self.menu_selection, false) {
                    self.deferred = Some(deferred);
                };
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
        for conversation in self.conversations.iter().take(GRAPH_CONVERSATION_LIMIT) {
            self.client.send(Command::ListMessages {
                base: base.clone(),
                token: token.clone(),
                conversation: conversation.id,
            });
        }
        self.client.send(Command::ListConversations { base, token });
    }

    /// 把后端数据投影成关系图：每个会话是一棵有向树——
    /// 会话（根）→ 系统提示词 → 用户/助手交替的消息链。
    /// `messages.parent_id` 还没落地（Phase 3），所以链还是线性的；那次迁移之后
    /// 只需把"上一条→这条"换成"父→子"，分叉（重新生成/分支）会自然长出来。
    fn rebuild_graph(&mut self) {
        let mut graph = BlockGraph::new();
        let mut column_left = 0.0_f32;
        let mut max_height = 1.0_f32;

        for conversation in &self.conversations {
            // 一列的块：会话（根）→ 系统提示词 → 用户/助手交替，**自上而下**
            let mut nodes: Vec<(GraphNodeKind, String)> = Vec::new();
            let title = if conversation.title.is_empty() {
                format!("会话 {}", &conversation.id.to_string()[..8])
            } else {
                conversation.title.clone()
            };
            nodes.push((GraphNodeKind::Conversation, title));

            if let Some(prompt) = self.effective_system_prompt(conversation) {
                let excerpt: String = prompt.chars().take(16).collect();
                nodes.push((GraphNodeKind::System, format!("system · {excerpt}")));
            }

            for (index, message) in self
                .messages
                .get(&conversation.id)
                .into_iter()
                .flatten()
                .enumerate()
            {
                let (kind, who) = match message.role {
                    ApiRole::User => (GraphNodeKind::User, "你"),
                    ApiRole::Assistant => (GraphNodeKind::Assistant, "助手"),
                };
                let excerpt: String = message.content.chars().take(12).collect();
                nodes.push((kind, format!("{} {}·{}", index + 1, who, excerpt)));
            }

            // 列内：块自上而下摞起来；列本身宽度取该列最宽的块，所有块共用一条中心轴
            let sizes: Vec<egui::Vec2> = nodes
                .iter()
                .map(|(_, label)| graph_node::block_size(label))
                .collect();
            let width = column_width(&sizes);
            let center_x = column_left + width / 2.0;
            let heights: Vec<f32> = sizes.iter().map(|size| size.y).collect();
            let centers_y = stacked_centers(&heights, STACK_GAP);

            let mut previous = None;
            for ((kind, label), y) in nodes.into_iter().zip(centers_y.iter().copied()) {
                let node = graph.add_node_custom((), |node| {
                    node.set_label(label);
                    node.set_color(kind.color());
                    node.set_location(egui::pos2(center_x, y));
                });
                if let Some(previous) = previous {
                    graph.add_edge(previous, node, ());
                }
                previous = Some(node);
            }

            let last_half = sizes.last().map_or(0.0, |size| size.y / 2.0);
            let column_height = centers_y.last().copied().unwrap_or(0.0) + last_half;
            max_height = max_height.max(column_height);
            column_left += width + COLUMN_GAP;
        }

        self.graph_bounds = egui::Rect::from_min_max(
            egui::pos2(0.0, 0.0),
            egui::pos2((column_left - COLUMN_GAP).max(1.0), max_height),
        );
        self.graph_needs_fit = true;
        self.graph = Some(graph);
    }

    /// 真正会发给模型的系统提示词：会话级覆盖优先，否则用 agent 的（内置默认也算）。
    fn effective_system_prompt(&self, conversation: &ApiConversation) -> Option<String> {
        let agent_prompt = self
            .agents
            .as_ref()
            .and_then(|config| config.resolve(&conversation.agent_id))
            .map(|agent| agent.system_prompt);
        resolve_system_prompt(&conversation.system_prompt, agent_prompt.as_deref())
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
        let mut want_fit = false;
        ui.horizontal(|ui| {
            if ui
                .add_enabled(self.graph.is_some(), egui::Button::new("重新布局"))
                .on_hover_text("按会话重新排列块的位置")
                .clicked()
            {
                self.rebuild_graph();
                want_fit = true;
            }
            if ui
                .add_enabled(self.graph.is_some(), egui::Button::new("适配视图"))
                .clicked()
            {
                want_fit = true;
            }
            ui.separator();
            ui.label(RichText::new("滚轮缩放 · 拖空白平移 · 拖块移动").weak());
        });
        ui.horizontal(|ui| {
            for (kind, label) in [
                (GraphNodeKind::Conversation, "会话"),
                (GraphNodeKind::System, "系统"),
                (GraphNodeKind::User, "用户"),
                (GraphNodeKind::Assistant, "助手"),
            ] {
                let (rect, _) = ui.allocate_exact_size(egui::vec2(10.0, 10.0), egui::Sense::hover());
                ui.painter().add(egui::Shape::rect_filled(
                    rect,
                    egui::CornerRadius::same(2),
                    kind.color(),
                ));
                ui.label(RichText::new(label).weak());
            }
            ui.label(RichText::new("（每个会话一列，自上而下＝消息顺序；分支＝重新生成/分支，待消息父指针落地）").weak());
        });

        let Some(graph) = &mut self.graph else {
            ui.label(RichText::new("点右上角「重新拉取」载入").weak());
            return;
        };

        let interactions = egui_graphs::SettingsInteraction::new()
            .with_dragging_enabled(true)
            .with_node_selection_enabled(true);
        let navigation = egui_graphs::SettingsNavigation::new()
            .with_zoom_and_pan_enabled(true)
            // 每帧 fit 会跟滚轮缩放、拖动打架，所以关掉——重建后自己适配一次
            .with_fit_to_screen_enabled(false);

        let mut viewport = None;
        ui.allocate_ui(ui.available_size(), |ui| {
            let response = BlockGraphView::new()
                .with_interactions(&interactions)
                .with_navigations(&navigation)
                .with_id(Some(GRAPH_ID.to_owned()))
                .show(ui, graph)
                .response;
            wheel_zoom(ui, &response, GRAPH_ID);
            viewport = Some(response.rect);
        });

        if let Some(viewport) = viewport {
            if want_fit || self.graph_needs_fit {
                fit_view(ui, viewport, GRAPH_ID, self.graph_bounds);
                self.graph_needs_fit = false;
            }
        }
    }

    fn sidebar(&mut self, ui: &mut egui::Ui) {
        let mut switch_to: Option<Uuid> = None;
        let mut delete: Option<Uuid> = None;
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
        if self.conversations.is_empty() {
            ui.label(RichText::new("还没有会话：新建一个").weak());
        }
        egui::ScrollArea::vertical()
            .id_salt("conversation_list")
            .auto_shrink([false, false])
            .show(ui, |ui| {
                for conversation in &self.conversations {
                    let title = if conversation.title.is_empty() {
                        NEW_TITLE
                    } else {
                        conversation.title.as_str()
                    };
                    ui.horizontal(|ui| {
                        // 会话标题必须**可截断**：不截断的话这一行的最小宽度等于标题全宽，
                        // 而 egui 不允许面板窄于内容最小宽度——分隔线就会"只能往右拖、往左拖不动"。
                        let room = 26.0; // 给右边的关闭按钮留位
                        let size = egui::vec2(
                            (ui.available_width() - room).max(40.0),
                            ui.spacing().interact_size.y,
                        );
                        // 先占位、铺底色，**再**把文字放进去——反过来的话底色会盖住标题。
                        let (rect, row) = ui.allocate_exact_size(size, egui::Sense::click());
                        if self.current == Some(conversation.id) {
                            ui.painter().rect_filled(
                                rect,
                                egui::CornerRadius::same(4),
                                ui.visuals().selection.bg_fill,
                            );
                        }
                        // `selectable(false)` 不能省：默认的 Label 会 sense 点击/拖动（为了选文本），
                        // 压在占位矩形上就把点击吃了——整行就点不动了。
                        ui.put(
                            rect.shrink2(egui::vec2(4.0, 0.0)),
                            egui::Label::new(title).truncate().selectable(false),
                        );
                        if row.clicked() {
                            switch_to = Some(conversation.id);
                        }
                        row.on_hover_text(title);
                        ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                            if close_button(ui).clicked() {
                                delete = Some(conversation.id);
                            }
                        });
                    });
                }
            });

        if new_conv {
            self.create_conversation();
        } else if let Some(id) = delete {
            self.delete_conversation(id);
        } else if let Some(id) = switch_to {
            self.open_conversation(id);
        }
    }

    /// 右侧变量面板：生效值 + 本会话改动 + 全局变量。
    ///
    /// 生效值由后端 fold 出来（前端不重算一遍——重算就有两个真相来源）。
    /// 带 ◆ 的是被本会话覆写过的键：一眼看出"这一局改了什么"。
    fn vars_panel(&mut self, ui: &mut egui::Ui, conversation: Uuid) {
        let view = self
            .variables
            .as_ref()
            .filter(|_| self.variables_for == Some(conversation));
        let changed: Vec<&str> = view
            .map(|view| view.session.iter().map(|row| row.key.as_str()).collect())
            .unwrap_or_default();

        ui.add_space(4.0);
        ui.horizontal(|ui| {
            ui.label(RichText::new("变量").strong());
            if let Some(view) = view {
                ui.label(RichText::new(format!("本会话 {} 处改动", view.session.len())).weak());
            }
            ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                if ui
                    .small_button("复制协议")
                    .on_hover_text("把状态块语法复制到剪贴板，粘进 agent 的系统提示词")
                    .clicked()
                {
                    ui.output_mut(|out| {
                        out.commands.push(egui::OutputCommand::CopyText(
                            microchat::vars::PROTOCOL_HINT.to_owned(),
                        ))
                    });
                }
            });
        });
        ui.separator();

        let Some(view) = view else {
            ui.label(RichText::new("变量还没拉到").weak());
            return;
        };

        egui::ScrollArea::vertical()
            .id_salt("vars_scroll")
            .auto_shrink([false, false])
            .show(ui, |ui| {
                ui.label(RichText::new("生效值（发给模型的就是这张表）").weak());
                if view.effective.is_empty() {
                    ui.label(RichText::new("（空）").weak());
                }
                for (key, value) in &view.effective {
                    let mark = if changed.contains(&key.as_str()) {
                        "◆ "
                    } else {
                        ""
                    };
                    ui.horizontal_wrapped(|ui| {
                        ui.label(RichText::new(format!("{mark}{key}")).monospace());
                        ui.label(RichText::new("=").weak());
                        ui.label(RichText::new(value).monospace());
                    });
                }

                ui.add_space(10.0);
                ui.label(RichText::new(format!("本会话改动（{}）", view.session.len())).weak());
                if view.session.is_empty() {
                    ui.label(RichText::new("（还没有）").weak());
                }
                for row in &view.session {
                    let text = match (&row.kind, &row.value) {
                        (microchat::vars::OpKind::Set, Some(value)) => {
                            format!("{} = {value}", row.key)
                        }
                        (microchat::vars::OpKind::Set, None) => format!("{} = （空串）", row.key),
                        (microchat::vars::OpKind::Delete, _) => format!("del {}", row.key),
                    };
                    ui.label(RichText::new(format!("#{} {}", row.seq, text)).monospace());
                }

                ui.add_space(10.0);
                ui.label(RichText::new(format!("全局变量（{}）", view.global_values.len())).weak());
                for (key, value) in &view.global_values {
                    ui.label(RichText::new(format!("{key} = {value}")).monospace());
                }

                ui.add_space(10.0);
                ui.label(RichText::new("在消息末尾写：").weak());
                ui.label(
                    RichText::new("<state>\nset HP = 12\ndel 火把\n</state>")
                        .monospace()
                        .weak(),
                );
            });
    }

    fn chat_view(&mut self, ui: &mut egui::Ui, composing: bool, enter: bool) {
        let Some(current) = self.current else {
            ui.centered_and_justified(|ui| {
                ui.label(RichText::new("还没有会话：点左上角「＋ 新建对话」").weak());
            });
            return;
        };
        let sending = self.pending_send == Some(current);

        // 会话上方一条：当前模型与 Agent，都能直接在下拉里换（换完 PATCH 回后端）。
        egui::Panel::top("chat_header").show(ui, |ui| {
            ui.add_space(4.0);
            ui.horizontal(|ui| {
                let conversation = self.conversations.iter().find(|c| c.id == current).cloned();
                let (provider, model, agent_id) = match &conversation {
                    Some(c) => (c.provider.clone(), c.model.clone(), c.agent_id.clone()),
                    None => (String::new(), String::new(), String::new()),
                };

                ui.label(RichText::new("渠道").weak());
                let mut picked_provider: Option<String> = None;
                egui::ComboBox::from_id_salt("chat_provider")
                    .selected_text(RichText::new(self.provider_label(&provider)).monospace())
                    .width(150.0)
                    .show_ui(ui, |ui| {
                        for provider_view in self.providers.iter().flatten() {
                            let label = format!(
                                "{} · {}",
                                provider_view.id,
                                kind_label(provider_view.kind)
                            );
                            if ui
                                .selectable_label(provider_view.id == provider, label)
                                .clicked()
                            {
                                picked_provider = Some(provider_view.id.clone());
                            }
                        }
                        if self
                            .providers
                            .as_ref()
                            .is_none_or(|list| list.is_empty())
                        {
                            ui.label(
                                RichText::new("还没有渠道：去「设置 → 服务器设置」新建").weak(),
                            );
                        }
                    });
                if let Some(provider) = picked_provider {
                    self.switch_provider(current, provider);
                }

                ui.separator();

                ui.label(RichText::new("模型").weak());
                let mut picked_model: Option<String> = None;
                egui::ComboBox::from_id_salt("chat_model")
                    .selected_text(RichText::new(self.model_label(&provider, &model)).monospace())
                    .width(220.0)
                    .show_ui(ui, |ui| {
                        // 模型只在渠道内唯一：下拉按当前渠道过滤
                        let models = self
                            .providers
                            .as_ref()
                            .and_then(|list| list.iter().find(|p| p.id == provider))
                            .map(|p| p.models.as_slice())
                            .unwrap_or(&[]);
                        if provider.is_empty() {
                            ui.label(RichText::new("先选渠道").weak());
                        } else if models.is_empty() {
                            ui.label(
                                RichText::new("该渠道还没有模型：去设置里点「获取模型」").weak(),
                            );
                        }
                        for model_view in models {
                            if ui
                                .selectable_label(model_view.upstream_id == model, &model_view.name)
                                .clicked()
                            {
                                picked_model = Some(model_view.upstream_id.clone());
                            }
                        }
                    });
                if let Some(model) = picked_model {
                    self.switch_model(current, model);
                }

                ui.separator();

                ui.label(RichText::new("Agent").weak());
                let mut picked_agent: Option<String> = None;
                egui::ComboBox::from_id_salt("chat_agent")
                    .selected_text(RichText::new(self.agent_label(&agent_id)).monospace())
                    .width(180.0)
                    .show_ui(ui, |ui| {
                        for agent in self.agents.iter().flat_map(|config| config.agents.iter()) {
                            let label = if agent.name.is_empty() {
                                agent.id.clone()
                            } else {
                                agent.name.clone()
                            };
                            if ui.selectable_label(agent.id == agent_id, label).clicked() {
                                picked_agent = Some(agent.id.clone());
                            }
                        }
                    });
                if let Some(agent_id) = picked_agent {
                    self.switch_agent(current, agent_id);
                }
            });
            ui.add_space(4.0);
        });

        egui::Panel::bottom("composer").show(ui, |ui| {
            ui.add_space(8.0);
            ui.horizontal(|ui| {
                let hint = if self.settings.enter_sends {
                    "输入消息…  Enter 发送 / Shift+Enter 换行"
                } else {
                    "输入消息…  Shift+Enter 换行"
                };
                let hint_text = hint;
                let width = (ui.available_width() - 88.0).max(80.0);
                let draft = self.drafts.entry(current).or_default();
                let edit = ui.add_sized(
                    [width, 88.0],
                    TextEdit::multiline(draft)
                        .desired_rows(3)
                        .hint_text(hint_text),
                );
                self.composer_id = Some(edit.id);
                if self.focus_pending {
                    edit.request_focus();
                    self.focus_pending = false;
                }
                if edit.has_focus() && self.preedit_active {
                    ui.ctx().request_repaint();
                }
                // 右键菜单：选区快照 + 剪切/复制/粘贴/全选（见 attach_edit_menu）
                if let Some(draft) = self.drafts.get(&current)
                    && let Some(deferred) =
                        attach_edit_menu(ui, &edit, draft, &mut self.menu_selection, false)
                {
                    self.deferred = Some(deferred);
                }

                let has_text = self
                    .drafts
                    .get(&current)
                    .is_some_and(|draft| !draft.trim().is_empty());
                let send = ui.add_enabled(
                    has_text && !sending,
                    egui::Button::new(if sending { "等待…" } else { "发送" }),
                );
                if !sending
                    && (send.clicked()
                        || (enter && self.settings.enter_sends && !composing && has_text))
                {
                    self.send_message();
                } else if enter && self.settings.enter_sends && !composing && !sending {
                    // multiline 的 Enter 会顺带插入换行，空输入时把它吃掉。
                    if let Some(draft) = self.drafts.get_mut(&current) {
                        draft.clear();
                    }
                }
            });
            ui.add_space(8.0);
        });

        egui::Panel::right("chat_vars")
            .resizable(true)
            .default_size(260.0)
            .show(ui, |ui| self.vars_panel(ui, current));

        let stick = self.settings.stick_to_bottom;
        let messages = self
            .messages
            .get(&current)
            .map(Vec::as_slice)
            .unwrap_or(&[]);
        if messages.is_empty() {
            ui.centered_and_justified(|ui| {
                ui.label(RichText::new("输入消息开始对话").weak());
            });
            return;
        }
        // 快照与延迟动作在闭包里只能用局部变量：进来取出来，出去再放回去
        let mut local_selection = self.menu_selection.take();
        let mut deferred: Option<DeferredMenu> = None;
        // 编辑态先从 self 里取出来：下面闭包要同时可变借用草稿，而 `messages` 正借着 self.messages
        let mut editing = self.editing.take();
        let mut start_edit: Option<(Uuid, String)> = None;
        let mut save_edit: Option<(Uuid, String)> = None;
        let mut cancel_edit = false;
        let mut edit_box_id: Option<egui::Id> = None;
        egui::ScrollArea::vertical()
            .id_salt("chat_messages")
            .auto_shrink([false, false])
            .stick_to_bottom(stick)
            .show(ui, |ui| {
                // 左右留白：消息块不贴边——否则右侧滚动条会压在文字上，看着也憋。
                egui::Frame::NONE
                    .inner_margin(egui::Margin::symmetric(16, 6))
                    .show(ui, |ui| {
                        for message in messages {
                            let (label, color) = match message.role {
                                ApiRole::User => ("你", Color32::from_rgb(0x2f, 0x6f, 0xe0)),
                                ApiRole::Assistant => ("助手", Color32::from_rgb(0x2f, 0x9e, 0x44)),
                            };
                            let is_editing = editing
                                .as_ref()
                                .is_some_and(|(id, _)| *id == message.id);

                            // 每条消息是一个块：底色 + 圆角 + 内边距，块与块之间留间距。
                            egui::Frame::NONE
                                .fill(ui.visuals().faint_bg_color)
                                .corner_radius(egui::CornerRadius::same(6))
                                .inner_margin(egui::Margin::same(8))
                                .show(ui, |ui| {
                                    ui.horizontal(|ui| {
                                        ui.colored_label(color, RichText::new(label).strong());
                                        ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                                            if is_editing {
                                                if ui.small_button("保存").clicked() {
                                                    save_edit = editing.clone();
                                                }
                                                if ui.small_button("取消").clicked() {
                                                    cancel_edit = true;
                                                }
                                            } else {
                                                if ui.small_button("编辑").clicked() {
                                                    start_edit =
                                                        Some((message.id, message.content.clone()));
                                                }
                                                let text = message.content.clone();
                                                if ui.small_button("复制").clicked() {
                                                    ui.output_mut(|o| {
                                                        o.commands.push(egui::OutputCommand::CopyText(text))
                                                    });
                                                }
                                            }
                                        });
                                    });

                                    match editing.as_mut() {
                                        Some((id, draft)) if *id == message.id => {
                                            let box_response = ui.add_sized(
                                                [ui.available_width(), 140.0],
                                                TextEdit::multiline(draft).desired_rows(5),
                                            );
                                            edit_box_id = Some(box_response.id);
                                            if let Some(picked) = attach_edit_menu(
                                                ui,
                                                &box_response,
                                                draft,
                                                &mut local_selection,
                                                false,
                                            ) {
                                                deferred = Some(picked);
                                            }
                                        }
                                        _ => {
                                            // 只读文本用"绑到不可变 str 的 TextEdit"（egui 官方讨论推荐）：
                                            // 看起来就是一段文字，但能选中、能 Ctrl+C，也能走同一套右键菜单。
                                            let mut text = message.content.as_str();
                                            let response = ui.add(
                                                TextEdit::multiline(&mut text)
                                                    .frame(egui::Frame::NONE)
                                                    .desired_width(f32::INFINITY),
                                            );
                                            if let Some(picked) = attach_edit_menu(
                                                ui,
                                                &response,
                                                &message.content,
                                                &mut local_selection,
                                                true,
                                            ) {
                                                deferred = Some(picked);
                                            }
                                        }
                                    }
                                });
                            ui.add_space(12.0);
                        }
                    });
            });

        if let Some(pair) = start_edit {
            editing = Some(pair);
        }
        if cancel_edit {
            editing = None;
        }
        self.menu_selection = local_selection;
        if let Some(deferred) = deferred {
            self.deferred = Some(deferred);
        }
        if let Some((message, content)) = save_edit {
            editing = None;
            let (base, token) = (self.settings.server_address.clone(), self.token.clone());
            self.client.send(Command::EditMessage {
                base,
                token,
                conversation: current,
                message,
                content,
            });
        }
        self.editing = editing;
    }
}

impl eframe::App for App {
    fn ui(&mut self, ui: &mut egui::Ui, _frame: &mut eframe::Frame) {
        self.pump_events();

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

/// 普通滚轮也缩放。
///
/// egui_graphs 只认 egui 的 `zoom_delta()`——那等于 **Ctrl+滚轮 / 触控板捏合**，
/// 普通滚轮被它直接忽略（用户会以为"不能缩放"）。这里按它自己的坐标公式补一刀：
/// `screen = canvas * zoom + pan`，所以要保持指针下的画布点不动：
/// `pan' = pointer - (pointer - pan) * (zoom'/zoom)`。
///
/// 取的是**原始**滚轮事件，不是 `smooth_scroll_delta`：后者是 egui 逐帧平滑过的值，
/// 一次滚轮会连续改好几帧状态（观感像缩放动画，也白烧性能）。原始事件一帧就是一次。
fn wheel_zoom(ui: &mut egui::Ui, response: &egui::Response, id: &str) {
    let Some(pointer) = response.hover_pos() else {
        return;
    };
    let points = ui.input(|i| {
        i.raw
            .events
            .iter()
            .filter_map(|event| match event {
                egui::Event::MouseWheel { unit, delta, .. } => Some(match unit {
                    egui::MouseWheelUnit::Line => delta.y * POINTS_PER_LINE,
                    egui::MouseWheelUnit::Point => delta.y,
                    egui::MouseWheelUnit::Page => delta.y * POINTS_PER_PAGE,
                }),
                _ => None,
            })
            .sum::<f32>()
    });
    if points == 0.0 {
        return;
    }

    let mut meta = egui_graphs::MetadataFrame::new(Some(id.to_owned())).load(ui);
    let old_zoom = meta.zoom;
    let new_zoom = (old_zoom * (1.0 + points * 0.002)).clamp(0.05, 4.0);
    if new_zoom == old_zoom {
        return;
    }

    let pointer = pointer.to_vec2();
    let canvas_point = (pointer - meta.pan) / old_zoom;
    meta.pan = pointer - canvas_point * new_zoom;
    meta.zoom = new_zoom;
    meta.save(ui);
}

/// 把整张图放进视口。
///
/// 位置是我们自己排的，所以包围盒也知道——不必依赖 crate 的每帧 fit
/// （那个会在用户拖块/滚轮缩放时不断抢回去）。
fn fit_view(ui: &mut egui::Ui, viewport: egui::Rect, id: &str, bounds: egui::Rect) {
    if !bounds.width().is_finite() || !bounds.height().is_finite() {
        return;
    }
    if bounds.width() <= 0.0 || bounds.height() <= 0.0 || !viewport.width().is_finite() {
        return;
    }

    let padding = 32.0;
    let zoom = ((viewport.width() - padding) / bounds.width())
        .min((viewport.height() - padding) / bounds.height())
        .clamp(0.05, 4.0);

    let mut meta = egui_graphs::MetadataFrame::new(Some(id.to_owned())).load(ui);
    meta.zoom = zoom;
    meta.pan = viewport.center().to_vec2() - bounds.center().to_vec2() * zoom;
    meta.save(ui);
}

/// 把一串「尺寸 + 间隔」折成一维的中心坐标。
///
/// 一列里的块自上而下摞起来（块高固定，于是是等差数列）、各列左右并排（列宽取该列最宽的块），
/// 都用它。用块自己的尺寸推进，所以宽高不一的块**绝不重叠**——之前"全挤在一起"
/// 正是因为布局引擎按固定间距摆，而这里的块宽度从 90 到 420 不等。
fn stacked_centers(sizes: &[f32], gap: f32) -> Vec<f32> {
    let mut cursor = 0.0;
    sizes
        .iter()
        .map(|size| {
            let center = cursor + size / 2.0;
            cursor += size + gap;
            center
        })
        .collect()
}

/// 一列的宽度：该列**最宽**的块（列内所有块共用一条中心轴；列宽不够，相邻列就会叠）。
fn column_width(sizes: &[egui::Vec2]) -> f32 {
    sizes.iter().map(|size| size.x).fold(0.0, f32::max)
}

/// 真正会发给模型的系统提示词：会话级覆盖优先，否则用 agent 的。
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

/// 换渠道后模型怎么落：
/// - 新渠道里也有同名模型 → 保持不变（省得白切回一次）
/// - 否则取新渠道的第一个模型
/// - 新渠道一个模型都没有 → 清空（此时会话走兜底话术）
fn model_after_provider_switch(models: &[String], current: &str) -> String {
    if models.iter().any(|id| id == current) {
        return current.to_owned();
    }
    models.first().cloned().unwrap_or_default()
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

    use super::{
        column_width, slice_chars, stacked_centers, update_selection_snapshot, SelectionSnapshot,
    };
    use microchat::config::{Agent, ProviderKind};
    use microchat::registry::ProviderView;

    use super::{
        agent_draft_differs, header_lines, model_after_provider_switch, parse_header_lines,
        provider_draft_differs, resolve_system_prompt,
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

        let centers = stacked_centers(&widths, super::COLUMN_GAP);
        assert!(centers[1] > centers[0], "列必须从左往右递增: {centers:?}");
        let apart = centers[1] - centers[0];
        let needed = (widths[0] + widths[1]) / 2.0 + super::COLUMN_GAP;
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
                    let range = crate::TextEditState::load(ui.ctx(), response.id)
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
                    let range = crate::TextEditState::load(ui.ctx(), response.id)
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
    fn system_prompt_prefers_conversation_override() {
        assert_eq!(resolve_system_prompt("会话覆盖", Some("agent 的")), Some("会话覆盖".to_owned()));
        assert_eq!(resolve_system_prompt("  ", Some("agent 的")), Some("agent 的".to_owned()));
        assert_eq!(resolve_system_prompt("", None), None, "两边都空就没有系统提示词");
        assert_eq!(resolve_system_prompt("", Some("   ")), None);
    }

    #[test]
    fn provider_switch_picks_a_sane_model() {
        let models = ["a".to_owned(), "b".to_owned()];
        assert_eq!(model_after_provider_switch(&models, "b"), "b", "同名模型保留");
        assert_eq!(model_after_provider_switch(&models, "x"), "a", "否则落到第一个");
        assert_eq!(model_after_provider_switch(&[], "x"), "", "渠道没模型就清空");
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
