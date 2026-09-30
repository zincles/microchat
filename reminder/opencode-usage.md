# OpenCode GO 的套餐用量 —— 端点、认证、实测

> 专题参考：**OpenCode GO 订阅的"还剩多少"** 从哪来。工程取舍在 `AGENTS.md`（那一节讲的是
> `/chat/completions` 的身份头），这份只讲**用量接口**。
> 结论 = **有官方接口**，而且比发消息那套**宽松得多**（只要 key，不挑身份）。下面是证据。

## 一、结论（先看这个）

| 项 | 值 | 来源 |
|---|---|---|
| 方法 / 路径 | `GET https://opencode.ai/zen/go/v1/usage` | **实测 200** ✓ + omp 二进制里的源码 |
| 认证 | `authorization: Bearer <OPENCODE_API_KEY>` | **实测** ✓（**只要这一个头**） |
| 必需头（除认证外） | **一个都不需要** ✗ —— 无 UA、无 `accept`、**无 `x-opencode-session`** 一样 200 | **实测** ✓ |
| 响应 | `{"usage":{"rolling":{…},"weekly":{…},"monthly":{…}}}` | **实测** ✓ |
| 窗口字段 | `status`（`"ok"` / `"rate-limited"`）、`percent`（**该窗口自己**的已用百分比 0–100）、`resetsAt`（ISO/RFC3339） | `status=ok`+`percent`+`resetsAt` **实测** ✓；`rate-limited` **推测**（omp 的校验认它，本机没撞上） |
| 计费口径 | 三个窗口 = 月度的 **20% / 50% / 100%**（5 小时 / 每周 / 每月） | 官方文档 `opencode.ai/docs/go`（**文档说了限额，没说 API**） |
| 非 200 | `401` + `{"type":"error","error":{"type":"AuthError","message":"…"}}`（没 key ⇒ `"Missing API key."`，key 不对 ⇒ `"Unauthorized"`） | **实测** ✓ |
| 路径必须**逐字** | `/v1/usage/`（多个尾斜杠）⇒ **401**（不是重定向）；写错路径 ⇒ 站点的 404 HTML | **实测** ✓ |

真实样例（**脱敏**：体里本来就没有 key；这是 2026-09-30 三次只读 GET 的原样返回，三次数值不同 ⇒ 它是活的）：

```jsonc
// 17:36（首次）
{"usage":{"rolling":{"status":"ok","percent":3,"resetsAt":"2026-09-30T13:47:01.482Z"},
          "weekly": {"status":"ok","percent":1,"resetsAt":"2026-10-05T00:00:00.000Z"},
          "monthly":{"status":"ok","percent":0,"resetsAt":"2026-10-28T18:39:36.000Z"}}}
// 17:38 → rolling=4 weekly=1 monthly=0；17:40（`-debug usage`）→ 5 / 2 / 1
```

> 注：`resetsAt` 里的 13:47:01.482Z 是**滚动窗口**的重置时刻（每 5 小时一格）；
> 每周窗口对齐到周一 00:00Z；每月窗口像个"订阅周期"的锚点（本机是 10-28 18:39:36Z）。

## 二、这东西是怎么被找到的（三条路）

### 路 1：本机的 omp / pi ✓（**决定性**）

`omp` 是 Bun 编译的单文件（`~/.local/bin/omp`，285 MB，`omp/18.4.3`）——
**Bun 把 TS 原文打进了二进制**，所以 `strings | grep` 直接捞到源码：

```
$ strings -n 6 ~/.local/bin/omp | grep -i 'opencode.*usage'
  return "https://opencode.ai/zen/go/v1";
  OpenCode Go usage endpoint returned ${a.status}…
  t.logger?.warn("OpenCode Go usage fetch failed", {
  "@oh-my-pi/pi-ai/usage/opencode-go": JFu,
// packages/ai/src/usage/opencode-go.ts          ← 文件名注释也在里面（byte 168676651）
var zWe = "opencode-go", WHo = "https://opencode.ai/zen/go", SQi = "/v1/usage", LHo, BWe, KWe;
```

