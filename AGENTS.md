# AGENTS.md — 工作须知

microchat：轻量 SillyTavern 替代（RPG 向）。三层，边界要清楚：

| 层 | 位置 | 状态 |
|---|---|---|
| 后端（**唯一权威**） | `core/`（Go + 标准库 + `modernc.org/sqlite`） | **新代码一律写这儿** |


**为什么是 Go**：用户不读 Rust ⇒「出事也能修」那个前提失效；Go 语法少、像 C、标准库自带 HTTP/JSON。
| Godot 前端 | `frontend/` | **用户自己在编辑器里设计**——动手前先问；还没接真实数据 |

## 术语表（一个词只指一件事）

> **会话（Session）与消息（Message）的完整定义、字段、以及"故意不存什么"，见 `DEFINE.md`** ——
> 那份是词表的唯一权威；这里只列最常用的几个词。
> **下一步做什么看 `TODO.md`** —— 那里只列"还没动"的；设计口径看 `DEFINE.md`。
> **任务（Task）与两族调用（对话型 / 功能型）的完整定义**同样在 `DEFINE.md`（那一节：Task = 一次上游请求的全程）。

| 词 | **只**指什么 | 别叫 |
|---|---|---|
| **世界状态（state）** | `<state>` 那套：沿当前路径**现演**的键值 —— 词汇只有两种：**变量**（键 → 值）与**表**（命名空间，不写表名就落到 `global` 表）；删除不留痕迹。+ 三层来源（底子 / 本会话 / 生效） | 孤立地说"状态"（得说清是世界状态、turn 状态还是 Task 状态） |
| **能力（Ability）** | **写死在代码里的一段固定流程**（**会调模型的 Task 种类**：现在 `title` / `compact` / `judge` 三个）；开关在 Agent 上、流程在代码里。**"对话本身"不是能力** ✗（那是 Agent 存在的方式） | "工具"（撞函数调用）、"子 Agent" |
| **Agent** | **一份命名的人格 + 一组能力开关**（`agents.json`）：同一个运行时，只是开关不同 | 拿它指"某个能力" |
| **任务（Task）** | **一次上游请求的全程**（发起 → 收尾 → 可能被取消）：一轮生成、一次压缩、一次刷新模型都挂号。**会调模型的一定是 Task**（**反之不成立** —— `refresh_models` 也是 Task，但它不调模型）。**TaskID 是 UUIDv7，且不进库** | "后台作业"（不都在后台）；拿它指"某个能力" |

三层咬合：**Agent（人格 + 能力开关）→ 一次调用（对话型 / 功能型=能力）→ Task（这次上游请求的全程）**。
**Task 的种类 ≈ 能力 id** ✓（2026-09-30 定，别再分两层）：`title` / `compact` 是，`judge` 能力的 Task 面叫 **`judgement`**
（JEV 那一发的名字；见 `DEFINE.md`「任务（Task）与两族调用」），而 `turn` 作业发起的是**对话型调用**（**不算能力**）、`refresh_models` 发起 0 次。
"能不能自主选下一步"**不是** Agent 的判据，将来是某个能力（如 `plan`）的事。

## 代码布局

```
core/                      ← 后端（Go **核心**；叫 core 是因为它不只是"服务端"：
  main.go                  一个二进制 —— 既能在终端里当 TUI 直接聊，又监听端口给别的前端用）
  internal/
    statelang/             `<state>` 语法的唯一权威（零依赖纯函数 + testdata/cases.json 共享语料）
    state/                 世界状态：分层现演、注入渲染、**唯一**出站拼装
    store/                 SQLite：打开/迁移/读写（migrations/*.sql）
    server/                HTTP：路由、错误体、鉴权（只编排，不含业务）
    model/ config/         纯结构 / `config/` 读写

frontend/                  ← Godot 4.8
reminder/                  ← 专题参考（`tool-calls.md` 工具调用 / `jev.md` 决策模型）；
                             `AGENTS.md` 只留形状与指针，细节住这儿
```

## 模块地图：一个模块 = 一份唯一权威 + 一条不变量

**四条"唯一"，就是四条能写成守卫测试的规矩**：

| 模块 | 唯一权威 | 不变量 |
|---|---|---|
| `model` | 纯结构 | 无依赖 |
| `statelang` | `<state>` 语法 | 零依赖纯函数；**解析即验证**；`Cleaned` 里**永不出现标签**（fuzz 守着）；写回（`Format`）**带表名**（丢了就悄悄落回 `global`）|
| `store` | **只有它碰 SQL** | 正文/提示词原样存档；变量不落库；会话是**线性**的（顺序在 `id` 上，没有父指针/leaf）；摘要盖的是**一段**（`begin/end`）；生成中的回复不在库里；消息序号 `idx` **派生**（按 `id` 排第几条，不落库）|
| `config` | `config/` 四个文件 | 可缺失；整体重写；密钥不回显 |
| `registry` | 模型口径 | 身份 `(provider, upstream_id)`；发现与覆盖分列，刷新不动用户列 |
| `providers` | **只有它碰网络** | 只走成熟库；超时必配；**选后端**（哪条渠道来答）也只有这一处 |
| `state` | **只有它拼出站文本** | 发出去的剔除标签、改注入当前状态；底子分层由提示词来源决定 |
| `blocks` | 块切分 | 块是推导的、不入库；不许劈开；开着的那块永不压 |
| `compact` | 摘要唯一入口 | **只往 summaries 插行**；消息级不许覆盖已压缩的区间（**合并级相反**：只吃同层顶层摘要）；失败不阻塞一轮 |
| `abilities` | 能力身份（枚举） | 产出不进历史/树/变量；失败不阻塞一轮；**缺条目 = 默认全开** |
| `template` | `{{…}}` | 白名单 = 枚举；只扫一遍；会话 Agent 的提示词永不替换 |
| `chat` | 一轮生成怎么跑 | 拿到整段才 INSERT |
| `turn` | 这一轮的流细节 | 增量什么都不算；游标读不消费 |
| `task` | 作业的身份 + 生死（= 一次上游请求的全程） | `defer` 兜底（**Go 没有 Drop**）；绝不留下僵尸条目；TaskID 是 UUIDv7、不进库 |
| `reroll` | 重摇的**候选列表**（进程内、会话级） | 候选**只在内存里**（不建表、不写库、Copy 不带走）；两个平行家族：**尾条 assistant**（reroll-message）与**没有父的摘要**（reroll-summary）；切换 = 就地换正文（消息 **UUID 不变** / 摘要 **id 不变**）；与生成**共用同一把闸**；**装配与摘要都不另写一份**（借 chat 的装配、借 compact 的生成半程） |
| `server` | **只有它碰 HTTP** | 错误体固定；只编排不含业务 |

依赖方向单向：`server → chat/compact/abilities → state（含 statelang）/blocks → registry/providers/config → store → model`。
（`compact` 另要 `abilities` 与 `providers` —— 校验能力开关、借同一套真发；`blocks` 只依赖 `model`。）
`turn`/`task` 是横切（谁都能挂号，不被依赖）。
`reroll` **谁也不依赖、也不被谁依赖** ✓：它借 `chat` 的**装配**（`chat.Service.RerollMessages`）、
`compact` 的**生成半程**（`compact.Service.RegenerateSummary`：只生成、不落库）与 `turn` 的**闸门**，
靠三个**接口**接上（`reroll.Assembler` / `chat.CandidateOwner` / `reroll.SummaryGenerator`，都在消费方那一侧定义；
`compact` → `reroll` 的适配器在 `main.go` 接线）——
于是"重摇的装配就是一轮对话的装配""摘要重摇的材料就是当初那次压缩的材料"这两条不变量成立，
而各包不会互相 import（Go 也就不会报环）。
**Go 用编译器强制这张图**。

⚠ **表里还没建的只剩 `template`**（`{{…}}` 那套）：`compact` / `blocks` / `abilities` 都已落地 ✓（`chat` 早就有了 ✓）。
（其余都在 ✓）—— 所以要"强制这张图"目前只对已达成的部分成立 ✓，别拿这张地图当现有代码读。

## 不变量（破坏了会静默出错）

1. **存储是 SQLite**（不选全 JSON：崩溃安全、并发、查询都白拿）：`data/microchat.db`。手写配置只在 `config/`（**严格 JSON**：注释与尾逗号都报错——程序会整体重写）。**配置住数据目录里**（默认 `data/config/`）⇒ 备份 / 搬家只要搬 `data/` 一个目录；
  两者路径**相对工作目录**，可用 `MICROCHAT_CONFIG_DIR` / `MICROCHAT_DATA_DIR` 覆盖。界面偏好另存 `~/.config/microchat/frontend.json`（不同机制，别混）。
  **连接级 pragma 挂在 DSN 上**（`?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)`）：`db.Exec("PRAGMA …")` 只管当时那一条连接，池子后来新开的就漏了（踩过，见「踩过的坑」）。
2. **正文与提示词都是存档**：`<state>` 块原样留在消息正文**和 system prompt** 里；**发给模型的文本一律剔除标签**，改注入当前状态表。唯一出口 `state.BuildOutgoing()`——不许写第二条拼装路径。
3. **世界状态不落库**：世界状态**每次现算**（`state.FromSources`）：先扫生效提示词里的块（**底子**），再按**会话顺序**扫正文里的块，然后 fold。每条操作带 `message_id`，"哪句话带来的状态"追得回来。**没有派生表** ⇒ 编辑/删除消息、改提示词、Copy 一条会话都不需要"重算"（换个会话看状态就是重拉 `/sessions/{session_id}/state`）。
   底子的**来源**决定它算哪一层：用 agent 的 ⇒ **全局**（同一 agent 的会话共享）；会话自己写了 ⇒ **本会话**。
   **删除不留痕迹**：`delete(键)` 与空值 `键 =` 都只是从**本层**拿掉 ⇒ 删底子里的键，值会从底子漏回来（"删"只作用于自己那一层；不想被删的键别写底子，写进第一条消息）。
   生效提示词由**一处**解析（会话覆盖 → agent 的 → 内置默认），出站 / 底子 / 界面共用它。
4. **会话是线性的**（2026-09-29 定：树 → 线性 + Copy + 删除级联，方案见 `DEFINE.md`）：
   **一条会话 = 一条线**。顺序**只**由 `messages.id` 定（UUIDv7：受理时铸、就地替换不改 id ⇒ 顺序稳）；
   没有 `parent_message_id`、没有 `sessions.current_leaf`、没有"当前路径"、没有兄弟 —— **别按 rowid 排**。
   "最新一条" = `ORDER BY id DESC LIMIT 1`（白拿 `messages_by_session(session_id, id)` 索引）。
   - **序号 `idx` 是派生的**（2026-09-30 补）：= 这条消息在会话里"按 `id` 排第几条"（1-based），
     **不落库**（没有那一列、也没有迁移）、**永不改变** —— 只删后缀（不留洞 ✓）、不往中间插 ✓、
     编辑/重摇不改 `id` ✓ ⇒ 分配了就变不了。`0` 留给**合成的系统提示词**（它不是消息 ✗）。
     算式**只有一处**（`store.IndexMessages`：`allMessages` 排完就编），别处只读这个号（`/messages` 与 `/outgoing` 共用它）。
   - **分岔靠 Copy**（`POST /sessions/{session_id}/copy`，不叫 fork）：新 session + 消息与摘要一并复制
     （摘要**新 id** 且消息上的 `summary_id`、摘要的 `parent_summary_id`、两端区间都重映射；
     标题/渠道/模型/agent/提示词带着）；**世界状态不复制**（它本来就现演）。Copy 时一批 id 铸完要**排序再发**
     （UUIDv7 只在毫秒上有序 ⇒ 一口气连铸会乱序，而顺序就是 id）。
   - **摘要盖的是一段**：`summaries.begin_message_id` / `end_message_id`（闭区间，可空）。装配**按区间 O(1) 跳**，
     没有"父的孩子一个不缺"那道闸（区间就是覆盖范围本身）；指针悬空 / 区间缺失就逐条走原文（宁可细，不许漏）。
   - **删除 = 删这条及之后全部**（`DELETE /sessions/{session_id}/messages/{message_id}`）：删【目标及其之后的全部消息】
     +【覆盖区间与之相交的全部摘要】+【这些摘要的全部祖先】，再把幸存者里指向死摘要的指针置空
     （消息的 `summary_id`、摘要的 `parent_summary_id`），一个事务里按 **①清指针 ②删摘要 ③删消息** 执行。
     **只删后缀 ⇒ 不留洞 ⇒ 不需要"退 leaf"**。"删这条及之后"判定很便宜：任何 `end_message_id` 落在后缀里的摘要都死。
   - **预览与执行共用一份计算**（`store.DeletionPlan`）：`GET .../deletion-preview` 只算不动；`DELETE` 照它干，
     且请求体带回 `last_deleted_message_id` 核对"末尾没变"，不符 ⇒ **409**（重新预览）。
     **编辑不级联**（就地换正文、id 不变 ⇒ 覆盖范围仍成立 ⇒ 只标 `dirty`、照用）。
   - **生成中的回复不在库里**：受理时先把 id 算好发给客户端，等**整段**拿到才用这个 id 与当时捕获的上文 INSERT
     ⇒ 停止/失败/被杀都不留半条，也没有"清理占位"要维护。
5. **模型身份 = `(provider, upstream_id)`**；显示名三级回退（用户覆盖 → 上游名 → prettify）**只在后端**做。
   新建会话的 agent 取 `agents.json` 的 `default_agent`（空串/缺失都算没配 ⇒ 内置默认）。**agent 的 id** 新建时由后端生成；改名走 `PATCH /agents/{agent_id}` 的 `new_id`——一次把 `default_agent` 与所有会话的引用搬过去（软引用无外键，找不到就回落**内置默认那句**而不是回空，所以人设会悄悄换掉 —— 仍必须由这一处维护）。
