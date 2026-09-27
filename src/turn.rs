//! 会话的「这一轮在不在跑」——**进程内的一次性事实**，不进库。
//!
//! 后端一重启，就没有任何生成在跑，状态自然回到 `idle`——这是诚实的。**库里不会有
//! 生成中的痕迹**：一条回复用它受理时就定好的 id 标识（前端拿它指认"正在生成的那条"），
//! 但要等整段从上游拿到才 INSERT 进库——存档里只放完整的对话。
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
    /// 正在生成的那条回复的 id：受理时就定好并发给了客户端，**这一刻它还没进库**
    /// （拿到整段才 INSERT，用的就是这个 id）。
    #[serde(skip_serializing_if = "Option::is_none")]
    pub message_id: Option<Uuid>,
    /// 这一轮已经等了多久（毫秒）。`error` 态下是上一轮的总耗时。
    pub elapsed_ms: u64,
    /// 已经攒了多少字。非流式是"回到 idle 那一刻一次性到位"；
    /// 流式落地后它就是前端的增量游标。
    pub chars: usize,
    /// **思考**流攒了多少字（推理型模型先吐的那段）。界面用它显示"思考中…"。
    /// 它只服务动画：不进库、不进最终消息。
    pub thinking_chars: usize,
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
            thinking_chars: 0,
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
    /// 这一轮**已收到的正文**。**只服务动画**：客户端按游标读它做逐字效果；它不进库、
    /// 不参与变量与出站计算——那些只认流结束后完整落库的那条消息。下一轮 `begin`
    /// 时随 entry 一起被替换，所以它的生命周期就是"一轮"。
    text: String,
    /// **思考**流（推理型模型先吐的那一大段）。同样只服务动画——它连最终消息都不进，
    /// 纯粹是让界面在"想"的时候别看起来死着。
    thinking: String,
    /// 思考流已收到多少字（游标用）。
    thinking_chars: usize,
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
            thinking_chars: entry.thinking_chars,
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
                text: String::new(),
                thinking: String::new(),
                thinking_chars: 0,
                error: None,
                token,
                handle: None,
            },
        );
        Ok(token)
    }

    /// 收到一段**正文**增量：拼进缓冲区，并把 phase 从 `pending` 抬到 `streaming`（只抬一次）。
    ///
    /// 令牌对不上（这一轮已被 `stop` 顶掉）就直接丢弃——**过期任务的字节不许进缓冲区**。
    /// 返回是否收下了。
    pub fn append_content(&self, conversation: Uuid, token: u64, delta: &str) -> bool {
        self.append(conversation, token, delta, false)
    }

    /// 收到一段**思考**增量（推理型模型先吐的那段）。规则同上，另存一份——
    /// 它**不进最终消息**，只让界面在"想"的时候别看起来死着。
    pub fn append_reasoning(&self, conversation: Uuid, token: u64, delta: &str) -> bool {
        self.append(conversation, token, delta, true)
    }

    fn append(&self, conversation: Uuid, token: u64, delta: &str, reasoning: bool) -> bool {
        let Ok(mut map) = self.inner.lock() else {
            return false;
        };
        let Some(entry) = map.get_mut(&conversation) else {
            return false;
        };
        if entry.token != token {
            return false;
        }
        if reasoning {
            entry.thinking.push_str(delta);
            entry.thinking_chars = entry.thinking.chars().count();
        } else {
            entry.text.push_str(delta);
            entry.chars = entry.text.chars().count();
        }
        if entry.phase == Phase::Pending {
            entry.phase = Phase::Streaming;
        }
        true
    }

    /// **游标读**（两条流各一条游标）：正文与思考，从各自的 `from` 起各取一段。
    ///
    /// 不消费、不清空——所以多个客户端（egui + Godot）与断线重连各拿各的，互不偷。
    /// 结束后仍可补拉尾巴（`done = true`），界面用它把最后一个字接上。
    pub fn stream_since(
        &self,
        conversation: Uuid,
        from: usize,
        think_from: usize,
    ) -> StreamSlice {
        let empty = |from: usize, think_from: usize| StreamSlice {
            text: String::new(),
            next: from,
            thinking: String::new(),
            think_next: think_from,
            done: true,
        };
        let Ok(map) = self.inner.lock() else {
            return empty(from, think_from);
        };
        let Some(entry) = map.get(&conversation) else {
            return empty(from, think_from);
        };
        let done = !entry.phase.is_busy();
        let slice = |value: &str, total: usize, from: usize| -> (String, usize) {
            if from >= total {
                (String::new(), total)
            } else {
                (value.chars().skip(from).collect(), total)
            }
        };
        let (text, next) = slice(&entry.text, entry.chars, from);
        let (thinking, think_next) = slice(&entry.thinking, entry.thinking_chars, think_from);
        StreamSlice {
            text,
            next,
            thinking,
            think_next,
            done,
        }
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

    /// 这一轮已收到的**思考**全文（留档用）。思考与正文一样，只在本轮有效。
    pub fn thinking_of(&self, conversation: Uuid) -> String {
        self.inner
            .lock()
            .ok()
            .and_then(|map| map.get(&conversation).map(|entry| entry.thinking.clone()))
            .unwrap_or_default()
    }

    /// 中止这一轮。
    ///
    /// - `None` = 本来就没在跑（幂等：重复按"停止"不算错）；
    /// - `Some(handle)` = 原来在跑，`handle` 是要不要 `abort` 的那个后台任务
    ///   （任务还没挂上来时是 `None`——登记与 spawn 之间有个极窄的窗口）。
    ///
    /// **没有要清理的库内痕迹**：生成中的那条回复只在内存里（文本在任务手里），
    /// 落库发生在拿到整段之后。
    pub fn stop(
        &self,
        conversation: Uuid,
    ) -> Option<Option<tokio::task::JoinHandle<()>>> {
        let mut map = self.inner.lock().ok()?;
        let entry = map.get_mut(&conversation)?;
        if !entry.phase.is_busy() {
            return None;
        }
        let handle = entry.handle.take();
        // 摘掉登记：旧任务回来时令牌对不上，就不会把过期结果写进库。
        map.remove(&conversation);
        Some(handle)
    }
}

