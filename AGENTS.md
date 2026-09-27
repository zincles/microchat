# AGENTS.md — 工作须知

microchat：轻量 SillyTavern 替代（RPG 向）。三层，边界要清楚：

| 层 | 位置 | 状态 |
|---|---|---|
| 后端（唯一权威） | `src/`（Rust + axum + **SQLite**） | 活跃开发 |
| egui 前端（**当前主用界面**） | `src/main.rs`、`src/client.rs` | 活跃开发：会话树、变量栏、设置页都在这儿 |
| Godot 4.8 前端 | `frontend/` | **用户自己在编辑器里设计**——动手前先问，别替他做设计决定；还没接真实数据 |

## 常用命令

```bash
cargo test                                     # 全量测试（当前 115 项）
./target/debug/server                          # 后端，默认 127.0.0.1:8787
./target/debug/microchat                       # egui 前端（主用界面）

cd frontend
godot --headless --path . --quit-after 3       # 加载并跑几帧（用它当"语法+运行"检查）
godot --headless --script /tmp/x.gd            # 灌事件跑帧做无头验证
ANDROID_HOME=~/Android/Sdk godot --headless --path . --export-debug "Android" /tmp/x.apk
adb install -r /tmp/x.apk && adb logcat -s godot
```

## 不变量（破坏了会静默出错）

1. **存储是 SQLite**：`data/microchat.db`。用户手写的配置只在 `config/`（**严格 JSON**：注释与尾逗号都报错——程序会整体重写这些文件；格式见文末）。两者路径都**相对工作目录**，可用 `MICROCHAT_CONFIG_DIR` / `MICROCHAT_DATA_DIR` 覆盖。界面偏好另存一份：`~/.config/microchat/frontend.json`（不同机制，别混）。
2. **正文与提示词都是存档**：`<state>` 块原样留在消息正文**和 system prompt** 里；**发给模型的文本一律剔除标签**，改注入当前变量表。唯一出口 `vars::build_outgoing()`——不要写第二条拼装路径。
3. **变量不落库**：库里只有正文/提示词与那几张配置、发现表。变量表**每次现算**：`vars::VariableView::from_sources(会话, 生效的 system prompt, 来源, 消息列表)`——先扫提示词里的 `<state>` 块（**底子**），再按**当前路径**顺序扫正文里的块，最后 fold。每条现演操作都带 `message_id`，"哪句话带来的状态"追得回来。**没有派生表** ⇒ 编辑/删除消息、改提示词、换分支都不需要"重算变量"（换分支只要重拉 `/conversations/{id}/variables`，那就是重算本身）。
   底子的**来源**决定它算哪一层：会话没写自己的提示词 ⇒ 用 agent 的 ⇒ 算**全局**（同一 agent 的会话共享）；会话自己写了 ⇒ 覆盖 agent 的 ⇒ 算**本会话**。`del` 写墓碑，挡住全局同名键，不会从底下漏回来。生效提示词由 `server::effective_system_prompt()` 一处解析（会话覆盖优先 → agent 的 → 内置默认兜底），出站消息 / 变量底子 / 界面显示共用它。**别再把操作日志写回库**（老表 `variable_ops` 已 DROP；`config/variables.json`、`setglobal`/`delglobal` 都已废弃）。
4. **消息是一棵树**：`messages.parent_id`（自引用、`ON DELETE CASCADE`）+ `conversations.current_leaf`。整条对话 = 从 `current_leaf` 沿 `parent_id` 回溯到根（`store::path_from`）。**兄弟就是分支**：
   - **重新发送 = 再长一个兄弟**（旧的留着，不是覆盖）；尾条是用户消息时照它生成；
   - **切分支只允许在最新那句上**（`store::switch_leaf_to_sibling`，目标必须是当前尾巴的兄弟，否则 400）。**别放开"切到任意旧消息"**：那是把对话倒回去，而变量沿路径现演 ⇒ 世界状态会跟着倒退；
   - **删除只允许删整棵子树**（`store::delete_subtrees`，返回条数）。leaf 若落在被删子树里，退到**上文下还活着的最新一个孩子**（= 上一条兄弟），没有才退到上文——只退到上文会让界面看起来"整条分支都没了"；
   - 「删除全部」= 清掉一组兄弟（连同各自子树），只留上文。
   - **生成中的回复不在库里**：受理时先把它的 id 算好（`Uuid::now_v7()`）随回执发给客户端（前端拿它指认"正在生成的那条"），但要等**整段**从上游拿到，才用这个 id 与受理时捕获的上文 `INSERT`（`store::insert_message_with_id`）。存档里只有完整的对话：停止、失败、进程被杀都不会留下半条，因此也**没有任何"清理占位"的机制**要维护。
   **别再按 rowid 或 id 排消息**；`rowid` 只在"同龄兄弟谁先谁后"里当顺序用。