6. **`data/`（含 `data/config/`）永不入库**（端口口令、连接信息、密钥、存档）。默认值全在代码里，文件缺失也能跑。`.gitignore` 只放行 Godot 项目的非缓存部分。
7. **HTTP API 看下面那节**（**唯一权威是路由表**）。改了接口就跑 `go -C core test ./...`。
   （旧版 Rust 已删 —— 对账工具跟着一起没了 ✓ 现在只有这一份实现 ✓）

## 常用命令

```bash
./run.sh                                                  # ← 最常用：不加参数，服务 + TUI 一起起（内部就是 go -C core run .）
go -C core build -o ../microchat .                        # 要二进制就这条；产出 ./microchat
./microchat                                               # 起来就既在 8787 服务、又在终端里开 TUI
go -C core test ./...                                     # 后端测试（`-C core` 是 Go 自带，不用 cd）

./microchat -debug <op> [参数…]                            # ← 直操模式：**同进程**调 internal/*，
                                                          #   不起服务 / 不发 HTTP / 不开 TUI，打印就退出
                                                          #   （每行一条 JSON，好接管道；op 一览见 `-debug` 无参数）
                                                          #   典型：new → send（同步跑完一整轮）→ outgoing / state / status
                                                          #   与 -config / -data / -addr 共存；给了 -debug 就不监听端口


cd frontend
godot --headless --path . --quit-after 3                   # 语法+运行检查
godot --headless --script /tmp/x.gd                        # 灌事件跑帧
ANDROID_HOME=~/Android/Sdk godot --headless --path . --export-debug "Android" /tmp/x.apk
```

**直操模式（`-debug`）**：验收 / 排障时**不隔着 TUI 与 HTTP**看后端的一条路（实现在 `core/debug.go`，
只从 `main.go` 进来）。两条要知道的：

- 它是"一个 op 一个进程" ⇒ `turn` / `task` 登记表当然是空的（**状态是进程内的事实**，这是诚实的事实，不是缺功能）；
  `tasks` 打的就是这一进程的面板。
- 于是"并发再发 ⇒ 409"与"另一个进程里 stop"靠一枚 **per-session 的 pid 锁**（`data/debug-turns/*.lock`）：
  锁活着 ⇒ 后到的那个 409；`stop` 找到活的 pid 就发 SIGTERM，那边的 send 当成本地按停。
  它**只服务直操模式**（server / TUI 的权威仍是进程内的登记表，它们从不看这个文件）。

## `<state>` 的语法（记事板，不是脚本）

```
<state 玩家状态>      # 可选的表名（命名空间）；不写就落到 global 表
AA = 123456          # 赋值（值一律字符串）
B = 234; C = 落石     # `;` 当换行（一行一个变量）
delete(AA)           # 删除
</state>
```

- **词汇只有两种** ✓（2026-09-30 定）：**变量**（一格键 → 值）与**表**（命名空间，把变量分组）。
  `<state>…</state>` = 不写表名 ⇒ 落到 **`global` 表**；`<state global>…</state>` 写的就是那张表 ⇒
  **两种写法是同一张表**（同一份 JSON 里不许两义），混着写也照常「后写覆盖先写」。
  解析结果里 `table` **恒非空**（`statelang.DefaultTable = "global"`），空串不再是任何表的键。
- **算完为空的表照旧自动消失**，**`global` 是唯一例外：它恒在** ✓ ——
  一个变量都没有也回 `{"global": {}}`（`statelang.EnsureDefaultTable`；`Fold` 本身不加这条，
  由往外给的那一层补：`POST /statelang` 的 `tables` 与 `GET /state` 的 `tables`）。
  空着的那张 `global` **不渲染**（`state.RenderTable` 跳过空表）⇒ 出站内容不会平白多一句空的"当前变量:"。
- **每种写法只有一种规范形态**：赋值 `键 = 值`（`set` 已砍）· 删除 `delete(键)`（`del …` 全砍）。砍掉的写法各给一条说得清的报错，不做兼容——LLM 写十次要能十次写对。
- **空值就是清掉**：`键 =`（trim 后为空）与 `delete(键)` 同效 ⇒ **存不下空串**。解析照实报 `set`（值是空串），动手的是**计算**。
- 块可在**任意位置**；无运算、无变量作右值；值里可以有 `=`（只在第一个处切）、**不能有空格**（⇒ `A=1 B=2` 报错，而不是静默删空格）。
- 解析是**行式**的（无文法）；坏行进诊断（带行号，整体按行号升序），整块仍从正文剔除。
- 表之间互不影响（同名键分属各自的表）；没有「表级删除」。
- **账本 ≠ 墓碑**：接口里的操作流水仍留删除那一行（「哪句话改了什么」要追得回来），但算当前值时它不留痕迹。
- **写回（`statelang.Format`）带表名** ✓（2026-09-30 补）：把语句序列化回文本时**必须写表名** ——
  命名表 ⇒ `<state 表名>`、`global` ⇒ `<state>`（不写就是 `global` ✓）。**别丢它**：丢了不报错，
  只是整块**悄悄**落回 `global`（不报错、结果不一样，最难查）。表名不同的相邻两段各写一块（表名住标签上，
  一块只装一张表），同一张表的连续语句合一块、超 `StatementsMaxBlock` 就分块 ⇒ `Scan(Format(ss)).Statements`
  与 `ss` 逐字段相同、再 `Format` 一次不变。
- **截至第 N 条的现演**（`GET /state?at_idx=N`，2026-09-30）：底子**永远用当前的生效提示词**（不追究历史 ⇒
  **不是真快照**），正文只 fold 到第 N 条（含）——`0` ⇒ 只有底子、越界 ⇒ 当作到最后一条。
  口径与实现见 `AGENTS.md` 的 API 表与 `DEFINE.md` 的「世界状态」。

## 配置文件长什么样

**`data/config/`** 里是**严格 JSON**（程序整体重写），都可缺失。文件名一律 `.json`：

- `config.json` — `{ "server": { "port": 8787, "auth_token": "可选", "refresh_models_on_start": true }, "defaults": { "provider": "dummy", "model": "dummy", "agent": "default" }, "chat": { "model_context_tokens": 131072, "compact_trigger_tokens": null, "compact_blocks": 10 } }`（`compact_blocks` = 压缩不给块数时压几个**对话块**；`refresh_models_on_start` = 启动时要不要自动去问每个渠道有哪些模型，**默认 true**，类型是 `*bool` ⇒ 没写就是开）
- `providers.json` — `{ "providers": [ { "id": "dummy", "vendor": "dummy" }, { "id": "openrouter", "vendor": "openrouter", "api_key": "sk-…" }, { "id": "自建", "vendor": "custom", "base_url": "https://…/v1", "api_key": "sk-…" } ] }`（`vendor` 是**唯一必填**；`kind` 只剩存量读入别名，新配置必须写 `vendor`；`protocol` 缺省 `openai-chat-completion`）
  **密钥就写在这一条里**（空串 = 没配）：整个 `config/` 在忽略范围内；接口一律不回显（只回 `has_key`），调试页读它时先打码；写回权限收紧到 0600。
- `agents.json` — `{ "default_agent": "跑团", "agents": [ { "id": "跑团", "name": "跑团主持人", "system_prompt": "你是跑团主持人。<state>季节 = 初冬</state>" } ] }`
  每个 agent 上可以带**能力开关** `abilities`（键 = 能力 id：`title` / `compact` / `judge`；**未知的 id 在写入与启动读取时都报错**，不静默忽略）：
  `"abilities": { "compact": { "enabled": false, "provider": "deepseek", "model": "deepseek-chat", "prompt": "…" } }`
  —— **缺字段 = 默认全开** ✓（不带 `abilities` 的 agent 三个能力都是开着的，现有文件一个字都不用改）；
  `provider` / `model` 留空 = 用**会话自己的**；`prompt` 留空 = 用代码里的默认模板；`enabled: false` ⇒ 调用方**明确拒绝**执行那一次（不是静默降级）。
  **没有 `abilities.json`** ✗（口子就在 agent 上 —— 于是"能力只能覆盖、不能造"自然成立）。
  **agent 上也没有 `params` / `prompt_order`** ✗（2026-09-30 **删掉**的两个死字段：声明了、没人读、用途未定 ——
  要用再加。老文件里写了它们也**不报错**，只是**下次整份重写时**会没掉：开发阶段可接受 ✓ 记得别依赖它）。

## HTTP API（客户端契约）

**唯一权威是 Go 版的路由表**（`core/internal/server/`）。全部挂在 **`/api/v1`** 下，请求与响应都是 JSON。

- 配了口令时（`config.json` 的 `server.auth_token`）**所有**接口都要 `Authorization: Bearer <token>`（`/health` 也不例外）；
  缺失 / 不符 ⇒ **401**（错误体同下，`code: unauthorized`）。**预检 `OPTIONS` 例外**——它不带凭据，在鉴权**之前**就被接管。
  **没配**口令（空 / 未设）⇒ 全部放行（默认行为）。
- 错误体固定 `{"error":{"code","message"}}`，`code` ∈ `invalid` / `not_found` / `conflict` / `unauthorized` / `upstream` / `internal`——客户端按 `code` 分支，别匹配文案。
- **405 由框架自己回**（路径在、方法不对），不带上面的体，但带 `allow:` 头。
- **CORS 全开 ✓ + 预检 204 ✓**（`Access-Control-*` 盖住**每一个**响应：200 / 401 / 404 / 405 / 错误体都带；
  `OPTIONS` 在**鉴权之前**被接管 ⇒ 预检不带 `Authorization` 也放行、回 **204 无体**；只有 `OPTIONS` 被我们截下，其余方法照旧透给路由）。
  `Allow-Origin` 有 `Origin` 就**回显**它、否则 `*`（并带 `Vary: Origin`）；`Allow-Headers` 回显 `Access-Control-Request-Headers`（没有则 `Authorization, Content-Type`）；
  `Max-Age: 600`；`Expose-Headers: Allow`（405 的 `allow:` 不在安全名单里，浏览器默认读不到）。
  **默认不设 auth_token**，所以别把端口暴露到公网。

### 故意砍掉的（2026-09-29 定：**先简化，再往深走**）

这一轮把"无用"与"可选"两类路由从契约里**删掉**了（不是没搬 —— 是**不要**）：

- 无用：`GET /debug/state`、`GET /debug/file/{name}`、`DELETE /providers/{provider_id}/models`、`GET /sessions/{session_id}/export`
  （理由：后端自述会说谎、配置文件原文不该给远程客户端、上游模型列表本就自动清理、导出该由 Agent 卡带承担）
- 可选（**待重做**，不做兼容）：`POST /models/probe`、`POST /sessions/{session_id}/archive`、
  `GET /sessions/{session_id}/summaries`、`GET/PUT /abilities`
  （`POST /sessions/{session_id}/compact` **已经重做落地** ✓ —— 见下面的路由表；
  `GET /tasks` 以**新形状**重做落地 ✓ —— 见"运维"那节，不在会话下、只读进程内面板）
- **`POST /sessions/{session_id}/fork` 已彻底作废** ✗（不是"待重做"）：线性会话里分岔就是 **Copy** ✓（见上表）
- **不要照旧版补回来** ✗ —— 旧版是参照，不是目标；要加先改这张表。

### 会话与消息

