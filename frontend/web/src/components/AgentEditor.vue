<script setup lang="ts">
// Agent 编辑器（设置视图 Agent tab 内的二级页，列表 ⇄ 详情就地切换）：新建 / 改 id·名 / 提示词 / 能力开关 / 设默认 / 删除。
// isNew（boolean）：无 agent 快照=新建（空表单）；有=编辑（带入快照，保存后父重拉，id 变了也对得上）。关即销毁。
//
// 2026-10-28 用户：「在具体某个 Agent 的设置界面里，我们最好也分 TAB，不然的话，全挤在一起很丑。」
// ⇒ 一条长滚动拆成页签：基本 / 系统提示词 / 每个能力一页（页签条用 Vuetify `VTabs`，与设置主栏同一套）。
// 保存一行留在页签**外面**（哪一页都能保存；翻页不丢已填的东西，状态都在 ref 里）。
import { ref, computed, onMounted, onUnmounted } from "vue";
import { sharedApi } from "../utils/prefs.ts";
import { ABILITY_IDS, ABILITY_LABELS, ABILITY_TEMPLATE_SEGMENTS, buildAbilitiesPatch, modelKey, parseModelKey, modelLabel } from "../utils/format.ts";
import type { AbilityRow } from "../utils/format.ts";
import { assemblePrompt, abilityPrompt } from "../utils/prompt.ts";
import { buildCartridge } from "../utils/cartridge.ts";
import { downloadJSON } from "../utils/download.ts";
import SegmentList from "./SegmentList.vue";
import type { SegmentItem } from "./SegmentList.vue";
import { VTab, VTabs, VTabsWindow, VTabsWindowItem, VSwitch, VBtn, VTextField, VTextarea, VSelect } from "vuetify/components";
import type { AbilityToggle, Agent, CreateAgentRequest, TavernPreset, UpdateAgentRequest } from "../api/types.ts";

interface AgentTemplate {
  name?: string;
  system_prompt?: string;
  first_message?: string;
  prepend_state?: boolean;
  abilities?: Record<string, AbilityToggle>;
  segments?: string[];
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
  template?: AgentTemplate | null; // 新建模板 {name, system_prompt, prepend_state, abilities}
  segments?: SegmentItem[]; // 段库（picker 用；随 GET /agents 一起给）
  presets?: TavernPreset[]; // 酒馆原始预设（绑定用）
  ask?: ((text: string) => Promise<boolean>) | null; // 全局确认框（删除前先摊开）
}>(), {
  agent: null,
  isDefault: false,
  template: null,
  segments: () => [],
  presets: () => [],
  ask: null,
});
const emit = defineEmits<{ close: []; saved: [] }>();

const api = sharedApi();
const isNew = !props.agent;
// 页签：基本 → 系统提示词 → 每个能力一页（顺序跟着 `ABILITY_IDS` 走，加能力就自动多一页）。
const tabs: Array<{ id: string; label: string }> = [
  { id: "base", label: "基本" },
  { id: "prompt", label: "系统提示词" },
  ...ABILITY_IDS.map((id) => ({ id, label: ABILITY_LABELS[id] ?? id })),
];
const modelSourceOptions = [
  { label: "跟随全局设置", value: "global" },
  { label: "使用当前模型", value: "session" },
  { label: "跟随 Agent 设置", value: "agent" },
];
const agentModelOptions = computed(() => [{ label: "未选择（回落全局）", value: "" }, ...modelOptions.value]);
const sessionModelOptions = computed(() => [{ label: "跟随会话", value: "" }, ...modelOptions.value]);
const tab = ref("base");
const aid = ref(props.agent?.id ?? "");
const name = ref(props.agent?.name ?? props.template?.name ?? "");
const prompt = ref(props.agent?.system_prompt ?? props.template?.system_prompt ?? "");
const firstMsg = ref(props.agent?.first_message ?? props.template?.first_message ?? "");
const rows = ref<Record<string, AbilityDraft>>({});
const prepend = ref(!!(props.agent?.prepend_state ?? props.template?.prepend_state));
// 追加段（顺序 = 拼接顺序）。段库在 `props.segments`；挑 / 排 / 删与预览都在
// `SegmentList` 里（与能力那块**同一个组件** —— 用户 2026-10-28：「不协调」）。
const segs = ref<string[]>([...(props.agent?.segments ?? props.template?.segments ?? [])]);
const presetId = ref(props.agent?.preset_id ?? "");
const assembled = computed(() => assemblePrompt(prompt.value, segs.value, props.segments));
const modelOptions = ref<ModelOption[]>([]);
const status = ref("");

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
  if (!name.value.trim()) { status.value = "给个名字"; return; }
  const patch = buildAbilitiesPatch(toPatch());
  try {
    if (isNew) {
      const body: CreateAgentRequest = {
        name: name.value.trim(),
        system_prompt: prompt.value,
        first_message: firstMsg.value,
        segments: segs.value,
        preset_id: presetId.value,
        abilities: patch,
        ...(prepend.value ? { prepend_state: true } : {}),
      };
      await api.createAgent(body);
    } else {
      const agent = props.agent!;
      const body: UpdateAgentRequest = {
        system_prompt: prompt.value,
        first_message: firstMsg.value,
        segments: segs.value,
        preset_id: presetId.value,
        abilities: patch,
      };
      if (!!prepend.value !== !!agent.prepend_state) body.prepend_state = prepend.value;
      if (name.value.trim() !== agent.name) body.name = name.value.trim();
      if (aid.value.trim() && aid.value.trim() !== agent.id) body.new_id = aid.value.trim();
      await api.patchAgent(agent.id, body);
    }
    emit("saved");
    emit("close");
  } catch (e: unknown) { status.value = `保存失败：${e instanceof Error ? e.message : String(e)}`; }
}

