//! 会话的「这一轮在不在跑」——**进程内的一次性事实**，不进库。
//!
//! 后端一重启，就没有任何生成在跑，状态自然回到 `idle`——这是诚实的。库里会留下的
//! 只有**占位消息**（生成前先插进去的那条空助手消息，为的是让前端有个可指认的
//! `message_id`）；进程被杀时它会留着，所以启动时要清一遍
//! （[`crate::store::Store::drop_empty_assistant_messages`]）。
//!
//! 状态按**用户看到什么**分，不按传输方式分（别叫 `get_streaming` 之类——换 SSE /
//! WebSocket / 轮询时那种名字立刻开始骗人）：
//!
//! ```text
//!              POST /messages 或 /resend
//!    idle ──────────────────────────────► pending ──► idle
//!     ▲                                     │  │
//!     │              流式才有 ┌─────────────┘  │ 失败
//!     │                       ▼                ▼
//!     │                  streaming          error ──►（下一轮）pending
//!     └── POST /stop ────────┴────────────────┘
//! ```
//!
//! - `pending`：已发出、**还没有任何可见输出**（非流式期间它一直是 pending）；
//! - `streaming`：有增量在涨（流式落地后才进，非流式永远不进）；
//! - `error`：**上一轮的结局**，不是"进行中"——它挂到下一轮开始为止，前端据此
//!   显示"上次失败了，可重试"。
//!
//! 每个会话同时只有一个这一轮；已经在跑时再发 → HTTP 409（见 `server`）。
//! 登记表里的 `token` 用来分辨"我这一轮"：被 `stop` 顶掉的旧任务回来时令牌对不上，
//! 就不会把过期的结果写进库里。

use std::collections::HashMap;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Mutex;
use std::time::Instant;

use serde::{Deserialize, Serialize};
use uuid::Uuid;

/// 一个会话此刻处在哪一档。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Phase {
    Idle,
    Pending,
    Streaming,
    Error,
}

impl Phase {
    /// "还在跑"：`pending` / `streaming`。`error` 不算——它是上一轮的结局。
    pub fn is_busy(self) -> bool {
        matches!(self, Phase::Pending | Phase::Streaming)
    }
}

/// 对外（HTTP）给的那份状态。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct TurnStatus {
    pub phase: Phase,
    /// 正在生成的那条助手消息（生成前先占位插进去，所以一开始就有 id）。
    #[serde(skip_serializing_if = "Option::is_none")]
    pub message_id: Option<Uuid>,
    /// 这一轮已经等了多久（毫秒）。`error` 态下是上一轮的总耗时。
    pub elapsed_ms: u64,
    /// 已经攒了多少字。非流式是"回到 idle 那一刻一次性到位"；
    /// 流式落地后它就是前端的增量游标。
    pub chars: usize,
    /// 上一轮的结局：失败时的文案。成功、或从没跑过都是 `None`。
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

impl Default for TurnStatus {
    fn default() -> Self {
        Self {
            phase: Phase::Idle,
            message_id: None,
            elapsed_ms: 0,
            chars: 0,
            error: None,
        }
    }
}

struct Entry {
    phase: Phase,
    message_id: Option<Uuid>,
    started: Instant,
    /// 跑完后定格的耗时（`error` 态还看得见"等了 42 秒才失败"）。
    elapsed_ms: u64,
    chars: usize,
    error: Option<String>,
    token: u64,
    handle: Option<tokio::task::JoinHandle<()>>,
}

/// 所有会话的当前轮次。一个进程一份，挂在 `AppState` 上。
#[derive(Default)]
pub struct TurnRegistry {
    inner: Mutex<HashMap<Uuid, Entry>>,
    next_token: AtomicU64,
}

impl TurnRegistry {
    pub fn new() -> Self {
        Self::default()
    }

    /// 某个会话的状态。没登记的（含从没跑过的、跑完很久的）都是 `idle`。
    pub fn status(&self, conversation: Uuid) -> TurnStatus {
        let Ok(map) = self.inner.lock() else {
            return TurnStatus::default();
        };
        let Some(entry) = map.get(&conversation) else {
            return TurnStatus::default();
        };
        TurnStatus {
            phase: entry.phase,
            message_id: entry.message_id,
            elapsed_ms: if entry.phase.is_busy() {
                entry.started.elapsed().as_millis() as u64
            } else {
                entry.elapsed_ms
            },
            chars: entry.chars,
            error: entry.error.clone(),
        }
    }

    /// 登记新的一轮。已经在跑（`pending` / `streaming`）时返回 `Err`——
    /// 调用方据此给 409，别排队（排队会让"我按了发送但什么都没发生"变得难以解释）。
    pub fn begin(&self, conversation: Uuid, message_id: Uuid) -> Result<u64, Busy> {
        let mut map = self.inner.lock().map_err(|_| Busy)?;
        if map.get(&conversation).is_some_and(|e| e.phase.is_busy()) {
            return Err(Busy);
        }
        let token = self.next_token.fetch_add(1, Ordering::Relaxed);
        map.insert(
            conversation,
            Entry {
                phase: Phase::Pending,
                message_id: Some(message_id),
                started: Instant::now(),
                elapsed_ms: 0,
                chars: 0,
                error: None,
                token,
                handle: None,
            },
        );
        Ok(token)
    }

    /// 把后台任务的句柄挂上（`stop` 时用它 `abort`，别让上游白白跑完）。
    pub fn attach(&self, conversation: Uuid, token: u64, handle: tokio::task::JoinHandle<()>) {
        let Ok(mut map) = self.inner.lock() else { return };
        if let Some(entry) = map.get_mut(&conversation) {
            if entry.token == token {
                entry.handle = Some(handle);
            }
        }
    }

