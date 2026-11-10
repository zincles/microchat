<script setup lang="ts">
// 设置**视图**（2026-10-10 起与对话同级，不再是弹窗 —— OpenWebUI 那种）：占满左栏右边的整块区域，
// 自带小头（[◧] 左栏开关 · 设置 · [×] 返回对话）。子编辑器（Agent/Provider）改为对应 tab 内的二级页（列表 ⇄ 详情就地切换）。
// 「外观」那族的个人偏好（主题/缩放/背景图/发送键/预演）住在**本视图的「外观」区**
//（2026-11-01 从删掉的用户视图整块搬进来，由 `AppearancePane.vue` 渲染）。
// 关走 close（Esc/×）—— 视图不卸载就丢状态的口径照旧：v-if 挂载，进视图拉一次。
import { ref, computed, watch, onMounted, onUnmounted } from "vue";
import { VTab, VTabs, VTabsWindow, VTabsWindowItem, VSwitch, VBtn, VTextField, VSelect, VTable } from "vuetify/components";
import AgentEditor from "./AgentEditor.vue";
import AppearancePane from "./AppearancePane.vue";
import SegmentEditor from "./SegmentEditor.vue";
import assistantPreset from "../../presets/assistant.json";
import rpPreset from "../../presets/rp-agent.json";
import ProviderEditor from "./ProviderEditor.vue";
import ModelRulesEditor from "./ModelRulesEditor.vue";
import UpdatePane from "./UpdatePane.vue";
import TavernJsonViewer from "./TavernJsonViewer.vue";
import TavernPreview from "./TavernPreview.vue";
import { ICONS } from "./icons.ts";
import { sharedApi } from "../utils/prefs.ts";
import { SETTINGS_TABS, groupModelsByProvider, modelOption, providerChoices, modelChoicesFor, modelKey, parseModelKey, providerDisplayName } from "../utils/format.ts";
import type { ModelGroup } from "../utils/format.ts";
import { downloadJSON, downloadBlob } from "../utils/download.ts";
import { buildCartridge, cartridgeAgent } from "../utils/cartridge.ts";
import type { Agent, ChatConfig, DefaultsConfig, ProviderView, Segment, SessionView, TavernPreset } from "../api/types.ts";

// models-changed：渠道/模型那摊变了（删除渠道会连模型一起没）⇒ 让 App 重拉顶栏的模型下拉。
// 与会话左栏那个 sessions-changed 是**两件事**，别合并（各自的读口不同）。
const emit = defineEmits<{
  close: [];
  "toggle-left": [];
  "sessions-changed": [];
  "models-changed": [];
  "goto-session": [id: string];
  "prefs-saved": [];
  "chat-saved": [];
}>();
// ask：全局确认框（App 的 Overlays.confirmAsk）—— 设置里的删除（会话/渠道/Agent）也必须先摊开再点头。
// escBlocked：外面临时弹窗（会话设置）开着时，Esc 归它 —— 一次按键不许关两层。
const props = withDefaults(defineProps<{
  ask?: ((text: string) => Promise<boolean>) | null;
  escBlocked?: boolean;
  // only：单独入口（"session" / "provider"）—— 只显示那一页、不渲染设置页签；
  // 空串 = 完整设置。两者共用同一份数据加载与同一套 panel。
  only?: string;
}>(), {
  ask: null,
  escBlocked: false,
  only: "",
});

const api = sharedApi();
const errMsg = (e: unknown): string => (e instanceof Error ? e.message : String(e));
const leftSvg = ICONS.panelLeft;

// 四个设置区 + 两个独立管理页（会话 / 渠道）。
// （「连接」那一区 2026-10-28 删了 —— 理由写在 format.js 的 SETTINGS_TABS 上。）
const tab = ref(props.only || "chat");
// 单独入口的页头文字；空串 = 完整设置（显示 VTabs）。
const onlyTitle = computed(() => (props.only === "session" ? "会话管理"
  : props.only === "provider" ? "渠道管理"
  : props.only === "update" ? "更新"
  : ""));
