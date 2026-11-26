<script setup lang="ts">
// Agent 编辑器（设置视图 Agent tab 内的二级页，列表 ⇄ 详情就地切换）：新建 / 改 id·名 / 提示词 / 能力开关 / 设默认 / 删除。
// isNew（boolean）：无 agent 快照=新建（空表单）；有=编辑（带入快照，保存后父重拉，id 变了也对得上）。关即销毁。
//
// 2026-10-28 用户：「在具体某个 Agent 的设置界面里，我们最好也分 TAB，不然的话，全挤在一起很丑。」
// ⇒ 一条长滚动拆成页签：基本 / 系统提示词 / 每个能力一页（页签条用 Vuetify `VTabs`，与设置主栏同一套）。
// 保存一行留在页签**外面**（哪一页都能保存；翻页不丢已填的东西，状态都在 ref 里）。
import { ref, shallowRef, computed, watch, onMounted, onUnmounted } from "vue";
import { sharedApi } from "../utils/prefs.ts";
import { ABILITY_IDS, ABILITY_LABELS, ABILITY_TEMPLATE_SEGMENTS, buildAbilitiesPatch, modelKey, parseModelKey, modelLabel } from "../utils/format.ts";
import type { AbilityRow } from "../utils/format.ts";
import { assemblePrompt, abilityPrompt } from "../utils/prompt.ts";
import { buildCartridge } from "../utils/cartridge.ts";
import { downloadJSON } from "../utils/download.ts";
import SegmentList from "./SegmentList.vue";
import FlowEditor from "./FlowEditor.vue";
import RequestLoading from "./RequestLoading.vue";
import SettingsBack from "./SettingsBack.vue";
import { emptyFlowGraph, normalizeFlowGraph } from "../utils/flow.ts";
import type { SegmentItem } from "./SegmentList.vue";
import { VTab, VTabs, VTabsWindow, VTabsWindowItem, VSwitch, VBtn, VTextField, VTextarea, VSelect } from "vuetify/components";
import type { AbilityToggle, Agent, CreateAgentRequest, FlowGraph, TavernPreset, UpdateAgentRequest } from "../api/types.ts";

export interface AgentTemplate {
  name?: string;
  description?: string;
  kind?: "prompt" | "flow";
  system_prompt?: string;
  first_message?: string;
  prepend_state?: boolean;
  abilities?: Record<string, AbilityToggle>;
  segments?: string[];
  latest_prompt_segments?: string[];
}
interface AbilityDraft {
  enabled: boolean;
  model: string;
  modelSource: "global" | "session" | "agent";
  prompt: string;
  segments: string[];
}
interface ModelOption {
  value: string;
  label: string;
}

const props = withDefaults(defineProps<{
  agent?: Agent | null;
  isDefault?: boolean;
  template?: AgentTemplate | null;
  kind?: "prompt" | "flow";
  segments?: SegmentItem[];
  presets?: TavernPreset[];
  ask?: ((text: string) => Promise<boolean>) | null;
  escBlocked?: boolean;
  segmentRename?: { oldId: string; newId: string; revision: number } | null;
}>(), {
  agent: null,
  isDefault: false,
  template: null,
  kind: "prompt",
  segments: () => [],
  presets: () => [],
  ask: null,
  escBlocked: false,
  segmentRename: null,
});
const emit = defineEmits<{ close: []; saved: []; "edit-segment": [id: string] }>();

const api = sharedApi();
const isNew = !props.agent;
const agentKind = computed<"prompt" | "flow">(() => (props.agent?.kind === "flow" ? "flow" : props.agent ? "prompt" : props.kind));
// 页签：FlowAgent 是「流程 → 基本 → 能力」；传统 Agent 是「基本 → 系统提示词 → 能力」。
// FlowAgent 的流程图是它的主体，必须是最左、打开就落在这一页；能力按 `ABILITY_IDS`
// 生成，加能力自动多一页。
const tabs = computed<Array<{ id: string; label: string }>>(() => agentKind.value === "flow"
  ? [
      { id: "flow", label: "流程" },
      { id: "base", label: "基本" },
      // FlowAgent 暂不支持起标题/压缩这些能力，页签也不摆出来。
    ]
  : [
      { id: "base", label: "基本" },
      { id: "prompt", label: "系统提示词" },
      ...ABILITY_IDS.map((id) => ({ id, label: ABILITY_LABELS[id] ?? id })),
    ]);
