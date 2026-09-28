//! microchat 前端（egui）的可执行入口。
//!
//! 真正的实现都在 `frontend/` 下：`mod.rs`（状态 + 帧循环 + 事件泵）、`chat.rs`（会话视图）、
//! `settings.rs`（设置六页）、`debug.rs`（调试五页）、`graph.rs`（关系图）、
//! 以及 `client.rs`（HTTP 客户端）、`fonts.rs`、`graph_node.rs`、`frontend_settings.rs`。
//!
//! 这里只做两件事：声明模块、把控制权交出去。

mod frontend;

fn main() -> eframe::Result {
    frontend::run()
}
