# frontend/web — Web 前端（最小可跑版）

与 TUI / TG Bot **同一条 HTTP 契约**（只调 `/api/v1`，见 `AGENTS.md` API 表；不碰后端内部）。

## 跑起来

```bash
./start-web-ui.sh    # 一键：后端 :8787 + vite :5173（Ctrl+C 一起停）
# 或分开跑：
cd frontend/web
npm ci            # 从锁文件重装（node_modules 永不入库）
npm run dev       # vite :5173（连本机 8787 后端；远端改 localStorage.mc_api）
npm test          # node 原生 test runner，无测试框架
npm run build     # 产出 dist/（不入库，部署时现打）
```

后端另起：`./microchat`（8787 服务；CORS 全开，预检 204）。

## 依赖（唯一：vite）

`package.json` 里只有一个 `devDependencies: vite@7`（构建用；运行时**零依赖**——`fetch` 直调）。
版本锁死、LTS 线；加依赖先问。`node_modules/`、`dist/` 永不进库（根 `.gitignore` 已锁）。

## 现在能干什么

健康检查 → 启动编排（清 0 消息无标题会话 → 建真会话）→ 看消息 → 发一句话（202 受理 + 300ms 轮询 `status` + 游标读 `turn/text`）。
多行粘贴、压缩、重摇、agents、providers 都还没做 —— 先把这一屏跑稳再加。
