// Package compact：高级压缩（2026-11-05）—— 把一段消息按块切成 N 个 chunk，
// 并发生成、每块可重试，最后**一个事务**把 N 条摘要一起写库。
//
// 第一版只做**消息级**：chunk 的成员是原始消息；合并级（摘要并摘要）先不在这里做。
// 修洞模式走同一套计划/生成/提交，只是 chunk 来源从“指定区间”换成“已封段之间的空洞”。
//
// 一条硬边界：计划阶段把块切好；生成阶段只读；提交阶段才碰库。任何一块最终失败 ⇒
// 整批不落库（与普通压缩“取消 = 没做过”同一条哲学）。
package compact

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"microchat/internal/abilities"
	"microchat/internal/blocks"
	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/task"

	"github.com/google/uuid"
)

// errAdvancedCanceled：用户按停的 sentinel —— 用来把"用户取消"与"某一块真失败后
// 内部掐断其他块"分开。没有它，第一个连接错误会被报成"已取消"，掩盖真正的失败原因。
var errAdvancedCanceled = errors.New("advanced compact canceled by user")

// AdvancedRequest：高级压缩/修洞的参数。
//
//	BeginIdx/EndIdx：1-based 消息闭区间；Repair 时两端可都空（扫全部），也可只扫一段。
//	StepBlocks：每个 chunk 吃几个完整 block（不是消息条数：块不能被劈开）。
//	Concurrency：本作业最多几个活跃请求；第一版只限作业，endpoint 级限流留到调度器。
//	MaxAttempts：单个 chunk 最多尝试几次（只重试 upstream/timeout，不重试参数/冲突）。
type AdvancedRequest struct {
	BeginIdx    int  `json:"begin_idx"`
	EndIdx      int  `json:"end_idx"`
	StepBlocks  int  `json:"step_blocks"`
	Concurrency int  `json:"concurrency"`
	MaxAttempts int  `json:"max_attempts"`
	Repair      bool `json:"repair"`
}

// CoverageHole：同一层两个已封段之间的未压缩区间（1-based 消息序号，闭区间）。
type CoverageHole struct {
	FromIdx int `json:"from_idx"`
	ToIdx   int `json:"to_idx"`
}

// AdvancedChunk：计划/作业里的一个 chunk。State 在 preview 里是 ready；
// 运行时是 pending / running / done / failed / canceled。
type AdvancedChunk struct {
	Index    int    `json:"index"`
	FromIdx  int    `json:"from_idx"`
	ToIdx    int    `json:"to_idx"`
	Blocks   int    `json:"blocks"`
	Messages int    `json:"messages"`
	State    string `json:"state"`
	Attempts int    `json:"attempts"`
	Error    string `json:"error,omitempty"`
}

// AdvancedPreview：只算不动的计划。Chunks 是真正会并发跑的 chunk；Holes 是扫描到的空洞。
type AdvancedPreview struct {
	Mode          string          `json:"mode"` // normal / repair
	BeginIdx      int             `json:"begin_idx"`
	EndIdx        int             `json:"end_idx"`
	StepBlocks    int             `json:"step_blocks"`
	Concurrency   int             `json:"concurrency"`
	MaxAttempts   int             `json:"max_attempts"`
	Chunks        []AdvancedChunk `json:"chunks"`
	Holes         []CoverageHole  `json:"holes,omitempty"`
	TotalRequests int             `json:"total_requests"`
	TotalBlocks   int             `json:"total_blocks"`
	TotalMessages int             `json:"total_messages"`
}

