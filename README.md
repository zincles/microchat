# MicroChat

> **本项目是 100% VERIFIED AI VIBE SH*T。100% 代码由 DeepSeek V4.1 Flash 编写；这份 README.md 同样由 AI 编写。**
>
> 没有人类逐行审阅的担保。请先备份 `data/`，再自行决定是否把它放到重要数据上。

轻量 SillyTavern 替代（RPG 向）：单体 Go 后端 + Vue Web 前端，SQLite 存档，支持世界状态、摘要压缩、重摇、PWA 与内置更新。

## 技术栈

### 后端

| 组件 | 说明 |
|---|---|
| Go 1.26 | 主语言；`net/http` 标准库路由，没有 Web 框架 |
| `modernc.org/sqlite` | 纯 Go SQLite 驱动，单文件数据库 |
| `github.com/google/uuid` | UUIDv7（消息 / 会话 id） |
| `github.com/tmaxmax/go-sse` | 流式生成用的 SSE 服务端 |
| `golang.org/x/sys` | 间接依赖 |

- 存储：`data/microchat.db`（SQLite WAL）；配置：`data/config/*.json`。
- 没有 ORM；所有 SQL 只在 `core/internal/store`。
- 世界状态不落库，每次从消息正文现演。

### 前端

| 组件 | 说明 |
|---|---|
| Vue 3.5 + TypeScript | 单页应用，Composition API + `<script setup>` |
| Vite 8.3.6（Rolldown） | 开发服务器 / 生产构建；PWA 由 Vite 插件生成 |
| Vuetify 4 | 输入栏、设置页、对话框、菜单等界面组件 |
| marked | Markdown 渲染 |
| KaTeX | 数学公式渲染 |
| `vite-plugin-pwa` | Service Worker / manifest / 离线壳 |
| `vite-plugin-vuetify` | Vuetify 按需导入 |
| `@vitejs/plugin-vue` | Vue SFC 编译 |
| `vue-tsc` / `tsx` | 类型检查 / 测试运行 |

- 前端源码在 `frontend/web/`。
- PWA 只在 HTTPS 或 localhost 下可安装。

## 部署

> ⚠ **本项目仅考虑单人使用，未设置任何鉴权。** 对外使用时，请务必搭配反向代理（例如 Nginx）并配置鉴权验证（Basic Auth / OAuth / Cloudflare Access 等）后再访问；不要把 `8788` 直接暴露到公网。

环境要求：Linux、Docker 与 Docker Compose（本机是 Podman，`podman compose` 同样可用）。

```bash
git clone https://github.com/zincles/microchat.git
cd microchat
docker compose up -d
```

打开：

```text
http://127.0.0.1:8788
```

说明：

- 容器内前端（Vite preview）在 `8788`，Go 后端在容器内的 `8787`；浏览器只访问 `8788`，`/api` 由同容器前端代理。
- 默认**只绑定 `127.0.0.1`**，且**没有账号、没有鉴权**。
- 要让手机 / 平板或公网访问，需要把 `docker-compose.yml` 里的 `127.0.0.1:8788:8788` 改成 `8788:8788`（或让反代转发到回环口）；**同时必须在前面加反代 + 鉴权验证**。只改端口绑定等于对同网段/公网裸奔，风险自负。
- 数据全部在 `data/`（数据库、配置、界面偏好、自定义背景）；备份这个目录即可。
- `docker compose up -d` 使用现有镜像；改了 Dockerfile / 依赖层才需要：
  ```bash
  docker compose up -d --build
  ```
- `docker compose down` 不会删除镜像，也不会删除 `data/`。

## 内置更新

左栏底部有「更新」入口，点开进入更新面板：

- 上方「当前本地版本」：提交、分支、提交标题、完整 hash、工作区是否脏；
- 中间分隔线；
- 下方「GitHub 最新版本」：远端最新提交，以及它与本地的关系：
  - `✓ 已是最新提交`
  - `本地领先远端 N 个提交（尚未推送）`
  - `远端有新提交 N 个，可拉取并重启`
  - `本地与远端已分叉`

点「拉取并重启」会：

1. 执行 `git pull --ff-only`（不 merge、不 stash）；
2. 成功后写 `data/.restart-request`；
3. 容器 `entrypoint` 收掉旧的 Go / Vite 进程，原地重新编译并重启前后端。

所以部署后日常更新不需要 SSH 进去手动 `git pull`。

注意：

- 工作区脏、或本地与远端分叉时，`git pull --ff-only` 会失败；**不会重启**，错误会显示在面板里。
- 拉取需要容器里有 `git`（Dockerfile 已安装；旧镜像第一次请 `docker compose up -d --build`）。
- `docker compose restart` 只重启容器，不重建镜像；源码是 bind 进去的，entrypoint 会按源码哈希决定是否重新编译。
- 远端没有新提交时按钮也可以重启，只是 `git pull` 会输出 `Already up to date.`。

## 开发

```bash
# 本机直接跑（同进程服务，不需要容器）
./run.sh

# 后端测试
go -C core test ./...

# 前端热更新（5173，代理到 8787）
cd frontend/web
./node_modules/.bin/vite

# 前端测试 / 类型检查 / 生产构建
cd frontend/web
npm test
npm run typecheck
npm run build
```

更细的模块边界、不变量与接口契约见 `AGENTS.md`、`DEFINE.md`、`TODO.md`。

## 许可

AGPL-3.0，见 `LICENSE`。
