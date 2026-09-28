# AGENTS.md — 工作须知

microchat：轻量 SillyTavern 替代（RPG 向）。三层，边界要清楚：

| 层 | 位置 | 状态 |
|---|---|---|
| 后端（**唯一权威**） | `src/`（Go + 标准库 + `modernc.org/sqlite`） | **新代码一律写这儿**（2026-09-29 起）|
| 后端（**已废弃**） | `deprecated/`（旧的 Rust + axum 版） | **只读参照**：别再往里加功能；迁移完就删 |
| egui 前端 | `deprecated/src/main.rs` + `deprecated/src/frontend/` | **已冻结**（2026-09-28 起）：只修 bug、**不加功能**。
新界面一律做在 `frontend/`（Godot，**用户设计**，别替他做设计决定）；调试页暂留 egui |
| Godot 4.8 前端 | `frontend/` | **用户自己在编辑器里设计**——动手前先问，别替他做设计决定；还没接真实数据 |

## 术语表（**一个词只指一件事** —— 叙述一律用这里的词；代码内部名与历史对话原文不追）

| 词 | **只**指什么 | 别叫 |
|---|---|---|
| **世界状态（state）** | `<state>` 那套：沿当前路径**现演**的键值 + `del` 墓碑 + 三层来源（全局 / 本会话 / 生效） | 别叫"变量"；也别单说"状态"（那会跟任务状态、连接状态、流式状态撞） |
| **能力（Ability）** | **写死在代码里的一段固定流程**：程序（或将来的 Agent）拼材料、调它、产出回流（摘要器 / 索引 / 判断 / 角色扮演 / **对话本身**） | 别叫"工具"（撞函数调用）、"子 Agent"（暗示从属）、~~"内置 Agent"/"工序"~~（都已废弃） |
| **Agent** | **一份命名的人格 + 一组能力开关**（`agents.json` 的 `abilities`：谁启用什么）—— 同一个运行时，只是开关不同 | 别拿它指"某个能力" | 
| **任务（task）** | **一次有始有终的后台作业**：谁在跑、跑了多久、什么结果（`src/task.rs`）。**凡是会调 LLM 的必定是 Task**（反过来不成立：刷新模型不调 LLM，但也是 Task） | — |

词源：这个词改过好几轮（子 Agent → 工具 → 内置 Agent → 工序 → **能力**），根因是**当时没有定义**；
现在三层咬合、各就各位：**Agent（开关）→ 能力（流程）→ 任务（一次执行）**。
"能不能自主选下一步"**不再是 Agent 的判据**，而是将来某个能力（如 `plan`）的事。

## 代码布局

```
src/                       ← **后端（Go）**：主体代码都在这里
  go.mod · main.go         可执行入口（起服务、读配置、跑迁移）
  internal/
    statelang/             `<state>` 语法的唯一权威（零依赖纯函数 + testdata/ 共享语料）
    store/                 SQLite：打开/迁移/读写（migrations/*.sql 照搬自旧的 Rust 版）
    model/ config/ server/ …（逐个从下面那张模块地图搬过来）
deprecated/                ← 旧的 Rust 版（含 Cargo.toml），**只读参照**
  src/                     lib.rs / bin/server.rs / main.rs / frontend/（egui，已冻结）
frontend/                  ← Godot 4.8 前端（用户自己在编辑器里设计）
```

## 模块地图：一个模块 = 一份唯一权威 + 一条不变量

（按资源与不变量切，不按"谁调谁"切；完整表与理由见 `IMPORTANT_DISCUSSION.md` §35。）

**四条"唯一"，就是四条能写成守卫测试的规矩**：

| 模块 | 唯一权威 | 一句话不变量 |
|---|---|---|
| `model` | 纯结构 | 无依赖，谁都能用 |
| `statelang` | **`<state>` 语法的唯一权威** | 零依赖纯函数；**解析即验证**（没有第二个 Validate）；`Cleaned` 里**永不出现标签**（fuzz 守着）|
| `store` | **只有它碰 SQL** | 正文/提示词原样存档；变量不落库；树在 `parent_id`；生成中的回复不在库里 |
| `config` | `config/` 四个文件 | 可缺失；整体重写；密钥不回显 |
| `registry` | 模型口径 | 身份 `(provider, upstream_id)`；发现与覆盖分列，刷新不动用户列 |
| `providers` | **只有它碰网络** | 只走成熟 crate；超时必配 |
| `state` | **只有它拼出站文本** | 发出去的文本剔除标签、改注入世界状态；底子分层由提示词来源决定 |
| `blocks` | 块切分 | 块是推导的、不入库；不许劈开；开着的那块永不压 |
| `compact` | 摘要唯一入口 | **只往 summaries 插行** |
| `abilities` | 能力身份（枚举） | 产出不进历史/树/变量；失败不阻塞一轮 |
| `template` | `{{…}}` | 白名单 = 枚举；只扫一遍；会话提示词永不替换 |
| `chat` | 一轮生成怎么跑 | 拿到整段才 INSERT |
| `turn` | 这一轮的流细节 | 增量什么都不算；游标读不消费 |
| `task` | 后台作业的身份 + 生死 | Drop 兜底；绝不留下僵尸条目 |
| `server` | **只有它碰 HTTP** | 错误体固定；只编排不含业务 |

依赖方向单向：`server → chat/compact/abilities → state（含 statelang）/blocks → registry/providers/config → store → model`。
`turn`/`task` 是横切（谁都能挂号，不被依赖）。**Go 版由编译器强制这张图**（循环依赖编译不过）。

## 后端迁移：Rust → Go（2026-09-28 起）

**为什么要迁**：最初的出发点是"出事了我们也能修"，但用户不读 Rust ⇒ 那个前提失效了；
Go 语法少、读起来像 C（无指针运算）、标准库自带 HTTP/JSON ⇒ 用户（Go 1 年）能自己维护。

**迁移状态（2026-09-29）**：Go 版已经进 `src/` 成为主体；Rust 版整体挪进 `deprecated/` 当参照。
下面四条纪律继续有效 —— 它们现在管的是"Go 版必须与旧版一模一样"。

**迁移纪律（四条，缺一不可）**
1. **表结构照搬**：`src/internal/store/migrations/*.sql` 由 `deprecated/src/store.rs` 的 `MIGRATIONS` **生成**，
   **一字不改** —— 两版必须能开同一个 SQLite 文件（`PRAGMA user_version` 同一个机制）；
2. **接口照搬** `AGENTS.md` 那张表：`python3 scripts/api-audit.py` 全绿 = 接口层转对了；
3. **行为照搬**：171 条测试搬进 `go test` 全绿 = 行为层转对了；
4. **Godot 一行不改**：它只认 HTTP（当初"前端无关"的坚持，这天回本）。

**已验**：Go 建的库与 Rust 建的库**表结构逐字一致**；两版读同一份真库副本，账目相同（conversations/messages 一致）。