5. **模型身份 = `(provider, upstream_id)`**；显示名三级回退（用户覆盖 → 上游名 → prettify）在**后端**完成，前端别再实现一遍。新建会话的 agent 取 `agents.json` 的 `default_agent`（空串/缺失都算没配 ⇒ 用内置默认），否则 agent 的提示词与变量底子对任何新会话都不生效。
   **agent 的 id**：新建时由后端生成 UUIDv7（`POST /agents` 只收 `name` + `system_prompt`，空名 400）；**已存在的可以在编辑器里改**，那走 `PATCH /agents/{id}` 的 `new_id`——一次把 `default_agent` 与**所有会话的引用**搬过去（`agent_id` 是软引用、无外键，`resolve()` 找不到只会静默回空提示词，所以必须由这一处维护一致性）。
6. **`config/` 整块与 `data/` 永不入库**（都是你个人的：端口口令、provider 连接信息、agent 预设、密钥、存档）。默认值全在代码里（各结构的 `Default` + 内置 dummy provider + 内置 default agent），这些文件不存在也能跑。`.gitignore` 只放行 Godot 项目的非缓存部分（`.godot/`、`export/` 排除）。
7. **HTTP API 看「HTTP API（客户端契约）」那一节**：那张表是从 `src/server.rs` 抽出来的，并**逐条发真实请求核过**。改了接口就跑一遍 `cargo build && python3 scripts/api-audit.py`（它自起沙盒，不碰你的 `config/`、`data/`）。

## 与上游（provider）通信：只许用成熟的外部库

硬规矩：**发往上游的流量只走现成的成熟 crate，不自己写传输层、也不自己写协议解析。**

- HTTP 一律 `reqwest`（0.13，rustls）。**不许另开第二个 HTTP 栈**——探活、拉模型列表、对话都走 `providers::Client`；`Client::from_parts` 只服务"先探测再保存"那条路。
- 数据结构用 `serde` / `serde_json` 映射成 `WireMessage` / `ChatResponse` 这类**纯数据形状**（这是数据映射，不是协议实现）。
- **流式（下一步）用现成的 SSE crate**（`reqwest-eventsource` / `eventsource-stream` 一类）：别手搓 `data: ` 分帧、断线重连、`[DONE]` 处理。手写协议解析是 bug 温床。
- 超时别忘：`providers.json` 的 `timeouts`（默认 connect 15s / total 300s）。**reqwest 默认不设总超时**，不配就是上游卡住、界面转一辈子。

## HTTP API（客户端契约）

**唯一权威是 `src/server.rs` 的路由表。** 下表由 `scripts/api-audit.py` 抽取而来，并**逐条发真实请求核过**：静态比对客户端每个调用点的方法/路径；动态对每条路由发正确方法（断言状态码）与错误方法（断言 405）。

现在**唯一在用的消费者是 egui 前端**（`src/client.rs`）；Godot 前端还没接任何接口（它一起来就是这份契约的第二个消费者，所以接口要稳）。

```bash
cargo build && python3 scripts/api-audit.py     # 改过接口就跑一遍（它自起沙盒）
```

### 通则

