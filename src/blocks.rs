//! 对话块（chat block）：压缩的**最小单位**（§16 §17）。
//!
//! > **一个 user 输入块，加紧随其后的 assistant 输出块，算一个对话块。**
//! > 精确成一条规则：**在 `assistant → user` 的交界处切块**
//! > （开头、以及任何"非 assistant 之后"的 user 消息，都开一个新块）。
//!
//! ```text
//! U1 A1 | U2 U3 A3 | U4 A4 A5
//! ```
//! - `U2 U3` 之间没有回复 ⇒ 同块（视作同一个用户输入）；
//! - `U4 A4 A5` 一条输入两条输出（"继续写"）⇒ 同块。
//!
//! **四条硬规矩**：
//! 1. 块**绝不被劈开** —— 压缩只在块边界上停；
//! 2. **最后那个"开着的块"永不压**（还在生成/刚失败待重发/用户刚说话还没答 —— 都没闭合）；
//! 3. 块是**推导**出来的：库里没有行、没有 id（要指一个块就用它两端的消息 id）；
//! 4. **块是压缩单位，不是文本合并** —— `U2 U3` 仍是两条消息两份原文，别把正文拼起来。
//!
//! 块边界同时是**状态的天然检查点**：压缩提示词里那两个前后状态就算在块边界上。

use uuid::Uuid;

use crate::model::{Message, Role};

/// 当前路径上的一个对话块。
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Block {
    /// 仅用于显示与计数（不入库）。
    pub index: usize,
    /// 块内第一条消息（稳定锚点）。
    pub first: Uuid,
    /// 块内最后一条（稳定锚点）。
    pub last: Uuid,
    /// 含几条消息。
    pub messages: usize,
    /// 是否已闭合。**最后一个"开着的块"永不压**（§16 规矩 2）。
    pub closed: bool,
}

impl Block {
    /// 这个块覆盖的消息条数（显示用；与 `messages` 同义，读起来顺一点）。
    pub fn len(&self) -> usize {
        self.messages
    }
}

/// 按 `assistant → user` 交界把当前路径切成块。
///
/// **最后一块永远是"开着的"** —— 用户刚说完还没答、或在等生成：它不算可压的单位。
/// （空路径 ⇒ 空结果；只有用户消息 ⇒ 一个开着块。）
pub fn blocks_of_path(path: &[Message]) -> Vec<Block> {
    let mut blocks: Vec<Block> = Vec::new();
    let mut current: Vec<&Message> = Vec::new();

    for message in path {
        // 交界：前面是 assistant、这条是 user ⇒ 在这里切一刀（这块到此闭合）
        let boundary = message.role == Role::User
            && current
                .last()
                .is_some_and(|previous| previous.role == Role::Assistant);
        if boundary {
            blocks.push(finish(&current, false));
            current.clear();
        }
        current.push(message);
    }
    if !current.is_empty() {
        blocks.push(finish(&current, true));
    }
    blocks
}

fn finish(chunk: &[&Message], open: bool) -> Block {
    let first = chunk.first().expect("块非空").id;
    let last = chunk.last().expect("块非空").id;
    Block {
        index: 0, // 由调用方 `blocks_of_path` 统一编号
        first,
        last,
        messages: chunk.len(),
        closed: !open,
    }
}

/// 给块编号（从 1 开始，方便显示成"第 3–5 块"）。
pub fn numbered(mut blocks: Vec<Block>) -> Vec<Block> {
    for (index, block) in blocks.iter_mut().enumerate() {
        block.index = index + 1;
    }
    blocks
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::Role;

    fn path(roles: &[Role]) -> Vec<Message> {
        let conversation = Uuid::now_v7();
        let mut parent = None;
        roles
            .iter()
            .enumerate()
            .map(|(index, role)| {
                let message = Message {
                    id: Uuid::now_v7(),
                    conversation_id: conversation,
                    role: *role,
                    content: format!("第 {index} 条"),
                    reasoning: None,
                    reasoning_ms: None,
                    duration_ms: None,
                    usage: None,
                    parent_id: parent,
                    summary_id: None,
                    created_at: 0,
                };
                parent = Some(message.id);
                message
            })
            .collect()
    }

    /// 三个例子（§16 的原文）：`U2 U3` 同块、`U4 A4 A5` 同块、最后那个块开着。
    #[test]
    fn blocks_follow_the_assistant_to_user_boundary() {
        use Role::{Assistant as A, User as U};
        let blocks = numbered(blocks_of_path(&path(&[U, A, U, U, A, U, A, A])));
        assert_eq!(blocks.len(), 3, "U1A1 | U2U3A3 | U4A4A5");
        assert_eq!(blocks[0].messages, 2, "U1 A1");
        assert_eq!(blocks[1].messages, 3, "U2 U3 A3 —— 两条用户输入同块");
        assert_eq!(blocks[2].messages, 3, "U4 A4 A5 —— 续写同块");
        assert!(blocks[0].closed && blocks[1].closed);
        assert!(!blocks[2].closed, "最后那个块开着：永不压");
        assert_eq!(blocks[2].index, 3, "编号从 1 起");
    }

    /// 只有用户消息 ⇒ 一个开着块（还没答）；空路径 ⇒ 空。
    #[test]
    fn open_and_empty_paths() {
        use Role::{Assistant as A, User as U};
        let blocks = blocks_of_path(&path(&[U]));
        assert_eq!(blocks.len(), 1);
        assert!(!blocks[0].closed, "用户刚说完还没答");

        let blocks = blocks_of_path(&path(&[U, A]));
        assert_eq!(blocks.len(), 1);
        assert!(!blocks[0].closed, "没有下一个 user ⇒ 最后这块仍开着");

        assert!(blocks_of_path(&[]).is_empty());
    }

    /// 块只是个**视图**：它不合并正文，两端的锚点就是块里首尾消息的 id。
    #[test]
    fn blocks_are_views_not_copies() {
        use Role::{Assistant as A, User as U};
        let messages = path(&[U, U, A]);
        let blocks = blocks_of_path(&messages);
        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].first, messages[0].id);
        assert_eq!(blocks[0].last, messages[2].id);
        assert_eq!(blocks[0].messages, 3, "三条消息各自还在，没被拼成一条");
        assert_eq!(blocks[0].len(), 3);
    }
}
