package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/store"
)

// 对话记录导出 / 导入（JSON；2026-10-11 起，2026-10-17 扩元数据，2026-10-28 加压缩金字塔）：
//
// v1（2026-10-11）：**只带足够重建对话的** —— 标题 + 每条 `{role, content}`。
// v2（2026-10-17 用户定："不止导入聊天记录，还导入当前 Agent / 渠道 / 模型"）：
//   **再加三个软引用** —— `provider` / `model` / `agent_id`（+ `agent_name` 只为报错时说得出口）。
//   就这三个：它们是会话自己的三格绑定（`sessions.provider/model/agent_id`），导回去"这条对话
//   当初是谁在什么模型上说的"就齐了。
// v3（2026-10-28 用户定："我们要完整的把那个金字塔结构导出出来，或者导入进去。必须做到"）：
//   **再加 `summaries`** —— 整张摘要金字塔（任意层级），外加每条消息的 `reasoning`。
//   为什么不带就"还原不了"：摘要正文是**模型写的**，再压一遍得到的是另一段字（`reroll-summary`
//   这个能力的存在本身就是证据）⇒ 它是不可重算的产物，不是缓存。从前导出去的会话导回来
//   等于"从没压过"，金字塔整棵没了。
//   形状与两个关键决定：
//     - 摘要在文件里用 `{key, parent_key}` 表达父子（**key = 导出时那条摘要的 id 原值**，只是
//       文件内的关联用途）。导入时**全部铸新 id** 再重映射 —— 照抄旧 id 会让同一个文件导两次
//       撞主键。
//     - 摘要自己的区间用 **`begin_idx` / `end_idx`**（1-based 消息序号），不是消息 id ——
//       导入后消息 id 全换新，用 id 会指空（与 `GET /summaries` 的 `begin_idx/end_idx` 同一把尺）。
//   **仍然不带**：system prompt（会话级覆盖）/ 消息的思考耗时与用量 / 消息 id / summary 的
//   `source_ids`（id 全换新，导入时按真实成员**重建**）。带回来就得面对"本机对不上怎么办"，
//   宁可少带、不许乱猜。
//   **agent 的"定义"不在这份文件里** ✗：人设搬家走 agent 卡带（`{version:2, agent, segments}`）；
//   本机没有那个 agent ⇒ 照绑 id（界面显示「不在列表」、人设回落内置默认），并在回执里
//   `agent_missing: true` 明说 —— **不静默换成 default_agent**（那等于悄悄换了个人）。
// 导入**不经过** `createSession` ⇒ 不会插 agent 的首段对话（JSON 自带完整对话）。
//
// 一条硬规矩（三个版本都成立）：**消息一条不少、正文一字不改**。压缩在库里从不改 `messages`
// 的正文（`RecordSummary` 只写 `summary_id`）⇒ 导出的是**全部原文**，包括已经被摘要盖住的那些；
// 导入也照原样落回去，绝不因为"这段被压过"就丢消息。

// sessionExport：导出的形状（导入也收它）。`version`：1 / 2 = 老文件（没有那三格 / 没有金字塔）。
type sessionExport struct {
	Version   int             `json:"version"`
	Title     string          `json:"title"`
	Provider  string          `json:"provider,omitempty"`
	Model     string          `json:"model,omitempty"`
	AgentID   string          `json:"agent_id,omitempty"`
	AgentName string          `json:"agent_name,omitempty"`
	Messages  []exportMessage `json:"messages"`
	// Summaries：v3 起。空 = 这条会话没压过（v1/v2 文件也解析成空）。
	Summaries []exportSummary `json:"summaries,omitempty"`
}

type exportMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// Reasoning：v3 起。模型的思考（界面折在那条回复下面那截）。老文件没有 ⇒ 空。
	Reasoning string `json:"reasoning,omitempty"`
}