```bash
cd src && go build -o /tmp/microchat-go . && /tmp/microchat-go -addr 127.0.0.1:8787 -data ../data
# 只用标准库的路由：Go 1.22+ 的 ServeMux 原生支持 "GET /x/{id}" 模式
```

## 常用命令

```bash
cd src && go test ./...                         # 后端（Go）测试
cd src && go build -o /tmp/microchat-go . && /tmp/microchat-go -addr 127.0.0.1:8787 -data ../data

cd deprecated && cargo test                     # 旧 Rust 版（参照，别再改）
./deprecated/target/debug/server               # 旧后端（当前还在跑的就是它）
./deprecated/target/debug/microchat            # egui 前端（已冻结）

cd frontend
godot --headless --path . --quit-after 3       # 加载并跑几帧（用它当"语法+运行"检查）
godot --headless --script /tmp/x.gd            # 灌事件跑帧做无头验证
ANDROID_HOME=~/Android/Sdk godot --headless --path . --export-debug "Android" /tmp/x.apk
adb install -r /tmp/x.apk && adb logcat -s godot
```

## 不变量（破坏了会静默出错）

1. **存储是 SQLite**：`data/microchat.db`。用户手写的配置只在 `config/`（**严格 JSON**：注释与尾逗号都报错——程序会整体重写这些文件；格式见文末）。两者路径都**相对工作目录**，可用 `MICROCHAT_CONFIG_DIR` / `MICROCHAT_DATA_DIR` 覆盖。界面偏好另存一份：`~/.config/microchat/frontend.json`（不同机制，别混）。
2. **正文与提示词都是存档**：`<state>` 块原样留在消息正文**和 system prompt** 里；**发给模型的文本一律剔除标签**，改注入当前世界状态表。唯一出口 `vars::build_outgoing()`——不要写第二条拼装路径。
3. **世界状态不落库**：库里只有正文/提示词与那几张配置、发现表。世界状态**每次现算**：`vars::VariableView::from_sources(会话, 生效的 system prompt, 来源, 消息列表)`——先扫提示词里的 `<state>` 块（**底子**），再按**当前路径**顺序扫正文里的块，最后 fold。每条现演操作都带 `message_id`，"哪句话带来的状态"追得回来。**没有派生表** ⇒ 编辑/删除消息、改提示词、换分支都不需要"重算变量"（换分支只要重拉 `/conversations/{id}/state`（即将改名 `/state`），那就是重算本身）。
   底子的**来源**决定它算哪一层：会话没写自己的提示词 ⇒ 用 agent 的 ⇒ 算**全局**（同一 agent 的会话共享）；会话自己写了 ⇒ 覆盖 agent 的 ⇒ 算**本会话**。`del` 写墓碑，挡住全局同名键，不会从底下漏回来。生效提示词由 `server::effective_system_prompt()` 一处解析（会话覆盖优先 → agent 的 → 内置默认兜底），出站消息 / 世界状态底子 / 界面显示共用它。**别再把操作日志写回库**（老表 `variable_ops` 已 DROP；`config/variables.json`、`setglobal`/`delglobal` 都已废弃）。
4. **消息是一棵树**：`messages.parent_id`（自引用、`ON DELETE CASCADE`）+ `conversations.current_leaf`。整条对话 = 从 `current_leaf` 沿 `parent_id` 回溯到根（`store::path_from`）。**兄弟就是分支**：
   - **重新发送 = 再长一个兄弟**（旧的留着，不是覆盖）；尾条是用户消息时照它生成；
   - **切分支只允许在最新那句上**（`store::switch_leaf_to_sibling`，目标必须是当前尾巴的兄弟，否则 400）。**别放开"切到任意旧消息"**：那是把对话倒回去，而变量沿路径现演 ⇒ 世界状态会跟着倒退；
   - **删除只允许删整棵子树**（`store::delete_subtrees`，返回条数）。leaf 若落在被删子树里，退到**上文下还活着的最新一个孩子**（= 上一条兄弟），没有才退到上文——只退到上文会让界面看起来"整条分支都没了"；
   - 「删除全部」= 清掉一组兄弟（连同各自子树），只留上文。
   - **生成中的回复不在库里**：受理时先把它的 id 算好（`Uuid::now_v7()`）随回执发给客户端（前端拿它指认"正在生成的那条"），但要等**整段**从上游拿到，才用这个 id 与受理时捕获的上文 `INSERT`（`store::insert_message_with_id`）。存档里只有完整的对话：停止、失败、进程被杀都不会留下半条，因此也**没有任何"清理占位"的机制**要维护。
   **别再按 rowid 或 id 排消息**；`rowid` 只在"同龄兄弟谁先谁后"里当顺序用。
5. **模型身份 = `(provider, upstream_id)`**；显示名三级回退（用户覆盖 → 上游名 → prettify）在**后端**完成，前端别再实现一遍。新建会话的 agent 取 `agents.json` 的 `default_agent`（空串/缺失都算没配 ⇒ 用内置默认），否则 agent 的提示词与世界状态底子对任何新会话都不生效。
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

现在在用的消费者是 egui 前端（`src/frontend/client.rs`，**已冻结**）；Godot 前端还没接任何接口
（它一起来就是这份契约的第二个消费者，所以接口要稳——**别为某个前端改接口**）。

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
| PATCH | `/conversations/{id}/messages/{mid}` | `EditMessageReq` | `Message` | 改正文 = 重写存档（世界状态随之现演） |
| DELETE | `/conversations/{id}/messages/{mid}` | — | `{"deleted": n}` | 删**整棵子树**；leaf 退到还活着的最新兄弟 |
| DELETE | `/conversations/{id}/messages/{mid}/siblings` | — | `{"deleted": n}` | 「删除全部」：清掉一组兄弟，只留上文 |
| POST | `/conversations/{id}/resend` | — | `TurnAccepted` · **202** | 尾条是助手就再长一个兄弟；是用户消息就照它重发 |
| GET | `/conversations/{id}/branches` | — | `{消息id: BranchInfo}` | 每条在同龄兄弟里第几/共几（`‹ 2/3 ›`） |
| GET | `/conversations/{id}/state` | — | `VariableView` | `global` / `session` / `effective`，**每次现算** |
| GET | `/conversations/{id}/outgoing` | — | `[Outgoing]` | "下次真会发出去的东西"（标签已剔除、世界状态已注入） |
| GET | `/config/chat` | — | `ChatConfig` | 服务端 `config.json` 的 `chat` 段（**不含密钥**——口令不在这一段） |
| PUT | `/config/chat` | `ChatConfig` | `ChatConfig` | **整段替换** `chat`（其余段原样保留；写回严格 JSON） |
| PATCH | `/models` | `{provider, upstream_id, context_override}` | `ModelView` | 设置/清除某模型的**上下文覆盖值**（`null` = 清掉）。**用户列**：刷新永不覆盖它 |
| GET | `/abilities` | — | `[AbilityView]` | 能力清单 + 生效模板/渠道/模型 + 版本短号 + 内置模板 |
| PUT | `/abilities` | `AbilitiesConfig` | `[AbilityView]` | 整段替换 `abilities.json`；**未知 id ⇒ 400**（身份在代码里） |
| POST | `/conversations/{id}/compact` | `{blocks: N}` | `CompactStatus` · **202** | **手动压缩**：把最老的 N 个**对话块**收成一条摘要（后台跑，进度看 `GET /status` 的 `compact`）。同一会话同时只允许一个 |
| GET | `/conversations/{id}/summaries` | — | `[SummaryView]` | 摘要列表（关系图/摘要面板用）：`members`、`first/last_message_id` 都是**现算**的（不存库）；`members == 0` ⇒ 孤立摘要 |
| GET | `/conversations/{id}/export` | — | `ConversationExport` | 自包含导出：会话 + **全部消息（含分支）** + 摘要 + 现演世界状态 + `current_path`。只读 |
| POST | `/conversations/{id}/archive` | — | `ArchiveReceipt` · 200 | 写 `data/archive/<会话>-<时间戳>.json`（**剪枝前的必做动作**），回收据 `{path, bytes, messages, summaries}` |
| POST | `/conversations/{id}/fork` | — | `Conversation` · **201** | 复制出新会话：当前路径 + 尾巴那一层的兄弟（含子树）+ 摘要行（指针按映射改写）。**世界状态不复制**（现演，逐键相同） |
| GET | `/conversations/{id}/context` | — | `ContextUsage` | **只有数字**：`used_tokens`（估算）/ `budget_tokens` / `trigger_tokens` / `remaining_tokens`（超了报负数）/ `ctx_len` / `max_output` / `ratio` / `estimated` / `last_prompt_tokens`（上一轮上游实测）/ `over_budget` |