- 全部挂在 **`/api/v1`** 下；请求与响应都是 JSON（`content-type: application/json`）。
- 配了口令时（`config.json` 的 `server.auth_token`）**所有**接口都要 `Authorization: Bearer <token>`，`/health` 也不例外；缺了或错了回 **401**。
- 错误体固定 `{"error":{"code","message"}}`，`code` 是稳定枚举（`invalid` / `not_found` / `conflict` / `unauthorized` / `upstream` / `internal`）——客户端按 `code` 分支，别匹配文案。
- **405 是 axum 自己回的**（路径在、方法不对），不带上面那个体，但带 `allow:` 响应头：该用哪个方法看它。
- CORS 全开（为将来的 Web / Flutter 客户端）；**默认不设 auth_token**，所以别把端口暴露到公网。

### 会话与消息

| 方法 | 路径 | 请求体 | 响应 | 说明 |
|---|---|---|---|---|
| GET | `/conversations` | — | `[ConversationView]` | 每项 = 会话 + `turn`（左栏据此标"生成中"） |
| POST | `/conversations` | `CreateConversationReq` | `Conversation` · 201 | 省略字段时取 `config.json` 的 `defaults` |
| PATCH | `/conversations/{id}` | `UpdateConversationReq` | `Conversation` | 改标题 / 换模型 / 换 agent / **切分支**（`current_leaf`） |
| DELETE | `/conversations/{id}` | — | 204 | 不存在 → 404 |
| GET | `/conversations/{id}/messages` | — | `[Message]` | **按树上的当前路径**，不是 rowid |
| POST | `/conversations/{id}/messages` | `SendReq` | `TurnAccepted` · **202** | 落用户消息 + 开工；不含回复正文（见下一节） |
| PATCH | `/conversations/{id}/messages/{mid}` | `EditMessageReq` | `Message` | 改正文 = 重写存档（变量随之现演） |
| DELETE | `/conversations/{id}/messages/{mid}` | — | `{"deleted": n}` | 删**整棵子树**；leaf 退到还活着的最新兄弟 |
| DELETE | `/conversations/{id}/messages/{mid}/siblings` | — | `{"deleted": n}` | 「删除全部」：清掉一组兄弟，只留上文 |
| POST | `/conversations/{id}/resend` | — | `TurnAccepted` · **202** | 尾条是助手就再长一个兄弟；是用户消息就照它重发 |
| GET | `/conversations/{id}/branches` | — | `{消息id: BranchInfo}` | 每条在同龄兄弟里第几/共几（`‹ 2/3 ›`） |
| GET | `/conversations/{id}/variables` | — | `VariableView` | `global` / `session` / `effective`，**每次现算** |
| GET | `/conversations/{id}/outgoing` | — | `[Outgoing]` | "下次真会发出去的东西"（标签已剔除、变量已注入） |

### 生成这一轮（202 + 轮询）

| 方法 | 路径 | 请求体 | 响应 | 说明 |
|---|---|---|---|---|
| GET | `/conversations/{id}/status` | — | `TurnStatus` | `idle` / `pending` / `streaming` / `error` + `message_id` + `elapsed_ms` + `chars`（正文）+ `thinking_chars`（思考）+ `error` |
| GET | `/conversations/{id}/turn/text?from=N&think_from=M` | — | `StreamSlice` | 流式增量的**游标读**（正文与思考各一条游标，`from` = 第几个字符）：**只服务动画**，见下节 |
| POST | `/conversations/{id}/stop` | — | `{"stopped": bool}` | **幂等**：没在跑也 200（`false`） |

同一会话在跑时再来一发 → **409**（`conflict`）。`TurnAccepted` = `{user?, backend, turn}`：`turn.message_id` 是**这条回复的 id**（受理时就定好，但那会儿它还没进库——拿到整段才 `INSERT`）。

### provider / 模型 / agent