// exportSummary：金字塔里的一条。
//
// `Key` 是**导出时那条摘要的 id**，只作文件内的关联句柄；`ParentKey` 是它父亲在同一个数组里的
// `Key`（空 = 顶层）。两者导入时都会被换成新 id。
//
// 其余字段照抄 `model.Summary`：`blocks` / `tokens` / `provider` / `model` / `prompt_version` /
// `usage` / `dirty` 都是"当初那次压缩的账"，带回来才有意义（`dirty` 尤其 —— 它是"材料被改过、
// 该重摇"的标记，不带就等于把所有摘要洗成新鲜的）。
type exportSummary struct {
	Key           string          `json:"key"`
	ParentKey     *string         `json:"parent_key,omitempty"`
	Type          string          `json:"type"` // message（成员是消息）/ summary（成员是摘要，金字塔的合并级）
	BeginIdx      *int            `json:"begin_idx,omitempty"`
	EndIdx        *int            `json:"end_idx,omitempty"`
	Text          string          `json:"text"`
	Blocks        int64           `json:"blocks,omitempty"`
	Tokens        int64           `json:"tokens,omitempty"`
	Provider      string          `json:"provider,omitempty"`
	Model         string          `json:"model,omitempty"`
	PromptVersion int64           `json:"prompt_version,omitempty"`
	Usage         json.RawMessage `json:"usage,omitempty"`
	Dirty         bool            `json:"dirty,omitempty"`
	CreatedAt     int64           `json:"created_at,omitempty"`
}

