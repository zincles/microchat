// microchat Web API 的响应/请求形状（唯一权威是 core/internal 的 struct 与路由表）。
//
// 这一层只描述**线上 JSON**，不掺 Vue 状态。字段名与后端 `json:` tag 逐字对齐；
// `omitempty` / 指针字段用 `?` / `| null` 表达，别自己“补默认值”。
//
// 分阶段迁移（2026-11-06）：先让 api/ 有类型，utils 与 .vue 仍是 JS（tsconfig checkJs=false）。

export type Usage = Record<string, unknown>;
export type Role = "user" | "assistant";
export type TurnPhase = "idle" | "pending" | "streaming" | "error";
export type SummaryType = "message" | "summary";

export interface Message {
  id: string;
  session_id: string;
  role: Role;
  content: string;
  reasoning?: string;
  reasoning_ms?: number;
  duration_ms?: number;
  usage?: Usage | null;
  summary_id?: string;
  created_at: number;
  updated_at: number;
  idx?: number;
}

export interface Summary {
  id: string;
  session_id: string;
  parent_summary_id?: string;
  type: SummaryType;
  begin_message_id?: string;
  end_message_id?: string;
  begin_idx?: number;
  end_idx?: number;
  text: string;
  blocks: number;
  tokens: number;
  source_ids: string[];
  provider: string;
  model: string;
  prompt_version: number;
  usage?: Usage | null;
  dirty: boolean;
  created_at: number;
  /** 派生字段：只由 GET /summaries 填；没算过就不出现。 */
  level?: number;
  undoable?: boolean;
}

export interface Session {
  id: string;
  title: string;
  system_prompt: string;
  provider: string;
  model: string;
  agent_id: string;
  thinking_level: string;
  created_at: number;
  updated_at: number;
}

export interface TurnStatus {
  phase: TurnPhase;
  message_id?: string;
  elapsed_ms: number;
  chars: number;
  thinking_chars: number;
  error?: string;
}

export interface CompactStatus {
  state: "running" | "done" | "error";
  blocks: number;
  compacted: number;
  summary_id?: string;
  error?: string;
  at_ms: number;
  from_idx: number;
  to_idx: number;
  merged: boolean;
}

export interface RerollStatus {
  state: "running" | "done" | "error";
  elapsed_ms: number;
  error?: string;
}

/** GET /sessions/{id}/status 的形状（比 SessionView.turn 多两档）。 */
export interface SessionStatus extends TurnStatus {
  compact?: CompactStatus;
  reroll?: RerollStatus;
}

export interface SessionView extends Session {
  messages: number;
  turn: TurnStatus;
}

export interface Accepted {
  user?: Message;
  backend: string;
  turn: TurnStatus;
}

export interface StreamSlice {
  text: string;
  next: number;
  thinking: string;
  think_next: number;
  done: boolean;
}

export interface StreamCursor {
  from: number;
  thinkFrom: number;
}

export interface StreamAccumulator {
  text: string;
  reasoning: string;
}

export interface MergedStream {
  text: string;
  reasoning: string;
  cur: StreamCursor;
  reset: boolean;
}

export interface TurnPoll {
  status: SessionStatus;
  slice: StreamSlice;
}

export interface ContextUsage {
  used_tokens: number;
  budget_tokens: number;
  trigger_tokens: number;
  remaining_tokens: number;
  ctx_len: number;
  max_output: number;
  ratio: number;
  estimated: boolean;
  last_prompt_tokens: number;
  over_budget: boolean;
}

export interface StateOp {
  seq: number;
  table: string;
  kind: string;
  key: string;
  value: string | null;
  message_id: string | null;
  session_id: string | null;
  created_at: number;
}

export interface StateView {
  session: StateOp[];
  effective: Record<string, string>;
  tables: Record<string, Record<string, string>>;
}

export interface LastPayload {
  method: string;
  url: string;
  headers: Record<string, string>;
  body: unknown;
  at: number;
}

export interface PreviewResult {
  from_idx: number;
  to_idx: number;
  merged: boolean;
  blocks: number;
  source_ids: string[];
  action: string;
}

