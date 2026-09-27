//! 后台任务登记表：**随时知道这会儿有哪些活在跑**。
//!
//! 一个"任务"不是线程、不是进程、更不是 agent —— 它就是**一次后台作业的登记项**：
//! 一轮生成、一次压缩、一次模型刷新……凡是"发起了、还没结束"的活，都在这儿挂个号。
//!
//! 分工（别造第三份真相）：
//! - **本表**：身份 + 生死（谁、什么时候起、跑完什么结果）；
//! - `turn::TurnRegistry`：**流**的细节（已经流了几个字、thinking 字符数、取消句柄）——
//!   那是"这一轮现在流到哪了"，跟"有哪些活在跑"是两件事，不必也不该合并；
//! - `turn::CompactStatus`：压缩自己的细节（压了几块、产出哪条摘要）—— 给点了按钮的那个客户端轮询用。
//!
//! **收尾靠 RAII**：`begin` 发一张 [`TaskGuard`]，正常路径 `guard.finish("…")`，
//! 忘了收或 panic 了则由 `Drop` 兜底（记成"中断"）。绝不会留下永远 `running` 的僵尸条目。

use std::collections::BTreeMap;
use std::sync::{Arc, Mutex};

use serde::{Deserialize, Serialize};
use uuid::Uuid;

/// 任务的类型。**加一种就在这儿加一个变体**（面板按它分组/上色）。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum TaskKind {
    /// 前台：某个会话的一轮生成。
    Turn,
    /// 后台：把最老的 N 个对话块收成一条摘要。
    Compact,
    /// 后台：从上游拉取某个渠道的模型列表。
    RefreshModels,
}

impl TaskKind {
    pub fn label(self) -> &'static str {
        match self {
            Self::Turn => "生成",
            Self::Compact => "压缩",
            Self::RefreshModels => "刷新模型",
        }
    }
}

/// 一条任务记录。**进程内的事实，不进库** —— 后端一重启就没了，这是诚实的：
/// 那些任务本来也就没了。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct TaskRecord {
    pub id: Uuid,
    pub kind: TaskKind,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub conversation: Option<Uuid>,
    /// 面板上那一行字（发起处写，例如「生成 · 会话 3f2a」）。
    pub title: String,
    pub started_at: i64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub finished_at: Option<i64>,
    /// 收尾时那一句人话（成功 / 失败 / 中断）。
    #[serde(skip_serializing_if = "Option::is_none")]
    pub outcome: Option<String>,
}

impl TaskRecord {
    pub fn running(&self) -> bool {
        self.finished_at.is_none()
    }

    /// 已经跑了多久（正在跑）或一共跑了多久（已结束），毫秒。
    pub fn elapsed_ms(&self, now: i64) -> i64 {
        self.finished_at.unwrap_or(now) - self.started_at
    }
}

/// 给界面看的一屏：正在跑几个 + 一列任务（正在跑的在最前）。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct TaskBoard {
    pub running: usize,
    pub tasks: Vec<TaskRecord>,
    pub now: i64,
}

/// 保留多少条已结束的记录（正在跑的永远都在）。
const KEEP_FINISHED: usize = 60;

#[derive(Clone, Default)]
pub struct TaskRegistry {
    inner: Arc<Mutex<BTreeMap<Uuid, TaskRecord>>>,
}

impl TaskRegistry {
    pub fn new() -> Self {
        Self::default()
    }

    /// 挂个号：拿到 guard 就算"在跑了"。
    pub fn begin(&self, kind: TaskKind, conversation: Option<Uuid>, title: impl Into<String>) -> TaskGuard {
        let id = Uuid::now_v7();
        let record = TaskRecord {
            id,
            kind,
            conversation,
            title: title.into(),
            started_at: now_ms(),
            finished_at: None,
            outcome: None,
        };
        if let Ok(mut tasks) = self.inner.lock() {
            tasks.insert(id, record);
        }
        TaskGuard {
            registry: self.clone(),
            id,
            closed: false,
        }
    }

    /// 正在跑几个。
    pub fn running(&self) -> usize {
        self.inner
            .lock()
            .map(|tasks| tasks.values().filter(|task| task.running()).count())
            .unwrap_or(0)
    }

