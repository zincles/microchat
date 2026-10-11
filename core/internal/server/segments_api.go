package server

import (
	"net/http"
	"strings"

	"microchat/internal/config"
)

// 提示词段（system prompt segment，用户定 2026-10-11）：`agents.json` 顶层的可复用文本库。
//
// 读口不另开 —— 段库随 `GET /agents` 一起给（那份就是 AgentsConfig 全貌）；这里只有三个写口。
// 写口 = 设置面（admin）。删除有守卫：还被 agent 引用 ⇒ 拒绝（列出引用者，先摘引用再删）。

type segmentReq struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

func (s *Server) createSegment(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
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
	agents := s.loadAgents()
	if _, builtin := config.BuiltinSegmentByID(id); builtin {
		writeError(w, http.StatusConflict, "conflict", "内置段 id 保留："+id+"（内置只读，换个 id）")
		return
	}
	if _, taken := agents.SegmentByID(id); taken {
		writeError(w, http.StatusConflict, "conflict", "段 id 被占了："+id)
		return
	}
	agents.Segments = append(agents.Segments, config.Segment{ID: id, Text: req.Text})
	if err := s.saveAgents(agents); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, config.Segment{ID: id, Text: req.Text})
}

// updateSegment：改内容 / 改名。改名 = 把所有 agent 的引用一起搬（同文件内，一次重写）。
func (s *Server) updateSegment(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
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

// deleteSegment：删段。**还被引用 ⇒ 拒绝**（列出引用者 —— 先把引用摘掉再删）。
func (s *Server) deleteSegment(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
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
	users := []string{}
	for _, agent := range agents.Agents {
		for _, ref := range agent.Segments {
			if ref == id {
				users = append(users, agent.Name)
				break
			}
		}
	}
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