### statelang（给外部工具）

|方法|路径|请求体|响应|说明|
|---|---|---|---|---|
|POST|`/statelang`|`{"text": "任意文本"}`|`{"tables": {表名: {键: 值}}, "statements": [...], "diagnostics": [...]}`|**解析 + 计算**一段文本里的全部 `<state>` 块：不起对话、不落库、纯函数式。`tables` = 算完的值（删除生效、后写覆盖、空表消失；未命名表用**空串**作键）；`statements` = 读出来的操作（按出现顺序，删除未生效）；`diagnostics` 带行号。缺 `text` ⇒ 422，请求体语法错 ⇒ 400|

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

### 计划中的接口（**尚未实现**，形状先钉住）

| 方法 | 路径 | 说明 |
|---|---|---|


### 运维

| 方法 | 路径 | 请求体 | 响应 | 说明 |
|---|---|---|---|---|
| GET | `/health` | — | `{"status","version"}` | 探针；**也过鉴权**（401 与"连不上"是两种失败） |
| GET | `/debug/state` | — | `DebugState` | 后端自述（结构上就没有密钥字段） |
| GET | `/debug/last-payload` | — | `LastPayload` | **最近一次实际发给上游的请求体**（内存里一份，覆盖式；没发过 = `null`）。**dummy 也会拼一份**"本来会发出去"的（同一个 `chat_body`，形状一致、不联网）；fallback 仍是 `null`。与 `/outgoing` 的区别：那是"预计会发什么"（只有 messages），这是"实际发了什么"（连 model/stream 外壳） |
| GET | `/debug/file/{name}` | — | `RawFile` | 白名单只有 `config.json` / `providers.json` / `agents.json`；**`providers.json` 里的 `api_key` 会先打码成 `"***"`** |

## 流式输出：增量只服务动画，落库只认完整正文

一条铁律：**流式过程中的增量什么都不算**。它不进库、不进树、不参与世界状态与出站计算——那些只认"流结束后完整落库的那一条消息"。所以流断了、被停了、上游中途报错，档案永远是干净的。

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
- **`git commit` 不带路径 = 提交索引里的一切**：哪怕你刚核对过范围、只要用户那边还有 staged 的东西，一样会被卷走
  （本项目真发生过两次：一次是用户的 Godot 场景，一次是 `main.tscn`）。要只提自己那几样就给路径：
  `git commit <文件…>`（它记录这些文件的**工作区**内容，索引里别的东西一律不动）。改错了用
  `git restore --source=HEAD~1 --staged <文件>` + `git commit --amend` 修 —— **别用 `--worktree`**（那会丢用户的改动）。
- **批量改代码时逐文件落盘**：把 `write` 放在脚本末尾，中途任何一个断言失败都会让整批改动一起丢掉（改完一个文件就写一个，别攒着）。
- **Godot `offset_transform_position` 的单位是 ui，不是像素**。Android 的键盘高度是像素 → 必须乘 `视图高/窗口高`；直接填像素会**飞出屏幕**（表上实测 775px×2.22=1722px > 屏高 1600）。
- 同上的**两个开关默认是反的**：`offset_transform_enabled=false`、`offset_transform_visual_only=true`。只打开 enabled → "看得见、点不到"。
- **Web 上引擎读不到软键盘**：`virtual_keyboard_get_height()` 是基类桩（返回 0 + 每次告警），而 `has_feature(FEATURE_VIRTUAL_KEYBOARD)` **却是 true**。Web 只能读 `window.visualViewport`（`JavaScriptBridge`，且要用 `Engine.get_singleton` 拿，直接写类名会让桌面构建解析失败）。
- **Web 没有系统字体 fallback** → CJK 必须把字体**打进项目**并挂 `theme/custom_font` / `FontFile.Fallbacks`，否则全是白框；桌面和安卓看不出来（它们用系统字体兜底）。
- **Godot 的"嵌套太深"多半是没有职责的容器**；padding 应写进已有容器/控件的 `StyleBoxFlat.content_margin`，**别为留白加节点**。抽子场景**不减少运行时深度**，只减少你要翻的树。
- **`--check-only --script` 看不到 autoload / 全局类名**，会误报 `Identifier not found`；要检查就用 `--quit-after 3` 实跑。
- **egui 的右键菜单**：`TextEdit` 在**右键按下那一帧**就把选区折成光标，菜单只能在**上一帧的快照**上工作（见 `attach_edit_menu`），别现场读选区。
- **Godot 里没有 `[display] window/stretch`** 时视图坐标 ≠ 设计坐标：无头/手机上会得到 64×64 之类的怪尺寸，界面按 1:1 像素渲染（看起来只有一半大）。键盘换算用比例实现的，不受影响，但观感会变。

## egui 界面的现状（**已冻结**：改它之前先看这节，但只修 bug、不加功能）