// 导出卡带（2026-10-11 用户定）：**只导 agent 自己** —— 用到的段**拼平**进 system_prompt、
// 不留引用（免除依赖，方便再分发；导入后要不要拆段由人后续处理）。
function exportCartridge(): void {
  const doc = buildCartridge({
    name: name.value.trim() || "未命名",
    system_prompt: prompt.value,
    segments: segs.value,
    first_message: firstMsg.value,
    abilities: toPatch(),
    prepend_state: prepend.value,
  }, props.segments);
  downloadJSON(doc, `${name.value.trim() || "agent"}.card.json`);
  status.value = "已导出智能体文件";
}

async function makeDefault(): Promise<void> {
  if (isNew) return;
  try {
    await api.patchAgent(props.agent!.id, { make_default: true });
    emit("saved");
    emit("close");
  } catch (e: unknown) { status.value = `设默认失败：${e instanceof Error ? e.message : String(e)}`; }
}

async function remove(): Promise<void> {
  if (isNew) return;
  const agent = props.agent!;
  if (props.ask && !(await props.ask(`删除 Agent「${agent.name || agent.id}」？不可逆。`))) return;
  try {
    await api.deleteAgent(agent.id);
    emit("saved");
    emit("close");
  } catch (e: unknown) { status.value = `删除失败：${e instanceof Error ? e.message : String(e)}`; }
}

onMounted(async () => {
  try {
    const models = await api.models();
    modelOptions.value = (models ?? []).map((m) => ({
      value: modelKey(m.provider, m.upstream_id),
      label: `${m.provider}/${modelLabel(m)}`,
    }));
  } catch {}
  document.addEventListener("keydown", onKey);
});
onUnmounted(() => document.removeEventListener("keydown", onKey));
function onKey(e: KeyboardEvent): void {
  if (e.key === "Escape") emit("close");
}
</script>

