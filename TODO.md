# TODO.md — 下一步做什么

只列**还没动**的东西 ✓；设计口径全在 `DEFINE.md` ✓，过程与来回不记（git 里有历史）。

## 现在就挡路的 ✓

- ~~**CORS + OPTIONS 预检**~~ ✓ **已完成**（CORS 全开、预检 **204** 且**先于鉴权**；顺手把文档承诺、代码里却缺失的 Bearer 鉴权补上了）。
- ~~**取尾 N 条**~~ ✓ **已完成**（2026-09-30）—— 顺带把"取区间"一起做了，**参数形状改了**：`?last=N`（取尾）+ `?from_idx=N&to_idx=M`（闭区间）；
  越界给现有的那几条（不算错）、混用/非正数/反区间 ⇒ 400。序号 `idx` 一并落地（**派生**、不落库、永不改变；`0` = 合成的系统提示词）——
  "第 5 到第 10 条"这种话说得出口了（见 `AGENTS.md` 的接口表与 `DEFINE.md` 的「各种 id」）。

## 接着做 ✓

2. ~~**起标题**（`title` 能力）~~ ✓ **已完成**（2026-09-30）—— 真调模型 ✓、只在标题空着时自动起一次 ✓、用户改过名不再覆盖 ✓。
3. ~~**重摇（re-roll）**~~ ✓ **已完成**（2026-09-30，见 `DEFINE.md` 的「重摇（Re-roll）」）—— 位次候选、切换就地重建、UUID 不变 ✓。
4. ~~**思考流的呈现**~~ ✓ **已完成**（2026-09-30）—— 落库那条在正文上方给暗色折叠「思考（2.1s）」，`/think` 展开全文 ✓。
5. ~~**`messages.updated_at`**~~ ✓ **已完成**（2026-09-30）—— 列已加、改正文时刷新 ✓（`dirty` 是事件式的，没有可换的时间戳比较）。
6. **压缩的自动触发** —— `compact_trigger_tokens` 只算了不算；手动那条（`/compact` 与路由）已经能用 ✓。
7. ~~**二次压缩（金字塔）**~~ ✓ **已完成**（2026-10-01，合并写侧落地 —— 见下「已完成」一节，含验收证据）。
8. ~~**摘要重摇（容错）**~~ ✓ **已完成**（2026-10-01）—— 重摇任意**没有父**的摘要（= 根）：命令给**消息 idx** 上溯到根
   （同树任意 idx 指向同一个目标 ✓）。API 两个平行家族：`…/reroll-message`（原 `/reroll` 改名）+ `…/reroll-summary`（进模式体 `{"idx": N}`），各五件（进 / 状态 / switch / delete / 清）。
   产出**借 compact 的"只生成、不落库"半程**（`RegenerateSummary`；摘要不许绕过 compact 拼请求）；apply = 就地换正文（守卫 `parent_summary_id IS NULL`，id / 区间不动）。
   **验收（实测）** ✓：假上游（每发回话不同）造 s1/s2/s3 金字塔 ⇒ `POST reroll-summary {"idx":3}` **202**（target=s3、`from/to=1..6`）
   ⇒ 摇出第二版 ⇒ `switch {"idx":2}` ⇒ 库里 s3.text 换成新一版、**id / 区间 / 父指针不动** ⇒ 删到只剩一条 ⇒ **退出模式且保留当前版**；
   消息家族改名后照常（进 202 / 清 204）；旧 `/reroll*` ⇒ **404**。
## 收拾性质 ✓

9. ~~删死字段 `Agent.params` / `Agent.prompt_order`~~ ✓ **已删**（2026-09-30）—— 老 `agents.json` 里的它们会在
   下次整份重写时消失（开发阶段可接受 ✓；文档已记一句）。
10. ~~`TERM=dumb` 时满线退回 ASCII~~ ✓ **已完成**（2026-09-30）—— 满线 `-`、重摇位次标记 `< 2/3 >`；
   与颜色降级分开判（`TERM=dumb` / 非 TTY 才退字符集，`NO_COLOR` 只关颜色）。
11. ~~`Task` 上加 `Ability` 字段~~ ✗ **已作废**（2026-09-30）—— `Task.Kind` 与能力 id 合层后该字段冗余，已删 ✓。

