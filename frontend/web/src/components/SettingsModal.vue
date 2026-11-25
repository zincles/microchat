<script setup lang="ts">
// 设置与聊天同级。目录负责查找，子页负责编辑；品牌与工作流可直达对应子页。
import { ref, computed, nextTick, useTemplateRef, watch, onMounted, onUnmounted } from "vue";
import { VTab, VTabs, VTabsWindow, VTabsWindowItem, VSwitch, VBtn, VTextField, VSelect, VTable, VMenu, VList, VListItem } from "vuetify/components";
import AgentEditor from "./AgentEditor.vue";
import AppearancePane from "./AppearancePane.vue";
import SegmentEditor from "./SegmentEditor.vue";
import assistantPreset from "../../presets/assistant.json";
import rpPreset from "../../presets/rp-agent.json";
import ProviderEditor from "./ProviderEditor.vue";
import ModelRulesEditor from "./ModelRulesEditor.vue";
import type { AgentTemplate } from "./AgentEditor.vue";
import UpdatePane from "./UpdatePane.vue";
import TavernInspector from "./TavernInspector.vue";
import SettingsHome from "./SettingsHome.vue";
import SettingsBack from "./SettingsBack.vue";
import CloseButton from "./CloseButton.vue";
import { ICONS } from "../assets/icons.ts";
import { sharedApi } from "../utils/prefs.ts";
import { groupModelsByProvider, modelOption, providerChoices, modelChoicesFor, modelKey, parseModelKey, providerDisplayName, sessionTitle } from "../utils/format.ts";
import type { ModelGroup } from "../utils/format.ts";
import { SETTINGS_SECTIONS, type SettingsLocation } from "../utils/settingsCatalog.ts";
import { downloadJSON, downloadBlob } from "../utils/download.ts";
import { cartridgeAgent } from "../utils/cartridge.ts";
import type { Agent, ChatConfig, DefaultsConfig, ProviderView, Segment, SessionView, TavernPreset } from "../api/types.ts";
import type { ConfirmOptions } from "../types/app.ts";

interface ChatForm {
  title_chars: string | number;
  title_model: string;
  model_context_tokens: string | number;
  history_window: string | number;
  compact_blocks: string | number;
  compact_trigger_tokens: string | number | null;
  replay_reasoning: boolean;
  auto_compact: boolean;
}

// models-changed：渠道/模型那摊变了（删除渠道会连模型一起没）⇒ 让 App 重拉顶栏的模型下拉。
// 与会话左栏那个 sessions-changed 是**两件事**，别合并（各自的读口不同）。
const emit = defineEmits<{
  close: [];
  "toggle-left": [];
  "sessions-changed": [deletedId?: string];
  "models-changed": [];
  "goto-session": [id: string];
  "prefs-saved": [];
  "chat-saved": [];
}>();
// ask：全局确认框（App 的 Overlays.confirmAsk）—— 设置里的删除（会话/渠道/Agent）也必须先摊开再点头。
// escBlocked：外面临时弹窗（会话设置）开着时，Esc 归它 —— 一次按键不许关两层。
const props = withDefaults(defineProps<{
  ask?: ((text: string, options?: ConfirmOptions) => Promise<boolean>) | null;
  escBlocked?: boolean;
  // 品牌进入更新，工作流进入特定智能体；空串进入完整设置目录。
  only?: "" | "agent" | "update";
  focusAgentId?: string;
}>(), {
  ask: null,
  escBlocked: false,
  only: "",
  focusAgentId: "",
});

const api = sharedApi();
const errMsg = (e: unknown): string => (e instanceof Error ? e.message : String(e));
const leftSvg = ICONS.panelLeft;

const settingsHome = useTemplateRef<InstanceType<typeof SettingsHome>>("settingsHome");
const settingsTitle = useTemplateRef<HTMLHeadingElement>("settingsTitle");
const settingsBody = useTemplateRef<HTMLDivElement>("settingsBody");
const tab = ref(props.only || "home");
const activeTitle = computed(() => {
  if (tab.value === "home") return "设置";
  return SETTINGS_SECTIONS.find((s) => s.id === tab.value)?.label ?? "设置";
});
watch(() => props.only, (v) => { tab.value = v || "home"; });
const chat = ref<ChatForm>({ title_chars: "", title_model: "", model_context_tokens: "", history_window: "", compact_blocks: "", compact_trigger_tokens: "", replay_reasoning: false, auto_compact: true });
const chatStatus = ref({ context: "", generation: "" });
const chatReady = ref(false);
const savingChat = ref(false);
const defaults = ref<DefaultsConfig>({ provider: "", model: "", agent: "" });
const defStatus = ref("");
const defaultsReady = ref(false);
const agents = ref<Agent[]>([]);
const segmentsLib = ref<Segment[]>([]); // 提示词段库（随 GET /agents 一起给）
const curDefault = ref("");
const editAgent = ref<Agent | "new" | null>(null);
const agentTemplate = ref<AgentTemplate | null>(null);
let pendingAgentFocus = props.focusAgentId;
const agentFlowOpen = computed(() => {
  const editingFlow = editAgent.value === "new"
    ? newAgentKind.value === "flow"
    : editAgent.value?.kind === "flow";
  return !!editingFlow && (props.only === "agent" || tab.value === "agent");
});
function focusRequestedAgent(): void {
  if (!pendingAgentFocus) return;
  const target = agents.value.find((agent) => agent.id === pendingAgentFocus);
  if (!target) return;
  pendingAgentFocus = "";
  tab.value = "agent";
  editExistingAgent(target);
}
watch(() => props.focusAgentId, (id) => {
  if (!id) return;
  pendingAgentFocus = id;
  focusRequestedAgent();
});
const newAgentKind = ref<"prompt" | "flow">("prompt"); // 新建时的两种类型
function startNewAgent(kind: "prompt" | "flow"): void {
  newAgentKind.value = kind;
  agentTemplate.value = null;
  editAgent.value = "new";
}
function editExistingAgent(agent: Agent): void {
  agentTemplate.value = null;
  editAgent.value = agent;
}
function closeAgentEditor(): void {
  editAgent.value = null;
  agentTemplate.value = null;
}
const editSeg = ref<Segment | "new" | null>(null); // null | 'new' | segment（「提示词」tab 内的二级页）
// 谁引用了这个段（「提示词」tab 列表与编辑器共用）。
const segRefs = (id: string): string[] => agents.value.filter((a) => (a.segments ?? []).includes(id)).map((a) => a.name || a.id);
// 列表里的“类型”是**当前配置的形态**，不是又一个手填字段：
// 绑了酒馆预设 ⇒ 整套预设接管；有手工提示词计划 ⇒ 计划；否则就是基础提示词。
// 将来 FlowAgent 落地时，这里再优先看 kind=flow —— 现在不先造一个只有一种值的字段。
function agentType(a: Agent): string {
  if (a.kind === "flow") return "FlowAgent";
  if (a.preset_id) return "酒馆预设";
  if ((a.prompt_entries ?? []).length) return "提示词计划";
  return "基础提示词";
}
function agentTypeTitle(a: Agent): string {
  if (a.kind === "flow") return "由 FlowGraph 驱动；不能调用另一个 FlowAgent";
  if (a.preset_id) return "已绑定酒馆预设：Agent 自己的提示词不参与";
  if ((a.prompt_entries ?? []).length) return "使用手工提示词计划";
  return "基础提示词 + 片段";
}
const providers = ref<ProviderView[]>([]);
// 顶栏/会话设置用的是同一份 `GET /models`（跨渠道拍平那一条）：默认值那两格的下拉也从它取。
const modelGroups = ref<ModelGroup[]>([]);
// 全局标题模型的下拉：跨渠道拍平（显示名带渠道前缀，两个渠道有同名模型时才分得清）。
const titleModelOptions = computed<Array<{ value: string; label: string }>>(() => modelGroups.value.flatMap((g) =>
  (g.items ?? []).map((item) => ({ value: item.value, label: `${g.provider} / ${item.label}` }))));
