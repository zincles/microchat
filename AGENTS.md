# AGENTS.md — 工作须知

microchat：轻量 SillyTavern 替代（RPG 向）。三层，边界要清楚：

| 层 | 位置 | 状态 |
|---|---|---|
| 后端（**唯一权威**） | `src/`（Go + 标准库 + `modernc.org/sqlite`） | **新代码一律写这儿** |
| 后端（已废弃） | `deprecated/`（旧的 Rust + axum 版，含 egui 前端） | **只读参照**：能编译，别再改、别再启动 |

**为什么是 Go**：用户不读 Rust ⇒「出事也能修」那个前提失效；Go 语法少、像 C、标准库自带 HTTP/JSON。
| Godot 前端 | `frontend/` | **用户自己在编辑器里设计**——动手前先问；还没接真实数据 |

## 术语表（一个词只指一件事）

| 词 | **只**指什么 | 别叫 |
|---|---|---|
| **世界状态（state）** | `<state>` 那套：沿当前路径**现演**的键值（可分组为多张表；删除不留痕迹）+ 三层来源（全局 / 本会话 / 生效） | "变量"；孤立地说"状态" |
| **能力（Ability）** | **写死在代码里的一段固定流程**（摘要 / 索引 / 判断 / 角色扮演 / **对话本身**） | "工具"（撞函数调用）、"子 Agent" |
| **Agent** | **一份命名的人格 + 一组能力开关**（`agents.json`）：同一个运行时，只是开关不同 | 拿它指"某个能力" |
| **任务（task）** | **一次有始有终的后台作业**。**凡是会调 LLM 的必定是 Task**（反过来不成立） | — |

三层咬合：**Agent（开关）→ 能力（流程）→ 任务（一次执行）**。"能不能自主选下一步"**不是** Agent 的判据，将来是某个能力（如 `plan`）的事。

## 代码布局

```
src/                       ← 后端（Go）
  main.go                  可执行入口（起服务、读配置、跑迁移）
  internal/
    statelang/             `<state>` 语法的唯一权威（零依赖纯函数 + testdata/cases.json 共享语料）
    state/                 世界状态：分层现演、注入渲染、**唯一**出站拼装
    store/                 SQLite：打开/迁移/读写（migrations/*.sql）
    server/                HTTP：路由、错误体、鉴权（只编排，不含业务）
    model/ config/         纯结构 / `config/` 读写
deprecated/                ← 旧 Rust 版（含 Cargo.toml）；只读参照
frontend/                  ← Godot 4.8
```

## 模块地图：一个模块 = 一份唯一权威 + 一条不变量

**四条"唯一"，就是四条能写成守卫测试的规矩**：

| 模块 | 唯一权威 | 不变量 |
|---|---|---|
| `model` | 纯结构 | 无依赖 |
| `statelang` | `<state>` 语法 | 零依赖纯函数；**解析即验证**；`Cleaned` 里**永不出现标签**（fuzz 守着）|
| `store` | **只有它碰 SQL** | 正文/提示词原样存档；变量不落库；树在 `parent_id`；生成中的回复不在库里 |
| `config` | `config/` 四个文件 | 可缺失；整体重写；密钥不回显 |
| `registry` | 模型口径 | 身份 `(provider, upstream_id)`；发现与覆盖分列，刷新不动用户列 |
| `providers` | **只有它碰网络** | 只走成熟库；超时必配 |
| `state` | **只有它拼出站文本** | 发出去的剔除标签、改注入当前状态；底子分层由提示词来源决定 |
| `blocks` | 块切分 | 块是推导的、不入库；不许劈开；开着的那块永不压 |
| `compact` | 摘要唯一入口 | **只往 summaries 插行** |
| `abilities` | 能力身份（枚举） | 产出不进历史/树/变量；失败不阻塞一轮 |
| `template` | `{{…}}` | 白名单 = 枚举；只扫一遍；会话 Agent 的提示词永不替换 |
| `chat` | 一轮生成怎么跑 | 拿到整段才 INSERT |
| `turn` | 这一轮的流细节 | 增量什么都不算；游标读不消费 |
| `task` | 作业的身份 + 生死 | Drop 兜底；绝不留下僵尸条目 |
| `server` | **只有它碰 HTTP** | 错误体固定；只编排不含业务 |

依赖方向单向：`server → chat/compact/abilities → state（含 statelang）/blocks → registry/providers/config → store → model`。
`turn`/`task` 是横切（谁都能挂号，不被依赖）。**Go 用编译器强制这张图**。

## 不变量（破坏了会静默出错）