那段源码（从二进制 byte 168678680 起 dump 出来的，函数 `MQi` 的头部）逐字是这样：

```js
const o = `${vQi(e.baseUrl)}${SQi}`;            //  → https://opencode.ai/zen/go + /v1/usage
const a = await t.fetch(o, { headers: {
   accept: "application/json",
   authorization: `Bearer ${s.apiKey}`,
   "User-Agent": Vo,                            //  omp 自己的 `omp/<版本>`（同 bundle 作用域里的 Vo = `omp/${version}`）
   "x-opencode-session": Yg(),                  //  装机级 UUID（实测**不是必需**）
}, signal: e.signal });
if (!a.ok) { if (a.status === 401 || a.status === 403) throw new Error(`…returned ${a.status}: ${msg}`) ; return null }
if (!X(n) || !X(n.usage)) return null;
for (const a of LHo) { … }                      //  LHo = rolling / weekly / monthly 三个窗口
```

`LHo`（窗口定义，同样在二进制里）：

```js
LHo = [
  { key: "rolling", limitId: "rolling-5h", windowId: "5h", label: "5 Hour",  durationMs: 5 * Rl },
  { key: "weekly",  limitId: "weekly",     windowId: "7d", label: "Weekly",  durationMs: 7 * gf },
  { key: "monthly", limitId: "monthly",    windowId: "monthly", label: "Monthly", durationMs: undefined },
];
```

它校验的也正是我们钉住的那三样：`percent` 是 0–100 的数、`status ∈ {ok, rate-limited}`、
`resetsAt` 能 `Date.parse`；**三个窗口缺一个就整份不用**（`"…missing or malformed windows"`）。
计划名是 omp 自己贴的标签：`metadata: { planType: "OpenCode Go", endpoint }` ← 接口**不回**套餐名。

**顺带否掉两条**（省得下次再挖）：

- `~/.omp/` 下**没有**用量实现的源码（omp 是打包好的二进制）；`~/.omp/stats.db` 里也没有
  `usage_history` 表（那份 schema 在二进制里，是 omp 的**本地**记账，另一回事）。
- `omp` 的 usage registry（二进制 byte 152153800 那段模块名清单）里有
  `usage/opencode-go`、`usage/openai-codex`、`usage/zai`… 但**没有** `usage/opencode-zen`
  ⇒ OpenCode **Zen**（非 Go 套餐）大概没有这个接口（见下"墙"里的 404）。

### 路 2：官方文档 / 源码 ✗（有意思的**空白**）

- `opencode.ai/docs/go`（★ 官方）：只有"限额 = 月度金额，5 小时 / 每周 / 每月 = 20% / 50% / 100%"
  这段口径。**没有一个字**提用量 API。
- `~/.nvm/…/node_modules/@earendil-works/pi-coding-agent`（Pi 主线的 npm 包）：
  `grep -rl 'v1/usage'` **零命中**。
- `/home/zincles/pi`（`badlogic/pi-mono` 的 checkout，commit `c90d9ea`）：**没有** `packages/ai/src/usage/` 这个目录。
  ⇒ 用量查询是 **omp 的 fork**（`@oh-my-pi/pi-ai`）先做的，主线 Pi 还没有。
- `anomalyco/opencode#16017`（2026-03-04 开，2026-09-15 关）：35 条评论全在**要**这个接口；
  08-07 有人实测 `GET /zen/go/v1/usage`、`/zen/v1/usage`、`/zen/go/v1/balance`
  **三个都回 SPA 的 404 HTML**，普通响应里也没有 `X-RateLimit-*` 头。
  收尾那条只是有人做了个 TUI 插件（`and7ey/opencode-go-usage`）。
  ⇒ **08 月还没有，09 月有了**；官方文档里没有它（所以它是"能用但没文档"的接口 —— 官网只有限额口径）。
- 近亲：`#18648`（同年 03-22）、`#10448`（Zen 余额，**另一码事**：那是按量付费的**美元余额**）。

### 路 3：真打（**只读**，见下表）✓