const editProvider = ref<ProviderView | "new" | null>(null); // null | 'new' | provider（二级编辑器）
// 渠道页本身也分两页：列表 / 全局模型规则（后者与列表同级，不塞进单个渠道的详情）。
const providerTab = ref<"list" | "global">("list");

async function openSetting({ page, anchor }: SettingsLocation): Promise<void> {
  if (anchor === "global-model-rules") {
    editProvider.value = null;
    providerTab.value = "global";
  }
  tab.value = page;
  await nextTick();
  if (tab.value !== page) return;
  const body = settingsBody.value;
  if (body) body.scrollTop = 0;
  const target = anchor && body?.querySelector<HTMLElement>(`#setting-${CSS.escape(anchor)}`);
  if (target) {
    target.scrollIntoView({ block: "center", inline: "nearest" });
    target.focus({ preventScroll: true });
  } else {
    settingsTitle.value?.focus({ preventScroll: true });
  }
}
async function backToDirectory(): Promise<void> {
  tab.value = "home";
  await nextTick();
  if (tab.value === "home") settingsHome.value?.restoreFocus();
}
const routesStatus = ref("");

async function loadChat(): Promise<void> {
  try {
    const c = await api.getChat();
    chat.value = {
      title_chars: c.title_chars ?? "",
      title_model: c.title_provider && c.title_model ? modelKey(c.title_provider, c.title_model) : "",
      model_context_tokens: c.model_context_tokens ?? "",
      history_window: c.history_window ?? "",
      compact_blocks: c.compact_blocks ?? "",
      compact_trigger_tokens: c.compact_trigger_tokens ?? "",
      replay_reasoning: !!c.replay_reasoning,
      auto_compact: !!c.auto_compact, // 缺字段 = 关（与后端同一口径；这套未经验证，默认不开）
    };
    chatReady.value = true;
  } catch (e) {
    const message = `读取对话设置失败：${errMsg(e)}`;
    chatStatus.value = { context: message, generation: message };
  }
}
async function saveChat(section: "context" | "generation"): Promise<void> {
  if (!chatReady.value || savingChat.value) return;
  const num = (v: unknown): number | undefined => (String(v ?? "").trim() === "" ? undefined : Number(v));
  const form = { ...chat.value };
  savingChat.value = true;
  try {
    const body: ChatConfig = await api.getChat();
    if (section === "context") {
      body.model_context_tokens = num(form.model_context_tokens) ?? body.model_context_tokens;
      body.history_window = num(form.history_window) ?? body.history_window;
      body.compact_blocks = num(form.compact_blocks) ?? body.compact_blocks;
      body.auto_compact = form.auto_compact;
      const trigger = num(form.compact_trigger_tokens);
      if (trigger === undefined) delete body.compact_trigger_tokens;
      else body.compact_trigger_tokens = trigger;
    } else {
      const model = parseModelKey(form.title_model);
      body.title_chars = num(form.title_chars) ?? body.title_chars;
      body.title_provider = model?.provider ?? "";
      body.title_model = model?.model ?? "";
      body.replay_reasoning = form.replay_reasoning;
    }
    await api.putChat(body);
    chatStatus.value[section] = "已保存";
    emit("chat-saved");
  } catch (e) { chatStatus.value[section] = `保存失败：${errMsg(e)}`; }
  finally { savingChat.value = false; }
}

async function loadDefaults(): Promise<void> {
  try {
    const d = await api.getDefaults();
    defaults.value = { provider: d.provider ?? "", model: d.model ?? "", agent: d.agent ?? "" };
    defaultsReady.value = true;
  } catch (e) { defStatus.value = `读取默认值失败：${errMsg(e)}`; }
}
async function saveDefaults(): Promise<void> {
  if (!defaultsReady.value) return;
  try {
    await api.putDefaults({ ...defaults.value });
    defStatus.value = "已保存";
  } catch (e) { defStatus.value = `保存失败：${errMsg(e)}`; }
}

// 会话行导出（2026-10-11）：与 ☰ 里那颗同一支笔（GET /sessions/{id}/export → 下载）。
async function exportSessionRow(s: SessionView): Promise<void> {
  try {
    const doc = await api.exportSession(s.id);
    downloadJSON(doc, `${s.title || "新会话"}.json`);
    // v3 起文件里还有整张摘要金字塔 ⇒ 说一声"压缩状态一起走了"，不然用户不知道它带没带。
    const packed = doc.summaries?.length ? `，压缩块 ${doc.summaries.length} 个` : "";
    sessionMgrStatus.value = `已导出 ${doc.messages.length} 条${packed}：${s.title || "新会话"}`;
  } catch (e) { sessionMgrStatus.value = `导出失败：${errMsg(e)}`; }
}

