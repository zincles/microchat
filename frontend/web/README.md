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
  重发 / 重摇 / 压缩 / 删除（带预览与确认）· 改名 / 换模型 / 换 Agent / 会话提示词 · ST（SillyTavern）JSONL 导入
- **右载荷栏**：真请求预演（草稿也能 —— 客户端铸会话 id，`POST /outgoing`）+ 世界状态 tab + **压缩树**（文件管理器那种看体积的树：目录 = 摘要、嵌套 = 嵌套压缩）
- **设置六 tab**：聊天 / 界面 / 连接 / 会话 / Agent / Provider（与对话**同级的视图**，不是弹窗；窄屏自动收起左栏）；
  主题七套（默认 `deepseek`，色值实测对齐 chat.deepseek.com）· **背景图**（URL 或上传，本地存；没设 = 纯平底）
- **命令面板**：`/` 进（`/resume` `/compact` `/cut` `/reroll` `/edit` `/editsum` …，只放已有路由的命令）
- **共享层**：`utils/prefs.js`（界面偏好 + `sharedApi()` + 发送键判定/主题应用，含 TG 守卫）· `api/client.js` 的 `mergeStreamSlice`（流式合并唯一规则）· 错误带 `code`/`status`（按 code 分支）· `format.js` 的 modelKey/modelLabel/whoText/sessionTitle（键与标签唯一出口）

口径、路由表与"为什么这么设计"全在根目录 `AGENTS.md`。
