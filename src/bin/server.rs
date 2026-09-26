//! HTTP 服务入口：读配置 → 开库 → 起 axum。
//!
//! 目录：`config/`（用户手写 jsonc，程序不隐式改写）与 `data/`（SQLite）。
//! 可用 `MICROCHAT_CONFIG_DIR` / `MICROCHAT_DATA_DIR` 覆盖。

use std::net::SocketAddr;

use microchat::config::{Config, Paths};
use microchat::{server, store::Store};

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let paths = Paths::default();
    let config = Config::load(&paths.config_json())?;

    let db_path = paths.database();
    if let Some(dir) = db_path.parent() {
        std::fs::create_dir_all(dir)?;
    }
    let mut store = Store::open(&db_path)?;
    // 生成前会先插一条**空的占位助手消息**（好让前端有个 message_id 可指认）。
    // 进程被杀时它会留在库里，开服前清一遍——正常回复不可能为空（上游回空算错误）。
    let dropped = store.drop_empty_assistant_messages()?;
    if dropped > 0 {
        eprintln!("清理了 {dropped} 条空的占位助手消息（上次没跑完的生成）");
    }

    let addr = SocketAddr::new(config.server.host.parse()?, config.server.port);
    let app = server::router(server::AppState::new(store, &config, paths.clone()));
    let listener = tokio::net::TcpListener::bind(addr).await?;

    eprintln!(
        "microchat server: http://{addr} (db: {}, config dir: {})",
        db_path.display(),
        paths.config_dir.display()
    );
    axum::serve(listener, app).await?;
    Ok(())
}
