# MicroChat

轻量 SillyTavern 替代，面向角色扮演与长对话。采用 Go 后端、Vue 3 / TypeScript / Vuetify Web 前端与 SQLite 存档；模型生成使用官方 OpenAI Go SDK，支持 Chat Completions 和 Responses 兼容渠道。

> 本项目由 AI 辅助开发，未逐行人工审阅。重要数据请先备份。

## 当前能做什么

- 流式阅读对话，编辑正文或思考，续写已有历史，重摇回复或摘要。支持工具进度、来源和用量明细。
- 在上下文工作区查看请求、世界状态和历史压缩；压缩保留原文。智能体可用提示词片段、SillyTavern 预设或 Flow 图。
- 复制、导入与导出会话；导入 SillyTavern 聊天记录。导出会话不是完整数据备份。
- 安装为 PWA，换设备继续使用同一服务器。PWA 外壳可离线加载，访问会话和模型仍需后端在线。
- 左栏「生图」「项目」暂为占位；没有远程 Agent 执行、登录或多用户系统。

## 部署

> **应用没有内建鉴权。** 默认端口只绑定回环。要从其他设备访问，请保留回环绑定，在前面加反向代理和鉴权（如 Basic Auth、OAuth 或 Cloudflare Access），不要直接暴露端口。

在 Linux 上安装 Docker Compose，或使用 Podman Compose：

```bash
git clone https://github.com/zincles/microchat.git
cd microchat
docker compose up -d
```

打开 <http://127.0.0.1:8788>，不需要初始化账号。`8788` 提供页面并同源代理 `/api`，反代要指这个端口；`8787` 只提供 API，发布回环口供本机开发使用。适当配置 HTTPS 才能从远端安装 PWA（`localhost` 例外）。

会话库、配置与背景都在 `data/`，默认配置目录是 `data/config/`。备份或迁移整个数据目录；若单独指定了配置目录，记得另存。不要把数据或渠道密钥提交到 Git。只改源码用 `docker compose restart`；改镜像或依赖构建层用 `docker compose up -d --build`，改端口映射要重建容器。

## 内置更新

点左栏品牌「MicroChat」，或进入「设置 → 更新」。版本区显示本地提交与远端分支关系；前端缓存区单独显示当前页面、服务器构建及 Service Worker 状态。

- **拉取并重启**走 `git pull --ff-only`，不自动合并或 stash。分叉时只在明确选择「变基并重启」后变基；失败不重启。Compose 的 `MICROCHAT_RESTART_FILE` 让入口脚本在容器内重新构建前后端；没有此配置的宿主二进制不能代为重启。
- 自动更新可在面板关闭。服务器成功构建不等于浏览器已经用新壳；若顶部出现前端更新提示，点「刷新」。

## 开发

从仓库根运行 `./run.sh` 启动 Go 服务。若容器已在使用同一 `data/`，不要再起第二个后端；开发前端可直接代理本机容器的 `8787`。

```bash
go -C core test ./...
# 另一个终端，只启动前端开发服务（127.0.0.1:5173）
./start-web-ui.sh
```

前端命令与交互约束见 [frontend/web/README.md](frontend/web/README.md)。维护入口：[工程约定](AGENTS.md) · [领域词表](DEFINE.md) · [近期待办](TODO.md)。

## 许可

AGPL-3.0，见 [LICENSE](LICENSE)。