    /// 一屏：正在跑的在最前，然后按开始时间倒序。
    pub fn board(&self) -> TaskBoard {
        let now = now_ms();
        let mut tasks: Vec<TaskRecord> = self
            .inner
            .lock()
            .map(|tasks| tasks.values().cloned().collect())
            .unwrap_or_default();
        tasks.sort_by(|left, right| {
            right
                .running()
                .cmp(&left.running())
                .then(right.started_at.cmp(&left.started_at))
        });
        let running = tasks.iter().filter(|task| task.running()).count();
        tasks.truncate(running + KEEP_FINISHED);
        TaskBoard {
            running,
            tasks,
            now,
        }
    }

    /// 收尾（guard 正常路径走它；`Drop` 兜底也走它）。
    fn close(&self, id: Uuid, outcome: String) {
        if let Ok(mut tasks) = self.inner.lock() {
            if let Some(record) = tasks.get_mut(&id) {
                record.finished_at = Some(now_ms());
                record.outcome = Some(outcome);
            }
        }
    }
}

/// 任务的凭据：拿着它 = 这个任务在跑；`finish` 收尾，忘了收由 `Drop` 记成中断。
pub struct TaskGuard {
    registry: TaskRegistry,
    id: Uuid,
    closed: bool,
}

impl TaskGuard {
    /// 正常收尾，写一句人话（"完成 · 12 块" / "失败：上游 401"）。
    pub fn finish(mut self, outcome: impl Into<String>) {
        self.close(outcome.into());
    }

    fn close(&mut self, outcome: String) {
        if self.closed {
            return;
        }
        self.closed = true;
        self.registry.close(self.id, outcome);
    }
}

impl Drop for TaskGuard {
    fn drop(&mut self) {
        // 没显式收尾就走这儿：多半是 panic 或中途 return 了。
        self.close("中断（未正常收尾）".to_owned());
    }
}

fn now_ms() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis() as i64)
        .unwrap_or(0)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn begin_and_finish_show_up_on_the_board() {
        let registry = TaskRegistry::new();
        assert_eq!(registry.running(), 0);

        let first = registry.begin(TaskKind::Turn, None, "生成 · 会话 aaa");
        let second = registry.begin(TaskKind::Compact, None, "压缩 10 块");
        assert_eq!(registry.running(), 2);

        first.finish("完成 · 21 字");
        assert_eq!(registry.running(), 1, "收尾一个就少一个");

        let board = registry.board();
        assert_eq!(board.running, 1);
        assert_eq!(board.tasks.len(), 2);
        assert!(board.tasks[0].running(), "正在跑的排最前");
        assert_eq!(board.tasks[0].kind, TaskKind::Compact);
        assert_eq!(board.tasks[1].outcome.as_deref(), Some("完成 · 21 字"));
        assert!(board.tasks[1].finished_at.is_some());
        assert!(board.tasks[1].elapsed_ms(board.now) >= 0);

        second.finish("完成 · 2 块");
        assert_eq!(registry.running(), 0);
    }

    /// 忘了收尾（panic / 中途 return）⇒ `Drop` 记成"中断"，**不许留僵尸**。
    #[test]
    fn dropping_a_guard_without_finishing_marks_it_aborted() {
        let registry = TaskRegistry::new();
        {
            let _guard = registry.begin(TaskKind::RefreshModels, None, "刷新模型 · stub");
            assert_eq!(registry.running(), 1);
        }
        assert_eq!(registry.running(), 0, "离开作用域就该收尾");
        let board = registry.board();
        assert_eq!(board.tasks[0].outcome.as_deref(), Some("中断（未正常收尾）"));
    }

    /// `finish` 之后 `Drop` 不再补一刀（不能把结局改回去）。
    #[test]
    fn finish_then_drop_keeps_the_first_outcome() {
        let registry = TaskRegistry::new();
        let guard = registry.begin(TaskKind::Turn, None, "生成");
        guard.finish("失败：上游 401");
        let board = registry.board();
        assert_eq!(board.tasks[0].outcome.as_deref(), Some("失败：上游 401"));
    }

    /// 已结束的只留最近若干条（正在跑的永远都在）。
    #[test]
    fn finished_records_are_capped_but_running_ones_are_not() {
        let registry = TaskRegistry::new();
        let running = registry.begin(TaskKind::Turn, None, "长跑的那个");
        for index in 0..(KEEP_FINISHED + 10) {
            registry
                .begin(TaskKind::RefreshModels, None, format!("刷新 {index}"))
                .finish("完成");
        }
        let board = registry.board();
        assert_eq!(board.running, 1);
        assert_eq!(board.tasks.len(), KEEP_FINISHED + 1, "正在跑的 + 最近 {} 条", KEEP_FINISHED);
        assert!(board.tasks[0].running());
        running.finish("完成");
    }
}