| 方法 | 路径 | 请求体 | 响应 | 说明 |
|---|---|---|---|---|
| GET | `/sessions` | — | `[SessionView]` | 每项 = 会话 + `messages`（**这条会话有几条消息** —— 客户端的启动编排据此认定"空会话"，不必逐条会话再拉一次消息）+ `turn`（左栏据此标"生成中"）|
| POST | `/sessions` | `CreateSessionReq` | `Session` · 201 | 省略字段时取 `config.json` 的 `defaults`；`title` **真生效**（建一条已命名的会话，不必再多发一次 `PATCH`）—— 不给 / `""` ⇒ 标题空着（等首条用户消息自动起名）|
| PATCH | `/sessions/{session_id}` | `UpdateSessionReq` | `Session` | 标题 / 模型 / agent / **提示词**（`system_prompt` 是**指针** ⇒ 没给或 `null` = 不动、`""` = **清掉会话级覆盖** ⇒ 回落 agent 的、非空 = 写进 `sessions.system_prompt`）|
| DELETE | `/sessions/{session_id}` | — | 204 | 不存在 → 404 |
| POST | `/sessions/{session_id}/copy` | — | `Session` · 201 | **Copy**（线性会话里的"分岔"）：新 id、消息与摘要一并复制、摘要新 id 且指针重映射；**世界状态不复制** |
| GET | `/sessions/{session_id}/messages` | — | `[Message]` | **按 `id` 升序的整条会话**（线性 ⇒ 没有"当前路径"）。三条查询参数（**都不给 = 全部**，语义不变）：`?from_idx=5&to_idx=10` **闭区间**（含两端；缺一端补默认：起点 1 / 终点末尾）、`?last=20` **取尾**。**参数错 ⇒ 400**（`last` 与区间混用、非正整数、区间反了）；**越界不是错** ⇒ 给现有的那几条（起点在末尾之后 ⇒ 空数组）。响应仍是**数组** ✗（不换成对象）。每条带 `idx`：**0 = 合成的系统提示词；1..N = 真消息** —— 它是**派生的**（= 按 `id` 排第几条）、**不落库**、**永不改变**（只删后缀 ⇒ 不留洞；不往中间插；编辑/重摇不改 `id`）|
| POST | `/sessions/{session_id}/messages` | `SendReq` | `TurnAccepted` · **202** | 落用户消息 + 开工；不含回复正文 |
| PATCH | `/sessions/{session_id}/messages/{message_id}` | `EditMessageReq` | `Message` | 改正文 = 重写存档（世界状态随之现演；摘要只标 `dirty`、**不级联**）|
| GET | `/sessions/{session_id}/messages/{message_id}/deletion-preview` | — | `DeletionPlan` | **只算不动**（安全 ⇒ GET）：会删掉哪些消息 / 摘要、哪些指针会被置空 |
| DELETE | `/sessions/{session_id}/messages/{message_id}` | `{"last_deleted_message_id"}` | `DeletionPlan` | 删**这条及之后的全部**（级联见 `DEFINE.md`）。核对字段不符 ⇒ **409**（重新预览）；缺字段 ⇒ 422 |
| GET | `/sessions/{session_id}/state` | — | `StateView` | `baseline` / `session` / `baseline_values` / `effective` / `tables`，**每次现算**。层名是 **`baseline`**（底子）、**不是** `global` ✗ —— `global` 现在是 **`tables` 里那张表**（不写表名的块落到它，且它**恒在**：空也回 `{}`）。查询参数 `?at_idx=N` ⇒ **截至第 N 条的现演**：底子**永远用当前的生效提示词**（不追究历史 ⇒ **不是真快照**）、正文只 fold 到第 N 条（含）、`0` ⇒ 只有底子、越界 ⇒ 当作到最后一条（与 `/messages` 一个口径）、不给 ⇒ 当前状态；负 / 非整数 ⇒ **400** |
| GET | `/sessions/{session_id}/outgoing` | — | `[Outgoing]` | **(b) 当前已定历史的载荷**：把**已入库的东西**装配一遍 —— **不含还没发出去的那一句** ✗（标签已剔、状态已注入、**压缩已生效**）。每条带类型：`type` = `system`/`message`/`summary`（后者另有 `summary_id`、`blocks`、`children` 嵌套—— summary 项往下展开一层，叶子只给 id+idx）—— **检查压缩效果靠它**，别去猜正文抬头。每条还带序号：`type=system` ⇒ **`idx: 0`**（合成的、不是消息）、`type=message` ⇒ 它自己那条的 `idx`、`type=summary` ⇒ 它**替代的**范围 `from_idx`/`to_idx`（于是"第 5–10 条被压成了哪一条"一眼可见；摘要**没有**自己的 `idx` ✗） |
| POST | `/sessions/{session_id}/outgoing` | `{"content":"…"}` | `[Outgoing]` | **(c) 把这条 content 当成即将追加的那句用户消息之后**，真会发出去的东西（与 (b) **逐项同字段**、只**多**那条 user 项 —— **只有**那一项带 **`pending: true`**（`omitempty` ⇒ 其余各项不带这个键）：它的 `message_id` 是**预测值**，真发那一刻另铸一个 ⇒ **别拿它去查消息**；它的 `idx` = **下一条**（现有最大 + 1 ✓））。**只算不写**：不落库、不改任何状态。待发那句若带 `<state>` 块 ⇒ **状态表跟着变** ⇒ 必须重走一遍现演与装配（不是"(b) + 一条消息"）。缺 / 空白 `content` ⇒ **400** |
| GET | `/sessions/{session_id}/context` | — | `ContextUsage` | 只有数字：`used_tokens`（估算）/ `budget_tokens` / `trigger_tokens` / `remaining_tokens` / `ctx_len` / `max_output` / `ratio` / `estimated` / `last_prompt_tokens` / `over_budget` |
| GET | `/sessions/{session_id}/prompt` | — | `{"text","type"}` | **生效的系统提示词**（三级解析的**结果**，与出站拼装读同一处）：`type` = `conversation`（会话自己写了 `sessions.system_prompt`）/ `agent`（`agents.json` 里那个 agent 的）/ `builtin`（两级都没有 ⇒ 代码里的内置默认）。**永不给空**：解析全落空（典型：会话的 `agent_id` 软引用**悬空**）也退内置那句、`type` 报 `builtin`。将来做了可拼接的提示词，这里回**运算后**的结果（形状不变）|
| GET | `/sessions/{session_id}/status` | — | `TurnStatus` | `phase`（`idle`/`pending`/`streaming`/`error`）+ `message_id` + `elapsed_ms` + `chars` + `thinking_chars` + `error`。另搭两档**与轮次无关**的活状态：`compact`（`state`=`running`/`done`/`error` + `blocks`/`compacted`/`summary_id`/`from_idx`/`to_idx`/`merged` + `error`）与 `reroll`（`state` + `elapsed_ms` + `error`）|
| GET | `/sessions/{session_id}/turn/text?from=N&think_from=M` | — | `StreamSlice` | 流式增量的**游标读**（正文与思考各一条游标，`from` = 第几个字符）：只服务动画 |
| POST | `/sessions/{session_id}/stop` | — | `{"stopped": bool}` | **幂等**：没在跑也 200（`false`）|
| POST | `/sessions/{session_id}/compact` | `CompactReq` | `CompactStatus` · **202** | **压缩**：`{"blocks": N}` 或 `{"begin_idx": i, "end_idx": j}`（1-based 消息序号，与 `/messages?from_idx=&to_idx=` 同一套词；两种给法互斥、区间两端都得给；都不给 ⇒ 用 `chat.compact_blocks`）。**按块，不按条**（块 = assistant→user 交界，一块 ≥2 条）。按块数 ⇒ 策略（底层凑不够就抬头并摘要）；按区间 ⇒ 全未覆盖走消息级、**同层顶层摘要恰好铺满**走合并（金字塔）、其余 ⇒ 400（混合有洞，不后台跑）。**同一个会话同时只允许一次**（在跑 ⇒ 409）。跑完的结局在 `GET .../status` 的 `compact` 那一档（`running`/`done`/`error` + `from_idx`/`to_idx`/`merged` + 原因）|
| POST | `/sessions/{session_id}/compact/preview` | `{"blocks": N}` | `PreviewResult` · 200 | **压缩预览**：**只算不动**（调同一套策略；区间入口自己就是答案，不走这里 ⇒ 400）。回 `from_idx`/`to_idx` + `merged` + `source_ids` + 人话一句（"压第a–b条（N块）"/"并第a–b条那N坨"）。落库/调上游/挂号一概不碰 |
| POST | `/sessions/{session_id}/reroll-message` | — | `{target_message_id, task_id, state}` · **202** | **重摇·消息**：进模式并**立刻摇一次**（已在模式里 ⇒ 再摇一版）。候选（`RerolledMessage`）**只在内存里**、**不进历史** —— 选中才 apply 回那条 Message。尾条必须是 assistant（不是 ⇒ 400 说清那是"重发"）；与生成**共用同一把闸**（在跑 ⇒ 409）|
| GET | `/sessions/{session_id}/reroll-message` | — | `{active, target_kind, target_message_id, count, current_idx, running, elapsed_ms, error, items:[…]}` | 重摇状态（**不吐全文**，预览几十字）。没进模式 ⇒ `active: false`（**不是 404**）|
| POST | `.../reroll-message/switch` | `{"idx": n}` | 同上 | 选中第 `n` 版 ⇒ **就地重建**那条 Message（**UUID 不变**；正文与 usage / 耗时 / 思考一起换；摘要照旧标 `dirty`）|
| DELETE | `.../reroll-message/{idx}` | — | 同上 | 删掉第 `idx` 版（**位次不是身份**）；**删到只剩一条 ⇒ 退出模式 + 清列表**（**不 apply**：message 保留当前这版）|
| DELETE | `.../reroll-message` | — | 204 | **显式退出**（message 保留当前这版）|
| POST | `/sessions/{session_id}/reroll-summary` | `{"idx": N}` | `{target_summary_id, task_id, state}` · **202** | **重摇·摘要**：`N` = 1-based **消息** idx ⇒ 沿 `summary_id → parent_summary_id` 上溯到**根**（同树任意 idx 指向同一个目标）—— 只重摇**没有父**的摘要。材料与当初那次压缩**同源**（走 compact 的"只生成、不落库"半程）；没被摘要盖住 / 越界 ⇒ 400 |
| GET | `/sessions/{session_id}/reroll-summary` | — | 同上形状（多 `target_summary_id`、`from_idx`/`to_idx`） | 摘要模式状态；与消息家族**同一套闸与位次**（一个会话同时只有一个重摇模式）|
| POST | `.../reroll-summary/switch` | `{"idx": n}` | 同上 | 选中第 `n` 版 ⇒ **就地换那条摘要的正文**（`id` 与区间不动；守卫"仍无父"—— 期间被并走 ⇒ 冲突）。`dirty` = 材料新鲜度 |
| DELETE | `.../reroll-summary/{idx}` | — | 同上 | 同消息家族（后方位次 -1；只剩一条 ⇒ 退出）|
| DELETE | `.../reroll-summary` | — | 204 | **显式退出**。通用错误：`idx` 越界 / 没被摘要盖住 ⇒ 400；没进模式 ⇒ 404；切/删在摇的那版 ⇒ 409；目标被并走 ⇒ 状态回 `active:false` |

同一会话在跑时再发 → **409**。`TurnAccepted` = `{user?, backend, turn}`：`turn.message_id` 是**这条回复的 id**（受理时定好，那会儿还没进库）。

**出站载荷有三件事 —— 别用一个词糊过去** ✗（各有名字、各有入口）：

| | 是什么 | 入口 | 能否重算 |
|---|---|---|---|
| **(a) 上一次真发出去的那一发** | 那一刻请求的**快照**（**含请求头**、覆盖式：只留最近一发） | `GET /debug/last-payload` | **不能** ✗ —— 历史事实：改一条旧消息就回不去了 |
| **(b) 当前已定历史的载荷** | 把**已入库的东西**装配一遍（**不含还没发的那句** ✗） | `GET /sessions/{session_id}/outgoing` | 能 ✓ —— 改旧消息 / 换 agent / 世界状态变了，它立刻不同 |
| **(c) 把待发那句追加进去之后** | (b) **+ 那条 prompt**（**只有**那条待发项带 `pending: true`；`omitempty` ⇒ 其余各项不带这个键。它的 `message_id` 是**预测值**，别拿它去查消息） | `POST /sessions/{session_id}/outgoing` | 能 ✓，但**只有服务端算得出来** |

**(c) 为什么客户端拼不出来** ✗：待发那句里若带 `<state>` 块 ⇒ **注入系统提示词的状态表会跟着变** ⇒ (c) 不是"(b) + 一条消息" ✗，必须重走一遍状态现演与装配。

**序号 `idx`** ✓（2026-09-30 补）：(b) 与 (c) 的每一项都带它 —— `system` ⇒ **0**（合成的，不是消息）、
`message` ⇒ 自己那条 ✓、`summary` ⇒ 它**替代的**范围 `from_idx`/`to_idx` ✓（"第 5–10 条被压成了哪一条"一眼可见）；
(c) 里那条待发的 user 项拿的是**下一条**的号（= 现有最大 + 1 ✓，与它的 `pending: true` 一致）。
它是**派生的**（= 按 `id` 排第几条）、**不落库**、**永不改变** —— 见「不变量」第 4 条与 `DEFINE.md` 的「各种 id」。

**真发的那一轮装配的就是 (c) 的前身** ✓：受理时先把用户消息落库，再走**同一段装配**
（`chat.outgoingFor` ⇒ `assemble` ⇒ `state.FromSources` + `state.BuildOutgoing`）——
唯一的差别只是"那句已经进了库、`ListMessages` 里有它" ⇒ **预演与真发不出第二份答案** ✓（`POST /outgoing` 走的正是同一个 `assemble`）。

### statelang（给外部工具）

| 方法 | 路径 | 请求体 | 响应 | 说明 |
|---|---|---|---|---|
| POST | `/statelang` | `{"text": "任意文本"}` | `{"tables": {表名: {键: 值}}, "statements": [...], "diagnostics": [...]}` | **解析 + 计算**一段文本里的全部 `<state>` 块：不起对话、不落库。`tables` = 算完的值（删除生效、后写覆盖、空表消失；不写表名的块进 **`global` 表**，而 `global` **恒在** —— 空也回 `{}`）；`statements` = 读出来的操作（按出现顺序，删除未生效）；`diagnostics` 带行号。缺 `text` ⇒ 422 |

### provider / 模型 / agent