## 已完成：金字塔压缩（多层摘要）✓ —— 2026-10-01 落地

**装配侧取粗的早就跑着** ✓（`A, BF, G` 那条行走 ✓）；合并写侧（喂同层顶层摘要、写 `parent_summary_id`）**已落地** ✓。口径如下（设计记录）：

### 手动入口：两种给法，二选一
- `{"blocks": N}`：最老的 N 个已闭合块 —— **只吃新消息**（"压最老的一批新对话"；合并不走这儿）。
- `{"begin_idx": i, "end_idx": j}`：1-based，与 `/messages?from_idx=&to_idx=`、`/state?at_idx=` **同一套词**。
  range 只说一句"**把第 i–j 条收成一条摘要**"，后端按覆盖情况分派：
  **全未覆盖 ⇒ 消息级；同层顶层摘要恰好铺满 ⇒ 合并；其余 ⇒ 400**（按 idx 说清下一步）。
- **作废** ✗：`summaries` 字段、`begin_id/end_id`（id 端点）—— idx 就是 range 的心智，且不再需要"认出端点是消息还是摘要"。

### 一条铁律：一次压缩只吃**同一层级**（消息 = 第 0 层）
- 合并只能在**同一层的顶层摘要**之间；跨层（含"一段已覆盖 + 一段新消息"）⇒ **400**。
- 理由 = **保真度**：深处的节点已经被压过多次 ⇒ 再压就多丢一道（RP 长线记忆要**衰减最慢**）。
- 推论：合并永远取**同级兄弟**、**尽量取满一段**（增广度、减深度）；将来的自动触发也守这条，**绝不造深链**。

### 合并的四条闸（正确性，一条都不能省）
① 孩子全是**顶层**（单亲指针不能有两个爹）；② 区间里每条消息都被盖住，且盖住它的顶层摘要**恰好是这批孩子**（防漏选/防外人/防空洞）；③ 至少两条；④ 不碰最后那个开着的块。

### 落库：**零新增结构、零新增名字**
`store.RecordSummary(summary, sourceIDs)` **单入口**按 `summary.SourceKind` 分支：
`message` ⇒ 回填 `messages.summary_id`（今天就这样）；`summary` ⇒ 回填孩子的 `parent_summary_id`。
**单事务** + 事务内复核（仍在、仍顶层、区间未变、恰好铺满）。

### 细节（定案）
- 合并材料 = 子摘要正文 + 轻抬头（"第 a–b 条（N 块）"）；末尾照旧附"算到区间末的状态"。
- 父的 `blocks` = 子**和**；`dirty` **传播**（父 = 任一孩子脏）。
- 消息的 `summary_id` **保持指孩子**（升格靠 walk 的左端对齐）⇒ **装配侧零改动**。
- 挂起中 ✗：`GET /sessions/{session_id}/summaries`（金字塔要看得见 —— 注意 AGENTS.md L225-226 把它列在"可选（待重做）"里，两边一致）、`-debug summaries`（op 一览里没有，见 `-debug` 无参数输出；要加先改这条）。
- **验收（实测，2026-10-01）** ✓：4 轮（8 条）→ 压 2 块（s1: 1–4）→ 压 1 块（s2: 5–6）→ `--begin-idx 1 --end-idx 6` 合并 ⇒
  s3（`source_kind=summary`、blocks=3），python 裸读库：**s1/s2 的父都指 s3、s3 无父**；`/outgoing` 变一条 `source=summary`（blocks 3，idx 1–6）；
  删第 5 条 ⇒ 消息 5–8 + s2 + s3（**父链一起作废**）+ s1 解链幸存 ✓。

**待核（我的欠账）** ✓ **已核**（2026-10-01）：重摇那条"**删到只剩一条 ⇒ 保留当前版、不回原文**"的库内比对 ——
固定路径造一遍（进模式 ⇒ 切到候选 1 ⇒ 删原文那版）后用 **python 裸读 SQL** 看库：尾条正文 = 「候选 1」、
**UUID 不变**、`updated_at` 已刷新 ✓（`sqlite3` CLI 那次的中文编码问题用 python 绕开；比对用的是一次性测试，跑完已删）。