export interface PayloadPreview {
  payload: LastPayload;
  from_idx: number;
  to_idx: number;
  merged: boolean;
  blocks: number;
  messages: number;
  material_chars: number;
  state_before: boolean;
  state_after: boolean;
}

export interface AdvancedRequest {
  begin_idx?: number;
  end_idx?: number;
  step_blocks?: number;
  concurrency?: number;
  max_attempts?: number;
  repair?: boolean;
}

export interface CoverageHole {
  from_idx: number;
  to_idx: number;
}

export type AdvancedChunkState = "ready" | "pending" | "running" | "done" | "failed" | "canceled";

export interface AdvancedChunk {
  index: number;
  from_idx: number;
  to_idx: number;
  blocks: number;
  messages: number;
  state: AdvancedChunkState;
  attempts: number;
  error?: string;
}

export interface AdvancedPreview {
  mode: "normal" | "repair";
  begin_idx: number;
  end_idx: number;
  step_blocks: number;
  concurrency: number;
  max_attempts: number;
  chunks: AdvancedChunk[];
  holes?: CoverageHole[];
  total_requests: number;
  total_blocks: number;
  total_messages: number;
}

export interface AdvancedJob {
  id: string;
  session_id: string;
  mode: "normal" | "repair";
  state: "running" | "done" | "failed" | "canceled";
  error?: string;
  started_at: number;
  finished_at?: number;
  from_idx: number;
  to_idx: number;
  step_blocks: number;
  concurrency: number;
  max_attempts: number;
  chunks: AdvancedChunk[];
  holes?: CoverageHole[];
}

export interface RerollItem {
  idx: number;
  preview: string;
  at: number;
  pending: boolean;
  current: boolean;
}

export interface RerollState {
  active: boolean;
  target_kind?: "message" | "summary";
  target_message_id?: string;
  target_summary_id?: string;
  from_idx?: number;
  to_idx?: number;
  count: number;
  current_idx: number;
  running: boolean;
  elapsed_ms: number;
  error?: string;
  items: RerollItem[];
}

export interface RerollAccepted {
  target_message_id?: string;
  target_summary_id?: string;
  task_id?: string;
  state: RerollState;
}

export interface DeletionPlan {
  deleted_message_ids: string[];
  deleted_summary_ids: string[];
  unlinked_message_ids: string[];
  unlinked_summary_ids: string[];
  last_deleted_message_id: string;
}

export interface UndoItem {
  id: string;
  from_idx: number;
  to_idx: number;
  level: number;
}

export interface UndoPreview {
  summary_id: string;
  level: number;
  count: number;
  items: UndoItem[];
}

export type TaskKind =
  | "turn"
  | "reroll"
  | "compact"
  | "compact_job"
  | "title"
  | "judgement"
  | "refresh_models";

export type TaskState = "进行中" | "成功" | "失败" | "取消" | "中断";
export type TaskGroup = "llm" | "plain";

export interface TaskRecord {
  id: string;
  kind: TaskKind;
  group: TaskGroup;
  session?: string;
  title: string;
  started_at: number;
  finished_at?: number;
  outcome: TaskState;
  error?: string;
  cancelable?: boolean;
  parent_id?: string;
}

export interface QueueStep {
  session_id: string;
  blocks: number;
}

export interface Queue {
  id: string;
  kind: string;
  group: TaskGroup;
  title: string;
  index: number;
  total: number;
  state: TaskState;
  error?: string;
  started_at: number;
  finished_at?: number;
  current?: QueueStep;
}

export interface TaskBoard {
  running: number;
  tasks: TaskRecord[];
  now: number;
  queues: Queue[];
}

export interface EnqueueQueueRequest {
  kind?: string;
  title?: string;
  steps?: QueueStep[];
  session_id?: string;
  blocks?: number;
  times?: number;
}

export interface ModelView {
  upstream_id: string;
  name: string;
  upstream_name?: string | null;
  owned_by?: string | null;
  context_length?: number | null;
  max_output?: number | null;
  context_override?: number | null;
  display_name?: string | null;
  params?: unknown;
  upstream_params?: unknown;
  // model_define_rules 的派生结果（后端算，不落库）。
  brand?: string;
  icon?: string;
  thinking_levels?: string[];
  matched_rules?: string[];
}