| 方法 | 路径 | 请求体 | 响应 | 说明 |
|---|---|---|---|---|
| GET | `/providers` | — | `[ProviderView]` | **含 `has_key`，绝不含密钥内容** |
| POST | `/providers` | `CreateProviderReq` | `ProviderView` · 201 | `api_key` 写进 `providers.json` 那条记录；重名 → 409 |
| PATCH | `/providers/{id}` | `UpdateProviderReq` | `ProviderView` | `api_key`：`None` = 不动，`""` = 清除 |
| DELETE | `/providers/{id}` | — | 204 | 密钥随记录一起没 |
| POST | `/providers/{id}/refresh` | — | `ProviderView` | **POST**（会写发现态）：拉 `/models` 并落库 |
| DELETE | `/providers/{id}/models` | — | `{"deleted": n}` | 清掉这个渠道**已发现的模型**（发现态）：不动 `providers.json`、不动历史会话；上游不可用时也能清干净 |
| POST | `/models/probe` | `ProbeReq` | `ProbeResult` | 用一次性 url+key 试拉，**不落库**（"先探测再保存"） |
| GET | `/models` | — | `[ModelListItem]` | 跨 provider 拍平，给"渠道 / 模型"一个下拉用 |
| GET | `/agents` | — | `AgentsConfig` | 生效列表（含内置默认 agent） |
| POST | `/agents` | `CreateAgentReq` | `Agent` · 201 | 只收 `name` + `system_prompt`；id 由后端生成 |
| PATCH | `/agents/{id}` | `UpdateAgentReq` | `Agent` | `new_id` = 重命名（把 `default_agent` 与会话引用一起搬） |
| DELETE | `/agents/{id}` | — | 204 | 内置默认 agent 不可删 |

### 运维

| 方法 | 路径 | 请求体 | 响应 | 说明 |
|---|---|---|---|---|
| GET | `/health` | — | `{"status","version"}` | 探针；**也过鉴权**（401 与"连不上"是两种失败） |
| GET | `/debug/state` | — | `DebugState` | 后端自述（结构上就没有密钥字段） |
| GET | `/debug/last-payload` | — | `LastPayload` | **最近一次实际发给上游的请求体**（内存里一份，覆盖式；没发过 = `null`）。与 `/outgoing` 的区别：那是"预计会发什么"，这是"实际发了什么" |
| GET | `/debug/file/{name}` | — | `RawFile` | 白名单只有 `config.json` / `providers.json` / `agents.json`；**`providers.json` 里的 `api_key` 会先打码成 `"***"`** |

## 流式输出：增量只服务动画，落库只认完整正文

一条铁律：**流式过程中的增量什么都不算**。它不进库、不进树、不参与变量与出站计算——那些只认"流结束后完整落库的那一条消息"。所以流断了、被停了、上游中途报错，档案永远是干净的。

- **上游**：`reqwest-eventsource`（建在同一个 `reqwest` 上）——按既定规矩用成熟库，不手搓 `data: ` 分帧与 `[DONE]`。落在 `providers::Client::chat_completion_stream`；`chat::complete_with(.., stream, on_chunk)` 是唯一入口（`complete` 是它的薄封装）。
- **服务端**：每个增量 `TurnRegistry::append_*` 进这一轮的缓冲区，顺手把 `pending` 抬成 **`streaming`**；令牌对不上（已被 `stop`）的字节直接丢弃。缓冲区随 entry 存活一轮，下一轮 `begin` 覆盖。
- **思考流单独一条缓冲**：推理型模型（DeepSeek V4 系等）先一连串吐 `reasoning` / `reasoning_content`，**再**吐 `content`——上游字段名两家不同，两个都认。思考同样只服务动画，**不参与任何计算**：它不进历史（不回喂给模型）、不扫 `<state>`、不可编辑。
- **思考会留档**：落库时写进 `messages.reasoning`（可空列），界面上默认折叠成「思考（N 字）」。`providers.json` 里给 provider 写 `"store_reasoning": false` 就只走动画、不留档（默认开）。实测它常比正文长十倍上下。
- **给前端**：`GET /conversations/{id}/turn/text?from=N&think_from=M` 是**游标读，不消费**——两条流各一条游标，多个客户端（egui + Godot）与断线重连各拿各的，互不偷。`done` 表示这一轮已结束，结束后仍可补拉尾巴。
- **落库时机不变**：流跑完 → 完整正文 → 才用受理时发出去的那个 `id` `INSERT`。界面先拿增量渲染一个**合成气泡**（不在库里），真消息一落库，同一个 id 让它自然接管。
- **关掉流式**：`providers.json` 里给某个 provider 写 `"stream": false` 就退回非流式（界面只是"不播动画"）。默认开。