1. **存储是 SQLite**（不选全 JSON：崩溃安全、并发、查询都白拿）：`data/microchat.db`。手写配置只在 `config/`（**严格 JSON**：注释与尾逗号都报错——程序会整体重写）。两者路径**相对工作目录**，可用 `MICROCHAT_CONFIG_DIR` / `MICROCHAT_DATA_DIR` 覆盖。界面偏好另存 `~/.config/microchat/frontend.json`（不同机制，别混）。
2. **正文与提示词都是存档**：`<state>` 块原样留在消息正文**和 system prompt** 里；**发给模型的文本一律剔除标签**，改注入当前状态表。唯一出口 `state.BuildOutgoing()`——不许写第二条拼装路径。
3. **世界状态不落库**：世界状态**每次现算**（`state.FromSources`）：先扫生效提示词里的块（**底子**），再按**当前路径**顺序扫正文里的块，然后 fold。每条操作带 `message_id`，"哪句话带来的状态"追得回来。**没有派生表** ⇒ 编辑/删除消息、改提示词、换分支都不需要"重算"（换分支只要重拉 `/conversations/{id}/state`）。
   底子的**来源**决定它算哪一层：用 agent 的 ⇒ **全局**（同一 agent 的会话共享）；会话自己写了 ⇒ **本会话**。
   **删除不留痕迹**：`delete(键)` 与空值 `键 =` 都只是从**本层**拿掉 ⇒ 删底子里的键，值会从底子漏回来（"删"只作用于自己那一层；不想被删的键别写底子，写进第一条消息）。
   生效提示词由**一处**解析（会话覆盖 → agent 的 → 内置默认），出站 / 底子 / 界面共用它。
4. **消息是一棵树**：`messages.parent_id`（自引用 `CASCADE`）+ `conversations.current_leaf`。整条对话 = 从 `current_leaf` 回溯到根。**兄弟就是分支**：
   - **重新发送 = 再长一个兄弟**（旧的留着）；尾条是用户消息时照它生成；
   - **切分支只许切到尾巴的兄弟**（否则世界状态会跟着倒退）；
   - **删除只允许删整棵子树**；leaf 若落在被删子树里，退到**上文下还活着的最新一个孩子**（不是退到上文——那会让界面像"整条分支都没了"）；「删除全部」= 清掉一组兄弟；
   - **生成中的回复不在库里**：受理时先把 id 算好发给客户端，等**整段**拿到才用这个 id 与当时捕获的上文 INSERT ⇒ 停止/失败/被杀都不留半条，也没有"清理占位"要维护。
   **别按 rowid 或 id 排消息**；`rowid` 只当"同龄兄弟谁先谁后"的顺序。
5. **模型身份 = `(provider, upstream_id)`**；显示名三级回退（用户覆盖 → 上游名 → prettify）**只在后端**做。
   新建会话的 agent 取 `agents.json` 的 `default_agent`（空串/缺失都算没配 ⇒ 内置默认）。**agent 的 id** 新建时由后端生成；改名走 `PATCH /agents/{id}` 的 `new_id`——一次把 `default_agent` 与所有会话的引用搬过去（软引用无外键，找不到只会静默回空提示词，所以必须由这一处维护）。
6. **`config/` 与 `data/` 永不入库**（端口口令、连接信息、密钥、存档）。默认值全在代码里，文件缺失也能跑。`.gitignore` 只放行 Godot 项目的非缓存部分。
7. **HTTP API 看下面那节**。改了接口就跑 `cd src && go test ./...` + `python3 scripts/go-parity.py`（与旧版逐字节比）。`scripts/api-audit.py` 是**旧时代的闸**，搬完最后一批路由前还有参照价值。

## 常用命令

```bash
cd src && go test ./...                                   # 后端测试
cd src && go build -o /tmp/microchat-go . && /tmp/microchat-go -addr 127.0.0.1:8787 -data ../data

cd deprecated && cargo test                               # 旧版（参照，别再改）
cd deprecated && ./target/debug/server                    # 旧后端（只在对账时临时起）

cd frontend
godot --headless --path . --quit-after 3                   # 语法+运行检查
godot --headless --script /tmp/x.gd                        # 灌事件跑帧
ANDROID_HOME=~/Android/Sdk godot --headless --path . --export-debug "Android" /tmp/x.apk
```

## `<state>` 的语法（记事板，不是脚本）

```
<state 玩家状态>      # 可选的表名；不写就是未命名表
AA = 123456          # 赋值（值一律字符串）
B = 234; C = 落石     # `;` 当换行（一行一个变量）
delete(AA)           # 删除
</state>
```

- **每种写法只有一种规范形态**：赋值 `键 = 值`（`set` 已砍）· 删除 `delete(键)`（`del …` 全砍）。砍掉的写法各给一条说得清的报错，不做兼容——LLM 写十次要能十次写对。
- **空值就是清掉**：`键 =`（trim 后为空）与 `delete(键)` 同效 ⇒ **存不下空串**。解析照实报 `set`（值是空串），动手的是**计算**。
- 块可在**任意位置**；无运算、无变量作右值；值里可以有 `=`（只在第一个处切）、**不能有空格**（⇒ `A=1 B=2` 报错，而不是静默删空格）。
- 解析是**行式**的（无文法）；坏行进诊断（带行号，整体按行号升序），整块仍从正文剔除。
- 未命名的键与命名的键分属不同的表；**算完为空的表自动消失**；没有「表级删除」。
- **账本 ≠ 墓碑**：接口里的操作流水仍留删除那一行（「哪句话改了什么」要追得回来），但算当前值时它不留痕迹。

## 配置文件长什么样

`config/` 里是**严格 JSON**（程序整体重写），都可缺失。文件名一律 `.json`：