| 方法 | 路径 | 请求体 | 响应 | 说明 |
|---|---|---|---|---|
| GET | `/providers` | — | `[ProviderView]` | **含 `has_key`，绝不含密钥内容**；`base_url` 回**生效值**（只配 `kind` 时由预设供给）|
| GET | `/providers/presets` | — | `[PresetInfo]` | **内建预设清单**（kind / 端点 / 密钥环境变量 / 会话头 / 可选身份）—— 界面靠它生成"选一个内置 provider"的下拉 |
| POST | `/providers` | `CreateProviderReq` | `ProviderView` · 201 | `api_key` 写进 `providers.json`；重名 → 409 |
| PATCH | `/providers/{provider_id}` | `UpdateProviderReq` | `ProviderView` | `api_key`：`None` = 不动，`""` = 清除 |
| DELETE | `/providers/{provider_id}` | — | 204 | 密钥随记录一起没 |
| POST | `/providers/{provider_id}/refresh` | — | `ProviderView` | **POST**（会写发现态）：拉 `/models` 并落库 |
| GET | `/providers/{provider_id}/usage` | — | `ProviderUsageView` | **套餐余量**（只读）：**只对 `vendor = opencode-go` 的渠道有义** ⇒ 别的 vendor **400**（`invalid`）、没配 key 也 **400**；上游错原样传（`upstream`）。形状 = `plan` / `windows[].label/percent/resets_at` + `provider_id`。TUI 里 `/usage` 就是它 |
| GET | `/models` | — | `[ModelListItem]` | 跨 provider 拍平（"渠道 / 模型"下拉用）|
| PATCH | `/models` | `{provider, upstream_id, context_override}` | `ModelView` | 设/清上下文覆盖（`null` = 清）。**刷新永不覆盖用户列** |
| GET | `/agents` | — | `AgentsConfig` | 生效列表（含内置默认 agent）|
| POST | `/agents` | `CreateAgentReq` | `Agent` · 201 | 只收 `name` + `system_prompt`；id 由后端生成 |
| PATCH | `/agents/{agent_id}` | `UpdateAgentReq` | `Agent` | `new_id` = 重命名（搬 `default_agent` 与会话引用）|
| DELETE | `/agents/{agent_id}` | — | 204 | 内置默认 agent 不可删 |
| GET | `/config/chat` | — | `ChatConfig` | `config.json` 的 chat 段（不含密钥）|
| PUT | `/config/chat` | `ChatConfig` | `ChatConfig` | **整段替换** chat（其余段原样保留）|
| GET | `/config/telegram` | — | `{has_token,allowed_id,enabled,running}` | TG bot 状态（**token 只回有没有**，内容永不回显；`running` = 长轮询真跑着）|
| PUT | `/config/telegram` | `{allowed_id?,bot_token?,enabled?}` | 同上 | **三格各改各的**（nil = 不动；`bot_token:""` = 清掉文件里的回落 env）；改开关/token **重启生效**（runner 归 main 管）|

### 运维

| 方法 | 路径 | 响应 | 说明 |
|---|---|---|---|
| GET | `/health` | `{"status","version"}` | 探针；**也过鉴权** |
| GET | `/debug/last-payload` | `LastPayload` | **(a) 上一次真发出去的那一发**：那一刻请求的**快照**（method / url / **头** / 体；内存一份，覆盖式；没发过 = `null`）。**不可重算** ✗ —— 历史事实（改一条旧消息也回不去），且**含请求头**：用来回答"我上一发到底发了什么 / 为什么被拒"（网关拒的往往是头不是体）。头也回显且打码 |
| GET | `/tasks` | `TaskBoard` | **任务面板**：进程内的事实（`running` + 在跑的在前的 `tasks`；与 `-debug tasks` 同一份 `Board`，不进库）。轮询用（TUI 底栏指示器就问它）。重启就没 —— "没有在跑的"是诚实的事实 |

## 一轮生成（202 + 轮询）

**已实现** ✓（`core/internal/chat` + `server/turn_api.go` + `providers/stream.go`）：

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

**流式**：增量**什么都不算**（不进库、不参与世界状态与出站计算）——落库只认流结束后那一整段。所以流断了、被停了、上游中途报错，档案永远干净。

- 上游用现成的 SSE 库，不手搓 `data:` 分帧；
- 增量进这一轮的缓冲区，顺手把 `pending` 抬成 `streaming`；令牌对不上的字节直接丢；
- **思考流单独一条缓冲**：推理型模型先吐 `reasoning`/`reasoning_content` 再吐 `content`（两家字段名不同，两个都认）；思考只服务动画、**不参与任何计算**，但会落档（`messages.reasoning`；`store_reasoning: false` 则只走动画）；
- 给前端的是**游标读**（`?from=N&think_from=M`，正文与思考各一条）：多个客户端与断线重连各拿各的，互不偷；`done` 表示这轮结束，结束后仍可补拉尾巴。

## 后台任务 / 压缩 / 能力

**任务（Task）**：**一次上游请求的全程**（形状、五个状态、署名、落库前验证清单见 `DEFINE.md`「任务（Task）与两族调用」）。
一个登记表 —— 前台一轮生成、一次压缩、一次刷新模型都挂号；**会调模型的 Task 必填会话 id**（`Begin()` 当场断言）；
兜底靠每个任务 goroutine 第一行的 `defer guard.Interrupted()`：**忘了收或 panic 记成"中断"**（Go 没有 Drop）⇒ 绝不留僵尸条目。
三份状态**各管一段、不许互为镜像**：`task` = 身份 + 生死；`turn` = 流细节（几个字、取消）+ 压缩那一档；`compact` = 压缩自己的细节。

**压缩**（`internal/compact`，已落地 ✓；路由 `POST /sessions/{session_id}/compact` ⇒ **202**）：机制与策略两层。
- **机制** = 给它一段（两端用 **message id** 指；**成员可以是消息，也可以是同一层的顶层摘要** —— 后者就是金字塔合并）→ 拼材料（每一步正文过 `statelang.Scan(...).Cleaned`，`<state>` 块剔掉，末尾附上下面那份状态）→ 叫 `compact` 能力（**非流式**一次调用，骑本会话 id、`cache_retention: none`）→ 过五条验证 → **只往 `summaries` 插一行**。
  五条验证：① 剔 `<state>`；② 非空（剔完是空的 ⇒ **不写**，宁可什么都没有）；③ 区间自洽（两端在本会话、begin ≤ end、两端都还在）；④ **不许覆盖已压缩的区间**（消息级；`store.RecordSummary` 在事务里再核一遍 —— **合并级相反**：它吃的就是已压缩的那一层，复核 = 一条守卫 UPDATE「只认还顶层的孩子」）；⑤ 落库一并写 `prompt_version` 与 `usage`。
- **材料里附一份"算到区间末的状态"** ✓（2026-09-30 落地）：拿区间的 `to_idx` 去问 `at_idx` 那条链（`state.StateAt`：**当前的**生效提示词打底 + 正文只 fold 到区间末），
  用**同一个** `state.RenderTable` 渲染，抬头写死口气「【程序·状态（截至这段末尾；程序事实，仅供参考，不要写进梗概）】」。
  ⇒ 模型写梗概时**看得见**这一段结束时的变量，但**抄不进摘要**（摘要照旧不存状态）；一个变量都没有 ⇒ 不附（不塞空话）。
  **只此一处** ✗ —— 起标题那条链不许跟着加（`title` 不碰状态）。
- **策略** = "从第一条没被覆盖的消息起，取最老的 N 个**已闭合块**"（手动按钮的语义）；**最后那个开着的块永不压**；块是推导的、不入库（`internal/blocks`）。
  底层凑不够 N 个整块时**抬头** ✓（2026-10-02 落地）：从最老起取 N 个**同层、相邻、都没爹的顶层摘要**并上去（同一套合并闸；跨层不凑；一层找不齐就报"无可压缩"）。
  ⇒ 点"再压 N 块"会沿金字塔往上走（AB/CD/EF+G → 并 AB+CD 得 AD → …），每坨原话只被嚼一次（摊开，不逮着一坨反复压）。
- **结局带区间** ✓（2026-10-02 落地）：`Result` / `turn.CompactStatus` 都有 `from_idx` / `to_idx`（1-based）+ `merged`（是不是合并级）；
  TUI 轮询到 done 就报"压好第 a–b 条（N 块）"/"并好第 a–b 条那 N 坨"，不用再翻 `/outgoing`；`-debug compact` 同样打出来。
- 落库 = **单入口 `store.RecordSummary`**（按 `source_kind` 分岔）+ 守卫 UPDATE，**同一事务**：消息级把这一段消息的 `summary_id` 指过去；合并级把孩子的 `parent_summary_id` 回填（只认还顶层的）。**绝不插/改/删 `messages` 的其它列**（正文是存档）。
- 谁也**不许**绕过这里自己拼摘要请求。不持锁跨 await：取料在锁里、调用在锁外、落库再进锁。
- **失败不阻塞**：失败就把这一次报失败（带原因），会话一个字节都不动；**重试由调用方决定**，这里绝不重试。
- **还没做** ✗：自动触发（`compact_trigger_tokens` 只算了不算）、按范围的查看页（`GET .../summaries`；`POST /compact` 的 `begin_idx`/`end_idx` 区间入口已落地）。

**能力**（`internal/abilities`）：身份写死在代码里（`title` / `compact` / `judge` —— **枚举就是全部**），**开关 + 可选覆盖写在 `agents.json` 每个 agent 的 `abilities` 上**（不另开 `abilities.json`），只能**覆盖**模板/渠道/模型，不能造新的；**未知的 id 在写入与启动读取时都报错**。
- `abilities.Resolve(agent, id)` 是**唯一**解析处：缺条目 = **默认全开**；`provider`/`model` 空 = 用会话的；`enabled: false` ⇒ 调用方**明确拒绝**执行那一次（不是静默降级）。
- `judge` 的调用面 = `judge.Service.JudgeFor`（开关 + 选渠道 + `task.KindJudgement` 挂号 + 失败原样返回；state/questions 由调用方拼，产出是材料**不落库**）—— 动态组合术调的就是它。
- compact 换模型 / 改提示词**不用写代码**（例子：会话的渠道是 dummy，但压缩单独走 deepseek）：
  `"abilities": { "compact": { "provider": "deepseek", "model": "deepseek-chat", "prompt": "…" } }` ——
  `provider`/`model` 留空 = 蹭会话自己的；`prompt` 留空 = 代码里的默认模板（填了就整段替换，`prompt_version` 指纹跟着变）；
  `enabled: false` = 这份人格拒绝压缩（明说不做，不是降级）。
- 与会话 Agent 的三条硬边界：① 产出永不进历史/树/变量（落库由调用方决定，如摘要走 `store.RecordSummary`）；② 提示词永不进会话的系统提示词；③ **失败不阻塞任何一轮**。
- `prompt_version` = `sha256(生效模板)` 十六进制前 8 位（改一个字就变，别手写版本号；列是 INTEGER ⇒ 取 32 位那一截）。
- **Agent 的导出导入（卡带）**：导出**默认不带密钥**（`api_key` 置空，要带得显式勾）；**导入不需要「重算世界状态」**—— 变量不落库、每轮现演，换了底子状态自动就是新样子。要打包的就是 `agents.json`（能力开关就在它里面 —— **没有 `abilities.json`** ✗）。
- **占位符**（`{{…}}`）：白名单 = 枚举（`{{system_time}}` / `{{state_before}}` / `{{state_after}}` / `{{range}}` / `{{blocks}}`）；**不认识的 `{{foo}}` 原样留着**；**只扫一遍**（替换进去的值不再当模板扫）；**会话 Agent 的提示词永不替换**（它一变，前缀缓存每轮全废）。（`template` 模块本身还没建 ✗。）

## 数据模型

**只有一份**：`core/internal/store/migrations/001.sql`。
**开发阶段没有向后兼容**：改形状就改这个文件，然后把 `data/microchat.db` 删掉重来（用户 2026-09-29 定）——**不要**写 002、003。

**四张表，全是 TEXT id（UUIDv7）+ 毫秒整数时间戳。**

### `sessions`

`id` · `title`（首句自动生成；空串 = 还没起名）· `system_prompt`（**空串 = 没覆盖** ⇒ 底子算全局）· `provider` / `model` / `agent_id`（都是**软引用**，无外键）· `created_at` / `updated_at`（界面按 `updated_at` 排序）

（**没有 `current_leaf`**：线性会话不需要"停在哪儿"，"最新一条" = `ORDER BY id DESC LIMIT 1`。）

### `messages` —— 一条消息，**线性会话里的一格**

`id`（生成回复时**受理那一刻**就算好；**顺序就是它**）· `session_id`（FK CASCADE）· `role`（`CHECK IN ('user','assistant')`）· `content`（**存档本体**，`<state>` 块原样留着）· `created_at`
后四列**只服务显示**（出站/状态/编辑一律不看）：`reasoning` · `reasoning_ms`（受理 → 第一段正文）· `duration_ms` · `usage`（归一化后的 JSON）

### `models` —— 发现所得 + 用户覆盖（PK `(provider, upstream_id)`）

`upstream_name` / `owned_by` / `context_length` / `max_output` / `upstream_params` / `first_seen_at` / `last_seen_at`（**这次没见到就删行** ⇒ 列表 = 上游当前那份）
**用户列**（刷新永不动）：`display_name` · `params` · `context_override` · `tokenizer`（缺省 `{"kind":"approx","ratio":1.3}`）

### `summaries` + `provider_state`

`summaries`：`id` · `session_id` · `parent_summary_id`（**深度 = 指针链长度**，不存 level）· `source_kind`（`message`/`summary`，**同质**）· `begin_message_id` / `end_message_id`（**它盖住的那一段**，闭区间，可空）· `text`（只写叙事；混进 `<state>` 会被剔除并记日志）· `blocks` · `tokens` · `source_ids`（当时吃的是什么，供审计与重做）· `provider`/`model`/`prompt_version`/`usage` · `dirty`（被覆盖的消息被编辑过 ⇒ 1；界面显示"已过期"，**装配时照用**）· `created_at`
`provider_state`：`provider` · `last_refresh_at`（"上次拉取：N 分钟前"）

**索引/外键/运行时**：索引 `messages_by_session(session_id, id)`（**顺序就是 `id`**，取"最新一条"也走它）、`messages(summary_id)`、`summaries(session_id/parent_summary_id)`。外键**只有两条**（`messages` 的两个）：`session_id` ⇒ CASCADE、`summary_id` ⇒ SET NULL；`begin/end_message_id` 与 `agent_id`/`provider` 是故意不加约束的（`begin/end` 的失效由 `store.DeletionPlan` 一份计算负责）。迁移由 `PRAGMA user_version` 驱动（当前 **只有 001** —— 开发阶段改形状就改它、删库重来，**不许追加** 002）；`foreign_keys=ON`、`journal_mode=WAL`；写入由一把锁串行化。

