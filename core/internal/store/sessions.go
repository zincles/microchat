package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
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

// sessionColumns：会话自己的列（**没有 current_leaf**：线性会话不需要"停在哪儿"）。
const sessionColumns = "id, title, system_prompt, provider, model, agent_id, created_at, updated_at"

// 一个最小的扫描接口：sql.Row 与 sql.Rows 都满足它。
type scanner interface{ Scan(dest ...any) error }

func scanSession(row scanner) (model.Session, error) {
	var session model.Session
	err := row.Scan(
		&session.ID, &session.Title, &session.SystemPrompt,
		&session.Provider, &session.Model, &session.AgentID,
		&session.CreatedAt, &session.UpdatedAt,
	)
	return session, err
}

func (s *Store) GetSession(id string) (*model.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.session(id)
}

// session：**调用方持锁**（内部版，事务与复合操作要用）。
func (s *Store) session(id string) (*model.Session, error) {
	row := s.db.QueryRow("SELECT "+sessionColumns+" FROM sessions WHERE id = ?1", id)
	session, err := scanSession(row)
	if err == sql.ErrNoRows {
		return nil, nil // 找不到不是错误（Rust 版回 None）
	}
	if err != nil {
		return nil, err
	}
	return &session, nil
}

// ListSessions：按 updated_at 倒序（同刻按 id 倒序）—— 界面的排序口径。
func (s *Store) ListSessions() ([]model.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT " + sessionColumns +
		" FROM sessions ORDER BY updated_at DESC, id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sessions := []model.Session{}
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

// CreateSession：新会话。`title` 由调用方给（`POST /sessions` 的 `title`；不给 ⇒ 空串 ⇒
// 首条用户消息落库时才自动起名），`agent` 先落内置默认 —— 调用方（server）再按
// `agents.json` 的 default_agent 覆盖。
// 注意参数名不叫 model —— 那会遮蔽 model **包**（Go 会把它当成 string 用）。
func (s *Store) CreateSession(provider, modelID, systemPrompt, title string) (model.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := uuid.NewV7()
	if err != nil {
		return model.Session{}, err
	}
	now := nowMS()
	session := model.Session{
		ID: id.String(), Title: title, SystemPrompt: systemPrompt,
		Provider: provider, Model: modelID, AgentID: "default", // 内置默认，server 再按 agents.json 覆盖
		CreatedAt: now, UpdatedAt: now,
	}
	_, err = s.db.Exec(
		`INSERT INTO sessions (id, title, system_prompt, provider, model, agent_id, created_at, updated_at)
		 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)`,
		session.ID, session.Title, session.SystemPrompt,
		session.Provider, session.Model, session.AgentID, now, now)
	if err != nil {
		return model.Session{}, err
	}
	return session, nil
}

// 下面几个 UPDATE 都**不碰 updated_at** —— 与 Rust 版的 SQL 逐字一致
// （所以改标题不会让会话跳到列表最上面）。
func (s *Store) UpdateTitle(id, title string) error {
	return s.execTouch("UPDATE sessions SET title = ?1 WHERE id = ?2", title, id)
}

// SetTitleIfEmpty：**只有当标题还空着**时才写（一次 UPDATE，条件在 SQL 里）。返回是否真写了。
//
// 为什么条件在 SQL 里、而不是"先读再写"：自动起名在后台发生，期间用户完全可能刚手动改过名 ——
// 先读后写会把人的选择覆盖掉（口径：**用户改过名 ⇒ 标题非空 ⇒ 永不再自动覆盖**）。
// 一次 UPDATE 也就**没有跨语句的竞争**可言。
func (s *Store) SetTitleIfEmpty(id, title string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.Exec(
		"UPDATE sessions SET title = ?1 WHERE id = ?2 AND TRIM(title) = ''", title, id)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

func (s *Store) SetSessionModel(id, provider, model string) error {
	return s.execTouch("UPDATE sessions SET provider = ?1, model = ?2 WHERE id = ?3", provider, model, id)
}

func (s *Store) SetSessionAgent(id, agentID string) error {
	return s.execTouch("UPDATE sessions SET agent_id = ?1 WHERE id = ?2", agentID, id)
}

// SetSessionSystemPrompt：写会话级提示词覆盖（空串 = **清掉覆盖** ⇒ 解析回落到 agent 的）。
//
// 与其它几个 UPDATE 一样**不碰 updated_at**（改提示词不该让会话跳到列表最上面）。
func (s *Store) SetSessionSystemPrompt(id, systemPrompt string) error {
	return s.execTouch("UPDATE sessions SET system_prompt = ?1 WHERE id = ?2", systemPrompt, id)
}

// RenameAgentReferences：把**所有会话**对 agent 的软引用从 old 搬到 new。
//
// 改名时**先搬库、再写文件**（这个顺序是刻意的，理由见 server.updateAgent 的注释）。
// 0 行受影响很正常（还没有会话用它）⇒ 不算错。
func (s *Store) RenameAgentReferences(oldID, newID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.Exec("UPDATE sessions SET agent_id = ?1 WHERE agent_id = ?2", newID, oldID)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	return int(affected), err
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

// DeleteSession：连消息一起没（外键 CASCADE）。
func (s *Store) DeleteSession(id string) error {
	return s.execTouch("DELETE FROM sessions WHERE id = ?1", id)
}

// CopySession：**Copy 一条会话** —— 线性会话里"分岔"就是它（不叫 fork）。
//
// 新 session（新 id；标题 / 渠道 / 模型 / agent / 提示词都带着）+ 消息与摘要一并复制。
// 摘要有三处必须重映射，否则复制出来的会话会指向**原**会话的 id：
//   - 摘要自己的新 id ⇒ 消息上的 `summary_id` 与摘要的 `parent_summary_id`；
//   - 两端区间 `begin_message_id` / `end_message_id`（它们指的是**消息** id）；
//   - `source_ids`（审计 + 整批重做）—— 按 `type` 指消息或摘要（DB 列名是历史：`source_kind`）。
//
// **世界状态不复制**：它本来就现演，底子与正文都复制了就等于复制了它。
func (s *Store) CopySession(sessionID string) (model.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	source, err := s.session(sessionID)
	if err != nil {
		return model.Session{}, err
	}
	if source == nil {
		return model.Session{}, ErrNotFound
	}
	messages, err := s.allMessages(sessionID)
	if err != nil {
		return model.Session{}, err
	}
	summaries, err := s.listSummaries(sessionID)
	if err != nil {
		return model.Session{}, err
	}
	newSessionID, err := newID()
	if err != nil {
		return model.Session{}, err
	}
	messageIDs, err := mintOrderedIDs(len(messages))
	if err != nil {
		return model.Session{}, err
	}
	summaryIDs, err := mintOrderedIDs(len(summaries))
	if err != nil {
		return model.Session{}, err
	}
	messageIDOf := make(map[string]string, len(messages))
	for index := range messages {
		messageIDOf[messages[index].ID] = messageIDs[index]
	}
	summaryIDOf := make(map[string]string, len(summaries))
	for index := range summaries {
		summaryIDOf[summaries[index].ID] = summaryIDs[index]
	}

	now := nowMS()
	copied := model.Session{
		ID: newSessionID, Title: source.Title, SystemPrompt: source.SystemPrompt,
		Provider: source.Provider, Model: source.Model, AgentID: source.AgentID,
		CreatedAt: now, UpdatedAt: now,
	}
	tx, err := s.db.Begin()
	if err != nil {
		return model.Session{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(
		`INSERT INTO sessions (id, title, system_prompt, provider, model, agent_id, created_at, updated_at)
		 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)`,
		copied.ID, copied.Title, copied.SystemPrompt, copied.Provider, copied.Model,
		copied.AgentID, copied.CreatedAt, copied.UpdatedAt); err != nil {
		return model.Session{}, err
	}
	// 摘要先插（消息的 `summary_id` 外键指着它）；父指针由 `insertSummary` 先留 NULL、插完回填。
	for index := range summaries {
		summary := summaries[index]
		if err := insertSummary(tx, model.Summary{
			ID: summaryIDs[index], SessionID: copied.ID,
			Type:           summary.Type,
			BeginMessageID: optionalID(summary.BeginMessageID, messageIDOf),
			EndMessageID:   optionalID(summary.EndMessageID, messageIDOf),
			Text:           summary.Text,
			Blocks:         summary.Blocks,
			Tokens:         summary.Tokens,
			SourceIDs:      remapSourceIDs(summary, messageIDOf, summaryIDOf),
			Provider:       summary.Provider,
			Model:          summary.Model,
			PromptVersion:  summary.PromptVersion,
			Usage:          summary.Usage,
			Dirty:          summary.Dirty,
			CreatedAt:      summary.CreatedAt,
		}); err != nil {
			return model.Session{}, err
		}
	}
	for index := range summaries {
		parent := summaries[index].ParentSummaryID
		if parent == nil {
			continue
		}
		mapped, ok := summaryIDOf[*parent]
		if !ok {
			continue // 悬空父指针：复制出来当顶层（原样保留一个指不着东西的 id 没有意义）
		}
		if _, err := tx.Exec("UPDATE summaries SET parent_summary_id = ?1 WHERE id = ?2",
			mapped, summaryIDs[index]); err != nil {
			return model.Session{}, err
		}
	}
	for index := range messages {
		message := messages[index]
		if _, err := tx.Exec(
			`INSERT INTO messages
			   (id, session_id, role, content, created_at, updated_at, reasoning, reasoning_ms, duration_ms, usage, summary_id)
			 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11)`,
			messageIDs[index], copied.ID, string(message.Role), message.Content, message.CreatedAt, message.UpdatedAt,
			message.Reasoning, message.ReasoningMS, message.DurationMS, usageArg(message.Usage),
			remapID(message.SummaryID, summaryIDOf)); err != nil {
			return model.Session{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return model.Session{}, err
	}
	return copied, nil
}

// newID：铸一个 UUIDv7（会话 / 消息 / 摘要都用它）。
func newID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

// MintOrderedIDs：铸 n 个 id，**排序后按顺序发**。
//
// UUIDv7 只在**毫秒**上有序（同一毫秒里剩下那 74 位是随机的）⇒ 一口气连铸几十个会乱序；
// 而线性会话的顺序**就是** id 定的 ⇒ 乱序 = 静默错。铸完排一次序就稳（复制几十条，代价可忽略）。
//
// 生成那一轮也用它：受理时要一口气铸两个（用户消息 + 这条回复），**用户那句必须在前面**。
func MintOrderedIDs(count int) ([]string, error) { return mintOrderedIDs(count) }

func mintOrderedIDs(count int) ([]string, error) {
	ids := make([]string, count)
	for index := range ids {
		id, err := newID()
		if err != nil {
			return nil, err
		}
		ids[index] = id
	}
	sort.Strings(ids)
	return ids, nil
}

// optionalID：把可选 id 换成新 id；指不着东西的原样留着（改不改都一样指不着）。
func optionalID(id *string, table map[string]string) *string {
	if id == nil {
		return nil
	}
	mapped, ok := table[*id]
	if !ok {
		return id
	}
	return &mapped
}

// remapID：同上，但直接给 SQL 的可空列用（nil = NULL）。
func remapID(id *string, table map[string]string) any {
	if id == nil {
		return nil
	}
	return *optionalID(id, table)
}

// remapSourceIDs：`source_ids` 按 `type` 指向消息或摘要 ⇒ 一并换成新 id（DB 列名是历史：`source_kind`）。
//
// 不换也能跑，但复制出来的摘要会一直指着**原会话**的 id（"当时吃的是什么"要追得回来）。
func remapSourceIDs(summary model.Summary, messageIDOf, summaryIDOf map[string]string) []string {
	table := summaryIDOf
	if summary.Type == model.TypeMessages {
		table = messageIDOf
	}
	remapped := make([]string, 0, len(summary.SourceIDs))
	for _, id := range summary.SourceIDs {
		if mapped, ok := table[id]; ok {
			remapped = append(remapped, mapped)
			continue
		}
		remapped = append(remapped, id)
	}
	return remapped
}

// usageArg / dirtyArg：可选列进 SQL 的两种形状（空 JSON 要落 NULL，不是空串）。
func usageArg(usage json.RawMessage) any {
	if len(usage) == 0 {
		return nil
	}
	return string(usage)
}

func dirtyArg(dirty bool) int64 {
	if dirty {
		return 1
	}
	return 0
}