    /// 这一轮还是"我的"吗？——被 `stop` 顶掉后旧任务回来时令牌对不上。
    pub fn is_mine(&self, conversation: Uuid, token: u64) -> bool {
        self.inner
            .lock()
            .map(|map| map.get(&conversation).is_some_and(|e| e.token == token))
            .unwrap_or(false)
    }

    /// 收尾。`Ok` 表示这一轮确实是我的、状态已更新；`Err(())` 表示已被 `stop` 顶掉，
    /// 调用方**不许**再往库里写东西。
    pub fn finish(
        &self,
        conversation: Uuid,
        token: u64,
        phase: Phase,
        chars: usize,
        error: Option<String>,
    ) -> Result<(), ()> {
        let Ok(mut map) = self.inner.lock() else {
            return Err(());
        };
        let Some(entry) = map.get_mut(&conversation) else {
            return Err(());
        };
        if entry.token != token {
            return Err(());
        }
        entry.phase = phase;
        entry.chars = chars;
        entry.error = error;
        entry.elapsed_ms = entry.started.elapsed().as_millis() as u64;
        entry.message_id = if phase.is_busy() { entry.message_id } else { None };
        entry.handle = None;
        Ok(())
    }

    /// 中止这一轮。返回占位消息的 id（调用方负责在它还空着的时候删掉它）。
    /// `None` = 本来就没在跑（幂等：重复按"停止"不算错）。
    pub fn stop(&self, conversation: Uuid) -> Option<(Uuid, Option<tokio::task::JoinHandle<()>>)> {
        let Ok(mut map) = self.inner.lock() else {
            return None;
        };
        let entry = map.get_mut(&conversation)?;
        if !entry.phase.is_busy() {
            return None;
        }
        let placeholder = entry.message_id?;
        let handle = entry.handle.take();
        // 留在表里、降级成 idle：这样旧任务回来时令牌对不上，写不进去。
        entry.phase = Phase::Idle;
        entry.message_id = None;
        entry.elapsed_ms = entry.started.elapsed().as_millis() as u64;
        map.remove(&conversation);
        Some((placeholder, handle))
    }
}

/// `begin` 撞上"还在跑"。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Busy;

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn unknown_conversation_is_idle() {
        let turns = TurnRegistry::new();
        let status = turns.status(Uuid::now_v7());
        assert_eq!(status.phase, Phase::Idle);
        assert_eq!(status.message_id, None);
        assert!(!status.phase.is_busy());
    }

    #[test]
    fn busy_conversation_rejects_a_second_turn() {
        let turns = TurnRegistry::new();
        let id = Uuid::now_v7();
        let first = turns.begin(id, Uuid::now_v7()).unwrap();
        assert!(turns.begin(id, Uuid::now_v7()).is_err(), "同一会话不许两轮并行");

        assert_eq!(turns.status(id).phase, Phase::Pending);
        turns.finish(id, first, Phase::Idle, 3, None).unwrap();
        assert_eq!(turns.status(id).phase, Phase::Idle);
        assert_eq!(turns.status(id).chars, 3);
        // 收工之后可以再开一轮
        assert!(turns.begin(id, Uuid::now_v7()).is_ok());
    }

    #[test]
    fn stale_token_after_stop_cannot_write_back() {
        let turns = TurnRegistry::new();
        let id = Uuid::now_v7();
        let placeholder = Uuid::now_v7();
        let token = turns.begin(id, placeholder).unwrap();
        let stopped = turns.stop(id).expect("在跑，能停");
        assert_eq!(stopped.0, placeholder, "stop 要把占位消息交出来");
        assert!(!turns.is_mine(id, token), "旧任务回来时令牌已失效");
        assert!(turns.finish(id, token, Phase::Idle, 999, None).is_err(), "不许再写回状态");
        assert_eq!(turns.status(id).phase, Phase::Idle, "停完就是 idle");
        assert!(turns.stop(id).is_none(), "没在跑时再停是幂等的");
    }

    #[test]
    fn error_keeps_the_elapsed_time_and_the_reason() {
        let turns = TurnRegistry::new();
        let id = Uuid::now_v7();
        let token = turns.begin(id, Uuid::now_v7()).unwrap();
        turns
            .finish(id, token, Phase::Error, 0, Some("上游返回 HTTP 502".to_owned()))
            .unwrap();
        let status = turns.status(id);
        assert_eq!(status.phase, Phase::Error);
        assert_eq!(status.error.as_deref(), Some("上游返回 HTTP 502"));
        assert_eq!(status.message_id, None, "失败的那条占位消息已被删");
        assert!(!status.phase.is_busy(), "error 不是进行中");
        // 下一轮开始时错误就清掉了
        let token = turns.begin(id, Uuid::now_v7()).unwrap();
        assert_eq!(turns.status(id).phase, Phase::Pending);
        assert_eq!(turns.status(id).error, None);
        turns.finish(id, token, Phase::Idle, 1, None).unwrap();
    }

    #[test]
    fn streaming_phase_reports_live_elapsed() {
        let turns = TurnRegistry::new();
        let id = Uuid::now_v7();
        let token = turns.begin(id, Uuid::now_v7()).unwrap();
        turns.finish(id, token, Phase::Streaming, 12, None).unwrap();
        let status = turns.status(id);
        assert_eq!(status.phase, Phase::Streaming);
        assert_eq!(status.chars, 12);
        assert!(status.phase.is_busy());
        assert!(status.message_id.is_some(), "流式时前端要知道在写哪条");
    }
}
