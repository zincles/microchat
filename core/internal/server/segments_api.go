package server

import (
	"net/http"
	"strings"

	"microchat/internal/config"
)

// 提示词段：`agents.json` 顶层的可复用文本库，system prompt、最新用户消息和能力共用。
// 读口随 `GET /agents`；创建、克隆、改名/正文、删除在此编排。
// 删除有守卫：任一 Agent 仍引用 ⇒ 拒绝并列出引用者。

type segmentReq struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	// Imported：卡带 / ST 预设导入时置 true（来源标记；界面显示「导入」徽标）。
	// 手动建段不写就是 false。
	Imported bool `json:"imported,omitempty"`
	// PresetID：从某个 SillyTavern 预设的有效内容导入时，记下来源预设。
	// 服务端会查一遍这个预设还在不在，并把文件名快照进 PresetName。
	PresetID string `json:"preset_id,omitempty"`
}

func (s *Server) createSegment(w http.ResponseWriter, r *http.Request) {
	var req segmentReq
	if !decodeJSON(w, r, &req) {
		return
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid", "段 id 不能为空")
		return
	}
	if len([]rune(id)) > 64 {
		writeError(w, http.StatusBadRequest, "invalid", "段 id 太长（64 字以内）")
		return
	}
	presetID := strings.TrimSpace(req.PresetID)
	presetName := ""
	if presetID != "" {
		preset, _, err := s.store.GetTavernPreset(presetID)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if preset == nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid", "来源 ST 预设不存在："+presetID)
			return
		}
		presetName = preset.Filename
	}
	agents := s.loadAgents()
	if _, builtin := config.BuiltinSegmentByID(id); builtin {
		writeError(w, http.StatusConflict, "conflict", "内置段 id 保留："+id+"（内置只读，换个 id）")
		return
	}
	if _, taken := agents.SegmentByID(id); taken {
		writeError(w, http.StatusConflict, "conflict", "段 id 被占了："+id)
		return
	}
	segment := config.Segment{
		ID:         id,
		Text:       req.Text,
		Imported:   req.Imported || presetID != "",
		PresetID:   presetID,
		PresetName: presetName,
	}
	agents.Segments = append(agents.Segments, segment)
	if err := s.saveAgents(agents); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, segment)
}

// updateSegment：改内容 / 改名。改名 = 把所有 agent 的引用一起搬（同文件内，一次重写）。
func (s *Server) updateSegment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("segment_id")
	var req struct {
		NewID *string `json:"new_id"`
		Text  *string `json:"text"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, builtin := config.BuiltinSegmentByID(id); builtin {
		writeError(w, http.StatusBadRequest, "invalid", "内置段只读 —— 要改先「复制为自定义」")
		return
	}
	agents := s.loadAgents()
	index := -1
	for i := range agents.Segments {
		if agents.Segments[i].ID == id {
			index = i
		}
	}
	if index < 0 {
		writeError(w, http.StatusNotFound, "not_found", "段不存在")
		return
	}
	if req.NewID != nil {
		newID := strings.TrimSpace(*req.NewID)
		if newID == "" {
			writeError(w, http.StatusBadRequest, "invalid", "段 id 不能为空")
			return
		}
		if newID != id {
			if _, taken := agents.SegmentByID(newID); taken {
				writeError(w, http.StatusConflict, "conflict", "段 id 被占了："+newID)
				return
			}
			for i := range agents.Agents {
				for j, ref := range agents.Agents[i].Segments {
					if ref == id {
						agents.Agents[i].Segments[j] = newID
					}
				}
				for j, ref := range agents.Agents[i].LatestPromptSegments {
					if ref == id {
						agents.Agents[i].LatestPromptSegments[j] = newID
					}
				}
			}
			agents.Segments[index].ID = newID
		}
	}
	if req.Text != nil {
		agents.Segments[index].Text = *req.Text
	}
	if err := s.saveAgents(agents); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, agents.Segments[index])
}

// deleteSegment：删段。**仍被任何 Agent 位置引用 ⇒ 拒绝**（列出引用者，先摘引用再删）。
func (s *Server) deleteSegment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("segment_id")
	if _, builtin := config.BuiltinSegmentByID(id); builtin {
		writeError(w, http.StatusBadRequest, "invalid", "内置段删不掉（内嵌在应用里）")
		return
	}
	agents := s.loadAgents()
	index := -1
	for i := range agents.Segments {
		if agents.Segments[i].ID == id {
			index = i
		}
	}
	if index < 0 {
		writeError(w, http.StatusNotFound, "not_found", "段不存在")
		return
	}
	// 基座与能力追加段都算引用：能力里挂着的段删掉，同样是在悄悄改人格。
	users := segmentUsers(agents, []string{id})
	if len(users) > 0 {
		writeError(w, http.StatusBadRequest, "invalid", "还有 agent 在用它（"+strings.Join(users, "、")+"）—— 先把引用摘掉再删")
		return
	}
	agents.Segments = append(agents.Segments[:index], agents.Segments[index+1:]...)
	if err := s.saveAgents(agents); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// copySegment：复制用户段或内置段；只换 id，正文与来源快照原样保留。
func (s *Server) copySegment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	newID := strings.TrimSpace(req.ID)
	if newID == "" {
		writeError(w, http.StatusBadRequest, "invalid", "段 id 不能为空")
		return
	}
	if len([]rune(newID)) > 64 {
		writeError(w, http.StatusBadRequest, "invalid", "段 id 太长（64 字以内）")
		return
	}
	agents := s.loadAgents()
	if _, builtin := config.BuiltinSegmentByID(newID); builtin {
		writeError(w, http.StatusConflict, "conflict", "内置段 id 保留："+newID)
		return
	}
	if _, taken := agents.SegmentByID(newID); taken {
		writeError(w, http.StatusConflict, "conflict", "段 id 被占了："+newID)
		return
	}
	sourceID := r.PathValue("segment_id")
	segment, ok := agents.SegmentByID(sourceID)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "来源提示词段不存在："+sourceID)
		return
	}
	segment.ID = newID
	agents.Segments = append(agents.Segments, segment)
	if err := s.saveAgents(agents); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, segment)
}