<template>
  <!-- 二级页（2026-10-10 起不再是弹窗）：嵌在设置视图的 Agent tab 里，列表 ⇄ 详情就地切换。 -->
  <div class="detail-pane">
    <div class="panel-head">
      <VBtn icon variant="text" size="small" type="button" title="返回列表（Esc）" @click="emit('close')">←</VBtn>
      <span class="status">{{ isNew ? "新建智能体" : `编辑「${props.agent?.name ?? ""}」` }}{{ !isNew && isDefault ? "（默认）" : "" }}</span>
    </div>
    <VTabs
      v-model="tab"
      class="agent-tabs"
      density="compact"
      align-tabs="start"
    >
      <VTab v-for="t in tabs" :key="t.id" :value="t.id" @focus="tab = t.id">{{ t.label }}</VTab>
    </VTabs>

    <VTabsWindow v-model="tab" class="agent-window">
    <VTabsWindowItem value="base">
    <section class="pane-section">
      <label class="field"><span>标识（改名会同步所有引用）</span><VTextField density="compact" variant="outlined" hide-details v-model="aid" :disabled="isNew" placeholder="新建时自动生成" /></label>
      <label class="field"><span>名称</span><VTextField density="compact" variant="outlined" hide-details v-model="name" placeholder="例如：跑团主持人" /></label>
      <label class="field-block"><span>开场白（新会话里的第一条回复；留空 = 不启用）</span><VTextarea v-model="firstMsg" rows="3" density="compact" variant="outlined" hide-details placeholder="例如：你好，我是……（可以包含状态块来设置初始状态）" /></label>
      <label class="field"><span>在消息开头附上当前状态（角色扮演建议开启）</span><VSwitch v-model="prepend" color="primary" density="compact" hide-details /></label>
      <label class="field"><span>提示词方案（酒馆预设）</span><VSelect v-model="presetId" :items="[{ title: '不使用（用本 Agent 的提示词）', value: '' }, ...presets.map(p => ({ title: p.filename, value: p.id }))]" density="compact" variant="outlined" hide-details placeholder="不使用" /></label>
      <p class="hint">绑定后该 Agent 的会话使用原始预设的提示词计划；Agent 的 base / 段不参与。预设原件删掉后会回落。</p>
    </section>
    </VTabsWindowItem>

    <VTabsWindowItem value="prompt">
    <section class="pane-section">
      <label class="field-block"><span>系统提示词（基础人设）</span><VTextarea v-model="prompt" rows="6" density="compact" variant="outlined" hide-details placeholder="写这个智能体的基础设定…" /></label>
      <SegmentList
        v-model:ids="segs"
        :segments="props.segments"
        :assembled="assembled"
        heading="提示词片段（追加在基础提示词后面，顺序就是拼接顺序）" />
    </section>
    </VTabsWindowItem>

    <VTabsWindowItem v-for="id in ABILITY_IDS" :key="id" :value="id">
    <section class="pane-section">
      <label class="field"><span>启用（关闭后这项功能不会运行）</span><VSwitch v-model="rows[id].enabled" color="primary" density="compact" hide-details /></label>
      <template v-if="id === 'title'">
        <label class="field"><span>标题模型来源</span><VSelect v-model="rows[id].modelSource" :items="modelSourceOptions" item-title="label" item-value="value" density="compact" variant="outlined" hide-details /></label>
        <label v-if="rows[id].modelSource === 'agent'" class="field"><span>Agent 模型</span><VSelect v-model="rows[id].model" :items="agentModelOptions" item-title="label" item-value="value" density="compact" variant="outlined" hide-details /></label>
        <p class="hint">跟随全局设置：用设置 → 聊天 → 标题模型；全局空着就用当前会话模型。Agent 模型两项都填才生效，否则回落全局。</p>
      </template>
      <label v-else class="field"><span>模型</span><VSelect v-model="rows[id].model" :items="sessionModelOptions" item-title="label" item-value="value" density="compact" variant="outlined" hide-details /></label>
      <label class="field-block"><span>自定义提示词（留空 = 使用默认模板）</span><VTextarea v-model="rows[id].prompt" rows="3" density="compact" variant="outlined" hide-details /></label>
      <!-- 提示词段：追加在（覆盖 ?: 默认）模板**后面**，顺序 = 这里的顺序。
           改了模板不必把段里的字抄进来 —— 抄进来就是两份，改一处忘一处。
           编辑器就是 agent 级那一个 `SegmentList`（同一个组件 ⇒ 不会各长一样）。 -->
      <SegmentList
        v-model:ids="rows[id].segments"
        :segments="props.segments"
        :assembled="abilityAssembled(id)"
        :hint="abilitySegHint(id)"
        heading="提示词片段（追加在模板后面，顺序就是拼接顺序）" />
    </section>
    </VTabsWindowItem>
    </VTabsWindow>

    <div class="row">
      <VBtn color="primary" variant="flat" size="small" type="button" @click="save">保存</VBtn>
      <VBtn variant="text" size="small" type="button" @click="exportCartridge" title="导出智能体文件（JSON；提示词片段会展开成完整提示词）">导出智能体</VBtn>
      <VBtn variant="text" size="small" v-if="!isNew && !isDefault" type="button" @click="makeDefault">设为默认</VBtn>
      <VBtn color="error" variant="text" size="small" v-if="!isNew" type="button" @click="remove">删除</VBtn>
      <span class="status">{{ status }}</span>
    </div>
  </div>
</template>
