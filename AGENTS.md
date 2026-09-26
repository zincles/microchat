# AGENTS.md — 工作须知

microchat：轻量 SillyTavern 替代（RPG 向）。三层，边界要清楚：

| 层 | 位置 | 状态 |
|---|---|---|
| 后端（唯一权威） | `src/`（Rust + axum + **SQLite**） | 活跃开发 |
| 新前端（Godot 4.8） | `frontend/` | **由用户设计**——动手前先问，别替他做设计决定 |
| 旧前端（egui） | `src/main.rs`、`src/client.rs` | 已冻结：保持可用，不加功能 |

## 常用命令

```bash
cargo test                                     # 全量测试（当前 93 项）
./target/debug/server                          # 后端，默认 127.0.0.1:8787
./target/debug/microchat                       # 旧 egui 前端

cd frontend
godot --headless --path . --quit-after 3       # 加载并跑几帧（用它当"语法+运行"检查）
godot --headless --script /tmp/x.gd            # 灌事件跑帧做无头验证（egui/Godot 都适用）
ANDROID_HOME=~/Android/Sdk godot --headless --path . --export-debug "Android" /tmp/x.apk
adb install -r /tmp/x.apk && adb logcat -s godot
```

## 不变量（破坏了会静默出错）

1. **存储是 SQLite**：`data/microchat.db`。用户手写的 JSONC 只在 `config/`。两者路径都**相对工作目录**，可用 `MICROCHAT_CONFIG_DIR` / `MICROCHAT_DATA_DIR` 覆盖。前端的界面偏好另有一份：`~/.config/microchat/frontend.jsonc`（不同机制，别混）。
2. **正文与提示词都是存档**：`<state>` 块原样留在消息正文**和 system prompt** 里；**发给模型的文本一律剔除标签**，改注入当前变量表。唯一出口 `vars::build_outgoing()`——不要写第二条拼装路径。
3. **变量不落库**：库里只有正文/提示词与那几张配置、发现表。变量表**每次现算**：`vars::VariableView::from_sources(会话, 生效的 system prompt, 来源, 消息列表)`——先扫提示词里的 `<state>` 块（**底子**），再按消息顺序（rowid 序）扫正文里的块，最后 fold。每条现演操作都带 `message_id`，"哪句话带来的状态"追得回来；切到前 N 条就是回溯。**没有派生表** ⇒ 编辑/删除消息、改提示词都不需要"重算变量"。别再往库里写操作日志（老表 `variable_ops` 已 DROP；`config/variables.jsonc`、`setglobal`/`delglobal` 都已废弃）。
   底子的**来源**决定它算哪一层：会话没写自己的提示词 ⇒ 用 agent 的 ⇒ 算**全局**（同一 agent 的会话共享）；会话自己写了 ⇒ 覆盖 agent 的 ⇒ 算**本会话**。会话里的 `del` 写墓碑，挡住全局同名键，不会从底下漏回来。生效的那份提示词由 `server::effective_system_prompt()` 一处解析（会话覆盖优先 → agent 的 → 内置默认兜底）——出站消息、变量底子、界面显示共用它。
4. **编辑/删除消息只动正文**：改的是存档本身（单事务）；变量不用管——它是现演的。不能跨会话改/删（404）。
5. **模型身份 = `(provider, upstream_id)`**；显示名三级回退（用户覆盖 → 上游名 → prettify）在**后端**完成，前端别再实现一遍。新建会话的 agent 取 `agents.jsonc` 的 `default_agent`（请求可显式指定 `agent_id`）——否则 agent 的提示词与它写的变量底子对任何新会话都不生效。
   **agent 的 id 由后端生成（UUIDv7）、不可变**：`POST /agents` 只收 `name` + `system_prompt`（名称是唯一人类句柄，空名给 400）；界面里 id 只读。手写进 `agents.jsonc` 的自定义 id 照收（历史数据不改），要收拾历史 id 就走 `PATCH /agents/{id}` 的 `new_id`——它会把 `default_agent` 与**所有会话的引用**一起搬（`agent_id` 是软引用、无外键，`resolve()` 找不到只会静默回空提示词，所以必须由这一处维护一致性）。
6. `config/secrets.json` 与 `data/` **永不入库**；`.gitignore` 只放行 Godot 项目的非缓存部分（`.godot/`、`export/` 排除）。
7. **消息顺序 = 树上的当前路径**：每条消息有 `parent_id`（自引用、`ON DELETE CASCADE`），会话有 `current_leaf`——整条对话 = 从 `current_leaf` 沿 `parent_id` 回溯到根（`store::path_from`）。**兄弟就是分支**：重新发送 = 再长一个兄弟（旧的留着，`‹ 2/3 ›` 切回去），切分支 = 改 `current_leaf`（`PATCH /conversations/{id}` 的 `current_leaf` 会顺着最新的孩子走到末端）。**别再按 rowid 或 id 排消息**。删除只允许删**整棵子树**（`store::delete_message` 返回条数；leaf 若落在被删子树里就退回被删那条的父亲）。变量只按当前路径现演 ⇒ 换分支生效值跟着换。


## 踩过的坑（都是实测出来的）

