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

	"microchat/internal/config"
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

// deleteTavernPreset：删原始预设。
//
// 2026-11-11 用户定：从有效内容导入的提示词片段带 `preset_id` 来源；删预设时**提醒并可选**
// 一并删除它们。`?segments=delete` 才删段，`?segments=keep` 明确保留；不带参数且有导入段
// ⇒ 409（客户端先问用户，别让服务端替人做不可逆决定）。
// 段还被 Agent / 能力引用 ⇒ 拒绝 —— 删预设不该顺手改别人的人格。
func (s *Server) deleteTavernPreset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("preset_id")
	// 先确认预设真的在：不存在的 id 不该因为遗留的 preset_id 引用而顺手删掉别的数据。
	preset, _, err := s.store.GetTavernPreset(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if preset == nil {
		writeError(w, http.StatusNotFound, "not_found", "没有这个酒馆预设")
		return
	}
	agents := s.loadAgents()
	// Agent 绑定的是原始 preset id：删掉会让它静默回落 Agent 提示词 —— 先列出引用者，拒绝。
	for _, agent := range agents.Effective() {
		if agent.PresetID == id {
			writeError(w, http.StatusBadRequest, "invalid", "这个预设还被 Agent 引用："+agent.Name)
			return
		}
	}
	imported := make([]string, 0)
	for _, segment := range agents.Segments {
		if segment.Imported && segment.PresetID == id {
			imported = append(imported, segment.ID)
		}
	}
	mode := strings.TrimSpace(r.URL.Query().Get("segments"))
	if len(imported) > 0 {
		switch mode {
		case "":
			writeError(w, http.StatusConflict, "conflict",
				"这个预设导入了 "+strconv.Itoa(len(imported))+" 个提示词片段；用 ?segments=delete 一并删除，或 ?segments=keep 保留")
			return
		case "delete":
			if users := segmentUsers(agents, imported); len(users) > 0 {
				writeError(w, http.StatusBadRequest, "invalid",
					"导入的提示词片段还被 Agent 引用（"+strings.Join(users, "、")+"）—— 先摘引用再删")
				return
			}
			keep := make([]config.Segment, 0, len(agents.Segments))
			drop := make(map[string]bool, len(imported))
			for _, sid := range imported {
				drop[sid] = true
			}
			for _, segment := range agents.Segments {
				if !drop[segment.ID] {
					keep = append(keep, segment)
				}
			}
			agents.Segments = keep
			if err := s.saveAgents(agents); err != nil {
				writeStoreError(w, err)
				return
			}
		case "keep":
			// 保留片段；PresetID/PresetName 是来源快照，留着仍能说清从哪来。
		default:
			writeError(w, http.StatusBadRequest, "invalid", "segments 只能是 delete 或 keep")
			return
		}
	}
	ok, err := s.store.DeleteTavernPreset(id)
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

// segmentUsers：哪些 Agent（基座或能力追加段）引用了这些提示词段。名字优先，空名字回 id。
func segmentUsers(agents config.AgentsConfig, ids []string) []string {
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	users := make([]string, 0)
	seen := make(map[string]bool)
	for _, agent := range agents.Agents {
		used := false
		for _, ref := range agent.Segments {
			if wanted[ref] {
				used = true
				break
			}
		}
		if !used {
			for _, ability := range agent.Abilities {
				for _, ref := range ability.Segments {
					if wanted[ref] {
						used = true
						break
					}
				}
				if used {
					break
				}
			}
		}
		if !used {
			continue
		}
		name := agent.Name
		if name == "" {
			name = agent.ID
		}
		if !seen[name] {
			seen[name] = true
			users = append(users, name)
		}
	}
	return users
}