// AdvancedJob：一次高级压缩的整体状态。**整体只有 running / done / failed / canceled**；
// 每块自己的 state 只是进度细节，不是整体成功与否。
type AdvancedJob struct {
	ID          string          `json:"id"`
	SessionID   string          `json:"session_id"`
	Mode        string          `json:"mode"`
	State       string          `json:"state"`
	Error       string          `json:"error,omitempty"`
	StartedAt   int64           `json:"started_at"`
	FinishedAt  *int64          `json:"finished_at,omitempty"`
	FromIdx     int             `json:"from_idx"`
	ToIdx       int             `json:"to_idx"`
	StepBlocks  int             `json:"step_blocks"`
	Concurrency int             `json:"concurrency"`
	MaxAttempts int             `json:"max_attempts"`
	Chunks      []AdvancedChunk `json:"chunks"`
	Holes       []CoverageHole  `json:"holes,omitempty"`
	cancel      context.CancelFunc
}

type advancedChunkPlan struct {
	begin, end int // 0-based，闭区间
	blocks     int
	messages   int
}

type advancedPlan struct {
	mode        string
	begin, end  int
	chunks      []advancedChunkPlan
	holes       []CoverageHole
	stepBlocks  int
	concurrency int
	maxAttempts int
}

// PreviewAdvanced：只算计划，不挂号、不调上游、不写库。
func (s *Service) PreviewAdvanced(session model.Session, req AdvancedRequest) (AdvancedPreview, error) {
	plan, err := s.planAdvanced(session, req)
	if err != nil {
		return AdvancedPreview{}, err
	}
	return previewOfPlan(plan), nil
}

func previewOfPlan(plan *advancedPlan) AdvancedPreview {
	out := AdvancedPreview{
		Mode: plan.mode, StepBlocks: plan.stepBlocks, Concurrency: plan.concurrency,
		MaxAttempts: plan.maxAttempts, Holes: plan.holes,
		TotalRequests: len(plan.chunks),
	}
	if plan.begin >= 0 {
		out.BeginIdx = plan.begin + 1
	}
	if plan.end >= 0 {
		out.EndIdx = plan.end + 1
	}
	for index, chunk := range plan.chunks {
		out.Chunks = append(out.Chunks, AdvancedChunk{
			Index: index, FromIdx: chunk.begin + 1, ToIdx: chunk.end + 1,
			Blocks: chunk.blocks, Messages: chunk.messages, State: "ready",
		})
		out.TotalBlocks += chunk.blocks
		out.TotalMessages += chunk.messages
	}
	return out
}

// planAdvanced：加载一次消息/摘要，校验能力开关，按块切 chunk；不调用上游。
func (s *Service) planAdvanced(session model.Session, req AdvancedRequest) (*advancedPlan, error) {
	_, agents, _, err := config.Load(s.Paths)
	if err != nil {
		return nil, &Error{Code: "internal", Message: "读配置失败：" + err.Error()}
	}
	agent, _ := agents.Resolve(session.AgentID)
	setting := abilities.Resolve(agent, abilities.Compact)
	if !setting.Enabled {
		return nil, invalid("会话 %s 的 agent（%s）关掉了 compact 能力（abilities.compact.enabled = false）："+
			"压缩不做就是不做，不会降级执行", shortID(session.ID), agentLabel(agent))
	}
	messages, err := s.Store.ListMessages(session.ID)
	if err != nil {
		return nil, err
	}
	summaries, err := s.Store.ListSummaries(session.ID)
	if err != nil {
		return nil, err
	}
	all := blocks.Split(messages)
	step := req.StepBlocks
	if step <= 0 {
		step = 1
	}
	if step > 64 {
		return nil, invalid("每次压缩的对话段数不能超过 64。")
	}
	concurrency := req.Concurrency
	if concurrency <= 0 {
		concurrency = 2
	}
	if concurrency > 8 {
		concurrency = 8
	}
	attempts := req.MaxAttempts
	if attempts <= 0 {
		attempts = 5
	}
	if attempts > 10 {
		attempts = 10
	}
	plan := &advancedPlan{stepBlocks: step, concurrency: concurrency, maxAttempts: attempts}
	if req.Repair {
		plan.mode = "repair"
		begin, end := req.BeginIdx-1, req.EndIdx-1
		if req.BeginIdx <= 0 && req.EndIdx <= 0 {
			begin, end = 0, len(messages)-1
		}
		if begin < 0 || end >= len(messages) || begin > end {
			return nil, invalid("待补范围无效：这条对话共 %d 条消息，当前范围是 %d–%d。",
				len(messages), req.BeginIdx, req.EndIdx)
		}
		plan.begin, plan.end = begin, end
		plan.holes = coverageHoles(messages, summaries, begin, end)
		for _, hole := range plan.holes {
			if err := appendChunks(plan, messages, all, hole.FromIdx-1, hole.ToIdx-1); err != nil {
				return nil, err
			}
		}
		return plan, nil
	}

	plan.mode = "normal"
	if req.BeginIdx <= 0 || req.EndIdx <= 0 {
		return nil, invalid("请填写起始和结束的消息序号。")
	}
	if req.BeginIdx > len(messages) || req.EndIdx > len(messages) {
		return nil, invalid("所选范围超出这条对话：只有 %d 条消息（当前 %d–%d）。",
			len(messages), req.BeginIdx, req.EndIdx)
	}
	begin, end := req.BeginIdx-1, req.EndIdx-1
	if begin > end {
		return nil, invalid("起始序号不能大于结束序号。")
	}
	plan.begin, plan.end = begin, end
	if err := appendChunks(plan, messages, all, begin, end); err != nil {
		return nil, err
	}
	// 已经压过的区间不允许再走消息级（要合并得走普通合并；第一版高级压缩只吃原文）。
	for index := begin; index <= end; index++ {
		if messages[index].SummaryID != nil {
			return nil, invalid("所选范围包含已压缩内容（第 %d 条）。请换一段未压缩的消息，或先处理已有的摘要。", index+1)
		}
	}
	return plan, nil
}

