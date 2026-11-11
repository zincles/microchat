package server

import "net/http"

// flowRun：FlowAgent 最近一次图执行记录（进程内，覆盖式）。
// 普通 Agent / 还没跑过的 FlowAgent ⇒ 404（别回一份空轨迹）。
func (s *Server) flowRun(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	trace, ok := s.chat.FlowTrace(session.ID)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "这个会话还没有 FlowAgent 执行记录")
		return
	}
	writeJSON(w, http.StatusOK, trace)
}
