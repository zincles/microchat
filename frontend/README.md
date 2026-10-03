# frontend/ — 三个前端，分三个目录

`core/` 是唯一权威（Go 后端 + HTTP 契约）；这里只住**界面**，不含业务。

| 目录 | 是什么 | 代码在哪 |
|---|---|---|
| `godotui/` | Godot 4.8 图形前端（用户在编辑器里自己设计 —— 动手前先问） | 就在这个目录（`project.godot` 在这儿） |
| `tui/` | 终端 TUI（Bubble Tea v2，`View()` 纯函数 + 黄金测试） | `../core/internal/tui/`（与后端同一模块，走 HTTP 契约） |
| `telegram/` | Telegram Bot（长轮询 + 白名单门卫） | `../core/internal/tg/`（同上） |

`export/`（APK 等构建产物）不入库。TUI 与 TG bot 共用 `../core/internal/apiclient/`（同一套 HTTP 契约的薄封装）。
