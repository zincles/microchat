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
    let config = Config::load(&paths.config_jsonc())?;

    let db_path = paths.database();
    if let Some(dir) = db_path.parent() {
        std::fs::create_dir_all(dir)?;
    }
    let store = Store::open(&db_path)?;

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
