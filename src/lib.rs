//! 共享层：领域模型（`model`）与存储（`store`）。
//!
//! 模块名用 `model` 而不是 `core`：`core` 会遮蔽 Rust 内建 crate，日后写
//! `core::fmt` 之类的路径会莫名解析到自己的模块。

pub mod chat;
pub mod config;
pub mod model;
pub mod providers;
pub mod registry;
pub mod server;
pub mod store;
pub mod vars;