- 左栏：`＋ 新建对话` + 会话列表 + **底部的 `⟳ 刷新`**（一把把会话/消息/变量/分支/模型/agent 全拉一遍）。
- 输入栏上方一排：`归档`（写 `data/archive/*.json`）、`压缩 [N] 个块` + `开始`（手动 Compact，202 受理，底栏报结果）、`复制会话`（Fork 出新会话并选中它）。
- 会话视图：顶部一行**上下文占用**（`上下文 12.3k（上轮实测 11.9k）/ 40k`；超 80% 黄、超触发阈值红，悬停有口径）→ 系统提示词那块（可展开）→ 消息列表（每条：署名 + `编辑 / 复制 / 删除`）→ 底栏输入。
  - 助手署名 = **该会话 agent 的名字**（不是"助手"）；底部输入栏跟着软键盘浮（见 `frontend/main.gd` 的同名脚本）。
  - **分支操作只出现在最后一条消息上**：`‹ 2/3 ›`（切候选回复，只在最新那句允许）、`删除全部`、`重新发送`。
  - **生成中那条回复**是界面按状态**合成**的气泡（库里还没有它）：转圈 + "正在生成… 12.3s" + 「停止」。落库后同 id 的真实消息出现，气泡自然消失。发送按钮在此期间显示"等待…"；界面每 300ms 轮询 `/status`（`App::poll_turn`），收到 `idle` / `error` 才一次性重拉消息、变量、分支与列表。
  - **用量在写库前就归一化**：上游的 `usage` 各家字段名不一（缓存就有 `prompt_tokens_details.cached_tokens` 与 `prompt_cache_hit_tokens` 两种写法），归一化只有一处——`model::Usage::from_wire`。前端（egui 与将来的 Godot）只管读 `cached_tokens` 这些键，别各自再认一遍。**成本不在这里**：API 不返回 cost，要算得靠自己的价目表。
- **卡片脚注**：每条助手消息底部一行 `3.2s · 上行 1654 tok（缓存 1408 tok · 85%）· 下行 62 tok（思考 32 tok，含在内）`——只在有数据时显示（dummy / 兜底 / 老消息都没有）。**单位是 token**（上游 `usage` 报的）；生成中气泡上那个「思考中… N 字」是**字符数**（流式帧里没有 token 数，token 只在末帧 usage 里）——两处别混。另：`reasoning_tokens` 是 `completion_tokens` 的**子集**，不是另加。
- **思考**（`messages.reasoning`）默认折叠成「思考（2.1s）」——放的是**思考用时**（受理 → 第一段正文），不是字数（API 按 token 计费，"字"没意义）；迁移之前的老消息退回显示字数。点开才看内容 ✓；流式那会儿是实时展开的 ✓，正文一开始出就交给真消息（真消息里它还是折叠的 ✓）。
- 左栏：正在生成的会话标题后面挂着「 · 生成中…」（状态来自 `GET /conversations` 每项的 `turn`）。
- 摘要（`compact` 子 Agent 的产出）就落在 `summaries` 表：**只插行**，`messages` 一个字不动（有测试守着）。
- 关系图：一列一条会话，列内 `会话 → system → 用户/助手…`；**摘要不在链上**，挂在它覆盖的那一段右侧一条"摘要道"上
  （越上层越靠右），每条被覆盖的消息各拉一条边过去 —— 金字塔一眼可见；`dirty` 的摘要画成琥珀色。
- **设置页要的数据只从 `load_server_data()` 一处发**（连上后端时 / 点 ⟳ 时 / 走进某个设置页时都走它）。
  曾经 `refresh_all` 与它各写一份"要拉哪些"，新加的两样只进了 ⟳ 那条路 ⇒ 设置页永远"加载中…"（真发生过）。
- 调试页五个标签：**后端状态 / 最近发送载荷 / 原始配置 / 请求日志 / 关系图**。「最近发送载荷」原样显示最近一次发给上游的 `/chat/completions` 请求体（带 provider/模型、时间、字节数、复制按钮）——排查"看着都对、上游却报错"看它；**dummy 也有一份**（拼而不发，形状一样）。
- 设置分六页（左导航同级）：**连接 / Agent / 能力 / 模型与渠道 / 前端设置 / 关于**；右下角「保存」按页给出不同提示。
- 「模型与渠道」页顶部是**服务端上下文口径**：`模型上下文`（上游没报时的兜底）+ `摘要触发阈值`（留空 = 预算 × 0.8）+ 保存。
  每个渠道一行：`获取模型`（POST，成功后在**底栏**说"已获取 N 个模型"）、`删除全部模型`（清发现态，配置与历史会话都不动）、`编辑`。
  下面**逐个模型列出上下文**：上游给了就写 `上下文 1.0M`，没给就写"上游没报"；右边一个输入框可填**覆盖值**（留空 = 清掉覆盖）。
  优先顺序：**覆盖 > 上游发现 > 配置里的兜底**（只写在 `vars::resolve_context` 一处）。
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

## `<state>` 的语法（记事板，不是脚本）

```
<state>
AA = 123456       # 赋值（值一律字符串）
B = 234; C = 落石  # `;` 当换行（一行一个变量）
delete(AA)        # 删除（括号内外留空格也认）
</state>
```
**删除不留痕迹**（2026-09-28 定稿）：`delete(键)` 就是**直接把这个键删掉** —— 不是"记一笔已删除"。
于是折叠结果只有两态（有值 / 没这个键），Go 里是 `map[string]string`。
代价：**"删"只作用于它自己那一层** ⇒ 删**提示词底子**里的键，值会从底子**漏回来**。
变通：不想被删掉的键别写进底子，写进**第一条消息**（同一层，删得掉）。详见 §38。

**空值就是清掉**（2026-09-28 定）：`键 =`（trim 后为空）与 `delete(键)` **效果相同** ⇒ 这门语言里
**存不下空串**。注意分工：**解析**照实报 `set`（值是空串），动手的是**计算**（`Fold`/`Tables`/`tables_of`）。

**每种写法只有一种规范形态**（砍掉的写法各给一条说得清的报错，不做兼容）：
赋值 `键 = 值`（`set 键 = 值` 已砍）· 删除 `delete(键)`（`del 键` / `del(键)` / `delete 键` 已砍）。
理由：LLM 写十次要能十次写对 ⇒ 形态越少越稳；`delete(A)` 是**调用形态**，与 `A = 1` 并列时最像训练数据里的样板代码。

块可出现在**任意位置**；**没有运算、没有变量作右值**（值就是字面量）；值里可以有 `=`（只在第一个处切）、
**不能有空格**（⇒ `A=1 B=2` 不合法，报错而不是静默删空格）。解析是**行式**的（无文法）；
不认识的行进 `warnings`，整块仍从正文剔除。定稿理由与实跑证据见 `IMPORTANT_DISCUSSION.md` §36。

## 配置文件长什么样

`config/` 里的文件都是**严格 JSON**（没有注释可写——程序整体重写它们），且都可缺失（缺了用代码里的默认值）。文件名一律 `.json`：