const modelSourceOptions = [
  { label: "跟随全局设置", value: "global" },
  { label: "使用当前模型", value: "session" },
  { label: "跟随 Agent 设置", value: "agent" },
];
const agentModelOptions = computed(() => [{ label: "未选择（回落全局）", value: "" }, ...modelOptions.value]);
const sessionModelOptions = computed(() => [{ label: "跟随会话", value: "" }, ...modelOptions.value]);
const tab = ref(agentKind.value === "flow" ? "flow" : "base");
const aid = ref(props.agent?.id ?? "");
const name = ref(props.agent?.name ?? props.template?.name ?? "");
const description = ref(props.agent?.description ?? props.template?.description ?? "");
const prompt = ref(props.agent?.system_prompt ?? props.template?.system_prompt ?? "");
const firstMsg = ref(props.agent?.first_message ?? props.template?.first_message ?? "");
const rows = ref<Record<string, AbilityDraft>>({});
const prepend = ref(!!(props.agent?.prepend_state ?? props.template?.prepend_state));
// 系统提示词、最新用户消息与能力的段引用都使用同一个 SegmentList，编辑与排序规则一致。
const segs = ref<string[]>([...(props.agent?.segments ?? props.template?.segments ?? [])]);
const latestPromptSegs = ref<string[]>([...(props.agent?.latest_prompt_segments ?? props.template?.latest_prompt_segments ?? [])]);
const presetId = ref(props.agent?.preset_id ?? "");
const flowGraph = ref<FlowGraph>(normalizeFlowGraph(props.agent?.flow ?? emptyFlowGraph()));
const assembled = computed(() => assemblePrompt(prompt.value, segs.value, props.segments));
const latestPromptAssembled = computed(() => assemblePrompt("", latestPromptSegs.value, props.segments));
const modelOptions = ref<ModelOption[]>([]);
const status = ref("");
const loadingModels = shallowRef(false);
const modelsReady = shallowRef(false);
const modelLoadError = shallowRef("");
const action = shallowRef<"save" | "default" | "confirm-delete" | "delete" | null>(null);
let active = true;

// —— 能力里的提示词段（2026-10-28 用户定："用于 Compact 的提示词段"也要能自由组合）——
// 编辑器就是 `SegmentList`（与上面 agent 级**同一个组件**：同一个段库、同一条拼装规则、
// 同一套文案）。这里只剩"这一发拼出来是什么"和"要不要提醒一句"。
// 这能力**真正会发出去**的提示词（模板 + 段）。模板 = 你填的覆盖 ?: 那条内置模板段。
// 压缩的默认模板 2026-10-28 搬进段库（`压缩提示词`）⇒ 前端终于能把它显示全了
// （从前它在 Go 的常量里，预览只能显示"段"，不敢报总量）。
function abilityAssembled(id: string): string {
  const r = rows.value[id] ?? { prompt: "", segments: [] };
  return abilityPrompt(r.prompt, r.segments ?? [], props.segments, ABILITY_TEMPLATE_SEGMENTS[id]);
}
// 这个能力有没有"模板"（覆盖或内置段）：没有 ⇒ 预览要提醒"这一发只有你挑的段"
function abilityHasTemplate(id: string): boolean {
  const r = rows.value[id] ?? { prompt: "", segments: [] };
  if (String(r.prompt ?? "").trim()) return true;
  const candidates = ABILITY_TEMPLATE_SEGMENTS[id] ?? [];
  return candidates.some((candidate) => (r.segments ?? []).includes(candidate));
}
// 段清单上面那一行说明（`SegmentList` 的 hint）：只在"用户可能误判"的地方说一句。
//
// 在册的能力都有模板段（`ABILITY_TEMPLATE_SEGMENTS[0]` 是默认），三句话都建立在
// "这个能力有模板"之上 —— 没有模板的那种能力，是连流程都还没有的能力，它压根不该在册
// （`judge` 就是这个原因在 2026-10-28 退役的，别再把没有流程的 id 加回来）。
//
// 有多个候选时要**说清是替换不是叠加**（压缩有通用版与 RP 版）：不说的话，用户挑两条
// 只会得到一份自相矛盾的提示词 —— 一份写"不要复述原文"，另一份写"细节不能丢"。
function abilitySegHint(id: string): string {
  const candidates = ABILITY_TEMPLATE_SEGMENTS[id] ?? [];
  if (!candidates.length) return "";
  const r = rows.value[id] ?? { prompt: "", segments: [] };
  const others = candidates.slice(1);
  if (!(r.segments ?? []).length) {
    const more = others.length ? `；想换成「${others.join("」/「")}」就从下面挑它 —— 会整份替换默认模板，不会叠加。` : "";
    return `留空 = 使用默认模板（内置片段「${candidates[0]}」）${more}`;
  }
  if (!abilityHasTemplate(id)) {
    const all = candidates.map((one) => `「${one}」`).join(" / ");
    return `⚠ 没挑 ${all} 也没填自定义提示词 ⇒ 这一次不会带流程说明。`;
  }
  return "";
}