- `config.json` — `{ "server": { "port": 8787, "auth_token": "可选" }, "defaults": { "provider": "dummy", "model": "dummy", "agent": "default" }, "chat": { "model_context_tokens": 131072, "compact_trigger_tokens": null } }`
- `providers.json` — `{ "providers": [ { "id": "dummy", "kind": "dummy" }, { "id": "openrouter", "base_url": "https://openrouter.ai/api/v1", "headers": {…}, "api_key": "sk-…", "timeouts": { "connect_seconds": 15, "total_seconds": 300 } } ] }`
  **密钥就写在这一条里**（空串 = 没配）：整个 `config/` 在忽略范围内；接口一律不回显（只回 `has_key`），调试页读它时先打码；写回权限收紧到 0600。
- `agents.json` — `{ "default_agent": "跑团", "agents": [ { "id": "跑团", "name": "跑团主持人", "system_prompt": "你是跑团主持人。<state>季节 = 初冬</state>" } ] }`
- `abilities.json` — 能力**只能覆盖、不能造**：`{ "abilities": { "compact": { "system_prompt": "…（留空=用内置）…", "provider": null, "model": null } } }`（未知 id ⇒ 400）。

## HTTP API（客户端契约）

**唯一权威是 Go 版的路由表**（`src/internal/server/`）。全部挂在 **`/api/v1`** 下，请求与响应都是 JSON。

- 配了口令时（`config.json` 的 `server.auth_token`）**所有**接口都要 `Authorization: Bearer <token>`（`/health` 也不例外）。
- 错误体固定 `{"error":{"code","message"}}`，`code` ∈ `invalid` / `not_found` / `conflict` / `unauthorized` / `upstream` / `internal`——客户端按 `code` 分支，别匹配文案。
- **405 由框架自己回**（路径在、方法不对），不带上面的体，但带 `allow:` 头。
- CORS 全开；**默认不设 auth_token**，所以别把端口暴露到公网。

### 会话与消息

| 方法 | 路径 | 请求体 | 响应 | 说明 |
|---|---|---|---|---|
| GET | `/conversations` | — | `[ConversationView]` | 每项 = 会话 + `turn`（左栏据此标"生成中"）|
| POST | `/conversations` | `CreateConversationReq` | `Conversation` · 201 | 省略字段时取 `config.json` 的 `defaults` |
| PATCH | `/conversations/{id}` | `UpdateConversationReq` | `Conversation` | 标题 / 模型 / agent / **切分支**（`current_leaf`）|
| DELETE | `/conversations/{id}` | — | 204 | 不存在 → 404 |
| GET | `/conversations/{id}/messages` | — | `[Message]` | **按树上的当前路径**，不是 rowid |
| POST | `/conversations/{id}/messages` | `SendReq` | `TurnAccepted` · **202** | 落用户消息 + 开工；不含回复正文 |
| PATCH | `/conversations/{id}/messages/{mid}` | `EditMessageReq` | `Message` | 改正文 = 重写存档（世界状态随之现演）|
| DELETE | `/conversations/{id}/messages/{mid}` | — | `{"deleted": n}` | 删**整棵子树**；leaf 退到还活着的最新兄弟 |
| DELETE | `/conversations/{id}/messages/{mid}/siblings` | — | `{"deleted": n}` | 「删除全部」|
| POST | `/conversations/{id}/resend` | — | `TurnAccepted` · **202** | 尾条是助手 ⇒ 再长一个兄弟；是用户 ⇒ 照它重发 |
| GET | `/conversations/{id}/branches` | — | `{消息id: BranchInfo}` | 每条在同龄兄弟里第几/共几（`‹ 2/3 ›`）|
| GET | `/conversations/{id}/state` | — | `StateView` | `global` / `session` / `global_values` / `effective` / `tables`，**每次现算** |
| GET | `/conversations/{id}/outgoing` | — | `[Outgoing]` | "下次真会发出去的东西"（标签已剔除、状态已注入）|
| POST | `/conversations/{id}/compact` | `{blocks: N}` | `CompactStatus` · **202** | 手动压缩；同一会话同时只允许一个 |
| GET | `/conversations/{id}/summaries` | — | `[SummaryView]` | `members`、`first/last_message_id` 现算；`members == 0` ⇒ 孤立摘要 |
| GET | `/conversations/{id}/export` | — | `ConversationExport` | 自包含导出（全部消息含分支 + 摘要 + 现演状态 + `current_path`）|
| POST | `/conversations/{id}/archive` | — | `ArchiveReceipt` | 写 `data/archive/<会话>-<时间戳>.json`（**剪枝前的必做动作**）|
| POST | `/conversations/{id}/fork` | — | `Conversation` · 201 | 复制新会话：当前路径 + 尾层兄弟（含子树）+ 摘要行 |
| GET | `/conversations/{id}/context` | — | `ContextUsage` | 只有数字：`used_tokens`（估算）/ `budget_tokens` / `trigger_tokens` / `remaining_tokens` / `ctx_len` / `max_output` / `ratio` / `estimated` / `last_prompt_tokens` / `over_budget` |
| GET | `/conversations/{id}/status` | — | `TurnStatus` | `idle`/`pending`/`streaming`/`error` + `message_id` + `elapsed_ms` + `chars` + `thinking_chars` + `error` |
| GET | `/conversations/{id}/turn/text?from=N&think_from=M` | — | `StreamSlice` | 流式增量的**游标读**（正文与思考各一条游标，`from` = 第几个字符）：只服务动画 |
| POST | `/conversations/{id}/stop` | — | `{"stopped": bool}` | **幂等**：没在跑也 200（`false`）|