- `config/config.json` — `{ "version": 1, "server": { "port": 8787, "auth_token": "可选" }, "defaults": { "provider": "dummy", "model": "dummy", "agent": "default" } }`
- `config/providers.json` — `{ "version": 1, "providers": [ { "id": "dummy", "kind": "dummy" }, { "id": "openrouter", "name": "显示名可选", "base_url": "https://openrouter.ai/api/v1", "headers": { "X-Title": "microchat" } } ] }`
  **密钥就写在这一条里**（`"api_key": "sk-…"`，空串 = 没配）：整个 `config/` 在忽略范围内，所以它不进版本库；接口一律不回显（只回 `has_key`），调试页读这个文件时也会先打码。文件本身写回时权限收紧到 0600。
- `config/agents.json` — `{ "version": 1, "default_agent": "跑团", "agents": [ { "id": "跑团", "name": "跑团主持人", "system_prompt": "你是跑团主持人。<state>set 季节 = 初冬</state>" } ] }`
- `config/abilities.json` — **能力**的覆盖项：`{ "version": 1, "abilities": { "compact": { "system_prompt": "…（留空=用内置）…", "provider": null, "model": null } } }`。模板里可用 `{{…}}` 占位符（见「能力」一节）。
  它们的身份/名字/内置模板**写死在代码里**（`crate::abilities::Ability` 的枚举变体，目前只有 `compact` 摘要器），这里只能覆盖行为、不能造新的（未知 id 一律 400）。
  `system_prompt` 里的 `<state>` 块就是**变量底子**（和消息正文同一套语法）；`id` 手写可用可读 id，界面新建则生成 UUIDv7。
- `~/.config/microchat/frontend.json` — 界面偏好（主题、缩放、服务器地址、回车是否发送……）。

## 后台任务（`src/task.rs`）—— "这会儿有哪些活在跑"的唯一登记处

- **Task = 一次后台作业的登记项**（不是线程、不是进程、不是 agent）：前台一轮生成、一次压缩、一次刷新模型都挂号。
- `TaskKind`（`Turn` / `Compact` / `RefreshModels`……加一种就加一个变体）、`TaskRecord`（谁、何时起、跑了多久、什么结果）、
  `TaskRegistry`（进程内）、**`TaskGuard`（RAII）**：`let task = tasks.begin(…)` → `task.finish("完成 · 12 块")`；
  **忘了收或 panic 由 `Drop` 兜底记成"中断"** ⇒ 绝不会留永远 `running` 的僵尸条目。
- 三份状态各管一段，**不许互为镜像**：
  `task` = 身份 + 生死；`turn::TurnRegistry` = 流的细节（流了几个字、thinking 字符数、取消）；
  `turn::CompactStatus` = 压缩自己的细节（压了几块、产出哪条摘要，给点了按钮的客户端轮询）。
- 接口：`GET /tasks` ⇒ `TaskBoard { running, tasks, now }`（正在跑的在最前，已结束的留最近 60 条）。
- 界面：左栏底部一行**指示器**（`后台：空闲` / `后台：跑着 N 个`，点一下进「任务」页）+ 与 账户/设置/调试 **同级的「任务」页**；
  前端每 2 秒问一次 `/tasks`（就这一个端点）。

## 压缩（`src/compact.rs`）—— 唯一入口，分机制与策略两层

- **机制**：`compact::summarize_span(store, prompt, providers, abilities, conversation_id, Span{begin, end})`
  —— 给它一段（两端用 id 指，**可以是 message 也可以是 summary**），它拼材料（§15）→ 叫能力 →
  过闸（剔 `<state>`）→ 落库（单事务）。**二次压缩（金字塔）就是"喂 summary id"**，同一个函数。
- **策略**：`compact::summarize_blocks(…, limit)` —— 从第一条没被覆盖的消息起，取最老的 N 个**已闭合**块
  （手动按钮的语义）。将来的自动触发换个挑法，复用机制那一层。
- 谁也**不许**绕过这里自己拼摘要请求：材料形状、占位符、过闸、落库都只在这一处。
- 它**不该知道** `agents.json` / `providers.json` 长什么样 —— 那些由 `server::do_compaction` 解析成
  `compact::Prompt{text, source}` 传进来。
- 不持锁跨 await：取料在锁里、调用在锁外、落库再进锁（与 `turn::run_turn` 同一条纪律）。
- 块的"开/合"要看**整条路径**，不是看切出来的那一段（切到末尾的块看着"开着"，其实只要后面还有消息就已闭合）。

## 决策模型（JEV 一类）：**不是聊天模型** —— 用法见 `JEV.md`

上游不只"聊天"一种形状：`typesafe/jev` 那类是**决策模型**（状态 + 类型化问题 → 概率，`Choice`/`Noul`/`Score`），
不生成文本、不能驱动会话、也不在 `GET /models` 的发现列表里（所以"渠道与模型"下拉里找不到它，不是列表过期）。
要用它得**加第三种 provider kind**（现在是 `dummy` / `openai-compat`）。规划中的用途（判断（能力）/ 意图分类 /
触发角色心理）与四条规矩（只动尾部 / 失败降级 / 产出不进历史与变量 / 阈值实测校准）都记在 **`JEV.md`**。

## 能力（`src/abilities.rs`）—— 写死在代码里的固定流程

**能力一定是专用的，没有复用可言**（§25）⇒ 身份写死在代码里：`crate::abilities::Ability` 的枚举变体
（目前只有 `SubAgent::Compact` = 摘要器），id / 显示名 / 说明 / 内置模板都在代码里；
`config/abilities.json` 只能**覆盖**它的模板、渠道、模型，**不能造新的**。

与会话 Agent（`agents.json`，有人格、提示词进会话、能写 `<state>` 底子）的三条硬边界：
① 产出永不进历史/树/变量（落库由调用方决定：摘要器走 `store::record_summary`）；
② 提示词永不进会话的系统提示词；③ **失败不阻塞任何一轮**。

`abilities::run(config, Ability::Compact, material, conversation, providers)` 复用同一个 `providers::Client`（非流式）；
`prompt_version = abilities::prompt_version(生效模板)`（64 位哈希，改一个字就变 —— 别手写版本号）。

**占位符**（`src/template.rs`）：能力的模板里可以写 `{{name}}`，发请求前替换。
**「变量」这个词只留给世界状态**（`<state>` 那套 / `src/vars.rs`）—— 两者毫无关系，别混。**白名单就是枚举**：
`{{system_time}}`（UTC）/ `{{state_before}}` / `{{state_after}}` / `{{range}}` / `{{blocks}}`。
三条规矩：① 不认识的 `{{foo}}` **原样留着**（界面会提示），不报错也不猜；
② **只扫一遍** —— 替换进去的值不再当模板扫（否则用户文本里的 `{{` 就能玩坏注入）；
③ **会话 Agent 的提示词永不替换**（它一变，前缀缓存每轮全废 —— `build_outgoing` 里连这个词都不出现，有守卫测试）。
dummy 下也走全链路（回固定文本 + 拼好 payload，便于无 key 验证）。
接口：`GET /abilities`（清单 + 生效值 + 版本短号）、`PUT /abilities`（整段替换覆盖项、未知 id ⇒ 400）、`POST /conversations/{id}/compact`。