## 一轮生成的生命周期（202 + 轮询）

发送不再"一次请求等到整段回复"：**受理与生成分开**。因为客户端要答三个问题——"在不在跑""跑了多久""怎么停"——而这三个答案都不该绑死在一次 HTTP 请求上。顺带，非流式与流式由此**共用同一套前端**：流式不过是"同一个消息 id 的正文在变长"。

```text
  idle ──POST /messages 或 /resend──► pending ──（流式才进）──► streaming
    ▲                                   │                         │
    │                                   └──── 成功 ──► idle ◄─────┘
    │                                             失败 ──► error
    └── POST /stop ────────────────────────────────────────────┘
```

- 状态在 `src/turn.rs` 的 `TurnRegistry`（进程内 `HashMap`，**不进库**）：后端一重启就没有生成在跑，回到 `idle` 是诚实的；
- 每次受理：**先算好这条回复的 id → 登记 → `tokio::spawn` 生成**。`begin` 是唯一的并发闸门（同一会话只放行一轮），所以两个请求同时挤进来只会有一个 202，另一个 409；
- **生成中的回复不在库里**：等整段回来才 `INSERT`，用的就是受理时发出去的那个 id。前端先按状态合成一个气泡，落库后它"变成"同一条消息（id 一样，不会重影）；
- 忙时再发/重发 → **409**（不排队：排队会让"按了发送却什么都没发生"难以解释）；
- 后台任务落库前核对 `token`（`is_mine`）：被 `stop` 顶掉的旧任务回来时令牌对不上，结果直接丢掉——**不会把过期回复写进库**；
- `stop` = 摘登记 → `abort` 后台任务（别让上游白跑完）。**库里没有要收拾的东西**（回复根本没进去）；
- `turn` 里的 `elapsed_ms` / `chars` / `error` 就是给界面用的：正在生成… 12.3s、失败原因、以及流式落地后的增量游标；
- 上游超时写在 `providers.json` 的 `timeouts`（默认 connect 15s / total 300s）。**reqwest 默认不设总超时**——不配就是上游卡住、界面转一辈子。

## 踩过的坑（都是实测出来的）

- **`reqwest` 默认不设总超时**：`Client::builder().build()` 出来的客户端，上游不回应就永远挂着——界面上只有一个转不完的"等待…"。超时现在写在 `providers.json` 的 `timeouts` 里（默认 connect 15s / total 300s）。
- **删掉"当前那条回复"时，leaf 不能退到上文**：那样路径上一条回复都没有，界面看起来像"整条分支被删了"（其实兄弟都在树上）；再点重新发送又会多出一条，于是"怎么又变三条了"。正解 = 退到上文下**还活着的最新兄弟**。
- **配置文件里没有注释可写**：程序整体重写这些文件（改一个 provider 就重写整份），JSONC 会给人"写了也会丢"的假象。现在严格 JSON，写坏了报的是 `Expecting property name enclosed in double quotes: line L column C`。
- **一次失败的 `git commit`（"无文件可提交"）会把此前 staged 的东西留在索引里** → 下一次提交把它们一起卷走（本项目真发生过：用户 Godot 编辑器的改动被带进了一笔无关提交）。**提交前先 `git diff --cached --name-status` 核对范围**，别只看 `git status`。
- **批量改代码时逐文件落盘**：把 `write` 放在脚本末尾，中途任何一个断言失败都会让整批改动一起丢掉（改完一个文件就写一个，别攒着）。
- **Godot `offset_transform_position` 的单位是 ui，不是像素**。Android 的键盘高度是像素 → 必须乘 `视图高/窗口高`；直接填像素会**飞出屏幕**（表上实测 775px×2.22=1722px > 屏高 1600）。
- 同上的**两个开关默认是反的**：`offset_transform_enabled=false`、`offset_transform_visual_only=true`。只打开 enabled → "看得见、点不到"。
- **Web 上引擎读不到软键盘**：`virtual_keyboard_get_height()` 是基类桩（返回 0 + 每次告警），而 `has_feature(FEATURE_VIRTUAL_KEYBOARD)` **却是 true**。Web 只能读 `window.visualViewport`（`JavaScriptBridge`，且要用 `Engine.get_singleton` 拿，直接写类名会让桌面构建解析失败）。
- **Web 没有系统字体 fallback** → CJK 必须把字体**打进项目**并挂 `theme/custom_font` / `FontFile.Fallbacks`，否则全是白框；桌面和安卓看不出来（它们用系统字体兜底）。
- **Godot 的"嵌套太深"多半是没有职责的容器**；padding 应写进已有容器/控件的 `StyleBoxFlat.content_margin`，**别为留白加节点**。抽子场景**不减少运行时深度**，只减少你要翻的树。
- **`--check-only --script` 看不到 autoload / 全局类名**，会误报 `Identifier not found`；要检查就用 `--quit-after 3` 实跑。
- **egui 的右键菜单**：`TextEdit` 在**右键按下那一帧**就把选区折成光标，菜单只能在**上一帧的快照**上工作（见 `attach_edit_menu`），别现场读选区。
- **Godot 里没有 `[display] window/stretch`** 时视图坐标 ≠ 设计坐标：无头/手机上会得到 64×64 之类的怪尺寸，界面按 1:1 像素渲染（看起来只有一半大）。键盘换算用比例实现的，不受影响，但观感会变。

