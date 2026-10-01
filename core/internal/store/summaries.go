package store

import (
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"

	"microchat/internal/model"
)

// RecordSummary：**压缩的落库口** —— 一个事务里插一行摘要，再把**它的成员**指过去。
//
// 成员是谁由 `summary.SourceKind` 定（**同质**：一条摘要的成员不许混）：
//
//	message ⇒ 回填那一段消息的 `summary_id`（`messages` 上唯一允许被压缩改的那一格）；
//	summary ⇒ 回填那批子摘要的 `parent_summary_id`（合并级）。
//
// **绝不插 / 改 / 删 `messages` 的其它列** —— 正文是存档（`AGENTS.md` 的第一条不变量）。
//
// 顺序：先插摘要（外键指着它），再更新指针。
//
// 消息级的两处重核（调用上游那段时间里别人可能动过库）：
//   - `sourceIDs` 必须**一条不少地**属于这条会话（少一条 = 那段被删/改了，区间不再自洽）；
//   - 那些消息必须**都还没被覆盖**（`summary_id IS NULL`）⇒ 谁先到谁算，后到的**报错**
//     （"不许覆盖已压缩的区间"这条闸在这里落地，不在调用点）。
//
// 合并级**不加 SELECT 预检**：那条 `parent_summary_id IS NULL` 的守卫 UPDATE 就是复核
// （摘要 append-only、删消息级联整链 ⇒ "行数对不上"已经足够说明别人抢过 / 区间变了）。
//
// `sessions.updated_at` **不动**：压的是派生数据，对话本身没变（列表排序该按"最后一次说话"）。
func (s *Store) RecordSummary(summary model.Summary, sourceIDs []string) error {
	switch summary.SourceKind {
	case model.SourceMessages, model.SourceSummaries:
	default:
		return InvalidError("摘要的成员只能是消息或摘要（source_kind 不认识：" + string(summary.SourceKind) + "）")
	}
	if len(sourceIDs) == 0 {
		return InvalidError("压缩要盖住至少一条（空区间不落库）")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if summary.SourceKind == model.SourceSummaries {
		if err := insertSummary(tx, summary); err != nil {
			return err
		}
		updated, err := updateSummaryParent(tx, summary.SessionID, summary.ID, sourceIDs)
		if err != nil {
			return err
		}
		if updated != len(sourceIDs) {
			return InvalidError("要并的这几条已经不是原来那几条顶层摘要了（被抢过 / 被删过）：重新取一次")
		}
		return tx.Commit()
	}

	covered, err := coveredCount(tx, summary.SessionID, sourceIDs)
	if err != nil {
		return err
	}
	if covered != len(sourceIDs) {
		return InvalidError("这段区间里的消息已经不是原来那几条了（被删过 / 换过会话）：重新取一次")
	}
	already, err := alreadyCovered(tx, summary.SessionID, sourceIDs)
	if err != nil {
		return err
	}
	if already != 0 {
		return InvalidError("这段区间里已经有消息被摘要盖住了：压缩不许覆盖已压缩的区间")
	}
	if err := insertSummary(tx, summary); err != nil {
		return err
	}
	updated, err := updateMessageSummary(tx, summary.SessionID, summary.ID, sourceIDs)
	if err != nil {
		return err
	}
	if updated != len(sourceIDs) {
		return InvalidError("这段区间里的消息已经不是原来那几条了（被删过 / 换过会话）：重新取一次")
	}
	return tx.Commit()
}

// coveredCount：这批 id 里**属于这条会话**的有几条（少一条都说明区间变了）。
// **调用方持事务**。
func coveredCount(tx *sql.Tx, sessionID string, messageIDs []string) (int, error) {
	query, args := idBatchQuery("SELECT COUNT(*) FROM messages WHERE session_id = ?1 AND id IN (",
		sessionID, messageIDs)
	var count int
	if err := tx.QueryRow(query, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// alreadyCovered：这批 id 里**已经被摘要盖住**的有几条（一个都不许有）。**调用方持事务**。
func alreadyCovered(tx *sql.Tx, sessionID string, messageIDs []string) (int, error) {
	query, args := idBatchQuery(
		"SELECT COUNT(*) FROM messages WHERE session_id = ?1 AND summary_id IS NOT NULL AND id IN (",
		sessionID, messageIDs)
	var count int
	if err := tx.QueryRow(query, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// updateMessageSummary：把这一段消息的 `summary_id` 指过去。**调用方持事务**。
func updateMessageSummary(tx *sql.Tx, sessionID, summaryID string, messageIDs []string) (int, error) {
	marks := make([]string, len(messageIDs))
	args := make([]any, 0, len(messageIDs)+2)
	args = append(args, summaryID, sessionID) // ?1 = summary_id，?2 = session_id
	for index, id := range messageIDs {
		marks[index] = "?" + strconv.Itoa(index+3)
		args = append(args, id)
	}
	result, err := tx.Exec("UPDATE messages SET summary_id = ?1 WHERE session_id = ?2 AND id IN ("+
		strings.Join(marks, ",")+")", args...)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	return int(affected), err
}

// updateSummaryParent：把这一段**子摘要**的 `parent_summary_id` 指过去。**调用方持事务**。
//
// 条件 `parent_summary_id IS NULL` 就是合并级那条复核：只认**还没爹**的（仍顶层），
// 谁先到谁算；`session_id` 保证不跨会话乱认爹。行数对不上 ⇒ 调用方报错并整笔回滚。
func updateSummaryParent(tx *sql.Tx, sessionID, summaryID string, childIDs []string) (int, error) {
	marks := make([]string, len(childIDs))
	args := make([]any, 0, len(childIDs)+2)
	args = append(args, summaryID, sessionID) // ?1 = parent_summary_id，?2 = session_id
	for index, id := range childIDs {
		marks[index] = "?" + strconv.Itoa(index+3)
		args = append(args, id)
	}
	result, err := tx.Exec("UPDATE summaries SET parent_summary_id = ?1 "+
		"WHERE session_id = ?2 AND parent_summary_id IS NULL AND id IN ("+
		strings.Join(marks, ",")+")", args...)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	return int(affected), err
}

// idBatchQuery：拼一条 `… IN (?2, ?3, …)` 的查询（`?1` 留给调用方第一个参数）。
func idBatchQuery(prefix, sessionID string, ids []string) (string, []any) {
	marks := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, sessionID)
	for index, id := range ids {
		marks[index] = "?" + strconv.Itoa(index+2)
		args = append(args, id)
	}
	return prefix + strings.Join(marks, ",") + ")", args
}

// markSummaryChainDirty：把"覆盖了这条消息"的摘要**及其各级祖先**标为过期。
// **在调用方的事务里跑**（递归 CTE 一趟上去：messages.summary_id → summaries.parent_summary_id → …）。
func markSummaryChainDirty(tx *sql.Tx, messageID string) error {
	_, err := tx.Exec(
		`WITH RECURSIVE chain(id, parent_summary_id) AS (
		     SELECT summaries.id, summaries.parent_summary_id
		     FROM messages JOIN summaries ON summaries.id = messages.summary_id
		     WHERE messages.id = ?1
		     UNION ALL
		     SELECT summaries.id, summaries.parent_summary_id
		     FROM summaries JOIN chain ON summaries.id = chain.parent_summary_id
		 )
		 UPDATE summaries SET dirty = 1 WHERE id IN (SELECT id FROM chain)`, messageID)
	return err
}

// summaryColumns：摘要自己的列。`begin_message_id` / `end_message_id` = 它盖住的那一段
// （线性会话里的起止消息，闭区间；装配按它 O(1) 跳过去）。两端都可为空 ⇒ 那条摘要不跳（逐条走）。
const summaryColumns = "id, session_id, parent_summary_id, source_kind, begin_message_id, end_message_id, " +
	"text, blocks, tokens, source_ids, provider, model, prompt_version, usage, dirty, created_at"

func scanSummary(row scanner) (model.Summary, error) {
	var summary model.Summary
	var parent, begin, end, sourceIDs, usage sql.NullString
	var dirty int64
	err := row.Scan(
		&summary.ID, &summary.SessionID, &parent, &summary.SourceKind, &begin, &end,
		&summary.Text, &summary.Blocks, &summary.Tokens, &sourceIDs,
		&summary.Provider, &summary.Model, &summary.PromptVersion, &usage, &dirty, &summary.CreatedAt,
	)
	if err != nil {
		return model.Summary{}, err
	}
	if parent.Valid {
		summary.ParentSummaryID = &parent.String
	}
	if begin.Valid {
		summary.BeginMessageID = &begin.String
	}
	if end.Valid {
		summary.EndMessageID = &end.String
	}
	if sourceIDs.Valid && sourceIDs.String != "" {
		_ = json.Unmarshal([]byte(sourceIDs.String), &summary.SourceIDs)
	}
	if usage.Valid {
		summary.Usage = json.RawMessage(usage.String)
	}
	summary.Dirty = dirty != 0
	return summary, nil
}

// listSummaries：按 id 顺序取这条会话的摘要（装配的行走、删除计划、Copy 都要用）。
// **调用方持锁**。
func (s *Store) listSummaries(sessionID string) ([]model.Summary, error) {
	rows, err := s.db.Query("SELECT "+summaryColumns+
		" FROM summaries WHERE session_id = ?1 ORDER BY id", sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	summaries := []model.Summary{}
	for rows.Next() {
		summary, err := scanSummary(rows)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

// ListSummaries：公开版（持锁）。
func (s *Store) ListSummaries(sessionID string) ([]model.Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listSummaries(sessionID)
}

// insertSummary：插一行摘要（**父指针一律先写 NULL** —— `parent_summary_id` 是自引用外键，
// 父必须先存在；先全插再回填父指针，就与摘要的插入顺序无关）。**调用方持事务**。
func insertSummary(tx *sql.Tx, summary model.Summary) error {
	sourceIDs := summary.SourceIDs
	if sourceIDs == nil {
		sourceIDs = []string{} // 落 `[]` 而不是 `null`（列有默认值，别人也按数组读）
	}
	encoded, err := json.Marshal(sourceIDs)
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		`INSERT INTO summaries
		   (id, session_id, parent_summary_id, source_kind, begin_message_id, end_message_id,
		    text, blocks, tokens, source_ids, provider, model, prompt_version, usage, dirty, created_at)
		 VALUES (?1, ?2, NULL, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15)`,
		summary.ID, summary.SessionID, string(summary.SourceKind),
		summary.BeginMessageID, summary.EndMessageID, summary.Text, summary.Blocks, summary.Tokens,
		string(encoded), summary.Provider, summary.Model, summary.PromptVersion,
		usageArg(summary.Usage), dirtyArg(summary.Dirty), summary.CreatedAt)
	return err
}
