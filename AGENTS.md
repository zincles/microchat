# AGENTS.md — 工作须知

microchat：轻量 SillyTavern 替代（RPG 向）。三层，边界要清楚：

| 层 | 位置 | 状态 |
|---|---|---|
| 后端（**唯一权威**） | `core/`（Go + 标准库 + `modernc.org/sqlite`） | **新代码一律写这儿** |
| Web 前端（**当前主力**） | `frontend/web/` | **优先迭代这儿**（2026-10-04 定：迭代快，助手擅 Web 栈；新界面能力先落这儿） |
| ~~TUI / TG Bot~~ | **已删除**（2026-11-01：用户「砍掉CLI对话器，只保留服务web前端的功能」） | 只剩 web 一个前端 |

## 术语表（一个词只指一件事）

> **会话（Session）与消息（Message）的完整定义、字段、以及"故意不存什么"，见 `DEFINE.md`** ——
> 那份是词表的唯一权威；这里只列最常用的几个词。
> **下一步做什么看 `TODO.md`** —— 那里只列"还没动"的；设计口径看 `DEFINE.md`。
> **任务（Task）与两族调用（对话型 / 功能型）的完整定义**同样在 `DEFINE.md`（那一节：Task = 一次上游请求的全程）。

| 词 | **只**指什么 | 别叫 |
|---|---|---|
| **世界状态（state）** | `<state>` 那套：**只从消息正文现演**的键值 —— 词汇只有两种：**变量**（键 → 值）与**表**（命名空间，不写表名就落到 `global` 表）；删除不留痕迹（提示词里的块不算数） | 孤立地说"状态"（得说清是世界状态、turn 状态还是 Task 状态） |
| **能力（Ability）** | **写死在代码里的一段固定流程**（**会调模型的 Task 种类**：现在 `title` / `compact` 两个；`judge` 2026-10-28 退役）；开关在 Agent 上、流程在代码里。**"对话本身"不是能力** ✗（那是 Agent 存在的方式） | "工具"（撞函数调用）、"子 Agent" |
| **Agent** | **一份命名的人格 + 一组能力开关**（`agents.json`）：同一个运行时，只是开关不同 | 拿它指"某个能力" |
| **任务（Task）** | **一次上游请求的全程**（发起 → 收尾 → 可能被取消）：一轮生成、一次压缩、一次刷新模型都挂号。**会调模型的一定是 Task**（**反之不成立** —— `refresh_models` 也是 Task，但它不调模型）。**TaskID 是 UUIDv7，且不进库** | "后台作业"（不都在后台）；拿它指"某个能力" |
| ~~用户 / 系统令牌~~ | **这两个词已经作废**（2026-11-01 砍掉账号与鉴权）：没有登录者、没有凭据、没有归属。只剩"这台机器上的一个使用者" | 别再用"用户"指任何东西 —— 那是历史 |

三层咬合：**Agent（人格 + 能力开关）→ 一次调用（对话型 / 功能型=能力）→ Task（这次上游请求的全程）**。
**Task 的种类 ≈ 能力 id** ✓（2026-09-30 定，别再分两层）：`title` / `compact` 是，`judge` 能力的 Task 面叫 **`judgement`**（能力已退役、这一发的种类与实现都留着）
（JEV 那一发的名字；见 `DEFINE.md`「任务（Task）与两族调用」），而 `turn` 作业发起的是**对话型调用**（**不算能力**）、`refresh_models` 发起 0 次。
"能不能自主选下一步"**不是** Agent 的判据，将来是某个能力（如 `plan`）的事。

## 代码布局

```
core/                      ← 后端（Go **核心**；叫 core 是因为它不只是"服务端"：
  main.go                  一个二进制 —— **只服务**（TUI 2026-11-01 删了，见下）
  internal/
    statelang/             `<state>` 语法的唯一权威（零依赖纯函数 + testdata/cases.json 共享语料）
    state/                 世界状态：分层现演、注入渲染、**唯一**出站拼装
    store/                 SQLite：打开/迁移/读写（migrations/*.sql）
    server/                HTTP：路由、错误体、鉴权（只编排，不含业务）
    （2026-11-01 删了 apiclient/ tui/ tg/ —— 见下面那条）
    queue/                 队列任务：一串**串行**跑的原子任务（一步失败即整体结束、可取消）
    model/ config/         纯结构 / `config/` 读写

frontend/
  web/                     ← Web 前端（**当前主力**，Vue 3 + Vite：`src/App.vue` 单文件接线 + `api/` + `utils/` + SFC）
  （没有 archive/ —— Godot 前端 2026-11-01 已彻底删除，见 git 历史）
reminder/                  ← 专题参考（`tool-calls.md` 工具调用 / `jev.md` 决策模型 / `opencode-usage.md` 用量口径 / `gateway.md` 网关实测与会话头 / `compact.md` 区间装配与剪枝）；
```

## 模块地图：一个模块 = 一份唯一权威 + 一条不变量

**四条"唯一"，就是四条能写成守卫测试的规矩**：

| 模块 | 唯一权威 | 不变量 |
|---|---|---|
| `model` | 纯结构 | 无依赖 |
| `statelang` | `<state>` 语法 | 零依赖纯函数；**解析即验证**；`Cleaned` 里**永不出现标签**（fuzz 守着）；写回（`Format`）**带表名**（丢了就悄悄落回 `global`）|
| `store` | **只有它碰 SQL** | 正文/提示词原样存档；变量不落库；会话是**线性**的（顺序在 `id` 上，没有父指针/leaf）；摘要盖的是**一段**（`begin/end`）；生成中的回复不在库里；消息序号 `idx` **派生**（按 `id` 排第几条，不落库）|
| `config` | `config/` 五个文件 | 可缺失；整体重写；密钥不回显 |
| `registry` | 模型口径 | 身份 `(provider, upstream_id)`；发现与覆盖分列，刷新不动用户列 |
| `providers` | **只有它碰网络** | 只走成熟库；超时必配；**选后端**（哪条渠道来答）也只有这一处 |
| `state` | **只有它拼出站文本** | 发出去的**正文**剔标签、改注入当前状态（**提示词原样出站**）；提示词拼装（base + 追加段）也在这儿（`AssembleSystemPrompt`） |
| `blocks` | 块切分 | 块是推导的、不入库；不许劈开；开着的那块永不压 |
| `compact` | 摘要唯一入口 | **只往 summaries 插行**；消息级不许覆盖已压缩的区间（**合并级相反**：只吃同层顶层摘要）；失败不阻塞一轮 |
| `abilities` | 能力身份（枚举） | 产出不进历史/树/变量；失败不阻塞一轮；**缺条目 = 默认全开** |
| `template` | `{{…}}` | 白名单 = 枚举；只扫一遍；会话 Agent 的提示词永不替换 |
| `chat` | 一轮生成怎么跑 | 拿到整段才 INSERT |
| `turn` | 这一轮的流细节 | 增量什么都不算；游标读不消费 |
| `task` | 作业的身份 + 生死（= 一次上游请求的全程） | `defer` 兜底（**Go 没有 Drop**）；绝不留下僵尸条目；TaskID 是 UUIDv7、不进库；**按停只是「请求停止」**（真停不停由任务自己决定 —— 见「取消 = 没做过」）|
| `reroll` | 重摇的**候选列表**（进程内、会话级） | 候选**只在内存里**（不建表、不写库、Copy 不带走）；两个平行家族：**尾条 assistant**（reroll-message）与**没有父的摘要**（reroll-summary）；切换 = 就地换正文（消息 **UUID 不变** / 摘要 **id 不变**）；与生成**共用同一把闸**；**装配与摘要都不另写一份**（借 chat 的装配、借 compact 的生成半程）；**候选生成走流式**（消息族，流进 `turn` 缓冲、摇完清空；摘要族仍整段式走 compact 半程） |
| `queue` | 队列任务（一串串行的原子任务） | **严格串行**（一步收尾才开下一步）；**一步失败即整体结束**；**可随时取消**（在飞的那一发一起掐断）；**不认识压缩**（Runner 由 `main.go` 接） |
| `server` | **只有它碰 HTTP** | 错误体固定；只编排不含业务 |

依赖方向单向：`server → chat/compact/abilities → state（含 statelang）/blocks → registry/providers/config → store → model`。
（`compact` 另要 `abilities` 与 `providers` —— 校验能力开关、借同一套真发；`blocks` 只依赖 `model`。）
`turn`/`task` 是横切（谁都能挂号，不被依赖）。
`queue` **只依赖 `task` + `store`**，不认识 `compact`：它只知道"有个 Runner 能把一步跑完"，
适配器 `compactStepper` 住 `main.go`（组装点）—— 于是队列不必 import compact，压缩也不必知道有队列。
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
  两者路径**相对工作目录**，可用 `MICROCHAT_CONFIG_DIR` / `MICROCHAT_DATA_DIR` 覆盖。
  **界面偏好也归 `data/`**（`data/config/prefs.json` + `data/prefs/background.bin`，2026-11-01 起）——
  从前它们在浏览器 localStorage（`mc_theme` 那一串），现在服务器是权威：换设备/清缓存/装 PWA 都不丢。
  ⚠ 只有两样仍住 localStorage，各有硬理由：`mc_api`（"去哪个服务器"，拿到地址之前没法请求）与
  `mc_last_session`（**设备状态**，不是设置）。
  **连接级 pragma 挂在 DSN 上**（`?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)`）：`db.Exec("PRAGMA …")` 只管当时那一条连接，池子后来新开的就漏了（踩过，见「踩过的坑」）。
2. **正文与提示词都是存档**：`<state>` 块原样留在消息正文里；**发出去的正文一律剔块**、改注入当前状态表。**系统提示词原样出站**（它是说明文：块不算状态、也不剔除 —— 2026-10-11 修：从前剔块会把提示词里的语法示例剪碎）。唯一出口 `state.BuildOutgoing()`——不许写第二条拼装路径。
3. **世界状态不落库**：世界状态**每次现算**（`state.FromSources`）——**只按会话顺序扫消息正文里的块**，然后 fold。每条操作带 `message_id`，"哪句话带来的状态"追得回来。**没有派生表** ⇒ 编辑/删除消息、Copy 一条会话都不需要"重算"（换个会话看状态就是重拉 `/sessions/{session_id}/state`）。
   **提示词里的块不算数**（说明文；底子层 2026-10-07 已删——`<state>` 示例可以放心写进提示词/段里，见「agents.json」一节）。
   **删除不留痕迹**：`delete(键)` 与空值 `键 =` 就是把这个键从现演里拿掉（无墓碑）。
   生效提示词由**一处**解析（会话覆盖 → agent 的（**base + 追加段拼装**）→ 内置默认），出站 / 界面共用它。
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
6. **`data/`（含 `data/config/`）永不入库**（端口口令、连接信息、密钥、存档）。默认值全在代码里，文件缺失也能跑。`.gitignore` 只排除构建产物与依赖（`web/dist`、`node_modules`）。
7. **HTTP API 看下面那节**（**唯一权威是路由表**）。改了接口就跑 `go -C core test ./...`。
   （旧版 Rust 已删 —— 对账工具跟着一起没了 ✓ 现在只有这一份实现 ✓）

## 常用命令

```bash
./run.sh                                                  # ← 开发机直接跑：就是一台 web 服务（没有 TUI 了，2026-11-01）
docker compose up -d                                      # ← 部署（单容器，2026-10-14）：只公开 8788（前端），
                                                          #   后端只在容器里、地址零配置 —— 版本身份与口径见「Docker 部署」一节
cd frontend/web && ./node_modules/.bin/vite               # 前端 dev（0.0.0.0:5173；**8788 是部署口**，见「Docker 部署」；
                                                          #   监听地址写在 vite.config.js；别走 npm exec —— npm 会吃掉 --port/--host）
go -C core build -o ../microchat .                        # 要二进制就这条；产出 ./microchat
./microchat                                               # 起来就在 8787 服务（只服务，不开 TUI）
go -C core test ./...                                     # 后端测试（`-C core` 是 Go 自带，不用 cd）
podman compose restart                                    # ← **每次提交之后都跑一次**（2026-10-28 用户定）：
                                                          #   源码是 bind 的、entrypoint 每次启动现编译（约 10 秒），
                                                          #   这是把改动推到 http://<本机>:8788 的唯一一步。
                                                          #   改 Dockerfile / 依赖层才要 `up -d --build`

./microchat -debug <op> [参数…]                            # ← 直操模式：**同进程**调 internal/*，
                                                          #   不起服务 / 不发 HTTP / 不开 TUI，打印就退出
                                                          #   （每行一条 JSON，好接管道；op 一览以 `-debug` 无参数输出为准，不在这里列全）
                                                          #   典型：new → send（同步跑完一整轮）→ outgoing / state / status
                                                          #   （`reset-users` / `set-root-password` 两个 op 2026-11-01
                                                          #    随账号一起**删了** —— 没有用户可重置、没有口令可设）
                                                          #   与 -config / -data / -addr 共存；给了 -debug 就不监听端口

```

**直操模式（`-debug`）**：验收 / 排障时**不隔着 TUI 与 HTTP**看后端的一条路（实现在 `core/debug.go`，
只从 `main.go` 进来）。两条要知道的：

- 它是"一个 op 一个进程" ⇒ `turn` / `task` 登记表当然是空的（**状态是进程内的事实**，这是诚实的事实，不是缺功能）；
  `tasks` 打的就是这一进程的面板。
- 于是"并发再发 ⇒ 409"与"另一个进程里 stop"靠一枚 **per-session 的 pid 锁**（`data/debug-turns/*.lock`）：
  锁活着 ⇒ 后到的那个 409；`stop` 找到活的 pid 就发 SIGTERM，那边的 send 当成本地按停。
  它**只服务直操模式**（server 的权威仍是进程内的登记表，它从不看这个文件）。

## Docker 部署（单容器，2026-10-14 用户定）

**`git clone` → `docker compose up -d` → 开 `http://127.0.0.1:8788`**（没有初始化步骤 —— 2026-11-01 起没有账号）。一个容器、**只听回环**：

- **单容器两进程**：前端（Node + vite preview，**8788**）与后端（Go，只在容器网络里可达：
  另有一条 `127.0.0.1:8787:8787`，给本机的 vite dev/脚本用）。
  ⚠ **两个口都只绑 `127.0.0.1`**（2026-11-01）：那是唯一的访问闸门，理由见「HTTP API」那节第一条。
  要让手机/平板用就把 compose 里那行改成 `"8788:8788"` —— 同时**没有任何保护**了。
  vite 把 `/api` **代理**给同容器的后端（`frontend/web/vite.config.js` 的 `server.proxy` + `preview.proxy`）
  ⇒ 浏览器**永远同源**、部署里**没有任何要填的地址**。
- **容器里的后端绑 `0.0.0.0`**（2026-11-02 修）：后端默认绑 `127.0.0.1`（那是**宿主机**的唯一闸门），
  但在容器里绑回环 ⇒ 宿主的端口发布转发到容器的 eth0，而那里没人听 ⇒
  `127.0.0.1:8787:8787` 那条发布**能连上却永远没有应答**（注释里写"给本机 dev/curl 用"，实际是假的）。
  所以 entrypoint 起后端时给 `-addr "0.0.0.0:$PORT"`（端口仍从 `config.json` 读）。
  ⚠ **这不放松安全性**：容器的 `0.0.0.0` 只在本容器的网络名字空间里可见，外界够不够得着
  **完全由 compose 的端口绑定决定**（两条都是 `127.0.0.1:` 开头）—— 闸门没有从端口绑定挪走。
  ⚠ 但也因此 **8787 是"后端敞开、没有页面、没有鉴权"**：`/` 是 404、`/api/v1/*` 全通。
  反代 / 域名要指 **8788**（前端）—— 指到 8787 就会看到 `404 page not found`（2026-11-02 用户实踩）。
- **Host 闸门默认不限制**（2026-11-02）：vite 默认只放行 localhost 与 IP（防 DNS rebinding），
  于是每次域名部署都要先撞一次 `Blocked request. This host (...) is not allowed.`。
  半天之内这条路上的坑踩了两回，用户定案：「前端就不能不设域名限制吗」⇒ **默认 `allowedHosts: true`**
  —— 反代、内网 IP、隧道，什么都不用配就能访问。
  **想收窄**才设 `MICROCHAT_ALLOWED_HOSTS`（逗号分隔，`.example.com` 连子域）：
  `MICROCHAT_ALLOWED_HOSTS=localhost,127.0.0.1,tavern.zincles.cc`。
  ⚠ 收窄挡的是 **DNS rebinding**（恶意网页把自己的域名解析到 127.0.0.1，拿本机服务当自己的接口用）；
  本项目 `/api` 的 CORS 是 `*`，真被 rebind 成功对方能**读走存档**。所以口径是：
  **默认按"跑在远处（VPS / 反代后面）"配**（那里 rebinding 没有意义），
  **本机跑又在意的**，把名单设成 localhost 那几个。
  ⚠ 生效值会打一行日志（`[vite] allowedHosts = …`）—— 部署时"到底生效没有"不该靠猜。
  ⚠ 名单**不塞 `config.json`**：那个文件后端会整体重写（`SaveJSON` 只写它认识的字段），
  塞进去的自定义键会在下一次改设置时**悄悄消失**。
- **后端地址的唯一权威 = 后端自己的 `data/config/config.json`**（`server.host/port`）：vite 启动时读它算代理目标
  （`0.0.0.0` 当连接地址时换成 `127.0.0.1`）；读不到（首次启动、文件还没生成）⇒ 默认 `127.0.0.1:8787`。
  （2026-11-01：原来还有个「连接门板」让人填地址/口令，**整块删了** —— 没有要填的东西了。）
- **版本全部钉死**（用户定）：`Dockerfile` 两个 base 按 **digest** 锁死（`golang:1.26.9-alpine3.24` +
  `node:24.19.0-alpine3.24` 的 `/usr/local` COPY 进前者 —— 同一个 alpine/musl，二进制互相认）；
  npm 依赖走 `package-lock.json`（entrypoint 一律 `npm ci`）；Go 依赖走 `go.mod/go.sum`。
- **一切现场编译**（`docker/entrypoint.sh`）：先后端（`go build` 到 /tmp → 按配置端口等它就绪）→ 后前端
  （源码拷到容器内 `/build` → `npm ci`（只一次，卷住 `/build/web/node_modules`）→ `vite build` → `vite preview`）。
  **为什么前端要拷到 `/build`**：① 别在 bind 底下挂命名卷 —— podman-compose 把 bind 排在卷之后、
  整个盖住嵌套卷（实测踩过）；② 就算挂得住，宿主 glibc / 容器 musl 的 `node_modules` 也不通用
  （rollup 原生包，实测直接崩）⇒ 依赖必须与宿主隔离。`/go` 是命名卷（模块 + 编译缓存，重启不用重编）；
  `./` bind 进 `/app` ⇒ `data/`（库 + 配置）就落在宿主机仓库里，`down` 不丢。
- **改了代码怎么生效**：源码是 bind 的 ⇒ `docker compose restart`（podman：`podman compose restart`）即可
  （entrypoint 每次启动都现编译：前端源码先拷进 `/build`、再装/构建）—— 实测 **10 秒**就绪；改 `Dockerfile`/依赖层才要 `up -d --build`。
- **热重载**：**Go 没有任何内建热重载**（标准库不监视文件；生态里是 air 那类第三方工具）——我们的口径就是"`restart` = 重编译"。
  前端要 **HMR** 用宿主 `./node_modules/.bin/vite`（:5173）：它读同一份后端配置、代理到 `127.0.0.1:8787`
  —— 那正是容器的**回环口**（见上）⇒ 改前端文件即时热更新，而数据只有容器一个写者、宿主 node_modules 也不受影响。
  容器自己跑的是 **preview（静态产物）**，故意没有 HMR —— 生产形态要的就是这个。
- **podman 机器**（本机就是：没装 docker，用户定"专心 podman"）：就用 **`podman compose up -d`** ——
  rootless 用户态更安全、零 shim，provider 是 podman-compose。想敲 `docker compose` 这个词则
  `sudo apt install podman-docker`（`/usr/bin/docker` = `exec podman "$@"`，扒包看过）—— 非必需。
  compose 文件是 vanilla 规格（无 podman 专属写法），podman-compose **原文实跑过**；docker compose v2 无实机可测（本机没 docker）。
  注意 **卷名带项目前缀**：compose 里的 `microchat-go` 实际叫 `microchat_microchat-go`（`down -v` / `volume rm` 时按实际名）。
- 已知取舍：容器以 root 跑 ⇒ `data/` 里的文件属 root（宿主机上要 `sudo` 删）；PWA 安装仍要 HTTPS（老话题）。

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
- **截至第 N 条的现演**（`GET /state?at_idx=N`）：正文只 fold 到第 N 条（含）——`0` ⇒ 空状态、越界 ⇒ 当作到最后一条。
  口径与实现见 `AGENTS.md` 的 API 表与 `DEFINE.md` 的「世界状态」。

## 配置文件长什么样

**`data/config/`** 里是**严格 JSON**（程序整体重写），都可缺失。文件名一律 `.json`：

- `config.json` — `{ "server": { "host": "127.0.0.1", "port": 8787, "refresh_models_on_start": true }, "defaults": { "provider": "dummy", "model": "dummy", "agent": "default" }, "chat": { "model_context_tokens": 131072, "history_window": 5, "compact_trigger_tokens": null, "compact_blocks": 5, "title_provider": "", "title_model": "" } }`
  （**全局标题模型**：`title_provider` / `title_model` 都空 = **使用当前会话模型**（默认）；都填 = 起标题固定走这一对。只填一个不算数。）
（`host` **默认 `127.0.0.1`**（2026-11-01 用户定：「我们只允许来自 127.0.0.1 的访问」）——
  账号与鉴权都砍了，所以**端口绑定就是唯一的访问闸门**；改成 `0.0.0.0` 即对 LAN 开放，
  那时**没有任何保护**（同网段任何设备都能改数据）。`host`/`port` 随时改这个文件生效（`-addr host:port` 可临时盖）。
  ⚠ 旧配置里若还写着 `0.0.0.0` 与 `jwt_key`：前者**照旧生效**（程序不替人改主意），后者已经没人读（留着无害））
（`auto_compact` = **自动压缩**开关（**缺字段 = 关**；2026-10-11 用户定——这套未经验证，默认不开，要用的自己去设置里打开；关着就全靠手动 `/compact`）·
  `history_window` = 消息列首屏 / “加载更多”一页多少**段**（默认 **5**；段 = 一条消息或一张顶层摘要卡）；`compact_blocks` = 压缩不给块数时压几个**对话块**（**缺省 3**；2026-10-11 从 5 改 3——小步快跑，每坨原话只嚼一次，且抬头那条路要凑满 N 条同层相邻顶层摘要，N 大常凑不齐直接报无可压缩）；`refresh_models_on_start` = 启动时要不要自动去问每个渠道有哪些模型，**默认 true**，类型是 `*bool` ⇒ 没写就是开）