// appendChunks：把一个消息区间按“完整块”切成若干 chunk。边界必须落在块边界上。
func appendChunks(plan *advancedPlan, messages []model.Message, all []blocks.Block, begin, end int) error {
	first, last := -1, -1
	for index, block := range all {
		if block.Begin == begin {
			first = index
		}
		if block.End == end {
			last = index
		}
	}
	if first < 0 {
		return invalid("所选范围的起点不在对话段边界上。请调整起止位置（对话段不能被拆开）。")
	}
	if last < 0 {
		return invalid("所选范围的结尾不在对话段边界上。请调整起止位置（对话段不能被拆开）。")
	}
	if first > last {
		return invalid("区间的块边界反了")
	}
	if last == len(all)-1 {
		return invalid("最后一段对话还在继续，暂时不能压缩。请等它完成后再试。")
	}
	for start := first; start <= last; start += plan.stepBlocks {
		stop := start + plan.stepBlocks - 1
		if stop > last {
			stop = last
		}
		span, err := messageRangeSpan(messages, all, all[start].Begin, all[stop].End)
		if err != nil {
			return err
		}
		plan.chunks = append(plan.chunks, advancedChunkPlan{
			begin: span.Begin, end: span.End, blocks: span.Blocks, messages: len(span.Messages),
		})
	}
	return nil
}