同一会话在跑时再发 → **409**。`TurnAccepted` = `{user?, backend, turn}`：`turn.message_id` 是**这条回复的 id**（受理时定好，那会儿还没进库）。

### statelang（给外部工具）

| 方法 | 路径 | 请求体 | 响应 | 说明 |
|---|---|---|---|---|
| POST | `/statelang` | `{"text": "任意文本"}` | `{"tables": {表名: {键: 值}}, "statements": [...], "diagnostics": [...]}` | **解析 + 计算**一段文本里的全部 `<state>` 块：不起对话、不落库。`tables` = 算完的值（删除生效、后写覆盖、空表消失；未命名表用**空串**作键）；`statements` = 读出来的操作（按出现顺序，删除未生效）；`diagnostics` 带行号。缺 `text` ⇒ 422 |

### provider / 模型 / agent

| 方法 | 路径 | 请求体 | 响应 | 说明 |
|---|---|---|---|---|
| GET | `/providers` | — | `[ProviderView]` | **含 `has_key`，绝不含密钥内容** |
| POST | `/providers` | `CreateProviderReq` | `ProviderView` · 201 | `api_key` 写进 `providers.json`；重名 → 409 |
| PATCH | `/providers/{id}` | `UpdateProviderReq` | `ProviderView` | `api_key`：`None` = 不动，`""` = 清除 |
| DELETE | `/providers/{id}` | — | 204 | 密钥随记录一起没 |
| POST | `/providers/{id}/refresh` | — | `ProviderView` | **POST**（会写发现态）：拉 `/models` 并落库 |
| DELETE | `/providers/{id}/models` | — | `{"deleted": n}` | 清掉已发现模型；不动配置与历史会话 |
| POST | `/models/probe` | `ProbeReq` | `ProbeResult` | 一次性 url+key 试拉，**不落库** |
| GET | `/models` | — | `[ModelListItem]` | 跨 provider 拍平（"渠道 / 模型"下拉用）|
| PATCH | `/models` | `{provider, upstream_id, context_override}` | `ModelView` | 设/清上下文覆盖（`null` = 清）。**刷新永不覆盖用户列** |
| GET | `/agents` | — | `AgentsConfig` | 生效列表（含内置默认 agent）|
| POST | `/agents` | `CreateAgentReq` | `Agent` · 201 | 只收 `name` + `system_prompt`；id 由后端生成 |
| PATCH | `/agents/{id}` | `UpdateAgentReq` | `Agent` | `new_id` = 重命名（搬 `default_agent` 与会话引用）|
| DELETE | `/agents/{id}` | — | 204 | 内置默认 agent 不可删 |
| GET | `/abilities` | — | `[AbilityView]` | 清单 + 生效模板/渠道/模型 + 版本短号 |
| PUT | `/abilities` | `AbilitiesConfig` | `[AbilityView]` | 整段替换；**未知 id ⇒ 400** |
| GET | `/config/chat` | — | `ChatConfig` | `config.json` 的 chat 段（不含密钥）|
| PUT | `/config/chat` | `ChatConfig` | `ChatConfig` | **整段替换** chat（其余段原样保留）|

### 运维

| 方法 | 路径 | 响应 | 说明 |
|---|---|---|---|
| GET | `/health` | `{"status","version"}` | 探针；**也过鉴权** |
| GET | `/debug/state` | `DebugState` | 后端自述（结构上就没有密钥字段）|
| GET | `/debug/last-payload` | `LastPayload` | **最近一次真正发给上游的请求体**（内存一份，覆盖式；没发过 = `null`）。dummy 也会拼一份"本来会发的" |
| GET | `/debug/file/{name}` | `RawFile` | 白名单 `config.json`/`providers.json`/`agents.json`；`api_key` 先打码 |
| GET | `/tasks` | `TaskBoard` | `{running, tasks, now}`：正在跑的在最前，已结束留最近 60 条 |

## 一轮生成（202 + 轮询）

发送不再"一次请求等到整段回复"：**受理与生成分开**——客户端要答"在不在跑""跑了多久""怎么停"，这三个答案都不该绑死在一次请求上。

```text
  idle ──POST /messages 或 /resend──► pending ──（流式才进）──► streaming
    ▲                                   │                         │
    │                                   └──── 成功 ──► idle ◄─────┘
    │                                             失败 ──► error
    └── POST /stop ────────────────────────────────────────────┘
```

- 状态在**进程内**（不进库）：后端一重启就没有生成在跑，回到 `idle` 是诚实的；
- 每次受理：**先算好回复 id → 登记 → spawn**。`begin` 是唯一并发闸门 ⇒ 两个请求挤进来只有一个 202，另一个 409（**不排队**：排队会让"按了发送却什么都没发生"难以解释）；
- 后台任务落库前核对令牌：被 `stop` 顶掉的旧任务回来时令牌对不上，结果直接丢掉；
- `stop` = 摘登记 + abort 后台任务（别让上游白跑完）。**库里没有要收拾的东西**。