- `providers.json` — `{ "providers": [ { "id": "dummy", "vendor": "dummy" }, { "id": "openrouter", "vendor": "openrouter", "api_key": "sk-…", "model_filter": "gpt" }, { "id": "自建", "vendor": "custom", "base_url": "https://…/v1", "api_key": "sk-…" } ] }`（`vendor` 是**唯一必填**；`kind` 只剩存量读入别名，新配置必须写 `vendor`；`protocol` 缺省 `openai-chat-completion`；`max_concurrency` = 该 endpoint 同时最多几个请求，0/缺 = 不限）
  **密钥就写在这一条里**（空串 = 没配）：整个 `config/` 在忽略范围内；接口一律不回显（只回 `has_key`），调试页读它时先打码；写回权限收紧到 0600。
  **模型筛选器 `model_filter`（2026-10-22 用户定）** = **只有 id 命中任意一条规则的模型才导入**
  （聚合站动辄几百个模型：OpenRouter 实测 470 个，真正常用的就几个）。空 / 纯空白 = **不筛**。
  语义**只有一份**：`registry.SplitModelFilter` + `registry.ValidateModelFilter` + `registry.FilterModels` ——
  **多条规则用半角逗号分隔、命中任意一条就算中**（2026-10-28 用户定：`deepseek,mimo` ⇒
  `deepseek-v4-flash` 与 `mimo-v2.6-flash` **都**导入，「因为它们分别满足了一个规则」；是 **OR** 不是 AND）；
  每条都是**子串**匹配 `upstream_id`（`openai/` 这类前缀**也参与**比较）、**不分大小写**、**不支持通配符**
  （`*` 被拒绝、报错**点名是哪一条**；要全部就留空）；每条首尾空白吃掉、空条丢掉
  （`gpt,,claude,` ⇒ 两条规则；全是逗号 = 没填 = 不筛）、分隔符**只有半角逗号**
  （全角 `，` 是字面字符：多一个分隔符就多一个「id 里正好有它」的可能）。坏写法在**写入时**就 422；
  万一它从手改的文件里钻进来，刷新会**报错且一行都不动**（先验后拉 —— 否则"筛完是空"会顺着
  「没见到的删行」把整张模型表清空）。⚠ 它在**发现那一步**生效 ⇒ **改了要再刷一次模型**；
  刷完不匹配的旧行会被删掉（连同那些行上的用户覆盖：模型是可再发现的数据，覆盖不可）。
  **vendor / protocol 的中文名只有一个来源**（2026-10-28 用户：「vendor 和 protocol……应该用中文化的名字吧」）：
  `providers/presetNames`（vendor → 中文名）与 `providers/protocolNames`（protocol → 中文名 + 一句人话）——
  界面只**显示**它们（`GET /providers/presets` 的 `name` / `protocol_options`，`GET /providers` 的
  `vendor_name` / `protocol_name` / `protocol_options`），**不许自己另抄一张翻译表**（少一条就留个点不动的死选项）。
  列表页打中文名、内部 id 挂在 `title` 上（手改配置文件的人还得能对上号，出错信息里打的也是 id）。
  ⚠ **占位协议不出现在下拉里**：`anthropic-messages` / `gemini-generate-content` 在 `Validate` 里一律被拒，
  它们**没有中文名**，而 `protocolOptionsFor` 只放认识的那些 ⇒ 结构上就列不出来。从前前端写死一张 `PROTOCOLS`
  表把它们摆进下拉，那是递给人一个按下去必炸的选项。
  **新建渠道的流程**（2026-10-28）：先问「服务商」再问「渠道标识」（标识会跟着服务商变，先填等于白填）——
  换 vendor 时 id 跟着变，**但只在 id 还是自动填的那个时才动**（手填过就不抢方向盘，判据是纯函数
  `providerIdForNextVendor`，住 `frontend/web/src/utils/format.js`）；vendor 下拉按后端的 `primary` 分成
  「常见」/「其它预设」两组；端点那一格留空时会把**真会打到的地址**显示出来（"空"不等于"没有端点"）。
  渠道上的 **`max_concurrency`**（2026-11-05）：同一 provider id 的全局信号量，0 = 不限；
  所有真网络 `Chat` / `Complete` 都过它（dummy/本地假上游不占槽）。高级压缩的 `concurrency`
  是作业上限，最终并发 = min(作业值, 该渠道剩余槽位)。
