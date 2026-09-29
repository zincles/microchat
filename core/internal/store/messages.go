package store

import (
	"database/sql"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"microchat/internal/model"
)

// messageColumns：这条会话的消息列（**没有 parent_message_id**：线性会话没有父指针）。
const messageColumns = "id, session_id, role, content, created_at, " +
	"reasoning, reasoning_ms, duration_ms, usage, summary_id"

func scanMessage(row scanner) (model.Message, error) {
	var message model.Message
	var reasoning, summaryID sql.NullString
	var usage []byte
	var reasoningMS, durationMS sql.NullInt64
	err := row.Scan(
		&message.ID, &message.SessionID, &message.Role, &message.Content,
		&message.CreatedAt,
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
	return messages, rows.Err()
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

// InsertMessage：把**一整条**消息落库（用户那句，或一条已经拿到整段的回复）。
//
// 地址由调用方铸好（**受理那一刻**就发给客户端；顺序也由它定）⇒ 这里不自作主张生成 id。
// 与 copy 的插入同一条路：同一个事务里插消息 + 把会话的 `updated_at` 往前推
// （改了内容就该在列表最上面）。
func (s *Store) InsertMessage(message model.Message) (model.Message, error) {
	if message.CreatedAt == 0 {
		message.CreatedAt = nowMS()
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return model.Message{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(
		`INSERT INTO messages
		   (id, session_id, role, content, created_at, reasoning, reasoning_ms, duration_ms, usage, summary_id)
		 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, NULL)`,
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

// UpdateMessage：改正文 = **重写存档**（世界状态随之现演，仓库里没有任何派生表要同步）。
//
// 就地替换 ⇒ `message_id` 不变 ⇒ 摘要的覆盖区间仍然成立（只标 dirty、照用；级联只归删除）。
// 与 PATCH /sessions 不同，这一个**要动 updated_at**（改了一句话 = 这篇对话变了）。
func (s *Store) UpdateMessage(sessionID, messageID, content string) (model.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return model.Message{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := execTouchLocked(tx,
		"UPDATE messages SET content = ?1 WHERE id = ?2 AND session_id = ?3",
		content, messageID, sessionID); err != nil {
		return model.Message{}, err
	}
	updated, err := scanMessage(tx.QueryRow("SELECT "+messageColumns+" FROM messages WHERE id = ?1", messageID))
	if err != nil {
		return model.Message{}, err
	}
	if _, err := tx.Exec("UPDATE sessions SET updated_at = ?1 WHERE id = ?2",
		nowMS(), sessionID); err != nil {
		return model.Message{}, err
	}
	// 改的正文可能正被某条摘要覆盖 ⇒ 那条摘要（含各级祖先）标过期
	if err := markSummaryChainDirty(tx, messageID); err != nil {
		return model.Message{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Message{}, err
	}
	return updated, nil
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
