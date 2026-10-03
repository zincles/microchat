# frontend/ — 四个前端，分四个目录

`core/` 是唯一权威（Go 后端 + HTTP 契约）；这里只住**界面**，不含业务。

| 目录 | 是什么 | 代码在哪 |
|---|---|---|
|---|---|---|
| `godotui/` | Godot 4.8 图形前端（用户在编辑器里自己设计 —— 动手前先问） | 就在这个目录（`project.godot` 在这儿） |
| `tui/` | 终端 TUI（Bubble Tea v2，`View()` 纯函数 + 黄金测试） | `../core/internal/tui/`（与后端同一模块，走 HTTP 契约） |
| `telegram/` | Telegram Bot（长轮询 + 白名单门卫） | `../core/internal/tg/`（同上） |
| `web/` | Web 前端（vite 唯一依赖，运行时零依赖） | 就在这个目录（`package.json` 在这儿） |

(`godotui/export/` 的 APK 等构建产物）与 `web/dist/` 不入库。Web 与 TUI/TG 共用同一套 HTTP 契约（web 是 `fetch` 直调，TUI/TG 走 `../core/internal/apiclient/`）。
