package server

import (
	"net/http"

	"microchat/internal/tavern"
)

// previewTavernPreset：只读解析预览（不建 Agent、不写库）。
func (s *Server) previewTavernPreset(w http.ResponseWriter, r *http.Request) {
	preset, data, err := s.store.GetTavernPreset(r.PathValue("preset_id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if preset == nil {
		writeError(w, http.StatusNotFound, "not_found", "没有这个酒馆预设")
		return
	}
	preview, err := tavern.Parse(data)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, preview)
}
