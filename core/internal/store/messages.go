package store

import (
	"database/sql"
	"encoding/json"

	"microchat/internal/model"
)

// messageColumns：与 Rust 版 list_all_messages 的 SELECT 逐字相同。
const messageColumns = "id, session_id, role, content, parent_id, created_at, " +
	"reasoning, reasoning_ms, duration_ms, usage, summary_id"

func scanMessage(row scanner) (model.Message, error) {
	var message model.Message
	var parentID, reasoning, summaryID sql.NullString
	var usage []byte
	var reasoningMS, durationMS sql.NullInt64
	err := row.Scan(
		&message.ID, &message.SessionID, &message.Role, &message.Content,
		&parentID, &message.CreatedAt,
		&reasoning, &reasoningMS, &durationMS, &usage, &summaryID,
	)
	if err != nil {
		return message, err
	}
	if parentID.Valid {
		message.ParentID = &parentID.String
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
func (s *Store) allMessages(sessionID string) ([]model.Message, error) {
	rows, err := s.db.Query("SELECT "+messageColumns+
		" FROM messages WHERE session_id = ?1 ORDER BY rowid", sessionID)
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

// ListMessages：**当前路径**（从 current_leaf 沿 parent_id 回溯到根，正序返回）。
// 注意：不是"最后 N 条"，是树上这一条链 —— 切分支换来换去的都是它。
func (s *Store) ListMessages(sessionID string) ([]model.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.allMessages(sessionID)
	if err != nil {
		return nil, err
	}
	session, err := s.session(sessionID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return []model.Message{}, nil // 会话不存在 ⇒ 空列表（Rust 版就是这样，不是 404）
	}
	return pathFrom(all, session.CurrentLeaf), nil
}

// pathFrom：照搬 Rust 版的回溯（含防环保险）。
func pathFrom(all []model.Message, leaf *string) []model.Message {
	byID := make(map[string]*model.Message, len(all))
	for i := range all {
		byID[all[i].ID] = &all[i]
	}
	path := []model.Message{}
	cursor := leaf
	for cursor != nil {
		message, ok := byID[*cursor]
		if !ok {
			break // 指针悬空当到根（导入/删过就可能有）
		}
		path = append(path, *message)
		cursor = message.ParentID
		if len(path) > len(all) {
			break // 防环
		}
	}
	// 反转 = 从根到叶
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}

// BranchInfo：每条消息在同龄兄弟里第几/共几（界面上的「‹ 2/3 ›」）。
// 分组按 parent_id（**整棵树**，不只是当前路径），组内顺序 = 生成先后。
func (s *Store) BranchInfo(sessionID string) (map[string]model.BranchInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.allMessages(sessionID)
	if err != nil {
		return nil, err
	}
	groups := map[string][]string{}
	order := []string{}
	for _, message := range all {
		key := ""
		if message.ParentID != nil {
			key = *message.ParentID
		}
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], message.ID)
	}
	info := map[string]model.BranchInfo{}
	for _, key := range order {
		siblings := groups[key]
		for index, id := range siblings {
			info[id] = model.BranchInfo{Index: index + 1, Total: len(siblings), Siblings: siblings}
		}
	}
	return info, nil
}

// UpdateMessage：改正文 = **重写存档**（世界状态随之现演，仓库里没有任何派生表要同步）。
//
// 与 PATCH /sessions 不同，这一个**要动 updated_at**（改了一句话 = 这篇对话变了）——
// 与 Rust 版逐字一致（差这一处，列表的排序就会不一样）。
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
	// 改的正文可能正被某条摘要覆盖 ⇒ 那条摘要（含各级祖先）标过期（§18）
	if err := markSummaryChainDirty(tx, messageID); err != nil {
		return model.Message{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Message{}, err
	}
	return updated, nil
}

// DeleteMessage：删一条**连它整棵子树**（树上的删除只允许这一种）。
func (s *Store) DeleteMessage(sessionID, messageID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.allMessages(sessionID)
	if err != nil {
		return 0, err
	}
	var parent *string
	found := false
	for index := range all {
		if all[index].ID == messageID {
			parent, found = all[index].ParentID, true
			break
		}
	}
	if !found {
		return 0, ErrNotFound
	}
	return s.deleteSubtrees(sessionID, []string{messageID}, parent)
}

// DeleteSiblings：删掉某条消息的**所有兄弟**（连同各自的子树）——「删除全部」。
// 删完 leaf 自然退到它们的父亲（= 那句用户消息）上，接着就能重新生成。
func (s *Store) DeleteSiblings(sessionID, messageID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.allMessages(sessionID)
	if err != nil {
		return 0, err
	}
	var parent *string
	found := false
	for index := range all {
		if all[index].ID == messageID {
			parent, found = all[index].ParentID, true
			break
		}
	}
	if !found {
		return 0, ErrNotFound
	}
	siblings := []string{}
	for index := range all {
		if sameOptional(all[index].ParentID, parent) {
			siblings = append(siblings, all[index].ID)
		}
	}
	return s.deleteSubtrees(sessionID, siblings, parent)
}

func sameOptional(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// deleteSubtrees：删一批消息**连各自整棵子树**，并按规则修 `current_leaf`。**调用方持锁**。
//
// 返回一共删了几条。`parent` 是这批量共同的上文，用来决定 leaf 退到哪儿。
func (s *Store) deleteSubtrees(sessionID string, roots []string, parent *string) (int, error) {
	all, err := s.allMessages(sessionID)
	if err != nil {
		return 0, err
	}
	if len(roots) == 0 {
		return 0, nil
	}
	doomed := map[string]bool{}
	for _, root := range roots {
		exists := false
		for index := range all {
			if all[index].ID == root {
				exists = true
				break
			}
		}
		if !exists {
			return 0, ErrNotFound
		}
		doomed[root] = true
	}
	// 子树 = 根们 + 所有后代。`all` 按 rowid（父亲一定排在孩子前面）走一遍就够。
	for index := range all {
		parentID := all[index].ParentID
		if parentID != nil && doomed[*parentID] {
			doomed[all[index].ID] = true
		}
	}
	count := len(doomed)

	session, err := s.session(sessionID)
	if err != nil {
		return 0, err
	}
	var leaf *string
	if session != nil {
		leaf = session.CurrentLeaf
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	// **先标后删**：删掉消息它指向摘要的指针就没了，那时再想标就找不到人（§18）
	for id := range doomed {
		if err := markSummaryChainDirty(tx, id); err != nil {
			return 0, err
		}
	}
	for id := range doomed {
		if _, err := tx.Exec("DELETE FROM messages WHERE id = ?1 AND session_id = ?2",
			id, sessionID); err != nil {
			return 0, err
		}
	}
	if leaf != nil && doomed[*leaf] {
		// 退到哪里：优先"上文下**还活着的最新一个孩子**"——删单条时就是它的上一条兄弟。
		// 只退到父亲是不够的：界面上会看到"整条分支都没了"（其实兄弟都在树上）。
		var fallback *string
		for index := range all {
			if sameOptional(all[index].ParentID, parent) && !doomed[all[index].ID] {
				id := all[index].ID
				fallback = &id // 越靠后越新（all 按 rowid 排）
			}
		}
		if fallback == nil {
			fallback = parent
		}
		if _, err := tx.Exec("UPDATE sessions SET current_leaf = ?1 WHERE id = ?2",
			fallback, sessionID); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec("UPDATE sessions SET updated_at = ?1 WHERE id = ?2",
		nowMS(), sessionID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}
