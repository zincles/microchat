package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"microchat/internal/model"
)

// messageColumns：这条会话的消息列（**没有 parent_message_id**：线性会话没有父指针）。
const messageColumns = "id, session_id, role, content, created_at, updated_at, " +
	"reasoning, reasoning_ms, duration_ms, usage, summary_id"

func scanMessage(row scanner) (model.Message, error) {
	var message model.Message
	var reasoning, summaryID sql.NullString
	var usage []byte
	var reasoningMS, durationMS sql.NullInt64
	err := row.Scan(
		&message.ID, &message.SessionID, &message.Role, &message.Content,
		&message.CreatedAt, &message.UpdatedAt,
		&reasoning, &reasoningMS, &durationMS, &usage, &summaryID,
	)
	if err != nil {
		return message, err
	}
	if reasoning.Valid {
		message.Reasoning = reasoning.String
	}
	if reasoningMS.Valid {
		message.ReasoningMS = &reasoningMS.Int64
	}
	if durationMS.Valid {
		message.DurationMS = &durationMS.Int64
	}
	if len(usage) > 0 {
		message.Usage = json.RawMessage(usage)
	}
	if summaryID.Valid {
		message.SummaryID = &summaryID.String
	}
	return message, nil
}

// IndexMessages：给一条会话的消息**按顺序**编上序号（1-based）—— **这是 `idx` 唯一算处**。
//
// 位置 = 按 `id` 排序后的下标（`allMessages` 的 `ORDER BY id` 保证顺序 ⇒ `messages_by_session` 索引白拿）。
// 线性会话**只删后缀**（不留洞）、不往中间插、编辑/重摇不改 id ⇒ **一旦分配就永不改变**。
// `idx` **不落库** ✗：没有那一列，也没有迁移（存它 = 第二个真相来源，迟早与顺序打架）；
// `0` 留给**合成的系统提示词**（它不是消息）。
//
// 幂等：对已经编过号的一段再编一遍，值一模一样（`chat.assemble` 把"待发那句"追在末尾后就再走一遍，
// 于是那条拿到的正是"下一条"的号 —— **下一个序号也仍然只有这一处算**）。
func IndexMessages(messages []model.Message) {
	for index := range messages {
		messages[index].Idx = index + 1
	}
}

