package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// 酒馆原始导入：**只存原始字节**，导出时逐字节原样写出。
// 解析 / 转换成 Agent / 角色卡是后续独立的只读步骤；这里绝不 json.Unmarshal 回写。
const maxTavernPresetBytes = 32 << 20 // 32MB（常见 ST 预设 1.5MB）

func (s *Server) listTavernPresets(w http.ResponseWriter, _ *http.Request) {
	list, err := s.store.ListTavernPresets()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) createTavernPreset(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxTavernPresetBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "读上传内容失败："+err.Error())
		return
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "invalid", "上传内容为空")
		return
	}
	if len(body) > maxTavernPresetBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "invalid", "预设文件太大（上限 32MB）")
		return
	}
	// 校验 JSON 但不重写：BOM 允许（有些导出工具会加），原始字节照存。
	check := bytes.TrimPrefix(body, []byte{0xEF, 0xBB, 0xBF})
	if !json.Valid(check) {
		writeError(w, http.StatusUnprocessableEntity, "invalid", "不是合法 JSON；原始字节未保存")
		return
	}
	filename := strings.TrimSpace(r.URL.Query().Get("name"))
	if filename == "" {
		filename = "preset.json"
	}
	// 路径分隔符不进屋（只是显示名）；保留中文/空格/版本号。
	filename = path.Base(strings.ReplaceAll(filename, "\\", "/"))
	if filename == "." || filename == "/" {
		filename = "preset.json"
	}
	if utf8Len := len([]rune(filename)); utf8Len > 200 {
		filename = string([]rune(filename)[:200])
	}
	preset, err := s.store.CreateTavernPreset(filename, body)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, preset)
}

func (s *Server) getTavernPreset(w http.ResponseWriter, r *http.Request) {
	preset, data, err := s.store.GetTavernPreset(r.PathValue("preset_id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if preset == nil {
		writeError(w, http.StatusNotFound, "not_found", "没有这个酒馆预设")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if cd := mime.FormatMediaType("attachment", map[string]string{"filename": preset.Filename}); cd != "" {
		w.Header().Set("Content-Disposition", cd)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) deleteTavernPreset(w http.ResponseWriter, r *http.Request) {
	ok, err := s.store.DeleteTavernPreset(r.PathValue("preset_id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "没有这个酒馆预设")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