watch(() => props.only, (v) => { tab.value = v || "chat"; });
const chat = ref<Record<string, any>>({ title_chars: "", title_model: "", model_context_tokens: "", history_window: "", compact_blocks: "", compact_trigger_tokens: "", replay_reasoning: false, auto_compact: true });
const chatStatus = ref("");
const defaults = ref<Record<string, any>>({ provider: "", model: "", agent: "" });
const defStatus = ref("");
const agents = ref<Agent[]>([]);
const segmentsLib = ref<Segment[]>([]); // 提示词段库（随 GET /agents 一起给）
const curDefault = ref("");
const editAgent = ref<any>(null); // null | 'new' | agent（二级编辑器）
const editSeg = ref<any>(null); // null | 'new' | segment（「提示词」tab 内的二级页）
// 谁引用了这个段（「提示词」tab 列表与编辑器共用）。
const segRefs = (id: string): string[] => agents.value.filter((a) => (a.segments ?? []).includes(id)).map((a) => a.name || a.id);
const providers = ref<ProviderView[]>([]);
// 顶栏/会话设置用的是同一份 `GET /models`（跨渠道拍平那一条）：默认值那两格的下拉也从它取。
const modelGroups = ref<ModelGroup[]>([]);
// 全局标题模型的下拉：跨渠道拍平（显示名带渠道前缀，两个渠道有同名模型时才分得清）。
const titleModelOptions = computed<Array<{ value: string; label: string }>>(() => modelGroups.value.flatMap((g) =>
  (g.items ?? []).map((item) => ({ value: item.value, label: `${g.provider} / ${item.label}` }))));
const editProvider = ref<any>(null); // null | 'new' | provider（二级编辑器）
// 渠道页本身也分两页：列表 / 全局模型规则（后者与列表同级，不塞进单个渠道的详情）。
const providerTab = ref<"list" | "global">("list");
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
  } catch {}
}
async function saveChat(): Promise<void> {
  const num = (v: unknown): number | undefined => (String(v ?? "").trim() === "" ? undefined : Number(v));
  const body: Partial<ChatConfig> = {};
  const t = num(chat.value.title_chars);
  const m = num(chat.value.model_context_tokens);
  const b = num(chat.value.compact_blocks);
  const hw = num(chat.value.history_window);
  if (t !== undefined) body.title_chars = t;
  if (m !== undefined) body.model_context_tokens = m;
  if (b !== undefined) body.compact_blocks = b;
  if (hw !== undefined) body.history_window = hw;
  const titleModel = parseModelKey(chat.value.title_model ?? "");
  const cur = await api.getChat().catch(() => null);
  if (!cur) { chatStatus.value = "保存失败：拿不到当前配置"; return; }
  const fullBody: ChatConfig = {
    ...cur, ...body,
    title_provider: titleModel?.provider ?? "",
    title_model: titleModel?.model ?? "",
    replay_reasoning: !!chat.value.replay_reasoning,
    auto_compact: !!chat.value.auto_compact,
  };
  if (String(chat.value.compact_trigger_tokens ?? "").trim() === "") delete fullBody.compact_trigger_tokens;
  else fullBody.compact_trigger_tokens = Number(chat.value.compact_trigger_tokens);
  try {
    await api.putChat(fullBody);
    chatStatus.value = "已保存";
    emit("chat-saved");
  } catch (e) { chatStatus.value = `保存失败：${errMsg(e)}`; }
}

