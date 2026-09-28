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

// listSummaries：按 id 顺序取这条会话的摘要（装配时的行走算法要用）。
func (s *Store) listSummaries(conversationID string) ([]model.Summary, error) {
	rows, err := s.db.Query(
		`SELECT id, conversation_id, parent_summary_id, source_kind, text, blocks, tokens,
		        source_ids, provider, model, prompt_version, usage, dirty, created_at
		 FROM summaries WHERE conversation_id = ?1 ORDER BY id`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	summaries := []model.Summary{}
	for rows.Next() {
		var summary model.Summary
		var parent, sourceIDs, usage sql.NullString
		var dirty int64
		if err := rows.Scan(
			&summary.ID, &summary.ConversationID, &parent, &summary.SourceKind, &summary.Text,
			&summary.Blocks, &summary.Tokens, &sourceIDs, &summary.Provider, &summary.Model,
			&summary.PromptVersion, &usage, &dirty, &summary.CreatedAt,
		); err != nil {
			return nil, err
		}
		if parent.Valid {
			summary.ParentSummaryID = &parent.String
		}
		if sourceIDs.Valid && sourceIDs.String != "" {
			_ = json.Unmarshal([]byte(sourceIDs.String), &summary.SourceIDs)
		}
		if usage.Valid {
			summary.Usage = json.RawMessage(usage.String)
		}
		summary.Dirty = dirty != 0
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

// ListSummaries：公开版（持锁）。
func (s *Store) ListSummaries(conversationID string) ([]model.Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listSummaries(conversationID)
}