// allMessages：**调用方持锁**（内部版，事务与复合操作要用）。
//
// 顺序 = `id`（UUIDv7：受理时铸、就地替换不改 id ⇒ 时间序稳），走 `messages_by_session` 索引。
// **别按 rowid 排** —— rowid 是 SQLite 的实现细节，复制 / VACUUM 之后没有意义。
func (s *Store) allMessages(sessionID string) ([]model.Message, error) {
	rows, err := s.db.Query("SELECT "+messageColumns+
		" FROM messages WHERE session_id = ?1 ORDER BY id", sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := []model.Message{}
	for rows.Next() {
		message, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	IndexMessages(messages)
	return messages, nil
}

// ListMessages：这条会话的**全部消息**，按 id 升序。
//
// 线性会话里"整条会话"就是它 —— 没有当前路径、没有兄弟，所以不是一个子集。
// 会话不存在 ⇒ 空列表（口径与旧版一致：不是 404）。
func (s *Store) ListMessages(sessionID string) ([]model.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.allMessages(sessionID)
}

// MessageCounts：每条会话有几条消息（**一条 SQL，不是 N 次查询**）。
//
// `GET /sessions` 的列表项靠它带上 `messages` ⇒ 客户端判定"空会话"不必逐条去拉消息
// （会话一多就是 N 次请求）。没有消息的会话**不出现在 map 里**（读出来是 0，正是要的）。
func (s *Store) MessageCounts() (map[string]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT session_id, COUNT(*) FROM messages GROUP BY session_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var sessionID string
		var count int
		if err := rows.Scan(&sessionID, &count); err != nil {
			return nil, err
		}
		counts[sessionID] = count
	}
	return counts, rows.Err()
}

// LastMessageID：这条会话**最后一条消息**的 id（"最新一条" = `ORDER BY id DESC LIMIT 1`，白拿索引）。
//
// 空会话 ⇒ 空串（不是错误）。重摇用它核对"候选说的那条尾巴还是不是那条"
// —— 被 `/cut` 删掉、或已经换了尾巴 ⇒ 候选自然消失（口径见 `DEFINE.md`）。
func (s *Store) LastMessageID(sessionID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var id string
	err := s.db.QueryRow("SELECT id FROM messages WHERE session_id = ?1 ORDER BY id DESC LIMIT 1",
		sessionID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

// MessageWindow：`GET /sessions/{session_id}/messages` 的三个查询参数（**在这里收口**）。
//
// 零值 = 没给。**参数的合法性在 server 判**（那是参数层的事，400 也在那儿）；这里只管语义。
type MessageWindow struct {
	Last    int // >0 ⇒ 取**尾部** N 条
	FromIdx int // >0 ⇒ 区间起点（含）；0 ⇒ 从头
	ToIdx   int // >0 ⇒ 区间终点（含）；0 ⇒ 到末尾
}

// ListMessagesWindow：按序号窗口取消息（**仍按 id 升序** —— 切一段出来，不重排）。
//
// 区间的两端缺一就补默认（起点 ⇒ 1、终点 ⇒ 末尾）："只看第 5 条往后"说得出口。
// **越界不算错**（分页的常见语义）：要 5..99 而只有 3 条 ⇒ 给现有的那几条；
// 起点落在末尾之后 ⇒ **空列表**（不是 404：会话没有"第几页不存在"这回事）。
func (s *Store) ListMessagesWindow(sessionID string, window MessageWindow) ([]model.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	messages, err := s.allMessages(sessionID)
	if err != nil {
		return nil, err
	}
	return windowSlice(messages, window), nil
}

// windowSlice：**唯一一处**按窗口切消息 —— 切出来的仍是 id 升序、仍是同一条会话里的 `idx`。
func windowSlice(messages []model.Message, window MessageWindow) []model.Message {
	if window.Last > 0 {
		if window.Last >= len(messages) {
			return messages
		}
		return messages[len(messages)-window.Last:]
	}
	if window.FromIdx <= 0 && window.ToIdx <= 0 {
		return messages // 一个都没给 = 全部（语义不变）
	}
	begin := max(window.FromIdx, 1)
	end := window.ToIdx
	if end <= 0 || end > len(messages) {
		end = len(messages) // 越界 ⇒ 给现有的那几条
	}
	if begin > end {
		return []model.Message{} // 起点在末尾之后 ⇒ 空（仍是数组，不是 null）
	}
	return messages[begin-1 : end]
}

// InsertMessage：把**一整条**消息落库（用户那句，或一条已经拿到整段的回复）。
//
// 地址由调用方铸好（**受理那一刻**就发给客户端；顺序也由它定）⇒ 这里不自作主张生成 id。
// 与 copy 的插入同一条路：同一个事务里插消息 + 把会话的 `updated_at` 往前推
// （改了内容就该在列表最上面）。
func (s *Store) InsertMessage(message model.Message) (model.Message, error) {
	if message.CreatedAt == 0 {
		message.CreatedAt = nowMS()
	}
	// 新建时 updated_at = created_at（没改过的两者相等）。
	message.UpdatedAt = message.CreatedAt
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return model.Message{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(
		`INSERT INTO messages
		   (id, session_id, role, content, created_at, updated_at, reasoning, reasoning_ms, duration_ms, usage, summary_id)
		 VALUES (?1, ?2, ?3, ?4, ?5, ?5, ?6, ?7, ?8, ?9, NULL)`,
		message.ID, message.SessionID, string(message.Role), message.Content, message.CreatedAt,
		message.Reasoning, message.ReasoningMS, message.DurationMS, usageArg(message.Usage)); err != nil {
		return model.Message{}, err
	}
	if err := execTouchLocked(tx, "UPDATE sessions SET updated_at = ?1 WHERE id = ?2",
		message.CreatedAt, message.SessionID); err != nil {
		return model.Message{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Message{}, err
	}
	return message, nil
}

// MessageEdit：改一条消息的字段（nil = 不动）。
//
// Content：正文（存档本体，`<state>` 块原样留着）；Reasoning：思考原文（"" = 清掉）。
// 至少给一个 ⇒ 两个都 nil = InvalidError（与 PATCH 那层的 422 对齐）。
type MessageEdit struct {
	Content   *string
	Reasoning *string
}

// UpdateMessage：改**任意**一条消息的正文 / 思考 = **重写存档**（世界状态随之现演，仓库里没有任何派生表要同步）。
//
// 就地替换 ⇒ `message_id` 不变 ⇒ 摘要的覆盖区间仍然成立（只标 dirty、照用；级联只归删除）。
// 与 PATCH /sessions 不同，这一个**要动 updated_at**（改了一句话 = 这篇对话变了）。
// 只改思考也标 dirty + 推 `updated_at`：从简，不为"动没动正文"分两路（标脏是保守的，装配照用不误）。
func (s *Store) UpdateMessage(sessionID, messageID string, edit MessageEdit) (model.Message, error) {
	if edit.Content == nil && edit.Reasoning == nil {
		return model.Message{}, InvalidError("改消息：content 与 reasoning 至少给一个（两个都不给等于没改）")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return model.Message{}, err
	}
	defer func() { _ = tx.Rollback() }()
	now := nowMS()
	sets := []string{"updated_at = ?"}
	args := []any{now}
	if edit.Content != nil {
		sets = append(sets, "content = ?")
		args = append(args, *edit.Content)
	}
	if edit.Reasoning != nil {
		sets = append(sets, "reasoning = ?")
		args = append(args, *edit.Reasoning)
	}
	args = append(args, messageID, sessionID)
	if err := execTouchLocked(tx,
		"UPDATE messages SET "+strings.Join(sets, ", ")+" WHERE id = ? AND session_id = ?",
		args...); err != nil {
		return model.Message{}, err
	}
	return finishMessageEditLocked(tx, sessionID, messageID, now)
}

// finishMessageEditLocked：改消息的收尾 —— 回读改后那条 + 推会话 updated_at + 标脏覆盖它的摘要链。**调用方持锁 + 持事务**。
func finishMessageEditLocked(tx *sql.Tx, sessionID, messageID string, now int64) (model.Message, error) {
	updated, err := scanMessage(tx.QueryRow("SELECT "+messageColumns+" FROM messages WHERE id = ?1", messageID))
	if err != nil {
		return model.Message{}, err
	}
	if _, err := tx.Exec("UPDATE sessions SET updated_at = ?1 WHERE id = ?2",
		now, sessionID); err != nil {
		return model.Message{}, err
	}
	// 改的消息可能正被某条摘要覆盖 ⇒ 那条摘要（含各级祖先）标过期
	if err := markSummaryChainDirty(tx, messageID); err != nil {
		return model.Message{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Message{}, err
	}
	return updated, nil
}

// MessageMeta：一条消息上那组**只服务显示**的附带信息（usage / 耗时 / 思考）。
//
// 单拎出来是因为"改正文"有两种：只换正文（编辑 ⇒ 附带信息原样留着），
// 以及**连同附带信息一起换**（重摇接受 = 你选中的那一版连它的用量/思考一起生效）。
type MessageMeta struct {
	Reasoning   string
	ReasoningMS *int64
	DurationMS  *int64
	Usage       json.RawMessage
}

// ReplaceMessage：**连同附带信息一起换**（重摇接受走它）。
//
// 与 `UpdateMessage` 共用同一处写入（`writeMessageLocked`）⇒ 两条路不会漂移：
// `message_id` **不变**（⇒ 摘要的覆盖区间仍然成立、只标 `dirty`；级联只归删除）、
// `updated_at` 刷成当前、会话的 `updated_at` 跟着往前推。
func (s *Store) ReplaceMessage(sessionID, messageID, content string, meta MessageMeta) (model.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeMessageLocked(sessionID, messageID, content, &meta)
}

// writeMessageLocked：**改一条消息的唯一落点**（编辑 / 重摇接受共用它）。
//
// `meta == nil` ⇒ 只换正文（附带信息一个字都不动：改一句话不该把用量抹掉——
// 那组字段只服务显示，与"这句话说了什么"不是一回事）；非 nil ⇒ 连那四列一起换。
func (s *Store) writeMessageLocked(sessionID, messageID, content string, meta *MessageMeta) (model.Message, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return model.Message{}, err
	}
	defer func() { _ = tx.Rollback() }()

	now := nowMS()
	if meta == nil {
		err = execTouchLocked(tx,
			"UPDATE messages SET content = ?1, updated_at = ?2 WHERE id = ?3 AND session_id = ?4",
			content, now, messageID, sessionID)
	} else {
		err = execTouchLocked(tx,
			"UPDATE messages SET content = ?1, updated_at = ?2, reasoning = ?3, reasoning_ms = ?4, "+
				"duration_ms = ?5, usage = ?6 WHERE id = ?7 AND session_id = ?8",
			content, now, meta.Reasoning, meta.ReasoningMS, meta.DurationMS, usageArg(meta.Usage),
			messageID, sessionID)
	}
	if err != nil {
		return model.Message{}, err
	}
	return finishMessageEditLocked(tx, sessionID, messageID, now)
}

// DeletionPlan：算出"删这条及之后"会动到什么（**只算不动**）。
//
// **一份计算**：预览（GET）与执行（DELETE）走的是它 —— 分成两条路，预览必然与真删漂移。
//
// 语义：删【目标及其之后的全部消息】+【覆盖区间与这些消息**相交**的全部摘要】+【这些摘要的**全部祖先**】，
// 再把幸存者里指向死摘要的指针收进 `Unlinked*`（执行时置空）。
// 被删的永远是**后缀**（线性会话 + 按 id 排序）⇒"区间与后缀相交"等价于"区间的右端落在后缀里"。
func (s *Store) DeletionPlan(sessionID, messageID string) (model.DeletionPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deletionPlan(sessionID, messageID)
}

// deletionPlan：**调用方持锁**（`ApplyDeletion` 的同门；也是"一份计算"的本体）。
func (s *Store) deletionPlan(sessionID, messageID string) (model.DeletionPlan, error) {
	messages, err := s.allMessages(sessionID)
	if err != nil {
		return model.DeletionPlan{}, err
	}
	cut := -1
	for index := range messages {
		if messages[index].ID == messageID {
			cut = index
			break
		}
	}
	if cut < 0 {
		return model.DeletionPlan{}, ErrNotFound
	}
	suffix := messages[cut:]
	doomedMessage := make(map[string]bool, len(suffix))
	for index := range suffix {
		doomedMessage[suffix[index].ID] = true
	}

	summaries, err := s.listSummaries(sessionID)
	if err != nil {
		return model.DeletionPlan{}, err
	}
	byID := make(map[string]model.Summary, len(summaries))
	for index := range summaries {
		byID[summaries[index].ID] = summaries[index]
	}
	// ① 右端落在后缀里的摘要（区间相交 ⇔ 右端在后缀里；后缀是"从某条往后的全部"）
	dead := map[string]bool{}
	stack := []string{}
	for index := range summaries {
		end := summaries[index].EndMessageID
		if end == nil || !doomedMessage[*end] {
			continue
		}
		dead[summaries[index].ID] = true
		stack = append(stack, summaries[index].ID)
	}
	// ② 它们的**全部祖先**：孩子死了 ⇒ 父覆盖的范围也不再成立
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		parent := byID[id].ParentSummaryID
		if parent == nil || dead[*parent] {
			continue // 悬空父指针或已经排过：到此为止
		}
		dead[*parent] = true
		stack = append(stack, *parent)
	}
	// ③ 幸存者里指向死摘要的指针（被删的那些不用管：它们的指针下一句就没了）
	unlinkedMessages := []string{}
	for index := range messages {
		if doomedMessage[messages[index].ID] || messages[index].SummaryID == nil {
			continue
		}
		if dead[*messages[index].SummaryID] {
			unlinkedMessages = append(unlinkedMessages, messages[index].ID)
		}
	}
	unlinkedSummaries := []string{}
	for index := range summaries {
		parent := summaries[index].ParentSummaryID
		if dead[summaries[index].ID] || parent == nil {
			continue
		}
		if dead[*parent] {
			unlinkedSummaries = append(unlinkedSummaries, summaries[index].ID)
		}
	}

	deletedSummaryIDs := make([]string, 0, len(dead))
	for id := range dead {
		deletedSummaryIDs = append(deletedSummaryIDs, id)
	}
	sort.Strings(deletedSummaryIDs) // 确定性（messages 已经按 id 排好，摘要也照 id 排）
	deletedMessageIDs := make([]string, 0, len(suffix))
	for index := range suffix {
		deletedMessageIDs = append(deletedMessageIDs, suffix[index].ID)
	}
	return model.DeletionPlan{
		SessionID:            sessionID,
		DeletedMessageIDs:    deletedMessageIDs,
		DeletedSummaryIDs:    deletedSummaryIDs,
		UnlinkedMessageIDs:   unlinkedMessages,
		UnlinkedSummaryIDs:   unlinkedSummaries,
		LastDeletedMessageID: suffix[len(suffix)-1].ID,
	}, nil
}

// ApplyDeletion：照计划动手 —— 一个事务里 ① 清指针 ② 删摘要 ③ 删消息。
//
// 顺序是刻意的：先清指针（它们指的行下一句就没了）→ 再删摘要 → 最后删消息；
// 摘要先没了，就没有"指针指到已经不存在的东西上"的机会。
func (s *Store) ApplyDeletion(plan model.DeletionPlan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// ① 清指针（幸存者 → 死摘要）
	for _, id := range plan.UnlinkedMessageIDs {
		if _, err := tx.Exec("UPDATE messages SET summary_id = NULL WHERE id = ?1 AND session_id = ?2",
			id, plan.SessionID); err != nil {
			return err
		}
	}
	for _, id := range plan.UnlinkedSummaryIDs {
		if _, err := tx.Exec("UPDATE summaries SET parent_summary_id = NULL WHERE id = ?1 AND session_id = ?2",
			id, plan.SessionID); err != nil {
			return err
		}
	}
	// ② 删摘要：**一条语句删一整批**（父与子必须同批 —— 自引用外键在**语句末尾**才核；
	//    逐条删就会出现"父先没了、孩子还指着它" ⇒ FOREIGN KEY constraint failed —— 实测踩过）。
	if err := deleteBatch(tx, "summaries", plan.SessionID, plan.DeletedSummaryIDs); err != nil {
		return err
	}
	// ③ 删消息（后缀 ⇒ 不留洞 ⇒"最新一条"推得出来，不用额外维护什么）
	if err := deleteBatch(tx, "messages", plan.SessionID, plan.DeletedMessageIDs); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE sessions SET updated_at = ?1 WHERE id = ?2",
		nowMS(), plan.SessionID); err != nil {
		return err
	}
	return tx.Commit()
}

// deleteBatch：一条语句删掉一批 id（**一条语句**很关键：同批里的父与子要靠"语句末尾才核外键"
// 才过得去；也省得为几十条消息开几十条语句）。`table` 只由本包的字面量传进来。
func deleteBatch(tx *sql.Tx, table, sessionID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	marks := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, sessionID)
	for index, id := range ids {
		marks[index] = "?" + strconv.Itoa(index+2) // ?1 留给 session_id
		args = append(args, id)
	}
	_, err := tx.Exec("DELETE FROM "+table+" WHERE session_id = ?1 AND id IN ("+
		strings.Join(marks, ",")+")", args...)
	return err
}