**流式**：增量**什么都不算**（不进库、不进树、不参与世界状态与出站计算）——落库只认流结束后那一整段。所以流断了、被停了、上游中途报错，档案永远干净。

- 上游用现成的 SSE 库，不手搓 `data:` 分帧；
- 增量进这一轮的缓冲区，顺手把 `pending` 抬成 `streaming`；令牌对不上的字节直接丢；
- **思考流单独一条缓冲**：推理型模型先吐 `reasoning`/`reasoning_content` 再吐 `content`（两家字段名不同，两个都认）；思考只服务动画、**不参与任何计算**，但会落档（`messages.reasoning`；`store_reasoning: false` 则只走动画）；
- 给前端的是**游标读**（`?from=N&think_from=M`，正文与思考各一条）：多个客户端与断线重连各拿各的，互不偷；`done` 表示这轮结束，结束后仍可补拉尾巴。

## 后台任务 / 压缩 / 能力

**任务**：一个登记表 —— 前台一轮生成、一次压缩、一次刷新模型都挂号。`TaskGuard`（RAII）兜底：忘了收或 panic 由 Drop 记成"中断" ⇒ 绝不留僵尸条目。
三份状态**各管一段、不许互为镜像**：`task` = 身份 + 生死；`turn` = 流细节（几个字、取消）；`compact` = 压缩自己的细节。

**压缩**：机制与策略两层。机制 = `summarize_span`（给它一段，两端用 id 指、**可以是 message 也可以是 summary** ⇒ 二次压缩就是喂 summary id）→ 拼材料 → 叫能力 → 过闸（剔 `<state>`）→ 落库（单事务）。策略 = "从第一条没被覆盖的消息起，取最老的 N 个**已闭合块**"（手动按钮的语义）。
谁也**不许**绕过这里自己拼摘要请求。不持锁跨 await：取料在锁里、调用在锁外、落库再进锁。

**能力**：身份写死在代码里（枚举变体），`abilities.json` 只能**覆盖**模板/渠道/模型，不能造新的。
与会话 Agent 的三条硬边界：① 产出永不进历史/树/变量（落库由调用方决定，如摘要走 `record_summary`）；② 提示词永不进会话的系统提示词；③ **失败不阻塞任何一轮**。
`prompt_version` = 生效模板的 64 位哈希（改一个字就变，别手写版本号）。
**Agent 的导出导入（卡带）**：导出**默认不带密钥**（`api_key` 置空，要带得显式勾）；**导入不需要「重算世界状态」**—— 变量不落库、每轮现演，换了底子状态自动就是新样子。要打包的就是 `agents.json` / `abilities.json` 这两个文件。
**占位符**（`{{…}}`）：白名单 = 枚举（`{{system_time}}` / `{{state_before}}` / `{{state_after}}` / `{{range}}` / `{{blocks}}`）；**不认识的 `{{foo}}` 原样留着**；**只扫一遍**（替换进去的值不再当模板扫）；**会话 Agent 的提示词永不替换**（它一变，前缀缓存每轮全废）。

## 数据模型

**两版各一份、逐字一致**：活的是 `src/internal/store/migrations/*.sql`（由 `deprecated/src/store.rs` 的 `MIGRATIONS` 生成）。

**四张表，全是 TEXT id（UUIDv7）+ 毫秒整数时间戳。**

### `conversations`

`id` · `title`（首句自动生成；空串 = 还没起名）· `system_prompt`（**空串 = 没覆盖** ⇒ 底子算全局）· `provider` / `model` / `agent_id`（都是**软引用**，无外键）· `current_leaf` · `created_at` / `updated_at`（界面按 `updated_at` 排序）

### `messages` —— 一条消息，**同时是树的一个节点**

`id`（生成回复时**受理那一刻**就算好）· `conversation_id`（FK CASCADE）· `role`（`CHECK IN ('user','assistant')`）· `content`（**存档本体**，`<state>` 块原样留着）· `parent_id`（FK CASCADE；**树就在这一列**）· `created_at`
后四列**只服务显示**（出站/状态/分支/编辑一律不看）：`reasoning` · `reasoning_ms`（受理 → 第一段正文）· `duration_ms` · `usage`（归一化后的 JSON）

### `models` —— 发现所得 + 用户覆盖（PK `(provider, upstream_id)`）

`upstream_name` / `owned_by` / `context_length` / `max_output` / `upstream_params` / `first_seen_at` / `last_seen_at`（**这次没见到就删行** ⇒ 列表 = 上游当前那份）
**用户列**（刷新永不动）：`display_name` · `params` · `context_override` · `tokenizer`（缺省 `{"kind":"approx","ratio":1.3}`）

### `summaries` + `provider_state`

`summaries`：`id` · `conversation_id` · `parent_summary_id`（**深度 = 指针链长度**，不存 level）· `source_kind`（`message`/`summary`，**同质**）· `text`（只写叙事；混进 `<state>` 会被剔除并记日志）· `blocks` · `tokens` · `source_ids`（当时吃的是什么，供审计与重做）· `provider`/`model`/`prompt_version`/`usage` · `dirty`（被覆盖的消息被编辑过 ⇒ 1；界面显示"已过期"，**装配时照用**）· `created_at`
`provider_state`：`provider` · `last_refresh_at`（"上次拉取：N 分钟前"）