// Agent 卡带导入（2026-10-11）：卡带 = agent + 它引用到的用户段（id+原文）。
// 合并规则（用户定）：系统里已有同 id 的段（用户段或内置）⇒ **丢弃卡带那份**；没有 ⇒ 建（打「导入」标记）。
const cartridgeInput = ref<HTMLInputElement | null>(null);
const cartridgeStatus = ref("");
async function importCartridge(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  if (!file) return;
  cartridgeStatus.value = "导入中…";
  try {
    const card = cartridgeAgent(JSON.parse(await file.text()));
    const known = new Set(segmentsLib.value.map((s) => s.id));
    let created = 0;
    let skipped = 0;
    for (const seg of card.segments) {
      if (known.has(seg.id)) { skipped++; continue; }
      await api.createSegment({ id: seg.id, text: seg.text, imported: true });
      known.add(seg.id);
      created++;
    }
    const made = await api.createAgent(card.agent);
    cartridgeStatus.value = `已导入「${made.name}」（段：新建 ${created} / 复用 ${skipped}）`;
    await loadAgents();
  } catch (e) { cartridgeStatus.value = `导入失败：${errMsg(e)}`; }
  input.value = "";
}

// JSON 对话导入（2026-10-11）：与 ST 导入同口径 —— 文件框 → POST /sessions/import-json → 刷列表。
const jsonStatus = ref("");
async function importJSONFile(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  if (!file) return;
  jsonStatus.value = "导入中…";
  try {
    const doc = JSON.parse(await file.text()) as Record<string, unknown>;
    const out = await api.importJson(doc);
    const bits = [`已导入 ${out.messages} 条：${out.session?.title || "新会话"}`];
    // v3 起文件可能带着整张摘要金字塔（含被压缩消息的摘要，任意层级）。
    if (out.summaries) bits.push(`压缩块 ${out.summaries} 个`);
    // v2 起文件带着会话的三格绑定 ⇒ 回执里能看出"搬过来之后绑的是谁"。
    const bound = [out.session?.provider, out.session?.model].filter(Boolean).join("/");
    if (bound) bits.push(`渠道/模型 ${bound}`);
    // agent 本机没有 ⇒ 照绑了 id（不静默换人），但人设会回落内置默认 —— 必须说出来 + 给出下一步。
    if (out.agent_missing) {
      bits.push(`⚠ 智能体「${doc?.agent_name || doc?.agent_id || "?"}」本机没有：已按名称绑上，人设会回落内置默认；要人设请从「智能体」导入它的文件`);
    }
    jsonStatus.value = bits.join(" · ");
    await loadSessions();
    emit("sessions-changed");
  } catch (e) { jsonStatus.value = `导入失败：${errMsg(e)}`; }
  input.value = "";
}

// 已有对话管理器：ST 导入（文件框 → POST /sessions/import-st，导完刷会话 tab + 左栏）。
const importTitle = ref("");
const importStatus = ref("");
const stInput = ref<HTMLInputElement | null>(null);   // ST 的隐藏文件框（按钮负责长相，见下）
const jsonInput = ref<HTMLInputElement | null>(null); // 对话 JSON 的隐藏文件框
async function importSTFile(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  if (!file) return;
  importStatus.value = "导入中…";
  try {
    const text = await file.text();
    const out = await api.importSt(text, importTitle.value.trim() || undefined);
    importStatus.value = `已导入 ${out.messages} 条（跳过 ${out.skipped} 行）`;
    await loadSessions();
    emit("sessions-changed");
  } catch (e) { importStatus.value = `导入失败：${errMsg(e)}`; }
  input.value = "";
}
// 酒馆原始导入（2026-11-06）：只存原始字节；列表/删除/下载，不解析。
const tavernPresets = ref<TavernPreset[]>([]);
const tavernStatus = ref("");
const tavernInput = ref<HTMLInputElement | null>(null);
// 「内容」只把元数据交给查看器；原始字节由查看器按需下载，列表不背 1.5MB 的正文。
const inspectingPreset = ref<TavernPreset | null>(null);
function sizeText(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(2)} MB`;
}
function timeText(ms: number): string { return new Date(ms).toLocaleString(); }
async function loadTavernPresets(): Promise<void> {
  try { tavernPresets.value = await api.listTavernPresets(); }
  catch (e) { tavernStatus.value = `读取酒馆导入失败：${errMsg(e)}`; }
}
async function importTavernPresetFile(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  if (!file) return;
  tavernStatus.value = `保存 ${file.name} 中…`;
  try {
    const p = await api.uploadTavernPreset(file);
    tavernStatus.value = `已原样保存：${p.filename}（${sizeText(p.size)}）`;
    await loadTavernPresets();
  } catch (e) { tavernStatus.value = `保存失败：${errMsg(e)}`; }
  input.value = "";
}
function inspectTavernPreset(p: TavernPreset): void {
  inspectingPreset.value = p;
}
// 导入片段后刷新段库；Agent 编辑器的段下拉也读这一份。
function onPresetSegmentImported(): void {
  loadAgents().catch(() => {});
}
function presetLabel(id?: string): string {
  if (!id) return "";
  return tavernPresets.value.find((p) => p.id === id)?.filename || id;
}
function segmentSource(segment: Segment): string {
  if (!segment.preset_id) return "导入";
  return `导入自 ST 预设：${segment.preset_name || presetLabel(segment.preset_id)}`;
}
async function downloadTavernPreset(p: TavernPreset): Promise<void> {
  try { downloadBlob(await api.downloadTavernPreset(p.id), p.filename); }
  catch (e) { tavernStatus.value = `导出失败：${errMsg(e)}`; }
}
async function removeTavernPreset(p: TavernPreset): Promise<void> {
  if (props.ask && !(await props.ask(`删除原始预设「${p.filename}」？不可逆。`))) return;
  const linked = segmentsLib.value.filter((s) => s.imported && s.preset_id === p.id);
  // 有导入片段时再问一次：删预设不等于删片段，两者都可以是用户想要的。
  let mode: "delete" | "keep" | undefined;
  if (linked.length) {
    const also = props.ask ? await props.ask(
      `这个预设导入了 ${linked.length} 个提示词片段。要一并删除它们吗？

