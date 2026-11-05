# TODO.md — 下一步做什么

只列**还没动**的东西；设计口径全在 `DEFINE.md`，接口契约看 `AGENTS.md`，过程与来回不记（git 里有历史）。
**不要**从 git 历史里把已删的东西（TUI / TG Bot / Godot 前端 / 账号鉴权 / `display_mode` / 布局切换）补回来 —— 那些是用户明确定的删除，不是欠账。

## 还没动

### 1. `template` 模块（`{{…}}` 占位符）
- 唯一还空着的模块格子。口径已定：白名单 = 枚举（`{{system_time}}` / `{{state_before}}` / `{{state_after}}` / `{{range}}` / `{{blocks}}`）；
  **不认识的 `{{foo}}` 原样留着**；**只扫一遍**（替换进去的值不再当模板扫）；
  **会话 Agent 的提示词永不替换**（一变前缀缓存每轮全废）。
- 落点：`core/internal/template` + 在提示词拼装那一处接线；`GET /sessions/{id}/prompt` 与出站必须走同一处。
- 现在 `{{…}}` 原样进提示词，不报错 —— 这是"还没做"，不是特性。

### 2. 归档 / 剪枝 / 清原文
- 大件，三条铁律已写死在 `AGENTS.md`：**剪枝前必须先导出** ✓、**剪枝与落摘要同一事务** ✓、**报数不许静默** ✓。
- 现在没有任何剪枝代码（`messages` 只增不减，除手动删后缀）；导出 v3（含金字塔）是唯一搬家手段。

### 3. JEV 判断模型：复活路径已留好，功能停着
- 2026-10-28 用户定「暂时砍掉 agent 的 judge 功能」：能力已从 `definitions` 摘掉、界面不出，
  `config.RetiredAbilityIDs` 读入即摘（老 `agents.json` 写着它照常能读）。
- 代码**没删**：`internal/judge`、`task.KindJudgement` 都在，只是没有调用点。
- 要接着做：① 把它从 `RetiredAbilityIDs` 拿出来、往能力表加回一行；② 动态组合调用点（动作→动作 prompt，只动尾部）；
  ③ 阈值实测校准（别拍脑袋，含中文语料）。见 `reminder/jev.md`。

### 4. `DEFINE.md` 里两处"待定"
- **role 里要不要有 `system`**：现状没有（系统提示词走三级解析，不进消息表）。要改就得动迁移 + 世界状态现演。
- **上游把思考内嵌在正文里**（` thinking…<｜end▁of▁thinking｜>` 这种）：现在只认独立字段（`reasoning` / `reasoning_content`），
  内嵌风格需要一步"剥离"（剥出的进 `reasoning`，正文删掉）—— 现在没做。

## 已知取舍（不是 TODO，别当漏项）

- **自动压缩默认关**（`chat.auto_compact` 缺字段 = 关）：未经验证，用户定默认不开；要用的去设置里打开。
- **PWA 安装要 HTTPS 或 localhost**：局域网 `http://<IP>` 装不了；云端反代终结 TLS 即可。
- **`vite preview` 不是生产静态服务器**：只 gzip + HTTP/1.1；公网部署由 Caddy/nginx/Cloudflare 开 brotli + HTTP/2。
- **左栏「生图」tab 只有选中态**，没有接任何 API。
- **访问控制靠端口绑定**（compose 两条都绑 `127.0.0.1`；后端 `host` 默认 `127.0.0.1`）：Go 里没有鉴权，也判不出来源。
- **模型筛选器/路由表刷新**：筛选器改了要再刷一次模型；`refresh-routes` 要手跑（启动不跑）。

## 已完成（封存）

只记结论与落点，细节看 `AGENTS.md` / `DEFINE.md` / git。

**后端（`core/`）**
- 会话线性 + `idx` 派生 + 删除只删后缀（UUIDv7 顺序即真相）；Copy 复制消息与摘要、世界状态不复制。
- 世界状态：`<state>` 语法的唯一权威 `statelang`；`state.FromSources` 现演、`BuildOutgoing` 唯一出站拼装；`at_idx` 回溯。
- 金字塔压缩：消息级 + 合并级（同层、区间、单事务 `RecordSummary`）、自动触发（默认关）、`preview` / `payload` / 高级压缩（分段 + 修洞，并发 + 单事务落库）、撤销压缩（顶层 + 级联）。
- 重摇两族（消息 / 摘要）：候选只在内存、UUID / id 不变、候选流走打字机；与生成共用一把闸。
- 能力：`title` / `compact`；默认模板住内置段（通用 + RP）；能力可拼段；标题模型三档（global / session / agent）。
- Task：一次上游请求的全程；五状态；`defer` 兜底；可取消（`POST /tasks/{id}/cancel`）；队列任务（串行、一步失败整体结束、可取消）。
- 渠道：vendor × protocol 两轴、中文名、`max_concurrency` 信号量、测试连通性、模型筛选器（OR）、`model_route` 查表切 `/responses`。
- 出站三份账：(a) 上次真发（`/debug/last-payload`）、(c) 追加待发那句（`POST .../outgoing`）、(d) 草稿第一句（`POST /outgoing`）；预演 = 真发。
- 导出 / 导入：对话 JSON v3（含金字塔 + reasoning）、ST JSONL 导入；Agent 卡带（agent + 引用到的用户段）。
- 2026-11-01 砍掉：账号 / 鉴权 / TUI / TG Bot / Godot 前端。

**前端（`frontend/web/`）**
- 单文件接线 + 纯函数 utils；契约测试钉请求形状与"删掉的名字"。
- 聊天：SSE + 轮询兜底、打字机（按包间隔）、终端光标、前台对齐、窗口分页（`/messages/batch`）、摘要卡、上下文环（后台任务数 + 旋转）。
- 上下文工作区：载荷 / 状态 / 压缩树 / 高级压缩 / 修洞；平铺、分块默认折叠、折叠全部（无展开全部）。
- 偏好全住服务器（`data/config/prefs.json`）；markdown 逐类型开关；字体 / 缩放 / 背景；会话列表五档分组。

**部署**
- 单容器两进程（Go + vite preview），只公开 `127.0.0.1:8788`；源码 bind，`podman compose restart` ≈ 10–12s 生效。
- 容器里后端绑 `0.0.0.0`（否则宿主 `127.0.0.1:8787` 发布打空）；Host 闸门默认不限制（`MICROCHAT_ALLOWED_HOSTS` 可收窄）。