## egui 界面的现状（改它之前先看这节）

- 左栏：`＋ 新建对话` + 会话列表 + **底部的 `⟳ 刷新`**（一把把会话/消息/变量/分支/模型/agent 全拉一遍）。
- 会话视图：顶部**系统提示词那块**（可展开）→ 消息列表（每条：署名 + `编辑 / 复制 / 删除`）→ 底栏输入。
  - 助手署名 = **该会话 agent 的名字**（不是"助手"）；底部输入栏跟着软键盘浮（见 `frontend/main.gd` 的同名脚本）。
  - **分支操作只出现在最后一条消息上**：`‹ 2/3 ›`（切候选回复，只在最新那句允许）、`删除全部`、`重新发送`。
  - **生成中那条回复**是界面按状态**合成**的气泡（库里还没有它）：转圈 + "正在生成… 12.3s" + 「停止」。落库后同 id 的真实消息出现，气泡自然消失。发送按钮在此期间显示"等待…"；界面每 300ms 轮询 `/status`（`App::poll_turn`），收到 `idle` / `error` 才一次性重拉消息、变量、分支与列表。
  - **用量在写库前就归一化**：上游的 `usage` 各家字段名不一（缓存就有 `prompt_tokens_details.cached_tokens` 与 `prompt_cache_hit_tokens` 两种写法），归一化只有一处——`model::Usage::from_wire`。前端（egui 与将来的 Godot）只管读 `cached_tokens` 这些键，别各自再认一遍。**成本不在这里**：API 不返回 cost，要算得靠自己的价目表。