## 摘要 / 压缩的设计（**压缩 + 金字塔合并已落地** ✓：`internal/compact` + `internal/blocks` + `POST /sessions/{session_id}/compact`；`summaries` 表与**区间**早就在用 —— 装配按区间跳、Copy 复制它、删除级联按它判定）

- **摘要不是"消息的节点"**：不往 `messages` 插行；**压缩只往 `summaries` 插一行**，绝不插/改/删 `messages`（删除那条路是另一回事：级联会删摘要，见不变量 4）。
- **盖的是哪一段：`begin_message_id` / `end_message_id`**（创建时写一次）⇒ 装配/级联都是 **O(1) 查区间**：不"数过去"，也不需要"父的孩子一个不缺"那道闸（线性会话里区间**就是**覆盖范围本身）。两端可空（老数据）⇒ 那时逐条走原文（宁可细，不许漏）。
  失效规则只有两条：① 被覆盖的消息**被删** ⇒ 该摘要连同**父链**作废（删除级联干的就是这件事）；② 被覆盖的消息**被改写** ⇒ 只标 `dirty`、**照用**。

  **具体走一遍**（用户的例子）：`A B C D E F G`，压缩出 `BD`(B,C,D)、`EF`(E,F)，再压出 `BF`(盖 `BD`+`EF`，
  区间 B..F)。走到 B ⇒ `B.summary_id = BD` ⇒ BD 的父是 BF、**左端同起点** ⇒ 用 BF、一次跳到 F ⇒ 下一个是 G
  ⇒ 队列 = `A, BF, G` ✓。**不需要"父的孩子一个不缺"那道闸** ✓：区间就是覆盖范围本身 ✓ ——
  孩子必然在父的区间里、且**左端对齐**（压缩只吃连续的段），所以"父的左端 = 这条的左端"就敢用父 ✓。

- **跳过去靠查区间（O(1)），不靠"数"** ✗：先按 `id` 把两端换成下标 ✓，然后 `index = end + 1` ✓。
  升到更粗的那一层只看一条：**祖先与自己的左端同起点**（左端不同 ⇒ 父盖的前半截已经发过了，那是同一批孩子里的老二）✓。
  指针悬空 / 区间缺失（老数据）/ 区间不从这条起 ⇒ 这一条**照原文发**、往前一步 ✓（宁可细，不许漏）。

- **摘要**必须**记区间**（`begin_message_id` / `end_message_id`）✓ —— **这就是定案**（2026-09-29 改的）：
  早先"不许加起止字段"的理由是"可推导"，但**推导的代价是 O(路径长 × 摘要数)**（要"数过去"、还要靠
  "父的孩子一个不缺"兜底）✗；记一次区间换来装配与**删除级联**都是 O(1) ✓。
  区间被"撞坏"的两种情形都有出路 ✓：消息**被删** ⇒ 级联把该摘要连同父链作废（同一份计算，见不变量 4）；
  消息**被改写** ⇒ 只标 `dirty`、**照用** ✓（`id` 不变 ⇒ 区间仍然成立）。两端可空 ⇒ 装配退回逐条走原文 ✓（不会静默错）。
- **装配时的行走**（`state.BuildOutgoing`）：按 `id` 顺序往前走 —— 这条有 `summary_id`、且它的**区间左端就是这条** ⇒
  升到最粗的左端对齐的祖先、跳到区间右端之后；否则发原文。"跳过"是查区间，**不再"数过去"**。**能取粗的不取精，但宁可用细的也不许漏内容**。
- **MASK（术语）**：有摘要覆盖的那一段，装配时**不发明文**、只发摘要的正文 —— 存放处一个字节都不动，被"遮掉"的只是**这一次请求**。
- **只有 Compact，没有"丢"**（铁律）：不存在"少发一段没人代表的内容"。超预算 ⇒ **先压再发**；真压不动 ⇒ **报错原路返回**，不做静默补救。
- **粒度：用户态按块，算法层是 Message**（2026-10-01 定）：块在 `assistant → user` 交界处切（`U1 A1 | U2 U3 A3 | U4 A4 A5`）。
  ① **手动入口（用户态）只收整块对齐**的区间，`{"blocks": N}` 也只数块；② **最后那个开着的块永不压**；③ 章 = 凑够 N 个块（只数块，不按 token）；④ 块是**推导**的、不入库（需要指一个块时用两端消息 id）。
  ⇒ **细粒度能力留在算法层**：摘要的区间两端就是 message id，单条 Message 的摘要**合法**（存 / 装配 / 显示都支持）——"按块"是**入口**的约束，**不是数据层的约束** ✗。
- **摘要里不存状态**：状态是**端点**的属性，不是段的属性 —— 存进去就是第二个真相来源，改一条旧消息它就过期。压缩的模板里也就写死这一句：「不要把世界状态写进梗概」（`DefaultTemplate`）；上游万一还是写了 `<state>`，落库前会被 `statelang.Scan(...).Cleaned` **剔掉**。
- **真需要快照时它得是独立系统**（`state_snapshots`：从第一条推起、能接着上一个快照往后推进；覆盖的消息一经编辑即失效）。**现在不做** —— 现演的代价足够低。
- **参数**：`compact_blocks = 10`（单位是块；`config.json` 的 `chat.compact_blocks`，不给块数时用它）；预算 = 模型上下文 − 输出预留（缺省 4096）；触发阈值用户设（**缺省 = 预算 × 0.8**，`chat/usage.go` 那条算式）；停手线 / 终保护区（阈值 × 0.8 / 最近约 10k token）**还没实现** —— 自动触发没落地之前它们只是名字。
- **剪枝**（未做；**线性 + 区间摘要之后前提变了 ⇒ 要重做设计**）：早先的形态是"压缩即定稿 ⇒ 只留当前路径上的孩子、其余子树删掉"，
  但**删消息的级联会把盖住它的摘要一起作废** ⇒ "压完就删旧消息"等于把刚落的摘要也删了 ✗。
  要真做，得先想清"删了之后谁来代表那段"（现在只有 Compact，没有"丢"）⇒ **先当没这回事**。三条旧条件里仍然成立的两条：
  ① **同一事务**；② **动手前先导出** `data/archive/*.json`；③ 报"剪掉 N 条（已导出）"，**不许静默**。
- **次序**（✅ = 已经做到；**其余一律 ✗**）：P1 预算 + 占用 ✓ · P1.5 导出/归档 ✗ ·
  P2 摘要 ✓（压缩：手动、按块；**自动触发还 ✗**）· P3 金字塔 ✓（合并写侧 2026-10-01 落地：同层顶层摘要并成父）· 清原文 ✗；
  另外**装配侧按区间跳** ✓ 与**删除 / Copy 时对摘要的处理** ✓ 早就在跑。

## 与上游通信

**只许用成熟的外部库**：HTTP 一律标准库 + 现成的 SSE 库（**不手搓协议解析**）；数据结构用 serde/encoding-json 映射成纯数据形状。**不自己写传输层、不自己写协议解析**（手写协议解析是 bug 温床）。
超时别忘：`providers.json` 的 `timeouts`（缺省 connect 15s / total 300s）。**不设总超时 = 上游卡住、界面转一辈子**。
**辅助调用另有自己的短上限** ✓（`title` 10s / `compact` 60s，常量就在各自包里）：它们跑在**这一轮翻 idle 之前**（`title.Auto` 排在 `chat.run` 的 `Finish` 前）⇒ 只靠渠道那个 300s 的话，上游"接了不回"会把这一轮按住 5 分钟不翻 idle。
`usage` 各家的字段名不一（缓存就有 `prompt_tokens_details.cached_tokens` 与 `prompt_cache_hit_tokens` 两种），**归一化只有一处**；前端只管读 `cached_tokens` 这些键。成本算不了：API 不返回 cost。

## 怎么"像编码 Agent"：实测出来的请求形状

有些网关（订阅型的 provider）会按**客户端指纹**放行。以下是从 `deepseek-ai/deepseek-harness` 与 `badlogic/pi-mono`
的源码里**读出来的**（两家共用同一套请求层：dsh 的 `llm-pi-ai` 就是 Pi 的 `@earendil-works/pi-ai`）—— 不是猜的。

**实测（OpenCode GO 的网关日志，2026-09-29）** —— 同一台机器，两个请求：

| | 被**拒** ✗ | 被**允许** ✓ |
|---|---|---|
| `user-agent` | `node-fetch` | `pi (linux 7.1.8+deb13-amd64; x64)` |
| `accept` | `*/*` | `application/json` |
| `x-opencode-client` | （无）| `pi` |
| `x-opencode-session` | （无）| `01a0e965-4156-741d-a8bd-5e16f28eb90d`（**UUIDv7** —— 和我们的 `sessions.id` 同构 ✓）|
| `content-type` | `application/json` | `application/json` |

⇒ 网关按**客户端身份**放行：`user-agent` + 它自家的两个 `x-opencode-*` 头；`accept` 也要像那么回事。

**但 2026-09-29 拿真 key 打过之后，结论收窄了** —— **硬闸只有一个：`x-opencode-session`**：

| 变体 | 结果 |
|---|---|
microchat 默认头（`user-agent: microchat/0.1.0` + `x-opencode-client: microchat` + 会话头）| **200** ✓ |
逐字装 Pi（`pi (linux …; x64)` + `x-opencode-client: pi` + 会话头）| 200 ✓ |
**裸库名**（`user-agent: Go-http-client/1.1` + 会话头）| **200** ✓ ← 库名实测**没被拦** |
只留 `user-agent` + 会话头（不带 `x-opencode-client`）| 200 ✓ |
**缺 `x-opencode-session`** | **400 `MissingSessionID`**：*"Request is missing x-opencode-session and cannot be routed efficiently"* |

⇒ 官方文档那句"别用通用库名"目前是**要求而非强制**（我们仍照做 —— 迟早会真拦）；
`x-opencode-client` 也不是必需。**`/models` 与 `/chat/completions` 都验过（真回话 + 真 usage）**。
usage 的真实形状：`{prompt_tokens, completion_tokens, total_tokens, prompt_tokens_details:{}}`（无缓存命中时该对象为空）。
**会话 id 每会话一个**（就是 UUIDv7 ✓ 我们现成有 ✓）。
`user-agent` 里那段 `(linux <release>; x64)` 是客户端自报的运行环境 —— 我们**写死**即可（不必忠实反映本机）。

**源码里的形状**（`deepseek-ai/deepseek-harness` 与 `badlogic/pi-mono`，两家共用同一套请求层）——作为背景：

| 发什么 | 值 / 条件（源码出处）|
|---|---|
| `User-Agent` | `product/version (+url)`，如 `deepseek-harness/0.1.0 (+https://github.com/deepseek-ai/deepseek-harness)`。注释明说**不许放密钥/路径/会话 id/提示词**（`llm/src/attribution.ts`）|
| `session_id` / `x-client-request-id` / `x-session-affinity` | 都有值 = 同一个**会话 id**；前一个只在 `sessionAffinityFormat === "openai"` 时发（`api/openai-completions.ts:772`）|
| `x-session-id` | OpenRouter 那种格式只发这一个（同上）|
| 体：`stream: true` | 一直是流式 ✓ |
| 体：`stream_options: {include_usage: true}` | 除非该 provider 明确不支持（默认发）—— 流式下要 `usage` 就得要它 |
| 体：`prompt_cache_key` | **只在 `api.openai.com`（或长缓存场景）**发 —— 乱发可能被别的网关拒 |
| 体：`store: false` / `prompt_cache_retention: "24h"` | 看 provider 能力，能支持才发 |
| SDK 指纹 | 它们用官方 `openai` JS SDK ⇒ 线上还有 `x-stainless-*` 那套。**我们复现不了**（版本漂移），要查就抓一次包 |

### 内建 provider 预设（2026-10-01 起按 `vendor` × `protocol` 两轴；`kind` 只剩废弃读入）

| vendor | base_url（缺省，可覆写） | 密钥从哪来 | 每请求必带的头 |
|---|---|---|---|
| `dummy` | （无，不联网） | 不用 | 无 |
| `openai-compat` / `custom` | （无，全靠配置给） | 按配置 / 环境 | 无（custom 可覆写端点跑 systemone，见下） |
| `opencode-go` | `https://opencode.ai/zen/go/v1`（官方 discussion 里有人实测这条路 200）| `OPENCODE_API_KEY` | **`x-opencode-session: {{session_id}}`（网关必需** —— 注释原话"required per-conversation routing header"）+ `x-opencode-client: pi` + `user-agent: pi (linux <release>; x64)` + `accept: application/json` |
| `opencode` | `https://opencode.ai/zen` | 同上 | 同上 |
| `openrouter` | `https://openrouter.ai/api/v1` | `OPENROUTER_API_KEY` | `HTTP-Referer: https://pi.dev` / `X-OpenRouter-Title: pi` / `X-OpenRouter-Categories: cli-agent`（**可选**，归属用 —— Pi 那边还跟着"安装遥测开关"一起关）；systemone 走 `https://openrouter.ai/api/alpha/decisions`（verbatim 覆写） |
| `deepseek` | `https://api.deepseek.com` | `DEEPSEEK_API_KEY` | 无（直连官方：标准 UA + `stream_options.include_usage`；思考回传用官方的 `reasoning_content` —— OpenAI 兼容，不会话头、不归属头） |
| `openai` | `https://api.openai.com/v1` | `OPENAI_API_KEY` | 会话亲和三条 + `prompt_cache_key`（长缓存才 `24h`） |
| `ollama` / `lmstudio` | `http://127.0.0.1:11434/v1` / `http://127.0.0.1:1234/v1` | 不用 | 无（本地） |
| `typesafe`（JEV 决策） | `https://api.typesafe.ai/v1/systemone`（**verbatim 整条 URL，原样 POST，不拼路径**） | `TYPESAFE_API_KEY`（备 `JEV_API_KEY`） | 只有 `Content-Type` + `Accept: application/json` + `Authorization`（无会话头：上下文在 state 里自带） |
- **两轴**：`vendor` = 找谁（端点预设 + key env + 内建头）；`protocol` = 说什么话（`openai-chat-completion` 缺省 / `systemone` / 占位中的 `openai-response` 等）。
  渐进式配置：先选 `protocol`（干什么），再按兼容矩阵选 `vendor`（找谁），有预设的只输 `api_key`，只有 `custom` 才要 `base_url`。