## 数据模型（`src/store.rs` 的 `MIGRATIONS` 是唯一权威）

**四张表，全部是 TEXT id（UUIDv7，按时间可排序）+ 毫秒整数时间戳。**

### `conversations` —— 一处会话

| 列 | 类型 | 说明 |
|---|---|---|
| `id` | TEXT PK | UUIDv7 |
| `title` | TEXT NOT NULL DEFAULT '' | 首句自动生成；空串 = 还没起名 |
| `system_prompt` | TEXT NOT NULL DEFAULT '' | **空串 = 没覆盖**（用 agent 的 ⇒ 变量底子算"全局"）；自己写了 ⇒ 算"本会话" |
| `provider` | TEXT NOT NULL | **软引用**（无外键）→ `providers.json` 的 id；删了就回落兜底话术 |
| `model` | TEXT NOT NULL | 该渠道下的 `upstream_id`；`(provider, model)` 才是模型身份 |
| `agent_id` | TEXT NOT NULL DEFAULT 'default' | **软引用** → `agents.json`；找不到静默回空提示词（所以重命名由 PATCH 一处维护） |
| `current_leaf` | TEXT | **整条对话 = 从它沿 `parent_id` 回溯到根**；无外键，删子树时由代码退回 |
| `created_at` / `updated_at` | INTEGER NOT NULL | 毫秒；界面按 `updated_at` 排序 |

### `messages` —— 一条消息，**同时是树的一个节点**

| 列 | 类型 | 说明 |
|---|---|---|
| `id` | TEXT PK | 生成回复时**受理那一刻就算好**并发给前端，落库用同一个 |
| `conversation_id` | TEXT NOT NULL，FK → `conversations(id)` **CASCADE** | |
| `role` | TEXT NOT NULL，`CHECK(role IN ('user','assistant'))` | 只有两种 |
| `content` | TEXT NOT NULL | **存档本体**：`<state>` 块原样留着（发出前才剔除、改注入当前世界状态表） |
| `parent_id` | TEXT，FK → `messages(id)` **CASCADE** | **树就在这一列**：NULL = 根；兄弟 = 分支；删父连子孙一起删 |
| `created_at` | INTEGER NOT NULL | 毫秒 |
| `reasoning` | TEXT | 推理型模型的"思考"：**只留档**——不进历史、不扫 `<state>`、不可编辑 |
| `reasoning_ms` | INTEGER | 思考用时（受理 → 第一段正文）；没思考过 NULL |
| `duration_ms` | INTEGER | 这一轮整段耗时（受理 → 落库） |
| `usage` | TEXT（JSON） | 归一化后的 `{prompt_tokens, completion_tokens, total_tokens, cached_tokens, reasoning_tokens, raw}` |

后四列**只服务显示**：出站、变量、分支、编辑一律不看它们。

### `models` —— 发现所得 + 用户覆盖（PRIMARY KEY `(provider, upstream_id)`）

| 列 | 类型 | 说明 |
|---|---|---|
| `provider` / `upstream_id` | TEXT NOT NULL | 联合主键；软引用 + 上游原样 id |
| `upstream_name` / `owned_by` | TEXT | 发现时记下；**用户覆盖 > 上游名 > prettify** |
| `context_length` / `max_output` | INTEGER | 上游给的（**实测 DeepSeek / commandcode 都不给 context_length** ⇒ 常 NULL） |
| `display_name` | TEXT | **用户覆盖名**；与发现所得分列，刷新不会抹掉 |
| `params` | TEXT NOT NULL DEFAULT '{}' | **用户覆盖参数**（JSON） |
| `upstream_params` | TEXT NOT NULL DEFAULT '{}' | 发现时记下的建议参数 |
| `tokenizer` | TEXT NOT NULL DEFAULT `{"kind":"approx","ratio":1.3}` | 算预算用；默认"估算 ×1.3" |
| `first_seen_at` / `last_seen_at` | INTEGER NOT NULL | 每次刷新更新 `last_seen_at`；**这次没见到就删行** ⇒ 列表 = 上游当前那份 |

### `provider_state`

| 列 | 类型 | 说明 |
|---|---|---|
| `provider` | TEXT PK | 软引用 |
| `last_refresh_at` | INTEGER NOT NULL | "上次拉取：N 分钟前"；`forget_provider` 连它一起清 |

### 索引 / 外键 / 运行时

- 索引：`messages_by_conv(conversation_id, id)`（`id` 是 UUIDv7 ⇒ 同龄兄弟按时间分先后）、`messages_by_parent(parent_id)`（级联 + 数孩子）。
- 外键**只有两条**：`messages.conversation_id → conversations`、`messages.parent_id → messages`，都 CASCADE。`current_leaf` / `agent_id` / `provider` 是**故意不加约束**的软引用。
- 迁移由 `PRAGMA user_version` 驱动（当前 **9**），只追加、不改旧的；`foreign_keys=ON`、`journal_mode=WAL`；写入由一把 `Mutex<Store>` 串行化。

## 对话段 / 摘要（summary）—— 骨架已定，**尚未实现**

> **过程与依据见 `IMPORTANT_DISCUSSION.md`**（那一份记着讨论过程中的**对话原文**与逐步结论；
> 本节只是抽出来的**形状**。两处冲突时以那份为准。）

目标：让"故事梗概"的体积**永远**压在预算内（而不是随回合数线性涨），**原文一律保留**。

### 形状：指针链，不是层级数字

- `messages.summary_id`（可空）：这条消息被收拢进了哪条摘要 —— **多条消息指向同一条摘要**；
- `summaries.parent_summary_id`（可空）：这条摘要又被收拢进了哪条摘要 —— **深度 = 指针链长度**，**不存 `level`**；
- 摘要**不是树的节点** ✗：不往 `messages` 插行、不改 `parent_id`；`AF, G` 是**装配出来的视图**，不是存储形态；
- **硬不变量**：压缩**只允许往 `summaries` 插一行**，绝不插/改/删 `messages`。

### 为什么不需要"区间见证"

因为**分叉只允许出现在尾巴上**（切分支只允许在最新那句 —— 现在的代码就是这样，旧消息上根本没有分支按钮），
而压缩**只吃尾巴之外**（终保护区）⇒ 被压的那段在压缩那一刻**是线性的** ⇒
永不出现"区间里某条消息不在当前路径上"⇒ **纯指针足够**。

### 压缩 = 定稿 ⇒ 可以剪枝

`compact_prunes_siblings`（默认开）：压缩时对区间内每条被覆盖的消息，**只保留它在当前路径上的那个孩子**，
其余整棵子树删掉（复用 `store::delete_subtrees`）。三条硬条件：