function fillRows(a?: Agent | AgentTemplate | null): void {
  const ab = a?.abilities ?? {};
  const out: Record<string, AbilityDraft> = {};
  for (const id of ABILITY_IDS) {
    const one = ab[id] ?? {};
    const both = one.provider && one.model ? modelKey(one.provider, one.model) : "";
    const source = ["global", "session", "agent"].includes(one.model_source ?? "")
      ? one.model_source as "global" | "session" | "agent"
      : (both ? "agent" : "global");
    // segments：能力的追加段（2026-10-28）。老配置没有这一格 ⇒ 空数组（不是 undefined，
    // 否则模板里 `rows[id].segments.length` 会在老数据上炸）。
    out[id] = {
      enabled: one.enabled !== false,
      model: both,
      // 标题模型来源（2026-11-04）：新/未配置 = global；旧配置有 provider/model = agent（保住旧覆盖）。
      modelSource: source,
      prompt: one.prompt ?? "",
      segments: [...(one.segments ?? [])],
    };
  }
  rows.value = out;
}
fillRows(props.agent ?? props.template);

watch(() => props.segmentRename?.revision, (revision) => {
  const change = props.segmentRename;
  if (!revision || !change) return;
  const remap = (ids: string[]): string[] => ids.map((id) => id === change.oldId ? change.newId : id);
  segs.value = remap(segs.value);
  latestPromptSegs.value = remap(latestPromptSegs.value);
  for (const row of Object.values(rows.value)) row.segments = remap(row.segments);
});

function toPatch(): Record<string, AbilityRow> {
  const out: Record<string, AbilityRow> = {};
  for (const id of ABILITY_IDS) {
    const r = rows.value[id] ?? { enabled: true, model: "", modelSource: "global", prompt: "", segments: [] };
    const parsed = parseModelKey(r.model ?? "");
    out[id] = {
      enabled: !!r.enabled,
      provider: parsed?.provider ?? "",
      model: parsed?.model ?? "",
      prompt: r.prompt ?? "",
      segments: [...(r.segments ?? [])],
    };
    // model_source 只有 title 认识（compact 的模型仍只有"跟随会话/指定"两档）。
    if (id === "title") out[id].model_source = r.modelSource || "global";
  }
  return out;
}

async function save(): Promise<void> {
  if (action.value) return;
  if (!name.value.trim()) { status.value = "给个名字"; return; }
  action.value = "save";
  try {
    const patch = buildAbilitiesPatch(toPatch());
    if (isNew) {
      const body: CreateAgentRequest = {
        name: name.value.trim(),
        description: description.value.trim(),
        kind: agentKind.value,
        ...(agentKind.value === "flow" ? { flow: flowGraph.value } : {}),
        system_prompt: prompt.value,
        first_message: firstMsg.value,
        segments: segs.value,
        preset_id: presetId.value,
        latest_prompt_segments: agentKind.value === "flow" ? [] : latestPromptSegs.value,
        ...(agentKind.value === "flow" ? {} : { abilities: patch }),
        ...(prepend.value ? { prepend_state: true } : {}),
      };
      await api.createAgent(body);
    } else {
      const agent = props.agent!;
      const body: UpdateAgentRequest = {
        description: description.value.trim(),
        kind: agentKind.value,
        ...(agentKind.value === "flow" ? { flow: flowGraph.value } : {}),
        system_prompt: prompt.value,
        first_message: firstMsg.value,
        segments: segs.value,
        preset_id: presetId.value,
        latest_prompt_segments: agentKind.value === "flow" ? [] : latestPromptSegs.value,
        ...(agentKind.value === "flow" ? {} : { abilities: patch }),
      };
      if (!!prepend.value !== !!agent.prepend_state) body.prepend_state = prepend.value;
      if (name.value.trim() !== agent.name) body.name = name.value.trim();
      if (aid.value.trim() && aid.value.trim() !== agent.id) body.new_id = aid.value.trim();
      await api.patchAgent(agent.id, body);
    }
    finishEdit();
  } catch (e: unknown) { status.value = `保存失败：${e instanceof Error ? e.message : String(e)}`; }
  finally { action.value = null; }
}