- **兼容矩阵即校验表**（`Provider.Validate`，错配 loud error）：chat-completion ← 除 typesafe 外全部 vendor；systemone ← `typesafe` / `openrouter` / `custom`；
  其余 protocol（`openai-response` / `anthropic-messages` / `gemini-generate-content`）是占位，选了就报"暂不支持"。
- **不需要搬它们的模型目录** ✓：模型列表是**发现**来的（`GET /models` ⇒ `registry`）；内建预设只提供
  **端点 + 头 + 认证从哪取**，每家十几行。**systemone 例外**：JEV 没有统一 `/models` 口径 ⇒ `refresh` 直接跳过（不算错）。
- **单载体多协议 = 写多条** ✓（2026-10-01 OpenRouter 真测）：一个 vendor 挂多个 protocol 时（如 OpenRouter 同时有 chat / decisions / images），
  **每个 protocol 写一条渠道**（`base_url` 各自 verbatim，key 共用；模型归属照抄 `provider`/`model` 两格）。会话绑的是"渠道+模型"两格，
  跨载体没有统一发现口径本来就是现状 —— **结构不用动**。
- **身份开关（服务器级）**：`config.json` 的 `server.identity` ——
  `""`（默认）= **`pi`**（用户 2026-09-29 定：默认就照 Pi 的形状）/ `"microchat"` / `"bare"` = 什么都不装。
  `providers.json` 里单个渠道的 `identity` **覆盖**它（没写就跟随服务器）✓。视图回**生效值** ✓。
  （诚实标注：文档建议用「自己的 UA」—— 默认选 `pi` 是用户的决定；`microchat` 一行配置可切回。）
- **渠道级三个覆写选项**（`providers.json` 每条上写；都是「空 = 跟随默认」的三态）：

  | 字段 | 三态 | 作用 |
  |---|---|---|
  | `client_ua_override` | 空 = 不用；有值 = **逐字用它** | 覆写 UA，**优先级最高**（高过 `identity` 与服务器默认）|
  | `session_header` | `nil` = 按 vendor（opencode 系 ⇒ `x-opencode-session`）；`""` = **明确不发**；有值 = 用这个名字 | 会话 id 透传成哪个头 |
  | `reasoning_field` | `nil` = 按 vendor（opencode 系 ⇒ `reasoning_content`；deepseek 恒 `reasoning_content`）；`""` = **不回传**；有值 = 用这个名字 | 回传思考用哪个字段名 |
- **Pi 的 UA 里没有版本号** ✓ —— 那截 `<release>` 是**本机内核**（`pi (linux 7.1.13+deb13-amd64; x64)` ✓）：
  `providers` 里读 `/proc/sys/kernel/osrelease` ✓ 天然逐字一致 ✓。

### 模型列表：**一启动就重建一遍**（2026-09-30 定）

"每当我们打开软件，软件就会试图向每个 provider 询问有哪些模型可用" —— 会话里绑的是模型的
**字符串 id**（`provider` + `upstream_id`，例如 `opencode-go` / `deepseek-v4.1-flash`），**不是 UUID** ✓
⇒ 列表**随时可以重建**，重建不会碰到已有会话 ✓。

- **怎么刷只有一份**：`registry.RefreshOne`（拉 `/models` ⇒ `store.RefreshDiscovered`，**只写发现列**）。
  三处共用它：HTTP 的 `POST /providers/{id}/refresh`、**启动时的自动刷新**、`-debug refresh-models`。
  **别写第二份** ✗ —— 第二份几乎必然漏掉"不动用户列"或"这次没见到的删行"，而漏了都是**静默**的。
- **路由表是另一个命令**：`-debug refresh-routes [provider_id]`（拉 models.dev 快照 ⇒ `model_route` 落库，
  只认 opencode-go/opencode；建表按需，`IF NOT EXISTS`）。两个命令**各管各的**：
  models 表管"有哪些模型"，model_route 管"每个模型走哪条 API" —— 别藕断丝连混成一个。
  快照 5MB，一周手跑一次足够；**启动时不跑**（浪费）；快照失败不拦别家（旧行留着）。

**客户端的身份规则（OpenCode 官方文档原话）**：

- **用自己的 user agent 标识**（例：`my-coding-agent/1.0`）——**不能是通用 SDK 或 HTTP 库的名字**
  （`node-fetch` / `Go 的默认 Go-http-client/1.1` 都属于被拒那类）。
  ⇒ **我们默认报 `pi` 的形状**（用户定）；`microchat/<版本>` 也可选（文档更推荐那种做法）。
- `x-opencode-session` = **每会话稳定 id**（官方唯一硬要求）；官方已验证的客户端有 Hermes / Claude Code /
  Codex / ZCode / **Pi** / jcode / Kilo Code CLI。

**Pi 的会话 id 模式（照它对齐，2026-09-29 读源码得到）**：

```ts
function createSessionId(): string { return uuidv7(); }   // 裸 UUIDv7，无前缀
assertValidSessionId(id)  // 只允许 [A-Za-z0-9._-]，首尾必须是字母数字
```
载入会话时沿用文件头里的 id（跨恢复不变）；**fork / 建分支时铸新 id**。
⇒ **我们的 `sessions.id` 与它逐字节同形（裸 UUIDv7）—— 已经对齐，不用改。**

**"子调用拒绝缓存"的确切做法**（照 Pi，2026-09-29 读源码）：**不是 header**，是请求选项 **`cacheRetention: "none"`**
（`type CacheRetention = "none" | "short" | "long"`）。Pi 的调用点原话："Avoid cache writes for one-off summaries"。
`"none"` 的实际效果（只在**体**里，且只对该 provider 支持时）：不发 `prompt_cache_key`、不发
`prompt_cache_retention: "24h"`、Anthropic 格式的 `cache_control` 断点直接不生成。
**`x-opencode-session` 照发**（头是路由、体是缓存，两回事）；但**会话 id 必填** ✓ —— 会调模型的 Task 缺了就在
`task.Begin()` 当场报错 ✓（**不学 Pi 的"没有上下文就铸一个新的"** ✗ —— 见 `DEFINE.md`「任务（Task）与两族调用」✓）。
⇒ 对 OpenCode GO 来说 `"none"` 是 **no-op**（它那几个参数本就只对 api.openai.com / Anthropic 系存在）——
**别为此发明 header**：Pi 没做，我们也不做。

**2026-09-29 实测补充（omp 17:30–17:36 的真日志）**：

- **缓存命中与 session id 无关** ✓✓：同一 session 的两次调用 **0 命中**；而**另铸了一个 session** 的第三次反而命中
  **7552** tokens ⇒ 网关的缓存是**按内容前缀**算的，session id 只负责**路由/亲和**。所以"给辅助调用另开会话"
  对缓存**没有影响**（别为缓存去分子会话）。**但会话 id 仍是路由/亲和的硬要求** ✓ ⇒ 结论不变：
  会调模型的 Task **照样骑同一个会话 id** ✓（见 `DEFINE.md`「任务（Task）与两族调用」✓）。
- **真实客户端确实会中途另铸会话**：omp 在同一个会话里出现了两个 session id，且两者**同一秒铸造**
  （UUIDv7 前 8 位相同 `01a0ec83`）⇒ 它确实为某些调用开了**侧会话**。**我们仍照 Pi（共用一个）**：
  两种做法网关都收，但"一个会话对网关表现为一个会话"更像人类用法。
- **`reasoning_effort` 的下限是 `low`**：设 "off" 也只到 low，而且 low 下仍花 46–198 reasoning tokens
  ⇒ 这个模型**关不掉思考**。我们暴露 low/medium/high/max 即可，别假装能关。
- usage 的真实三段：`Input`（非缓存部分）+ `Cache read`（prompt 的**子集**）+ `Output`（含 `Reasoning`，**子集**）
  ⇒ 归一化与卡片脚注都按这个口径显示。

**辅助调用（标题/摘要/压缩）用同一个会话 id**（Pi 原话：routing session ID "forwarded **without enabling
prompt caching**"）。**不要为能力造子 session id**：网关的会话 id 是**路由/亲和键**，不是缓存键
（缓存按**前缀**算）⇒ 同一个 id 不会污染缓存；反过来"一个会话对网关表现为 N 个 session"才像异常流量。

**会话头由 provider 层自动加，不是配置项**（`deepseek-harness` discussion #5495 定死了口径）：

- **09/05 起，没带 `x-opencode-session` 的请求直接报错**（不是警告）；要求"每会话一个稳定 UUID"；
- **线上值 = 裸 UUID**（社区实现把 `session-<uuid>` 剥前缀再发；我们的 `sessions.id` 本来就是裸的 ⇒ 零转换）；
- **生命周期**：跨轮次 / 恢复 / **压缩** / 重试**都不变**；**新会话、fork、子任务**用**新** id
  ⇒ 对照我们：重发、编辑、删消息 = 同一会话 ⇒ **同一 id**；**Copy 出来的新会话** = 新 id（天然满足 ✓）；
  （**我们不做"给子任务另开会话"** ✗：会调模型的 Task 一律骑 `sessions.id` ✓，见下面第 1 条 ✓）；
- **辅助调用骑同一个 key**：标题生成、摘要压缩也要带**当前会话**的 id（不是每条请求随机）；
- 归口：**provider 层按 vendor 自动加**（维护者原话："该由 pi-ai 归一化各家的特殊需求"）；
  动态的会话头**压过**静态同名头；`opencode*` 之外不发。
- 出处：`github.com/deepseek-ai/deepseek-harness/discussions/5495`（OpenCode 员工开的，含 09/05 硬期限）。
- 会话 id 见上面那条规则（`SessionIDFor`）；`user-agent` 里那截
  `<platform> <release>; <arch>`（如 `linux 7.1.8+deb13-amd64`）**写死**即可。
- **一个真实分歧**：OpenCode GO 上有的模型走 **Anthropic messages** 协议（不是 OpenAI 兼容）——
  我们只做 OpenAI 兼容的话那些模型用不了；真要用得在 `providers` 里再加一种协议适配。
- Pi 的 UA 出处：`packages/ai/src/utils/pi-user-agent.ts:18`；会话头包装：`providers/opencode-headers.ts`；
  归因头：`coding-agent/src/core/provider-attribution.ts`；会话 id：`agent/src/harness/session/session.ts:237`。

**思考（reasoning）两面都要做**（照 Pi；OpenCode GO 上已实测字段名 ✓）：

- **往下（给前端）**：思考走**独立增量**（`turn.AppendReasoning` ⇒ 「思考中…」动画），落档进 `messages.reasoning` ✓；
- **往上（回传上游）**：assistant 消息要带着**当时的思考**一起回传 —— Pi 原话：`reasoning_details` 是
  *replay metadata*（不这么做，多轮推理就断了）。字段名认三种（`reasoning` / `reasoning_content` / `reasoning_text`）；
  **OpenCode GO 实测用 `reasoning_content`** ✓（非流式的 `message` 里有 ✓、流式 delta 里的键也是它 ×35 ✓、
  而且**历史里带它网关照收** ✓ ⇒ 回传安全）。
  预设里用 `reasoningField` 表达（openai-compat 系为 `""` = 不回传 ✓）。

**DeepSeek 那条"字段必须在"的规矩，OpenCode GO 上实测不强制** ✓（2026-09-29 拿真 key 打的）：

| 场景（assistant 带 `tool_calls`）| 结果 |
|---|---|
不回传 `reasoning_content` | **200** ✓ |
回传 | 200 ✓ |
回传**空字符串**（Pi 的保险）| 200 ✓ |

⇒ DeepSeek 官方 API 的硬要求（带工具调用时必须回传思维链）**在这个网关上没有被强制**。
但**保险照做**（Pi 就是这么防的）：**模型名含 `deepseek` 时**，assistant 消息若没有思考就补一个
**空字符串**（`reasoning_content: ""`）；非 deepseek 系**一个字段都不塞**（有些上游见到不认识的字段会 400）。
整块关掉的办法：渠道配置里 `reasoning_field: ""` ✓。

**工具（Tools）**：**不在提示词里** —— 是请求体顶层的 `tools` 数组；模型的回话是助手消息里的 `tool_calls`，
结果以 `{role:"tool", tool_call_id}` 回填。细节（形状 / 开关 / 与 Pi 的对照 / 实测记录）见 **`reminder/tool-calls.md`**。
两条必须记住的：**历史里出现过工具 ⇒ `tools` 参数必须在**（哪怕 `[]`）；**不认识的字段默认不发**（有些上游直接 400）。