async function loadDefaults(): Promise<void> {
  try {
    const d = await api.getDefaults();
    defaults.value = { provider: d.provider ?? "", model: d.model ?? "", agent: d.agent ?? "" };
  } catch {}
}
async function saveDefaults(): Promise<void> {
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
// 查看只把元数据交给阅读器；原始字节由阅读器按需下载，列表不背 1.5MB 的正文。
const viewingPreset = ref<TavernPreset | null>(null);
const previewingPreset = ref<TavernPreset | null>(null);
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
function viewTavernPreset(p: TavernPreset): void {
  viewingPreset.value = p;
}
function previewTavernPreset(p: TavernPreset): void {
  previewingPreset.value = p;
}
async function downloadTavernPreset(p: TavernPreset): Promise<void> {
  try { downloadBlob(await api.downloadTavernPreset(p.id), p.filename); }
  catch (e) { tavernStatus.value = `导出失败：${errMsg(e)}`; }
}
async function removeTavernPreset(p: TavernPreset): Promise<void> {
  if (props.ask && !(await props.ask(`删除原始预设「${p.filename}」？不可逆。`))) return;
  try {
    await api.deleteTavernPreset(p.id);
    tavernStatus.value = `已删除：${p.filename}`;
    await loadTavernPresets();
  } catch (e) { tavernStatus.value = `删除失败：${errMsg(e)}`; }
}

// 会话 tab：列表 + 新建/进入/删除（编辑走左栏 ☰ 二级）。
const sessions = ref<SessionView[]>([]);
const sessionMgrStatus = ref("");

async function loadAgents(): Promise<void> {
  try {
    const data = await api.agents();
    curDefault.value = data.default_agent ?? "";
    agents.value = data.agents ?? [];
    segmentsLib.value = data.segments ?? [];
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
  editAgent.value = { template: { ...src, name: "" } };
}

// 默认值那两格的下拉选项（纯函数在 format.js，测试直引）。
// 模型跟着渠道走：换了渠道却留着上一家的模型 id，等于存了一对配不上的值。
const providerOpts = computed(() => providerChoices(providers.value, defaults.value.provider));
const modelOpts = computed(() => modelChoicesFor(modelGroups.value, defaults.value.provider, defaults.value.model));
const titleSelectOptions = computed(() => [{ label: "使用当前模型", value: "" }, ...titleModelOptions.value]);
const providerSelectOptions = computed(() => [{ label: "内置默认（本地示例）", value: "" }, ...providerOpts.value]);
const modelSelectOptions = computed(() => [{ label: "内置默认（本地示例）", value: "" }, ...modelOpts.value]);
const agentSelectOptions = computed(() => [{ label: "内置默认", value: "" }, ...agents.value.map((a) => ({ label: a.name || a.id, value: a.id }))]);
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
  if (props.ask && !(await props.ask(`删除会话「${title || "新会话"}」？消息与摘要一并删，不可逆。`))) return;
  try {
    await api.deleteSession(id);
    await loadSessions();
    emit("sessions-changed");
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
  if (e.key !== "Escape") return;
  // 二级页（Agent/渠道详情）或外面临时弹窗开着 ⇒ Esc 归它们；一次按键只关一层（踩过：双关）。
  if (editAgent.value || editSeg.value || editProvider.value || props.escBlocked) return;
  emit("close");
}
</script>

<template>
  <section id="settings-view">
    <div class="settings-bar">
      <VBtn variant="text" size="small" type="button"
        title="显示/隐藏会话栏"
        aria-label="显示/隐藏会话栏"
        @click="$emit('toggle-left')"
        v-html="leftSvg"></VBtn>
      <VTabs
        v-if="!only"
        v-model="tab"
        class="settings-tabs"
        density="compact"
        align-tabs="start"
      >
        <VTab v-for="t in SETTINGS_TABS" :key="t.id" :value="t.id" @focus="tab = t.id">{{ t.label }}</VTab>
      </VTabs>
      <h2 v-else class="settings-title">{{ onlyTitle }}</h2>
      <VBtn icon variant="text" size="small" id="settings-close" type="button" title="退出设置（Esc）" aria-label="退出设置" @click="$emit('close')">
        ×
      </VBtn>
    </div>
    <div class="settings-body">
      <div class="settings-box">
        <div class="settings-cols">
        <!-- 会话/渠道是**页面切换**，不是设置页签：key 随 only 变 ⇒ 重挂载、不跑 tab 滑入。
             否则「渠道 → 设置」会从最后一个 window item 跳回 chat，方向反着走
             （2026-11-06 用户报「有 Tab 效果，而且顺序是错的」）。 -->
        <VTabsWindow v-model="tab" :key="props.only || 'settings'" class="settings-window">
        <VTabsWindowItem value="chat">
        <section id="panel-chat" class="panel">
          <section class="settings-card">
          <!-- 文案（2026-10-28 用户提）：「缺省」是技术圈黑话，普通用户读不出"默认值"。
               一律说「默认」；「会话」留着 —— 它是 DEFINE.md 的词（左栏、导出、删除确认都这么说），
               一块面板里混用「对话/会话」只会更乱。 -->
          <h3>新会话默认设置</h3>
          <!-- 三格都是**下拉**（2026-10-28）：从前渠道/模型是手打 id 的文本框，说明文自己都写着
               「填错不会报错，只会在发送时回落成『未配置模型』」—— 那是把校验推给用户。
               现在能选出来的都真实存在；当前值即使不在列表里（渠道被删、手改过配置文件、
               或 dummy 这种永远发现不到模型的渠道）也会补一条并标明，**不许**静默丢掉。 -->
          <label class="field"><span>渠道</span><VSelect :model-value="defaults.provider" :items="providerSelectOptions" item-title="label" item-value="value" density="compact" variant="outlined" hide-details @update:model-value="onDefaultProviderChange" /></label>
          <label class="field"><span>模型</span><VSelect v-model="defaults.model" :items="modelSelectOptions" item-title="label" item-value="value" density="compact" variant="outlined" hide-details /></label>
          <label class="field"><span>智能体</span><VSelect v-model="defaults.agent" :items="agentSelectOptions" item-title="label" item-value="value" density="compact" variant="outlined" hide-details /></label>
          <div class="row"><VBtn color="primary" variant="flat" size="small" type="button" @click="saveDefaults">保存默认值</VBtn><span class="status">{{ defStatus }}</span></div>
          <p class="hint" v-if="!modelOpts.length">这个渠道还没有拉到任何模型 —— 可以先留空用内置默认，或去「渠道」里点「保存并刷新」。</p>
          <p class="hint">新建会话时使用这三项；留空 = 内置默认（本地示例模型，不联网，开箱就能回话）。</p>
          </section>
          <section class="settings-card">
            <h3>回复与自动压缩</h3>
            <label class="field"><span>自动标题最多字数</span><VTextField density="compact" variant="outlined" hide-details v-model="chat.title_chars" type="number" min="1" /></label>
            <label class="field"><span>标题模型</span><VSelect v-model="chat.title_model" :items="titleSelectOptions" item-title="label" item-value="value" density="compact" variant="outlined" hide-details /></label>
            <p class="hint">全局的起标题模型。默认「使用当前模型」；Agent 里选「跟随全局设置」时读这里。</p>
            <label class="field"><span>上下文长度（token）</span><VTextField density="compact" variant="outlined" hide-details v-model="chat.model_context_tokens" type="number" min="1" /></label>
            <!-- 为什么右上角那枚环的分母常常比这里小：它是**输入预算** = 上下文 − 给回复留的输出额度。
                 移动端没有 hover，所以这句话必须写在这儿（不能只藏在环的 title 里）。 -->
            <p class="hint">右上角那枚环显示的是<b>还能输入多少</b>：这里填的上下文长度减去给回复预留的空间。填 1000000 时通常显示 995904。</p>
            <label class="field"><span>首屏加载多少条消息</span><VTextField density="compact" variant="outlined" hide-details v-model="chat.history_window" type="number" min="1" placeholder="留空 = 5" /></label>
            <p class="hint">包含用户与 AI 消息；「加载更多」每页也按这个数量。默认 5，远程部署时首屏更快。</p>
            <label class="field"><span>自动压缩块数</span><VTextField density="compact" variant="outlined" hide-details v-model="chat.compact_blocks" type="number" min="1" placeholder="留空 = 3" /></label>
            <label class="field"><span>自动压缩</span><VSwitch v-model="chat.auto_compact" color="primary" density="compact" hide-details /></label>
            <p class="hint">占用到 80% 开始压缩，压到 50% 以下停；关着就只手动压。</p>
            <label class="field"><span>压缩触发点</span><VTextField density="compact" variant="outlined" hide-details v-model="chat.compact_trigger_tokens" type="number" min="1" placeholder="留空 = 80%" /></label>
            <label class="field"><span>回传思考过程</span><VSwitch v-model="chat.replay_reasoning" color="primary" density="compact" hide-details /></label>
            <div class="row"><VBtn color="primary" variant="flat" size="small" type="button" @click="saveChat">保存</VBtn><span class="status">{{ chatStatus }}</span></div>
          </section>
        </section>
        </VTabsWindowItem>
        <VTabsWindowItem value="session">
        <section id="panel-session" class="panel">
          <h3>已有会话（{{ sessions.length }} 条，点击打开）</h3>
          <div id="session-mgr-list">
            <VTable class="plist">
              <thead><tr><th>标题</th><th>消息数</th><th>模型</th><th></th></tr></thead>
              <tbody><tr v-for="s in sessions" :key="s.id" class="clickable" @click="gotoSession(s.id)">
                <td>{{ s.title || "新会话" }}</td>
                <td>{{ s.messages }}</td>
                <td :title="`${s.provider}/${s.model}`">{{ s.model || "—" }}</td>
                <td class="ops">
                  <VBtn variant="text" size="small" type="button" @click.stop="exportSessionRow(s)" title="导出这条对话（含全部消息、摘要，以及使用的渠道、模型和智能体）">导出</VBtn>
                  <VBtn color="error" variant="text" size="small" type="button" @click.stop="askDropSession(s.id, s.title)">删除</VBtn>
                </td>
              </tr></tbody>
            </VTable>
          </div>
          <div class="row" v-if="sessionMgrStatus"><span class="status">{{ sessionMgrStatus }}</span></div>
          <!-- 导入区（2026-10-17 用户点名「混在一起、都乱了」）：两种格式各一张卡 ——
               各有一句「这是哪种文件、导进去会怎样」，动作都是按钮 + 隐藏文件框（原生 file 框太丑，
               而且两张卡长一样 ⇒ 分不清谁是谁）。 -->
          <h3>导入 MicroChat 对话</h3>
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
          <p class="hint">两种导入都会<b>新建一条会话</b>，不会改动现有会话；导入完成后左栏马上能看到。</p>
        </section>
        </VTabsWindowItem>
        <VTabsWindowItem value="agent">
        <section id="panel-agent" class="panel">
          <template v-if="!editAgent">
            <div class="panel-head">
              <h3>已有智能体（点击编辑）</h3>
              <span class="head-btns">
                <VBtn variant="text" size="small" type="button" @click="cartridgeInput?.click()" title="导入智能体文件（JSON；提示词片段会一起带进来）">导入智能体…</VBtn>
                <input ref="cartridgeInput" type="file" accept=".json" hidden @change="importCartridge" />
                <VBtn color="primary" variant="flat" size="small" type="button" @click="editAgent = 'new'">新建</VBtn>
                <VSelect v-model="presetPick" :items="presetOptions" item-title="label" item-value="value" title="从模板创建" density="compact" variant="outlined" hide-details @update:model-value="presetNew" />
              </span>
            </div>
            <div class="row"><span class="status">{{ cartridgeStatus }}</span></div>
            <div id="agent-list">
              <VTable class="plist">
                <thead><tr><th>名称</th><th>标识</th><th>默认使用</th></tr></thead>
                <tbody><tr v-for="a in agents" :key="a.id" class="clickable" @click="editAgent = a">
                  <td>{{ a.name }}</td>
                  <td>{{ a.id }}</td>
                  <td>{{ a.id === curDefault ? "✓" : "" }}</td>
                </tr></tbody>
              </VTable>
            </div>
          </template>
          <AgentEditor
            v-if="editAgent"
            :agent="editAgent === 'new' || editAgent?.template ? null : editAgent"
            :template="editAgent?.template ?? null"
            :segments="segmentsLib"
            :presets="tavernPresets"
            :is-default="editAgent !== 'new' && editAgent?.id === curDefault"
            :ask="ask"
            @close="editAgent = null"
            @saved="loadAgents"
          />

        </section>
        </VTabsWindowItem>
        <VTabsWindowItem value="prompt">
        <section id="panel-prompt" class="panel">
          <template v-if="!editSeg">
            <div class="panel-head">
              <h3>提示词片段（可复用的文本，在智能体里按顺序引用）</h3>
              <span class="head-btns"><VBtn color="primary" variant="flat" size="small" type="button" @click="editSeg = 'new'">新建</VBtn></span>
            </div>
            <div id="prompt-list" v-if="segmentsLib.length">
              <VTable class="plist">
                <thead><tr><th>名称</th><th>内容预览</th><th>谁在用</th></tr></thead>
                <tbody><tr v-for="s in segmentsLib" :key="s.id" class="clickable" @click="editSeg = s">
                  <td>{{ s.id }}<i v-if="s.builtin" class="builtin-tag">内置</i><i v-if="s.imported" class="builtin-tag imported">导入</i></td>
                  <td>{{ s.text.slice(0, 40) }}</td>
                  <td>{{ segRefs(s.id).join("、") || "—" }}</td>
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
        <!-- 外观（2026-11-01 从删掉的用户视图搬来）：主题 / 缩放 / 背景图 / 显示模式 /
             滚动收起 / 发送键 / 预演 / 命令菜单。全部即时生效，改了就存。 -->
        <VTabsWindowItem value="appearance">
          <AppearancePane @prefs-saved="emit('prefs-saved')" />
        </VTabsWindowItem>
        <VTabsWindowItem value="tavern">
        <section id="panel-tavern" class="panel">
          <h3>酒馆</h3>
          <p class="hint">专门处理 SillyTavern 的预设、角色卡与聊天记录：这里负责把它们转换成 MicroChat 的智能体 / 提示词片段 / 会话。预设与角色卡转换还没接；聊天记录导入已经可用。</p>
          <section class="settings-card">
            <h3>原始 JSON 存档</h3>
            <p class="hint">只保存文件的原始字节，不解析、不改写；导出时逐字节原样返回。预设 / 角色卡的解析与转换以后再做。</p>
            <div class="row">
              <VBtn color="primary" variant="flat" size="small" type="button" @click="tavernInput?.click()">导入原始 JSON…</VBtn>
              <input ref="tavernInput" type="file" accept=".json,application/json" hidden @change="importTavernPresetFile" />
              <span class="status">{{ tavernStatus }}</span>
            </div>
            <div v-if="tavernPresets.length" id="tavern-file-list">
              <VTable class="plist">
                <thead><tr><th>文件</th><th>大小</th><th>导入时间</th><th></th></tr></thead>
                <tbody><tr v-for="p in tavernPresets" :key="p.id">
                  <td :title="p.filename">{{ p.filename }}</td>
                  <td>{{ sizeText(p.size) }}</td>
                  <td>{{ timeText(p.created_at) }}</td>
                  <td class="ops">
                    <VBtn variant="text" size="small" type="button" @click="previewTavernPreset(p)">解析预览</VBtn>
                    <VBtn variant="text" size="small" type="button" @click="viewTavernPreset(p)">查看</VBtn>
                    <VBtn variant="text" size="small" type="button" @click="downloadTavernPreset(p)">导出</VBtn>
                    <VBtn color="error" variant="text" size="small" type="button" @click="removeTavernPreset(p)">删除</VBtn>
                  </td>
                </tr></tbody>
              </VTable>
            </div>
            <p v-else class="hint">还没有导入原始文件。</p>
          </section>
          <h3>导入 SillyTavern 聊天记录</h3>
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
        <VTabsWindowItem value="provider">
        <section id="panel-provider" class="panel">
          <template v-if="!editProvider">
            <VTabs v-model="providerTab" class="agent-tabs" density="compact" align-tabs="start">
              <VTab value="list">渠道列表</VTab>
              <VTab value="global">全局模型规则</VTab>
            </VTabs>
            <VTabsWindow v-model="providerTab" class="agent-window">
            <VTabsWindowItem value="list">
            <h3>渠道</h3>
            <p class="hint">一条渠道 = 用哪个服务商、哪个密钥、哪些模型。点一行编辑；模型数是上次刷新到的数量。</p>
            <div id="provider-list">
              <VTable class="plist">
                <thead><tr><th>渠道</th><th>服务商</th><th>模型</th><th>密钥</th><th></th></tr></thead>
                <tbody><tr v-for="p in providers" :key="p.id" class="clickable" @click="editProvider = p">
                  <td :title="p.name ? `${p.name}（${p.id}）` : p.id">
                    <span class="provider-alias">{{ providerDisplayName(p) }}</span>
                    <!-- 别名之下保留真实 id：列表显示的是人话，但改配置 / 对日志时得能一眼对回内部标识。 -->
                    <code v-if="providerDisplayName(p) !== p.id" class="provider-id">{{ p.id }}</code>
                  </td>
                  <td :title="p.vendor">{{ p.vendor_name || p.vendor || "?" }}</td>
                  <td>{{ p.models?.length ?? 0 }} 个<i v-if="p.model_filter" class="io-tag" :title="`只导入匹配：${p.model_filter}`">筛选</i></td>
                  <td>{{ p.has_key ? "已配置" : "未配置" }}</td>
                  <td class="ops"><VBtn color="error" variant="text" size="small" type="button" @click.stop="askDeleteProvider(p.id)">删除</VBtn></td>
                </tr></tbody>
              </VTable>
            </div>
            <div class="row">
              <VBtn color="primary" variant="flat" size="small" type="button" @click="editProvider = 'new'">新建</VBtn>
              <span class="status">{{ routesStatus }}</span>
            </div>
            </VTabsWindowItem>
            <VTabsWindowItem value="global">
              <ModelRulesEditor @saved="emit('models-changed')" />
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
        <VTabsWindowItem value="update">
          <UpdatePane :ask="ask" />
        </VTabsWindowItem>
        </VTabsWindow>
      </div>
      </div>
    </div>
    <TavernJsonViewer v-if="viewingPreset" :preset="viewingPreset" @close="viewingPreset = null" />
    <TavernPreview v-if="previewingPreset" :preset="previewingPreset" @close="previewingPreset = null" />
  </section>
</template>
