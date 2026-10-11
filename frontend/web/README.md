# frontend/web — Web 前端（当前主力）

与 TUI / TG Bot **同一条 HTTP 契约**（只调 `/api/v1`，见根目录 `AGENTS.md` 的 API 表；不碰后端内部包）。

## 跑起来

```bash
./start-web-ui.sh    # 一键：后端 :8787 + vite :5173（Ctrl+C 一起停）
# 或分开跑：
cd frontend/web
npm ci            # 从锁文件重装（node_modules 永不入库）
npm run dev       # vite :5173（默认连本机 8787；连远端改 localStorage.mc_api）
npm test          # node 原生 test runner：纯函数与请求形状的契约测试
npm run build     # 产出 dist/（不入库，部署时现打）
```

后端另起：`./microchat`（8787 服务；CORS 全开，预检 204）。

## 依赖

`vue@3.5`（框架）+ `marked`（markdown 渲染）+ `katex`（数学）；构建用 `vite` + `@vitejs/plugin-vue`。
运行时没有自造传输层（`fetch` 直调）。**加依赖先问**；`node_modules/`、`dist/` 永不进库。

## 现在能干什么

- **会话**：草稿态（点＋/开机只开一张客户端草稿，**首句才建会话**）· 发送/流式（202 受理 + 轮询 + 游标读）·
  重发 / 重摇 / 压缩 / 删除（带预览与确认）· 改名 / 换模型 / 换 Agent / 会话提示词 · ST（SillyTavern）JSONL 导入 · 消息列只挂最后 20 段（`加载更多历史` 往回翻页；摘要卡展开才拉原文） · 摘要卡可 `⟳` 刷新（重摇出候选、旧版不丢）/ `✎` 编辑 · 输入框上方一行后台任务数（`/tasks`） · 消息列带编号（`你 #2`），摘要段折成 `Summary #a-b` 卡（可展开未压缩原文）
- **右载荷栏**：真请求预演（草稿也能 —— 客户端铸会话 id，`POST /outgoing`）+ 世界状态 tab + **压缩树**（文件管理器那种看体积的树：目录 = 摘要、嵌套 = 嵌套压缩）
- **设置五 tab**：聊天 / 连接 / 会话 / Agent / Provider（与对话**同级的视图**，不是弹窗；窄屏自动收起左栏）。**入口（⚙）只有 root（管理员）看得见**：普通用户连按钮都不给，进设置还有守卫兜底
- **登录 / 初始化**：第一次打开是「初始化 root」（uid 0 是建库种子，setup 只给它设口令、**只开一次** —— 不是注册制），之后是登录（JWT 存 `mc_token`，全请求带 `Authorization: Bearer`）；登出在用户视图
- **用户视图**（与设置平级，左栏底 `👤 用户`，人人可进）：身份（名字 · 管理员 · 退出登录）+ **改口令（自己）**（改完当场重登换票）+（**admin 才有**的）**用户管理**：列表 / 加人 / 行内改名、改口令 / 删人（两步确认："会话转给 root / 连会话一起删"）+ 个人偏好 —— 主题七套（默认 `deepseek`，色值实测对齐 chat.deepseek.com）· **背景图**（URL 或上传，本地存；没设 = 纯平底）· 发送键 · 输入预演
- **滑动面板**（触屏**跟手**）：左滑出右栏、右滑出左栏，半开维持、松手吸附；先关后开
- **移动端键盘不遮输入框**：visualViewport 拟合应用盒（浮层随键盘抬、命令面板限高不顶出小视口）
- **PWA（可"安装为应用"）**：`vite-plugin-pwa`（v2）。**只在 build/preview 开 SW**（dev 不开——免得开发时吃到旧缓存）。SW **只预缓存壳**（HTML/JS/CSS/图标/字体）：离线打开能进"连接门板"；`/api` 一概不碰（数据必须现取，且 API 在另一个来源，SW 天生管不着）。**安装的前提在来源：HTTPS 或 localhost** —— 局域网 `http://<IP>:8788` 装不了（不是安全上下文，连 SW 都不会注册）；云端形态（反代终结 TLS，如 Cloudflare）天然满足。图标 `public/pwa-192.png` / `pwa-512.png` / `pwa-maskable-512.png` / `apple-touch-icon.png`（由 `favicon.svg` 栅格化）。桌面/安卓 Chrome：地址栏装或菜单"安装应用"；iOS 16.4+：分享菜单"添加到主屏幕"
- **连接门板**：探不通后端先给全屏"连接"面板（默认地址跟着**你打开页面的主机**预填：`<该主机>:8787`；裸地址自动补 `http://` 与 `/api/v1`；探通即存偏好刷新进聊天）
- **命令面板**：`/` 进（`/resume` `/compact` `/cut` `/reroll` `/edit` `/editsum` …，只放已有路由的命令）
- **共享层**：`utils/prefs.js`（界面偏好 + `sharedApi()` + 发送键判定/主题应用，含 TG 守卫）· `api/client.js` 的 `mergeStreamSlice`（流式合并唯一规则）· 错误带 `code`/`status`（按 code 分支）· `format.js` 的 modelKey/modelLabel/whoText/sessionTitle（键与标签唯一出口）

口径、路由表与"为什么这么设计"全在根目录 `AGENTS.md`。