- **卡片脚注**：每条助手消息底部一行 `3.2s · 上行 1654 tok（缓存 1408 tok · 85%）· 下行 62 tok（思考 32 tok，含在内）`——只在有数据时显示（dummy / 兜底 / 老消息都没有）。**单位是 token**（上游 `usage` 报的）；生成中气泡上那个「思考中… N 字」是**字符数**（流式帧里没有 token 数，token 只在末帧 usage 里）——两处别混。另：`reasoning_tokens` 是 `completion_tokens` 的**子集**，不是另加。
- **思考**（`messages.reasoning`）默认折叠成「思考（2.1s）」——放的是**思考用时**（受理 → 第一段正文），不是字数（API 按 token 计费，"字"没意义）；迁移之前的老消息退回显示字数。点开才看内容 ✓；流式那会儿是实时展开的 ✓，正文一开始出就交给真消息（真消息里它还是折叠的 ✓）。
- 左栏：正在生成的会话标题后面挂着「 · 生成中…」（状态来自 `GET /conversations` 每项的 `turn`）。
- 调试页五个标签：**后端状态 / 最近发送载荷 / 原始配置 / 请求日志 / 关系图**。「最近发送载荷」原样显示最近一次发给上游的 `/chat/completions` 请求体（带 provider/模型、时间、字节数、复制按钮）——排查"看着都对、上游却报错"看它。
- 设置分五页（左导航同级）：**连接 / Agent / 模型与渠道 / 前端设置 / 关于**；右下角「保存」按页给出不同提示。
- 「模型与渠道」每个渠道一行：`获取模型`（POST，成功后在**底栏**说"已获取 N 个模型"）、`删除全部模型`（清发现态，配置与历史会话都不动）、`编辑`。
- 设置页底栏常驻"**已连接后端 vX**"（`App::connected`），后面跟 `·` 和最近一次动作的结果（`App::note`）——两者互不顶替：连接状态不该被下一条消息挤掉。刷新失败时 `note` 里带着状态码与后端的 `code`。
- 渠道与模型是**一个下拉**（`渠道 / 模型`，`GET /models` 拍平给的就是这个形状）；provider 的 `name` 缺省回退 `id`。

## 返回键 / 退出（方案已定，**尚未开工**）

两个开关**各管一扇门、互不代管**（都在 `SceneTree` 上，默认都是 `true`）：

| 开关 | 管哪扇门 | 对应通知 | 对应信号 |
|---|---|---|---|
| `auto_accept_quit` | 关窗请求（桌面右上角 ×、Web 关窗） | `NOTIFICATION_WM_CLOSE_REQUEST` = 1006 | `Window.close_requested` |
| `quit_on_go_back` | 安卓返回键 / 返回手势 | `NOTIFICATION_WM_GO_BACK_REQUEST` = 1007 | `Window.go_back_requested` |

引擎里的顺序（源码级，`scene/main/window.cpp`）：根窗口先 `_propagate_window_notification()` → **全树节点的 `_notification` 先收到** → 再 `emit_signal(...)` → SceneTree 按自己那个开关决定 `_quit`。即"通知一定先到，自动退出是之后才判的"。
`get_tree().quit()` **不走这条路**（直接 `_quit = true`、不发通知）——想"退出前干点事"别用它。

**当前设定**（`frontend/project.godot`）：`config/quit_on_go_back=false`（返回键留给面板栈），`auto_accept_quit` 保持默认 `true`（桌面/Web 的 × 直接退）。

**还没做（先别顺手做，等排期）**：
1. **返回栈**：`_on_back()` 一处收口——安卓接 `get_window().go_back_requested`，桌面/Web 接 `ui_cancel`（Esc），栈空才真退。
2. **安卓双击退出**：顶层时第一次按返回键只提示"再按一次退出"（约 2 秒内再来一次才 `quit()`）。
3. 安卓"从最近任务划掉" = **进程被杀**，任何开关都拦不到 → 该落盘的东西在 `NOTIFICATION_APPLICATION_PAUSED` 里落。

**坑**：返回栈接上之前，安卓按返回键**什么都不发生**。另：4.8.dev5 实测"只关 `auto_accept_quit`、返回键那条路也不退"，与 master 源码不符 → **别依赖实现细节**，要拦哪条路就显式关哪条路的开关。

## 配置文件长什么样

`config/` 里的文件都是**严格 JSON**（没有注释可写——程序整体重写它们），且都可缺失（缺了用代码里的默认值）。文件名一律 `.json`：

- `config/config.json` — `{ "version": 1, "server": { "port": 8787, "auth_token": "可选" }, "defaults": { "provider": "dummy", "model": "dummy", "agent": "default" } }`
- `config/providers.json` — `{ "version": 1, "providers": [ { "id": "dummy", "kind": "dummy" }, { "id": "openrouter", "name": "显示名可选", "base_url": "https://openrouter.ai/api/v1", "headers": { "X-Title": "microchat" } } ] }`
  **密钥就写在这一条里**（`"api_key": "sk-…"`，空串 = 没配）：整个 `config/` 在忽略范围内，所以它不进版本库；接口一律不回显（只回 `has_key`），调试页读这个文件时也会先打码。文件本身写回时权限收紧到 0600。
