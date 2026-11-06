package store

import (
	"database/sql"

	"github.com/google/uuid"

	"microchat/internal/model"
)

// ListTavernPresets：酒馆导入列表（新导入在前）。**不取 data** —— 列表只需要元数据。
func (s *Store) ListTavernPresets() ([]model.TavernPreset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT id, filename, size, created_at FROM tavern_presets ORDER BY created_at DESC, id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.TavernPreset{}
	for rows.Next() {
		var p model.TavernPreset
		if err := rows.Scan(&p.ID, &p.Filename, &p.Size, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CreateTavernPreset：只落盘**原始字节**；返回元数据。
func (s *Store) CreateTavernPreset(filename string, data []byte) (model.TavernPreset, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return model.TavernPreset{}, err
	}
	p := model.TavernPreset{ID: id.String(), Filename: filename, Size: int64(len(data)), CreatedAt: nowMS()}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(
		"INSERT INTO tavern_presets (id, filename, size, data, created_at) VALUES (?1, ?2, ?3, ?4, ?5)",
		p.ID, p.Filename, p.Size, data, p.CreatedAt,
	); err != nil {
		return model.TavernPreset{}, err
	}
	return p, nil
}

// GetTavernPreset：元数据 + 原始字节（下载口原样写出）。
func (s *Store) GetTavernPreset(id string) (*model.TavernPreset, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var p model.TavernPreset
	var data []byte
	err := s.db.QueryRow(
		"SELECT id, filename, size, created_at, data FROM tavern_presets WHERE id = ?1", id,
	).Scan(&p.ID, &p.Filename, &p.Size, &p.CreatedAt, &data)
	if err == sql.ErrNoRows {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return &p, data, nil
}

// DeleteTavernPreset：返回是否真的删到了（不存在 ⇒ false，不是错误）。
func (s *Store) DeleteTavernPreset(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec("DELETE FROM tavern_presets WHERE id = ?1", id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