// 导出卡带（2026-10-11 用户定）：**只导 agent 自己** —— 用到的段**拼平**进 system_prompt、
// 不留引用（免除依赖，方便再分发；导入后要不要拆段由人后续处理）。
function exportCartridge(): void {
  const doc = buildCartridge({
    name: name.value.trim() || "未命名",
    description: description.value.trim(),
    system_prompt: prompt.value,
    segments: segs.value,
    first_message: firstMsg.value,
    latest_prompt_segments: latestPromptSegs.value,
    abilities: toPatch(),
    prepend_state: prepend.value,
  }, props.segments);
  downloadJSON(doc, `${name.value.trim() || "agent"}.card.json`);
  status.value = "已导出智能体文件";
}

async function makeDefault(): Promise<void> {
  if (isNew || action.value) return;
  action.value = "default";
  try {
    await api.patchAgent(props.agent!.id, { make_default: true });
    finishEdit();
  } catch (e: unknown) { status.value = `设默认失败：${e instanceof Error ? e.message : String(e)}`; }
  finally { action.value = null; }
}

async function remove(): Promise<void> {
  if (isNew || action.value) return;
  const agent = props.agent!;
  action.value = "confirm-delete";
  try {
    if (props.ask && !(await props.ask(`删除 Agent「${agent.name || agent.id}」？不可逆。`))) return;
    if (!active) return;
    action.value = "delete";
    await api.deleteAgent(agent.id);
    finishEdit();
  } catch (e: unknown) { status.value = `删除失败：${e instanceof Error ? e.message : String(e)}`; }
  finally { action.value = null; }
}

function finishEdit(): void {
  if (!active) return;
  emit("saved");
  emit("close");
}

async function loadModels(): Promise<void> {
  if (loadingModels.value) return;
  loadingModels.value = true;
  try {
    const models = await api.models();
    if (!active) return;
    modelOptions.value = (models ?? []).map((m) => ({
      value: modelKey(m.provider, m.upstream_id),
      label: `${m.provider}/${modelLabel(m)}`,
    }));
    modelsReady.value = true;
    modelLoadError.value = "";
  } catch (e: unknown) {
    if (active) modelLoadError.value = `读取模型列表失败：${e instanceof Error ? e.message : String(e)}`;
  } finally {
    loadingModels.value = false;
  }
}

onMounted(() => {
  if (agentKind.value !== "flow") void loadModels();
  document.addEventListener("keydown", onKey);
});
onUnmounted(() => {
  active = false;
  document.removeEventListener("keydown", onKey);
});
function onKey(e: KeyboardEvent): void {
  if (e.key === "Escape" && !action.value && !props.escBlocked) emit("close");
}
</script>