- **Godot `offset_transform_position` 的单位是 ui，不是像素**。Android 的键盘高度是像素 → 必须乘 `视图高/窗口高`；直接填像素会**飞出屏幕**（表上实测 775px×2.22=1722px > 屏高 1600）。
- 同上的**两个开关默认是反的**：`offset_transform_enabled=false`、`offset_transform_visual_only=true`。只打开 enabled → "看得见、点不到"。
- **Web 上引擎读不到软键盘**：`virtual_keyboard_get_height()` 是基类桩（返回 0 + 每次告警），而 `has_feature(FEATURE_VIRTUAL_KEYBOARD)` **却是 true**。Web 只能读 `window.visualViewport`（`JavaScriptBridge`，且要用 `Engine.get_singleton` 拿，直接写类名会让桌面构建解析失败）。
- **Web 没有系统字体 fallback** → CJK 必须把字体**打进项目**并挂 `theme/custom_font` / `FontFile.Fallbacks`，否则全是白框；桌面和安卓看不出来（它们用系统字体兜底）。
- **Godot 的"嵌套太深"多半是没有职责的容器**；padding 应写进已有容器/控件的 `StyleBoxFlat.content_margin`，**别为留白加节点**。抽子场景**不减少运行时深度**，只减少你要翻的树。
- **`--check-only --script` 看不到 autoload / 全局类名**，会误报 `Identifier not found`；要检查就用 `--quit-after 3` 实跑。
- **egui 的右键菜单**：`TextEdit` 在**右键按下那一帧**就把选区折成光标，菜单只能在**上一帧的快照**上工作（见 `attach_edit_menu`），别现场读选区。
- **Godot 里没有 `[display] window/stretch`** 时视图坐标 ≠ 设计坐标：无头/手机上会得到 64×64 之类的怪尺寸，界面按 1:1 像素渲染（看起来只有一半大）。键盘换算用比例实现的，不受影响，但观感会变。

## 返回键 / 退出（方案已定，**尚未开工**）

两个开关**各管一扇门、互不代管**（都在 `SceneTree` 上，默认都是 `true`）：

| 开关 | 管哪扇门 | 对应通知 | 对应信号 |
|---|---|---|---|
| `auto_accept_quit` | 关窗请求（桌面右上角 ×、Web 关窗） | `NOTIFICATION_WM_CLOSE_REQUEST` = 1006 | `Window.close_requested` |
| `quit_on_go_back` | 安卓返回键 / 返回手势 | `NOTIFICATION_WM_GO_BACK_REQUEST` = 1007 | `Window.go_back_requested` |

引擎里的顺序（源码级，`scene/main/window.cpp`）：根窗口先 `_propagate_window_notification()` → **全树节点的 `_notification` 先收到** → 再 `emit_signal(...)` → SceneTree 按自己那个开关决定 `_quit`。即"通知一定先到，自动退出是之后才判的"。
`get_tree().quit()` **不走这条路**（直接 `_quit = true`、不发通知）——想"退出前干点事"别用它。

**当前设定**（`frontend/project.godot`，提交 `f9d04dd`）：`config/quit_on_go_back=false`（返回键留给面板栈），`auto_accept_quit` 保持默认 `true`（桌面/Web 的 × 直接退；状态在后端，前端没有要抢救的东西）。

**还没做（先别顺手做，等排期）**：

1. **返回栈**：`_on_back()` 一处收口——安卓接 `get_window().go_back_requested`，桌面/Web 接 `ui_cancel`（Esc），栈空才真退。
2. **安卓双击退出**：顶层时第一次按返回键只提示"再按一次退出"（约 2 秒内再来一次才 `quit()`）——防止手滑把应用滑没了。
3. 安卓"从最近任务划掉" = **进程被杀**，任何开关都拦不到 → 该落盘的东西要在 `NOTIFICATION_APPLICATION_PAUSED` 里落。

**坑**：返回栈接上之前，安卓按返回键**什么都不发生**（不退、也没人处理）。另：4.8.dev5 实测"只关 `auto_accept_quit`、返回键那条路也不退"，与 master 源码（`_main_window_go_back()` 只读 `quit_on_go_back`）不符 → **别依赖这个实现细节**，要拦哪条路就显式关哪条路的开关。

## 本机环境

- 搜索：`tavily` MCP（`mcp__tavily_*`，配置在 `~/.omp/agent/mcp.json`）——本机直连的几家搜索引擎常被反爬挡掉，优先用它。
- 真机：SM-X810（Galaxy Tab S9+，2560×1600 横屏），**无线 adb**；可 `adb shell input tap X Y` 精确点击、`screencap` 截图。
- Godot：`~/.local/bin/godot`（4.8.dev5），导出模板齐（Android/Web 都在）。
- Web 版服务与 SSL 由用户自己提供。

## 本项目的协作习惯

- 注释、提交信息用**中文**；提交信息写清"为什么"，别只写"改了什么"。
- 改完跑 `cargo test`；**UI 改动必须实跑**（真机或灌事件的无头验证），不要只凭代码断言。
- **后端代码一变就重启后端和 Rust 前端**：先 `cargo build`，再重启 `./target/debug/server`（唯一权威）与 `./target/debug/microchat`（界面）——不然你在界面上验的是旧二进制。
- **先量再断言**：能实测的就不猜（本项目几乎所有关键结论都来自实测）。
- 用户可能**同时在编辑器里改 `frontend/`**：改场景前先读最新文件（并留备份），他的未保存改动优先。
- 提交前确认没把 `secrets.json` / `data/` / 大 APK 带进去。