// exportSession：`GET /sessions/{session_id}/export` —— 会话域（owner 自己就能导）。
func (s *Server) exportSession(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	messages, err := s.store.ListMessages(session.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	summaries, err := s.store.ListSummaries(session.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out := sessionExport{
		Version: 3, Title: session.Title,
		Provider: session.Provider, Model: session.Model, AgentID: session.AgentID,
		AgentName: s.agentName(session.AgentID),
		Messages:  make([]exportMessage, 0, len(messages)),
		Summaries: make([]exportSummary, 0, len(summaries)),
	}
	for _, m := range messages {
		out.Messages = append(out.Messages, exportMessage{
			Role: string(m.Role), Content: m.Content, Reasoning: m.Reasoning,
		})
	}
	// 摘要：`Key` 直接用它自己的 id（文件内的句柄，导回去会换新 id）。
	// `ListSummaries` 已经按 id 升序 ⇒ 父必然排在子**后面**（父是后压出来的）。导入不依赖这个顺序
	// （父指针最后统一回填），但这样文件读起来就是"一层层往上的时间线"。
	for _, sum := range summaries {
		item := exportSummary{
			Key: sum.ID, Type: string(sum.Type),
			BeginIdx: sum.BeginIdx, EndIdx: sum.EndIdx,
			Text: sum.Text, Blocks: sum.Blocks, Tokens: sum.Tokens,
			Provider: sum.Provider, Model: sum.Model, PromptVersion: sum.PromptVersion,
			Usage: sum.Usage, Dirty: sum.Dirty, CreatedAt: sum.CreatedAt,
		}
		if sum.ParentSummaryID != nil {
			parent := *sum.ParentSummaryID
			item.ParentKey = &parent
		}
		out.Summaries = append(out.Summaries, item)
	}
	writeJSON(w, http.StatusOK, out)
}

// importJSON：`POST /sessions/import-json` —— 从导出的 JSON 建一条新会话。
//
// 校验**严格**（这是我们自己的格式，不是抓来的脏数据）：role 只许 user / assistant、
// 空 messages ⇒ 400；标题给了就用，没给 ⇒ 首条 user 起（与 ST 导入同口径）。
// 写口 = 设置面（admin）——与 ST 导入一致。
func (s *Server) importJSON(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var req sessionExport
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Version > 3 {
		writeError(w, http.StatusBadRequest, "invalid",
			fmt.Sprintf("这份文件的 version = %d 比本程序认识的（3）新：请升级后再导入", req.Version))
		return
	}
	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "invalid", "没有可导入的消息")
		return
	}
	parsed := make([]store.ImportMessage, 0, len(req.Messages))
	for index, m := range req.Messages {
		role := strings.TrimSpace(m.Role)
		if role != "user" && role != "assistant" {
			writeError(w, http.StatusBadRequest, "invalid",
				fmt.Sprintf("第 %d 条的 role 只许 user / assistant（得到 %q）", index+1, m.Role))
			return
		}
		parsed = append(parsed, store.ImportMessage{
			IsUser: role == "user", Content: m.Content, Reasoning: m.Reasoning,
		})
	}
	// v3 的摘要：**在这里就验完**（铸新 id、重映射父子、挡环、挡越界）。
	// 放在建会话之前 —— 一份坏文件不该在库里留下一条空壳会话再让人自己去删。
	planned, err := planSummaries(req.Summaries, len(parsed))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "摘要有问题："+err.Error())
		return
	}
	cfg, err := config.LoadConfig(s.paths)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	// v2 的三格软引用：文件给了就照抄（**渠道/模型不校验存在性** —— 本机没这个渠道也照样绑，
	// 界面会显示它、真发时当场报"未配置模型"，这比偷换成缺省诚实）；没给（v1 文件）⇒ 用本机缺省。
	provider, modelID := strings.TrimSpace(req.Provider), strings.TrimSpace(req.Model)
	if provider == "" {
		provider = cfg.Defaults.Provider
	}
	if modelID == "" {
		modelID = cfg.Defaults.Model
	}
	title := strings.TrimSpace(req.Title)
	session, err := s.store.CreateSession(identityOf(r.Context()).UserID, provider, modelID, "", title)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// agent：文件里点名了就绑它（本机有没有都绑 —— 软引用），只有**没点名**才用 default_agent。
	agentID := strings.TrimSpace(req.AgentID)
	agentMissing := agentID != "" && !s.agentExists(agentID)
	if agentID == "" {
		agentID = s.defaultAgentID()
	}
	if agentID != "" && agentID != session.AgentID {
		if err := s.store.SetSessionAgent(session.ID, agentID); err != nil {
			writeStoreError(w, err)
			return
		}
		session.AgentID = agentID
	}
	if err := s.store.ImportMessages(session.ID, parsed); err != nil {
		writeStoreError(w, err)
		return
	}
	if len(planned) > 0 {
		if err := s.store.RestoreSummaries(session.ID, planned); err != nil {
			// 摘要写不进去就把刚建的那条会话删掉：**半成品（有消息、没金字塔）比失败更糟** ——
			// 用户以为导进来了，其实压缩状态没了，而且不会再导第二次。
			_ = s.store.DeleteSession(session.ID)
			writeStoreError(w, err)
			return
		}
	}
	if title == "" {
		for _, item := range parsed {
			if item.IsUser && strings.TrimSpace(item.Content) != "" {
				_, _ = s.store.SetTitleIfEmpty(session.ID, model.TitleFrom(item.Content, cfg.Chat.TitleChars))
				break
			}
		}
	}
	sessionFresh, err := s.store.GetSession(session.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"session": sessionFresh, "messages": len(parsed),
		// summaries：v3 带回了几个压缩块（0 = 这条会话没压过，或文件是 v1/v2）。
		"summaries": len(planned),
		// agent_missing：文件点的 agent 本机没有 ⇒ 照绑了 id，但人设会回落内置默认（界面要说一句）。
		"agent_missing": agentMissing,
	})
}