**索引/外键/运行时**：索引 `messages_by_conv(conversation_id, id)`（UUIDv7 ⇒ 同龄兄弟按时间分先后）、`messages_by_parent`、`messages(summary_id)`、`summaries(conversation_id/parent_summary_id)`。外键**只有两条**（`messages` 的两个），都 CASCADE；`current_leaf`/`agent_id`/`provider` 是故意不加约束的软引用。迁移由 `PRAGMA user_version` 驱动（当前 **11**），只追加；`foreign_keys=ON`、`journal_mode=WAL`；写入由一把锁串行化。

## 摘要 / 压缩的设计（机制已实现；剪枝与清原文未做）

- **指针链，不是层级数字**：摘要**不是树的节点**（不往 `messages` 插行、不改 `parent_id`）；硬不变量：**压缩只往 `summaries` 插一行**，绝不插/改/删 `messages`。
- **为什么不需要"区间见证"**：分叉只允许出现在尾巴上，而压缩只吃尾巴之外 ⇒ 被压那段在压缩那一刻**是线性的** ⇒ 纯指针足够。
- **装配时的行走**（`state.BuildOutgoing`）：从路径根逐条往前走 —— 有 `summary_id` 就取它；若有父摘要、且**父的全部孩子就在眼前**（一个不缺）⇒ 用父并跳过整串；否则用细的、跳过它覆盖的那串；都没有 ⇒ 发原文。"跳过"靠继续读 `summary_id` 判断，**不存范围**。**能取粗的不取精，但宁可用细的也不许漏内容**。
- **MASK（术语）**：有摘要覆盖的那一段，装配时**不发明文**、只发摘要的正文 —— 存放处一个字节都不动，被"遮掉"的只是**这一次请求**。
- **只有 Compact，没有"丢"**（铁律）：不存在"少发一段没人代表的内容"。超预算 ⇒ **先压再发**；真压不动 ⇒ **报错原路返回**，不做静默补救。
- **粒度 = 对话块**：在 `assistant → user` 交界处切（`U1 A1 | U2 U3 A3 | U4 A4 A5`）。① 块绝不被劈开；② **最后那个开着的块永不压**；③ 章 = 凑够 N 个块（只数块，不按 token）；④ 块是**推导**的、不入库（需要指一个块时用两端消息 id）。
- **摘要里不存状态**：状态是**端点**的属性，不是段的属性 —— 存进去就是第二个真相来源，改一条旧消息它就过期。提示词里只**附**一份程序算好的状态（算到区间末），标明「程序事实，仅供参考，不要写进梗概」。
- **真需要快照时它得是独立系统**（`state_snapshots`：从第一条推起、能接着上一个快照往后推进；覆盖的消息一经编辑即失效）。**现在不做** —— 现演的代价足够低。
- **参数**：`compact_blocks = 10`（单位是块）；预算 = 模型上下文 − 输出预留（缺省 4096）；触发阈值用户设（缺省 = 预算）；停手线 = 阈值 × 0.8；终保护区 = 最近约 10k token。
- **剪枝**（未做；前置已就绪）：压缩即定稿 ⇒ 只保留当前路径上的那个孩子，其余子树删掉。三条硬条件：① **先落摘要、再剪枝、同一事务**；② **剪枝前先导出** `data/archive/*.json`；③ 报"剪掉 N 条旧分支（已导出）"，**不许静默**。
- **次序**：P1 预算 + 占用 ✅ · P1.5 导出/归档/Fork ✅ · P2 摘要 ✅（剪枝 ✗）· P3 金字塔 ⚪ 一半（指针链 + 取粗的装配都在跑）· 清原文 ✗。

## 与上游通信

**只许用成熟的外部库**：HTTP 一律 `reqwest`（旧版）/ 标准库 + 成熟 SSE 库（Go）；数据结构用 serde/encoding-json 映射成纯数据形状。**不自己写传输层、不自己写协议解析**（手写协议解析是 bug 温床）。
超时别忘：`providers.json` 的 `timeouts`（缺省 connect 15s / total 300s）。**不设总超时 = 上游卡住、界面转一辈子**。
`usage` 各家的字段名不一（缓存就有 `prompt_tokens_details.cached_tokens` 与 `prompt_cache_hit_tokens` 两种），**归一化只有一处**；前端只管读 `cached_tokens` 这些键。成本算不了：API 不返回 cost。

## 决策模型（JEV 一类）：**不是聊天模型**

`typesafe/jev` 那类是**决策模型**（状态 + 类型化问题 → 概率），不生成文本、不能驱动会话、也不在 `GET /models` 的发现列表里（下拉里找不到它**不是**列表过期）。要用得加第三种 provider kind。规划中的用途与四条规矩（只动尾部 / 失败降级 / 产出不进历史与变量 / 阈值实测校准）见 **`JEV.md`**。

## 界面该长什么样（**口径**；Godot 版照此对齐，目前尚未实现）