## 三、实测矩阵（2026-09-30 · 真 key · 全是 `GET`，不消耗额度）

| # | 变体 | 结果 |
|---|---|---|
| 1 | `/zen/go/v1/usage` + `authorization` + `user-agent` + `x-opencode-session` + `accept` | **200** ✓ |
| 2 | 同上，**去掉** `x-opencode-session` | **200** ✓（会话头**不是必需** —— 与 `/chat/completions` 不同） |
| 3 | 同上，**裸 `curl`**（无 UA ⇒ `curl/x.y`） | **200** ✓ |
| 4 | 只留 `authorization`（连 `accept` 都不给） | **200** ✓ ⇒ **认证是唯一的硬闸** |
| 5 | key 换成 `sk-not-a-real-key-000` | **401** `{"error":{"type":"AuthError","message":"Unauthorized"}}` |
| 6 | **不带** `authorization` | **401** `…"message":"Missing API key."` |
| 7 | `/zen/go/v1/usage/`（**多个尾斜杠**） | **401** ✗（不是 301/重定向；**路径要逐字**） |
| 8 | `/zen/go/v1/usages`（拼错） | **404**（整页 SPA HTML） |
| 9 | `/zen/go/v1`（根） | **404**（同 8） |
| 10 | `/zen/go/usage`（少 `/v1`） | **404** ✗ |
| 11 | `/zen/v1/usage`（**Zen** 那条路） | **404** ✗（本机 key 是 Go 的；Zen 有没有是**推测**：omp 那边也没有 zen 的 usage provider） |

结论：**只有 1–4 是通的；别发明别的路径。** `#4` 说明它连 `accept` 都不挑 ——
但代码里照旧发 `accept: application/json` 与自报身份的 UA（本仓口径：不占"用裸库名"的便宜）。

## 四、落地（本仓）

- `core/internal/providers/opencode_usage.go`（**新文件**）：
  `OpencodeUsage(ctx, apiKey) (PlanUsage, error)` / `OpencodeUsageAt(ctx, baseURL, apiKey)`。
  - **总超时 20s**（`OpencodeUsageTimeout`，交给 `NewClient` 的 `Timeout` + 连接超时）；
    调用方的 ctx 更早则以 ctx 为准 —— 两条都验过。
  - 错误**原样返回、不重试**；非 200 优先把上游的 `error.message` 带出来；
    **错误文本里永远没有 key**（结构体里也没有可以漏的地方：请求头是就地拼的）。
  - 三窗口（`rolling-5h` / `weekly` / `monthly`）**缺一个就报错**，不编半个结果。
  - 名字用 `PlanUsage` / `PlanWindow` —— `Usage` 那个名字被"这一发对话的 token 用量"占了
    （`stream.go`），两个"usage"别在同一层里打架。
- `core/internal/providers/opencode_usage_test.go`（**新文件**）：解析用 `httptest` 钉死（**不联网**）：
  请求形状（GET / 路径 / Bearer / accept / UA / **不发会话头**）、真实响应样例的三个窗口、
  没 key 时**一个包都不发**、401 带出上游 message、404 的 HTML 被缩成一行、
  六个畸形响应各报各的、`rate-limited` 与小数百分比照收、ctx 超时**真的**断。
  真联网那条（`TestOpencodeUsageLive`）**没有 `OPENCODE_API_KEY` 就 `t.Skip`**（`-short` 也跳）。
- `core/debug.go`（**最小一处**）：新增 `-debug usage [provider_id]` —— 只读 GET，
  打一行脱敏 JSON（plan / endpoint / 三个窗口）；不给 id 就用 `providers.json` 里第一个
  `opencode-go` 渠道（走 `FromConfig`+`ApplyPreset`，与真发同一条转换）。