// coverageHoles：同层已封段之间的空洞（不把头部 / 尾部未压区算进来）。
// 用顶层摘要的区间合并；顶级摘要之间的 raw 区间才是“必须补”的洞。
func coverageHoles(messages []model.Message, summaries []model.Summary, begin, end int) []CoverageHole {
	positions := make(map[string]int, len(messages))
	for index, message := range messages {
		positions[message.ID] = index
	}
	type interval struct{ begin, end int }
	intervals := make([]interval, 0, len(summaries))
	for _, summary := range summaries {
		if summary.ParentSummaryID != nil {
			continue
		}
		childBegin, childEnd, ok := childRange(summary, positions)
		if !ok {
			continue
		}
		if childEnd < begin || childBegin > end {
			continue
		}
		if childBegin < begin {
			childBegin = begin
		}
		if childEnd > end {
			childEnd = end
		}
		intervals = append(intervals, interval{childBegin, childEnd})
	}
	if len(intervals) == 0 {
		return nil
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].begin < intervals[j].begin })
	merged := make([]interval, 0, len(intervals))
	for _, current := range intervals {
		if len(merged) == 0 || current.begin > merged[len(merged)-1].end+1 {
			merged = append(merged, current)
			continue
		}
		if current.end > merged[len(merged)-1].end {
			merged[len(merged)-1].end = current.end
		}
	}
	holes := make([]CoverageHole, 0, len(merged))
	cursor := merged[0].begin
	for index, current := range merged {
		if index > 0 && current.begin > cursor {
			holes = append(holes, CoverageHole{FromIdx: cursor + 1, ToIdx: current.begin})
		}
		if current.end+1 > cursor {
			cursor = current.end + 1
		}
	}
	return holes
}

// StartAdvanced：受理一次高级作业。计划/校验在 202 前完成；生成在后台并发跑。
func (s *Service) StartAdvanced(session model.Session, req AdvancedRequest) (AdvancedJob, error) {
	plan, err := s.planAdvanced(session, req)
	if err != nil {
		return AdvancedJob{}, err
	}
	if len(plan.chunks) == 0 {
		return AdvancedJob{}, invalid("没有可压缩的内容。")
	}
	if _, err := s.Turns.BeginCompact(session.ID, len(plan.chunks), time.Now().UnixMilli()); err != nil {
		return AdvancedJob{}, busy()
	}
	job := &AdvancedJob{
		ID: uuid.Must(uuid.NewV7()).String(), SessionID: session.ID, Mode: plan.mode,
		State: "running", StartedAt: time.Now().UnixMilli(), FromIdx: plan.begin + 1, ToIdx: plan.end + 1,
		StepBlocks: plan.stepBlocks, Concurrency: plan.concurrency, MaxAttempts: plan.maxAttempts,
		Holes: plan.holes,
	}
	for index, chunk := range plan.chunks {
		job.Chunks = append(job.Chunks, AdvancedChunk{
			Index: index, FromIdx: chunk.begin + 1, ToIdx: chunk.end + 1,
			Blocks: chunk.blocks, Messages: chunk.messages, State: "pending",
		})
	}
	ctx, cancelCause := context.WithCancelCause(context.Background())
	cancel := func() { cancelCause(errAdvancedCanceled) }
	job.cancel = cancel
	s.advMu.Lock()
	s.advancedJobs[job.ID] = job
	s.advBySession[session.ID] = job.ID
	s.advMu.Unlock()
	go s.runAdvanced(ctx, session, plan, job, cancelCause)
	return snapshotAdvanced(job), nil
}

// AdvancedJobStatus：按 job id 取快照（作业结束后仍保留在进程内）。
func (s *Service) AdvancedJobStatus(sessionID, jobID string) (AdvancedJob, error) {
	s.advMu.Lock()
	defer s.advMu.Unlock()
	job := s.advancedJobs[jobID]
	if job == nil || job.SessionID != sessionID {
		return AdvancedJob{}, &Error{Code: "not_found", Message: "找不到这次压缩任务。"}
	}
	return snapshotAdvanced(job), nil
}

// CancelAdvanced：按停整个高级作业（子请求共用同一个 ctx）。
func (s *Service) CancelAdvanced(sessionID, jobID string) (AdvancedJob, error) {
	s.advMu.Lock()
	job := s.advancedJobs[jobID]
	if job == nil || job.SessionID != sessionID {
		s.advMu.Unlock()
		return AdvancedJob{}, &Error{Code: "not_found", Message: "找不到这次压缩任务。"}
	}
	if job.State == "running" && job.cancel != nil {
		job.cancel()
	}
	snapshot := snapshotAdvanced(job)
	s.advMu.Unlock()
	return snapshot, nil
}

