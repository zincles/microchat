package store

import (
	"database/sql"
	"encoding/json"

	"microchat/internal/model"
)

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