## 接下来的重点（顺序 + 要点）✓ —— 2026-09-30 记

### A. 金字塔压缩
**已落地** ✓ ⇒ 见上面「已完成：金字塔压缩」那一节（2026-10-01：idx 入口、一次只吃同一层、单入口落库）。
**历史债已还** ✓（2026-10-02）：`source`→`type` 三处改到位 + `Outgoing.children` 嵌套 + `POST .../compact/preview` +
`/compact` 按块注释。DB 列名 `source_kind` 是历史（注释写清，不改）。

### B0. Response 协议（主聊天已通 2026-10-02；辅助调用已切路 2026-10-03）
· 落了：`Provider.endpoints`（protocol→整条 verbatim，预设专用，配置写了忽略+log）+
  opencode-go/opencode 预设带 `/responses` + `Build` 按 protocol 分两路拼 URL（response 体先占位
  `{model,input,stream,max_output_tokens}`，解析下一步）+ `Validate` 矩阵 + Zen 写死表 31 模型
  （`gpt-*`/`grok-*` 前缀兜底，muse-spark 只认显式）+ registry 发现打标 `zen_protocol`（发现优先）。
  实测：refresh 打标 grok/gpt 行 ✓。调用方（chat/compact/title/reroll-message）按表切路。
· Zen `/models` 只有 4 个键（无端点字段）；OpenRouter 有 supported_parameters 但无端点字段（JEV `/alpha/decisions` 仍走特例）。
· 端到端实测 ✓：`muse-spark-1.3-contributor` 经 `/responses` 回话成功（"你好，我是 Muse Spark……"）——
  前提是模型名带 `-contributor`（裸 id 在该 key 上 unavailable）+ `model_route` 有行。
  查表键 = **渠道 id**（`model_route` 按渠道刷新；拿 vendor 查会串台——实测抓到，修掉了）。
· **尾巴已收** ✓（2026-10-03）：辅助调用（compact/title/reroll-message）已与 chat.go Accept 同一条查表
  （`model_route` 有 responses 行 ⇒ `/responses`，正文从 output_text 来；无行 ⇒ chat 缺省，零行为变化）。

### B. Telegram Bot（下一件大事 ✓ 用户点名"特色"；地基已落地 2026-10-02）
· 落了：`go-telegram/bot@v1.27.0`（零依赖）+ `config.json telegram{bot_token,allowed_id,enabled}`（token 文件优先/env 兜底，内容永不回显）
  + TUI `/telegram-bind`（单账户，顶掉旧的）`/telegram-bot-token-set` `/telegram-toggle` `/telegram-status`
  + 后端 `GET/PUT /config/telegram`（`has_token/allowed_id/enabled/running`；PUT 三格各改各的）
  + bot 长轮询（`enabled` 显式开才跑；没 token/连不上只 log）+ 白名单门卫（未绑只回 id）+ 超长按 rune 分段/翻页按钮（edit 同一条，不刷屏）。
· **岔路口已定** ✓：**单人白名单**（只认你的 Telegram id ✓ 保持单人自托管模型 ✓）。
· 真 token 实测 ✓（2026-10-02：getMe 通、收发回声通、菜单已摆 3 条；**发现并修了路由接错的 bug**——`PUT /config/telegram` 曾指着只认 allowed_id 的旧 handler，
  `/telegram-bot-token-set` 与 `/telegram-toggle` 实际不工作；现已指向三格版并有测试）。
· 三条增强 ✓（2026-10-02）：无效命令明说（`/xxx` 未登记 ⇒ 回"不认识"+可用清单，不再当普通文本 echo）；
  摆菜单前**清四个常用作用域的旧命令**（Hermes 残留这么来的）；**上线给绑定用户发问候**（失败只记日志）。
· TG Markdown 服务端渲染 ✓（2026-10-02）：转义后 `parse_mode=MarkdownV2` 发，对端直接画富文本；
  只转义不加糖，`can't parse entities` 才回退纯文本（429 照冒泡），每段独立回退。吐字动效 = 现有节流 edit（保持现状）。