func snapshotAdvanced(job *AdvancedJob) AdvancedJob {
	out := *job
	out.cancel = nil
	out.Chunks = append([]AdvancedChunk(nil), job.Chunks...)
	out.Holes = append([]CoverageHole(nil), job.Holes...)
	return out
}

// friendlyAdvancedError：高级压缩页/任务面板给人看的失败原因。
// 参数/区间错误已经是人话；这里主要把网络层原文（dial tcp / connection refused / timeout）
// 翻成用户能行动的一句话，别把 endpoint URL 和 Go 错误串直接摆在面板上。
func friendlyAdvancedError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "任务已取消。"
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "connection refused"), strings.Contains(text, "dial tcp"),
		strings.Contains(text, "no such host"), strings.Contains(text, "network is unreachable"):
		return "无法连接上游服务，请检查渠道地址与网络。"
	case strings.Contains(text, "timeout"), strings.Contains(text, "deadline exceeded"):
		return "上游请求超时，请稍后重试。"
	}
	return err.Error()
}

// runAdvanced：一个父 Task 下并发跑 chunk；全部成功才进入 RecordSummaryBatch。
func (s *Service) runAdvanced(ctx context.Context, session model.Session, plan *advancedPlan, job *AdvancedJob, cancel context.CancelCauseFunc) {
	title := "分段压缩 · 会话 " + shortID(session.ID)
	if plan.mode == "repair" {
		title = "补齐空档 · 会话 " + shortID(session.ID)
	}
	guard := s.Tasks.Begin(task.KindCompactJob, &session.ID, title)
	defer guard.Interrupted()
	guard.Cancellable(func() { cancel(errAdvancedCanceled) })

	summaries, err := s.generateAdvanced(ctx, session, plan, job, cancel, guard.ID())
	if err == nil {
		err = s.Store.RecordSummaryBatch(summaries)
	}
	if err != nil {
		canceled := errors.Is(context.Cause(ctx), errAdvancedCanceled)
		if canceled {
			guard.Cancel()
			s.finishAdvanced(job, "canceled", "", nil)
			s.Turns.FinishCompact(session.ID, "canceled", "", "", 0, 0, 0, false)
			return
		}
		message := friendlyAdvancedError(err)
		guard.Fail(message)
		s.finishAdvanced(job, "failed", message, nil)
		s.Turns.FinishCompact(session.ID, "error", "", message, 0, 0, 0, false)
		return
	}
	guard.Succeed()
	summaryID, blocksTotal, from, to := "", 0, plan.begin+1, plan.end+1
	if len(summaries) > 0 {
		summaryID = summaries[0].ID
	}
	for _, chunk := range plan.chunks {
		blocksTotal += chunk.blocks
	}
	s.finishAdvanced(job, "done", "", summaries)
	s.Turns.FinishCompact(session.ID, "done", summaryID, "", blocksTotal, from, to, false)
}

func (s *Service) finishAdvanced(job *AdvancedJob, state, message string, summaries []model.Summary) {
	s.advMu.Lock()
	defer s.advMu.Unlock()
	if job.State != "running" {
		// cancel 端点只发 ctx 取消，不提前改状态；这里仍是 running。
	}
	job.State = state
	job.Error = message
	now := time.Now().UnixMilli()
	job.FinishedAt = &now
	for index := range job.Chunks {
		if job.Chunks[index].State == "done" || job.Chunks[index].State == "failed" {
			continue
		}
		if state == "done" {
			job.Chunks[index].State = "done"
		} else if state == "canceled" {
			job.Chunks[index].State = "canceled"
		} else if job.Chunks[index].State != "running" {
			job.Chunks[index].State = "canceled"
		}
	}
	_ = summaries
}