- 左栏：`＋ 新建对话` + 会话列表 + 底部 `⟳ 刷新`（一把把会话/消息/状态/分支/模型/agent 全拉一遍）。正在生成的会话标题后面挂「 · 生成中…」。
- 会话视图：顶部一行**上下文占用**（`上下文 12.3k（上轮实测 11.9k）/ 40k`；超 80% 黄、超触发阈值红）→ 系统提示词块（可展开）→ 消息列表（署名 + `编辑 / 复制 / 删除`）→ 底栏输入。
  - 助手署名 = **该会话 agent 的名字**（不是"助手"）；输入栏跟着软键盘浮。
  - **分支操作只出现在最后一条消息上**：`‹ 2/3 ›`（切候选回复）、`删除全部`、`重新发送`。
  - **生成中那条回复**是界面按状态**合成**的气泡（库里还没有它）：转圈 + "正在生成… 12.3s" + 「停止」；落库后同 id 的真实消息出现，气泡自然消失。发送按钮显示"等待…"；每 300ms 轮询 `/status`，收到 `idle`/`error` 才一次性重拉。
- **卡片脚注**：`3.2s · 上行 1654 tok（缓存 1408 tok · 85%）· 下行 62 tok（思考 32 tok，含在内）`——只在有数据时显示。**单位是 token**（上游 `usage` 报的）；生成中气泡上「思考中… N 字」是**字符数**（流式帧里没有 token 数）——两处别混。`reasoning_tokens` 是 `completion_tokens` 的子集，不是另加。
- **思考**默认折叠成「思考（2.1s）」——放的是**思考用时**（受理 → 第一段正文），不是字数。
- 底部一排：`归档`（写 `data/archive/*.json`）、`压缩 [N] 个块` + `开始`（202 受理，底栏报结果）、`复制会话`（Fork 并选中它）。
- 设置六页：**连接 / Agent / 能力 / 模型与渠道 / 前端设置 / 关于**。底栏常驻"**已连接后端 vX**"，后面跟 `·` 和最近一次动作的结果——两者**互不顶替**；刷新失败要带状态码与 `code`。
  「模型与渠道」页：顶部**服务端上下文口径**（`模型上下文` 兜底 + `摘要触发阈值`）；每个渠道一行（`获取模型` / `删除全部模型` / `编辑`）；下面逐个模型列上下文，可填**覆盖值**（留空 = 清掉）。优先顺序：**覆盖 > 上游发现 > 配置兜底**，只许一处解析。
- 渠道与模型是**一个下拉**（`渠道 / 模型`，`GET /models` 拍平给的就是这个形状）；provider 的 `name` 缺省回退 `id`。
- **设置页要的数据只从一处发**（连上 / 点 ⟳ / 进设置页都走它）——曾经两处各写一份，新加的字段只进了一条路 ⇒ 设置页永远"加载中…"（真发生过）。

## 返回键 / 退出（方案已定，尚未开工）

两个开关**各管一扇门、互不代管**（都在 `SceneTree` 上，默认都 `true`）：

| 开关 | 管哪扇门 | 通知 | 信号 |
|---|---|---|---|
| `auto_accept_quit` | 关窗请求（桌面 ×、Web 关窗）| `NOTIFICATION_WM_CLOSE_REQUEST` = 1006 | `Window.close_requested` |
| `quit_on_go_back` | 安卓返回键 / 手势 | `NOTIFICATION_WM_GO_BACK_REQUEST` = 1007 | `Window.go_back_requested` |

引擎里的顺序：根窗口先 `_propagate_window_notification()` → **全树节点的 `_notification` 先收到** → 再 `emit_signal` → SceneTree 按开关决定 `_quit`。即"通知一定先到，自动退出是之后才判的"。`get_tree().quit()` **不走这条路**（直接 `_quit = true`、不发通知）。
**当前设定**（`frontend/project.godot`）：`quit_on_go_back=false`（返回键留给面板栈），`auto_accept_quit` 保持默认 `true`。
**还没做**：① 返回栈（`_on_back()` 一处收口；安卓接 `go_back_requested`、桌面/Web 接 `ui_cancel`；栈空才真退）；② 安卓双击退出（第一次提示，约 2 秒内再来一次才 `quit()`）；③ 安卓"从最近任务划掉" = **进程被杀**，任何开关都拦不到 ⇒ 该落盘的写在 `NOTIFICATION_APPLICATION_PAUSED` 里落。
**坑**：返回栈接上之前，安卓按返回键**什么都不发生**。4.8.dev5 实测"只关 `auto_accept_quit`、返回键那条路也不退" ⇒ **别依赖实现细节**，要拦哪条路就显式关哪条路的开关。

## 踩过的坑（都是实测出来的，改代码前扫一眼）