1. **先落摘要、再剪枝、同一个事务**（反过来：总结失败 = 白丢数据）；
2. **剪枝前先导出** `data/archive/*.json`（含被剪子树全文）；
3. 剪完在通知里报"剪掉 N 条旧分支（已导出）"，**不许静默**。

### 变量：摘要里**不存**状态

- 摘要正文**只写叙事**；产出里若混进 `<state>` 块，程序**剔除并记日志**
  （三条路同一道闸：初次生成 / 手改 / 重摇）；
- **摘要表上没有状态快照这一列** ✗ —— 状态永远**从原文现演**（`vars::VariableView::from_sources`），
  递推到哪条消息就是哪条的状态。理由：状态是**端点**的属性，不是**段**的属性；存进摘要 = 制造第二个真相来源
  （改一条旧消息它就悄悄过期）。而且变量计算比上下文计算便宜几个数量级 ——
  **现在的实现每一轮都在整条路径上现演一遍**，从来没成为瓶颈；
- 压缩的提示词里**附一份程序算好的状态**（算到**该区间结束**为止，**无论这层摘要有多少层深** ——
  L2 覆盖 1–100 就附 1–100 的状态），并标明「**程序事实，仅供参考，不要写进梗概**」；
- **快照（将来真需要时再上）**要**独立成系统**：`state_snapshots(conversation_id, message_id, values)`，
  始终**从第一条消息推起**，但能接着上一个快照往后推进（快照₂ = 快照₁ + 往后 100 条）；
  它覆盖的消息一经编辑即失效。**现在不做** —— 现演的代价足够低。

### 清除原文（P3 才做）

只有"删原文"会碰变量 ⇒ 先在该处放一条「（已归档）」存根，正文里写 `<state>` 块把净效果落回去 ⇒
再删 ⇒ 用**归档前后 `/variables` 逐键相等**做验收（不需要存快照：删之前现演一遍比对即可）。

清除次序（从最安全最划算开始）：① 清 `reasoning`（实测占**六成字节**，纯展示）→
② 状态存根 + 删原文 → ③ 整段导出 `data/archive/*.json` 再删。

### 装配时的行走算法（细节见 §20）

从**路径根**逐条往前走：消息有 `summary_id` 就取它；若它还有 `parent_summary_id`（P），
**往后瞄一眼** —— 紧接着那串消息的 `summary_id` 是否全 ∈ P 的孩子？是 → **用 P** 并跳过整串；
否 → 用 S 并跳过它覆盖的那串；都没有 → 发原文走一步。
**"跳过"靠继续读 `summary_id` 判断，不存范围**（这就是不需要"两端见证"的原因）。
同一条出站里**混着不同层级是正常的**（每处取当前能取到的最粗），各段互不重叠。

### 压缩粒度 = 对话块（细节见 §16）

**在 `assistant → user` 的交界处切块**（`U1 A1 | U2 U3 A3 | U4 A4 A5`）。四条硬规矩：
① **块绝不被劈开**（章**数块**：凑够 10 个，只在块边界停）；② **最后那个"开着的块"永不压**；
③ 章 = **凑够 10 个块**（**只数块，不按 token** —— §24）；
④ **块是推导出来的，不入库**（不加列/不加表）。**块是压缩单位，不是文本合并** —— `U2 U3` 仍是两条消息两份原文。
块边界同时是**状态的天然检查点**（§15 的 before/after 状态算在块边界上）。

### 压缩提示词的形状（细节见 `IMPORTANT_DISCUSSION.md` §15）

`[system 摘要规则] → [【前情提要】上一段摘要正文] → [原文（含当时 <state>，不含思考）] → [NOW Triggers Compaction + 【程序·状态】before/after]`。
要点：程序注入的块用**与正文不同的标签**（正文本来就有 `<state>`，会撞车）；末尾紧挨着状态再重复一句
"不要写进梗概"；两个状态都**从原始消息现演**（before = 区间前一条，after = 区间末条，层级多深都一样）；
这一整段模板属于 `prompt_version`。

### 只有 Compact，没有"丢"（**铁律**，细节见 `IMPORTANT_DISCUSSION.md` §24）

- **Compact** = 用一条 summary 代表多个 block 里的 message（**唯一的动作**）；
  **Mask / Forbid** = 已有 summary 代表 ⇒ 原文本轮**不发给** Provider，只发 summary。
- **没有"丢/丢弃/Drop"这个动作** ✗ —— 不存在"少发一段没人代表的内容"。上一版本文档里那套
  "超预算就从最老整块开始不发 + 【更早的 N 轮已省略】"是**误加，已作废**。
- **超预算 ⇒ 先 Compact、再发送**；**真压不动**（模型太小等）⇒ **报错原路返回**，不做静默补救。
- 触发：**摘要触发阈值**（用户设，例：1M 模型设 500K）；**停手线 = 阈值 × 0.8**。
- **手动压缩是个功能**：`POST /conversations/{id}/compact`，体 `{ "blocks": N }` ⇒ 202，
  进度/结果并进 `/status` 的 `compact`；手动**不受阈值限制**。
- P2 未上线期间若超预算：**照发全量、原样报错**（过渡状态，不是设计动作）。

口径（§19.C）：**模型上下文** = `models.context_length`（发现值优先）⇒ 回落 `chat.model_context_tokens`；
**预算** = 模型上下文 − 输出预留（`max_output`，缺省 4096）；**触发阈值** = `chat.compact_trigger_tokens`（缺省 预算 × 0.8）。
**单块自己超预算 ⇒ 照发、标 `over_budget`**（让模型自己报错，比我们瞎切好）。边界只看 **`assistant → user`** 一种交界。

### 装配时的 Mask（**已实现**，细节见 §20）

`build_outgoing` 走一遍**行走算法**：路径上被摘要覆盖的那一段**不发明文**，改发摘要正文
（前缀 `【前情提要·N 块】`，角色用 user —— 它不是谁说的话，是程序摆给模型的前情）。
**能取粗的不取精**：一条摘要若有父摘要、且**父的全部孩子就在眼前**，就直接用父、跳过整串；
不够格（盖不全）就退回用它的孩子 —— **宁可用细的，也不许漏内容**（有测试守着这两种情形）。
指针悬空（摘要被删/导入过）当没覆盖处理，照发原文。

### 装配顺序（只在 `build_outgoing` 这一处，仍是唯一拼装路径）

**前缀稳定是硬约束**：任何"按内容变化"的东西（意图分类的结果、角色心理、额外的规则块……）**只能出现在尾部** ——
顺序就是为缓存排的（实测缓存失效只发生在尾巴）；动前缀 = 每轮全价。"只动尾部"这条规矩在 `JEV.md` 里也写着（它是那边的使用前提）。



`[系统 + <state>] [粗梗概] [细梗概] [最近原文] [新消息]` ——
越靠前越稳定，与实测的缓存行为（缓存失效只发生在尾巴）对齐。

### 摘要是派生数据