· 命令面扩展 ✓（2026-10-02）：bot 18 条命令（/help /status /new /resume /rename /copy /delete /cut /stop /compact
  /state /outgoing /usage /think /system /providers /model + /start）——**经共享 `internal/apiclient` 调本机后端**
  （抽包后 TUI 与 bot 同用一个客户端，零第二份）；菜单与 /help 从注册表派生（不许两处手写）；/status 打印 CLI 底栏内容
  （会话 | 上下文 | 轮次 | TG | 后端）；/delete /cut 走按钮确认（callback_data ≤64B，待办表在内存）；
  /model 按钮翻页选模型。不做：/provider-add /provider-del（配置管理留 CLI）、/reroll*（下一步）、/telegram-*（CLI 管）、/quit。
· **接 chat** ✓（2026-10-02）：纯文本 = 真发一轮（发进 bot 当前会话 → 202 → 700ms 轮询，
  edit 节流 ≥1s 且文本不变不 edit；占位"生成中… 耗时"/"思考中… N 字"；超 4096 按段补发新消息；
  409 ⇒ "上一轮还在跑"；失败 ⇒ 占位改成原因；~10min 超时说清后停；生成中每 5s sendChatAction(typing)）。
  回声路径（onEcho/sendPaged/pageState）已删。
· **重摇按钮** ✓（2026-10-02）：每段回复底下挂 `[◀] [n/total] [▶]`——◀ 到最左 toast「当前是第一个备选回复」；
  ▶ 不是最右 ⇒ 切到下一版；▶ 在最右 ⇒ **摇一版**（202→轮询→自动切到新版）；中间是计数牌（点了不动作）。
  只有**最新一条回复**的按钮有效：新回复一来，旧按钮先摘登记表再逐段抹掉（点了报"已失效"兜底）；状态只在内存。
· 旧口径保留：长轮询（不要公网）✓；流式节流 edit（≥100–300ms）✓；4096 按 rune 切 ✓；48 小时以上改发新消息 ✓；Topics 一话题一会话 ✓。

### C. 压缩的自动触发（**先聊三件才动手** ✗）
① **何时压**（`compact_trigger_tokens` 只是其一 ✓ 要不要留缓冲 ✓）；② **压多少**（`compact_blocks` ✓ 压不动怎么办 ✓）；
③ **失败怎么办**（**不许静默** ✗ —— 报错还是降级 ✓ 降级也是一种骗 ✓）。它**会花额度** ✗。

### D. JEV 判断模型（函数已接线 ✓ 2026-10-03；剩的是"动态组合"调用点）
· 落了：`judge.Service.JudgeFor`（开关 + 选渠道 + `task.KindJudgement` 挂号 + 失败原样返回；产出是材料，**不落库**）—— 假上游 + OpenRouter 真测往返 ✓。
· 还没做：① **动态组合术**（动作→动作 prompt 这类按判断结果拼载荷的调用点 —— 只动尾部）；② 阈值实测校准（别拍脑袋）；③ 中文语料校准（CJK 精度低一档）。
· `refresh` 跳过 systemone（无统一 `/models` 口径，不算错）✓。

### E. 归档 / 剪枝 / 清原文（大件；三条铁律已写好在 AGENTS.md ✓）
剪枝前**必须先导出** ✓；剪枝与落摘要**同一事务** ✓；报"剪掉 N 条旧分支（已导出）"**不许静默** ✓。

## 大件（想清楚再动）✓

11. **归档 / 剪枝 / 清原文** —— `AGENTS.md` 的「摘要 / 压缩的设计」里有三条铁律：
    剪枝前**必须先导出**、剪枝与落摘要**同一事务**、报数**不许静默**。
12. **JEV 决策模型**（函数已接线 ✓：`judge.Service.JudgeFor`；剩动态组合调用点 + 阈值实测校准 + 产出不进历史/变量）—— 见 `reminder/jev.md`。
13. **Godot 前端接真实数据**（`frontend/godotui/` 由用户在编辑器里自己设计）。
14. **Telegram Bot**（地基已落地 ✓ 2026-10-02：长轮询 + 白名单 + 真 token 实测通；命令对齐 B 节清单 —— 细节见 B 节，不在这儿复述）。
    · Mini App 暂不做 ✗（不打算做 H5）。

