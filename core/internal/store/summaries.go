package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"microchat/internal/model"
)

// RecordSummary：**压缩的落库口** —— 一个事务里插一行摘要，再把**它的成员**指过去。
//
// 成员是谁由 `summary.Type` 定（**同质**：一条摘要的成员不许混）：
//
//	message ⇒ 回填那一段消息的 `summary_id`（`messages` 上唯一允许被压缩改的那一格）；
//	summary ⇒ 回填那批子摘要的 `parent_summary_id`（合并级）。
//
// **绝不插 / 改 / 删 `messages` 的其它列** —— 正文是存档（`AGENTS.md` 的第一条不变量）。
//
// 顺序：先插摘要（外键指着它），再更新指针。
//
// 消息级的两处重核（调用上游那段时间里别人可能动过库）：
//   - `sourceIDs` 必须**一条不少地**属于这条会话（少一条 = 那段被删/改了，区间不再自洽）；
//   - 那些消息必须**都还没被覆盖**（`summary_id IS NULL`）⇒ 谁先到谁算，后到的**报错**
//     （"不许覆盖已压缩的区间"这条闸在这里落地，不在调用点）。
//
// 合并级**不加 SELECT 预检**：那条 `parent_summary_id IS NULL` 的守卫 UPDATE 就是复核
// （摘要 append-only、删消息级联整链 ⇒ "行数对不上"已经足够说明别人抢过 / 区间变了）。
//
// `sessions.updated_at` **不动**：压的是派生数据，对话本身没变（列表排序该按"最后一次说话"）。
func (s *Store) RecordSummary(summary model.Summary, sourceIDs []string) error {
	switch summary.Type {
	case model.TypeMessages, model.TypeSummaries:
	default:
		return InvalidError("摘要的成员只能是消息或摘要（type 不认识：" + string(summary.Type) + "）")
	}
	if len(sourceIDs) == 0 {
		return InvalidError("压缩要盖住至少一条（空区间不落库）")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if summary.Type == model.TypeSummaries {
		if err := insertSummary(tx, summary); err != nil {
			return err
		}
		updated, err := updateSummaryParent(tx, summary.SessionID, summary.ID, sourceIDs)
		if err != nil {
			return err
		}
		if updated != len(sourceIDs) {
			return InvalidError("要并的这几条已经不是原来那几条顶层摘要了（被抢过 / 被删过）：重新取一次")
		}
		return tx.Commit()
	}

	covered, err := coveredCount(tx, summary.SessionID, sourceIDs)
	if err != nil {
		return err
	}
	if covered != len(sourceIDs) {
		return InvalidError("这段区间里的消息已经不是原来那几条了（被删过 / 换过会话）：重新取一次")
	}
	already, err := alreadyCovered(tx, summary.SessionID, sourceIDs)
	if err != nil {
		return err
	}
	if already != 0 {
		return InvalidError("这段区间里已经有消息被摘要盖住了：压缩不许覆盖已压缩的区间")
	}
	if err := insertSummary(tx, summary); err != nil {
		return err
	}
	updated, err := updateMessageSummary(tx, summary.SessionID, summary.ID, sourceIDs)
	if err != nil {
		return err
	}
	if updated != len(sourceIDs) {
		return InvalidError("这段区间里的消息已经不是原来那几条了（被删过 / 换过会话）：重新取一次")
	}
	return tx.Commit()
}

// ReplaceSummaryText：**就地换一条【顶层】摘要的正文与随行数据**（摘要重摇的 apply）。
//
// 与 `ReplaceMessage`（改正文那条路）同一套约定：**不换 id**（⇒ 摘要的覆盖区间、
// 它盖的那段消息、以及它自己的成员指针一概不动），只换正文与随行的那几格。
//
// 代价与随行数据都按"这一版摘要"重新落：`text / tokens / provider / model /
// prompt_version / usage / dirty`；其余列（含 `blocks / source_kind / source_ids /
// begin_message_id / end_message_id / parent_summary_id / created_at`）一个字都不动。
//
// 守卫 `parent_summary_id IS NULL` 只认**仍顶层**的那条：换正文是"重摇后替换"，
// 目标若已被并走（成了子摘要）或已被删 ⇒ 0 行 ⇒ `ErrNotFound`（与改正文一致）。
// **不碰 `messages`**（正文是存档）。
func (s *Store) ReplaceSummaryText(updated model.Summary) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return execTouchLocked(s.db,
		`UPDATE summaries
		    SET text = ?1, tokens = ?2, provider = ?3, model = ?4,
		        prompt_version = ?5, usage = ?6, dirty = ?7
		  WHERE id = ?8 AND session_id = ?9 AND parent_summary_id IS NULL`,
		updated.Text, updated.Tokens, updated.Provider, updated.Model,
		updated.PromptVersion, usageArg(updated.Usage), dirtyArg(updated.Dirty),
		updated.ID, updated.SessionID)
}

