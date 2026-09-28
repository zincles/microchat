package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"

	"microchat/internal/model"
)

// ErrNotFound：找不到（HTTP 层的 404）。
var ErrNotFound = errors.New("not_found")

// InvalidError：请求不合规矩（HTTP 层的 400）。
type InvalidError string

func (e InvalidError) Error() string { return string(e) }

func nowMS() int64 { return time.Now().UnixMilli() }

// conversationColumns：与 Rust 版 get_conversation/list_conversations 的 SELECT 逐字相同。
const conversationColumns = "id, title, system_prompt, provider, model, agent_id, current_leaf, created_at, updated_at"

// 一个最小的扫描接口：sql.Row 与 sql.Rows 都满足它。
type scanner interface{ Scan(dest ...any) error }

func scanConversation(row scanner) (model.Conversation, error) {
	var conversation model.Conversation
	var currentLeaf sql.NullString
	err := row.Scan(
		&conversation.ID, &conversation.Title, &conversation.SystemPrompt,
		&conversation.Provider, &conversation.Model, &conversation.AgentID,
		&currentLeaf, &conversation.CreatedAt, &conversation.UpdatedAt,
	)
	if currentLeaf.Valid {
		conversation.CurrentLeaf = &currentLeaf.String
	}
	return conversation, err
}

func (s *Store) GetConversation(id string) (*model.Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conversation(id)
}

// conversation：**调用方持锁**（内部版，事务与复合操作要用）。
func (s *Store) conversation(id string) (*model.Conversation, error) {
	row := s.db.QueryRow("SELECT "+conversationColumns+" FROM conversations WHERE id = ?1", id)
	conversation, err := scanConversation(row)
	if err == sql.ErrNoRows {
		return nil, nil // 找不到不是错误（Rust 版回 None）
	}
	if err != nil {
		return nil, err
	}
	return &conversation, nil
}

// ListConversations：按 updated_at 倒序（同刻按 id 倒序）—— 界面的排序口径。
func (s *Store) ListConversations() ([]model.Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT " + conversationColumns +
		" FROM conversations ORDER BY updated_at DESC, id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	conversations := []model.Conversation{}
	for rows.Next() {
		conversation, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		conversations = append(conversations, conversation)
	}
	return conversations, rows.Err()
}

// CreateConversation：新会话。title 一律空串（首条用户消息落库时才起名），
// agent 先落内置默认 —— 调用方（server）再按 `agents.json` 的 default_agent 覆盖。
// 注意参数名不叫 model —— 那会遮蔽 model **包**（Go 会把它当成 string 用）。
func (s *Store) CreateConversation(provider, modelID, systemPrompt string) (model.Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := uuid.NewV7()
	if err != nil {
		return model.Conversation{}, err
	}
	now := nowMS()
	conversation := model.Conversation{
		ID: id.String(), Title: "", SystemPrompt: systemPrompt,
		Provider: provider, Model: modelID, AgentID: "default", // 内置默认，server 再按 agents.json 覆盖
		CreatedAt: now, UpdatedAt: now,
	}
	_, err = s.db.Exec(
		`INSERT INTO conversations (id, title, system_prompt, provider, model, agent_id, created_at, updated_at)
		 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)`,
		conversation.ID, conversation.Title, conversation.SystemPrompt,
		conversation.Provider, conversation.Model, conversation.AgentID, now, now)
	if err != nil {
		return model.Conversation{}, err
	}
	return conversation, nil
}

// 下面几个 UPDATE 都**不碰 updated_at** —— 与 Rust 版的 SQL 逐字一致
// （所以改标题不会让会话跳到列表最上面）。
func (s *Store) UpdateTitle(id, title string) error {
	return s.execTouch("UPDATE conversations SET title = ?1 WHERE id = ?2", title, id)
}

func (s *Store) SetConversationModel(id, provider, model string) error {
	return s.execTouch("UPDATE conversations SET provider = ?1, model = ?2 WHERE id = ?3", provider, model, id)
}

func (s *Store) SetConversationAgent(id, agentID string) error {
	return s.execTouch("UPDATE conversations SET agent_id = ?1 WHERE id = ?2", agentID, id)
}

func (s *Store) execTouch(query string, args ...any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return execTouchLocked(s.db, query, args...)
}

// execTouchLocked：**调用方持锁**（也用于事务里的 `*sql.Tx`）。0 行受影响 ⇒ NotFound。
func execTouchLocked(db execer, query string, args ...any) error {
	result, err := db.Exec(query, args...)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// execer：`*sql.DB` 与 `*sql.Tx` 都满足（同一个函数能跑在事务里）。
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// DeleteConversation：连消息一起没（外键 CASCADE）。
func (s *Store) DeleteConversation(id string) error {
	return s.execTouch("DELETE FROM conversations WHERE id = ?1", id)
}

// SwitchLeafToSibling：界面上的「‹ 2/3 ›」—— 切到**当前尾巴的兄弟**，再顺着最新的孩子走到末端。
//
// 只允许切到兄弟：切到别的旧消息 = 把整条对话倒回去，而世界状态是沿当前路径现演的
// ⇒ 世界状态会跟着倒退。那种事必须是一个明确的"从这里重新开始"，不该藏在分支箭头里。
func (s *Store) SwitchLeafToSibling(conversationID, messageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.allMessages(conversationID)
	if err != nil {
		return err
	}
	conversation, err := s.conversation(conversationID)
	if err != nil {
		return err
	}
	if conversation == nil || conversation.CurrentLeaf == nil {
		return InvalidError("这条会话还没有对话，谈不上切分支")
	}
	find := func(id string) *model.Message {
		for index := range all {
			if all[index].ID == id {
				return &all[index]
			}
		}
		return nil
	}
	current, target := find(*conversation.CurrentLeaf), find(messageID)
	if target == nil {
		return ErrNotFound
	}
	sameParent := current.ParentID == nil && target.ParentID == nil
	if current.ParentID != nil && target.ParentID != nil && *current.ParentID == *target.ParentID {
		sameParent = true
	}
	if !sameParent {
		return InvalidError("只能在最新那句上切换分支（切到它的兄弟）")
	}
	return s.setCurrentLeafDeep(conversationID, messageID)
}

// setCurrentLeafDeep：落在那条上，再顺着**最新的孩子**一路往下（rowid 靠后的那个）。
// **调用方持锁**。
func (s *Store) setCurrentLeafDeep(conversationID, messageID string) error {
	all, err := s.allMessages(conversationID)
	if err != nil {
		return err
	}
	cursor := messageID
	for {
		next := ""
		for index := range all {
			message := all[index]
			if message.ParentID != nil && *message.ParentID == cursor && message.ID != messageID {
				next = message.ID // 越靠后越新（all 按 rowid 排）
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	return execTouchLocked(s.db, "UPDATE conversations SET current_leaf = ?1 WHERE id = ?2", cursor, conversationID)
}