选「取消」会保留片段、只删预设。`,
    ) : false;
    mode = also ? "delete" : "keep";
  }
  try {
    await api.deleteTavernPreset(p.id, mode);
    tavernStatus.value = mode === "delete"
      ? `已删除预设与 ${linked.length} 个导入片段：${p.filename}`
      : `已删除：${p.filename}`;
    await loadTavernPresets();
    if (mode === "delete") await loadAgents();
  } catch (e) { tavernStatus.value = `删除失败：${errMsg(e)}`; }
}

// 会话 tab：列表 + 新建/进入/删除（编辑走左栏 ☰ 二级）。
const sessions = ref<SessionView[]>([]);
const sessionMgrStatus = ref("");

async function loadAgents(): Promise<void> {
  try {
    const data = await api.agents();
    // 后端允许 default_agent 缺失/空串，语义是“用内置 default”；
    // 列表的“是否默认”必须按生效值画，不然默认助手那一行永远空着。
    curDefault.value = data.default_agent || "default";
    agents.value = data.agents ?? [];
    segmentsLib.value = data.segments ?? [];
    focusRequestedAgent();
  } catch {}
}

// 预设模板（随 git 走：presets/*.json）：长提示词不硬编码，前端不存第二份真相。
const presetPick = ref("");
function presetNew(value?: unknown): void {
  const kind = value === undefined ? presetPick.value : String(value ?? "");
  presetPick.value = "";
  if (!kind) return;
  // 名字清空等你起；其余（提示词/prepend/能力）照模板带。
  const src = kind === "rp" ? rpPreset : assistantPreset;
  agentTemplate.value = { ...src, name: "" };
  editAgent.value = "new";
}

// 默认值那两格的下拉选项（纯函数在 format.js，测试直引）。
// 模型跟着渠道走：换了渠道却留着上一家的模型 id，等于存了一对配不上的值。
const providerOpts = computed(() => providerChoices(providers.value, defaults.value.provider));
const modelOpts = computed(() => modelChoicesFor(modelGroups.value, defaults.value.provider, defaults.value.model));
const titleSelectOptions = computed(() => [{ label: "使用当前模型", value: "" }, ...titleModelOptions.value]);
const providerSelectOptions = computed(() => [{ label: "内置默认（本地示例）", value: "" }, ...providerOpts.value]);
const modelSelectOptions = computed(() => [{ label: "内置默认（本地示例）", value: "" }, ...modelOpts.value]);
const agentSelectOptions = computed(() => [
  { label: "内置默认", value: "" },
  ...agents.value.map((a) => ({ label: a.name || a.id, value: a.id })),
]);
const presetOptions = [
  { label: "从模板创建…", value: "" },
  { label: "通用助手", value: "assistant" },
  { label: "角色扮演助手", value: "rp" },
];

// 换渠道时**把配不上的模型清掉**：留着上一家的 model id，新建会话就会拿一对
// "渠道 A + 只存在于 B 的模型" 去发 —— 那正是这套下拉要消灭的那种错误。
// 清成空 = 回落内置默认（dummy），比静默发错强。
function pickDefaultProvider(next: string): void {
  defaults.value.provider = next;
  const stillThere = modelChoicesFor(modelGroups.value, next).some((c) => c.value === defaults.value.model);
  if (!stillThere) defaults.value.model = "";
}
function onDefaultProviderChange(value: unknown): void {
  pickDefaultProvider(String(value ?? ""));
}

async function loadProviders(): Promise<void> {
  try {
    providers.value = (await api.providers()) ?? [];
  } catch (e) { routesStatus.value = `渠道列表失败：${errMsg(e)}`; }
  // 模型清单另一个读口（`/models` 跨渠道拍平）：拉失败不该拖垮渠道列表 ⇒ 单独 catch。
  try {
    const models = await api.models();
    modelGroups.value = groupModelsByProvider(models).map((g) => ({
      provider: g.provider,
      items: g.items.map(modelOption),
    }));
  } catch {}
}

// onProviderSaved：保存回执带 id —— **改名之后必须换成新对象**。
// 旧 props 的 id 已经不存在了，编辑器留在原地的话下一次保存（哪怕是清密钥）必 404。
// 顶栏的模型下拉也吃 `/models`（按渠道分组），改名后分组名要跟着换 ⇒ 通知 App 重拉。
async function onProviderSaved(id: string): Promise<void> {
  await loadProviders();
  if (id && editProvider.value && editProvider.value !== "new") {
    editProvider.value = providers.value.find((p) => p.id === id) ?? null;
  }
  emit("models-changed");
}

async function askDeleteProvider(id: string): Promise<void> {
  // 文案要说清"模型也一起没"（2026-10-22）：从前删渠道只删配置、模型留在列表里仍可选中，
  // 用户以为删干净了；现在真的连模型一起删 —— 那就得在确认框里说出来，别让人删完才发现。
  if (props.ask && !(await props.ask(`删除渠道「${id}」？密钥与它发现到的模型会一起删掉，不可逆。`))) return;
  try {
    await api.deleteProvider(id);
    if (editProvider.value && editProvider.value !== "new" && editProvider.value.id === id) editProvider.value = null;
    loadProviders().catch(() => {});
    // 顶栏"渠道 / 模型"下拉吃的是 `/models`（另一个读口），得让它也重拉一次。
    // ⚠ `sessions-changed` 是**会话左栏**的刷新（App 里只挂到 refreshSessionsBar）——
    // 别拿它冒充"模型列表已刷新"，那是两件事（我一开始就这么写错了，核过 App 才发现）。
    emit("models-changed");
  } catch (e) { routesStatus.value = `删除失败：${errMsg(e)}`; }
}

// 会话 tab 动作：进会话关设置面板（App 切；删除也要先确认 —— 与左栏同一个口）。
async function loadSessions(): Promise<void> {
  try {
    sessions.value = await api.listSessions();
  } catch (e) { sessionMgrStatus.value = `拉列表失败：${errMsg(e)}`; }
}
function gotoSession(id: string): void {
  emit("goto-session", id);
}
async function askDropSession(id: string, title: string): Promise<void> {
  if (props.ask && !(await props.ask(`「${sessionTitle(title)}」及其消息和摘要将永久删除，无法恢复。`, { destructive: true, action: "删除会话" }))) return;
  try {
    await api.deleteSession(id);
    await loadSessions();
    emit("sessions-changed", id);
  } catch (e) { sessionMgrStatus.value = `删除失败：${errMsg(e)}`; }
}

onMounted(() => {
  document.addEventListener("keydown", onKey);
  // 更新页自带 GET /update；别为它白拉设置页那六份数据。
  if (props.only === "update") return;
  loadChat().catch(() => {});
  loadDefaults().catch(() => {});
  loadAgents().catch(() => {});
  loadProviders().catch(() => {});
  loadSessions().catch(() => {});
  loadTavernPresets().catch(() => {});
});
onUnmounted(() => document.removeEventListener("keydown", onKey));
function onKey(e: KeyboardEvent): void {
  if (e.key !== "Escape" || document.querySelector(".confirm-dialog.v-overlay--active")) return;
  // 二级页（Agent/渠道详情）或外面临时弹窗开着 ⇒ Esc 归它们；一次按键只关一层（踩过：双关）。
  if (editAgent.value || editSeg.value || editProvider.value || props.escBlocked) return;
  emit("close");
}
</script>

<template>
  <section id="settings-view" :class="{ 'flow-agent-open': agentFlowOpen }">
    <div class="settings-bar">
      <SettingsBack v-if="!only && tab !== 'home'" label="返回设置目录" @back="backToDirectory" />
      <VBtn
        v-else
        variant="text"
        size="small"
        type="button"
        title="显示/隐藏会话栏"
        aria-label="显示/隐藏会话栏"
        @click="$emit('toggle-left')"
        v-html="leftSvg"
      ></VBtn>
      <h2 ref="settingsTitle" class="settings-title" tabindex="-1">{{ activeTitle }}</h2>
      <CloseButton id="settings-close" label="退出设置" title="退出设置（Esc）" @click="$emit('close')" />
    </div>

    <div ref="settingsBody" class="settings-body">
      <div class="settings-box">
        <div class="settings-cols">
        <!-- 会话/渠道是**页面切换**，不是设置页签：key 随 only 变 ⇒ 重挂载、不跑 tab 滑入。
             否则「渠道 → 设置」会从最后一个 window item 跳回 chat，方向反着走
             （2026-11-06 用户报「有 Tab 效果，而且顺序是错的」）。 -->
        <VTabsWindow v-model="tab" :key="props.only || 'settings'" class="settings-window">
        <VTabsWindowItem v-if="!only" value="home">
          <SettingsHome ref="settingsHome" @select="openSetting" />
        </VTabsWindowItem>
        <VTabsWindowItem value="defaults">
        <section id="panel-defaults" class="panel">
          <p class="settings-lead">只影响以后新建的会话；已有会话在对话顶栏或会话选项里修改。</p>

          <section class="settings-card">
          <h3>新会话默认</h3>
          <p class="hint">新建会话时自动带入；改动<b>不影响已有会话</b>。</p>
          <!-- 三格都是**下拉**（2026-10-28）：从前渠道/模型是手打 id 的文本框，说明文自己都写着
               「填错不会报错，只会在发送时回落成『未配置模型』」—— 那是把校验推给用户。
               现在能选出来的都真实存在；当前值即使不在列表里（渠道被删、手改过配置文件、
               或 dummy 这种永远发现不到模型的渠道）也会补一条并标明，**不许**静默丢掉。 -->
          <label id="setting-default-provider" tabindex="-1" class="field"><span>默认渠道</span><VSelect :model-value="defaults.provider" :items="providerSelectOptions" item-title="label" item-value="value" density="compact" variant="outlined" hide-details @update:model-value="onDefaultProviderChange" /></label>
          <label id="setting-default-model" tabindex="-1" class="field"><span>默认模型</span><VSelect v-model="defaults.model" :items="modelSelectOptions" item-title="label" item-value="value" density="compact" variant="outlined" hide-details /></label>
          <label id="setting-default-agent" tabindex="-1" class="field"><span>默认智能体</span><VSelect v-model="defaults.agent" :items="agentSelectOptions" item-title="label" item-value="value" density="compact" variant="outlined" hide-details /></label>
          <p class="hint" v-if="!modelOpts.length">这个渠道还没有拉到任何模型 —— 可以先留空用内置默认，或去「渠道」里点「保存并刷新」。</p>
          <p class="hint">留空 = 内置默认（本地示例模型，不联网，开箱就能回话）。</p>
          <div class="row action-row">
            <VBtn color="primary" variant="flat" size="small" type="button" :disabled="!defaultsReady" @click="saveDefaults">保存默认值</VBtn>
            <span class="status">{{ defStatus }}</span>
          </div>
          </section>
        </section>
        </VTabsWindowItem>
        <VTabsWindowItem value="appearance">
          <AppearancePane section="appearance" @prefs-saved="emit('prefs-saved')" />
        </VTabsWindowItem>
        <VTabsWindowItem value="input">
          <AppearancePane section="input" @prefs-saved="emit('prefs-saved')" />
        </VTabsWindowItem>
        <VTabsWindowItem value="context">
        <section id="panel-context" class="panel">
          <p class="settings-lead">调整消息加载、模型的输入预算以及自动压缩策略。</p>

          <section class="settings-card">
          <h3>上下文与加载</h3>
          <p class="hint">决定模型一次能带多少历史，以及消息列一次拉多少；不改变模型自己的上限。</p>
          <label id="setting-context-length" tabindex="-1" class="field"><span>上下文长度（token）</span><VTextField density="compact" variant="outlined" hide-details v-model="chat.model_context_tokens" type="number" min="1" /></label>
          <!-- 为什么右上角那枚环的分母常常比这里小：它是**输入预算** = 上下文 − 给回复留的输出额度。
               移动端没有 hover，所以这句话必须写在这儿（不能只藏在环的 title 里）。 -->
          <p class="hint">右上角那枚环显示的是<b>还能输入多少</b>：这里填的上下文长度减去给回复预留的空间。填 1000000 时通常显示 995904。</p>
          <label id="setting-history-window" tabindex="-1" class="field"><span>首屏加载条数</span><VTextField density="compact" variant="outlined" hide-details v-model="chat.history_window" type="number" min="1" placeholder="留空 = 5" /></label>
          <p class="hint">按消息段计数（一条消息或一张顶层摘要卡）；「加载更多」每页同样数量。远程部署时调小可减少首屏等待。</p>
          </section>

          <section class="settings-card">
          <h3>自动压缩</h3>
          <p class="hint">上下文快满时把较早的对话压成摘要；原文仍保留在会话里，撤销压缩可恢复。</p>
          <label id="setting-auto-compact" tabindex="-1" class="field"><span>启用自动压缩</span><VSwitch v-model="chat.auto_compact" color="primary" density="compact" hide-details /></label>
          <p class="hint">达到触发线就后台压缩，压到输入预算的 50% 以下停；关闭后只能手动压缩。</p>
          <label id="setting-compact-blocks" tabindex="-1" class="field"><span>每次压缩块数</span><VTextField density="compact" variant="outlined" hide-details v-model="chat.compact_blocks" type="number" min="1" placeholder="留空 = 3" /></label>
          <p class="hint">块 = 一段完整对话；一次压几个块。默认 3，小步快跑更容易保持细节。</p>
          <label id="setting-compact-trigger" tabindex="-1" class="field"><span>压缩触发阈值（token）</span><VTextField density="compact" variant="outlined" hide-details v-model="chat.compact_trigger_tokens" type="number" min="1" placeholder="留空 = 输入预算的 80%" /></label>
          <p class="hint">填绝对值；留空按输入预算的 80% 计算。</p>
          </section>
          <div class="row action-row">
            <VBtn color="primary" variant="flat" size="small" type="button" :disabled="!chatReady || savingChat" @click="saveChat('context')">保存上下文设置</VBtn>
            <span class="status" role="status">{{ chatStatus.context }}</span>
          </div>
        </section>
        </VTabsWindowItem>
        <VTabsWindowItem value="generation">
        <section id="panel-generation" class="panel">
          <p class="settings-lead">调整自动起标题，以及模型能否收到历史消息的思考过程。保存后从下一轮生效。</p>

          <section class="settings-card">
          <h3>自动标题</h3>
          <label id="setting-title-chars" tabindex="-1" class="field"><span>自动标题最多字数</span><VTextField density="compact" variant="outlined" hide-details v-model="chat.title_chars" type="number" min="1" placeholder="留空 = 32" /></label>
          <p class="hint">标题只在会话标题为空时自动生成；用户手动改名后不再覆盖。</p>
          <label id="setting-title-model" tabindex="-1" class="field"><span>全局标题模型</span><VSelect v-model="chat.title_model" :items="titleSelectOptions" item-title="label" item-value="value" density="compact" variant="outlined" hide-details /></label>
          <p class="hint">默认「使用当前模型」；智能体里的起标题能力选「跟随全局设置」时读这里。</p>
          </section>
          <section class="settings-card">
          <h3>历史思考</h3>
          <label id="setting-replay-reasoning" tabindex="-1" class="field"><span>回传思考过程</span><VSwitch v-model="chat.replay_reasoning" color="primary" density="compact" hide-details /></label>
          <p class="hint">把思考过程随历史发回上游，多轮推理更连续；关闭可省上下文，思考仍会存档可看。</p>
          <div class="row action-row">
            <VBtn color="primary" variant="flat" size="small" type="button" :disabled="!chatReady || savingChat" @click="saveChat('generation')">保存生成设置</VBtn>
            <span class="status" role="status">{{ chatStatus.generation }}</span>
          </div>
          </section>
        </section>
        </VTabsWindowItem>
        <VTabsWindowItem value="markdown">
          <AppearancePane section="markdown" @prefs-saved="emit('prefs-saved')" />
        </VTabsWindowItem>
        <VTabsWindowItem value="provider">
        <section id="panel-provider" class="panel">
          <template v-if="!editProvider">
            <VTabs v-model="providerTab" class="agent-tabs" density="compact" align-tabs="start">
              <VTab value="list">渠道列表</VTab>
              <VTab value="global">全局模型规则</VTab>
            </VTabs>
            <VTabsWindow v-model="providerTab" class="agent-window">
            <VTabsWindowItem value="list">
            <div class="panel-head">
              <h3>渠道（{{ providers.length }}）</h3>
              <VBtn color="primary" variant="flat" size="small" type="button" @click="editProvider = 'new'">新建渠道</VBtn>
            </div>
            <ul v-if="providers.length" id="provider-list" class="settings-list" aria-label="渠道">
              <li v-for="p in providers" :key="p.id" class="settings-list-row">
                <VBtn type="button" variant="text" class="settings-list-entry manage-entry" @click="editProvider = p">
                  <span class="settings-list-copy">
                    <span class="manage-meta manage-name">
                      <strong>{{ providerDisplayName(p) }}</strong>
                      <code v-if="providerDisplayName(p) !== p.id">{{ p.id }}</code>
                    </span>
                    <span class="manage-meta">
                      <span>{{ p.vendor_name || p.vendor }}</span>
                      <span>{{ p.models?.length ?? 0 }} 个模型</span>
                      <span>{{ p.has_key ? "密钥已配置" : "未配置密钥" }}</span>
                      <span v-if="p.model_filter" :title="`只导入匹配：${p.model_filter}`">已筛选</span>
                    </span>
                  </span>
                  <span class="settings-list-chevron" aria-hidden="true" v-html="ICONS.chevronRight"></span>
                </VBtn>
                <div class="manage-actions">
                  <VBtn variant="text" size="small" type="button" class="manage-delete" @click="askDeleteProvider(p.id)" :aria-label="`删除 ${providerDisplayName(p)}`">删除</VBtn>
                </div>
              </li>
            </ul>
            <p v-else class="manage-empty">还没有渠道</p>
            <p v-if="routesStatus" class="status" role="status">{{ routesStatus }}</p>
            </VTabsWindowItem>
            <VTabsWindowItem value="global">
              <div id="setting-global-model-rules" tabindex="-1"><ModelRulesEditor @saved="emit('models-changed')" /></div>
            </VTabsWindowItem>
            </VTabsWindow>
          </template>
          <ProviderEditor
            v-else
            :provider="editProvider === 'new' ? null : editProvider"
            @close="editProvider = null"
            @saved="onProviderSaved"
            @model-saved="emit('models-changed')"
          />
        </section>
        </VTabsWindowItem>
        <VTabsWindowItem value="session">
        <section id="panel-session" class="panel">
          <p class="settings-lead">
            管理已有会话：点击一行回到对话，或导出、删除；也可以从对话记录文件新建会话。
          </p>
          <h3>已有会话（{{ sessions.length }}）</h3>
          <ul v-if="sessions.length" id="session-mgr-list" class="settings-list" aria-label="已有会话">
            <li v-for="s in sessions" :key="s.id" class="settings-list-row">
              <VBtn class="settings-list-entry manage-entry" variant="text" type="button" @click="gotoSession(s.id)">
                <span class="settings-list-copy">
                  <strong>{{ sessionTitle(s.title) }}</strong>
                  <span class="manage-meta">
                    <span>{{ s.messages }} 条消息</span>
                    <span v-if="s.model" :title="`${s.provider}/${s.model}`">{{ s.model }}</span>
                  </span>
                </span>
                <span class="settings-list-chevron" aria-hidden="true" v-html="ICONS.chevronRight"></span>
              </VBtn>
              <div class="manage-actions">
                <VBtn variant="text" size="small" type="button" @click="exportSessionRow(s)" :aria-label="`导出 ${sessionTitle(s.title)}`">导出</VBtn>
                <VBtn variant="text" size="small" type="button" class="manage-delete" @click="askDropSession(s.id, s.title)" :aria-label="`删除 ${sessionTitle(s.title)}`">删除</VBtn>
              </div>
            </li>
          </ul>
          <p v-else class="manage-empty">还没有会话</p>
          <div class="row" v-if="sessionMgrStatus"><span class="status">{{ sessionMgrStatus }}</span></div>
          <h3 id="setting-import-json" tabindex="-1">导入 MicroChat 对话</h3>
          <div class="io-cards">
            <div class="io-card">
              <div class="io-head"><b>MicroChat 对话记录</b><i class="io-tag">.json</i></div>
              <p class="hint">本应用导出的对话记录：标题、每条消息的说话人和正文（消息里的状态块会保留，导回去后状态照常重算），以及这条会话当时使用的渠道、模型和智能体（本机没有那个智能体会提示，人设回落到内置默认）。</p>
              <div class="row">
                <VBtn color="primary" variant="flat" size="small" type="button" @click="jsonInput?.click()">选择 .json 文件…</VBtn>
                <input ref="jsonInput" type="file" accept=".json" hidden @change="importJSONFile" />
                <span class="status">{{ jsonStatus }}</span>
              </div>
            </div>
          </div>
          <p class="hint">导入只会<b>新建一条会话</b>，不会改动现有会话。</p>
        </section>
        </VTabsWindowItem>
        <VTabsWindowItem value="agent">
        <section id="panel-agent" class="panel">
          <template v-if="!editAgent">
            <div class="panel-head">
              <h3>智能体</h3>
              <span class="head-btns">
                <VBtn variant="text" size="small" type="button" @click="cartridgeInput?.click()" title="导入智能体文件（JSON；提示词片段会一起带进来）">导入智能体…</VBtn>
                <input ref="cartridgeInput" type="file" accept=".json" hidden @change="importCartridge" />
                <VMenu location="bottom end" origin="top end">
                  <template #activator="{ props: activatorProps }">
                    <VBtn v-bind="activatorProps" color="primary" variant="flat" size="small" type="button">新建</VBtn>
                  </template>
                  <VList density="compact">
                    <VListItem title="传统智能体" subtitle="人设 + 提示词片段 + 能力" @click="startNewAgent('prompt')" />
                    <VListItem title="FlowAgent" subtitle="用节点图编排多个智能体" @click="startNewAgent('flow')" />
                  </VList>
                </VMenu>
                <VSelect v-model="presetPick" :items="presetOptions" item-title="label" item-value="value" title="从模板创建" density="compact" variant="outlined" hide-details @update:model-value="presetNew" />
              </span>
            </div>
            <p class="hint">每个智能体 = 一份人设 + 一组能力开关；会话在顶栏切换使用哪一个。</p>
            <p class="action-note">「从模板创建」只写入初始内容；导入智能体文件会把它引用的提示词片段一起带进来。</p>
            <p v-if="cartridgeStatus" class="status" role="status">{{ cartridgeStatus }}</p>
            <ul v-if="agents.length" id="agent-list" class="settings-list" aria-label="智能体">
              <li v-for="a in agents" :key="a.id" class="settings-list-row">
                <VBtn type="button" variant="text" class="settings-list-entry manage-entry" @click="editExistingAgent(a)">
                  <span class="settings-list-copy">
                    <span class="manage-meta manage-name">
                      <strong>{{ a.name || a.id }}</strong>
                      <span v-if="a.id === curDefault" class="manage-default">默认</span>
                    </span>
                    <span class="manage-meta">
                      <code>{{ a.id }}</code>
                      <span :title="agentTypeTitle(a)">{{ agentType(a) }}</span>
                    </span>
                    <span v-if="a.description" class="manage-description" :title="a.description">{{ a.description }}</span>
                  </span>
                  <span class="settings-list-chevron" aria-hidden="true" v-html="ICONS.chevronRight"></span>
                </VBtn>
              </li>
            </ul>
            <p v-else class="manage-empty">没有可显示的智能体</p>
          </template>
          <AgentEditor
            v-if="editAgent"
            :agent="editAgent === 'new' ? null : editAgent"
            :template="agentTemplate"
            :kind="editAgent === 'new' ? newAgentKind : (editAgent?.kind === 'flow' ? 'flow' : 'prompt')"
            :segments="segmentsLib"
            :presets="tavernPresets"
            :is-default="editAgent !== 'new' && editAgent?.id === curDefault"
            :ask="ask"
            @close="closeAgentEditor"
            @saved="loadAgents"
          />

        </section>
        </VTabsWindowItem>
        <VTabsWindowItem value="prompt">
        <section id="panel-prompt" class="panel">
          <template v-if="!editSeg">
            <div class="panel-head">
              <h3>提示词片段</h3>
              <span class="head-btns">
                <span class="action-note">新建后还要在智能体里引用</span>
                <VBtn color="primary" variant="flat" size="small" type="button" @click="editSeg = 'new'">新建</VBtn>
              </span>
            </div>
            <p class="hint">可复用的文本库；片段本身不会生效，要在智能体或能力的「提示词片段」里按顺序引用。</p>
            <div id="prompt-list" v-if="segmentsLib.length">
              <VTable class="plist">
                <thead><tr><th>名称</th><th>内容预览</th><th>谁在用</th></tr></thead>
                <tbody><tr v-for="s in segmentsLib" :key="s.id" class="clickable" @click="editSeg = s">
                  <td data-label="名称">{{ s.id }}<i v-if="s.builtin" class="builtin-tag">内置</i><i v-if="s.imported" class="builtin-tag imported" :title="segmentSource(s)">导入</i></td>
                  <td data-label="内容预览">{{ s.text.slice(0, 40) }}</td>
                  <td data-label="谁在用">{{ segRefs(s.id).join("、") || "—" }}</td>
                </tr></tbody>
              </VTable>
            </div>
            <div v-else class="status">（还没有片段 —— 点「新建」创建一个，例如教模型写状态块、数学公式或联网规则）</div>
          </template>
          <SegmentEditor
            v-else
            :segment="editSeg === 'new' ? null : editSeg"
            :agents="agents"
            :ask="ask"
            @close="editSeg = null"
            @saved="loadAgents"
          />
        </section>
        </VTabsWindowItem>
        <VTabsWindowItem value="tavern">
        <section id="panel-tavern" class="panel">
          <h3>酒馆</h3>
          <p class="hint">专门处理 SillyTavern 的预设、角色卡与聊天记录：预设可以解析并导入提示词片段，聊天记录可以导入成会话；角色卡转换还没接。</p>
          <section class="settings-card">
            <h3 id="setting-import-preset" tabindex="-1">原始 JSON 存档</h3>
            <p class="hint">只保存文件的原始字节，不解析、不改写；导出时逐字节原样返回。解析与导入片段走每行的「解析与导入」。</p>
            <div class="row">
              <VBtn color="primary" variant="flat" size="small" type="button" @click="tavernInput?.click()">导入原始 JSON…</VBtn>
              <span class="action-note">只保存原始字节、不改写文件；解析和导入片段走「解析与导入」。</span>
              <input ref="tavernInput" type="file" accept=".json,application/json" hidden @change="importTavernPresetFile" />
              <span class="status">{{ tavernStatus }}</span>
            </div>
            <div v-if="tavernPresets.length" id="tavern-file-list">
              <VTable class="plist">
                <thead><tr><th>文件</th><th>大小</th><th>导入时间</th><th></th></tr></thead>
                <tbody><tr v-for="p in tavernPresets" :key="p.id">
                  <td data-label="文件" :title="p.filename">{{ p.filename }}</td>
                  <td data-label="大小">{{ sizeText(p.size) }}</td>
                  <td data-label="导入时间">{{ timeText(p.created_at) }}</td>
                  <td data-label="操作" class="ops">
                    <VBtn variant="text" size="small" type="button" title="解析预览 / 有效内容与导入 / 原始 JSON" @click="inspectTavernPreset(p)">解析与导入</VBtn>
                    <VBtn variant="text" size="small" type="button" @click="downloadTavernPreset(p)">导出</VBtn>
                    <VBtn color="error" variant="text" size="small" type="button" @click="removeTavernPreset(p)">删除</VBtn>
                  </td>
                </tr></tbody>
              </VTable>
            </div>
            <p v-else class="hint">还没有导入原始文件。</p>
          </section>
          <h3 id="setting-import-st" tabindex="-1">导入 SillyTavern 聊天记录</h3>
            <div class="io-card">
              <div class="io-head"><b>SillyTavern 聊天记录</b><i class="io-tag">.jsonl</i></div>
              <p class="hint">SillyTavern 导出的聊天记录（一行一条）。只读取说话人、正文、思考过程和时间，其余内容会丢掉。</p>
              <div class="row">
                <VBtn color="primary" variant="flat" size="small" type="button" @click="stInput?.click()">选择 .jsonl 文件…</VBtn>
                <input ref="stInput" type="file" accept=".jsonl,.json,.txt" hidden @change="importSTFile" />
                <span class="status">{{ importStatus }}</span>
              </div>
              <label class="field"><span>标题</span><VTextField density="compact" variant="outlined" hide-details v-model="importTitle" placeholder="留空 = 用第一句话自动起名" /></label>
            </div>
        </section>
        </VTabsWindowItem>
        <VTabsWindowItem value="update">
          <UpdatePane :ask="ask" />
        </VTabsWindowItem>
        </VTabsWindow>
      </div>
      </div>
    </div>
    <TavernInspector
      v-if="inspectingPreset"
      :preset="inspectingPreset"
      :segments="segmentsLib"
      @close="inspectingPreset = null"
      @imported="onPresetSegmentImported"
    />
  </section>
</template>

<style scoped>
.manage-meta { display: flex; align-items: baseline; flex-wrap: wrap; min-width: 0; gap: 0.2em 0.75em; color: var(--md-on-surface-variant); font-size: var(--md-type-label-medium); }
.manage-name { font-size: inherit; }
.manage-name code { font-size: var(--md-type-label-medium); }
.manage-description { color: var(--md-on-surface-variant); font-size: var(--md-type-body-medium); display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; overflow: hidden; }
.manage-default { color: var(--md-primary); font-size: var(--md-type-label-medium); }
.manage-actions { display: flex; flex: none; gap: calc(var(--chat-space) / 4); padding-inline-end: calc(var(--chat-space) / 2); }
#app .manage-actions .v-btn { min-width: 0; min-height: var(--chat-control); padding-inline: calc(0.75 * var(--chat-space)); color: var(--md-on-surface-variant); font-size: var(--md-type-label-large); letter-spacing: normal; }
#app .manage-actions .manage-delete:is(:hover, :focus-visible) { color: var(--md-error); }
.manage-empty { color: var(--md-on-surface-variant); font-size: var(--md-type-body-medium); }
</style>
