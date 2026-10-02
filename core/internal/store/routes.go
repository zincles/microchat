package store

// ReplaceModelRoutes：models.dev 快照整批替换某渠道的行 —— 删旧插新，同一事务。
// 只记 `(provider, upstream_id, api, checked_at)` 四列；空表 ⇒ 把该渠道清干净（整份没了就别留旧的）。
func (s *Store) ReplaceModelRoutes(provider string, routes map[string]string, now int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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