**microchat 怎么落**（都属于 providers 那一波）：
1. 会话 id 的**规则**（2026-09-29 定；**Task 那节改定 ⇒ 一个会话对上游就只表现成一个 session** ✓）：
   **凡是会调模型的调用（主聊天、摘要、标题、判断、将来的子 Agent）一律带 `sessions.id`** ✓ ——
   **不许按提示词派生第二个 id** ✗（早先 `providers.SessionIDFor(会话 id, 提示词)` 那个 UUIDv5 方案**作废** ✗：
   "一个会话对网关表现成 N 个 session"正是他们滥用监控盯的形状 ✓；口径见 `DEFINE.md`「任务（Task）与两族调用」✓）。
   **绝不为"没有会话上下文"随机铸 id** ✗（Pi 在 `compaction.ts:654` 就是这么干的，**我们不学这一步** ✗）：
   会调模型的 Task **SessionID 必填**，在 `task.Begin()` **当场断言**（缺了就报错 ✓）；
   只有**不调模型**的 Task（`refresh_models`）才允许没有会话 ✓；
2. `providers.json` 的 `headers` 支持**占位符**（白名单枚举里加 `{{session_id}}`）⇒ 配置里写
   `"x-session-affinity": "{{session_id}}"` / `"x-opencode-session": "{{session_id}}"` 就把会话头配齐了，**不用新概念**；
   静态头（`x-opencode-client` ✓ `accept` ✓ `user-agent` ✓）直接写死在配置里 ✓。
   **注意**：Go 里不设 `User-Agent` 就是 `Go-http-client/1.1` ✗ —— 和 `node-fetch` 同属"被拒"的那类；必须显式设上；`Accept` 也一样（Go 默认不发）。
3. 体里那几样做成 `providers.json` 的 `body_extras`（原样并进请求体）+ `stream_options.include_usage` 默认开；
4. **`/debug/last-payload` 必须连请求头一起回显**（打码）—— 不然"为什么 403"只能靠猜。

## 决策模型（JEV）：**不是聊天模型** —— 已落地 ✓（2026-10-01：`protocol: systemone` + `task.KindJudgement` + `internal/judge` 裸调用）

`typesafe/jev` 那类是**决策模型**（状态 + 类型化问题 → 概率），不生成文本、不能驱动会话、不走 `refresh`（没有统一 `/models` 口径 ⇒ 跳过不算错）。
落点：`vendor: typesafe`（或承载它的 openrouter / custom）× `protocol: systemone`（verbatim 整条 URL 原样 POST `{model,state,questions}`）；
`judge` 能力的 Task 面就是 `task.KindJudgement`（"判断"，要会话 id —— 调外部模型就要归属可查）；裸调用在 `internal/judge`（10s 超时、不重试、失败原路返回）。
规划中的用途与四条规矩（只动尾部 / 失败降级 / 产出不进历史与变量 / 阈值实测校准）见 **`reminder/jev.md`**。**裁决（阈值/NPC 动作集）还没做** —— 那是第二刀。

## 界面该长什么样（**口径**；Godot 版照此对齐，目前尚未实现）

**TUI 一屏就三块**（2026-09-30 用户定稿；**左栏已删** ✗ —— 会话选择只走 `/resume`，输入框上方**不提供**"上下选会话"）：

```
消息区：**只显示当前会话的对话**（最近的贴着输入框，从下往上排；装不下的**整块**不显示，
        顶部如实提示「（上面还有 N 条）」；会话还没有消息时给一句话）
> 输入行（命令与消息都从这儿走）
────────────────────────────────────── （满线 `─`；**窄屏（< 60 列）或 `TERM=dumb`/非 TTY 退回 ASCII `-`**）
会话名 | 短id | 渠道/模型 | 12.3k/131k (9.4%) | 生成中 2.4s / 空闲 | 已连接 v0.1.0 | 最近一次动作
```

- **进 TUI 先做一次"启动编排"：先清空会话、再建一条真会话 —— 顺序不能反** ✓（2026-09-30 用户定）：
  ① 把【**0 条消息** 且 **标题为空**】的会话 `DELETE` 掉（**带标题的空会话留着** ✗ —— 那是用户 `/rename`
  改过名的，删了就把命名丢了；判据就这两条，**两条都在 `GET /sessions` 的列表项里**
  —— `messages` 那一格就是条数 ⇒ 编排**只看列表**，会话再多也**只发一次**请求，
  **别**逐条会话去拉 `GET /sessions/{id}/messages` ✗）；
  ② `POST /sessions`（空体 ✓ ⇒ 后端套 `config.json` 的 `defaults`：默认 Agent、默认 provider/model）
  建一条**真的**空会话并**进去**（选中它、拉它的消息 / 占用 / 提示词）。
  ⇒ 从此**永远活在一个真会话里**；"没有会话"只是**兵灾态**（建那一条失败了 / 列表暂时拉不到 ⇒ 命令**如实提示**，
  `/new` 是出路，**别 panic、也别假装在会话里**）。
  **为什么要这样**（这是这条规矩存在的全部理由）：不建那条真的，"空会话"就是一个**只存在于客户端**的状态
  （库里没有那一行）⇒ `/outgoing` `/rename` `/system` `/cut` 每一处都得先判"没有会话"
  （用户实跑撞上的"当前是空会话，没有载荷可看"就是那里漏出来的），而 `-debug` 那条路拿的是**真 id**
  ⇒ 两条路行为不一致。先建一条真的 ⇒ **那一整类补丁全可以删掉**，复杂度不再膨胀。
  **顺序反了会怎样**：先建后清 ⇒ 刚建的那条（0 消息、无标题）正好满足"该清"的判据，当场被删掉。
- `/delete` 删完**立刻再建一条并进去** ✓（没有"删掉之后回到空会话"这回事）；`/new` 仍是
  "当前会话**还没有消息** ⇒ 无效" ✓（语义变成"你已经在一条新会话里了"）。

- **会话状态行**（底下 1-2 行，**最多两行**）：前五个字段是**当前会话**的；后两半（`已连接 vX` 与"最近一次动作"）**互不顶替**（旧口径也这么定的）。一行放不下就折两行。
  里面还搭一个 **TG 指示段** ✓（2026-10-02 加，10s 自续期轮询 `GET /config/telegram`，没配就一个包都不发）：
  `enabled && running` ⇒ `TG ✓`（绿）/ `enabled && !running` ⇒ `TG ✗`（红）/ `enabled && !has_token` ⇒ `TG 缺token`（红）/
  `has_token && !enabled` ⇒ `TG 关`（暗）；没配 / 没拉过 ⇒ **不显示**。
- **输入行有光标**（2026-09-30 加 ✓ —— 早先是"只会在末尾追加"，`←`/`→` 移不动、也看不见光标）：
  打字**插在光标处**、退格删**光标前**一个 rune、`Delete` 删**光标处**那个、回车 / `Esc` 之后光标归 0；
  键位 `←`/`→` 一次一个 rune，`Home`/`Ctrl+A` 到首、`End`/`Ctrl+E` 到尾。
  **粘贴走括号粘贴** ✓（2026-10-02 加：终端把粘贴整段包着发过来，`tea.PasteMsg` —— 与打字同一条路，插光标处、按 rune 算；picker/viewer/confirm 开着不吃）。
  - 挪的是**真终端光标**（Bubble Tea v2 的 `View.Cursor`）✗ **不是**画进正文的反色方块 ⇒ `View().Content` 一个字节都不变（逐字节断言因此一个字都不用改）。
  - **列 = `2 + runewidth(光标前那段输入)`**（`> ` 前缀两格；CJK 一个字两格 —— 按 rune 数算会指到字中间，见「踩过的坑」）；输入太长被截断时夹到最后一格。
  - 挑选项（`/resume` `/model`）与查看器（`/outgoing`、确认框）铺满消息区时**把光标藏掉** ✗（输入行那时不是活动面）；**生成中照旧显示** ✓（输入行还能用，只是提交会被 409 顶回来）。
- **消息区最上面那条「系统提示词」**（调试用，2026-09-30 定）：进会话 / 会话切换时拉一次 `GET /sessions/{session_id}/prompt`，把它当成一条 `role=system` 的消息摆在**消息区最上面**（标签「系统提示词」用 `system` 那档 = **暗色**，正文保持默认色）；与别的块同一条规矩 —— **块不劈开、装不下就从顶部挤掉**。`/system` 切换显示，开关**持久化**在 `~/.config/microchat/tui.json` 的 `{"show_system_prompt": true}`（**默认开**；`*bool` ⇒ "没写"≠"写了 false"）——界面偏好**不进**后端 `config/` 与 `data/`，也**不动** Godot 那份 `frontend.json`。
- **`/rename <新名字>`**：给当前会话改名 —— 走**已有的** `PATCH /sessions/{session_id}`（`{"title": …}`），**不新增路由**；名字里可以有空格（`/rename 我的 新名字` 整串拼回、带引号则剥掉那对引号），**空名字 ⇒ 拒绝**（会退回"还没起名"，说不通）。改完就是"用户改过名" ⇒ 后端的自动起标题**永不再覆盖**（不变量在后端，客户端不多事）；回执那条会话**就地换掉本地列表** ⇒ 状态行里的会话名**立刻**跟上（不等下一次刷新）。
- **上下文占用那一档**（`12.3k/131k (9.4%)`，照 Pi 的形状）夹在 `渠道/模型` 与 `生成中 / 空闲` **之间**，数据来自 `GET /sessions/{session_id}/context`（**只取数字**，别为一个数去拉整份 `/outgoing`）。
  **拉取时机只有两个**：**进会话时**、**一轮结束后**（占用只在整段落库后才变 ⇒ 别跟着 300ms 的轮询一起拉）。
  **`over_budget` ⇒ 这一段用红**；没拉到就先不显示这一档（**不许编数**，也别去打扰底栏）。
  换会话时那份还旧着 ⇒ 记着它**属于哪条会话**（`contextFor`），对不上就不许拿来画。
- **上色口径（2026-09-30 定）：只有前景色，绝对不给背景上色** ✗ —— 只用 16 色基础 ANSI（`30–37` / `90–97`）+ 粗体 / 暗色属性，不用 256 色 / truecolor、不用渐变、更不用 `4x` 背景：

  | 元素 | 前景 | 备注 |
  |---|---|---|
  | 会话名（状态行开头）| **粗体** `1` | 不上色也行，亮度就够拉层级 |
  | 满线；次要信息（短 id / 计数 / 百分比 / `渠道/模型` / `已连接 vX`）| **暗色** `2` | 不抢内容 |
  | 你自己（`你：` 署名）| **青** `36` | |
  | 助手（`助手 2.4s：` 署名）| **洋红** `35` | 耗时数字用暗色 |
  | 生成中（`生成中… 1.2s`，含「`/stop` 停止」）| **黄** `33` | |
  | 错误 / 失败（状态行那句**还摆着的**失败、删除预览的「不可逆」）| **红** `31` | |
  | 选中的列表项（挑选项 / 命令面板）| **粗体 + 青** `1;36` | 未选中保持默认 |
  | `/outgoing` 查看器 | `type=summary` **黄** / `type=system` **暗** / `type=message` 默认 | 摘要是压缩过的，要一眼看出来 |

- **降级两档**（一次判清，只有一处读环境）：
  · **颜色**：`NO_COLOR`（业界约定，设了就关）、`TERM=dumb`、非 TTY ⇒ 一律纯文本。
    **`./microchat -debug …` 的输出永不带色** ✗（它是 JSON，给管道与断言读；好在 debug 根本不过 TUI）。
    实现：`styler` 一个开关、**零值即关** ⇒ 测试里给零值 ⇒ `View()` 仍是纯文本（现有逐字节断言一个字都不用改）。
  · **字符集**（2026-09-30 补）：`TERM=dumb`、非 TTY ⇒ **界面骨架退回 ASCII** —— 满线用 `-`（不是 `─`）、
    重摇位次标记用 `< 2/3 >`（不是 `‹ 2/3 ›`）；**与屏幕多宽无关**（窄屏那条是另一回事，见 `rule`）。
    `NO_COLOR` **不在**这一档 ✗：它说的是"别给我上色"，不是"我不认 Unicode"。
  · 两档都在 `Run()` 里判一次（`detectTerminal` ⇒ `styler` + `model.ascii`），**不散落在渲染代码里**
    （那会让 `View()` 不再是纯函数）。**其余 Unicode（中文标点 `…` `·` 之类）不降级** ——
    那是文案，不是骨架，为它重写一遍文案不值当。
- `/resume`、`/model` 的挑选项与 `/outgoing` 之类的查看器照旧**铺满消息区**（不是挤在输入行上面一小块）。
- **生成中那条回复**是界面按状态**合成**的气泡（库里还没有它）：显示"生成中… 耗时"与「停止」；
  数据来自 `GET /sessions/{session_id}/status` 与 `GET /sessions/{session_id}/turn/text?from=N&think_from=M`（游标读，读不消费），每 300ms 一次，收到 `idle`/`error` 才一次性重拉消息。
  收到 `idle`、以及**生成中按停**（`stopped: true`）这两趟都**顺带静默刷一次会话列表**（`loadSessionsQuiet`：更新列表但**不动底栏**）—— 自动起的标题是后端在翻 idle **之前**写好的，不补这一趟左栏就一直停在「（还没起名）」（用户实跑抓到过）。
- **当前会话认 `id`、不认位置** ✗：`GET /sessions` 按 `updated_at` 排，发一句话就会重排 —— 存下标必然指到别人身上（用户实跑抓到过"发一句话后消息区一片空白"）。
- 会话视图（Godot 版）：消息列表（署名 = **该会话 agent 的名字**，不是"助手"）；上方一行**上下文占用**（`上下文 12.3k（上轮实测 11.9k）/ 40k`；超 80% 黄、超触发阈值红）**待做**。
- **消息级操作只出现在最后一条上**（线性会话里"从这里往后不要了"就是它）：`重摇`（可接受 / 丢弃）、`剪切`（删这条及之后）。
  消息级删除**必须先看删除预览**（`GET .../deletion-preview`）并让用户确认，再带 `last_deleted_message_id` 发 DELETE。