- `prefs.json` — **界面偏好**（2026-11-01 用户定「所有设置均存储在服务器上」）。
  它**由程序写**（不是手写配置，虽然也能手改）：改设置就整份重写，0600。
  形状：`{ "markdown": {"enabled": true, "disabled": ["table","image"]}, "theme": "dark",
  "text_scale": "1.1", "font_body": "", "font_mono": "", "background": {"kind":"file","version":"…"},
  "send": "enter", "preview": true, "payload_preview": true, "command_menu": false, "chrome_hide": true,
  ⚠ **改完立刻刷新不能丢**（2026-11-04 用户报界面缩放总回 115%）：偏好回写有 250ms 防抖，
  但 `flushPrefs()` 会在滑杆 `change`、`pagehide`、`visibilitychange(hidden)` 时取消防抖并**带
  `keepalive`** 立刻发；开机 `cache` 先读 `mc_prefs` 镜像铺第一帧，否则服务器答话前会先闪回 100%。
  115% 不是代码默认（默认是 100%/空串），而是服务器上**上一次成功写入**的值 —— 后来那几次没写进去。
  ⚠ **每一格 `*bool` 的默认值都要独立**（`new(true)` / `new(false)`，Go 1.26）：从前 `DefaultPrefs` 用
  `on := true; off := false` 再 `&on` / `&off`，多格共用同一个变量 ⇒ JSON 解码时互相改写，
  最终值取决于字段顺序；表现是"开关怎么点都存不住"（2026-11-02 实测，见「踩过的坑」）。
  **`markdown.disabled` 是唯一权威的"关掉哪几类"** —— 空/缺失 = **全开**（新加的语法类型自动跟着开）。
  用列表而不是一排布尔，就是为了躲 `bool` 零值那个坑（见 `abilities.enabled` 的教训）。
  未知 id 在**写入与读取时都报错并点名**（写了不生效最难查）；坏文件 GET 直接报错，
  **不悄悄回落全默认**（那等于"用户的开关自己全开了"，而他一点线索都没有）。
  写出的 `disabled` 按类型清单排序 ⇒ 同一个状态永远写同一份文件。
- `agents.json` — `{ "default_agent": "跑团", "agents": [ { "id": "跑团", "name": "跑团主持人", "system_prompt": "你是跑团主持人。", "first_message": "你好！我是跑团主持人。" } ] }`
  每个 agent 可以带 **`first_message`（首段对话，2026-10-11 用户定）**：以它**新建**会话时，这段文本作为
  **第一条 assistant 消息（idx 1）**落库（正文**原样存档** ⇒ 可带 `<state>` 设初始状态）；空 = 不启用。
  草稿预演（`POST /outgoing`）把它**模拟**进去（"预演 = 真发"）；Copy 不重复插、ST 导入不受影响、改 agent 不追溯。
  每个 agent 上可以带**能力开关** `abilities`（键 = 能力 id：`title` / `compact`；**未知的 id 在写入与启动读取时都报错**，不静默忽略；**退役的 `judge` 例外** —— 老文件里写着它照常能读，读入时就地摘掉）：
  `"abilities": { "compact": { "enabled": false, "provider": "deepseek", "model": "deepseek-chat", "prompt": "…" } }`
  —— **缺字段 = 默认全开** ✓（不带 `abilities` 的 agent 三个能力都是开着的，现有文件一个字都不用改）；
  `provider` / `model` 留空 = 用**会话自己的**；`prompt` 留空 = 用代码里的默认模板；`enabled: false` ⇒ 调用方**明确拒绝**执行那一次（不是静默降级）。
  **能力也能拼段**（2026-10-28 用户定：「我希望"用于Compact的提示词段"也可以被**自由组合**」）：
  `"abilities": { "compact": { "segments": ["StateLang 语法", "在RolePlay里使用StateLang"] } }` ——
  跟 agent 的 `segments` **同一个段库、同一条拼装规则**（`state.JoinPrompt`，顺序 = 追加顺序、段间空行）、
  同一条校验（引用不落地 ⇒ 写入与启动读取都报）。拼装结果 = 模板（`prompt` 覆盖 ?: 代码里的默认）**+ 段**，
  `prompt_version` 从**拼完的模板**算（加一个段也会变）。
  **没有 `abilities.json`** ✗（口子就在 agent 上 —— 于是"能力只能覆盖、不能造"自然成立）。
  **agent 上也没有 `params` / `prompt_order`** ✗（2026-09-30 **删掉**的两个死字段：声明了、没人读、用途未定 ——
  要用再加。老文件里写了它们也**不报错**，只是**下次整份重写时**会没掉：开发阶段可接受 ✓ 记得别依赖它）。
  **提示词段（system prompt segment，2026-10-11 用户定）**：顶层 `segments` 是**可复用文本库**（`{ "id": "katex", "text": "…" }`），
  agent 用 `"segments": ["katex", "mcp"]` 引用（**顺序 = 追加顺序**）。拼装 = base（`system_prompt`）+ 逐段
  **纯字符串拼接**（段间空行 `\n\n`；无替换、无递归、无运行时语义 —— 同一形状永远拼出同一结果）。
  段 id 非空且唯一；引用（agent 的、能力的）必须落地（**写入与启动读取都报错**，同 abilities 口径）；删段有守卫（还被引用 ⇒ 400 列出引用者）；
  段改名 = 所有 agent 的引用一起搬。**内置段**（2026-10-11 用户定）：正文住 `core/internal/config/segments/*.md`
  （`go:embed` 进二进制 —— **git 里可 diff、新装即得**，正是"方便提交"那一条）、**只读删不掉**（要改就「复制为自定义」）、
  **id 保留**（用户段撞名 ⇒ 写入与启动读取都报）；`GET /agents` 的 `segments` = 内置（带 `builtin: true`，排在前）+ 用户段。
  **目前七条**（顺序 = `builtinSegmentFiles`）：`压缩提示词` / `RP压缩提示词` / `起标题提示词` / `StateLang 语法` /
  `合理的RolePlay` / `在RolePlay里使用StateLang` / `Markdown RolePlay相关`（后四条 2026-10-11 从用户库迁入，**一字未改**；早期那条 `current_state` 已删）。
  **能力的默认模板一律住段库**（2026-10-28 用户定：「把默认压缩载荷挑出来，制作成单独的内置 segment」，
  起标题那条同一个道理 —— 否则"凭什么这个能力的模板我读不到"就是个没道理的特例）：
  住 `core/internal/config/segments/{compact,compact-rp,title}-prompt.md`，`abilities` 定义表里由
  `Definition.TemplateSegments`（**数组，第一个是默认**）指过去
  （`config.CompactTemplateSegmentID` / `config.CompactRPTemplateSegmentID` / `config.TitleTemplateSegmentID`）——
  于是那段话可读、可「复制为自定义」、可当普通段参与自由组合，**Go 里不再另有一份字符串**（`Definition.Template` 只剩给"模板还不是段"的能力用）。
  **压缩有两个模板：通用版与 RP 版**（2026-10-28 用户：「编写一个适用于 RP Agent 的压缩用提示词」；
  同日用户再提「完善RP部分的提示词…压缩提示词需要修改」后重写）——
  RP 版（1788 字 / 40 行）比通用版（334 字）厚得多，因为它是**按"下一段会写错什么"倒推**出来的，九条：
  停点 / 时空 / 人（称呼、口癖、会被再提的外貌伤与随身物）/ 因果 / 关系与账 / **已经消耗掉的**（用掉的资源、
  受的伤、失去的信任不许在梗概里悄悄恢复）/ 玩家的意思（只写已做的，不写可能做的）/ 悬着的事 / 已立住的设定。
  三条 RP 专有：**用第二人称"你"**（与 `roleplay.md` 的旁白一致，换人称会让接上去的一段文风断裂）、
  **材料有两种形态**（「【要压缩的这段对话】」= 原文 ⇒ 逐句收拢；「【下面是各段已有梗概…】」= 已压过 ⇒
  真正合并，不并列重抄）、**合并级产出不许比最长的源梗概更长**（金字塔往上该更粗）。
  ⚠ 提示词里引用的抬头**字面**来自 `compact/materialHeader` 与 `mergeHeader` —— 两边分岔不会报错，
  只会让模型找不到抬头、把梗概当原文重抄。`compact/compact_test.go` 的
  `TestCompactRPPromptKnowsTheRealMaterialHeaders` 把两者钉在一起；另有一条
  `TestCompactRPPromptCoversRoleplaySpecifics` 钉上面那几条 RP 专有内容（砍掉哪条都会说清"下一段会怎么错"）。
  挑中**任何一条候选**都算"我要这个模板" ⇒ **整份替换**默认，**不是叠加**：两份模板挂在同一条消息上必然互相打架
  （通用版写"不要复述原文"、RP 版写"细节不能丢"），模型只会挑一个执行、或者两头都不到。
  **RP 预设默认就挑 RP 版**（2026-10-28 用户：「帮我改内置RPAgent」）：`frontend/web/presets/rp-agent.json`
  的 `abilities.compact.segments = ["RP压缩提示词"]` —— 从「智能体 → 从模板创建 → 角色扮演助手」建出来的
  RP Agent 一开始就用 RP 版的压缩口径，不必再手动去挑。⚠ 预设是**建的时候**烘进去的（段引用存进
  `agents.json` 的 `abilities.compact.segments`），所以**已经建好的 agent 不会跟着变** —— 老的那些要手动
  打开「智能体 → 压缩」那一页把 `RP压缩提示词` 挑上。
  **能力拼装只有一处**（`abilities.AssemblePrompt`，`title` 与 `compact` 都调它）三条：
  ① base = `prompt` 覆盖 ?: **默认的那条**内置模板段（候选里的第一条）；② **段列表里已经挑了任何一条模板段 ⇒ 不再垫一份**
  （否则同一段说明文读两遍；挑候选的那条同理 —— 挑了 RP 版就不夹通用版）；
  ③ 段按列表顺序追加在 base 后（走 `state.JoinPrompt`，与 agent 提示词同一条规则）。`prompt_version` 从**拼完的**模板算。
  ⚠ 只挑普通段、两条模板都没碰 ⇒ **照旧垫默认**（否则"我加了一条风格段，流程说明却没了"）。
  ⚠ 从前 `title` 用的是裸 `abilities.Template`（**不拼段**）⇒ 编辑器让你给 title 挑段、后端一个字都不读（静默无效）；已修。
  ⚠ 这条规则前端有一份镜像（`utils/prompt.js` 的 `abilityPrompt`，模板 id 表是
  `utils/format.js` 的 `ABILITY_TEMPLATE_SEGMENTS`，同样是"第一条 = 默认"）只做预览；这些 id 由契约测试
  对着 `core/internal/config/config.go` 的常量和内置段清单核 —— 写错不报错、只会让预览少一段
  （清单里挂着一条库里没有的 id 更糟：挑它时后端默默跳过，界面照挑不误）。
  `prepend_state`（agent 上的开关，缺省关）= 把当前状态渲染成 `<current_state>` 贴到
  **最后一条用户消息**句首（历史不动；前缀缓存友好）。

**上下文工作区**（2026-10-29 用户定：「每个对话分为俩部分：对话界面 和 上下文工作区 界面」）：
顶栏右上角、**绿环左边**一颗两段开关（`.ws-switch`，`role=tablist`）切 `聊天 / 上下文`；
切的是 `#messages-vue` + `#composer` 与 `ContextWorkspace.vue`（三处 `v-show`）。
**顶栏不在切换范围内**（它自己带着开关，切掉就再也点不回来 —— 死锁）；**状态不写 localStorage**
（存下来下次打开直接落在空白工作区，人会以为消息没了）；换会话也不重置（正对照上下文时不该被弹出去）。
2026-10-29 第二轮（用户：「要不我们把右侧栏那个上下文给挪过来？」）：**右侧载荷栏整块搬进工作区** ——
它那几 tab（载荷 / 状态 / 压缩 / 分段压缩 / 补齐空档）一个不少地住进 `ContextWorkspace.vue`（由它渲染 `PayloadBar.vue`，
并把 `setWire/setTables/setTree/setCompactWire` 四个 setter 透给 App）。
**栏本身连同它的一切一起删了**：`#payload-bar`、`[◨]` 开关（`rightHidden` / `toggleRight`）、
`#scrim-right`、≤1100 那段右抽屉规则、`mc_bar_right_hidden` 偏好、`barSide`/`scrimSide` 两个 helper。
⚠ **搬一半最糟**：面板在新地方、开关还在顶栏，点了没反应 —— 契约测试逐文件钉着"一处都不许剩"。
⚠ 判"有没有残留"要**先剥注释**（注释里写一句"`#scrim-right` 已经不存在了"是对的）。
⚠ 面板是 `v-show` 常驻的 ⇒ tab 状态会留着（这是对的），但"看着压缩 tab"App 只在**切 tab 那一下**知道
⇒ 切进工作区、以及在工作区里换会话时都要补喊一次 `refreshTree()`（它自己按 `treeVisible` 早退），
否则树会一直空着、或者显示上一条会话的账。
那颗开关的**尺寸有硬上限**（2026-10-29 用户：「把那个切换按钮弄大一点」）：从 0.78em/`0.16em 0.55em`
（总高 ≈1.35em，比旁边的环小一圈、读起来像附注）放大到 **0.88em / `0.3em 0.8em` ⇒ 总高 ≈1.91em**，
与环（1.8em）平齐。⚠ **别再加**：顶栏是 `absolute` 悬浮层；桌面 `.messages-inner` 的 `padding-top: 3.2em`
是给它让位的东西，窄屏则按 App 实测的 `--topbar-h` + 0.6em（模型名换行也算进去），超过就压住第一条消息（不报错、只是看不见）。契约测试按实际声明**算一遍高度**
两头钉：`>= 1.8em`（别改回去）且 `< 留白 − 0.5em`（别顶穿）。窄屏只收横向 padding，
**不缩字**（缩字正好把"弄大一点"抵消掉）。

**平铺，不是面板**（2026-10-29 用户定：「不需要面板…我们完全可以平铺，就像聊天界面的模型输出一样，
不需要气泡框起来」）：参照物是聊天里的 `.msg.assistant` —— 它本来就**没有底**（只有 `.msg.user` 才是气泡）。
于是 `.ws-panel` 没有底色/圆角/内边距（只留"纵向 flex + pane 自己滚 + 对齐 `--read-col`"）；
`.pv-msg` 每块从"小卡片"改成**裸文本 + 一条细线**（与 `#state-view .state-row` 同一个手法）；
`.pv-role` 从彩色药丸变回**纯文字**（聊天里那行本来也只是"助手 #3"几个字）；
`.pv-chip` 的元信息去掉边框、靠 `::before` 加分隔点。这些 tab 也从"等分胶囊"改成**下划线导航**。
⚠ 这些都不会报错、只会"悄悄长回去"（想给某块加分组时，最顺手的就是 border + background + radius
三件套一上就又变回面板）⇒ 契约测试正面钉住那几条声明。
**载荷只在用户进入「上下文」面板时拉、分块默认全折叠**（2026-11-04 用户定：远端/恶劣网络省流量）：
`refreshHistoryPreview` 只受**载荷自己的** `payload_preview` 开关影响（不再是输入框的 `previewOn`），
切进上下文 / 换会话才请求；载荷工具栏最左是「预览载荷」开关，关掉立即清空且不发 `/outgoing`。
分块视图与压缩载荷的分块都默认收起。
**载荷页有「折叠全部」，没有「展开全部」**（2026-10-29 用户定：「我希望有个按钮可以一键折叠全部
（但没有一键展开的）」）。这个**不对称是刻意的**：折叠是**收拾**（最坏只是"再点开一条"），
展开是**倾泻**（这一发里常有整份 system prompt，一键铺开等于灌满面板还要人手动滚回去）——
所以只做单向，看全文就逐条点。实现要点两条：
① `collapseAll` 仍把每一条显式设成 false；现在 `blockOpen/cpOpen` **默认就是 false**（2026-11-04 用户定：
载荷分块默认全折叠），显式写法保留只是为了"这个动作永远是全收"的语义；
② 全收起来时按钮 `:disabled`（并给禁用样式）—— 否则就是个"点了没反应"的死按钮。
⚠ 顺手修了个同类死按钮：压缩页那个「收起」× 从前是 `@click="compactWire = null"`，
而 `compactWire` 是 **computed（只读）** ⇒ 赋值只打一条警告、什么都不发生；要改的是来源 `compactInfo`。
这类 bug 不报错、不误路由、构建和契约测试也看不见 —— 契约测试现在钉着"不许给那个 computed 赋值"。
**压缩页也有「折叠全部」**（2026-10-29 用户：「在"压缩"里，也请准备一键折叠的按钮吧」）。
这一页有两堆能折的东西：**压缩树**（目录节点）与「看载荷」拉回来的**分块列表** ——
一个写着"全部"的按钮只该有一个含义 ⇒ **两堆一起收**，禁用条件也两堆一起算。
为此把树的折叠状态从 `CompactTree` 里**提到了 `PayloadBar`**（`v-model:collapsed`）：
留在子组件里的话，父组件要么摸不进去、要么靠 `defineExpose` 约定，而"全收了吗"会变成跨组件读取、
禁用条件算不出来。⚠ `CompactTree.toggle` 必须**发新 Set**、不改传进来的那份（就地改 prop = 绕过 update 约定）。
⚠ `setTree` 要清空折叠集合：换会话后节点 id 全是外人的，留着只涨不落。
⚠ 两处按钮**共用 `.collapse-all` 一个类**（载荷页的 `.pv-toolbar` 与压缩页的 `.ctree-head`）——
同一个动作该长得一模一样，各写一套必然分岔（SegmentList 那条教训）。
这些 tab（载荷/状态/压缩/高级压缩/修洞压缩）**要比它标注的内容更大一档**（2026-10-29 用户：「载荷/状态/压缩 三个字
至少要大一点吧」）。⚠ 这里有个**叠乘陷阱**：`.ws-panel` 自己 `font-size: 0.92em`，tab 的 em 是
**相对它**的 ⇒ "tab 是 0.95em" 落到根上只有 **0.87em**，比正文还小，而光看声明数字（0.95 接近 1）
完全看不出来。现在 tab 是 `1.15em` ⇒ 根上 **≈1.06em**，纵向 padding `0.35em`（行高约 32px）。
契约测试按实际声明**乘一遍**再判：`≥ 0.98em`（不小于正文）且 `≤ 1.35em`（别反过来压过内容），
外加 padding `≥ 0.3em`（触摸目标）。
**撤销压缩**（2026-11-01 用户定：「增加一个'删除'图标的按钮，悬停会显示为'撤销'」，并给了金字塔的例子）：
按钮在**两处**（压缩树的摘要行 + 聊天界面的摘要卡，与刷新/编辑并排），图标是垃圾桶、悬停文案「撤销」。
「合法」**判定只有一份**（`core/internal/compact/undo.go` 的 `PlanUndo` / `UndoBlocker`，纯函数）：
① **只算顶层**：被上层盖住的摘要不能单独撤，撤了会在上层的覆盖区间里留一个洞；想撤它先撤上层。
② **同层是一串按位置排的链**：撤更早的压缩，会**连同同层更晚的压缩一起删**（`Cascade`）。
   理由：只撤中间那条会留下"后面还压着、前面空了"的不连续历史；整段退回同一条时间线才说得清。
界面在删之前先调 `GET .../summaries/{id}/undo-preview`；`count > 1` 时弹警告，列出会一起没的区间。
`undoable` = 顶层都可撤（更早的也能，但会级联）；不能撤的按钮**留着但禁用** + 压暗 + 说明。
⚠ 落库是 `UndoSummaries` 一个事务：批量释放孩子 parent → 批量清消息 `summary_id` → 批量删摘要。
反了就是 `FOREIGN KEY constraint failed`（自引用外键在语句末尾才核，与 `ApplyDeletion` 同一个坑）。
⚠ 撤销会让**装配变长**（那段回到原文）⇒ 前端三处都要重算：消息列、状态视图、上下文档（契约测试钉着）。
**压缩树的每行带一段原文预览**（2026-10-29 用户定：「条目不是简单的 `#3 你`，而是
`#3 你 XXXXX…`（原始输入文本的前 n 个字符）」，摘要块同理）。否则翻十条全是"#3 你"，
根本定位不到是哪一条。`utils/tree.js` 的 `previewOf` 三个讲究，缺一个就没法定位：
① **先剥 `<state>` 块**（RP 消息常以状态块开头，不剥则每行都是 "state …"）；
⚠ 复用 `markdown.js` 的 `splitState` 这个唯一权威，**不许**在 tree.js 里另写一条正则
（两份一分岔，预览和气泡就对不上，而且没人知道该信谁 —— 契约测试钉着）；
② **压平空白**（换行/连续空格并成一个空格，单行预览）；③ 截断 40 字 + `…`。
样式上名字（`#3 你` / `#3–12 摘要×2`）**永不截断**（它是定位的锚），被省略号吃掉的只能是预览那半
⇒ 拆成两个 flex 子项，别让外层一个 `ellipsis` 把名字也吃掉。预览前加个 `·` 分隔
（不然 "你 你推开门…" 会读成两个"你"连在一起）。
草稿态（还没有会话）另说一句 —— 这些 tab 确实全是空的。`prop has-session` 靠 `sessionInfo.id` 判草稿
（⚠ 曾经从真会话切草稿时 `Object.assign` 不清旧键，`id` 会留着上一条的 id —— 现在 `startDraft` 显式写 `id: ""`）。
⚠ `liftOverlays` 里 `if (h <= 0) return`：切到工作区时输入行 `display:none`、量出来是 0，
那不是"输入行 0 高"而是"这一下没在量"；照写会让 `--composer-h` 掉到 0，切回来时消息列底部留白塌一帧。

**取消 = 没做过**（2026-11-01 用户定：「任务菜单里，我希望有办法取消任务。而且要考虑原子性。
如果任务被取消了，那么就相当于没做过。例如，A B C .... E 序列。我们提交了一个压缩任务：AB。
此时如果失败或者被取消，我们再次提交压缩任务，仍然应该压缩 AB 才对」）。

界面上是任务浮板每行一颗「取消」（只画在 **`cancelable` 且还在跑** 的那些行上；
接口是 `POST /tasks/{task_id}/cancel`）。

但真正的要求是**原子性**，而它**不是靠回滚**做到的 —— 是**靠把写库放到最后一步**：

```
prepare（算区间）→ 调上游（慢、可能被掐）→ RecordSummary（一个事务，唯一一次写）
```

取消落在中间那一步 ⇒ 写库那一步根本没走到 ⇒ 库里一个字节都没动 ⇒ 重提交一次算出来的
还是**同一段**（`prepare` 每次从库里现算，没有任何"压到哪儿了"的游标可被污染）。
所以"取消 = 没做过"是**结构上的**，不是事后收拾出来的 —— 那才是它可靠的原因。

同一把尺子已经/也该用在别处：`chat` 是"整段拿到才 INSERT"（停止/失败/被杀都不留半条）；
`reroll` 的候选只在内存里；`title` 被按停时**不兜底**（兜底会往 `sessions.title` 写一段首句截断，
而 `Auto` 只在标题为空时才跑 ⇒ 一次误按会留下一个**永久的次品名字**）。

三条纪律（加新的可取消任务时照做）：

1. **写库必须是最后一步，并且一个事务**。中途写一半再"取消时清理"是错的 ——
   总有清理不到的时候（进程被杀、panic），而那种半成品最难查。
2. **取消要记成「取消」，不是「失败」**。`task.State` 里早就有这个词（"用户按停 —— 正常操作，不是错误"）。
   ⚠ 判"是不是被按停"要**看 `ctx.Err()`，不要看错误串**：上游库把 `context.Canceled` 包成什么样不该猜，
   而且**超时与按停是两回事**（前者该报失败）。
3. **不能停的活就别登记停止方式**（`cancelable` 为 false）。纯本地的活（`refresh_models`）一口气跑完、
   没有可断的地方 —— 给它画一颗按不动的按钮是最糟的。

**界面那一半同样是"取消 = 没做过"**（2026-11-01 用户报的 bug：「当我取消重摇后（从小面板里），
对话界面还是老样子，在等待」）。三个洞，每个都**不报错、不误路由、构建也过**：

1. **轮询只在"活着"时写回状态**。两条重摇轮询原来写的是
   `if (st.active && ...) rerollState.value = st` —— 状态变成"没了"（取消 / 失败 / 只剩原文一版）时
   **一个字都不写**，于是 `rerollState` 停在旧的 `running:true` 上 ⇒ `rerollTargetLive` 一直为真
   ⇒ 气泡一直是空白 + "正在输出"，直到下一次进会话才恢复。
   **规矩：状态要么写新的、要么显式写"没有"，不许"不写"**（`refreshReroll` 本来就是这条口径，
   两条轮询现在与它一致）。
2. **按停被当成了"摇完"**。`switchReroll(st.count)` 无条件跑 ⇒ 取消时候选列表还在（之前摇过几版就还在）、
   `count` 没变 ⇒ 正文被切到最后**已经存在**的那版上。用户按的是"取消"，界面却换了一版内容。
   改成**先记"摇之前有几版"，只有真的多出一版才切**（摘要族同一个洞同一条改法）。
3. **按停与挂 ctx 之间有个窗口**（`turn.Registry.Attach`）：`Begin` 在受理那一刻，`Attach` 在后台
   goroutine 第一行；两次"按停"都把整条登记摘掉，若正好落在中间，`Attach` 找不到条目 ——
   从前它**什么都不做** ⇒ 那个 ctx 永远没人取消 ⇒ 上游请求白跑到底（结果会被 `IsMine` 丢掉，
   所以不会写错库，但 token 已经烧了，而用户以为自己按住了）。现在 `Attach` 发现"这一趟已经没了"
   就**当场取消**。

⚠ 这类 bug 的共同形状：**"没有变化"被当成了"没消息"**。只要界面上有"我等一个状态变化"的地方，
就要问一句"如果它永远不变（或者直接消失），我怎么退出来？"

⚠ 踩过的坑（写测试时）：**别在 handler 里等 `r.Context().Done()` 来证明"上游被掐断"**。
Go 1.26 + httptest 实测：客户端 ctx 一取消，**客户端立刻**返回 `context canceled`，
但 server 端那个 handler 的 `r.Context()` **并不会及时触发** ⇒ handler 一直挂着，
`httptest.Server.Close()` 又一直等这条 active 连接 ⇒ **整条测试死锁**（真踩了，排查了两轮）。
要证明取消生效，就断言**客户端侧**的后果：任务翻成「取消」+ 库里没东西 + 重提压的是同一段。

**回到前台就对齐**（2026-11-01 用户定：「如果我从别的地方回到这个网页里，能不能刷新一下状态……
我发送了消息，llm 要一分钟才能返回结果。此时我切换到其他网页。然后 2min 后回来。此时，我必须
打开别的对话再回到当前会话才能刷新结果。这尤其常见于安卓浏览器里」）。

根子是**后台标签页的计时器会被节流、甚至整个冻住**（安卓内存紧时直接丢页面）。于是
"我发了一句话然后切走"这件事，回来时本地那个轮询循环要么卡在很久以前那一拍、要么已经因为
一次网络错退出 —— 而**界面还停在"正在输出"**。所以：**唯一可信的是后端，回来就问一句相位**
（`resyncOnForeground`，接 `visibilitychange` / `pageshow` / `focus` 三条）。

三条分寸，缺一条都会帮倒忙：

1. **还在跑就不许重拉消息列**。重拉会把正在流的那个气泡换成库里的，等于把活人从屏幕上抹掉。
   这种情况只把轮询**接回来**。
2. **已经落地就一次性对齐**：消息列、状态视图、上下文占用、重摇、任务、预演 —— 用户回来该看到
   的是"答案就在那儿"，不是"还得再点一下"。
3. **连发事件要合并**（`resyncing || now - lastResyncAt < 300`）：切回来时三条事件常常一串来，
   每条跑一遍就是三倍请求。resync 本身幂等，重复跑不会错，只是浪费。
4. **重拉消息列要保住滚动位置**（2026-11-03 用户报：「出去截个图再回来，滚动条自己退回最新一条」）：
   落地后的 `reloadWindow()` 默认 `scrollBottom()` ⇒ 人明明停在上面也被拽到底部。改走
   `reloadWindowKeepingScroll()`：重拉前量 `nearBottom`（48px 内算贴底），贴底才继续贴底，
   否则恢复原 `scrollTop`。`pollTurn` 落库那一拍也走同一条。`fetch`/visibility 都别绕过它。

⚠ **判"要不要接轮询"必须看"循环在不在"（`liveTurnPoll`），不能看"有没有 stream 气泡"**：
气泡是**循环死掉之后留在列表里**的那个东西，拿它当判据就永远不接 —— 那正是"永远在等待"的形状。

⚠ 同时把轮询本身改韧了：**单拍失败继续下一拍**（连着十拍不行才认栽）。切后台/换网那几拍
请求本来就会失败，从前一抛出去循环就没了，而气泡还留着 —— 这就是那个 bug 的另一半。
⚠ **同一个会话只能有一个轮询循环**（2026-11-03 修"流式文字顺序错乱"）：`activeTurnPolls` 是前端单飞闸，
`pollTurn` 先查再登记、finally 释放；迟到的旧循环还要用 `status.message_id !== turnID` 自裁。
网络重试/重复响应则由 `mergeStreamSlice` 兜底：**同一游标读两次不追加**（`next/think_next` 与
`cur` 相等直接返回原累积），否则同一块会被拼两遍。前端所有游标都是后端返回的偏移量，不要拿
JS 字符串长度自己推。

**markdown 渲染是逐类型可关的**（2026-11-01 用户定：「增加 markdown 渲染开关 …… 所有的会被
Markdown 解析的非 <div> HTML 块，我们为每个类型都加个开关。不然瞎解析不好。」）：
设置 → 外观的最下面一区，一个**总开关** + 十六类**逐类型开关**（按 `块级`/`行内` 分两组，
每组带「全关 / 全开」）。类型清单**由后端给**（`GET /config/prefs` 的 `markdown_types`），
前端不另抄一张表 —— 少一条就留个点不动的死开关，多一条就是个后端不认识的选项。

**关掉的含义 = 原样显示**，不是"渲染成别的"、更不是"吃掉标记"：关掉粗体就看见 `**粗**` 本身。
总开关关掉 ⇒ 整条消息一个字都不解析（但**换行留着** —— `.msg .text` 没有 `white-space: pre-wrap`，
不留换行就挤成一坨）。

实现（`utils/markdown.js`）：**每次渲染建一个 `new Marked()` 实例**，只给"关掉的那几类"挂
渲染器覆盖（覆盖体 = 把 `token.raw` 转义后原样吐出）；开着的类型一个都不覆盖 ⇒ 走 marked 自己的
默认渲染。这样既不重写渲染器（那必然与上游行为分岔），也不必手搓解析。
⚠ 三处踩过的坑，都写了测试钉着：
① **`String.replace` 的替换串里 `$$` 是转义序列** ⇒ 最早那版把 `$$x^2$$` 悄悄吃成 `$x^2$`
   （不报错，只是内容少一半）。填回数学必须用**函数**替换。
② **关掉数学要保留原始定界符**：`$a+b$` 不许显示成 `\(a+b\)` —— 一个用户写什么就该看到什么。
③ **块级关掉时换行要转 `<br>`**：`.msg .text` 没有 `pre-wrap`，不转的话一个列表会挤成一行
   （那既不是"渲染了"也不是"原样"）。
⚠ **裸 HTML 永远不解析**，且这不是开关能改的：我们**先行转义** `&` 与 `<`（XSS 的门在那儿），
所以 `<div>` 之类根本到不了 marked。用户那句"非 `<div>` HTML 块"说的正是这条边界。

**提示词段的编辑器只有一个**（2026-10-28 用户：「不协调。总觉得和编辑系统提示词的有区别。」）：
agent 级（base 后面）与能力级（模板后面）都用 `frontend/web/src/components/SegmentList.vue` ——
挑段 / 上下移 / 移除 / 缺段警告 / 拼装预览**只有这一份实现**。从前两处各写一套，于是标题一边是 `<h3>`、
另一边是灰色小字加一条底边线；预览一边叫「拼装预览」、另一边叫「这一发真会发出去的提示词」；
截断 48 vs 40 字；缺段警告一边有一边没有 —— 这些都不是功能差异，纯粹是忘了同步。
契约测试钉的就是"两处都用同一个组件"（谁再手写一套就炸）。

## HTTP API（客户端契约）

**唯一权威是 Go 版的路由表**（`core/internal/server/`）。全部挂在 **`/api/v1`** 下，请求与响应都是 JSON。

- **没有鉴权**（2026-11-01 用户定：「干脆砍掉鉴权功能吧。我们只允许来自 127.0.0.1 的访问。还有砍掉账号功能。」）：
  **每个请求都不需要凭据**，`Authorization` 头完全没人看。鉴权那一套（JWT / bcrypt / 环回系统令牌 /
  「root 无口令 ⇒ 服务未启用」那道总闸 / `/auth/*` 五个口 / `/users` 五个口）**全部删掉了**，
  连同 `internal/auth` 包、`users` 表与 `sessions.owner_id` 一起。
- ⚠ **访问闸门不在 Go 里，在端口绑定上**。为什么：浏览器 → vite（**8788**）→ 代理到 Go（**8787**），
  于是 **Go 看到的来源永远是 127.0.0.1** —— 在 handler 里判"是不是来自本机"是**判不出来的**
  （那样写出来的检查会永远放行，看着很安全，其实什么都没挡）。
  真能挡住的是两处：
  ① `docker-compose.yml` 的 **`127.0.0.1:8788:8788`**（LAN 与外网都够不着）；
  ② 后端自己的 `server.host` **默认 `127.0.0.1`**（`config.json` 改成 `0.0.0.0` 就放开 —— 那时**没有**任何保护）。
  实测：`ss -lnt` 两行都该是 `127.0.0.1:`；用本机 LAN IP 打 8788 拿不到连接（curl 回 `000`）。
- **改了端口映射要 `podman compose up -d`，不是 `restart`** —— `restart` 只重启容器、**不重新创建**，
  端口绑定还是老的（踩过：改完 `ss` 一看还是 `*:8788`，全网卡都在听，而 `restart` 报"成功"）。
- 错误体固定 `{"error":{"code","message"}}`，`code` ∈ `invalid` / `not_found` / `conflict` / `unauthorized` / `upstream` / `internal`——客户端按 `code` 分支，别匹配文案。
- **405 由框架自己回**（路径在、方法不对），不带上面的体，但带 `allow:` 头。
- **CORS 全开 ✓ + 预检 204 ✓**（`Access-Control-*` 盖住**每一个**响应：200 / 401 / 404 / 405 / 错误体都带；
  `OPTIONS` 在**鉴权之前**被接管 ⇒ 预检不带 `Authorization` 也放行、回 **204 无体**；只有 `OPTIONS` 被我们截下，其余方法照旧透给路由）。
  `Allow-Origin` 有 `Origin` 就**回显**它、否则 `*`（并带 `Vary: Origin`）；`Allow-Headers` 回显 `Access-Control-Request-Headers`（没有则 `Authorization, Content-Type`）；
  `Max-Age: 600`；`Expose-Headers: Allow`（405 的 `allow:` 不在安全名单里，浏览器默认读不到）。
- **TLS 不归我们**（用户定）：云上由反代终结（Cloudflare 一类）；本二进制只管 HTTP。

### 故意砍掉的（2026-09-29 定：**先简化，再往深走**）

这一轮把"无用"与"可选"两类路由从契约里**删掉**了（不是没搬 —— 是**不要**）：

- 无用：`GET /debug/state`、`GET /debug/file/{name}`、`DELETE /providers/{provider_id}/models`
  （理由：后端自述会说谎、配置文件原文不该给远程客户端、上游模型列表本就自动清理）
  —— ⚠ `GET /sessions/{session_id}/export` 2026-10-11 **以"对话 JSON 导出"的名义回来了**（2026-10-17 扩成 v2：再带渠道/模型/Agent 三格软引用；**2026-10-28 扩成 v3**：再带整张摘要金字塔 + 每条消息的 `reasoning`；见下表）
- 可选（**待重做**，不做兼容）：`POST /models/probe`、`POST /sessions/{session_id}/archive`、
  `GET/PUT /abilities`
- **`POST /sessions/{session_id}/fork` 已彻底作废** ✗（不是"待重做"）：线性会话里分岔就是 **Copy** ✓（见上表）
- **不要照旧版补回来** ✗ —— 旧版是参照，不是目标；要加先改这张表。
### 访问控制（2026-11-01 · 取代原来的「鉴权」与「用户管理」两节）

**只有一个使用者、只从本机访问** ⇒ 没有登录、没有账号、没有权限区分。原来看三样东西现在都不存在了：

| 原来 | 现在 |
|---|---|
| `Authorization: Bearer <JWT>` / 环回系统令牌 | **不要凭据**（那个头没人读） |
| `POST /auth/login` · `/auth/setup` · `GET /auth/me` · `POST /auth/logout` · `GET /auth/status` | **全删**（访问这些路径 ⇒ 404） |
| `GET/POST/PATCH/DELETE /users*`（加人 / 改名 / 改口令 / 删人） | **全删** |
| 「root 无口令 ⇒ 服务未启用 ⇒ 403 `setup_required`」 | **删**（没有"启用"这回事） |
| `sessions.owner_id`（归属过滤；非本人 ⇒ 404） | **删列**（只判会话在不在） |
| `-debug set-root-password` / `-debug reset-users` | **删**（没有口令可设、没有用户可重置） |
| 设置面 = root 专属 | **不设限**（都只有一个使用者） |

⚠ `store.dropRetired()`：新库本来就没有 `users` 表与 `owner_id` 列（`001.sql` 已经删了那一段），
**老库**则还留着它们 —— 迁移靠 `PRAGMA user_version` 拦着，改 `001.sql` 对老库**不生效**（不会重跑）。
所以打开库时顺手 `DROP TABLE IF EXISTS users` + `ALTER TABLE sessions DROP COLUMN owner_id`（**幂等**，
只删这两个"没人再读"的东西，绝不动会话/消息/摘要的数据）。

### 会话与消息

| 方法 | 路径 | 请求体 | 响应 | 说明 |
|---|---|---|---|---|
| GET | `/sessions` | — | `[SessionView]` | 每项 = 会话 + `messages`（**这条会话有几条消息** —— 客户端的启动编排据此认定"空会话"，不必逐条会话再拉一次消息）+ `turn`（客户端据此标"生成中"）|
| GET | `/sessions/{session_id}/export` | — | `{version:3,title,provider,model,agent_id,agent_name,messages:[{role,content,reasoning}],summaries:[…]}` | **对话记录导出**（JSON；2026-10-11 起，10-17 扩成 v2，**10-28 扩成 v3**）：标题 + 每条 role/content（正文原样，`<state>` 块留着 ⇒ 导回去状态照常现演）+ **这条会话当初绑的渠道 / 模型 / Agent 三格软引用**（`agent_name` 只为"本机没有也能说清是谁"）+ **v3：整张摘要金字塔 + 每条消息的 `reasoning`**。<br>**消息一条不少**：压缩在库里从不改 `messages`（`RecordSummary` 只写 `summary_id`）⇒ 被摘要盖住的原文照旧全导。<br>**摘要必须带**：它是模型写的字，**不可重算**（重压得到另一段 —— `reroll-summary` 的存在就是证据），从前导回来等于"从没压过"。形状：`{key, parent_key?, type, begin_idx?, end_idx?, text, blocks, tokens, provider, model, prompt_version, usage, dirty, created_at}` —— `key`/`parent_key` 只是**文件内的句柄**（父子关系），`begin_idx`/`end_idx` 是 **1-based 消息序号**（不用消息 id：导入后 id 全换新）。<br>**仍不带**：system prompt / 消息 id / 消息的思考耗时与用量 / 摘要的 `source_ids`（导入时按真实成员重建）；**Agent 的定义也不带**（人设搬家走卡带）。**不要凭据**（没鉴权了） |
| POST | `/sessions/import-json` | 上面那个导出格式（v1/v2 也收） | `{session, messages, summaries, agent_missing}` · 201 | **JSON 导入**：建新会话 + 按序落库（id 批铸排序）；role 只许 user/assistant（坏 ⇒ 400 点名第几条）、空 messages ⇒ 400、`version > 3` ⇒ 400；标题没给 ⇒ 首条 user 起。**v2 的三格照抄**（`provider`/`model`/`agent_id` —— 本机没有也照抄、不静默换缺省；文件没点名 agent 才用 `default_agent`），agent 本机没有 ⇒ `agent_missing: true`（界面提示"人设回落内置默认、要人设导卡带"）。**v3 的金字塔**：摘要**全部铸新 id** 再重映射父子（照抄旧 id 让同一个文件导两次会撞主键）⇒ `store.RestoreSummaries` 一个事务写进去，并回填 `messages.summary_id`（重叠区间在那一层当场变错误）。形状/成环/越界**在动库之前**验完 ⇒ 坏文件不留空壳会话；真写失败则把刚建的会话删掉（**半成品比失败更糟**：用户以为导进来了）。**不经过** `POST /sessions` ⇒ 不插首段对话 |
| POST | `/sessions/import-st` | JSONL 原文（`?title=` 可选） | `{session, messages, skipped}` · 201 | **不要凭据**（设置面）。**ST 导入**：SillyTavern JSONL（一行一条）→ 建会话 + 按序落库；只认四样（is_user/mes/reasoning/send_date），其余全丢；空文件 ⇒ 400 |
| POST | `/sessions` | `CreateSessionReq` | `Session` · 201 | **配了 `first_message` 的 agent ⇒ 顺手把首段插成第一条 assistant（idx 1）**。省略字段时取 `config.json` 的 `defaults`；`title` **真生效**（建一条已命名的会话，不必再多发一次 `PATCH`）—— 不给 / `""` ⇒ 标题空着（等首条用户消息自动起名）。**`id`（可选，2026-10-09）**：客户端铸的 UUIDv7 —— 草稿态（web）靠它把"预演的那一发"与"真发的那一发"钉成同一个会话键；给了就用（先校形状 ⇒ 非 v7 **400**、已有 ⇒ **409**），不给 ⇒ 服务端现铸。⚠ **HACK（刻意，全项目唯一允许客户端铸 id 的口子）** —— 动机只有草稿预演那一条，别扩散（消息/摘要的 id 一律服务端铸） |
| PATCH | `/sessions/{session_id}` | `UpdateSessionReq` | `Session` | 标题 / 模型 / agent / **提示词**（`system_prompt` 是**指针** ⇒ 没给或 `null` = 不动、`""` = **清掉会话级覆盖** ⇒ 回落 agent 的、非空 = 写进 `sessions.system_prompt`）|
| DELETE | `/sessions/{session_id}` | — | 204 | 不存在 → 404 |
| POST | `/sessions/{session_id}/copy` | — | `Session` · 201 | **Copy**（线性会话里的"分岔"）：新 id、消息与摘要一并复制、摘要新 id 且指针重映射；**世界状态不复制**。入口在左栏会话的「选项」浮板（2026-11-04 从会话设置面板挪过来）；面板里不再有它 |
| GET | `/sessions/{session_id}/messages` | — | `[Message]` | **按 `id` 升序的整条会话**（线性 ⇒ 没有"当前路径"）。三条查询参数（**都不给 = 全部**，语义不变）：`?from_idx=5&to_idx=10` **闭区间**（含两端；缺一端补默认：起点 1 / 终点末尾）、`?last=20` **取尾**。**参数错 ⇒ 400**（`last` 与区间混用、非正整数、区间反了）；**越界不是错** ⇒ 给现有的那几条（起点在末尾之后 ⇒ 空数组）。响应仍是**数组** ✗（不换成对象）。每条带 `idx`：**0 = 合成的系统提示词；1..N = 真消息** —— 它是**派生的**（= 按 `id` 排第几条）、**不落库**、**永不改变**（只删后缀 ⇒ 不留洞；不往中间插；编辑/重摇不改 `id`）|
| POST | `/sessions/{session_id}/messages/batch` | `{ranges:[[from,to],…]}` | `[Message]` | **2026-11-04**：一次取多个 1-based 闭区间（`to=0` = 到末尾），按整条会话 id 顺序返回并去重；首屏 / 加载更多把摘要切开的几段压成一发，不再一个区间一个 RTT。区间非法 ⇒ 422 |
| POST | `/sessions/{session_id}/messages` | `SendReq` | `TurnAccepted` · **202** | 落用户消息 + 开工；不含回复正文 |
| POST | `/sessions/{session_id}/resend` | — | `TurnAccepted`（`user` 为空） · **202** | **重发历史**：不落新消息，拿现有历史再跑一轮（尾 user 没回复 / 尾 assistant 想再要一版都行；空会话 ⇒ 400；在跑 ⇒ 409）。空输入点发送走这条 |
| PATCH | `/sessions/{session_id}/messages/{message_id}` | `EditMessageReq{content?, reasoning?}`（至少给一个 ⇒ 422；nil=不动、`""`=清掉） | `Message` | 改**任意**一条消息的正文 / 思考 = 重写存档（世界状态随之现演；摘要只标 `dirty`、**不级联**）|
| GET | `/sessions/{session_id}/messages/{message_id}/deletion-preview` | — | `DeletionPlan` | **只算不动**（安全 ⇒ GET）：会删掉哪些消息 / 摘要、哪些指针会被置空 |
| DELETE | `/sessions/{session_id}/messages/{message_id}` | `{"last_deleted_message_id"}` | `DeletionPlan` | 删**这条及之后的全部**（级联见 `DEFINE.md`）。核对字段不符 ⇒ **409**（重新预览）；缺字段 ⇒ 422 |
| GET | `/sessions/{session_id}/state` | — | `StateView` | `session` / `effective` / `tables`，**每次现算**（状态只从**正文**来——提示词的块不算数）。`global` 是 **`tables` 里那张表**（不写表名的块落到它，且它**恒在**：空也回 `{}`）。查询参数 `?at_idx=N` ⇒ **截至第 N 条的现演**：正文只 fold 到第 N 条（含）、`0` ⇒ 空状态、越界 ⇒ 当作到最后一条（与 `/messages` 一个口径）、不给 ⇒ 当前状态；负 / 非整数 ⇒ **400** |
| POST | `/sessions/{session_id}/outgoing` | `{"content":"…"}` | `LastPayload`（`method/url/headers/体`） | **(c) 把这条 content 当成即将追加的那句用户消息之后**，真会发出去的那一发（**真请求**，与 (a) 同一支笔 `Snapshot`：`chat.PreviewWire` 与真发共用装配→`wireMessages`→选后端查表→`providers.Build` 两份对不上就地炸）。**只算不写**：不落库、不改任何状态。待发那句若带 `<state>` 块 ⇒ **状态表跟着变** ⇒ 必须重走一遍现演与装配（不是"(b) + 一条消息"）。缺 / 空白 `content` ⇒ **400** |
| POST | `/outgoing` | `{"id?","provider?","model?","agent_id?","content"}` | `LastPayload` | **草稿预演**（2026-10-09 加，web 草稿态用）：**还没有会话**时的第一句 —— 临时种子会话（零行、无历史）走同一个 `chat.PreviewWire`，**一条会话都不建**。`id` = 客户端（草稿）铸的 UUIDv7 ⇒ 进请求头（`x-opencode-session` 那类）就是将来真会话要用的那个；省略 ⇒ 服务端现铸。`provider/model/agent_id` 省略 ⇒ 与 `POST /sessions` **同一处口径**（config defaults + `default_agent`）。空 `content` ⇒ **400**（草稿没有历史可续写）。**agent 配了 `first_message` 时把它模拟成一条 assistant 垫在用户句前**（真会话会带它 ⇒ 预演 = 真发） |
| GET | `/sessions/{session_id}/context` | — | `ContextUsage` | 只有数字：`used_tokens`（估算）/ `budget_tokens` / `trigger_tokens` / `remaining_tokens` / `ctx_len` / `max_output` / `ratio` / `estimated` / `last_prompt_tokens` / `over_budget` |
| GET | `/sessions/{session_id}/prompt` | — | `{"text","type"}` | **生效的系统提示词**（三级解析的**结果**，与出站拼装读同一处）：`type` = `conversation`（会话自己写了 `sessions.system_prompt`）/ `agent`（`agents.json` 里那个 agent 的）/ `builtin`（两级都没有 ⇒ 代码里的内置默认）。**永不给空**：解析全落空（典型：会话的 `agent_id` 软引用**悬空**）也退内置那句、`type` 报 `builtin`。**回拼装后的结果**（base + 追加段，见 agents.json 的 `segments`）|
| GET | `/sessions/{session_id}/status` | — | `TurnStatus` | `phase`（`idle`/`pending`/`streaming`/`error`）+ `message_id` + `elapsed_ms` + `chars` + `thinking_chars` + `error`。另搭两档**与轮次无关**的活状态：`compact`（`state`=`running`/`done`/`error` + `blocks`/`compacted`/`summary_id`/`from_idx`/`to_idx`/`merged` + `error`）与 `reroll`（`state` + `elapsed_ms` + `error`）|
| GET | `/sessions/{session_id}/turn/text?from=N&think_from=M` | — | `StreamSlice` | 流式增量的**游标读**（正文与思考各一条游标，`from` = 第几个字符）：只服务动画 |
| GET | `/sessions/{session_id}/turn/poll?from=N&think_from=M` | — | `{status, slice}` | **2026-11-03**：`status` 与 `turn/text` 合并成一发（远程/CF 少一个完整 RTT）。web 的 `pollTurn` 用它；`/status` 与 `/turn/text` 仍保留（重摇/前台对齐等旧调用） |
| GET | `/sessions/{session_id}/turn/events?from=N&think_from=M` | — | SSE `{status, slice}` | **2026-11-03**：主路径改成 **SSE**（`id: next:think_next`，断线重连带 Last-Event-ID；服务端 100ms 看内存、状态 500ms 一次）。web 的 `pollTurnEvents` 优先用它；8s 没有首个事件才回退到 `/turn/poll` 轮询。CF 需要 `no-transform`/`X-Accel-Buffering:no`；已设 |
| POST | `/sessions/{session_id}/stop` | — | `{"stopped": bool}` | **幂等**：会话存在且是自己的、只是没在跑 ⇒ 200（`false`）；**不存在 / 不是自己的 ⇒ 404**（多用户起它不再是例外——不许掐别人的轮子）|
| POST | `/sessions/{session_id}/compact` | `CompactReq` | `CompactStatus` · **202** | **压缩**：`{"blocks": N}` 或 `{"begin_idx": i, "end_idx": j}`（1-based 消息序号，与 `/messages?from_idx=&to_idx=` 同一套词；两种给法互斥、区间两端都得给；都不给 ⇒ 用 `chat.compact_blocks`）。**按块，不按条**（块 = assistant→user 交界，一块 ≥2 条）。按块数 ⇒ 策略（底层凑不够就抬头并摘要）；按区间 ⇒ 全未覆盖走消息级、**同层顶层摘要恰好铺满**走合并（金字塔）、其余 ⇒ 400（混合有洞，不后台跑）。**同一个会话同时只允许一次**（在跑 ⇒ 409）。跑完的结局在 `GET .../status` 的 `compact` 那一档（`running`/`done`/`error` + `from_idx`/`to_idx`/`merged` + 原因）|
| POST | `/sessions/{session_id}/compact/preview` | `{"blocks": N}` | `PreviewResult` · 200 | **压缩预览**：**只算不动**（调同一套策略；区间入口自己就是答案，不走这里 ⇒ 400）。回 `from_idx`/`to_idx` + `merged` + `source_ids` + 人话一句（"压第a–b条（N块）"/"并第a–b条那N坨"）。落库/调上游/挂号一概不碰 |
| POST | `/sessions/{session_id}/compact/payload` | `CompactReq`（两种给法都收） | `PayloadPreview` · 200 | **压缩那一发的载荷预演**（2026-10-28 用户点名要）：这一趟压缩**真会发出去什么** —— 回 `payload`（与 `LastPayload` 同形：method / url / 头（密钥打码）/ 体）+ `from_idx`/`to_idx`/`merged`/`blocks`/`messages` + `material_chars`（材料正文字数，按 rune，不含抬头与附上的状态）+ `state_before`/`state_after`（材料里附没附那两份状态）。**与真压走同一段拼装**（同一个 `prepare` + 同一个 `wireFor`）⇒ 预演 = 真发（合同测试把两边的体**逐字节**比）；与 `…/compact/preview` 的分工：那条答「压哪儿」，这条答「发什么」。**只算不动**：不联网、不落库、不挂号，**也不写 `/debug/last-payload`**（那是"真发过什么"的记录）|
| POST | `/sessions/{session_id}/compact/advanced/preview` | `{begin_idx?,end_idx?,step_blocks?,concurrency?,max_attempts?,repair?}` | `AdvancedPreview` · 200 | **高级压缩/修洞计划（只算不动）**：按完整块切 chunk，返回 `chunks`（from/to/blocks/messages/state）、`holes`（同层已封段之间的压缩空洞）、总请求/块/条数。`repair:true` 时空范围 = 扫全部；普通模式必须给区间。不联网、不落库、不挂号 |
| POST | `/sessions/{session_id}/compact/advanced` | 同上 | `AdvancedJob` · 202 | **启动高级作业**：一个父 Task，chunk 并发生成（`concurrency`），每块最多 `max_attempts` 次（只重试 upstream）；全部成功才**一个事务**写 N 条摘要，任何一块最终失败 ⇒ 整批不落库。同一个会话仍与普通压缩共用一把闸（跑着 ⇒ 409） |
| GET | `/sessions/{session_id}/compact/advanced/{job_id}` | — | `AdvancedJob` | 整体状态 `running` / `done` / `failed` / `canceled` + 每块进度与 attempts；进程内状态，重启即失 |
| POST | `/sessions/{session_id}/compact/advanced/{job_id}/cancel` | — | `AdvancedJob` | 取消整个高级作业（所有子请求共用 ctx）；取消 = 没做过，不落库 |
| GET | `/sessions/{session_id}/summaries` | — | `[Summary]` | 摘要列表（picker / 消息列折叠 / 分页切段用；按 id 序；空 ⇒ `[]`）。每条带 **`begin_idx` / `end_idx`**（区间两端的**消息序号**，派生 —— 与消息 `idx` 同一把尺；端点悬空/缺 ⇒ 缺省 ⇒ 界面按「不折」处理），以及 2026-11-01 加的两格派生字段 **`level`**（层级：1 = 直接盖消息）与 **`undoable`**（顶层现在都能撤；被上层盖住的为 false）。⚠ 两格都 `omitempty`：**没算的地方不出现**（导出、单条 PATCH 的响应），语义是"这里没算过"而不是"算出来是 false" —— 前端按 `!!s.undoable` 读 |
| PATCH | `/sessions/{session_id}/summaries/{summary_id}` | `{"text":"…"}`（必填；空 ⇒ 400） | `Summary` | 手改**任意**一条摘要的正文 = 就地换 text（id / 区间 / 指针不动；改孩子 ⇒ 祖先标脏；自己变干净）|
| GET | `/sessions/{session_id}/summaries/{summary_id}/undo-preview` | — | `{summary_id,level,count,items:[{id,from_idx,to_idx,level}]}` | **撤销预览**：撤这条会连带删哪些同层更晚的摘要。被上层盖住 ⇒ 409；不存在 ⇒ 404。只算不写 |
| DELETE | `/sessions/{session_id}/summaries/{summary_id}` | — | 204 | **撤销压缩（支持级联）**：顶层都可撤；撤更早的会连同**同层更晚**的一起删。被上层盖住 ⇒ **409**（先撤上层）；没有 ⇒ 404。`UndoSummaries` 一个事务：批量释放孩子 parent → 批量清消息 `summary_id` → 批量删摘要。**不回体** |
| POST | `/sessions/{session_id}/reroll-message` | — | `{target_message_id, task_id, state}` · **202** | **重摇·消息**：进模式并**立刻摇一次**（已在模式里 ⇒ 再摇一版）。候选（`RerolledMessage`）**只在内存里**、**不进历史** —— 选中才 apply 回那条 Message。尾条必须是 assistant（不是 ⇒ 400 说清那是"重发"）；与生成**共用同一把闸**（在跑 ⇒ 409）。**候选生成走流式**（2026-10-10 起）：deltas 进 `turn` 缓冲（`BeginReroll` 清、`FinishReroll` 清）⇒ 摇的过程中 `/turn/text` 能读到正文在长（web 靠它做气泡内预览）|
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

**出站载荷有两件事 —— 别用一个词糊过去** ✗（各有名字、各有入口）：

| | 是什么 | 入口 | 能否重算 |
|---|---|---|---|
| **(a) 上一次真发出去的那一发** | 那一刻请求的**快照**（**含请求头**；覆盖式，只留最近一发） | `GET /debug/last-payload` | **不能** ✗ —— 历史事实：改一条旧消息就回不去了 |
| **(c) 把待发那句追加进去之后** | 同一段装配 + 同一段 Build 的**真请求**（`method/url/headers/体`，与 (a) 同一支笔；库内账一个不发；**只算不写**） | `POST /sessions/{session_id}/outgoing` | 能 ✓，但**只有服务端算得出来** |
| **(d) 草稿的第一句**（2026-10-09 加） | 同 (c)，但会话**还不存在**：临时种子会话（零行、无历史）；`id` 是客户端草稿铸的 UUIDv7 ⇒ 头上那个会话键就是将来真会话的 | `POST /outgoing` | 能 ✓ 同 (c) |

**(c) 为什么客户端拼不出来** ✗：待发那句里若带 `<state>` 块 ⇒ **注入系统提示词的状态表会跟着变** ⇒ (c) 不是"(b) + 一条消息" ✗，必须重走一遍状态现演与装配。
**（d) 与真发逐字节一致** ✓（2026-10-09 实测：`method/url/headers/体` 全同）—— 靠两个钉子：同一个 `chat.PreviewWire`；同一个 id（草稿铸的那枚最后就落成 `sessions.id`）。
注意 (a) 是**覆盖式**：一轮跑完后标题/压缩这些**辅助调用也发上游**，会把 (a) 顶成它们那一发 —— 想看"聊天这一轮发了什么"，等没有辅助调用的下一轮再对（或对 (c)/(d)）。
（旧 (b) 逐项形状已删：`message_id`/`idx`/`type` 全是库内账，上游一格都不要 —— 调 `GET /outgoing` 就 410 `gone`。）

**真发的那一轮装配的就是 (c) 的前身** ✓：受理时先把用户消息落库，再走**同一段装配**
（`chat.outgoingFor` ⇒ `assemble` ⇒ `state.FromSources` + `state.BuildOutgoing`）——
唯一的差别只是"那句已经进了库、`ListMessages` 里有它" ⇒ **预演与真发不出第二份答案** ✓（`POST /outgoing` 走的正是同一个 `assemble` + `Build`）。

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
| PATCH | `/providers/{provider_id}` | `UpdateProviderReq` | `ProviderView` | `api_key`：`None` = 不动，`""` = 清除。`model_filter` 同三态（`""` = 不筛）；**改了要再 `POST …/refresh` 才生效**。**`new_id` = 改渠道标识**（2026-11-05）：配置与库里的引用（`sessions.provider` / `models` / `model_route` / `provider_state` / `summaries.provider`）**一起搬**；撞已有渠道或在库里有同名数据 ⇒ **409**（**不合并**）；空 / 纯空白 ⇒ 422。先写配置、再搬库，库失败会把配置改回去 |
| DELETE | `/providers/{provider_id}` | — | 204 | 密钥随记录一起没 |
| POST | `/providers/{provider_id}/refresh` | — | `ProviderView` | **POST**（会写发现态）：拉 `/models` 并落库 |
| POST | `/providers/{provider_id}/test` | 可选 `{vendor,protocol,base_url,api_key,model_filter}` | `{ok,models,filtered,note?,error?}` | **测试连通性**（2026-11-02）：只拉一次 `/models`，**不落库、不保存**；请求体可带编辑器里尚未保存的值（api_key 空 = 用已存的）。返回 200 + `ok:false` 表示"连不上"（不是接口 500）；dummy 直接回 ok+note |
| POST | `/providers/{provider_id}/refresh-routes` | — | `{provider, models}` | 只刷路由表：拉 models.dev 快照 ⇒ `model_route` 落库（只认聚合站；**启动时不跑**，一周手跑一次）|
| GET | `/providers/{provider_id}/usage` | — | `ProviderUsageView` | **套餐余量**（只读）：**只对 `vendor = opencode-go` 的渠道有义** ⇒ 别的 vendor **400**（`invalid`）、没配 key 也 **400**；上游错原样传（`upstream`）。形状 = `plan` / `windows[].label/percent/resets_at` + `provider_id`（`-debug usage` 就是它；web 界面还没入口） |
| GET | `/models` | — | `[ModelListItem]` | 跨 provider 拍平（"渠道 / 模型"下拉用）|
| PATCH | `/models` | `{provider, upstream_id, context_override}` | `ModelView` | 设/清上下文覆盖（`null` = 清）。**刷新永不覆盖用户列** |
| GET | `/agents` | — | `AgentsConfig` | 生效列表（含内置默认 agent）|
| POST | `/agents` | `CreateAgentReq` | `Agent` · 201 | 收 `name` / `system_prompt` / **`segments`（追加段 id 列表）** / `abilities` / `prepend_state`；id 由后端生成。**没有 `display_mode`** ✗（2026-10-27 删：从前 `chat` 双边气泡 / `roleplay` 用户气泡+AI 纯文本二选一，现在**只剩后者**、不再是开关。老 `agents.json` / 老卡带里写着它**照常能读**，只是成了不认识的键、下次整份重写自己没掉） |
| PATCH | `/agents/{agent_id}` | `UpdateAgentReq` | `Agent` | `new_id` = 重命名（搬 `default_agent` 与会话引用）；`make_default` = 设默认；`segments` / `abilities` / `prepend_state` 整段替换（语义同 POST；`segments: []` = 清空）|
| POST | `/segments` | `{id,text}` | `Segment` · 201 | 建提示词段（id 非空唯一、≤64 字 ⇒ 重名 409；**与内置撞名 ⇒ 409**）|
| PATCH | `/segments/{segment_id}` | `{new_id?,text?}` | `Segment`| 改内容 / 改名（改名 = 所有 agent 的引用一起搬）；**内置段 ⇒ 400（只读）**|
| DELETE | `/segments/{segment_id}` | — | 204 | 删段；**还被引用 ⇒ 400（列出引用者）**、**内置段 ⇒ 400（删不掉）**。读口不另开 —— 段库随 `GET /agents` 一起给（内置带 `builtin` 标记）|
| DELETE | `/agents/{agent_id}` | — | 204 | 内置默认 agent 不可删 |
| GET | `/config/prefs` | — | `{prefs, markdown_types, defaults}` | **界面偏好**（2026-11-01 起住服务器）。<br>**`markdown_types` 是语法清单的唯一权威**（`[{id,name,kind,sample}]`）—— 界面据此排开关，**不许前端另抄一张**（少一条就留个点不动的死开关，多一条就是个后端不认识的选项）。<br>`defaults` = 出厂值（界面"恢复默认"与前端缺省都读它）。<br>⚠ 文件坏了（未知类型 / 坏背景）**直接报错**，不悄悄回落全默认 —— 那等于"用户的开关自己全开了"而他一点线索都没有 |
| PUT | `/config/prefs` | `Prefs` | 同上 | **整份替换**（偏好是一组独立开关，客户端手里就有全量；补丁语义只会多出"没给的算不动还是算清空"这类歧义）。<br>⚠ **`background` 单开指针语义**：没传 = **不动它**；要清空得显式给 `"background": {}`（否则"只改主题"的客户端会顺手抹掉壁纸 —— 静默的数据丢失，与 `PATCH /sessions` 的 `system_prompt` 同一条规矩）。<br>未知 `markdown.disabled` id ⇒ **422 并点名**（先验后写：坏请求一个字都不落盘）|
| GET | `/config/prefs/background` | — | 图片字节 | 服务器上那张自定义背景图（`data/prefs/background.bin`）。没有 ⇒ **404**；类型由 `DetectContentType` **现判**；带 `ETag`（= 登记里的 `version`），对上了回 **304**。`Cache-Control: private, max-age=0, must-revalidate` —— 每次回来问一句，换图才不会拿到旧的 |
| PUT | `/config/prefs/background` | 原始图片字节 | `Background` | **上传**（不是 multipart：调用方只有我们自己的前端，多一层编码只多一处出错的地方）。上限 **4MB**（前端已压到长边 1920）；非图片 ⇒ 422；上传成功铸一枚新 `version` 并登记进 `prefs.json` |
| DELETE | `/config/prefs/background` | — | 204 | 删掉那张（**幂等**：没设过也 204 —— "它现在没有图"是事实，不是错）|
| GET | `/config/chat` | — | `ChatConfig` | `config.json` 的 chat 段（不含密钥）|
| PUT | `/config/chat` | `ChatConfig` | `ChatConfig` | **整段替换** chat（其余段原样保留）|
| GET | `/config/defaults` | — | `{provider,model,agent}` | 新建会话的缺省三件（只读；`PUT /config/chat` 不动它）|
| PUT | `/config/defaults` | `{provider?,model?,agent?}` | 同上 | **三格各改各的**（nil = 不动；`""` = 清回内置缺省）；改完**当场**生效（`POST /sessions` 每次现读，不拿启动快照）|

### 运维

| 方法 | 路径 | 响应 | 说明 |
|---|---|---|---|
| GET | `/health` | `{"status","version"}` | 探针；**也过鉴权** |
| POST | `/tasks/{task_id}/cancel` | — | `{canceled}` | **按停一个任务**（2026-11-01 用户定：「任务菜单里，我希望有办法取消任务」）。<br>按的是 **TaskID** —— 面板上只有那个 id，它够不到任务自己的 ctx，所以这条路是必需的。<br>⚠ `canceled:false` **不是错误**，三种情况都回 **200**：没这个 id（早被 trim 掉了）、已经收尾、或者它压根不能停（`refresh_models` 那种纯本地的活 —— 它的 `cancelable` 是 false，界面据此**不画**按钮）。<br>⚠ 回 `true` 也**不等于它现在已经停了**：ctx 的取消是**请求**，真正停下要等任务自己走到下一个检查点（面板看到它翻成「取消」才算数）|
| GET | `/tasks` | `TaskBoard` | **任务面板**：进程内的事实（`running` + 在跑的在前的 `tasks` + 2026-11-01 加的 `queues`；与 `-debug tasks` 同一份 `Board`，不进库）。轮询用（状态指示器就问它）。重启就没 —— "没有在跑的"是诚实的事实。**没有权限过滤**（2026-11-01 账号砍了；面板就是"这台机器上此刻在跑什么"）。<br>2026-11-01 加两格：每条 `task` 带 **`group`**（`llm` / `plain` —— 界面显示「模型调用」与「其它任务」，由 `task.Kind.Group()` 这一处判定，界面**只读不猜**）；`queues` 是队列任务那一层（见下两条）。2026-11-05 起每条 task 还带 **`parent_id`**：分段压缩父 Job（`kind=compact_job`）下挂多个 `kind=compact` 子 Task；面板按它缩进显示，而不是平铺 |
| POST | `/tasks/queues` | `{kind?,title?,steps?}` 或 `{kind?,title?,session_id,blocks?,times?}` | `Queue` · 201 | **排一个队列任务**（2026-11-01 用户定）：把 N 步排成**一个**串行队列，立刻开跑。**两种给法二选一**（都给 ⇒ 400）：`steps`（显式列表，每步 `{session_id, blocks}`，跨会话也走它）或 `session_id + blocks + times`（同一个会话重复 N 次 —— 用户那句「这条对话连压 100 次」就是它）。`kind` 缺省 `compact`（现在也只有它）；`blocks` 缺省 0 = 用 `chat.compact_blocks`。<br>**三条规矩**：① **严格串行**（上一步收尾才开下一步）；② **一步失败 ⇒ 整个队列结束**（不跳过、不重试、不接着跑剩下的）；③ **可随时取消**（在飞的那一发一起掐断）。校验（会话在不在、步数、times）全在**开跑之前**做完 ⇒ 排错的队列当场被拒，不会跑到第 87 步才发现 id 写错。**不要凭据** |
| POST | `/tasks/queues/{queue_id}/cancel` | — | `Queue` · 200 | **随时取消**（幂等：已结束的再按一次，原样回它现在的样子）。**在飞的那一发会被 ctx 一起掐断** —— 压缩要整段拿到才落库 ⇒ **不会留半条摘要**。没有这个队列 ⇒ 404 |

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
- **前端打字机**（2026-11-03 用户定：「后端一块一块到，前端要在 0.5s 里平滑打出来」）：轮询到的整块不直接上屏。
  `vueStream` 仍是后端的完整目标，`streamDisplay` 是打字机当前露出的前缀；每拍 `setStreamTarget` 把新字符按
  `utils/typewriter.js` 的节奏摊开（`typewriterDuration(gapMs)`：用**上一包到这一包的实测间隔**当这一拍的时长，内容/间隔决定速度；第一包无上一包 ⇒ 默认 300ms，下限 80ms、上限 1200ms；`revealCount` 线性推进）。rAF 每帧推进，但**限到 ~30fps**（每帧重排 markdown 会吃满 CPU；30 与 60 肉眼无差）。
  一轮收尾 `await waitTypewriter()` 再 `reloadWindow`（rAF 在后台被冻住时超时 flush，不无限等）。
  ⚠ 正文与思考各走各的前缀；`bubbleReasoning` **不许回落到 `m.reasoning`** —— 那会让思考整块瞬间冒出。
  重摇候选流也走这套（`pollReroll` 把候选增量 `setStreamTarget`；气泡 live 读 `streamDisplay`，切版前 `waitTypewriter`）—— 两条流共用同一个打字机（同一时刻只可能有一条在跑）。
  末尾有**终端光标** `.stream-cursor`（2026-11-03 用户定）：它挂在 `.msg`（**不在 v-html 里**），每帧用 Range 量最后一个文字节点的末尾坐标后绝对定位 ——
  这样 Markdown 每帧重写正文不会把光标一起重建、闪烁动画也不会每帧重启。1.05s 硬切闪烁，`prefers-reduced-motion` 下常亮；流式结束就移除。

## 后台任务 / 压缩 / 能力

**任务（Task）**：**一次上游请求的全程**（形状、五个状态、署名、落库前验证清单见 `DEFINE.md`「任务（Task）与两族调用」）。
一个登记表 —— 前台一轮生成、一次压缩、一次刷新模型都挂号；**会调模型的 Task 必填会话 id**（`Begin()` 当场断言）；
兜底靠每个任务 goroutine 第一行的 `defer guard.Interrupted()`：**忘了收或 panic 记成"中断"**（Go 没有 Drop）⇒ 绝不留僵尸条目。
三份状态**各管一段、不许互为镜像**：`task` = 身份 + 生死；`turn` = 流细节（几个字、取消）+ 压缩那一档 + **重摇的候选流**（2026-10-10 起 reroll 也往里写）；`compact` = 压缩自己的细节。
（重摇是 Task：`task.KindReroll`，两族共用；看不到流与此无关 —— 当年是 `Complete` 非流式的老选择，已在 web 时代改掉。）

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
  **按块数的完整口径（2026-10-10 补记，用户曾因此困惑）**：①消息级 —— 从"第一条没被覆盖的消息"起取**至多 N 个**已闭合未覆盖块，**拿几条算几条（≥1 就走）**；②够不着才抬头 —— 顶层摘要里滑窗找**恰好 N 条**同层相邻、首尾相接、不碰开块的一组；两条路都空 ⇒ 报"无可压缩"。
  ⇒ **N 太大时"材料明明有"也会报错**（1 坨顶层摘要凑不齐 N）；爬金字塔的正确姿势：先把生消息按批压出**几坨同层**摘要，再拿 `N=2..`（或区间入口）并上去；区间入口点名一段已压缩区间，恰好被一层顶层摘要铺满时直接走合并。
- **结局带区间** ✓（2026-10-02 落地）：`Result` / `turn.CompactStatus` 都有 `from_idx` / `to_idx`（1-based）+ `merged`（是不是合并级）；
  客户端轮询到 done 就报"压好第 a–b 条（N 块）"/"并好第 a–b 条那 N 坨"，不用再翻 `/outgoing`；`-debug compact` 同样打出来。
- 落库 = **单入口 `store.RecordSummary`**（按 `source_kind` 分岔）+ 守卫 UPDATE，**同一事务**：消息级把这一段消息的 `summary_id` 指过去；合并级把孩子的 `parent_summary_id` 回填（只认还顶层的）。**绝不插/改/删 `messages` 的其它列**（正文是存档）。
- 谁也**不许**绕过这里自己拼摘要请求。不持锁跨 await：取料在锁里、调用在锁外、落库再进锁。
- **失败不阻塞**：失败就把这一次报失败（带原因），会话一个字节都不动；**重试由调用方决定**，这里绝不重试。
- **自动触发已落地** ✓（2026-10-11 用户定，口径全在 `chat/autocompact.go`）：**一轮翻 idle 之后**看占用
  （`ContextUsage` 同一把尺）——到**触发线**（`TriggerTokens`，缺省 = 预算 × 0.8；`compact_trigger_tokens` 可覆盖）
  就开始：**一次压 `compact_blocks` 个块（缺省 3）**，压完再看——仍 ≥ **退出线**（预算 50%）就接着压下一批，
  直到 < 50%、压不动（无可压缩/上游失败）、或这一步没换来任何缩减（防死循环）。后台跑（`go MaybeAutoCompact`），
  每一批自己挂号（`compact` 的 Task ⇒ "后台任务"行里看得见）；**失败不阻塞任何一轮**（停了下一轮再来看）。
  开关 = `chat.auto_compact`（**缺字段 = 关** —— 未经验证，默认不开）；关着就全靠手动。

**能力**（`internal/abilities`）：身份写死在代码里（`title` / `compact` —— **枚举就是全部**），**开关 + 可选覆盖写在 `agents.json` 每个 agent 的 `abilities` 上**（不另开 `abilities.json`），只能**覆盖**模板/渠道/模型，不能造新的；**未知的 id 在写入与启动读取时都报错**。
- **`judge` 2026-10-28 退役**（用户原话：「暂时砍掉 agent 的 judge 功能」）：它从来只有开关、没有流程（`judge.Service.JudgeFor` 一直没人调），界面上就是一块点了没反应的开关。
  退役办理：`internal/abilities/definitions` 里那一行拿掉（`Valid("judge")` 假、`IDs()` 不含它 ⇒ 界面不出、新写的配置不带）；
  `config.RetiredAbilityIDs` 记着它，**`config.LoadAgents` 读进来就地摘掉**（与 `display_mode` 同口径：读了不算数、写回去就没有）。
  **退役 ≠ 写错**：老 `agents.json` 里写着 `"judge": {…}` 是历史（现场那份就有四个 agent 带它），报错会让整份人格读不进来、服务起不来；而 `"judgee"` 那种真的写错仍然 400。
  流程与那发的实现**没删**（`internal/judge` + `task.KindJudgement`，见「决策模型（JEV）」）—— 要复活就往 `definitions` 加回一行、再把 `RetiredAbilityIDs` 那条删掉。
- **标题模型的来源三档**（2026-11-04 用户定）：`abilities.title.model_source` =
  `"global"`（默认：跟随 `config.json` 的 `chat.title_provider/title_model`，全局空 ⇒ 当前会话模型）/
  `"session"`（强制当前会话模型）/ `"agent"`（用本能力的 `provider`/`model`，两个都填才生效，否则回落 global）。
  编辑器在「智能体 → 起标题」；**默认 global**。旧配置没有这一格但填了 provider/model 的，按 **agent** 处理（保住既有覆盖）；
  未知来源在写入与启动读取时都报错。判定只有一处：`abilities.TitleModelSource`。
- `abilities.Resolve(agent, id)` 是**唯一**解析处：缺条目 = **默认全开**；`provider`/`model` 空 = 用会话的；`enabled: false` ⇒ 调用方**明确拒绝**执行那一次（不是静默降级）。
- `judge` 的调用面 = `judge.Service.JudgeFor`（开关 + 选渠道 + `task.KindJudgement` 挂号 + 失败原样返回；state/questions 由调用方拼，产出是材料**不落库**）—— 动态组合术调的就是它。**但能力已退役（2026-10-28）、至今没有调用点**：这段代码还在，接回去时记得先把 `judge` 从 `config.RetiredAbilityIDs` 里拿出来。
- compact 换模型 / 改提示词**不用写代码**（例子：会话的渠道是 dummy，但压缩单独走 deepseek）：
  `"abilities": { "compact": { "provider": "deepseek", "model": "deepseek-chat", "prompt": "…" } }` ——
  `provider`/`model` 留空 = 蹭会话自己的；`prompt` 留空 = 代码里的默认模板（填了就整段替换，`prompt_version` 指纹跟着变）；
  `enabled: false` = 这份人格拒绝压缩（明说不做，不是降级）。
- 与会话 Agent 的三条硬边界：① 产出永不进历史/树/变量（落库由调用方决定，如摘要走 `store.RecordSummary`）；② 提示词永不进会话的系统提示词；③ **失败不阻塞任何一轮**。
- `prompt_version` = `sha256(生效模板)` 十六进制前 8 位（改一个字就变，别手写版本号；列是 INTEGER ⇒ 取 32 位那一截）。
- **Agent 的导出导入（卡带，2026-10-11 用户定）**：卡带 = **agent 自己 + 它引用到的用户段**（id + 原文）——
  结构照旧按 id 引用（**不拼平**）：段随卡带走 ⇒ 零依赖，导进去还能继续组合、编辑。
  形状 `{version: 2, agent:{name, system_prompt(base), segments(id 列表), first_message, abilities, prepend_state}, segments:[{id,text}]}`；
  **内置段不带**（每台都有；内置 id 保留也造不出来）；**不带 id**（导入方现铸）、**不带渠道**（软引用）。
  **导入合并规则**：系统里已有同 id 的段（用户段或内置）⇒ **丢弃卡带那份**（用本地的——id 即认同，
  同 id 不同文本时也以本地为准）；没有 ⇒ 建它并打上 **`imported: true`**（来源标记，界面显示「导入」徽标；
  记的是"从哪来"，编辑不清标）。v1 老卡带（提示词已拼平、无 segments 数组）照样能导。
  **不另开路由**：拼/拆在客户端（`utils/cartridge.js`），落库用现有 `POST /segments` + `POST /agents` ✓。
- **占位符**（`{{…}}`）：白名单 = 枚举（`{{system_time}}` / `{{state_before}}` / `{{state_after}}` / `{{range}}` / `{{blocks}}`）；**不认识的 `{{foo}}` 原样留着**；**只扫一遍**（替换进去的值不再当模板扫）；**会话 Agent 的提示词永不替换**（它一变，前缀缓存每轮全废）。（`template` 模块本身还没建 ✗。）

## 数据模型

**只有一份**：`core/internal/store/migrations/001.sql`。
**开发阶段没有向后兼容**：改形状就改这个文件，然后把 `data/microchat.db` 删掉重来（用户 2026-09-29 定）——**不要**写 002、003。

**四张表，全是 TEXT id（UUIDv7）+ 毫秒整数时间戳。**

### `sessions`

`id` · `title`（首句自动生成；空串 = 还没起名）· `system_prompt`（**空串 = 没覆盖** ⇒ 用 agent 的拼装结果）· `provider` / `model` / `agent_id`（都是**软引用**，无外键）· `created_at` / `updated_at`（界面按 `updated_at` 排序）

（**没有 `current_leaf`**：线性会话不需要"停在哪儿"，"最新一条" = `ORDER BY id DESC LIMIT 1`。）

### `messages` —— 一条消息，**线性会话里的一格**

`id`（生成回复时**受理那一刻**就算好；**顺序就是它**）· `session_id`（FK CASCADE）· `role`（`CHECK IN ('user','assistant')`）· `content`（**存档本体**，`<state>` 块原样留着）· `created_at`
后四列**只服务显示**（出站/状态/编辑一律不看）：`reasoning` · `reasoning_ms`（受理 → 第一段正文）· `duration_ms` · `usage`（归一化后的 JSON）

### `models` —— 发现所得 + 用户覆盖（PK `(provider, upstream_id)`）

`upstream_name` / `owned_by` / `context_length` / `max_output` / `upstream_params` / `first_seen_at` / `last_seen_at`（**这次没见到就删行** ⇒ 列表 = 上游当前那份）
**用户列**（刷新永不动）：`display_name` · `params` · `context_override` · `tokenizer`（缺省 `{"kind":"approx","ratio":1.3}`）

### ~~`users`~~ —— **这张表已经删了**（2026-11-01，账号功能砍掉）

老库里的它由 `store.dropRetired()` 在打开库时 `DROP TABLE IF EXISTS users` 清掉（幂等）。
原来的形状（`id` / `name` / `pass_hash` / `is_admin` / `ver` / `tg_user_id` / `created_at`）
只剩历史意义 —— **别照它建回来**：没有登录者，就没有这张表。

### `summaries` + `provider_state`

`summaries`：`id` · `session_id` · `parent_summary_id`（**深度 = 指针链长度**，不存 level）· 出口另带**派生**的 `begin_idx`/`end_idx`（区间两端的消息序号，不落库）· `source_kind`（`message`/`summary`，**同质**）· `begin_message_id` / `end_message_id`（**它盖住的那一段**，闭区间，可空）· `text`（只写叙事；混进 `<state>` 会被剔除并记日志）· `blocks` · `tokens` · `source_ids`（当时吃的是什么，供审计与重做）· `provider`/`model`/`prompt_version`/`usage` · `dirty`（被覆盖的消息被编辑过 ⇒ 1；界面显示"已过期"，**装配时照用**）· `created_at`
`provider_state`：`provider` · `last_refresh_at`（"上次拉取：N 分钟前"）

**索引/外键/运行时**：索引 `messages_by_session(session_id, id)`（**顺序就是 `id`**，取"最新一条"也走它）、`messages(summary_id)`、`summaries(session_id/parent_summary_id)`。外键**只有两条**（`messages` 的两个）：`session_id` ⇒ CASCADE、`summary_id` ⇒ SET NULL；`begin/end_message_id` 与 `agent_id`/`provider` 是故意不加约束的（`begin/end` 的失效由 `store.DeletionPlan` 一份计算负责）。迁移由 `PRAGMA user_version` 驱动（当前 **只有 001** —— 开发阶段改形状就改它、删库重来，**不许追加** 002）；`foreign_keys=ON`、`journal_mode=WAL`；写入由一把锁串行化。

## 摘要 / 压缩：只插行、按区间跳（推导见 `reminder/compact.md`）

- **压缩只往 `summaries` 插行**，绝不插/改/删 `messages`（删消息的级联是另一条路）；摘要盖的是**一段区间**（`begin/end_message_id`，
  创建时写一次），失效只有两条：被盖的消息**被删** ⇒ 连同父链作废；**被改写** ⇒ 只标 `dirty`、照用。
- 装配**按区间跳**（`state.BuildOutgoing`）：有 `summary_id` 且区间左端就是这条 ⇒ 升到最粗的左端对齐祖先、一次跳到区间右端之后；
  否则发原文。**能取粗的不取精，宁用细的不许漏**。
- **只有 Compact，没有"丢"**：不存在"少发一段没人代表的内容"。超预算 ⇒ 先压再发；真压不动 ⇒ 报错原路返回。
- 用户态按块（整块对齐、开着的那块永不压）、算法层按 Message；摘要里**不存状态**；剪枝没做（先当没这回事）。
- **自动触发已落地** ✓（2026-10-11 用户定，口径全在 `chat/autocompact.go`）：一轮翻 idle 之后看占用，
  到触发线（`TriggerTokens`，缺省 = 预算 80%）就压，**一次压 `compact_blocks` 个块（缺省 3）**，
  压到退出线（预算 50%）以下停；压不动 / 没进展就停（防死循环）。后台跑、每批自挂号；开关 `chat.auto_compact`（**缺字段 = 关**）。

## 与上游通信：网关只认会话头（细节见 `reminder/gateway.md`）

**只许用成熟的外部库**：HTTP 一律标准库 + 现成的 SSE 库（**不手搓协议解析**）；数据结构用 serde/encoding-json 映射成纯数据形状。**不自己写传输层、不自己写协议解析**（手写协议解析是 bug 温床）。
超时别忘：`providers.json` 的 `timeouts`（缺省 connect 15s / total 300s）。**不设总超时 = 上游卡住、界面转一辈子**。
**辅助调用另有自己的短上限** ✓（`title` 10s / `compact` 60s，常量就在各自包里）：它们跑在**这一轮翻 idle 之前** ⇒ 只靠渠道那个 300s 会让一轮被上游按住五分钟。
`usage` 各家字段名不一，**归一化只有一处**；前端只管读 `cached_tokens` 这些键。
**成本：聚合站报，直连渠道不报**（2026-10-20 改口 —— 从前这句写的是"成本算不了：API 不返回 cost"）：
OpenRouter 每一发都在 `usage.cost` 里回**实扣美元金额**（不需要任何额外参数；官方文档明说
`stream_options.include_usage` 已废弃、用量永远给）。归一化把它折进 `usage.cost_usd`（**指针**：
`0` = 免费、`nil` = 上游没说 —— 两者不许混，界面上 `nil` 什么都不显示，绝不显示成 `$0.00`）。
DeepSeek / OpenCode GO 直连**确实不报**，所以那一格在那儿永远是 `nil`（这是诚实的缺失，不是 bug）。
**成本还有一道渠道能力闸**（2026-11-02 用户报的 bug）：`presets[].supportsCost` 只有 OpenRouter 为真；
不支持的渠道即使上游回了 `cost: 0`，也由 `Provider.costAwareUsage` 在**归一化之后、返回之前**摘掉 `cost_usd`
（不是显示成 `$0`，是整格不出现）。从前只看"有没有这一格"，于是不支持费用的渠道回个 0 就会被画成 $0。
气泡脚注 = `utils/format.js` 的 `usageFoot`（token + 成本；契约测试钉着）。
⚠ **别拿 `top_provider.max_completion_tokens` 当输出预留**（2026-10-20 踩过并回退）：OpenRouter 的
`max_completion_tokens` 是"这个模型**最多能吐多长**"（deepseek-v4.1-flash 是 943718），
而 `chat.ContextUsage` 要的是"**给回复留多少额度**"（Pi 的 `reserveTokens`，缺省 16384）——
语义不同、量级差两个数量级。按它算预算会把 1M 上下文的模型算成只剩 104K 可用，
等于凭空触发压缩。`max_output` 缺省就该回落 4096，除非上游明确报了"预留"这件事。

- **硬闸只有一个：`x-opencode-session`**（每会话稳定 UUID = `sessions.id`，裸 UUIDv7 零转换；缺了网关 400）。
  官方"别用通用库名"目前是要求而非强制（仍照做）；`x-opencode-client` 不是必需。实测矩阵见 `reminder/gateway.md`。
- **一个会话对上游只表现为一个 session**：凡会调模型的调用（主聊天、摘要、标题、判断、将来的子 Agent）一律带 `sessions.id`；
  **不许派生第二个 id**（UUIDv5 方案作废），**不为没上下文随机铸 id**（`task.Begin()` 当场断言；只有不调模型的 Task 可无会话）。
  缓存按**内容前缀**算、与 session id 无关 ⇒ 别为缓存分子会话；也别为"拒绝缓存"发明 header（Pi 的 `cacheRetention: "none"` 对 OpenCode GO 是 no-op）。
- 思考回传用 `reasoning_content`（网关照收；deepseek 系没思考补空串、别的系不塞）；`reasoning_effort` 关不掉（下限 low）。
- **工具不在提示词里**：请求体顶层 `tools` 数组；回话是助手消息里的 `tool_calls`，结果以 `{role:"tool", tool_call_id}` 回填。两条铁律：历史里出现过工具 ⇒ `tools` 参数必须在（哪怕 `[]`）；不认识的字段默认不发。形状/开关/实测见 `reminder/tool-calls.md`。
- **`/debug/last-payload` 必须连请求头一起回显**（打码）—— 不然"为什么 403"只能靠猜。

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
- **两轴**：`vendor` = 找谁（端点预设 + key env + 内建头）；`protocol` = 说什么话（`openai-chat-completion` 缺省 / `openai-response` / `systemone` / 占位中的 `anthropic-messages` 等）。
  渐进式配置：先选 `protocol`（干什么），再按兼容矩阵选 `vendor`（找谁），有预设的只输 `api_key`，只有 `custom` 才要 `base_url`。
- **兼容矩阵即校验表**（`Provider.Validate`，错配 loud error）：chat-completion ← 除 typesafe 外全部 vendor；`openai-response` ← opencode-go/opencode/openrouter/custom（`bd27768` 打通，muse 真测 ✓）；systemone ← `typesafe` / `openrouter` / `custom`；
  占位只剩 `anthropic-messages` / `gemini-generate-content`（选了就报"暂不支持"）。
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

## 决策模型（JEV）：**不是聊天模型** —— 已落地 ✓（2026-10-01：`protocol: systemone` + `task.KindJudgement` + `internal/judge` 裸调用）

`typesafe/jev` 那类是**决策模型**（状态 + 类型化问题 → 概率），不生成文本、不能驱动会话、不走 `refresh`（没有统一 `/models` 口径 ⇒ 跳过不算错）。
落点：`vendor: typesafe`（或承载它的 openrouter / custom）× `protocol: systemone`（verbatim 整条 URL 原样 POST `{model,state,questions}`）；
`judge` 能力的 Task 面就是 `task.KindJudgement`（"判断"，要会话 id —— 调外部模型就要归属可查）；裸调用在 `internal/judge`（10s 超时、不重试、失败原路返回）。
⚠ **2026-10-28 起 `judge` 这个能力退役了**（用户：「暂时砍掉 agent 的 judge 功能」）：开关没了、界面没了，`internal/judge` 与 `task.KindJudgement` 原样留着但**没人调**。
规划中的用途与四条规矩（只动尾部 / 失败降级 / 产出不进历史与变量 / 阈值实测校准）见 **`reminder/jev.md`**。**裁决（阈值/NPC 动作集）还没做** —— 那是第二刀。

## 界面（口径）

### 通用（所有前端都要守）

- **当前会话认 `id`、不认位置** ✗：`GET /sessions` 按 `updated_at` 排，发一句话就会重排 —— 存下标必然指到别人身上。
- **不可逆的删除先摊开、等人点头**：消息级删除必须先看删除预览（`GET .../deletion-preview`）并让用户确认，再带 `last_deleted_message_id` 发 DELETE；删整条会话同理。
- **旧数据认会话**：context / reroll / prompt 各记自己属于哪条会话，对不上就不许拿来画（换会话时旧的那份还留着）。
- **状态行的两个数是「人话格式」**（2026-10-17 用户点名）：`formatTokens`（`format.js`，唯一出口）——
  `9857 ⇒ 9.8K`、`995904 ⇒ 995K`、`1048576 ⇒ 1.04M`，**一律向下取整**（这两个数里有一个是「我用了多少」，
  向上取整会把用量说大；把 995904 写成 996K 还会让人以为分母真是 996K）。状态行**分母 = 输入预算**
  （`ctx_len − max_output`，**不是**模型上下文）—— 悬停给算式（`statusLineTitle`），移动端没有 hover ⇒
  **设置 → 聊天 → 模型上下文 tokens** 底下把同一句话写了一遍（「填 1000000 时显示 995904」就是这么来的 ✓）。
- **用量两处别混**：卡片脚注的单位是 token（上游 `usage` 报的；`reasoning_tokens` 是 `completion_tokens` 的子集，不是另加）；生成中气泡等首字节显示三点呼吸、思考先到则折叠框标签「思考中…」（**不显示字数**；流式帧里没有 token 数，字符数也没在气泡上展示）。
- **分岔 = 复制会话**（`POST /sessions/{session_id}/copy`）：没有"切分支"这回事 ✗（`current_leaf` 已删）。
- **重摇的位次与退出**（消息 / 摘要两家族同一套闸与位次）：进模式并摇一版；`switch <n>` 就地换正文（消息 UUID / 摘要 id 不变）；`delete <n>` 删一版（删到只剩一条 ⇒ 退出且不 apply）；`off` 显式退出。**候选只在内存里** ⇒ 发新消息 / 删目标 / 切会话 / 重启后端 ⇒ 自然消失。
- **web 的重摇入口**（2026-10-10 定，尾条气泡下）：`⟳` = 摇新一版并**自动切到最新那版**（**点下那一刻就把那条气泡切成空白 +「正在输出…」**，随候选流长字 —— 2026-10-17 用户定：旧正文一直挂到第一帧才被换掉"有点丑"；空白不等于丢内容，旧版就是候选 #1，切回去/摇失败照旧回来。判据是纯函数 `chatrows.rerollTargetLive`/`bubbleText`（`startingId` 刚落手 ∥ 后端 `running`），契约测试钉着；气泡的 ⟳ 会把**被点那条的 messageId**一起 emit，`rerollNow(targetId)` 优先用它置 `startingId`（命令/版本导航没有 id 才回落 `lastMsgId`）—— 别只靠"尾条 id"猜，猜错时那一拍不会立刻隐藏；摇完切版时**预览与 starting 同一拍清**，不然会闪一帧空白）（无论当前看着哪一版；`/reroll` 命令同一条路）；`◀ n/N ▶` = 翻版，**最右的 ▶ 再按 = 再摇一版**；摇的过程中候选流**就地预览在目标气泡里**（游标读 `/turn/text`，摇完/切版/失效即清），位次牌摇时显示 `n→N`。**位次是 1-based**（`Idx: 1` 是首发候选；`current_idx`/`items[].idx` 同基准 —— 别拿数组下标当位次传 `switch`，踩过：旧 `/reroll` 命令因此切版必错）。
- **`/rename` 只改名**：走已有的 `PATCH /sessions/{session_id}`，**不新增路由**；空名字 ⇒ 拒绝。用户改过名 ⇒ 后端自动起名永不再覆盖（不变量在后端，客户端不多事）。
- **生成中那条回复是合成的**（库里还没有它；等首字节时气泡给三点呼吸 **+「正在输出…」**，2026-10-17 用户点名要一句字 —— 只有点没有字既小又说不清是在输出还是在等）：数据来自 SSE `GET .../turn/events`（status + 游标读；断线重连带 Last-Event-ID；8s 没事件回落 `/turn/poll`），`idle`/`error` 才重拉消息；收到 `idle` 顺带刷一次会话列表（自动起的标题在翻 idle **之前**写好，不刷就一直停在"还没起名"）。等首字节那段气泡显示**三点呼吸**（正文与思考都还没来）；推理先到则换成思考折叠框、标签说「思考中…」（落库后才是「思考过程（2.1s）」）。
- **设置页要的数据只从一处发**（连上 / 刷新 / 进设置页都走它）——两处各写一份，新字段只进一条路 ⇒ 设置页永远"加载中"（见「踩过的坑」）。

### Web（当前主力，`frontend/web/`，Vue 3 + Vite）

- 布局：左会话栏（顶部**没有 MicroChat 字样**，改成「对话 / 生图」两个 tab（2026-11-04 用户定；生图暂未接 API，切过去只改选中态）；下面「＋开启新对话」；高亮当前；列表按**今天 / 昨天 / 一周内 / 一月内 / 更久**五组（2026-11-04 用户定；`utils/sessionGroups.js` 纯函数，本地日历日边界）；每行**只有一枚「选项」浮板**（会话设置 / 创建副本 / 导出 JSON / 删除会话，2026-11-04 用户定）；标题字号 0.92em、行 padding 0.32em（用户定：再小一点、再紧凑一点））｜`#content` = **对话与设置同级**（2026-10-10，OpenWebUI 式）—— 对话（顶栏（2026-10-14 用户定，**去壳版**）= **透明底**（无外框线；小字压背景图用双层柔影 + 提亮一档的 hint 色）+ 左上角 = **圆头像（当前 Agent，点开选人；名字首字 + id 派生色，2026-10-14 用户定）+ 当前模型平铺字**（点开是按渠道分组的浮板，见下面「模型选择器」那条）+ 两侧开关（**只有某侧栏收着时才出现**：左在左端、右吃 margin-left:auto 贴右端；栏开着时开关在栏自己头上 —— 左栏头右端、右栏 tabs 行**左端**（各指向自己缩回去的方向，2026-10-14 用户定））；**会话抬头不在顶栏**（左栏列表高亮 + 状态行都有它）、**Agent 的"选"就在那头像上**（切换 = 点头像掉浮板；"显示"在状态行小字里）—— 消息列 + 输入行（**滚动容器占满整个对话区**（滚动条贴最右缘，2026-10-14 用户定）—— 阅读列约束（`--read-col`，**920**，2026-10-15 从 760 放宽；输入行共用同一个变量）落在里面的 `.messages-inner`，气泡不拉长）；**顶栏与输入行都是悬浮层**（2026-10-14 用户定：盖在消息列上、消息从底下滑过，不再与它分地盘 —— 顶栏**不透明**（2026-11-01 用户定：「然后顶栏是不透明的比较好吧」—— 从前是 `background: none`，消息滚到栏底下能透出来，读顶栏那行字是隔着文字读的；现在用页面底色 `var(--tg-bg)` 铺满，**但栏上没有边框、没有分隔线**：要的是「挡住」，不是把栏画成一个盒子；
再补一道**向下阴影**（2026-11-01 用户定：「顶部栏有阴影」—— `--shadow-top`）：不透明之后那条边是齐刷刷一刀切，
像正文被裁掉了，影子才读成「栏在内容上面」⇒ 阴影在这儿**替边框干活**，所以仍然不描线。
⚠ 阴影的几何别照抄侧栏那道：`-6px` 的负扩散会把渐变大半缩到栏背后，露出只剩一截（读起来是脏边）；现在是
offset 6 / blur 20 / spread -4，渐变往下铺约 12px。深色主题底是 `#131313`，黑影在近黑底上本来就淡 ⇒ 浓度取到 34% 那档的上限。2026-10-14 那版是「全透明 + 双层柔影」，试过 mask 渐隐的毛玻璃，撤了），输入行自己就是浮卡；消息列底部留白 = `--composer-h`（输入行实测高，`composerRO` 现量现写，textarea 长高不脱节）。⚠ CSS 里 `#chat-col > *:not(header)`（1-0-1）会压过 `#composer`（1-0-0）的 position —— 那条共享规则**别写 position/z-index**，踩过）与**设置视图**互斥切换；右载荷栏（**tabs 常驻顶部，三个面板各自滚动**（2026-10-17 用户定/修）；三 tab：真请求直放（裸 JSON ⇄ **分块视图**——把 `body.messages` 一块块画成卡片，开关记忆在 `mc_payload_viz`；**整行可点开合、>500 字的块默认折叠**（system prompt 那种巨块不撑爆面板；折叠态给单行预览）；展开不设嵌套滚动——面板整体滚，触摸纵向手势按轴锁归浏览器（2026-10-11）/ 世界状态 / **压缩树**）随对话一起让位。**设置不再弹窗**：占满左栏右侧整块、顶部是**一行式工具栏** `[◧][五区 tabs][×]`（不设"顶部卡"、无标题；Esc 返回）。**窄屏（断点即身份，2026-10-10）**：≤1100 右载荷栏、≤700 左会话栏改**覆盖式抽屉**（`collapsed` = 关着；[◧][◨] 调出/收起 + 遮罩点击收起 + 选中会话/开新对话自动收）；窄屏**不读不写**桌面偏好（`mc_bar_*_hidden`），跨断点时两侧统一先关（不然会"啪"地盖上来）。遮罩的显隐由 CSS 兄弟选择器跟栏的类联动，不用 JS 状态。
- 接线全在 `src/App.vue`（SFC 直接挂模板）；`src/api/client.js` 只调 `/api/v1`；`src/utils/` 放纯函数（contract 测试钉住请求形状）。
- **门板/登录的视觉**（2026-10-14 用户定"更好看"）：玻璃卡（`backdrop-filter`）+ 品牌头（**内联 SVG** 标 —— 无外部资源，离线 SW 照旧）+ 主题色光晕 + 入场动画（尊重 `prefers-reduced-motion`）；**底自己铺 `--mc-bg-image`**（就是用户背景图那枚变量）+ 一层跟主题走的面纱 —— 门板在 `#app` 里、够不着 `body::before` 的图层，想要"透出背景图"**别用透明**（会把聊天壳一起透出来，实测截图里左栏顶栏全露馅）。
- **连接门板**（2026-10-10 加）：启动自检探不通后端（新设备第一次打开/地址没填对）⇒ 不开破界面，先给全屏"连接到 microchat 后端"面板：填地址（裸写 `192.168.0.110:8787` 也行 —— `normalizeApiBase` 自动补 `http://` 与 `/api/v1`）→ 过 ⇒ `saveNetPrefs` 存 **JWT** + **刷新页面**进聊天（沿用"改完刷新生效"）。**地址栏只在"探不通"时出现**（2026-10-14 用户定）：探通 ⇒ 收起，登录界面只有用户名/口令（单容器部署里浏览器永远同源，不该再问地址）；中途又断了 ⇒ 地址栏自动放回来。**2026-10-10 多用户改造**：启动自检探开放口 `/auth/status`——探不通 ⇒ 门板带原因；`needs_setup:true` ⇒ 「初始化 root」（**uid 0 恒在**，只设口令+确认；名字固定 root）；已初始化 ⇒ 登录（地址 / 用户名 / 口令）。探针两道闸照旧（`normalizeApiBase` + 必须回 `status:"ok"`）。
- **模型选择器**（2026-10-28 用户定：「既然助手切换器和上下文使用率指示器都有漂亮得点击后显示的面板，可否把模型的那个选择器也改成这种自定义面板，按 Provider 分类的（接近 openwebui 风格）」）：
  从前是 `select#session-model.flat-select`（**原生选项**）—— 聚合站动辄几百个模型（OpenRouter 实测 470），
  一条滚不完的长菜单里既搜不了、同一渠道的模型还不挨在一起。现在是顶栏那枚字点开的浮板
  （`#model-btn` + `.model-menu`，壳子借 `.agent-menu` 那一套 `--tg-float` / `--shadow-3`）：
  **搜索框**（命中显示名 / 渠道名 / 上游 id —— 上游 id 必须参与，显示名常常不含渠道信息）+
  **按渠道分组**（组头 = 渠道显示名 + 条数）+ 当前项打勾 + 上下键与回车。静息**仍然只有字**
  （顶栏是透明的，栏面上不该长出一颗按钮），悬停给一层薄底说"这能点"。
  ⚠ **过滤 / 分组 / 扁平序号是纯函数**，住 `frontend/web/src/utils/format.js`
  （`filterModelGroups` / `curModelLabelOf` / `modelMatches`）—— 住组件里就只能靠正则钉源码，
  钉不出"搜到了什么"。
  ⚠ 三块浮板（模型 / Agent 选人 / 上下文环）**互斥**：开一块要关掉另外两块（叠在一起既看不清也点不准）。
  ⚠ 顺带删掉了 `modelSel` / `reapplyValues` 那套补丁 —— 那是给 `<select>` 打的（选项异步到、`value` 先落进
  DOM 会被浏览器换掉）；自己的面板里值就只是数据。

- **进会话时模型名不闪**（2026-11-04 用户报 `dummy/dummy → deepseek/deepseek-v4.1-flash → DeepSeek V4.1 Flash`）：
  会话加载期间（`loadingSession`）顶栏渲染骨架并禁用模型按钮；模型列表还没到时 `curModelLabelOf` 回
  「加载模型…」而不是拿 provider/upstream_id 裸串；列表到了才出显示名。`loadingSession` 由 App 传给 TopBar，
  骨架住 `.model-loading`。
  ⚠ **`<script setup>` 里 `defineProps` 必须用 `const props =` 接住**：直接写 `props.loading` 编译器不会报错，
  运行时 `props` 未定义 → computed 抛错被 Vue 吞掉 → 标签**静默空白**（契约测试钉着 `const props = defineProps`）。
- **模型显示名的版本号**（2026-11-04 用户报 `claude-opus-5-5` 显示成 `Claude Opus 5 5`）：`registry.Prettify` 把两个相邻的**单数字**词合成点号（`5-5 → 5.5`），多位数日期段不拼（`4-5-20251101 → 4.5 20251101`）；用户覆盖与上游名仍优先，prettify 只是第三级回退。
- **模型名宽度**（2026-11-03 用户：「顶部那个模型名称栏目还能扩大，有空间就完整显示」）：不再一律截断。
  `.model-anchor` 吃满剩余空间、`.model-btn-label` 可收缩；≤1100 收紧顶栏 gap/padding（把空间还给模型名）；
  ≤700 真放不下时**允许换行**（`white-space: normal` + `overflow-wrap: anywhere`），当前模型名完整显示。
  顶栏因此可能变高 ⇒ App 的 `topbarRO` 把实测高度写进 `--topbar-h`，窄屏 `.messages-inner` 用
  `padding-top: max(2.8em, var(--topbar-h) + 0.6em)` 让位（这条必须写在所有 `.messages-inner` 规则之后，
  不然会被 `padding: 2.8em ...` 的简写覆盖）。实测 800px 宽长名单行、390px 两行，两者都不再省略。
- **默认后端地址跟着"你从哪儿打开页面"走**（2026-10-10 加；2026-10-14 改候选序）：没存过 `mc_api` 时启动自检按候选探：`apiBaseCandidates()` = 存过 ⇒ 只认存的；没存过 ⇒ [**同源 `/api/v1`（首选）**，`location.hostname:8787`]—— 同源优先是因为 vite 现在把 `/api` **代理**给后端（见「Docker 部署」），dev 与单容器部署都走它 ⇒ 浏览器**永远同源、零地址配置**；同主机 8787 退为兜底（前端被别的静态服务器端出来、没有代理时，手机从 `192.168.0.110:8788` 打开就直接连 `192.168.0.110:8787`）。非首选胜出 ⇒ 存下来 + 刷新一次（`sharedApi` 等一切读的都是 `apiBase()`，不存会指错）。推导只读不写 —— 不偷偷存。
- **滑动面板**（2026-10-10 加，触屏**跟手拖拽**）：左滑出右栏、右滑出左栏 —— **拖到哪儿面板停哪儿**（半开也维持着，不用等松手），松手才吸附（CSS 的 200ms 过渡演动画）：**中点吸附 + 两个方向的快甩都认**（`|v| ≥ 0.0025 开度/ms` 且已拖 6%）。⚠ 三条实测修正：① 只写"朝开"的 flick ⇒ 快关全落回"必须拖过 60%"，真机"关闭发涩"——对称补上"朝关"；② 速度样本有 **120ms 保鲜期**（甩完停住手再松手按位置算，不然历史峰值把"拖一半停手"也弹成开）；③ `v` 的单位是**开度/ms**（真实手速 0.002–0.01，写 0.5 = 死弦）。**先关后开**：已有一扇开着 ⇒ 只认"朝它关回去"的方向（跟手关，同样半开）——同向忽略，不许两扇同开。判定在 `utils/swipe.js`（纯函数：`classifyAxis` 先到先得的轴向锁 / `pickDrag` 拖哪扇从哪起 / `dragProgress` 位移→开度 / `resolveRelease` 松手吸附）；DOM 只做三件：跟手写内联 transform（transition 先掐）、松手清内联落 `collapsed` 类、scrim 透明度跟开度。不接管的场合：输入框/下拉、`pre`（自己的横向滚 —— 但**抽屉自己身上一律接管**，`pre` 也不例外：右栏载荷 tab 整块是 `pre`，早先被这条吞掉，真机表现"在面板上滑没反应"）、命令面板与 picker、设置视图、模态开着时；平板宽度（700–1100）上左栏是**常驻列** ⇒ 不算"该先关"、右滑也不动它。⚠ 坑（都实测咬过）：① 锁轴后**必须记住 axis**（漏了每枚 touchmove 都重进锁轴分支，第二枚就被自己摘掉的 `collapsed` 骗死）；② 锁轴时要**把 `d.p` 初始化成起点开度**（开=0/关=1），且**别动 `d.lastT`**（它在 touchstart 起表；重置会让第一枚 move 的 dt≈0、速度样本作废）；③ 速度阈值的单位是**开度/ms**（真实手速 0.002–0.01，早期写成 0.5 = 死弦）。
- **滚动隐藏上下栏**（2026-10-17 用户定：**触屏默认开**，用户视图 → 外观 可关）：跟浏览器一个味道 ——
  **往下看（内容前进）顶栏按位移逐步收起，往回看底栏逐步收起**，进度与位移**成正比**（不是"过阈值啪一下"，
  那是开关不是体验）；**一次只藏一边**（反向滚动把对面那栏放回来），到顶/见底立刻全放回；
  反向不足 6px 算抖动（真手指的滚动是一串正负交替小步，不滤会把栏抖出重影）；一次跳 >200px 视为切会话/贴底 ⇒ 重置。
  判据是纯函数 `utils/chrome.js` 的 `stepChrome`（契约测试钉着），App 只把算出的两个像素数写成根节点的
  `--chrome-top` / `--chrome-bottom`，CSS 只做 `translateY`（90ms linear 抹抖，reduced-motion 里关过渡）。
  触屏判定 = `(pointer: coarse)` ⇒ 鼠标滚轮不参与（桌面滚不隐藏）。
- **移动端键盘不遮输入框**（2026-10-10 加）：`utils/viewport.js` 用 **visualViewport** 把应用盒拟合过去 —— `#app` 高 = `vv.height`、`translateY(vv.offsetTop)`（iOS 的"可视窗上推"跟着走；捏合缩放 scale>1 时不拟合）。`#app` 一带 transform 就成了其内全部 `fixed` 浮层（命令面板 / picker / 确认框 / toast / 连接门板）的**包含块** ⇒ 浮层自动钉在**键盘上沿**；命令面板/picker 限高 `calc(100% - 7em)`（百分比按包含块解析 ⇒ 键盘缩盒时自动跟着缩，小屏不顶出视口，内容自己滚 —— 实测过 617px 面板裸奔出视口）。`index.html` 的 `interactive-widget=resizes-content` 给 Chromium 系先缩布局视口（与 vv 拟合叠加不打架，都以 vv 为准）。配套的**文档锁死**（`html, body { height: 100%; overflow: hidden; overscroll-behavior: none }`）缺不得 —— 见踩过的坑「页面还能延展」；`#session-list` 也终于有自己的滚动（此前没有任何滚动规则，会话一多直接被裁）。
- **用户视图**（2026-10-10 加，与设置**平级**的另一半）：左栏底 `👤 用户` 打开它 —— **设置管实例/服务器**（聊天参数、渠道、Agent、会话），**用户管登录者自己**—— 身份（`/auth/me`：名字 + 管理员标记 + **退出登录**）与个人偏好（主题 / 背景图 / 发送键 / 输入预演）；**用户管理（加人/踢人/改口令）下一步做**。两块**互斥**（一屏只开一个，互相打开时对方让位）；视图口径同设置：`[◧] 用户 [×]`、Esc 一次只关一层；设置视图因此从六区缩成**五区**（原「界面」区整块搬走，`SETTINGS_TABS` 在 format.js）。
- **上下文环（2026-11-02 合并后台任务）**：用户定「以上下文指示器的绿色圈圈为主体。中央原本空着的区域，放入代表活跃后台任务数量的数字。在后台任务运行时，绿色圈圈会转动」。
  ⇒ 顶栏只留 `#ctx-ring`：环长仍是已用 / 输入预算；**环心**是还在跑的条数（`max(/tasks.running, 未结束队列数)`；队列当前那一步与队列本身是同一件事，取 max 避免重复；`finished_at` 有值的不算）；`taskBusy`（原子任务或队列在跑）时整个 SVG 转（`@keyframes ctx-ring-spin`，1.1s 线性无限；`prefers-reduced-motion` 下不转，但数字仍在）。
  点环打开的面板分**两个 tab**（2026-11-02 用户定：「上下文状态 和 正在运行的任务，分为俩TAB」）：「上下文」= 数字、连接/助手/运行三行；「任务」= **只列正在跑的**队列 + 原子任务（进度、时长、失败红字、取消；结束的立刻消失）—— 取消仍走 `@cancel-task` / `@cancel-queue`。tab 状态是 `ctxTab`（默认上下文），不写 localStorage。
  面板文案要**严谨、专业**（2026-11-02 用户最终定：先嫌太机器，后嫌"还能装 / 给回复留的地方"太口语）：「上下文」页用标准口径 —— `已用 766 / 995K`、`不到 0.1%`、`剩余可用`、`自动压缩阈值`、`上次实际用量`（数值带 `tokens`），预算一行写成 `输入预算 995,904 tokens = 上下文上限 1,000,000 − 输出预留 4,096`；状态行 `连接状态 / 当前助手 / 运行状态`，相位 `空闲 / 等待响应 / 生成中 / 出错`；任务种类 `刷新模型 / 对话 / 压缩 / 分段压缩 / 起标题 / 重摇 / 判断`（原始 `refresh_models` 不上屏），耗时 `0.4 秒 / 1 分 20 秒`，分组 `模型调用 / 其他任务`；空态 `当前无运行任务`。不写"还能装 / 给回复留的地方 / 没在跑"这类口语。
  没有上下文但有任务时，环给一小段 `18 82` 的弧（`ringDash`），否则空环转起来看不出来。
  ⚠ 环心数字包一层 `.ctx-ring-n`（字号 0.68em 单独一层）：尺寸与字号写同一个元素会叠乘。
  ⚠ **0 个时环心不显示数字**（2026-11-02 用户定「如果没有后台任务，中间不显示 0」）：环本身仍在、不转；有任务才出现数字。契约测试钉着 `v-if="activeTaskCount > 0"`（别用 `taskRunning`，队列也要算）。
  ⚠ 数字只从 `/tasks` 的 `running` 与 `queues` 推（取 max，见上），别在前端自己 `filter` 数一遍 —— 前端时钟与「什么算在跑」都跟后端不一致时，环心的数与面板里的行数会打架。
  App 仍每 2s 拉一次 `/tasks`（进程内面板、零成本），受理类动作（发送/重发/压缩/重摇两族）后**立刻补刷一次**不等节拍；喂数只有一处（`topbarRef.setTasks(board)`，取不到就 `null`）。
  ⚠ 从前那行小字与 2026-11-01 的独立任务徽标都已删：**同一个数字住两处，只会让两处都可能过期**。
- **共享层（2026-10-10 审计整改）**：`utils/prefs.js` = 界面偏好与共享 api 客户端的唯一出口（主题应用含 TG 守卫 —— TG 里 data-theme 根本不设；发送键判定/提示、apiBase/Token、`sharedApi()` 按偏好缓存）；`api/client.js` 的 `mergeStreamSlice` = 流式合并唯一规则（游标回退 ⇒ 清零重建）；api 错误统一带 `code`/`status`（分支按 code，别猜文案）；`format.js` 的 modelKey/modelLabel/whoText/sessionTitle = 键与标签的唯一出口；`COMMAND_META`（commands.js）与 App 执行器启动对账。
- **CSS 复用基线（2026-11-02，用户定「减少 CSS 的使用量、多复用少重写」）**：全站仍是手写 CSS（不引 Tailwind —— 它与 em 缩放/大量状态选择器不合，且会把 CSS 搬进模板）。复用只有四处，新样式照此写：
  ① **幽灵按钮底座** `main.css` 那一组（`.icon-btn / .foot-btn / .edit-btn / .link-btn / .cp-close / .ctree-tw / .ctree-undo / .collapse-all / .session-item 的两枚 / 模型与 Agent 菜单项 / tabs`）= `background:none; border:none; cursor:pointer` **只写一次**，具体规则只写颜色/尺寸/圆角；
  ② **文字蒙层** 用 `theme.css` 的 `--mix-6 / 8 / 10 / 13 / 18`（加已有的 `--hover` = 7%），别再在 `main.css` 里现写 `color-mix(... N%, transparent)`；
  ③ **圆角** 用 `--r-xs(4) / --r-sm(6) / --r-md(8) / --r-btn / --r-card / --r-shell / --r-bubble`，不写字面量；
  ④ **等宽数字** 只在 `main.css` 末尾那一组写 `font-variant-numeric: tabular-nums`，新数字元素加进选择器清单，别单写一条。
  契约测试钉着这几条（"CSS 复用：幽灵按钮底座与蒙层 token 只有一处"）—— 抄回去不会报错，只会让下一次调色改不动。
- **外观三件（2026-10-28 加，照 OpenWebUI）**：
  **① 自托管 Roboto Flex**（2026-11-06 起，MD3 官方默认字体）：`frontend/web/public/fonts/RobotoFlex-Latin.woff2`
  （84KB 可变字重 100–900 + opsz，只含拉丁/标点/常用符号）+ `RobotoFlex-OFL.txt`；`main.css` 顶部一条
  `@font-face`，`body` 字族排成 `"Roboto Flex", "Roboto", -apple-system, BlinkMacSystemFont, system-ui,
  "Segoe UI", "PingFang SC", "Noto Sans SC", sans-serif` —— **Roboto Flex 第一**是 MD3 的口径；
  中文没有拉丁字形 ⇒ 自然落到后面两个中文字族。PWA 预缓存会带上它（离线也有字）。
  旧 Inter（81KB）已删：两套都带只会让首屏多背一份用不到的字。
  **② 界面缩放**：`theme.css` 的 `--app-text-scale`（根节点 inline style，`prefs.js` 的
  `applyTextScale`/`saveTextScale`，开机第一帧就应用）。它**只乘 body 的 `font-size`**
  （`calc(15px * var(--app-text-scale, 1))`）—— 全项目尺寸几乎都是 em、0 处 rem、0 处 px 字号，
  于是改这一格 = 整页等比缩放。范围/步长钉在 `prefs.js` 的 `TEXT_SCALE_MIN/MAX/STEP`（0.85–1.3，5% 一格），
  滑杆在**用户视图 → 外观**，**拖动即生效**（尺寸得看着调）。`--r-*` 圆角与滚动条是 px ⇒ 不跟着缩（OpenWebUI 同样如此）。
  **③ 自定义字体（2026-11-04 用户定）**：`prefs.json` 的 `font_body` / `font_mono`，空 = 内置系统字族。
  `applyFonts()` 把自定义值写到根节点 `--app-font-body` / `--app-font-mono`，并**在末尾补内置栈**
  （中文字形/没装的字体仍能回落）；`body` 读前者，`code, pre, kbd, samp` 读后者。
  设置 → 外观 有两格自由输入（CSS `font-family` 写法，可逗号分隔）；`normalizeFontFamily` 拒绝
  `;{}<>`（避免破坏声明），留空/坏值回落系统。
- **输入框只有发送，且在一体面板里**（2026-11-04 用户定：暂时不做附件/模式，也不留全屏键）：`.composer-box` 包住 textarea 与右侧发送键；左栏不再放品牌；空草稿保留欢迎块（新会话可以不是空白）。
- 输入行 `/` 进命令面板（**只放已有路由的命令**），`resume` 走 picker；`model` 只是重拉顶栏下拉（不弹 picker）；不可逆的走全屏确认。
  **命令菜单默认关**（2026-10-17 用户定：移动端那颗 ☰ 与 `/` 浮层纯碍事）—— 关着时输入框左侧**没有 ☰**、
  输入 `/` **不弹面板**（但 `/name` 回车照旧执行：命令本身没消失，只是没有"菜单"这一层）；
  开回来在**用户视图 → 输入 → 命令菜单**（个人偏好 `mc_cmd_menu`，保存后**即时生效**，不用刷新）。
- **输入栏只有一种布局**（2026-11-04 用户定：「不允许再设置紧凑和宽松布局」）：删掉 `float`/`solid` 二选一、`composer_style` 偏好、`data-composer` 属性与对应设置项；统一用现在的一体面板（`.composer-box` + 右侧发送键）。
- **草稿态**（2026-10-09 定，学 DeepSeek 网页版）：点＋/`/new` 只开一张**客户端草稿**（清空消息列与右栏，顶栏显示 `config` defaults 的模型/Agent，改它只改本地 prefs）；草稿**铸一枚 UUIDv7**（`utils/ids.js`）当将来那会话的 id —— **预演走 `POST /outgoing`**（临时会话、头上就是这枚 id）、**首条发送才 `POST /sessions`**（把这枚 id 与改过的 provider/model/agent 一起带上 ⇒ 预演那一发 = 真发那一发；受理失败 ⇒ 删掉刚建的空会话、退回草稿）。⚠ **这是 HACK**（客户端铸 `sessions.id`，全项目唯一的口子 —— 动机与代价见上面 `POST /sessions` 那行；别扩散）。库里不再产生空行 ⇒ 旧"启动编排"（先清空会话再预建真会话）连同一起拿掉。改模型/Agent、回草稿都会**立刻重跑预演**（`previewSeq` 丢弃迟到的旧版）。要会话的命令（`/compact` `/cut` `/reroll` `/switch` `/stop` `/resend` `/delete` `/edit` `/editsum`）在草稿里明确拒绝。（TUI 2026-11-01 已删，那句只作历史记录。）
- **压缩树默认全收**（2026-11-03 用户定：上下文面板里压缩后的文本默认折叠）：`PayloadBar.setTree` 用 `collapsibleSet` 把有孩子的节点全放进 `treeCollapsed`；摘要行的压缩预览在折叠时不显示，点 ▸ 才展开看摘要/子节点。⚠ 树为了算体积仍读全量消息结构（不是逐摘要懒加载）；聊天里的**摘要卡**原文才是展开才拉。
- **压缩树**（上下文工作区「压缩」tab，2026-10-10 加）：把压缩结构画成文件管理器那种树（目录 = 摘要、嵌套 = 嵌套压缩、行底 bar = 子树原文占比）。**后端零改动**：现取 `messages` + `summaries` 两个 GET，`utils/tree.js` 的 `buildCompactTree` 本地建树 —— 口径**逐字复刻 `state.walk`**（左端对齐才用摘要、升到最粗同左端祖先、跳过后不回看），`发` = 装配真跳到它、`脏` = 已过期。树顶一行是 **块数输入 + 压缩按钮**（空 = 用 config 的 `compact_blocks`；与 `/compact` 共用 `compactNow`）。压缩命令受理后 `pollCompact` 盯到收尾（拿受理回执的 `at_ms` 对上——状态位是持久的，别认旧结局）。
- **分段压缩 / 补齐空档（2026-11-05）**：上下文工作区第四、第五 tab，组件 `AdvancedCompact.vue` 共用（`repair` prop 区分）。参数 `begin_idx/end_idx`、`step_blocks`、`concurrency`、`max_attempts`；先调 `/compact/advanced/preview` 看 chunk、空洞、总请求，再启动。整体只有 running/done/failed/canceled；chunk 在一个父 Task 下并发生成，全部成功才 `RecordSummaryBatch` 单事务落库；修洞模式只把同层已封段之间的空洞转成 chunk（尾部 open tail 不算洞）。
- **消息列编号与摘要卡**（2026-10-10 加）：气泡头 `你 #2` / `助手 #4` —— 真消息用服务端 `idx`；库外的（乐观用户句、生成中那一发）按**后缀顺延**（线性追加 ⇒ 顺延号就是将来的真号）；纯合成的"失败"气泡不给号。被**顶层摘要**（没爹的那层 —— 金字塔只露最外）盖住的一段折成**四面圆角卡**（不是气泡）：头 `Summary #a-b`（+`脏`）、正文 = 摘要文本（发给模型的就是这份）、`查看未压缩文本` 展开原始气泡（**只读、无操作行** —— 用户定：展开里不许编辑/修改）。口径全在 `utils/chatrows.js`（纯函数，契约测试钉住）；端点悬空/不在场 ⇒ 不折（宁可露原文）。卡头右侧三件（2026-10-10 加）：`⟳` 刷新 = 摘要重摇 —— **当前那版先占第 1 版、不丢**（seedSummary 的规矩），摇完自动切最新版；生成走 compact 的"只生成、不落库"半程、**非流式**（卡头标「刷新中…」，没有流式预览）；`✎` 编辑 = 应用内模态编辑器（`/editsum` 同一条路 —— 原生 `window.prompt` 退休）；重摇模式活着时 `◀ n/N ▶`（与气泡同一套语义，最右 ▶ 再按 = 再摇一版）。消息/摘要两族共用一把闸：一族在跑，另一族进模式 ⇒ 后端 409、前端 toast。候选只在内存（发新消息 / 切会话 / 重启就散 —— 应用的那版已在库里）。
- **消息编辑是独立模态**（2026-11-02 用户定：「打开一个新窗口而不是直接在旧的对话气泡里修改文本」）：气泡上的「改正文 / 改思考」只 `emit('edit', messageId, kind)`，由 `MessageEditor.vue` 开 `modal-box message-edit-box`（96vw × 92vh，接近全屏；大 textarea 撑满剩余高度、取消/保存/保存并重摇固定在**底部右侧**、Esc 关闭）；不再把气泡就地换成 textarea，/edit 也不再走原生 `window.prompt`。保存 PATCH 后父组件 `reloadWindow()` + `refreshStateView()`。**保存并重摇**的合法位置只有两种（`utils/chatrows.js` 的 `editRerollTarget`）：尾条 assistant 自己，或**紧挨着它前面的那条 user**（user2 后面只剩 AI2 ⇒ 编辑 user2 也能重摇 AI2）。其它位置不画，避免按了必败；先 PATCH 保存，再 `rerollNow(rerollTargetId)`。尾条 assistant 的底栏还有**「编辑最新 Prompt」**（2026-11-02 用户定）：A-B-C-D-E-F 时直接开 E 的编辑器，不用翻回去点 E（按钮是 `ICONS.notebookPen` **记事本+笔图标**，不是纯文字大按钮；aria/title 仍写明）；目标由 `latestPromptFor` 算（最新 assistant 之前最近的一条 user），只在 `isLast` 的 assistant 上画。右上角还有**网页全屏**切换（2026-11-03 用户定：不要浏览器 Fullscreen API、不占用户整块屏幕）：`expanded` class 把 `.message-edit-box` 从 96vw × 92vh 铺成 `#app` 的 100% × 100%，图标用 `ICONS.maximize` / `ICONS.minimize`，不再有「全屏」两个字。
- **聊天阅读排版**（2026-11-03 用户定：「布局不专业」的整改）：阅读列 `--read-col` 从 920 收到 **840**；
  聊天正文 `font-size: calc(16px * --app-text-scale)`、`line-height: 1.7`；消息之间 `gap: .8em`、
  assistant 上下 `padding: .45em 0`、段落 `margin-bottom: .7em`；用量脚注 `.7em`、略提对比度但仍是小字。
  ⚠ 编号 `#idx` 与操作行是**用户明确要求常显**的（2026-11-03）：五个按钮（改正文/改思考/编辑最新 Prompt/重摇/删除）任何时候都显示，`.msg-ops` 常驻 opacity **0.72**、hover 才到 1；`.hover-only` 已删。别按"OpenWebUI 都 hover"去藏。
  改回 920/15px 之前先量中文行宽（~60 字一行太长）。
- **会话列表只留标题**（2026-11-04 用户定：「除了标题，其他啥元数据也不需要」）：`#session-list` 自己留 `0.25em 0.45em` 内边距；条目**只显示标题**（消息条数、相对时间都删了；`relativeTime` 纯函数也删）；关闭/设置两枚按钮改成线性图标（`ICONS.x` / `ICONS.settings`），圆形幽灵按钮、hover 才给底。
  **有任务在跑的行，标题右侧一枚小环**（2026-11-05 用户定：「如果一个对话有正在运行的 TASK，那么这个对话在左侧对话列表里，就应该有些反应」）：判定在 `utils/tasks.js` 的 `busySessionIds`（唯一处）—— 取 `/tasks` 同一拍的快照里 `finished_at` 为空且带 `session` 的原子任务，外加队列 `current.session_id`（两步之间可能短暂没有原子任务）；**已完成的不算**（面板还会把记录列一阵子，那是账不是状态）。App 在 `refreshTasks` 里算好、经 `:busy-ids` 传给 `SessionBar`；读不到 `/tasks` 就清空（别留一条永远转的假象）。样式 `.session-busy`：边框旋转、尺寸随字号、`prefers-reduced-motion` 下停转但保留缺口环。
- **消息列只挂窗口**（2026-10-10 加，防浏览器被撑炸）：进入会话只拉**最后 `history_window` 段**（默认 **5**；段 = 一条消息或**一张顶层摘要卡**，所以原始消息通常 ≤5）；更老的按 `↑ 加载更多历史` 一页一页往回（一页也这么多；值来自 `chat.history_window`，设置 → 聊天可改；插入前记高度、插完补回滚动锚）。**被摘要盖住的原文一个字节都不取**（按"消息段"拼连续区间，走 `POST /messages/batch` 一发拿全；摘要段整段跳过）；`查看未压缩文本` **按下才拉**（每次按下都拉新的一版，不吃旧缓存）。总数拿 `?last=1` 问尾条（比会话列表计数新鲜）；`reloadWindow` = 按最新段布局重拉当前窗口（一轮结束/编辑/删除/压缩/切版后），起点先 `alignWindowFrom` 对回段边界。**压缩树 tab 是例外**：看着它才拉**全量**消息+摘要建树（分析视图 = 用户显式要全量）。
- **PWA（可安装，2026-10-11 用户点名）**：`vite-plugin-pwa`（build/preview 开 SW、dev 关）——壳预缓存（离线开门板）、`/api` 永不缓存。**安装前提 = HTTPS 或 localhost**：局域网 `http://<IP>` 装不了（非安全上下文）；云端反代终结 TLS 即满足。图标在 `public/`（favicon 栅格化）。实测：localhost 下 manifest/SW/图标全过，`beforeinstallprompt` 真触发。**应用内安装入口 2026-11-02 删掉**（用户定；左栏底改成 会话/渠道/设置）——浏览器自带的安装菜单/地址栏图标仍可用，PWA 能力本身没动。
- **新版本由用户点（2026-10-20 用户定，从 `autoUpdate` 改成 `prompt`）**：`registerType: "prompt"`
  + 一条**常驻更新条**（`components/UpdateBar.vue`，注册在 `utils/pwa.js` 调 `virtual:pwa-register`）。
  **为什么改**（真踩过）：`autoUpdate` 在新 SW `activated` 时**自己** `location.reload()` ——
  它**静默**（你没看那标签页时刷新悄悄发生），且**从不告诉你"此刻看的是旧的"**。2026-10-20 因此白排查一轮：
  服务器上三份 bundle（宿主 dist / 容器 `/build` / 8788 服务）**逐字节相同**且都含新代码，界面上却看不到新加的选项，
  最后靠「清站点数据」才揭穿是 **SW 缓存的旧壳**。现在检测到新版本 ⇒ 弹更新条（**不自动消失** ——
  与 Toast 的分工：Toast 是"告知"、3 秒即走；这条是"要你动手点一下"，消失了等于没通知）⇒
  用户点「刷新」才 `updateServiceWorker()`（skipWaiting → controlling → reload）。
  ⚠ **验证这类事有个方法论陷阱**：`curl` **没有 SW**，它永远拿到服务器上那份新文件 ——
  于是"curl 验证通过"与"用户看到的还是旧的"**可以同时成立**。要验缓存层必须用真浏览器（或清站点数据后看）。
  ⚠ **别再手写 `injectRegister`**：插件检测到 `virtual:pwa-register` 的 import 后会自己把它转成 `false`
  （不再往 index.html 塞那段没有回调的 `registerSW.js`）——手写会跟它的自动检测打架。
- **生产交付性能（2026-11-02，用户报"部署后前端响应慢、没有缓冲"）**：
  ① **字体子集**：`public/fonts/RobotoFlex-Latin.woff2` **84KB**（可变 wght 100–900 + opsz；
  只留拉丁、标点、常用箭头/符号；中文本来就落系统字族）。`contract.test.js` 钉着 <150KB + wOF2 魔数。
  ② **缓存头**：`vite.config.js` 的 `assetCacheHeaders()` —— `/assets/*-<hash>.js/css` 与 `workbox-*.js`
  给 `public, max-age=31536000, immutable`；字体 1 周；index / manifest / sw / 图标仍 `no-cache`。
  从前全是 `no-cache`，内容寻址的包每次刷新都要多一个 304 往返。
  ③ **开机少发请求**：boot 拉过的 `/sessions` 直接传给 `enterSession(id, sessions)`；Agent 列表同一拍
  共享 `fetchAgentsOnce()`（从前两发 42KB）；`fillAgentSelect` 不再带上下文（context 从两次变一次）。
  ④ **工作区按需**：`enterSession` 不再无条件 `POST /outgoing` + `GET /state`；`refreshContextLazy()`
  按**当前 tab**（载荷/状态/压缩）只拉那一页，切进工作区 / 换会话时才发。
  ⑤ **加载骨架**：`loadingSession` + `.chat-skeleton`（三条 opacity 呼吸的灰条）——
  切会话时消息列不再是一段纯黑空白。
  ⚠ `vite preview` 只做 gzip、HTTP/1.1，**不是生产级静态服务器**；公网/反代部署应由 Caddy / nginx /
  Cloudflare 终结 TLS 并开 brotli + HTTP/2（缓存头已经由上面的中间件给出，不会被吃掉）。
- **MD3 风格层（2026-11-05 用户定：「目标是完美的 MD3 风格 + 动画 + 加载效果」，先改左栏）**：
  `theme.css` 底部新增 `--md-*` token 层（与 `--tg-*` **并存**）：MD3 颜色角色（primary / secondary-container /
  surface 容器层级 / outline / error）、shape scale、elevation 1–3、motion（standard / emphasized + 时长）、
  state layer（hover 8% / focus 10% / pressed 10%）、字体角色（**一律 em**：跟随 `--app-text-scale`，不写 px）。
  暗色/亮色取 MD3 baseline 色板，Breeze 用它的桌面蓝生成同构角色 —— 三套主题都是同一套语义。
  **落点先左栏 + 顶栏 + 输入区**（2026-11-05 第二刀）：左栏规则以 `#sessions-bar` 前缀收口
  （`.bar-head` / `.foot-btn` / `.empty` 是共享类，直接改会波及上下文工作区）。左栏结构 = MD3 navigation drawer：
  顶部 segmented button（对话/生图）+ 标准 icon button；新建 = **extended FAB**（primary-container、elevation 1→2、按下 scale）；
  列表 = 48dp 行、full 圆角、选中 secondary-container、`::after` 状态层（内容 z-index 压在它上面）；
  底部 = navigation drawer item；选项浮板 = MD3 menu（surface-container / 4dp 圆角 / elevation 2 / 48dp 行）。
  **忙碌环**改成 MD3 circular progress indicator：SVG 双圆（track 18% + arc），旋转 + dash 伸缩（`session-busy-dash`），
  reduced-motion 停转但保留缺口弧。**加载效果**：`sessionsLoading` → 6 条 shimmer 骨架（`aria-busy` 同步），
  `sessionsRefreshing` → header 下 3dp indeterminate linear progress。行进场 = `session-item-in`（200ms emphasized），
  reduced-motion 不跑。
  **顶栏**（2026-11-05 第二刀）：`#chat-col > header` = MD3 top app bar（surface-container-low、56dp、
  保留用户要的向下阴影、不描线）；模型按钮 = 40dp full 圆角 text button；Agent 头像 = 40dp avatar（状态层 / 按下 scale）；
  模型/Agent/上下文浮板 = MD3 menu（surface-container、4dp 圆角、elevation 2、48dp 行）+ `md-menu-in` 进场，
  顶栏整体接 `@keydown.esc`（一次收起所有浮板）；工作区 聊天/上下文 = MD3 primary tabs（3dp 圆角 indicator，宽度过渡）；
  上下文环 = 40dp 触控盒、track 用 outline-variant、弧仍是 `--tg-ok`（绿=正常 / 红=超预算）。
  **输入区**（2026-11-05 第二刀）：`.composer-box` = MD3 搜索栏（surface-container-high、28dp 圆角、focus-within 2px primary 环
  + elevation 2）；发送键 48dp primary 圆、按下 scale .96；停止态用 error-container；菜单键 40dp 透明 icon button；
  占位符 on-surface-variant。`.messages-inner` 顶部留白跟着顶栏从 3.2em 提到 **4.2em**（顶栏 3.5em + 间隙）。
  ⚠ 共享类暂时还是旧样式：`.bar-head.bar-tabs`、`.foot-btn` 基座、`.empty` 等**不要**在没加 `#sessions-bar` 前缀时改
  （否则会顺手改掉上下文工作区，且契约测试只钉了一部分）。
  ⚠ **全局 input 基线的特异性 (0,4)**（`input:not(...):not(...)`）会压掉 `.model-search` 这类单类选择器 ——
  搜索栏的 full 圆角 / 无下划线 / focus 环必须写成 `#chat-col > header .model-search`（踩过：蓝下划线又回来了）。
  **消息区**（2026-11-05 第三刀）：用户气泡 = `secondary-container` / `on-secondary-container`、
  16dp 圆角 + 4dp 尾巴；AI 仍是纯文本（不包气泡，保留既有形态）。消息头 = label-medium / on-surface-variant；
  操作行 = 32dp MD3 icon button（状态层 / danger 用 error）；思考折叠 = surface-container-low 圆角容器，
  summary 带 ▸/▾ 与 focus 环；摘要卡 = MD3 card（surface-container-low、12dp、elevation 1、dirty 用 error-container）；
  代码块 = surface-container-low + outline-variant 描边；等首字节三点与流式光标都用 primary；
  「加载更多」= MD3 outlined 按钮（outline-variant 描边、full 圆角、状态层）。
  ⚠ 用户气泡里的 `who` / `foot` 仍有旧规则的 `color: inherit; opacity` 覆盖（气泡内语义色继承是对的，别删）。
  **设置页 + 浮层 + 全局控件**（2026-11-05 第四刀）：
  全局 `button` = MD3 tonal 按钮（secondary-container、full 圆角、40dp），`.primary` = filled primary，
  `.danger` = text error；幽灵按钮族追加 `min-height: 0`（不吃 40dp 下限，否则 2em 的图标键被撑成椭圆）。
  全局输入框/select/textarea = MD3 outlined 字段（outline 1px、4dp 圆角、focus 1px primary 内环）；
  range = MD3 轨道（4px outline-variant）+ primary 圆钮；ToggleSwitch = MD3 switch（52×32 轨道、
  outline 描边、开态 primary + 24dp 滑块）。
  设置页：`#settings-view` = surface；`TabBar` = MD3 segmented（surface-container-low 底、
  secondary-container 滑动底片、emphasized 300ms）；h3 竖条改 primary；字段行分隔线 outline-variant；
  `plist` = surface-container-low 卡片、表头 label-small、行 hover 状态层；`.io-card` = outlined 卡片。
  浮层：`.modal` / `.confirm` / `.palette` / `.picker` = MD3 dialog/menu（surface-container-high、
  28dp / 12dp、elevation 3、scrim 45%、`md-dialog-in` 进场）；toast = snackbar（inverse-surface、
  4dp、elevation 3、语义点替代左边框）；更新条 = **顶部独立占位的大横幅**
  （`#app` 里 columns 之前的 flex 子项、primary-container 底、**占满整个顶部左右宽度**、
  大号刷新键；2026-11-06 用户定：「在顶部单独占据一个位置，文本+按钮，要显眼，方便快速切换」
  「占据整个顶部的左右宽度」——出现时把应用整体往下推，不压顶栏/tabs）。
  **上下文工作区**（2026-11-06 第五刀）：五 tab 改 MD3 primary tabs（3dp 圆角 indicator、无整条底线）；
  载荷行头 40dp + 状态层、role/idx/chars 用 label-small、正文 body-medium；状态表名 primary、
  行分隔 outline-variant；压缩树行 = 8dp 圆角 + hover 状态层 + primary 容量条、
  撤销键 32dp 圆形；高级压缩进度条 = 4px primary、空洞提示 = error-container、
  chunk 行 40dp、运行中圆点 primary；草稿提示 = surface-container-low 卡片；
  原始载荷 = MD3 代码容器（surface-container-low + outline-variant）；
  `#context-workspace` 顶部留白 **4.2em**（与 `.messages-inner` 同数，让开 56dp 顶栏）。
  ⚠ 载荷页的**平铺**口径仍然有效：`.pv-msg` 依旧无边框/无底色/无圆角、块间一条细线，
  role 与 chip 仍是裸文字（契约测试钉着；MD3 只换排版与颜色，不加回卡片）。
  ⚠ 输入框的全局基线特异性很高（`input:not(...):not(...)`），组件要改样式仍得加前缀/ID（模型搜索那条踩过）。
  快修（2026-11-06）：草稿欢迎页（title-large / body-medium / label kbd chip）、
  全局空态（on-surface-variant）、细滚动条（outline-variant）、
  思考/摘要原文/载荷块展开的 `md-fade-in`（reduced-motion 关闭）。
  **按下波纹**（2026-11-06 用户定「把按钮波纹做好」）：`utils/ripple.js` 在 `main.js` 装一次
  事件委托（`pointerdown` + 键盘 Enter/Space），给 `button / .session-item / .model-item /
  .agent-item / .palette-item / .picker-item` 在触点位置放一枚 `currentColor` 半透明圆，
  动画 `md-ripple` 结束自删；宿主临时加 `.md-ripple-host`（relative + overflow hidden）。
  ⚠ 波纹圆挂在宿主内的 **`.md-ripple-layer`**（absolute inset 0 + `border-radius: inherit` +
  overflow hidden）上，不是直接挂宿主：某些按钮/列表行的绝对定位子元素会逃出宿主的圆角裁剪
  （用户报「波纹扩散到按钮外部」），独立裁剪层是确定性的兜底。
  ⚠ **tab 类要再收一档**：设置 tab 的按钮框比视觉 thumb 高 0.2em×2、顶栏/上下文 tab 只有
  文字 + 下划线却带着 0.4em 内边距 —— 波纹铺满按钮框会从视觉胶囊/文字外溢。三处的裁剪层
  额外 `inset`（`.settings-nav .tab` / `.ws-switch .ws-tab` / `.bar-head.bar-tabs .tab`）收进
  "看得见的那块"。
  `prefers-reduced-motion` 下**整个不做**。⚠ 圆心 left/top 是**相对宿主**的，
  `rippleSpec` 必须减 `rect.left/top`（漏过：圆画到视口坐标、被裁掉 ⇒ 像没生效）。
  **密度**（2026-11-06 用户：「MD3 padding 太浪费」）：`main.css` 末尾一块密度覆盖 ——
  会话行 48→44、顶栏 56→48、FAB 56→48、输入框 ~69→55、菜单 48→40、设置行 0.7→0.45em、
  消息间距 0.8→0.55em；只改尺寸不改语义。要整体调松/调紧只改这一块。
  ⚠ 字体：2026-11-06 已按 MD3 换成自托管 **Roboto Flex**（旧 Inter 删除）；`--app-font-body` 的自定义
  字体仍排第一，没配时落 Roboto Flex → 系统 → 中文字族。
- **高延迟（1s+ RTT）下的界面口径**（2026-11-05 用户定：「优化服务器和用户之间存在 1000ms 以上延迟的极端场景」）：
  目标不是"让网络变快"，而是**别让等待凭空多出一个来回**，也别让旧数据挂在新语境里。
  ① **发送先上屏**：`send` / `sendFromDraft` 先 `addMessage("你", …)` 再等 202；回来用服务端 `user` 换掉
  （真 id/idx，编号与编辑/删除都认它），失败就撤掉并把文字 `setText` 还回输入框 —— 绝不留下假装发过的气泡。
  ② **切会话不再先拉列表**：`sessionsCache`（`renderSessions` 写、`refreshSessionsBar` 记时间）直接用；
  只有缓存里没有才 `GET /sessions`，之后 10 秒保鲜期外才在后台补一次（不挡这一屏）。
  ③ **窗口取数压到 1 个 RTT**：第一拍并行取 `listSummaries` + `?last=windowLimit` 尾部消息（末条 idx 就是总数），
  再算段；`seededMissingRuns` 只补"种子没盖住的老段"—— 没有摘要横跨窗口时第二拍是空的。
  ⚠ `GET /messages?last=N` 会带出被摘要盖住的消息，这是**故意**的：`buildChatRows` 只画段里的，
  别再加第二份"哪些该显示"的过滤。
  ④ **模型清单缓存先画后对**：`modelsCache` + `modelsInflight`，切会话不再先闪一下「加载模型…」；
  `models-changed` 那条路照旧会触发刷新，不靠 TTL 猜。
  ⑤ **编辑消息先本地生效**：`PATCH` 回执带回新消息，`onMessageEdited` 先换那一行正文，再
  `reloadWindowKeepingScroll()` 对账（摘要 dirty / 区间可能变）；滚动位置保住。
  ⑥ **回看缓存**（2026-11-05 用户定：「切换不同对话时，最好也预留缓存吧」）：`utils/windowcache.js` 的
  `rememberWindow` / `recallWindow` / `forgetWindow` 管一个 `Map<sessionId, {rows, segments, windowFrom}>`
  （LRU、上限 8）。`loadWindow` 成功后写、`loadMore` 扩窗后也写；`enterSession` 命中就**先画出来、
  不挂骨架**，等 `status` 说空闲再 `reloadWindowKeepingScroll({ onlyIfUnchanged: true })` 后台对账。
  存/取都拷贝（切走后的乐观消息不会改脏缓存）；对账落屏前比 `localRev` / `pendingSends`：期间发送/编辑/删除/压缩过，或有发送还没等到 202
  ⇒ 这一发作废，绝不拿旧快照覆盖刚画上去的字。回合在跑时（status= pending/streaming）不做对账，
  交给轮询收尾自己重拉。删会话时 `forgetWindow`。
  ⑦ **上下文环在切会话时先清空**（`setContext(null)`）：1s+ 下旧会话的占比会挂满一个 RTT，
  看起来像新会话的账 —— 那是"旧数据住在新语境里"。
  ⑧ **意图预取**（2026-11-05）：`SessionBar` 的行上 —— 桌面 `pointerenter` **停 140ms** 才算想点它
  （扫过列表不发）、键盘 `focusin` / 触屏 `touchstart` 立即 `emit("prefetch", id)`。
  App 的 `prefetchSession` 只把窗口取进 `windowCache`（**不动界面**、不抢当前会话轮询），
  并发上限 **3**、同一条 30 秒内不重复取。`fetchWindow` 的尾部那一屏做**单飞**（`tailInflight`）：
  悬停后马上点下去，两边拿同一个 Promise，不会发两遍。
  ⑨ **开屏并行**（2026-11-05）：boot 的 `/health` 探测与首批 `getChat` + `listSessions` **同时发**，
  warm 用 `apiBaseCandidates()[0]`（不是 `apiBase()` —— 单容器里它默认的 :8787 对外不可达）；
  只有 `winner === firstBase` 才吃 warm，否则丢掉现拉。
  ⑩ **弱网兜底**（2026-11-05）：`call` 对 GET 加 **20s 超时**（`AbortSignal.timeout`）+ 网络层失败
  **重试一次**（超时不重试，那等于把等待翻倍）；POST 等可能打上游长活的方法不套这个上限
  （刷新模型/导入本来就慢，20 秒会把正常请求剪断）。`probeHealth` 自带 **8s** 超时 ——
  黑洞地址不能把启动按住。
  ⚠ 契约测试钉着这些顺序（`高延迟：发送先上屏…` 那条），改回"先 await 再画"会红。
- **设置（⚙）谁都能进**（2026-11-01 起：账号砍了 ⇒ 没有"root 专属"）。左栏底那枚 `👤 用户` **删了** ——
  它原来是"登录者自己的事"，而登录者这个概念没了；里面两块：**身份 / 用户管理**跟着账号一起删，
  **外观 / 输入**整块搬进设置的「外观」区（`AppearancePane.vue`，四区里的最后一个）。
  **左栏底 = 会话 / 渠道 / 设置**（2026-11-02 用户定：设置面板太满，把会话与渠道拆成独立入口；
  原来的「安装说明」删掉）。会话 / 渠道与设置共用同一个视图：`SettingsModal` 的 `:only`
  只显示那一页、隐藏 TabBar，页头写「会话管理 / 渠道管理」；退出仍走同一个 close。
- 设置四 tab：**聊天 / 智能体 / 提示词 / 外观**（2026-11-02 起；会话与渠道不再是设置 tab）。聊天 / 智能体 / 提示词/外观的编辑都是 **tab 内二级页**（列表 ⇄ 详情就地切换，`[←]` 返回 —— 2026-10-10 从弹窗改过来，弹窗叠视图的账清了）；**Agent 编辑器的二级页自己也分页签**（2026-10-28 用户：「全挤在一起很丑」）——「基本」（id / 名称 / 首段对话 / 句首贴状态）/「系统提示词」（base + 它的段）/ **每个能力一页**（启用 / Model / 提示词覆盖 / 它的段，页面由 `ABILITY_IDS` 生成 ⇒ 加能力自动多一页），页签条与保存行：**保存那一行在页签外面**（哪一页都能存；藏在某一页里的那种，翻到别的页就找不到保存了）。页签条本身是 `frontend/web/src/components/TabBar.vue`，设置主栏那四区用的**同一个组件** —— 底片滑动的算法（量 `offsetLeft` / `offsetWidth`，还要跟 `window.resize` 与界面缩放滑杆）只有一份，契约测试钉着两处都不许自己画 `tab-thumb`、不许自己量宽度；「会话设置」（左栏 ☰）是**紧凑对话框**（560px、高随内容）。**Esc 一次只关一层**：二级页开着时设置视图不响应 Esc、会话设置弹窗开着时同理（踩过：两边各监听 document，一下关两层）。**会话管理页**（左栏「会话」入口）= 新会话默认值 + 列表（点进）+ 删除 + 尾部**导入对话**区（`POST /sessions/import-st` 与 `/sessions/import-json` 各一张卡：一句说明 + 按钮 + 隐藏文件框 —— 2026-10-17 用户点名「两种导入混在一起、都乱了」，改成两卡分开、原生 file 框换按钮）。
- **渠道详情里的模型清单**（2026-11-02 用户定：「具体某个模型的界面里，应该能看到当前这个渠道导入的模型的列表（虽然不能编辑）」）：
  `ProviderEditor` 底部「模型（N）」一张**只读**表 —— 模型名 + 上游 id（小字并排）+ 上下文；数据直接取 `GET /providers` 的 `models`，
  保存并刷新后用回执里的 `models` 更新（`models` 是唯一状态，数量由它派生）。条目多，表在 `.provider-models` 里内部滚动。
  不摆编辑入口（显示名/覆盖是模型自己的事）；空态说清「保存并刷新后会出现在这里」。
- **设置页平铺列表**（2026-11-04 统一设计系统）：`.settings-card` 只剩分节容器（无底色/圆角/内边距），字段行之间一条 `--line`；不再“卡片套卡片”。**tab 分两族**（2026-11-04 用户复核：「原来的 TabBar 没问题」）：设置 / Agent 二级页保留胶囊 + 滑动底片（`TabBar`），上下文这些 tab 与顶栏聊天·上下文是下划线；不要为了“统一”把用户认可的底片再删掉。**设置四 tab 与会话/渠道管理页统一 760 居中**（列表页有表格，但切页时宽度不该跳来跳去）。**所有 `h3` 统一一种标题语言**：正文色半粗 + 左侧蓝色短竖条（从前只有卡片里的 h3 有竖条，其它 tab 是裸文字 —— 用户指出不一致）。「输入」卡里是 **发送方式（`enter` / `shift-enter` / `button`）**、输入时预览、命令菜单 —— 这三格在 2026-11-01 从用户视图搬进 `AppearancePane` 时 **refs 与 persist 都搬了、模板整段漏了**（界面上找不到，2026-11-02 补回）。开关一律 `:model-value` + `setX(v) { ref = v; persist(); }`，**别写 `v-model + @update:model-value="persist"`** —— 两个监听同挂 update 时显式那个可能先跑，存进去的是旧值（实测：界面 on、服务端 off）。
  **文案一律说人话**（2026-11-02 用户「审计设计界面里有哪些不说人话的部分」）：可见标签不再用 `Agent` / `vendor` / `protocol` / `载荷` / `卡带` / `提示词段` 这类内部词 ——
  `Agent → 智能体`、`发送键 → 发送方式`、`预演载荷 → 输入时预览`、`提示词段 → 提示词片段`、`卡带 → 智能体文件`、`首段对话 → 开场白`、`句首贴状态 → 在消息开头附上当前状态`、`走哪家/说什么话 → 服务商/接口类型`。
  内部 id（渠道标识、片段名称）仍可在编辑器里看到（手改配置要对得上号），但只放在输入框/表格里，不写进说明文；`token` 只在“上下文长度”一处保留并加括号解释。
- **渠道管理页（2026-11-02 用户「这都是啥啊」）**：列表从 7 列收成 **渠道 / 服务商 / 模型 / 密钥** ——
  「走哪家 / 说什么话 / 筛选器」三个黑话列删掉：协议只在编辑器里改（有中文名 + 一句人话），
  筛选器并进「模型」那格（非空时一个「筛选」小标 + title 写明规则）。
  编辑器按钮从「保存 / 刷新模型 / 刷路由」收成 **「保存并刷新」+「测试连通性」**：
  保存后总是重拉一次模型；测试走 `POST /providers/{id}/test`，只拉 `/models`、不落库、可带未保存的表单值
  （API Key 留空 = 用已存的），返回 200 + `ok:false` 表示连不上（不是接口挂了）。「清密钥」保留为小号文字按钮。
- **开机回到上次的会话**（2026-10-11 用户定）：刷新 / 被安卓丢弃后重载 ⇒ **进回"上次在看的那条会话"**（`mc_last_session`，住 localStorage —— 纯本地偏好，与 cookie / 服务端无关），不再是一张空草稿；取不到（删了 / 换人登录 / 别的设备）⇒ 回落草稿（不猜、不串人）。人在草稿 ⇒ 该键清空，开机也回草稿。
- **背景图**（用户视图，2026-10-10 加；原先在设置→界面，随"用户"视图一起搬家）：**花纹整套砍掉**（用户定）—— doodle.svg / `--tg-pattern` 删净，`body::before` 改挂**自定义背景图**：URL（浏览器直接加载，跨域无妨）或**上传**（canvas 压到长边 1920 / JPEG 0.85，配额满降档 1280/0.75 重试一次；小图原样存保 PNG；住 localStorage `mc_bg`，**不上后端** —— 纯本地偏好）。没设 = `none` 纯平底。口径与工具在 `utils/background.js`（App 开机 `applyBackground()`）。设置里**即时生效**（不吃 saveUi 的保存键：上传是异步的，攒着容易对不上账）。
- 交互照 Pi：CJK 宽度对齐 / markdown 渲染（marked + KaTeX）。**渲染口径**（`utils/markdown.js`，2026-10-11）：
  **不能换行的内容用满整列**（2026-10-15：`装 .katex-display / pre 的消息` `:has` 抬到 100% —— 此前被气泡的 70%/80% 上限按住，公式在列里放得下却出横滚 ✗；真比整列宽的仍横滚，那是兜底）；
  `**粗**` / `*斜*`（`_斜_` 同）/ **`__下划线__` ⇒ `<u>`**（marked 默认当 bold —— RP 模型惯用 `__` 表下划线，
  行内扩展改掉它，**代码段里不转换**：`` `__init__` `` 原样）/ 行内码 / `~~删~~` / 引述块 `> OOC`。
  ⚠ 转义先行**只转 `&` 与 `<`**：`>` 必须留着——行首的它就是引述块记号（从前连 `>` 一起转，引述块从来没渲染过）。
  ⚠ **抽数学定界符前先保护代码**（2026-11-03 用户报的"红块"）：模型会在正文写「用 `$$` 包裹」——
  反引号里的 `$$` 不是公式开头；不先遮住它，后面所有 `$$` 配对整体错位，最后一大段正文被当成一条畸形公式，
  KaTeX 用 `.katex-error` 把整段涂红。行内 `$…$` 的**闭合 `$` 后面允许紧跟中文标点**（`$u(x)\to 0$，` / `$A-B$）`），
  旧规则要求后面是空白，于是中文句子里一路吞到下一个 `$`。两条都有契约测试。

## 踩过的坑（都是实测出来的，改代码前扫一眼）

- **删掉一个声明，就别只用"我记得"去清引用** ✗（2026-10-29 白屏事故）：删右栏时删了 `mqlRight` 的声明，
  却漏了底部 `onMounted`/`onUnmounted` 里两处 `mqlRight?.addEventListener(...)` ——
  **构建通过、94 条契约全绿**，浏览器一打开 `ReferenceError`、整页白。
  JS 里"用了没声明的名字"是**运行时**才报的：静态检查不管、字面匹配也搜不到（要搜的正是"它还在"）。
  两条防线（互补，别删任何一个）：
  ① `test/ssr-smoke.test.js` —— 把 App 真的 SSR 渲染一遍，抓**模块顶层与 `setup()`** 里抛的错；
  ② `contract.test.js` 里一张**"这次删掉的名字"清单** —— 逐名查 App 是否还在引用。
  ⚠ ①**盖不到** `onMounted`/`onUnmounted`/`watch` 回调（SSR 不执行挂载钩子），而那次错的正是这一档；
  ⚠ ②靠人记得往清单里加名字 —— 删声明时顺手加，漏了就等于没查。
  流程上还有一条最便宜的：删完声明**立刻 `grep` 那个名字**（我这次 grep 了 `rightHidden`/`narrowRight`/
  `payload-bar`，唯独没 grep `mqlRight` 本身）。

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
- **`*bool` 的默认值不能共用同一个局部变量**（2026-11-02 实测，设置页"开关点不动"的根因）：`DefaultPrefs` 从前写 `on := true; off := false`，再让 `Preview` / `ChromeHide` 都 `&on`、`CommandMenu` / `PayloadViz` / `BarLeftHidden` 都 `&off`。JSON 解码 `command_menu:true` 会把那个共享的 `off` 改成 true，下一行 `payload_viz:false` 又把它改回来 —— **最终值取决于字段在 JSON 里的顺序**，表现是界面拨了、服务端没变、刷新又回去。每格各写 `new(true)` / `new(false)`（Go 1.26），并有用混合值的 round-trip 测试（`config/prefs_test.go`）。
- **` overscroll-behavior: contain` 挂在"滚不动的自己"身上 = 断链炸弹** ✗（2026-10-17 实测，用户报"状态面板触屏和滚轮都滚不动"）：右栏原来是**栏自己滚**，而三个面板又各自 `overflow-y: auto` + `contain`，面板没有高度约束 ⇒ 它永远滚不动；`contain` 又只认"滚动者"⇒ 手势/滚轮滚到它就**不再往父级链** ⇒ 什么都不滚（载荷 tab 侥幸没事：它的 `<pre>` 没写 `contain`，链得到父级 ✓ —— 所以症状是"只有状态/压缩两栏死"）。
  **修法照左栏来**：栏改 `flex column; overflow: hidden`（自己不再滚），tabs `flex: none`（常驻），三个面板 `flex: 1 1 auto; min-height: 0; overflow-y: auto; overscroll-behavior: contain` —— **`contain` 只该写在"真的是那个滚动者"的元素上** ✓。
  （排查法：`elementFromPoint` 看链、看每个挂 `contain` 的元素 `scrollHeight > clientHeight` 是不是成立 ✓；⚠ 用合成触摸测时先确认**面板没被手势滑关**、触摸点落在视口内 —— 这两条能把"量不出"误判成"修不好"，都踩过。）
- **移动端"页面还能延展/往上滑不完"**（2026-10-10 真机症状）：`translateY(vv.offsetTop)` 与**文档可滚**是一对正反馈 —— 页面一滚，offsetTop 变大 → 再平移 → transform 又撑大可滚动区域 → 越滚越"延展"。解法：**文档锁死**（`html, body { height: 100%; overflow: hidden; overscroll-behavior: none }`），滚动只发生在内部容器（#messages-vue / #payload-bar / .settings-body / .modal-box / .palette / #session-list …），每个内部滚动容器再各挂 `overscroll-behavior: contain` 断掉滚动链与回弹。
- **连通性探针少一道闸就会"假连通"**（实测）：裸地址（没协议）会被 `fetch` 当**相对路径** ⇒ 撞上 vite 的 SPA 回退拿 200 HTML ⇒ 死地址也能"检查通过"；"随便一个 web 服务"回 200 同理。两道闸缺一不可：地址先 `normalizeApiBase`、回体必须是 JSON 且 `status == "ok"`。
- **flex 列里的卡片不许给 `overflow: hidden` 就完事**：`overflow: hidden`（圆角要它）会让 flex 项的**自动最小高度失效** ⇒ 不加 `flex-shrink: 0`，卡片会被压得比内容矮（展开的摘要原文被裁掉一截、还会把同列的别的卡一起压扁 —— 2026-10-10 在摘要卡上实测）。
- **批量改代码时逐文件落盘**：把 `write` 放在脚本末尾，中途任何断言失败都会让整批改动一起丢。
- **vite dev server 的 transform 缓存会发旧货**：文件已改、浏览器却报旧代码的错（实测：AgentEditor 磁盘上是 `sharedApi()`，5173 端口吐的还是 `createApi` 的混合体；`?t=` 时间戳照给但内容是旧的）。症状 = "代码明明改了怎么还报旧的 ReferenceError"。**处理：`find src -exec touch {} +` 敲一遍 mtime**（或重启 vite），再 curl 一下 `http://127.0.0.1:5173/src/…` 核对吐出来的字节 —— 别怀疑自己刚写的代码。
- **Go 的 `encoding/json` 默认把 `<` `>` `&` 转义成 `\u003c`**（本项目满地 LaTeX）⇒ 关掉 `SetEscapeHTML`；它的 `Encoder.Encode` 还会补一个 `\n` ⇒ 写字节断言时记得它。
- **纯结构之间手搓转换 = 字段静默丢失**：`config.Provider` → `providers.Provider` 字段名一样、类型不同，
  各处手搓字面量时漏一个字段就是静默失效（真发生过：`identity` 声明了却从没被转过去 ⇒ 配置里写 `"identity":"pi"` 被无声忽略）。
  **规矩**：这种转换只许有**一个**函数（`providers.FromConfig`），并配一条"每个字段都要活到请求上"的回归测试。
- **`r.PathValue("x")` 名字不匹配是静默空串** ✗：改了路由里的通配名（`{id}` → `{session_id}`）而忘了改 handler ✓，
  会**编译过、测试过、还回 200** ✓ 只是拿着空 id 去查（→ 404 或**串到别的会话** ✗✗）。改完**必须逐条真发请求验** ✓。
- **Go 的包名会被参数名遮蔽**（`func f(model string)` 里的 `model` 盖住 `model` 包）；**`sync.Mutex` 不可重入**（持锁的方法里别再调公开方法）。
- **正则改源码要小心字面量 `\n`**：源码里 `\n` 是两个字符，`\b`/lookbehind 都会失灵，贪吃匹配会把整行切坏。

## 本机环境

- 搜索：`tavily` MCP（`mcp__tavily_*`，配置在 `~/.omp/agent/mcp.json`）——直连的几家搜索引擎常被反爬挡掉，优先用它。
- 真机：SM-X810（Galaxy Tab S9+，2560×1600 横屏），**无线 adb**；可 `adb shell input tap X Y`、`screencap` 截图。

## 本项目的协作习惯

- **完成任务后自动提交**（2026-11-03 用户定：「每当我和你完成一个任务，你就提交」）：做完一段有意义的进度 → 跑测试 → 实跑验证 → **直接 `git commit`**（中文「为什么」体，正文写验证）→ 报告 commit hash。提交只带本次范围；**推送仍由用户指挥**（没说 push 就不推）。
- 提交时只带用户点名的范围（`git commit <文件…>` 只记这些文件；失败的 commit 会把 staged 留在索引里 ⇒ 下次先 `git diff --cached --name-status` 核对）；别把 `config/`、`data/`、大 APK 带进去。
- 注释、提交信息用**中文**；提交信息写清"**为什么**"，别只写"改了什么"。
- **造了测试数据就按显式 id 清** ✗：别按标题/内容匹配删（会话的自动标题是异步生成的 —— 匹配可能打到用户同名会话头上；实测踩过：按标题 find 首条 = 赌自己的那条标题已生成，别赌）。
- **无头验证的两个测量陷阱**（2026-10-10 实测，一下午的假线索都从这儿来）：① `page.on("console")` **一条都收不到**页面 console —— 别拿"探针没打日志"当证据；② `tab.run` 里的原生 `page.evaluate` 跑在**隔离世界**、`tab.evaluate` 直连才是**主世界** —— expando 探针（`window.__x`）跨世界不可见，读页面里写的探针**必须用 `tab.evaluate`**（写入口用哪个世界，读的时候也得对得上）。
- **模板内联处理器里别用 `Promise`/`window` 这类全局** ✗（2026-10-10 实测，找了很久）：编译器把它们编成 `_ctx.Promise` / `_ctx.window`（组件实例代理上**不存在**）⇒ 点击那一刻当场 TypeError、被 Vue 吞掉，动作**永远不执行且毫无声响**（`@pick="(a,it)=>Promise.resolve(a(it))…"` 让 picker 的**鼠标点击**从写下那天起就是死的 —— 键盘路径绕开它，所以测试一直全绿）。内联处理器只用 setup 能解析的绑定；复杂/异步逻辑挪回 `<script setup>`（如 `onPickAction`）。
- 改完跑 `go -C core test ./...`；**UI 改动必须实跑**（真机或灌事件的无头验证），不要只凭代码断言。
- **改完东西就 `podman compose restart`**（2026-10-17 用户定；2026-11-02 用户再确认：**每次会话完毕**都跑一次，默认动作，别等他问）：容器每次启动都现编译前后端（缺省 ~10–12s 就绪），他刷新 8788 就能看到；只改宿主端的东西（文档、纯纯的脚本）不用重启。容器停着时 `restart` 也会把它拉起来（实测）。
- **后端代码一变就重启后端**：先 `go -C core build -o ../microchat .`，再重启它（`tmux kill-session -t microchat` 后重起）——不然你验的是旧二进制。自检：`ls -l /proc/<pid>/exe` 带 ` (deleted)` = 跑的是旧货。
- **起服务用 tmux（后台 + 日志落文件）**，别用"受监督进程"那类会一直挂着输出的方式（那样一次工具调用会被进程输出拖住，CLI 卡死）：

  ```bash
  tmux new-session -d -s microchat -c <repo> './run.sh'
  ```

  重启 = `ss -ltnp | grep 8787` 拿 PID 精确 `kill`（确认端口空了再起）—— **别用 `tmux kill-session`** ✗：
  它杀不掉 `go run` 起的子进程（孤儿继续占端口 ⇒ 新实例绑不上就退，端口上回答你的是**老二进制** —— 症状就是"新加的路由一直 404"）。
  看日志 = 终端（TUI 删了之后日志不再落 data/microchat.log，直接打 stderr）；**tmux 下崩了没有通知** ⇒ 靠 `pgrep` 与日志。
- **先量再断言**：能实测的就不猜（本项目几乎所有关键结论都来自实测）。
- **重大决策留一句在案**（就写进本文件：结论 + 一句话理由）；**过程与来回不记**（git 里有历史，别把这份撑肿）。