<template>
  <!-- 二级页（2026-10-10 起不再是弹窗）：嵌在设置视图的 Agent tab 里，列表 ⇄ 详情就地切换。 -->
  <div class="detail-pane agent-editor">
    <div class="panel-head">
      <SettingsBack :disabled="!!action" @back="emit('close')" />
      <span class="status">{{ isNew ? "新建智能体" : `编辑「${props.agent?.name ?? ""}」` }}{{ !isNew && isDefault ? "（默认）" : "" }}</span>
    </div>
    <RequestLoading v-if="loadingModels && !modelLoadError" label="正在读取模型列表…" />
    <div v-else-if="modelLoadError" class="row">
      <span class="status" role="status">{{ modelLoadError }}</span>
      <VBtn variant="text" size="small" type="button" :loading="loadingModels" :disabled="loadingModels" @click="loadModels">重新载入</VBtn>
    </div>
    <VTabs
      v-model="tab"
      class="agent-tabs"
      density="compact"
      align-tabs="start"
    >
      <VTab v-for="t in tabs" :key="t.id" :value="t.id" @focus="tab = t.id">{{ t.label }}</VTab>
    </VTabs>

    <VTabsWindow v-model="tab" class="agent-window" :inert="!!action">
    <VTabsWindowItem value="base">
      <section class="pane-section">
        <div class="form-group">
          <h3 class="group-title">身份</h3>
          <label class="field"><span>标识</span><VTextField density="compact" variant="outlined" hide-details v-model="aid" :disabled="isNew" placeholder="新建时自动生成" /></label>
          <label class="field"><span>名称</span><VTextField density="compact" variant="outlined" hide-details v-model="name" placeholder="例如：跑团主持人" /></label>
          <label class="field-block"><span>描述</span><VTextarea v-model="description" rows="2" density="compact" variant="outlined" hide-details placeholder="例如：负责旁白与 NPC，语气克制" /></label>
        </div>
        <div class="form-group">
          <h3 class="group-title">对话行为</h3>
          <label class="field-block"><span>开场白</span><VTextarea v-model="firstMsg" rows="3" density="compact" variant="outlined" hide-details placeholder="新会话里的第一条回复；留空则不启用" /></label>
          <template v-if="agentKind !== 'flow'">
            <label class="field"><span>在消息开头附上当前状态</span><VSwitch v-model="prepend" color="primary" density="compact" hide-details /></label>
            <label class="field"><span>提示词方案</span><VSelect v-model="presetId" :items="[{ title: '不使用（用本 Agent 的提示词）', value: '' }, ...presets.map(p => ({ title: p.filename, value: p.id }))]" density="compact" variant="outlined" hide-details placeholder="不使用" /></label>
            <p v-if="presetId" class="field-warning">
              <b>注意：</b>启用后，这份酒馆预设会接管该 Agent 的对话提示词。Agent 自己的系统提示词、追加片段和手工提示词方案不参与装配；所有使用这个 Agent 的会话都会从下一轮起改用预设。只有会话自己的「自定义提示词」仍然优先；模型、能力开关和状态注入不受影响。仅用于本轮最新用户消息的片段仍会追加，不会进入存档。这些设置不会丢，选「不使用」并保存即可恢复。
            </p>
            <p v-else class="hint">不选择预设时，使用这个 Agent 自己的系统提示词与片段。预设每轮现读现算；原始文件被删掉或解析失败时才回落到 Agent 自己的方案。</p>
          </template>
          <p v-else class="hint">FlowAgent 的对话逻辑由「流程」页决定；系统提示词、片段、提示词方案和高级能力暂不参与。</p>
        </div>
      </section>
    </VTabsWindowItem>

    <VTabsWindowItem v-if="agentKind !== 'flow'" value="prompt">
      <section class="pane-section">
        <div class="form-group">
          <h3 class="group-title">基础提示词</h3>
          <label class="field-block"><span>系统提示词（基础人设）</span><VTextarea v-model="prompt" rows="6" density="compact" variant="outlined" hide-details placeholder="写这个智能体的基础设定…" /></label>
          <SegmentList
            v-model:ids="segs"
            :segments="props.segments"
            :assembled="assembled"
            heading="提示词片段（追加在基础提示词后面）"
            @edit-segment="emit('edit-segment', $event)" />
        </div>
        <div class="form-group">
          <h3 class="group-title">最新用户消息片段</h3>
          <SegmentList
            v-model:ids="latestPromptSegs"
            :segments="props.segments"
            :assembled="latestPromptAssembled"
            heading="仅用于本轮最新用户消息"
            hint="只追加到本轮最新一条用户消息的出站内容；不会写入消息或进入后续轮次历史。"
            @edit-segment="emit('edit-segment', $event)" />
        </div>
      </section>
    </VTabsWindowItem>

    <VTabsWindowItem v-if="agentKind === 'flow'" value="flow" class="agent-flow-pane">
      <p class="hint flow-stage-note">FlowAgent 已能接收会话消息并落一条 assistant 回复；目前是整段执行（非流式、无工具）。FlowAgent 暂不沿用普通 Agent 的上下文面板，绿色上下文环也停用；新的上下文面板另行设计。</p>
      <FlowEditor v-model:graph="flowGraph" :agent-id="props.agent?.id ?? ''" />
    </VTabsWindowItem>

    <VTabsWindowItem v-if="agentKind !== 'flow'" v-for="id in ABILITY_IDS" :key="id" :value="id">
      <section class="pane-section">
        <label class="field"><span>启用</span><VSwitch v-model="rows[id].enabled" color="primary" density="compact" hide-details /></label>
        <template v-if="id === 'title'">
          <label class="field"><span>标题模型来源</span><VSelect v-model="rows[id].modelSource" :items="modelSourceOptions" item-title="label" item-value="value" density="compact" variant="outlined" hide-details /></label>
          <label v-if="rows[id].modelSource === 'agent'" class="field"><span>Agent 模型</span><VSelect v-model="rows[id].model" :items="agentModelOptions" :disabled="!modelsReady" item-title="label" item-value="value" density="compact" variant="outlined" hide-details /></label>
          <p class="hint">跟随全局设置：用设置 → 聊天 → 标题模型；全局空着就用当前会话模型。Agent 模型两项都填才生效，否则回落全局。</p>
        </template>
        <label v-else class="field"><span>模型</span><VSelect v-model="rows[id].model" :items="sessionModelOptions" :disabled="!modelsReady" item-title="label" item-value="value" density="compact" variant="outlined" hide-details /></label>
        <label class="field-block"><span>自定义提示词（留空使用默认模板）</span><VTextarea v-model="rows[id].prompt" rows="3" density="compact" variant="outlined" hide-details /></label>
        <SegmentList
          v-model:ids="rows[id].segments"
          :segments="props.segments"
          :assembled="abilityAssembled(id)"
          :hint="abilitySegHint(id)"
          heading="提示词片段（追加在模板后面）"
          @edit-segment="emit('edit-segment', $event)" />
      </section>
    </VTabsWindowItem>
    </VTabsWindow>

    <div class="row agent-save-row">
      <VBtn color="primary" variant="flat" size="small" type="button" :loading="action === 'save'" :disabled="!!action" @click="save">保存</VBtn>
      <span class="action-note">保存后，新会话与后续轮次使用新设定；不会改写已有消息。</span>
      <VBtn v-if="agentKind !== 'flow'" variant="text" size="small" type="button" @click="exportCartridge" title="导出智能体文件（JSON；提示词片段会展开成完整提示词）">导出智能体</VBtn>
      <VBtn variant="text" size="small" v-if="!isNew && !isDefault && agentKind !== 'flow'" type="button" :loading="action === 'default'" :disabled="!!action" @click="makeDefault">设为默认</VBtn>
      <VBtn color="error" variant="text" size="small" v-if="!isNew" type="button" :loading="action === 'delete'" :disabled="!!action" @click="remove">删除</VBtn>
      <span class="status">{{ status }}</span>
    </div>
  </div>