- **`reqwest` 默认不设总超时**：上游不回应就永远挂着（界面上只有一个转不完的"等待…"）。超时写在 `providers.json`。
- **删掉"当前那条回复"时 leaf 不能退到上文**：那样路径上一条回复都没有，看起来像"整条分支被删了"；再点重新发送又多一条。正解 = 退到上文下**还活着的最新兄弟**。
- **配置文件里没有注释**：程序整体重写，JSONC 会给人"写了也会丢"的假象。严格 JSON，写坏了报 `Expecting property name…`。
- **批量改代码时逐文件落盘**：把 `write` 放在脚本末尾，中途任何断言失败都会让整批改动一起丢。
- **Go 的 `encoding/json` 默认把 `<` `>` `&` 转义成 `\u003c`**（本项目满地 LaTeX）⇒ 关掉 `SetEscapeHTML`；它的 `Encoder.Encode` 还会补一个 `\n`（axum 不补）⇒ 对账要连字节一起比。
- **Go 的包名会被参数名遮蔽**（`func f(model string)` 里的 `model` 盖住 `model` 包）；**`sync.Mutex` 不可重入**（持锁的方法里别再调公开方法）。
- **正则改源码要小心字面量 `\n`**：源码里 `\n` 是两个字符，`\b`/lookbehind 都会失灵，贪吃匹配会把整行切坏。
- **Godot `offset_transform_position` 的单位是 ui，不是像素**：安卓键盘高度是像素 ⇒ 必须乘 `视图高/窗口高`（实测 775px×2.22=1722px > 屏高 1600，直接飞出屏幕）。**两个开关默认是反的**：`offset_transform_enabled=false`、`offset_transform_visual_only=true`；只打开 enabled ⇒ "看得见、点不到"。
- **Web 上引擎读不到软键盘**：`virtual_keyboard_get_height()` 是基类桩（返回 0 + 告警），而 `has_feature(FEATURE_VIRTUAL_KEYBOARD)` **却是 true** ⇒ Web 只能读 `window.visualViewport`（`JavaScriptBridge`，且要用 `Engine.get_singleton` 拿，直接写类名会让桌面构建解析失败）。
- **Web 没有系统字体 fallback** ⇒ CJK 必须把字体**打进项目**并挂 `theme/custom_font` / `FontFile.Fallbacks`，否则全是白框（桌面和安卓看不出来）。
- **Godot 的"嵌套太深"多半是没有职责的容器**；padding 写进已有控件的 `StyleBoxFlat.content_margin`，**别为留白加节点**。抽子场景**不减少运行时深度**。
- **`--check-only --script` 看不到 autoload / 全局类名**（会误报 `Identifier not found`）；要检查就 `--quit-after 3` 实跑。
- **Godot 里没有 `[display] window/stretch`** 时视图坐标 ≠ 设计坐标（无头/手机上会得到 64×64 之类的怪尺寸，界面按 1:1 像素渲染，看起来只有一半大）。
- **一次失败的 `git commit`（"无文件可提交"）会把此前 staged 的东西留在索引里** ⇒ 下次提交把它们卷走（本项目真发生过）。**提交前先 `git diff --cached --name-status` 核对范围**；**`git commit` 不带路径 = 提交索引里的一切** ⇒ 只提自己那几样就给路径（`git commit <文件…>`，它记录这些文件的工作区内容，索引里别的一律不动）。改错了用 `git restore --source=HEAD~1 --staged <文件>` + `--amend` 修，**别用 `--worktree`**。

## 本机环境

- 搜索：`tavily` MCP（`mcp__tavily_*`，配置在 `~/.omp/agent/mcp.json`）——直连的几家搜索引擎常被反爬挡掉，优先用它。
- 真机：SM-X810（Galaxy Tab S9+，2560×1600 横屏），**无线 adb**；可 `adb shell input tap X Y`、`screencap` 截图。
- Godot：`~/.local/bin/godot`（4.8.dev5），导出模板齐（Android/Web 都在）。Web 版服务与 SSL 由用户自己提供。

## 本项目的协作习惯

- **提交由用户指挥**：做完一段有意义的进度 → 跑测试 → 实跑验证 → 报告，**停在这儿**等他说"可以提交了"。推送同理。
- 提交时只带用户点名的范围；**别把 `frontend/`**（用户自己在编辑器里改的）**卷进无关提交**；别把 `config/`、`data/`、大 APK 带进去。
- 注释、提交信息用**中文**；提交信息写清"**为什么**"，别只写"改了什么"。
- 改完跑 `cd src && go test ./...`；**UI 改动必须实跑**（真机或灌事件的无头验证），不要只凭代码断言。搬 Go 期间每搬一条路由就跑 `scripts/go-parity.py`（同数据、逐字节比）。
- **后端代码一变就重启后端**：先 `cd src && go build -o /tmp/microchat-go .`，再重启它（`tmux kill-session -t microchat-server` 后重起）——不然你验的是旧二进制。自检：`ls -l /proc/<pid>/exe` 带 ` (deleted)` = 跑的是旧货。
- **起服务用 tmux（后台 + 日志落文件）**，别用"受监督进程"那类会一直挂着输出的方式（那样一次工具调用会被进程输出拖住，CLI 卡死）：

  ```bash
  tmux new-session -d -s microchat-server -c <repo> \
      '/tmp/microchat-go > /tmp/microchat-server.log 2>&1'
  ```

  重启 = `tmux kill-session -t <名字>` 再起；看日志 = `tail -n 40 /tmp/microchat-*.log`；**tmux 下崩了没有通知** ⇒ 靠 `pgrep` 与日志。
- **先量再断言**：能实测的就不猜（本项目几乎所有关键结论都来自实测）。
- 用户可能**同时在编辑器里改 `frontend/`**：改场景前先读最新文件（并留备份），他的未保存改动优先。
- **重大决策留一句在案**（就写进本文件：结论 + 一句话理由）；**过程与来回不记**（git 里有历史，别把这份撑肿）。
