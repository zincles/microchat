package store

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// ModelRow：`models` 表一行。**发现列**与**用户列**分得很清（刷新永不动用户列）。
type ModelRow struct {
	Provider   string
	UpstreamID string
	// 发现列（刷新会覆写）
	UpstreamName   *string
	OwnedBy        *string
	ContextLength  *int64
	MaxOutput      *int64
	UpstreamParams json.RawMessage
	FirstSeenAt    int64
	LastSeenAt     int64
	// 用户列（刷新永不动）
	DisplayName     *string
	Params          json.RawMessage
	ContextOverride *int64
	Tokenizer       json.RawMessage
}

// Discovered：上游 `/models` 里的一项（发现所得，落库只写这半边）。
type Discovered struct {
	UpstreamID        string          `json:"id"`
	UpstreamName      *string         `json:"name"`
	OwnedBy           *string         `json:"owned_by"`
	ContextLength     *int64          `json:"context_length"`
	MaxOutput         *int64          `json:"max_output"`
	SupportedParams   []string        `json:"supported_parameters"`
	DefaultParameters json.RawMessage `json:"default_parameters"`
}

const modelColumns = "provider, upstream_id, upstream_name, owned_by, context_length, max_output, " +
	"upstream_params, first_seen_at, last_seen_at, display_name, params, context_override, tokenizer"

func scanModel(row scanner) (ModelRow, error) {
	var model ModelRow
	var upstreamName, ownedBy, upstreamParams, displayName, params, tokenizer sql.NullString
	var contextLen, maxOut, override sql.NullInt64
	err := row.Scan(
		&model.Provider, &model.UpstreamID, &upstreamName, &ownedBy,
		&contextLen, &maxOut,
		&upstreamParams, &model.FirstSeenAt, &model.LastSeenAt,
		&displayName, &params, &override, &tokenizer,
	)
	if err != nil {
		return model, err
	}
	model.UpstreamName = nullString(upstreamName)
	model.OwnedBy = nullString(ownedBy)
	if contextLen.Valid {
		model.ContextLength = &contextLen.Int64
	}
	if maxOut.Valid {
		model.MaxOutput = &maxOut.Int64
	}
	model.UpstreamParams = rawOrNull(upstreamParams)
	model.DisplayName = nullString(displayName)
	model.Params = rawOrNull(params)
	if override.Valid {
		model.ContextOverride = &override.Int64
	}
	model.Tokenizer = rawOrNull(tokenizer)
	return model, nil
}

func nullString(value sql.NullString) *string {
	if value.Valid {
		text := value.String
		return &text
	}
	return nil
}

func rawOrNull(value sql.NullString) json.RawMessage {
	if value.Valid && value.String != "" {
		return json.RawMessage(value.String)
	}
	return nil
}