func (s *Service) generateAdvanced(ctx context.Context, session model.Session, plan *advancedPlan, job *AdvancedJob, cancel context.CancelCauseFunc, parentID string) ([]model.Summary, error) {
	results := make([]model.Summary, len(plan.chunks))
	var next int64
	var firstErr error
	var errMu sync.Mutex
	var wg sync.WaitGroup
	workers := plan.concurrency
	if workers > len(plan.chunks) {
		workers = len(plan.chunks)
	}
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				index := int(atomic.AddInt64(&next, 1) - 1)
				if index >= len(plan.chunks) || ctx.Err() != nil {
					return
				}
				s.setChunkState(job, index, "running", 0, "")
				summary, err := s.runAdvancedChunk(ctx, session, plan.chunks[index], job, index, plan.maxAttempts, parentID)
				if err != nil {
					s.setChunkState(job, index, "failed", 0, err.Error())
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
						cancel(err)
					}
					errMu.Unlock()
					return
				}
				results[index] = summary
				s.setChunkState(job, index, "done", 0, "")
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return results, nil
}

// runAdvancedChunk：一个 chunk 在父 Job 下挂一个**子 Task**（面板里 Job + 3 Task 的来源）。
// 子 Task 只管这一发上游请求；取消仍由父 Job 的 ctx 统一裁决。
func (s *Service) runAdvancedChunk(ctx context.Context, session model.Session, chunk advancedChunkPlan, job *AdvancedJob, index, maxAttempts int, parentID string) (model.Summary, error) {
	child := s.Tasks.BeginChild(task.KindCompact, &session.ID,
		fmt.Sprintf("压缩第 %d 段 · 第 %d–%d 条", index+1, chunk.begin+1, chunk.end+1), parentID)
	defer child.Interrupted()
	summary, err := s.generateChunkWithRetry(ctx, session, chunk, job, index, maxAttempts)
	if err == nil {
		child.Succeed()
		return summary, nil
	}
	if errors.Is(context.Cause(ctx), errAdvancedCanceled) {
		child.Cancel()
	} else {
		child.Fail(friendlyAdvancedError(err))
	}
	return model.Summary{}, err
}

func (s *Service) generateChunkWithRetry(ctx context.Context, session model.Session, chunk advancedChunkPlan, job *AdvancedJob, index, maxAttempts int) (model.Summary, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return model.Summary{}, ctx.Err()
		}
		// prepare 每次重试都重新读库：上游期间如果有人改了消息，后面的 prepare/提交会拒绝。
		prepared, err := s.prepare(session, Request{BeginIdx: chunk.begin + 1, EndIdx: chunk.end + 1})
		if err != nil {
			return model.Summary{}, err
		}
		generated, err := s.generateText(ctx, prepared)
		if err == nil {
			summary, _, err := s.buildSummary(prepared, generated)
			if err != nil {
				return model.Summary{}, err
			}
			return summary, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return model.Summary{}, ctx.Err()
		}
		if !retryableCompactError(err) || attempt == maxAttempts {
			return model.Summary{}, err
		}
		s.setChunkAttempts(job, index, attempt+1)
		delay := time.Duration(attempt) * 250 * time.Millisecond
		select {
		case <-ctx.Done():
			return model.Summary{}, ctx.Err()
		case <-time.After(delay):
		}
	}
	return model.Summary{}, lastErr
}

func retryableCompactError(err error) bool {
	var ce *Error
	if !errors.As(err, &ce) {
		return false
	}
	return ce.Code == "upstream"
}

func (s *Service) setChunkState(job *AdvancedJob, index int, state string, attempts int, message string) {
	s.advMu.Lock()
	defer s.advMu.Unlock()
	if index < 0 || index >= len(job.Chunks) {
		return
	}
	job.Chunks[index].State = state
	if attempts > 0 {
		job.Chunks[index].Attempts = attempts
	}
	job.Chunks[index].Error = strings.TrimSpace(message)
}

func (s *Service) setChunkAttempts(job *AdvancedJob, index, attempts int) {
	s.advMu.Lock()
	defer s.advMu.Unlock()
	if index >= 0 && index < len(job.Chunks) {
		job.Chunks[index].Attempts = attempts
	}
}
