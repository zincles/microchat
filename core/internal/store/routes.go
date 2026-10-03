package store

// model_route 建表：不住 001（那是原生四表的地方）—— `refresh-routes` 按需建，
// 删库重来也不经过 001。`IF NOT EXISTS` ⇒ 建过就当没看见。
const modelRouteDDL = `CREATE TABLE IF NOT EXISTS model_route (
  provider    TEXT NOT NULL,
  upstream_id TEXT NOT NULL,
  api         TEXT NOT NULL,
  checked_at  INTEGER NOT NULL,
  PRIMARY KEY (provider, upstream_id)
)`

func (s *Store) ReplaceModelRoutes(provider string, routes map[string]string, now int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 表不住 001 ⇒ 用之前先建（`IF NOT EXISTS`，建过就当没看见）。
	// 调用方（测试与 refresh-routes）都不用记这一步。
	if _, err := s.db.Exec(modelRouteDDL); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec("DELETE FROM model_route WHERE provider = ?1", provider); err != nil {
		return err
	}
	for upstreamID, api := range routes {
		if _, err := tx.Exec(
			"INSERT INTO model_route (provider, upstream_id, api, checked_at) VALUES (?1, ?2, ?3, ?4)",
			provider, upstreamID, api, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ModelRoute：查一行；没有 ⇒ ("", false)。
func (s *Store) ModelRoute(provider, upstreamID string) (api string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.db.QueryRow(
		"SELECT api FROM model_route WHERE provider = ?1 AND upstream_id = ?2",
		provider, upstreamID).Scan(&api); err != nil {
		return "", false
	}
	return api, true
}