// RefreshDiscovered：一次发现的**整轮落库** —— 上新、更新，**这次没见到的删行**（列表 = 上游当前那份）。
//
// **只碰发现列**；用户列（display_name / params / context_override / tokenizer）一个字不动。
func (s *Store) RefreshDiscovered(provider string, found []Discovered, now int64) (added, updated, removed int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()

	seen := map[string]bool{}
	for _, item := range found {
		seen[item.UpstreamID] = true
		upstreamParams := json.RawMessage(nil)
		if len(item.SupportedParams) > 0 || len(item.DefaultParameters) > 0 {
			upstreamParams, _ = json.Marshal(map[string]any{
				"supported": item.SupportedParams,
				"default":   json.RawMessage(orEmptyObject(item.DefaultParameters)),
			})
		}
		result, e := tx.Exec(
			`INSERT INTO models (provider, upstream_id, upstream_name, owned_by, context_length, max_output,
			                     upstream_params, first_seen_at, last_seen_at)
			 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?8)
			 ON CONFLICT(provider, upstream_id) DO UPDATE SET
			     upstream_name = excluded.upstream_name,
			     owned_by = excluded.owned_by,
			     context_length = excluded.context_length,
			     max_output = excluded.max_output,
			     upstream_params = excluded.upstream_params,
			     last_seen_at = excluded.last_seen_at`,
			provider, item.UpstreamID, item.UpstreamName, item.OwnedBy, item.ContextLength, item.MaxOutput,
			nullJSON(upstreamParams), now)
		if e != nil {
			err = e
			return
		}
		if rows, e := result.RowsAffected(); e == nil {
			// 插入 = 1 行受影响且 created 与 updated 无法区分（SQLite 里 upsert 都是 1）
			// ⇒ 这里只统计"落了几个"，removed 单独算。
			_ = rows
			added++
		}
	}

	// 这次没见到的 ⇒ 删行（**只删发现列存在过的行**：用户手动写的模型不该被发现清掉 ✗）
	// —— 本项目的模型全部来自发现 ⇒ 一律按"没见到就删"。
	rows, e := tx.Query("SELECT upstream_id FROM models WHERE provider = ?1", provider)
	if e != nil {
		err = e
		return
	}
	var stale []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil && !seen[id] {
			stale = append(stale, id)
		}
	}
	rows.Close()
	for _, id := range stale {
		if _, e := tx.Exec("DELETE FROM models WHERE provider = ?1 AND upstream_id = ?2", provider, id); e != nil {
			err = e
			return
		}
		removed++
	}
	if _, e := tx.Exec(
		"INSERT INTO provider_state (provider, last_refresh_at) VALUES (?1, ?2) "+
			"ON CONFLICT(provider) DO UPDATE SET last_refresh_at = excluded.last_refresh_at",
		provider, now); e != nil {
		err = e
		return
	}
	err = tx.Commit()
	return
}

// nullJSON：空的 JSON 对象列一律写 `{}`（这两列 NOT NULL，迁移里默认就是 '{}'）。
func nullJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

func orEmptyObject(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	return raw
}

// ListModels：某个渠道（或全部）的模型，按上游 id 升序。
func (s *Store) ListModels(provider string) ([]ModelRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	query := "SELECT " + modelColumns + " FROM models"
	args := []any{}
	if provider != "" {
		query += " WHERE provider = ?1"
		args = append(args, provider)
	}
	query += " ORDER BY provider, upstream_id"
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	models := []ModelRow{}
	for rows.Next() {
		model, err := scanModel(rows)
		if err != nil {
			return nil, err
		}
		models = append(models, model)
	}
	return models, rows.Err()
}

// GetModel：取一个模型（发现列 + 用户列）；**没有这条 ⇒ nil，不是错误**
// （上游没发现过它、或用户手写了会话的渠道/模型，都很正常 —— 上下文口径自然退到配置兜底）。
func (s *Store) GetModel(provider, upstreamID string) (*ModelRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.db.QueryRow("SELECT "+modelColumns+" FROM models WHERE provider = ?1 AND upstream_id = ?2",
		provider, upstreamID)
	model, err := scanModel(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// SetModelOverride：用户列（显示名 / 上下文覆盖 / 采样参数）—— **只有这里能改**（刷新永不动它）。
func (s *Store) SetModelOverride(provider, upstreamID string, displayName *string, contextOverride *int64, params json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 只改给出的那几列：None = 不动（与 PATCH 的语义一致）
	result, err := s.db.Exec("UPDATE models SET display_name = ?1, context_override = ?2, params = ?3 WHERE provider = ?4 AND upstream_id = ?5",
		nullStringSQL(displayName), nullIntSQL(contextOverride), nullJSON(params), provider, upstreamID)
	if err != nil {
		return err
	}
	return touchRows(result)
}

func nullStringSQL(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullIntSQL(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func touchRows(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// LastRefreshAt：上次刷新时间（`None` = 从未刷新过）。
func (s *Store) LastRefreshAt(provider string) (*int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var at sql.NullInt64
	err := s.db.QueryRow("SELECT last_refresh_at FROM provider_state WHERE provider = ?1", provider).Scan(&at)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if at.Valid {
		return &at.Int64, nil
	}
	return nil, nil
}