</template>

<style scoped>
.agent-editor { display: flex; min-height: 0; flex-direction: column; }
.agent-editor .panel-head { flex: none; }
.agent-editor .agent-tabs { flex: none; }
.agent-editor .agent-window { flex: 1 1 auto; min-height: 0; }
.agent-editor .pane-section { display: flex; flex-direction: column; gap: calc(1.5 * var(--chat-space)); max-width: 56rem; margin-inline: auto; padding: var(--chat-space) calc(2 * var(--chat-space)) calc(2 * var(--chat-space)); }
.agent-editor .form-group { display: flex; flex-direction: column; gap: var(--chat-space); }
.agent-editor .group-title { margin: 0; color: var(--md-on-surface); font-size: var(--md-type-title-small); font-weight: 600; }
.agent-editor .agent-save-row { flex: none; flex-wrap: wrap; align-items: center; gap: calc(var(--chat-space) / 2); padding: var(--chat-space) calc(2 * var(--chat-space)); border-top: 1px solid var(--md-outline-variant); background: var(--md-surface-container-low); }
.agent-editor .agent-save-row .v-btn { min-height: var(--md-touch-target); }
.agent-editor .action-note { flex: 1 1 16rem; }
@media (max-width: 600px) {
  .agent-editor .pane-section { padding-inline: var(--chat-space); }
  .agent-editor .agent-save-row { padding-inline: var(--chat-space); }
}
</style>