// SummaryEdit：改一条摘要的字段（nil = 不动）。
//
// Text：梗概正文（"" = 不许：空摘要没有意义 ⇒ InvalidError，与压缩的"剔完是空的不写"同一条规矩）。
type SummaryEdit struct {
	Text *string
}

// UpdateSummaryText：手改**任意**一条摘要的正文（**就地**换 text，不换 id、不碰区间与指针）。
//
// 与 `ReplaceSummaryText`（摘要重摇的 apply）同一套约定：只换正文，其余列一个字都不动；
// 差别是**不要求顶层**（手改孩子也看得见 —— 装配按区间跳，孩子正文照用），
// 且改完**把各级祖先标脏**（父盖着被改过的内容 ⇒ 脏，与"改被覆盖的消息标脏"同一条规矩）。
func (s *Store) UpdateSummaryText(sessionID, summaryID string, edit SummaryEdit) (model.Summary, error) {
	if edit.Text == nil {
		return model.Summary{}, InvalidError("改摘要：text 必填（不给等于没改）")
	}
	if strings.TrimSpace(*edit.Text) == "" {
		return model.Summary{}, InvalidError("改摘要：text 不许是空的（空摘要没有意义）")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return model.Summary{}, err
	}
	defer func() { _ = tx.Rollback() }()
	now := nowMS()
	if err := execTouchLocked(tx,
		"UPDATE summaries SET text = ?1, dirty = 0 WHERE id = ?2 AND session_id = ?3",
		strings.TrimSpace(*edit.Text), summaryID, sessionID); err != nil {
		return model.Summary{}, err
	}
	// 改的摘要若有爹 ⇒ 各级祖先标脏（父盖着被改过的内容）
	if _, err := tx.Exec(
		`WITH RECURSIVE chain(id, parent_summary_id) AS (
		     SELECT id, parent_summary_id FROM summaries WHERE id = ?1
		     UNION ALL
		     SELECT summaries.id, summaries.parent_summary_id
		     FROM summaries JOIN chain ON summaries.id = chain.parent_summary_id
		 )
		 UPDATE summaries SET dirty = 1 WHERE id IN (SELECT parent_summary_id FROM chain WHERE parent_summary_id IS NOT NULL)`,
		summaryID); err != nil {
		return model.Summary{}, err
	}
	updated, err := scanSummary(tx.QueryRow("SELECT "+summaryColumns+" FROM summaries WHERE id = ?1", summaryID))
	if err != nil {
		return model.Summary{}, err
	}
	if err := fillSpanIdx(tx, sessionID, &updated); err != nil {
		return model.Summary{}, err
	}
	if _, err := tx.Exec("UPDATE sessions SET updated_at = ?1 WHERE id = ?2", now, sessionID); err != nil {
		return model.Summary{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Summary{}, err
	}
	return updated, nil
}

// coveredCount：这批 id 里**属于这条会话**的有几条（少一条都说明区间变了）。
// **调用方持事务**。
func coveredCount(tx *sql.Tx, sessionID string, messageIDs []string) (int, error) {
	query, args := idBatchQuery("SELECT COUNT(*) FROM messages WHERE session_id = ?1 AND id IN (",
		sessionID, messageIDs)
	var count int
	if err := tx.QueryRow(query, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// alreadyCovered：这批 id 里**已经被摘要盖住**的有几条（一个都不许有）。**调用方持事务**。
func alreadyCovered(tx *sql.Tx, sessionID string, messageIDs []string) (int, error) {
	query, args := idBatchQuery(
		"SELECT COUNT(*) FROM messages WHERE session_id = ?1 AND summary_id IS NOT NULL AND id IN (",
		sessionID, messageIDs)
	var count int
	if err := tx.QueryRow(query, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// updateMessageSummary：把这一段消息的 `summary_id` 指过去。**调用方持事务**。
func updateMessageSummary(tx *sql.Tx, sessionID, summaryID string, messageIDs []string) (int, error) {
	marks := make([]string, len(messageIDs))
	args := make([]any, 0, len(messageIDs)+2)
	args = append(args, summaryID, sessionID) // ?1 = summary_id，?2 = session_id
	for index, id := range messageIDs {
		marks[index] = "?" + strconv.Itoa(index+3)
		args = append(args, id)
	}
	result, err := tx.Exec("UPDATE messages SET summary_id = ?1 WHERE session_id = ?2 AND id IN ("+
		strings.Join(marks, ",")+")", args...)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	return int(affected), err
}

// updateSummaryParent：把这一段**子摘要**的 `parent_summary_id` 指过去。**调用方持事务**。
//
// 条件 `parent_summary_id IS NULL` 就是合并级那条复核：只认**还没爹**的（仍顶层），
// 谁先到谁算；`session_id` 保证不跨会话乱认爹。行数对不上 ⇒ 调用方报错并整笔回滚。
func updateSummaryParent(tx *sql.Tx, sessionID, summaryID string, childIDs []string) (int, error) {
	marks := make([]string, len(childIDs))
	args := make([]any, 0, len(childIDs)+2)
	args = append(args, summaryID, sessionID) // ?1 = parent_summary_id，?2 = session_id
	for index, id := range childIDs {
		marks[index] = "?" + strconv.Itoa(index+3)
		args = append(args, id)
	}
	result, err := tx.Exec("UPDATE summaries SET parent_summary_id = ?1 "+
		"WHERE session_id = ?2 AND parent_summary_id IS NULL AND id IN ("+
		strings.Join(marks, ",")+")", args...)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	return int(affected), err
}

// idBatchQuery：拼一条 `… IN (?2, ?3, …)` 的查询（`?1` 留给调用方第一个参数）。
func idBatchQuery(prefix, sessionID string, ids []string) (string, []any) {
	marks := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, sessionID)
	for index, id := range ids {
		marks[index] = "?" + strconv.Itoa(index+2)
		args = append(args, id)
	}
	return prefix + strings.Join(marks, ",") + ")", args
}

// markSummaryChainDirty：把"覆盖了这条消息"的摘要**及其各级祖先**标为过期。
// **在调用方的事务里跑**（递归 CTE 一趟上去：messages.summary_id → summaries.parent_summary_id → …）。
func markSummaryChainDirty(tx *sql.Tx, messageID string) error {
	_, err := tx.Exec(
		`WITH RECURSIVE chain(id, parent_summary_id) AS (
		     SELECT summaries.id, summaries.parent_summary_id
		     FROM messages JOIN summaries ON summaries.id = messages.summary_id
		     WHERE messages.id = ?1
		     UNION ALL
		     SELECT summaries.id, summaries.parent_summary_id
		     FROM summaries JOIN chain ON summaries.id = chain.parent_summary_id
		 )
		 UPDATE summaries SET dirty = 1 WHERE id IN (SELECT id FROM chain)`, messageID)
	return err
}

// summaryColumns：摘要自己的列。`begin_message_id` / `end_message_id` = 它盖住的那一段
// （线性会话里的起止消息，闭区间；装配按它 O(1) 跳过去）。两端都可为空 ⇒ 那条摘要不跳（逐条走）。
const summaryColumns = "id, session_id, parent_summary_id, source_kind, begin_message_id, end_message_id, " +
	"text, blocks, tokens, source_ids, provider, model, prompt_version, usage, dirty, created_at"

func scanSummary(row scanner) (model.Summary, error) {
	var summary model.Summary
	var parent, begin, end, sourceIDs, usage sql.NullString
	var dirty int64
	err := row.Scan(
		&summary.ID, &summary.SessionID, &parent, &summary.Type, &begin, &end,
		&summary.Text, &summary.Blocks, &summary.Tokens, &sourceIDs,
		&summary.Provider, &summary.Model, &summary.PromptVersion, &usage, &dirty, &summary.CreatedAt,
	)
	if err != nil {
		return model.Summary{}, err
	}
	if parent.Valid {
		summary.ParentSummaryID = &parent.String
	}
	if begin.Valid {
		summary.BeginMessageID = &begin.String
	}
	if end.Valid {
		summary.EndMessageID = &end.String
	}
	if sourceIDs.Valid && sourceIDs.String != "" {
		_ = json.Unmarshal([]byte(sourceIDs.String), &summary.SourceIDs)
	}
	if usage.Valid {
		summary.Usage = json.RawMessage(usage.String)
	}
	summary.Dirty = dirty != 0
	return summary, nil
}

// rowQuerier：*sql.DB 与 *sql.Tx 都能查一行（补序号两种上下文都要用）。
type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

// messageIdx：这条消息在会话里的**序号**（按 id 排第几条，1-based）——
// 与 messages.idx 同一把尺（派生、不落库）；消息不在 ⇒ ok=false。
func messageIdx(q rowQuerier, sessionID, messageID string) (int, bool, error) {
	var n, exists int
	err := q.QueryRow(
		`SELECT COUNT(*), COUNT(CASE WHEN id = ?2 THEN 1 END)
		   FROM messages WHERE session_id = ?1 AND id <= ?2`, sessionID, messageID).Scan(&n, &exists)
	if err != nil {
		return 0, false, err
	}
	return n, exists > 0, nil
}

// fillSpanIdx：给一条摘要补上区间两端的消息序号（界面画 `Summary #a-b`、分页切段要用）。
// 端点缺/悬空 ⇒ 那一端留缺省（前端按"不折"处理）。
func fillSpanIdx(q rowQuerier, sessionID string, summary *model.Summary) error {
	if summary.BeginMessageID != nil {
		if n, ok, err := messageIdx(q, sessionID, *summary.BeginMessageID); err != nil {
			return err
		} else if ok {
			summary.BeginIdx = &n
		}
	}
	if summary.EndMessageID != nil {
		if n, ok, err := messageIdx(q, sessionID, *summary.EndMessageID); err != nil {
			return err
		} else if ok {
			summary.EndIdx = &n
		}
	}
	return nil
}

// listSummaries：按 id 顺序取这条会话的摘要（装配的行走、删除计划、Copy 都要用）。
// **调用方持锁**。
func (s *Store) listSummaries(sessionID string) ([]model.Summary, error) {
	rows, err := s.db.Query("SELECT "+summaryColumns+
		" FROM summaries WHERE session_id = ?1 ORDER BY id", sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	summaries := []model.Summary{}
	for rows.Next() {
		summary, err := scanSummary(rows)
		if err != nil {
			return nil, err
		}
		if err := fillSpanIdx(s.db, sessionID, &summary); err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

// ListSummaries：公开版（持锁）。
func (s *Store) ListSummaries(sessionID string) ([]model.Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listSummaries(sessionID)
}

// insertSummary：插一行摘要（**父指针一律先写 NULL** —— `parent_summary_id` 是自引用外键，
// 父必须先存在；先全插再回填父指针，就与摘要的插入顺序无关）。**调用方持事务**。
func insertSummary(tx *sql.Tx, summary model.Summary) error {
	sourceIDs := summary.SourceIDs
	if sourceIDs == nil {
		sourceIDs = []string{} // 落 `[]` 而不是 `null`（列有默认值，别人也按数组读）
	}
	encoded, err := json.Marshal(sourceIDs)
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		`INSERT INTO summaries
		   (id, session_id, parent_summary_id, source_kind, begin_message_id, end_message_id,
		    text, blocks, tokens, source_ids, provider, model, prompt_version, usage, dirty, created_at)
		 VALUES (?1, ?2, NULL, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15)`,
		summary.ID, summary.SessionID, string(summary.Type),
		summary.BeginMessageID, summary.EndMessageID, summary.Text, summary.Blocks, summary.Tokens,
		string(encoded), summary.Provider, summary.Model, summary.PromptVersion,
		usageArg(summary.Usage), dirtyArg(summary.Dirty), summary.CreatedAt)
	return err
}

// —— 导入：把一整张金字塔压回一个新会话（`POST /sessions/import-json` 的 v3）——

// ImportSummary：一条要还原的摘要。
//
// `Summary.ID` / `ParentSummaryID` 由调用方**先换成新 id** —— 摘要在导入时全铸新 id（照抄会撞主键，
// 同一个文件导两次就炸），所以父子关系也得跟着重映射；`Summary.SessionID` 不看（一律用参数那个）。
// 区间用 **1-based 消息序号**（与 `messages.idx` 同一把尺），`0` = 这一端不写（库里可空同义）。
type ImportSummary struct {
	Summary  model.Summary
	BeginIdx int
	EndIdx   int
}

// RestoreSummaries：把一串摘要写进一个**刚导入的**会话，并把 `messages.summary_id` 回填回去。
//
// 为什么不能直接 `RecordSummary`：那条路是"新压一次"的闸（拒覆已覆盖区间、要求成员未被盖），
// 而导入是把**既成事实**搬进来 —— 金字塔的每一层都已经被盖过了。所以这里另开一条只服务导入的路，
// 但守住同样的不变量：一条摘要的区间自洽、父指针落地、`source_ids` 与真实成员一致。
//
// 三件容易错的事，各自挡住：
//   - **`source_ids` 重建**（不照抄文件）：id 在导入时全换新，照抄下来的全是悬空的旧 id。
//     消息级 = 区间里那几条消息的新 id；摘要级 = 父指针指向它的那几条子摘要的新 id。
//   - **父指针最后写**（`insertSummary` 一律先写 NULL，自引用外键要求父先存在）。
//   - **消息回填带 `summary_id IS NULL` 守卫**：区间重叠会在这里当场变成错误，而不是悄悄盖掉一条。
//
// 调用方必须在**消息已经导入完**之后调它（区间按消息序号解析）。
func (s *Store) RestoreSummaries(sessionID string, rows []ImportSummary) error {
	if len(rows) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	ids, err := messageIDsInOrder(tx, sessionID)
	if err != nil {
		return err
	}

	known := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row.Summary.ID == "" {
			return InvalidError("导入的摘要缺 id")
		}
		if known[row.Summary.ID] {
			return InvalidError("导入的摘要 id 重复：" + row.Summary.ID)
		}
		known[row.Summary.ID] = true
	}
	for _, row := range rows {
		if parent := row.Summary.ParentSummaryID; parent != nil && !known[*parent] {
			return InvalidError("导入的摘要指着一条不在文件里的父摘要：" + *parent)
		}
	}

	// leafRanges：消息级摘要的区间（回填 messages.summary_id 用）。
	type leafRange struct {
		id         string
		begin, end int
	}
	leafRanges := make([]leafRange, 0, len(rows))

	for _, row := range rows {
		summary := row.Summary
		summary.SessionID = sessionID
		if summary.Type != model.TypeMessages && summary.Type != model.TypeSummaries {
			return InvalidError("摘要的成员只能是消息或摘要（type 不认识：" + string(summary.Type) + "）")
		}
		// 区间：两头都给才写（只给一头 = 文件坏了，不猜）。
		summary.BeginMessageID, summary.EndMessageID = nil, nil
		begin, end := row.BeginIdx, row.EndIdx
		if begin > 0 || end > 0 {
			if begin < 1 || end < begin || end > len(ids) {
				return InvalidError(fmt.Sprintf(
					"摘要 %s 的区间 %d-%d 不在 1-%d 里", row.Summary.ID, begin, end, len(ids)))
			}
			beginID, endID := ids[begin-1], ids[end-1]
			summary.BeginMessageID, summary.EndMessageID = &beginID, &endID
		}
		if summary.CreatedAt <= 0 {
			summary.CreatedAt = nowMS()
		}
		// source_ids 重建（见函数注释第一条）。
		members := []string{}
		switch summary.Type {
		case model.TypeMessages:
			if summary.BeginMessageID != nil {
				members = append(members, ids[begin-1:end]...)
				leafRanges = append(leafRanges, leafRange{id: summary.ID, begin: begin, end: end})
			}
		case model.TypeSummaries:
			for _, child := range rows {
				if child.Summary.ParentSummaryID != nil && *child.Summary.ParentSummaryID == summary.ID {
					members = append(members, child.Summary.ID)
				}
			}
		}
		summary.SourceIDs = members
		if err := insertSummary(tx, summary); err != nil {
			return err
		}
	}

	// 父指针：全部插完之后再回填（自引用外键，父必须先存在）。
	for _, row := range rows {
		if row.Summary.ParentSummaryID == nil {
			continue
		}
		res, err := tx.Exec(
			`UPDATE summaries SET parent_summary_id = ?1
			  WHERE id = ?2 AND session_id = ?3 AND parent_summary_id IS NULL`,
			*row.Summary.ParentSummaryID, row.Summary.ID, sessionID)
		if err != nil {
			return err
		}
		if affected, err := res.RowsAffected(); err == nil && affected != 1 {
			return InvalidError("摘要的父指针没写进去：" + row.Summary.ID)
		}
	}

	// 消息回填：`summary_id IS NULL` 是重叠守卫（见函数注释第三条）。
	for _, span := range leafRanges {
		for index := span.begin - 1; index < span.end; index++ {
			res, err := tx.Exec(
				`UPDATE messages SET summary_id = ?1
				  WHERE id = ?2 AND session_id = ?3 AND summary_id IS NULL`,
				span.id, ids[index], sessionID)
			if err != nil {
				return err
			}
			if affected, err := res.RowsAffected(); err == nil && affected != 1 {
				return InvalidError(fmt.Sprintf(
					"消息 %d 已经被另一条摘要盖住了：两条消息级摘要的区间重叠（导入的文件有问题）", index+1))
			}
		}
	}
	return tx.Commit()
}

// messageIDsInOrder：这条会话的消息 id，按 `id` 升序 —— 下标 i 对应 `messages.idx = i+1`（同一把尺）。
// **调用方持事务**。
func messageIDsInOrder(tx *sql.Tx, sessionID string) ([]string, error) {
	rows, err := tx.Query(`SELECT id FROM messages WHERE session_id = ?1 ORDER BY id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