失效/坏掉只是"退回原文 + 截断"，**绝不阻塞一轮**；留 `provider` / `model` / `prompt_version` / `source_ids`
以便整批重做。

计划中的表（**尚未实现**，形状先钉住）：

| 列 | 类型 | 说明 |
|---|---|---|
| `id` | TEXT PK | UUIDv7 |
| `conversation_id` | TEXT NOT NULL，FK → `conversations(id)` CASCADE | |
| `parent_summary_id` | TEXT（可空），FK → `summaries(id)` | 合并进了哪条更高层的摘要；**深度 = 指针链长度**，不存 level |
| `source_kind` | TEXT NOT NULL，`CHECK(source_kind IN ('message','summary'))` | 成员是消息还是摘要（**同质**：一条摘要的成员不许混） |
| `text` | TEXT NOT NULL | 摘要正文（**只写叙事**；混进 `<state>` 会被剔除并记日志） |
| `tokens` | INTEGER NOT NULL DEFAULT 0 | 估算 token，排预算用 |
| `source_ids` | TEXT（JSON 数组） | 当时到底吃的是什么（消息 id 或摘要 id），供审计与整批重做 |
| `provider` / `model` | TEXT NOT NULL | 谁生成的（模型身份按不变量 5 分两列） |
| `prompt_version` | INTEGER NOT NULL | 模板一改就 +1 ⇒ 能整批重做 |
| `usage` | TEXT（JSON） | 这次摘要自己的花费（形状同 `messages.usage`） |
| `dirty` | INTEGER NOT NULL DEFAULT 0 | 被覆盖的消息被编辑过就置 1；界面显示"已过期 · 重新生成"，装配时照用不误 |
| `created_at` | INTEGER NOT NULL | 毫秒 |

索引：`messages(summary_id)`、`summaries(conversation_id)`、`summaries(parent_summary_id)`。
**成员与覆盖范围不用存**：成员靠 `messages.summary_id` 反查，范围靠它们在当前路径上的位置得出。

### 参数（建议默认）

`compact_blocks = 10`（**单位是块，不是消息**；**没有 token 闸** —— §24）；
章的关闭条件：走到某个块边界时，**凑够 10 个块**。触发：`chat.compact_trigger_tokens`（用户设，默认=预算）；停手线 = 阈值 × 0.8。
预算 = `min(模型上下文 − 输出预留, 用户期望的上下文长度)`；低水位 = 预算 × 0.8；终保护区 = 最近约 10k token。
块本身**不建表**（是当前路径上的视图，按 `assistant → user` 交界切）：需要指一个块时用**两端消息 id**，
块号只用于显示；`summaries` 只加一列 `blocks`（覆盖了几个块）。

### 次序

**P1** 预算 + 截断 + 占用显示（**先写两条测试**：压缩只插不改 / 归档前后 `/variables` 逐键相等）
→ **P1.5** 导出归档 + Fork（**剪枝的前置**）
→ **P2** 章节摘要 + 剪枝
→ **P3** 金字塔 + 清原文。

## 本机环境

- 搜索：`tavily` MCP（`mcp__tavily_*`，配置在 `~/.omp/agent/mcp.json`）——本机直连的几家搜索引擎常被反爬挡掉，优先用它。
- 真机：SM-X810（Galaxy Tab S9+，2560×1600 横屏），**无线 adb**；可 `adb shell input tap X Y` 精确点击、`screencap` 截图。
- Godot：`~/.local/bin/godot`（4.8.dev5），导出模板齐（Android/Web 都在）。
- Web 版服务与 SSL 由用户自己提供。

## 本项目的协作习惯

- **重大设计讨论要留档到 `IMPORTANT_DISCUSSION.md`**：记**对话原文 + 已达成的结论**，**不记推理过程**；AGENTS.md 只留形状与指针。上下文有限，交接就靠这两份。
- 注释、提交信息用**中文**；提交信息写清"为什么"，别只写"改了什么"。
- 改完跑 `cargo test`；**UI 改动必须实跑**（真机或灌事件的无头验证），不要只凭代码断言。
- **后端代码一变就重启后端和 egui 前端**：先 `cargo build`，再重启后端（`./target/debug/server`，唯一权威）
  与前端（`./target/debug/microchat`）——不然你在界面上验的是旧二进制。
- **起它们用 tmux（后台 + 日志落文件）**，别用"受监督进程"那类会一直挂着输出的方式：
  那样一次工具调用会被进程的输出拖住，CLI 卡死、下一步做不了（用户点名要求过）。

  ```bash
  tmux new-session -d -s microchat-server -c <repo> \
      './deprecated/target/debug/server > /tmp/microchat-server.log 2>&1'
  tmux new-session -d -s microchat-gui -c <repo> \
      'DISPLAY=:0 WAYLAND_DISPLAY=wayland-0 XAUTHORITY=/run/user/1000/xauth_UFgDfa \
       XDG_RUNTIME_DIR=/run/user/1000 ./deprecated/target/debug/microchat > /tmp/microchat-gui.log 2>&1'
  ```

  重启 = `tmux kill-session -t <名字>` 再起一次；看日志 = `tail -n 40 /tmp/microchat-*.log`；
  查活着没 = `pgrep -af "deprecated/target/debug/(server|microchat)$"`。**tmux 下崩了没有通知** ⇒ 靠 `pgrep` 与日志。
  **注意 `cargo test` 也会重编** ⇒ 只要那次重启之后又跑过 build/test，就该再重启一次。
  自检（精确）：`ls -l /proc/<pid>/exe` —— 路径后面若带 **` (deleted)`**，说明二进制在进程启动后被替换过 ⇒ 跑的是旧货，重启。
  （别拿 `/proc/<pid>/exe` 去 `stat -c %i` 比 inode：那是魔法符号链接，比出来的数没有意义。）
  前端要用图形环境起：`DISPLAY=:0 WAYLAND_DISPLAY=wayland-0 XAUTHORITY=/run/user/1000/xauth_UFgDfa XDG_RUNTIME_DIR=/run/user/1000 ./deprecated/target/debug/microchat`。
- **先量再断言**：能实测的就不猜（本项目几乎所有关键结论都来自实测）。
- 用户可能**同时在编辑器里改 `frontend/`**：改场景前先读最新文件（并留备份），他的未保存改动优先；提交时别把 `frontend/` 的改动卷进来（除非他让你一起提）。
- **提交由用户指挥**：**不要**每改一点就 `commit` + `push` —— 那样提交记录会碎成一地。做完一段有意义的进度后，先报告，**等用户说"可以提交了"**再提交；推送同理（用户没点名推送就不推）。默认节奏：改代码 → 跑测试 → 实跑验证 → 报告，**停在这里**。
- 提交时只带用户点名的范围：先 `git diff --cached --name-status` 核对，别把 `frontend/` 那边他自己在改的东西卷进来。
- 提交前确认没把 `config/`（里面有 `api_key`）、`data/`、大 APK 带进去。