// planSummaries：把文件里的摘要变成"可以直接落库"的一批 —— 验形状、铸新 id、重映射父子。
//
// 三件必须在**动库之前**拦住的事（坏文件不该留下半条会话）：
//   - `key` 非空且不重复（父子全靠它关联，重了就是两义）；
//   - `parent_key` 指得着，且**不成环** —— 环会让 `markDirty` 那条递归 CTE 一直转下去；
//   - 区间落在 `1..len(messages)` 里（越界 = 文件跟消息对不上，宁可报错也别猜）。
//
// 新 id **按文件顺序铸**（`MintOrderedIDs` 已排序）⇒ 导回来的金字塔照旧是"父排在子后面"的时间线。
func planSummaries(items []exportSummary, messageCount int) ([]store.ImportSummary, error) {
	if len(items) == 0 {
		return nil, nil
	}
	ids, err := store.MintOrderedIDs(len(items))
	if err != nil {
		return nil, err
	}
	remap := make(map[string]string, len(items))
	for index, item := range items {
		key := strings.TrimSpace(item.Key)
		if key == "" {
			return nil, fmt.Errorf("第 %d 条没有 key", index+1)
		}
		if _, seen := remap[key]; seen {
			return nil, fmt.Errorf("key 重复：%s", key)
		}
		remap[key] = ids[index]
	}

	out := make([]store.ImportSummary, 0, len(items))
	parents := make([]string, len(items)) // 每条的新父 id（空串 = 顶层），成环检查用
	for index, item := range items {
		summary := model.Summary{
			ID: ids[index], Text: item.Text, Blocks: item.Blocks, Tokens: item.Tokens,
			Provider: item.Provider, Model: item.Model, PromptVersion: item.PromptVersion,
			Usage: item.Usage, Dirty: item.Dirty, CreatedAt: item.CreatedAt,
		}
		// 手动写的文件可能省了 `type`：省了就按"成员是消息"读（我们自己的导出永远会写）。
		kind := strings.TrimSpace(item.Type)
		if kind == "" {
			kind = string(model.TypeMessages)
		}
		switch model.SummaryType(kind) {
		case model.TypeMessages, model.TypeSummaries:
			summary.Type = model.SummaryType(kind)
		default:
			return nil, fmt.Errorf("第 %d 条的 type 只许 message / summary（得到 %q）", index+1, item.Type)
		}
		if strings.TrimSpace(summary.Text) == "" {
			return nil, fmt.Errorf("第 %d 条的正文是空的", index+1)
		}
		if item.ParentKey != nil && strings.TrimSpace(*item.ParentKey) != "" {
			parent, ok := remap[strings.TrimSpace(*item.ParentKey)]
			if !ok {
				return nil, fmt.Errorf("第 %d 条指着一条不在文件里的父摘要：%s", index+1, *item.ParentKey)
			}
			parents[index] = parent
		}
		begin, end := 0, 0
		if item.BeginIdx != nil {
			begin = *item.BeginIdx
		}
		if item.EndIdx != nil {
			end = *item.EndIdx
		}
		if begin > 0 || end > 0 {
			if begin < 1 || end < begin || end > messageCount {
				return nil, fmt.Errorf("第 %d 条的区间 %d-%d 不在 1-%d 里", index+1, begin, end, messageCount)
			}
		}
		out = append(out, store.ImportSummary{Summary: summary, BeginIdx: begin, EndIdx: end})
	}

	// 成环检查：从每条往上走，步数超过条数就一定绕回来了（parent 已全是新 id）。
	byID := make(map[string]string, len(parents))
	for index, id := range ids {
		byID[id] = parents[index]
	}
	for index, id := range ids {
		seen := 0
		for cursor := byID[id]; cursor != ""; cursor = byID[cursor] {
			seen++
			if seen > len(ids) {
				return nil, fmt.Errorf("第 %d 条的父链成环了", index+1)
			}
		}
	}
	// 父指针最后才写进 Summary：`insertSummary` 会把它先置 NULL 再回填，这里只是把关系带上。
	for index := range out {
		if parents[index] == "" {
			continue
		}
		parent := parents[index]
		out[index].Summary.ParentSummaryID = &parent
	}
	return out, nil
}