- **重摇的位次与退出** ✓（2026-09-30 消息模式、2026-10-01 摘要模式）：进重摇模式后，**被摇的那条消息旁**给一个位次标记
  `‹ 2/3 ›`（**只在模式内**、当前那版高亮 = 粗体 + 青，与挑选项同一条口径），状态行报「重摇中… 耗时」（拿 `/status`
  的 `reroll` 那一档或对应家族的 `GET`），消息区另起一块**合成**气泡（**不假装**正文在往外蹦：那一趟是非流式的）；
  命令 `/reroll`（消息：进模式并摇一次；已在模式里 ⇒ 再摇一版）、`/reroll switch <n>`、`/reroll delete <n>`、
  `/reroll list`（摊开每一版的预览，只读查看器）、**`/reroll off` = 显式退出**（清单，message 保留当前那版）；
  **摘要模式**同为 `/reroll-summary <idx>` + `switch / delete / off / list`（`idx` = 1-based 消息序号，上溯到根；
  摘要没有"那条消息"可挂标记 ⇒ 目标与位次看底栏 / `list` 查看器；**新消息不清摘要模式**）。
  **候选只在内存里** ⇒ 一发出新消息（消息模式）/ 删目标 / 切会话 / 重启后端 ⇒ 自然消失（界面靠 `rerollFor` 认会话，与
  `contextFor` / `promptFor` 同一条规矩）。
- **分岔 = 复制会话**（`POST /sessions/{session_id}/copy`）：没有"切分支"这回事 ✗（`current_leaf` 已删）。
- **卡片脚注**：`3.2s · 上行 1654 tok（缓存 1408 tok · 85%）· 下行 62 tok（思考 32 tok，含在内）`——只在有数据时显示。**单位是 token**（上游 `usage` 报的）；生成中气泡上「思考中… N 字」是**字符数**（流式帧里没有 token 数）——两处别混。`reasoning_tokens` 是 `completion_tokens` 的子集，不是另加。
- **思考**默认折叠成「思考（2.1s）」——放的是**思考用时**（受理 → 第一段正文），不是字数。
- 底部一排：`归档`、`压缩 [N] 个块` + `开始`（202 受理，底栏报结果）、`复制会话`。
  路由都在了 ✓（压缩 = `POST /sessions/{session_id}/compact`；复制会话 = `.../copy`）；⚠ **`归档`（剪枝前置）还没做** ⇒ 那个按钮先不要做。
  TUI 里对应的命令是 `/compact [N]`（不给 N 用 `chat.compact_blocks`）、`/copy`。
- 设置六页：**连接 / Agent / 能力 / 模型与渠道 / 前端设置 / 关于**。底栏常驻"**已连接后端 vX**"，后面跟 `·` 和最近一次动作的结果——两者**互不顶替**；刷新失败要带状态码与 `code`。
  「模型与渠道」页：顶部**服务端上下文口径**（`模型上下文` 兜底 + `摘要触发阈值`）；每个渠道一行（`获取模型` / `删除全部模型` / `编辑`）；下面逐个模型列上下文，可填**覆盖值**（留空 = 清掉）。优先顺序：**覆盖 > 上游发现 > 配置兜底**，只许一处解析。
- 渠道与模型是**一个下拉**（`渠道 / 模型`，`GET /models` 拍平给的就是这个形状）；provider 的 `name` 缺省回退 `id`。
- **界面的将来：TUI**（2026-09-29 定；**ImGui 方案已否决并删除** ✗ —— 依赖重、要图形环境、调试还得跟图形栈纠缠）。
- 选型：**Bubble Tea v2**（`charm.land/bubbletea/v2`）+ Lip Gloss / Bubbles / Glamour ——
  理由：`View()` 是**纯函数**（`model → string`），**屏幕不持有应用状态**（对比保留式控件树那种"控件自己带可变状态"），
  而且渲染结果能直接做**黄金测试**（与本项目"逐字节比"的脾气一致）。
- **落地形状（2026-09-29 用户定稿）：一个二进制、一种模式** —— 起来就**既对外服务、又在终端里直接聊**
  （`./microchat` ⇒ 监听端口 + 开 TUI）。**没有 `--headless` / `--remote` 这些开关** ✗，别复杂化。
  代码住 `core/internal/tui/`（**同一个模块**，不是独立模块）；TUI 与别的客户端走**同一条 HTTP 契约**
  （回环到自己那个端口）⇒ 接口天天被主力界面跑着，不会烂。
  · 唯一自动处理的情形：**没有 TTY**（丢进 tmux 重定向到日志那种）⇒ 不进 TUI，只服务；
  · TUI 模式下日志落到 `data/microchat.log`（别糊在界面上）；
  · 退出用 **`/quit` 或 ctrl+c** —— **没有裸 `q`**（会和打字打架）。
  · **单实例锁** ✓（2026-10-02 加）：同一个 `data/` 起第二个实例时，**SIGTERM 掉旧的**再接管
    （`<data>/microchat.pid`；僵尸/陈旧 pid 直接覆盖；等 3s 不退就报错说清 pid）。为什么必须：
    TG 长轮询同 token 只允许一个消费者（否则 `conflict` 刷屏）、SQLite 同库两写、端口只能一个听。
    `-debug` **不拿锁**（直操模式本来就允许多开）。
- 跑法：`go -C core run .`（一条命令跑起来；要二进制就 `go -C core build -o ../microchat .`）。**TUI 内的命令**照 Pi：`/command`。**只放已经有路由的命令**，没搬完的在 `/help` 里如实列出来
  （现在能用的：`/help` `/new` `/delete` `/cut` `/copy` `/rename` `/resume` `/model` `/providers` `/provider-add` `/provider-del` `/telegram-bind` `/telegram-bot-token-set` `/telegram-toggle` `/telegram-status` `/outgoing` `/state` `/usage` `/think` `/system` `/compact` `/reroll` `/reroll-summary` `/stop` `/refresh` `/quit`；
  还没做的：`/archive` —— `/fork`／`/tasks`／`/probe` 那几条**已砍**，不会再有
  （`/fork` 的位置由 `/copy` 接管）。
  `/delete`（删整条会话 —— **删完立刻再建一条并进去**）与 `/cut`（删一条及之后）都**不可逆** ⇒ 两个都先摊开、等 `回车 / y` 点头才动手；
  `/cut` 的摊开内容来自 `GET .../deletion-preview`（后端那份计算），执行时带回 `last_deleted_message_id` 核对）。
- 要抄 Pi 的**交互决定**（它的 TUI 是手搓的，库选择无参考价值）：CJK 宽度对齐 / kill-ring 编辑 / LaTeX 降级显示 / markdown 渲染。
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

- **光标列必须按显示宽度算** ✗：输入行的光标摆在哪一列 = `2`（`> ` 前缀）+ `runewidth(光标前那段输入)`；
  **不能拿 rune 下标当列用** —— `你好|` 按 rune 数是 2+2=4，实际该是 2+4=6 ⇒ 光标指到**字中间**。
  两个身份别混：model 里存的是 **rune 下标**（增删要精确到"第几个字"），摆到终端上的是**显示列**（`runewidth` 现算）。
  同一族的还有一条：光标每次重画都要**与正文同一份布局**算出来（面板把输入行顶下去时，行号也要跟着走）。
- **转义序列零宽 ⇒ "先排版、后上色"** ✗：`truncate` / 对齐是**按格**算的，而 ANSI 颜色代码占 **0 格** ——
  反过来做（先上色再截断）会拿转义序列当字符吃格子 ⇒ 行超宽换行、**消息块被劈开**（看起来像"不知道谁说的"）。
  规矩：**截断 / 对齐一律作用在未着色的原文上，颜色最后才包**（落点是 `styler.concat`，别在别处拼彩色串）；
  也**别在一段里叠两层色**（行中间那个复位会把后半段打回默认色）。
  同族的两条：**emoji 只许出现在行首** ✗（宽度最不可靠，混进列里整列歪）；
  **模糊宽度字符（`─` `·` `…` `│`）只许"重复"，不许"填充"** ✗ —— `strings.Repeat` ✓、拿它 pad 对齐 ✗
  （宽度账按一格算、CJK 终端可能按两格排 ⇒ 满线在**窄屏**退回 ASCII `-`，见 `rule`）。
- **启动时刷模型列表：只许有一份实现、且必须放后台** ✗：HTTP / 启动 / `-debug` 三处走的都是
  `registry.RefreshOne`；启动那条路整批塞进一个 goroutine（**别**在 `main` 里同步跑 —— 上游慢一秒，
  界面就晚一秒可用），逐渠道**各自挂号** Task、**失败只 log**。刷新**只写发现列**
  （`display_name` / `params` / `context_override` / `tokenizer` 一个字都不许动）。
- **不设总超时 = 上游卡住、界面转一辈子**：超时写在 `providers.json` 的 `timeouts`（缺省 connect 15s / total 300s）。
  辅助调用（`title` / `compact`）**另有自己的短上限**（10s / 60s）—— 它们排在"这一轮翻 idle"**之前**，只靠渠道那 300s 会让一轮被上游按住五分钟。
- **`db.Exec("PRAGMA …")` 只管当时那一条连接** ✗：连接池后来新开的连接就漏了 —— `foreign_keys` 漏掉 = 级联**静默失效**（`ON DELETE CASCADE` 不生效，还回 204）；
  另外 SQLite 的 `busy_timeout` 默认是 **0** ⇒ 两个进程同时开同一个库（服务在跑 + 一个 `./microchat -debug <op>`）会当场回
  `database is locked (5) (SQLITE_BUSY)`，而"打开库"这一步就要拿一下写锁。两条都挂到 DSN 上（`?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)`）——**每建一条连接生效一次**，池子怎么开都带得上。
- **删消息只删后缀 ⇒ 不留洞**：所以不需要"退 leaf"这套东西（树那会儿要算"退到还活着的最新兄弟"，很绕且踩过）。
  想保住后面的内容 ⇒ **先 Copy 一条会话**再在原会话上删（线性会话里这是唯一的"从这里重新开始"）。
- **配置文件里没有注释**：程序整体重写，JSONC 会给人"写了也会丢"的假象。严格 JSON，写坏了报 `Expecting property name…`。
- **手写配置里 `bool` 的零值会撒谎**：`abilities` 的 `enabled` 若声明成 `bool`，"只填了 `provider`、没写 `enabled`"会**静默变成"关掉"**（Go 的零值就是 `false`）——
  而这一格的口径是"**缺字段 = 默认全开**" ⇒ 类型必须是 `*bool`（`nil` = 没写）。同一个坑在"三态"字段上一再出现（`session_header` / `reasoning_field` 也是 `*string`：空串与"没写"是两件事）。
- **批量改代码时逐文件落盘**：把 `write` 放在脚本末尾，中途任何断言失败都会让整批改动一起丢。
- **Go 的 `encoding/json` 默认把 `<` `>` `&` 转义成 `\u003c`**（本项目满地 LaTeX）⇒ 关掉 `SetEscapeHTML`；它的 `Encoder.Encode` 还会补一个 `\n`（axum 不补）⇒ 对账要连字节一起比。
- **纯结构之间手搓转换 = 字段静默丢失**：`config.Provider` → `providers.Provider` 字段名一样、类型不同，
  各处手搓字面量时漏一个字段就是静默失效（真发生过：`identity` 声明了却从没被转过去 ⇒ 配置里写 `"identity":"pi"` 被无声忽略）。
  **规矩**：这种转换只许有**一个**函数（`providers.FromConfig`），并配一条"每个字段都要活到请求上"的回归测试。
- **`r.PathValue("x")` 名字不匹配是静默空串** ✗：改了路由里的通配名（`{id}` → `{session_id}`）而忘了改 handler ✓，
  会**编译过、测试过、还回 200** ✓ 只是拿着空 id 去查（→ 404 或**串到别的会话** ✗✗）。改完**必须逐条真发请求** ✓；
  自检脚本要**按位置**比 handler 里的读取顺序与路由里的通配符顺序 ✓（"名字在不在里面"这种核对漏过两段通配的路由 ✗）。
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
- 改完跑 `go -C core test ./...`；**UI 改动必须实跑**（真机或灌事件的无头验证），不要只凭代码断言。
- **后端代码一变就重启后端**：先 `go -C core build -o ../microchat .`，再重启它（`tmux kill-session -t microchat` 后重起）——不然你验的是旧二进制。自检：`ls -l /proc/<pid>/exe` 带 ` (deleted)` = 跑的是旧货。
- **起服务用 tmux（后台 + 日志落文件）**，别用"受监督进程"那类会一直挂着输出的方式（那样一次工具调用会被进程输出拖住，CLI 卡死）：

  ```bash
  tmux new-session -d -s microchat -c <repo> './run.sh'
  ```

  重启 = `tmux kill-session -t microchat` 再起 —— **但 `kill-session` 杀不掉 `go run` 起的子进程** ✗
  （它会成为孤儿继续占着端口 ⇒ 新实例绑不上就退了，而端口上回答你的是**老二进制** —— 踩过两次，
  症状就是"新加的路由一直 404"）。稳妥做法：`ss -ltnp | grep 8787` 拿 PID 精确 `kill`，确认端口空了再起。
  看日志 = `data/microchat.log`（TUI 模式下日志落这儿）；**tmux 下崩了没有通知** ⇒ 靠 `pgrep` 与日志。
- **先量再断言**：能实测的就不猜（本项目几乎所有关键结论都来自实测）。
- 用户可能**同时在编辑器里改 `frontend/`**：改场景前先读最新文件（并留备份），他的未保存改动优先。
- **重大决策留一句在案**（就写进本文件：结论 + 一句话理由）；**过程与来回不记**（git 里有历史，别把这份撑肿）。