/// `begin` 撞上"还在跑"。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Busy;

/// 一次游标读的结果：正文与思考各一段 + 各自的新游标 + 这一轮是否已结束。
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
pub struct StreamSlice {
    pub text: String,
    pub next: usize,
    pub thinking: String,
    pub think_next: usize,
    pub done: bool,
}

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
        assert!(turns.stop(id).is_some(), "在跑，能停");
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

    /// 增量进缓冲区、phase 抬到 streaming；游标读**不消费**，多个客户端各拿各的。
    /// 思考与正文分开两条游标，互不干扰。
    #[test]
    fn appending_promotes_pending_to_streaming_and_cursor_reads_dont_consume() {
        let turns = TurnRegistry::new();
        let id = Uuid::now_v7();
        let token = turns.begin(id, Uuid::now_v7()).unwrap();

        assert!(turns.append_reasoning(id, token, "先想"));
        let status = turns.status(id);
        assert_eq!(status.phase, Phase::Streaming, "首个增量把 pending 抬成 streaming");
        assert_eq!(status.thinking_chars, 2);
        assert_eq!(status.chars, 0, "思考不算正文");

        assert!(turns.append_content(id, token, "你好"));
        assert!(turns.append_content(id, token, "，世界"));
        assert_eq!(turns.status(id).chars, 5);

        let all = "你好，世界".to_owned();
        let slice = turns.stream_since(id, 0, 0);
        assert_eq!(slice.text, all);
        assert_eq!(slice.next, 5);
        assert_eq!(slice.thinking, "先想");
        assert_eq!(slice.think_next, 2);
        assert!(!slice.done);

        assert_eq!(turns.stream_since(id, 2, 2).text, "，世界");
        assert_eq!(
            turns.stream_since(id, 2, 2).text,
            "，世界",
            "游标读不消费：第二个客户端还能拿到同一段"
        );
        assert_eq!(turns.stream_since(id, 5, 2).text, "");
        assert_eq!(turns.stream_since(id, 5, 2).think_next, 2);

        assert!(!turns.append_content(id, token + 1, "伪造"), "过期令牌不许写进缓冲区");
        assert!(!turns.append_reasoning(id, token + 1, "伪造"));

        turns.finish(id, token, Phase::Idle, 5, None).unwrap();
        let slice = turns.stream_since(id, 0, 0);
        assert_eq!(slice.text, all, "结束后仍能补拉尾巴（界面用它接上最后一个字）");
        assert!(slice.done, "done = 这一轮已结束");
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