export interface ModelListItem {
  provider: string;
  provider_name: string | null;
  upstream_id: string;
  name: string;
  display_name: string | null;
  thinking_levels: string[];
  brand?: string;
  icon?: string;
  matched_rules?: string[];
}

export interface SetModelRequest {
  provider: string;
  upstream_id: string;
  display_name?: string | null;
  context_override?: number | null;
  params?: unknown;
}

export interface ModelDefineRule {
  id?: string;
  match_contains?: string;
  match_exact?: string;
  match_provider?: string;
  is_final?: boolean;
  thinking_levels?: string[];
  brand?: string;
  icon?: string;
}

export interface ModelRulesConfig {
  model_define_rules: ModelDefineRule[];
}

export interface ProtocolOption {
  protocol: string;
  label: string;
  hint: string;
}

export interface PresetInfo {
  vendor: string;
  kind: string;
  name: string;
  base_url: string;
  needs_key: boolean;
  key_env: string;
  session_header?: string;
  protocols: string[];
  protocol_options: ProtocolOption[];
  primary: boolean;
  identities: string[];
}

export interface ProviderView {
  id: string;
  name: string | null;
  kind: string;
  vendor: string;
  protocol: string;
  vendor_name: string;
  protocol_name: string;
  protocol_options: ProtocolOption[];
  base_url: string;
  headers: Record<string, string>;
  has_key: boolean;
  model_filter: string;
  max_concurrency: number;
  identity: string;
  last_refresh_at: number | null;
  models: ModelView[];
  // 当前渠道自己的覆盖规则（应用在全局规则之后，优先）。
  model_define_rules?: ModelDefineRule[];
}

export interface TestProviderResult {
  ok: boolean;
  models: number;
  filtered: number;
  note?: string;
  error?: string;
}

export interface RefreshRoutesResult {
  provider: string;
  models: number;
  error?: string;
}

export interface AbilityToggle {
  enabled?: boolean;
  provider?: string;
  model?: string;
  model_source?: "global" | "session" | "agent" | string;
  prompt?: string;
  segments?: string[];
}

export interface Agent {
  id: string;
  name: string;
  system_prompt: string;
  first_message?: string;
  segments?: string[];
  abilities?: Record<string, AbilityToggle>;
  prepend_state?: boolean;
}

export interface Segment {
  id: string;
  text: string;
  imported?: boolean;
  builtin?: boolean;
}

export interface AgentsConfig {
  default_agent: string;
  segments?: Segment[];
  agents: Agent[];
}

export interface CreateAgentRequest {
  name: string;
  system_prompt?: string;
  first_message?: string;
  segments?: string[];
  abilities?: Record<string, AbilityToggle>;
  prepend_state?: boolean;
}

export interface UpdateAgentRequest {
  new_id?: string;
  make_default?: boolean;
  name?: string;
  system_prompt?: string;
  first_message?: string;
  segments?: string[];
  abilities?: Record<string, AbilityToggle>;
  prepend_state?: boolean;
}

export interface CreateSegmentRequest {
  id: string;
  text: string;
  imported?: boolean;
}

export interface UpdateSegmentRequest {
  new_id?: string;
  text?: string;
}

export interface ChatConfig {
  title_chars: number;
  title_provider?: string;
  title_model?: string;
  model_context_tokens: number;
  auto_compact?: boolean;
  compact_trigger_tokens?: number | null;
  compact_blocks: number;
  history_window: number;
  replay_reasoning?: boolean;
}

export interface DefaultsConfig {
  provider: string;
  model: string;
  agent: string;
}

export interface Background {
  kind?: "" | "url" | "file";
  value?: string;
  width?: number;
  height?: number;
  version?: string;
}

export interface MarkdownPrefs {
  enabled?: boolean;
  disabled?: string[];
}