- `config/agents.json` — `{ "version": 1, "default_agent": "跑团", "agents": [ { "id": "跑团", "name": "跑团主持人", "system_prompt": "你是跑团主持人。<state>set 季节 = 初冬</state>" } ] }`
  `system_prompt` 里的 `<state>` 块就是**变量底子**（和消息正文同一套语法）；`id` 手写可用可读 id，界面新建则生成 UUIDv7。
- `~/.config/microchat/frontend.json` — 界面偏好（主题、缩放、服务器地址、回车是否发送……）。

## 数据模型（`src/store.rs` 的 `MIGRATIONS` 是唯一权威）

```sql
conversations(id, title, system_prompt, provider, model, agent_id, current_leaf, created_at, updated_at)
messages(id, conversation_id → conversations ON DELETE CASCADE, role, content, parent_id → messages ON DELETE CASCADE, created_at,
         reasoning,                                  -- 推理型模型的"思考"：只留档，不进历史、不扫 <state>、不可编辑
         reasoning_ms,                               -- 思考用时（受理 → 第一段正文）；没思考过是 NULL
         duration_ms,                                -- 这一轮整段生成花了多久（受理 → 落库）
         usage)                                      -- 用量 JSON：{prompt_tokens, completion_tokens, total_tokens, cached_tokens, reasoning_tokens, raw}
models(provider, upstream_id, upstream_name, owned_by, context_length, max_output, display_name, params, tokenizer,
       upstream_params, first_seen_at, last_seen_at)   -- 发现所得与用户覆盖分列；PRIMARY KEY(provider, upstream_id)
provider_state(provider, last_refresh_at)
```

- 顺序：**树**（见不变量 4）；`messages_by_conv` / `messages_by_parent` 两个索引。
- 迁移由 `PRAGMA user_version` 驱动，只追加、不改旧的；`foreign_keys=ON`、`journal_mode=WAL`。

## 本机环境

- 搜索：`tavily` MCP（`mcp__tavily_*`，配置在 `~/.omp/agent/mcp.json`）——本机直连的几家搜索引擎常被反爬挡掉，优先用它。
- 真机：SM-X810（Galaxy Tab S9+，2560×1600 横屏），**无线 adb**；可 `adb shell input tap X Y` 精确点击、`screencap` 截图。
- Godot：`~/.local/bin/godot`（4.8.dev5），导出模板齐（Android/Web 都在）。
- Web 版服务与 SSL 由用户自己提供。

## 本项目的协作习惯

- 注释、提交信息用**中文**；提交信息写清"为什么"，别只写"改了什么"。
- 改完跑 `cargo test`；**UI 改动必须实跑**（真机或灌事件的无头验证），不要只凭代码断言。
- **后端代码一变就重启后端和 egui 前端**：先 `cargo build`，再重启 `./target/debug/server`（唯一权威）与 `./target/debug/microchat`（界面）——不然你在界面上验的是旧二进制。
- **先量再断言**：能实测的就不猜（本项目几乎所有关键结论都来自实测）。
- 用户可能**同时在编辑器里改 `frontend/`**：改场景前先读最新文件（并留备份），他的未保存改动优先；提交时别把 `frontend/` 的改动卷进来（除非他让你一起提）。
- **提交由用户指挥**：**不要**每改一点就 `commit` + `push` —— 那样提交记录会碎成一地。做完一段有意义的进度后，先报告，**等用户说"可以提交了"**再提交；推送同理（用户没点名推送就不推）。默认节奏：改代码 → 跑测试 → 实跑验证 → 报告，**停在这里**。
- 提交时只带用户点名的范围：先 `git diff --cached --name-status` 核对，别把 `frontend/` 那边他自己在改的东西卷进来。
- 提交前确认没把 `config/`（里面有 `api_key`）、`data/`、大 APK 带进去。