```console
# 注意：路径是**相对进程 cwd** 的，而 `go -C core run .` 的 cwd 是 `core/` —— 所以 `-data ../data -config ../data/config`
$ go -C core run . -data ../data -config ../data/config -debug usage      # 不给 id = 用第一个 opencode-go 渠道
$ go -C core run . -data ../data -config ../data/config -debug usage ocgo-probe

{"plan":"OpenCode Go","endpoint":"https://opencode.ai/zen/go/v1/usage","fetched_at":"2026-09-30T17:40:16.817640759+08:00",
 "windows":[{"id":"rolling-5h","label":"Rolling (5h)","percent":5,"status":"ok","resets_at":"2026-09-30T13:47:01.482Z"},
            {"id":"weekly","label":"Weekly","percent":2,"status":"ok","resets_at":"2026-10-05T00:00:00Z"},
            {"id":"monthly","label":"Monthly","percent":1,"status":"ok","resets_at":"2026-10-28T18:39:36Z"}]}
```

（同一时刻的 `curl` 回 `percent: 5 / 2 / 1` —— 函数解出来的数字与上游一致 ✓。）

## 五、墙与还没弄清的事

**撞过的墙**（都如实记下）：

- `/zen/v1/usage`（Zen）、`/zen/go/usage`、`/zen/go/v1/usages`、`/zen/go/v1` → **404**（SPA HTML）。
- 尾斜杠变体 → **401** 而不是 200/301（**别给这路径加斜杠**）。
- 官方文档与 GitHub 上**从来没有**这个端点的说明；主线 Pi 里也没有实现（只在 omp 的 fork 里）。
- `-debug usage` 第一次跑报"没有渠道"：**是用法坑不是上游坑** —— `-data` 不会连带改 `-config`
  （`config` 目录默认写死成 `<data>/config`），两个都要给；而且 `go -C core run .` 的 cwd 是 `core/`，
  相对路径会错位（写成 `-data data -config data/config` ⇒ 它会去找 `core/data/config`）。
  **这一处已修**：现在报错分得开三种"没有"（① 一个渠道都没读到 ② 没有这个 id
  ③ 有这个 id 但它不是 opencode-go），并且把**真读的配置目录**打在错误里：
  `{"error":{"code":"invalid","message":"一个渠道都没读到：检查 -config 指向的目录里有没有 providers.json（用法：-debug usage [provider_id]；配置目录：data/config）"}}`。

**还没弄清**（✗ = 推测/未知，别当结论用）：

- ✗ `status: "rate-limited"` 本机没撞上（omp 的校验认它，我们也认；真顶到限额时应是这个）。
- ✗ `percent` 是整数还是小数：本机三次都是整数；omp 也没假设它是整数（我们保留小数）。
- ✗ **Zen（非 Go）有没有对应接口**：omp 的 registry 里没有 `usage/opencode-zen`，
  本机 key 打 `/zen/v1/usage` 是 404 ⇒ 倾向"没有"，但没拿到 Zen 订阅的 key，**没实测**。
- ✗ 接口**不回**美元金额 / 套餐名 / 已花金额 —— `#16017` 里大家想要的那些字段一个都没有；
  只有三个百分比 + 重置时刻。
- ✗ 是否限流 / 有无缓存：未知（omp 自己带一层 `cacheVersion: 2` 的客户端缓存，那是它的事）。

## 六、出处（可复查）

| 出处 | 位置 |
|---|---|
| omp 的用量实现（**源码级**） | `~/.local/bin/omp`，byte 168676651 起（`// packages/ai/src/usage/opencode-go.ts`）；模块名清单在 byte 152153800 |
| omp 的 UA 常量 | 同二进制 byte 153195328：`Vo = \`omp/${version}\``（实测 `omp --version` ⇒ `omp/18.4.3`） |
| 官方限额口径 | `https://opencode.ai/docs/go`（"5-hour 20% / weekly 50% / monthly 100%"） |
| 社区来龙去脉 | `github.com/anomalyco/opencode` issues **#16017**（这份接口的来龙去脉全文）、**#18648**、**#10448**（Zen 余额，另一码事） |
| 主线 Pi | `/home/zincles/pi`（`badlogic/pi-mono` @ `c90d9ea`）—— **没有** usage 目录；npm 的 `@earendil-works/pi-coding-agent` 也 grep 不到 `v1/usage` |