export interface Prefs {
  markdown: MarkdownPrefs;
  theme?: string;
  text_scale?: string;
  font_body?: string;
  font_mono?: string;
  background?: Background;
  send?: string;
  preview?: boolean;
  payload_preview?: boolean;
  command_menu?: boolean;
  chrome_hide?: boolean;
  payload_viz?: boolean;
  bar_left_hidden?: boolean;
}

export interface MarkdownType {
  id: string;
  name: string;
  kind: "block" | "inline";
  sample: string;
}

export interface PrefsView {
  prefs: Prefs;
  markdown_types: MarkdownType[];
  defaults: Prefs;
}

export interface CreateSessionRequest {
  id?: string;
  title?: string;
  provider?: string;
  model?: string;
  agent_id?: string;
  system_prompt?: string;
  thinking_level?: string;
}

export interface UpdateSessionRequest {
  title?: string;
  provider?: string;
  model?: string;
  agent_id?: string;
  system_prompt?: string | null;
  thinking_level?: string;
}

export interface EditMessageRequest {
  content?: string | null;
  reasoning?: string | null;
}

export interface DraftOutgoingRequest {
  id?: string;
  provider?: string;
  model?: string;
  agent_id?: string;
  thinking_level?: string;
  content: string;
}

export interface CreateProviderRequest {
  id: string;
  name?: string;
  kind?: string;
  vendor?: string;
  protocol?: string;
  base_url?: string;
  headers?: Record<string, string>;
  api_key?: string;
  model_filter?: string;
  max_concurrency?: number;
  model_define_rules?: ModelDefineRule[];
}

export interface UpdateProviderRequest {
  new_id?: string;
  name?: string;
  vendor?: string;
  protocol?: string;
  base_url?: string;
  headers?: Record<string, string>;
  api_key?: string;
  model_filter?: string;
  max_concurrency?: number;
  model_define_rules?: ModelDefineRule[];
}

export interface TestProviderRequest {
  vendor?: string;
  protocol?: string;
  base_url?: string;
  api_key?: string;
  model_filter?: string;
}

export interface ExportMessage {
  role: Role;
  content: string;
  reasoning?: string;
}

export interface ExportSummary {
  key: string;
  parent_key?: string;
  type: SummaryType;
  begin_idx?: number;
  end_idx?: number;
  text: string;
  blocks: number;
  tokens: number;
  provider: string;
  model: string;
  prompt_version: number;
  usage?: Usage;
  dirty: boolean;
  created_at: number;
}

export interface ExportDocument {
  version: number;
  title: string;
  provider: string;
  model: string;
  agent_id: string;
  agent_name: string;
  messages: ExportMessage[];
  summaries: ExportSummary[];
}

export interface ImportJsonResult {
  session: Session;
  messages: number;
  summaries: number;
  agent_missing: boolean;
}

export interface ImportStResult {
  session: Session;
  messages: number;
  skipped: number;
}

export interface HealthResponse {
  status: string;
  version: string;
}

/** 版本更新：当前提交 / 分支 / 工作区状态。 */
export interface UpdateStatus {
  commit: string;
  short: string;
  branch: string;
  subject: string;
  dirty: boolean;
  changed_files: number;
  /** 本进程启动时间；重启后用它判断“回来的已经是新进程”。 */
  started_at: string;
  /** 当前部署是否有 entrypoint 看家循环（容器）。 */
  restart_supported: boolean;
  /** origin 目标分支最新提交；读不到时只有 remote_error。 */
  remote_commit?: string;
  remote_short?: string;
  remote_error?: string;
}

/** 拉取结果：前后两颗提交 + git 原文 + 是否已请求重启。 */
export interface UpdatePullResult {
  before: UpdateStatus;
  after: UpdateStatus;
  output: string;
  changed: boolean;
  restarting: boolean;
}

export interface StopResult {
  stopped: boolean;
}

export interface CancelTaskResult {
  canceled: boolean;
}

/** 统一错误面：message 是后端文案，code 原样带上。 */
export interface ApiError extends Error {
  code: string | null;
  status: number;
}

/** 酒馆原始导入：只存原始字节的元数据；正文从下载口按字节取。 */
export interface TavernPreset {
  id: string;
  filename: string;
  size: number;
  created_at: number;
}
