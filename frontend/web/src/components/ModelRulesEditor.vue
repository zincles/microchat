<script setup lang="ts">
import { computed, onMounted, reactive, ref, shallowRef, watch } from "vue";
import { VBtn, VSelect, VSwitch, VTextField } from "vuetify/components";
import HelpTip from "./HelpTip.vue";
import RequestLoading from "./RequestLoading.vue";
import { sharedApi } from "../utils/prefs.ts";
import { THINKING_LEVELS, thinkingLabel } from "../utils/thinking.ts";
import { MODEL_ICON_OPTIONS } from "../utils/modelIcons.ts";
import type { BuiltinModelRule, ModelDefineRule, ModelRule } from "../api/types.ts";
type Location = { groupId: string | null; index: number };
type Group = Extract<ModelRule, { kind: "group" }>;
type BuiltinRule = Extract<BuiltinModelRule, { kind?: never }>;
type ListEntry = { kind: "group"; group: Group; topIndex: number } | { kind: "rule"; rule: ModelDefineRule; location: Location; number: number };
type BuiltinEntry = { kind: "group"; id: string; name: string } | { kind: "rule"; rule: BuiltinRule; index: number };
const props = withDefaults(defineProps<{ scope?: "global" | "provider"; rules?: ModelRule[]; blocked?: boolean; applying?: boolean }>(), { scope: "global", rules: () => [] });
const emit = defineEmits<{ save: [rules: ModelRule[]]; saved: [] }>();
const api = sharedApi();
const isProvider = computed(() => props.scope === "provider");
const heading = computed(() => isProvider.value ? "当前渠道的覆盖规则" : "全局模型定义规则");
const hint = computed(() => isProvider.value ? "只作用于当前渠道；应用在全局规则之后，同名字段会覆盖全局。修改后保存渠道生效。" : "系统内建全局规则只读、可停用；用户规则在它们之后生效，可覆盖内建字段。按列表顺序匹配原始模型 id。");
const rules = ref<ModelRule[]>(cloneRules(props.rules));
const builtinRules = ref<BuiltinModelRule[]>([]);
const disabledBuiltin = ref<string[]>([]);
const editing = ref<Location | "new" | null>(null);
const expanded = ref(new Set<string>());
const selectedGroup = ref("");
type Draft = { id: string; match_contains: string; match_exact: string; match_provider: string; match_protocol: string; match_all: boolean; is_final: boolean; brand: string; icon: string; server_tools: string[]; local_tools_text: string; replay_mode: "" | "always" | "never" | "tools" | "tools-never" };
const draft = reactive<Draft>({ id: "", match_contains: "", match_exact: "", match_provider: "", match_protocol: "", match_all: false, is_final: false, brand: "", icon: "", server_tools: [], local_tools_text: "", replay_mode: "" });
const groupName = ref("");
const addingGroup = ref(false);
const thinkingOn = ref(false);
const levels = ref<string[]>([]);
const status = ref("");
const action = shallowRef<"initial" | "reload" | "save" | null>(null);
const loaded = shallowRef(false);
const ready = computed(() => isProvider.value || loaded.value);
const busy = computed(() => props.blocked || !!action.value);
const groupOptions = computed(() => rules.value.flatMap((rule) => rule.kind === "group" ? [{ title: rule.name, value: rule.id }] : []));
const entries = computed<ListEntry[]>(() => {
  const result: ListEntry[] = []; let number = 0;
  rules.value.forEach((item, topIndex) => {
    if (item.kind === "group") {
      result.push({ kind: "group", group: item, topIndex });
      if (expanded.value.has(item.id)) item.rules.forEach((rule, index) => result.push({ kind: "rule", rule, location: { groupId: item.id, index }, number: ++number }));
    } else result.push({ kind: "rule", rule: item, location: { groupId: null, index: topIndex }, number: ++number });
  }); return result;
});
const builtinEntries = computed<BuiltinEntry[]>(() => {
  const result: BuiltinEntry[] = []; let index = 0;
  for (const item of builtinRules.value) {
    if (item.kind === "group") {
      result.push({ kind: "group", id: item.id, name: item.name });
      for (const child of item.rules) if (child.kind !== "group") result.push({ kind: "rule", rule: child, index: ++index });
    } else result.push({ kind: "rule", rule: item, index: ++index });
  } return result;
});
const levelOptions = THINKING_LEVELS.map((value) => ({ title: thinkingLabel(value), value }));
const iconOptions = MODEL_ICON_OPTIONS.map((option) => ({ title: option.label, value: option.value }));
const protocolOptions = [{ title: "不限制", value: "" }, { title: "对话补全（chat）", value: "openai-chat-completion" }, { title: "应答接口（response）", value: "openai-response" }];
const serverToolOptions = [{ title: "X 搜索（x_search）", value: "x_search" }, { title: "网页搜索（web_search）", value: "web_search" }, { title: "代码执行（code_execution）", value: "code_execution" }];
function cloneRule(rule: ModelDefineRule): ModelDefineRule { return { ...rule, thinking_levels: rule.thinking_levels ? [...rule.thinking_levels] : undefined, server_tools: rule.server_tools ? [...rule.server_tools] : undefined, local_tools: rule.local_tools ? [...rule.local_tools] : undefined }; }
function cloneRules(source: ModelRule[]): ModelRule[] { return source.map((rule) => rule.kind === "group" ? { ...rule, rules: rule.rules.map(cloneRule) } : cloneRule(rule)); }
function cloneBuiltinRule(rule: BuiltinModelRule): BuiltinModelRule {
  if (rule.kind === "group") return { ...rule, rules: rule.rules.map(cloneBuiltinRule) };
  return { ...rule, thinking_levels: rule.thinking_levels ? [...rule.thinking_levels] : undefined, server_tools: rule.server_tools ? [...rule.server_tools] : undefined, local_tools: rule.local_tools ? [...rule.local_tools] : undefined };
}
function cloneBuiltin(source: BuiltinModelRule[]): BuiltinModelRule[] { return source.map(cloneBuiltinRule); }
function resetDraft(): void { Object.assign(draft, { id: "", match_contains: "", match_exact: "", match_provider: "", match_protocol: "", match_all: false, is_final: false, brand: "", icon: "", server_tools: [], local_tools_text: "", replay_mode: "" }); thinkingOn.value = false; levels.value = []; selectedGroup.value = ""; }
async function load(source: "initial" | "reload"): Promise<void> {
  if (busy.value) return; editing.value = null; status.value = "";
  if (isProvider.value) { rules.value = cloneRules(props.rules); return; }
  action.value = source;
  try { const result = await api.modelRules(); rules.value = cloneRules(result.model_define_rules ?? []); builtinRules.value = cloneBuiltin(result.builtin_model_rules ?? []); disabledBuiltin.value = [...(result.disabled_builtin_model_rules ?? [])]; loaded.value = true; }
  catch (e: unknown) { status.value = `读取规则失败：${e instanceof Error ? e.message : String(e)}`; }
  finally { action.value = null; }
}
function openNew(): void { resetDraft(); editing.value = "new"; status.value = ""; }
function locate(location: Location): ModelDefineRule | undefined {
  if (location.groupId === null) { const item = rules.value[location.index]; return item && item.kind !== "group" ? item : undefined; }
  const group = rules.value.find((item) => item.kind === "group" && item.id === location.groupId); return group?.kind === "group" ? group.rules[location.index] : undefined;
}
function openEdit(location: Location): void {
  const rule = locate(location); if (!rule) return; resetDraft();
  draft.id = rule.id ?? ""; draft.match_contains = rule.match_contains ?? ""; draft.match_exact = rule.match_exact ?? ""; draft.match_provider = rule.match_provider ?? ""; draft.match_protocol = rule.match_protocol ?? "";
  draft.match_all = rule.match_all === true; draft.is_final = rule.is_final === true; draft.brand = rule.brand ?? ""; draft.icon = rule.icon ?? ""; draft.server_tools = [...(rule.server_tools ?? [])]; draft.local_tools_text = (rule.local_tools ?? []).join(", ");
  draft.replay_mode = rule.replay_reasoning_with_tools !== undefined
    ? rule.replay_reasoning_with_tools ? "tools" : "tools-never"
    : rule.replay_reasoning === true ? "always" : rule.replay_reasoning === false ? "never" : "";
  thinkingOn.value = Array.isArray(rule.thinking_levels); levels.value = [...(rule.thinking_levels ?? [])]; selectedGroup.value = location.groupId ?? ""; editing.value = location; status.value = "";
}
function cancelEdit(): void { editing.value = null; status.value = ""; }
function buildRule(): ModelDefineRule | null {
  const matchContains = draft.match_contains.trim(), matchExact = draft.match_exact.trim(), matchProvider = draft.match_provider.trim();
  if (!draft.match_all && !matchContains && !matchExact && !matchProvider) { status.value = "至少填一个匹配条件，或选择匹配所有模型"; return null; }
  const brand = draft.brand.trim(), icon = draft.icon.trim(), localTools = draft.local_tools_text.split(",").map((x) => x.trim()).filter(Boolean);
  if (!thinkingOn.value && !brand && !icon && draft.server_tools.length === 0 && localTools.length === 0 && !draft.replay_mode) { status.value = "至少填一个覆写字段"; return null; }
  if ((draft.server_tools.length > 0 || localTools.length > 0) && (thinkingOn.value || brand || icon)) { status.value = "追加工具不要和品牌 / 图标 / 思考写在同一规则里（拆成两条）"; return null; }
  const rule: ModelDefineRule = { is_final: draft.is_final }; const id = draft.id.trim();
  if (id) rule.id = id; if (draft.match_all) rule.match_all = true; if (matchContains) rule.match_contains = matchContains; if (matchExact) rule.match_exact = matchExact; if (matchProvider) rule.match_provider = matchProvider; if (draft.match_protocol) rule.match_protocol = draft.match_protocol;
  if (thinkingOn.value) rule.thinking_levels = [...levels.value]; if (draft.server_tools.length) rule.server_tools = [...draft.server_tools]; if (localTools.length) rule.local_tools = localTools; if (brand) rule.brand = brand; if (icon) rule.icon = icon;
  if (draft.replay_mode === "always") rule.replay_reasoning = true; if (draft.replay_mode === "never") rule.replay_reasoning = false;
  if (draft.replay_mode === "tools") rule.replay_reasoning_with_tools = true; if (draft.replay_mode === "tools-never") rule.replay_reasoning_with_tools = false;
  return rule;
}
function saveDraft(): void {
  const rule = buildRule(); if (!rule) return;
  if (editing.value === "new") {
    if (selectedGroup.value) { const group = rules.value.find((item) => item.kind === "group" && item.id === selectedGroup.value); if (group?.kind === "group") group.rules.push(rule); }
    else rules.value.push(rule);
  } else if (editing.value) {
    const old = editing.value;
    if (old.groupId === (selectedGroup.value || null)) {
      if (old.groupId) { const group = rules.value.find((item) => item.kind === "group" && item.id === old.groupId); if (group?.kind === "group") group.rules.splice(old.index, 1, rule); }
      else rules.value.splice(old.index, 1, rule);
    } else {
      if (old.groupId) { const group = rules.value.find((item) => item.kind === "group" && item.id === old.groupId); if (group?.kind === "group") group.rules.splice(old.index, 1); } else rules.value.splice(old.index, 1);
      if (selectedGroup.value) { const group = rules.value.find((item) => item.kind === "group" && item.id === selectedGroup.value); if (group?.kind === "group") group.rules.push(rule); } else rules.value.push(rule);
    }
  }
  editing.value = null; status.value = "已修改；点下面的保存按钮写回";
}
function deleteRule(location: Location): void { if (location.groupId) { const group = rules.value.find((item) => item.kind === "group" && item.id === location.groupId); if (group?.kind === "group") group.rules.splice(location.index, 1); } else rules.value.splice(location.index, 1); editing.value = null; status.value = "已删除；点下面的保存按钮写回"; }
function canMoveRule(location: Location, delta: number): boolean {
  const target = location.index + delta;
  if (location.groupId === null) return target >= 0 && target < rules.value.length;
  const group = rules.value.find((item) => item.kind === "group" && item.id === location.groupId);
  return group?.kind === "group" && target >= 0 && target < group.rules.length;
}
function moveRule(location: Location, delta: number): void {
  if (!canMoveRule(location, delta)) return;
  const target = location.index + delta;
  if (location.groupId === null) {
    const current = rules.value[location.index], neighbor = rules.value[target];
    if (!current || !neighbor) return;
    rules.value[location.index] = neighbor;
    rules.value[target] = current;
  } else {
    const group = rules.value.find((item) => item.kind === "group" && item.id === location.groupId);
    if (group?.kind !== "group") return;
    const current = group.rules[location.index], neighbor = group.rules[target];
    if (!current || !neighbor) return;
    group.rules[location.index] = neighbor;
    group.rules[target] = current;
  }
  status.value = "已调整顺序；点下面的保存按钮写回";
}
function moveGroup(index: number, delta: number): void { const target = index + delta; if (target < 0 || target >= rules.value.length || rules.value[index]?.kind !== "group") return; const current = rules.value[index], neighbor = rules.value[target]; if (!current || !neighbor) return; rules.value[index] = neighbor; rules.value[target] = current; status.value = "已调整分组顺序；点下面的保存按钮写回"; }
function addGroup(): void { groupName.value = ""; addingGroup.value = true; }
function saveGroup(): void { const name = groupName.value.trim(); if (!name) return; const id = globalThis.crypto.randomUUID(); rules.value.push({ kind: "group", id, name, rules: [] }); expanded.value.add(id); addingGroup.value = false; status.value = "已新增分组；点下面的保存按钮写回"; }
function cancelGroup(): void { addingGroup.value = false; groupName.value = ""; }
function deleteGroup(id: string): void { const index = rules.value.findIndex((item) => item.kind === "group" && item.id === id); const group = rules.value[index]; if (index >= 0 && group?.kind === "group") rules.value.splice(index, 1, ...group.rules); expanded.value.delete(id); status.value = "已移除分组，组内规则已移至未分组列表；点下面的保存按钮写回"; }
function toggleGroup(id: string): void { const next = new Set(expanded.value); if (next.has(id)) next.delete(id); else next.add(id); expanded.value = next; }
function builtinDisabled(id: string | undefined): boolean { return !!id && disabledBuiltin.value.includes(id); }
function setBuiltinEnabled(id: string | undefined, on: boolean): void { if (!id) return; const next = new Set(disabledBuiltin.value); if (on) next.delete(id); else next.add(id); disabledBuiltin.value = [...next]; status.value = "已修改；点下面的保存按钮写回"; }
async function saveAll(): Promise<void> {
  if (busy.value || !ready.value) return; if (editing.value !== null) { status.value = "先完成正在编辑的规则"; return; } status.value = "";
  if (isProvider.value) { emit("save", cloneRules(rules.value)); return; }
  action.value = "save";
  try { const result = await api.saveModelRules({ model_define_rules: cloneRules(rules.value), disabled_builtin_model_rules: [...disabledBuiltin.value] }); rules.value = cloneRules(result.model_define_rules ?? []); builtinRules.value = cloneBuiltin(result.builtin_model_rules ?? []); disabledBuiltin.value = [...(result.disabled_builtin_model_rules ?? [])]; status.value = "已保存"; emit("saved"); }
  catch (e: unknown) { status.value = `保存失败：${e instanceof Error ? e.message : String(e)}`; } finally { action.value = null; }
}
function conditionText(rule: ModelDefineRule): string { const bits: string[] = []; if (rule.match_all) bits.push("所有模型"); if (rule.match_contains) bits.push(`包含 ${rule.match_contains}`); if (rule.match_exact) bits.push(`等于 ${rule.match_exact}`); if (rule.match_provider) bits.push(`渠道 ${rule.match_provider}`); if (rule.match_protocol) bits.push(`协议 ${rule.match_protocol}`); return bits.join(" · "); }
function defineText(rule: ModelDefineRule): string {
  const bits: string[] = []; if (Array.isArray(rule.thinking_levels)) bits.push(rule.thinking_levels.length ? `思考 ${rule.thinking_levels.map(thinkingLabel).join("/")}` : "思考 清空");
  if (rule.brand) bits.push(`品牌 ${rule.brand}`); if (rule.icon) bits.push(`图标 ${rule.icon}`); if (rule.server_tools?.length) bits.push(`服务端 ${rule.server_tools.join(" / ")}`); if (rule.local_tools?.length) bits.push(`本地 ${rule.local_tools.join(" / ")}`);
  if (rule.replay_reasoning_with_tools !== undefined) bits.push(rule.replay_reasoning_with_tools ? "工具启用时回传思考" : "工具启用时不回传思考"); else if (rule.replay_reasoning === true) bits.push("回传思考"); else if (rule.replay_reasoning === false) bits.push("不回传思考"); if (rule.is_final) bits.push("命中即停"); return bits.join(" · ");
}
watch(() => props.rules, (next) => { if (isProvider.value && editing.value === null) rules.value = cloneRules(next); }, { deep: true });
onMounted(() => { if (!isProvider.value) void load("initial"); });
</script>

<template>
  <section class="model-rules">
    <div class="rules-head">
      <div><h3>{{ heading }}</h3><p class="hint">{{ hint }}</p></div>
      <div class="rules-create">
        <VBtn variant="outlined" size="small" type="button" :disabled="busy || !ready" @click="addGroup">新增分组</VBtn>
        <VBtn color="primary" variant="flat" size="small" type="button" :disabled="busy || !ready" @click="openNew">新增规则</VBtn>
      </div>
    </div>
    <div v-if="addingGroup" class="group-editor">
      <VTextField v-model="groupName" density="compact" variant="outlined" hide-details autofocus label="分组名称" placeholder="例如 模型图标" @keydown.enter.prevent="saveGroup" />
      <VBtn color="primary" variant="flat" size="small" type="button" :disabled="busy || !groupName.trim()" @click="saveGroup">创建分组</VBtn>
      <VBtn variant="text" size="small" type="button" @click="cancelGroup">取消</VBtn>
    </div>
    <RequestLoading v-if="action === 'initial'" label="正在读取全局模型规则…" />
    <template v-if="ready">
      <template v-if="!isProvider">
        <div class="rules-subhead"><h4>系统内建全局规则</h4><span class="rule-badge">只读 · 可停用</span></div>
        <div v-if="builtinEntries.length" class="rule-list builtin-list">
          <template v-for="(entry, index) in builtinEntries" :key="entry.kind === 'group' ? entry.id : `builtin-${entry.rule.id ?? index}`">
            <div v-if="entry.kind === 'group'" class="group-heading builtin-group"><strong>{{ entry.name }}</strong></div>
            <div v-else class="rule-row builtin" :class="{ disabled: builtinDisabled(entry.rule.id) }">
              <div class="rule-index">{{ entry.index }}</div>
              <div class="rule-body"><div class="rule-cond">{{ conditionText(entry.rule) || "（没有匹配条件）" }}</div><div class="rule-def">{{ defineText(entry.rule) || "（没有覆写字段）" }}</div><div v-if="entry.rule.id" class="rule-id">id: {{ entry.rule.id }}</div></div>
              <div class="rule-ops"><VSwitch :model-value="!builtinDisabled(entry.rule.id)" density="compact" hide-details :disabled="busy" :label="builtinDisabled(entry.rule.id) ? '已停用' : '启用'" @update:model-value="(v) => setBuiltinEnabled(entry.rule.id, v === true)" /></div>
            </div>
          </template>
        </div>
        <div v-else class="status">（系统内建全局规则为空）</div>
        <div class="rules-subhead"><h4>用户规则</h4><span class="hint">在内置规则之后应用，可覆盖同名字段。</span></div>
      </template>
      <div v-if="rules.length" class="rule-list">
        <template v-for="(entry, index) in entries" :key="entry.kind === 'group' ? entry.group.id : `${entry.location.groupId ?? 'root'}-${entry.rule.id ?? entry.location.index}`">
          <div v-if="entry.kind === 'group'" class="group-heading">
            <VBtn class="group-toggle" variant="text" size="small" type="button" :aria-expanded="expanded.has(entry.group.id)" @click="toggleGroup(entry.group.id)">{{ expanded.has(entry.group.id) ? '▾' : '▸' }} {{ entry.group.name }} <span>({{ entry.group.rules.length }})</span></VBtn>
            <div class="rule-ops"><VBtn variant="text" size="small" type="button" :disabled="busy || entry.topIndex === 0" @click="moveGroup(entry.topIndex, -1)">上移组</VBtn><VBtn variant="text" size="small" type="button" :disabled="busy || entry.topIndex === rules.length - 1" @click="moveGroup(entry.topIndex, 1)">下移组</VBtn><VBtn color="error" variant="text" size="small" type="button" :disabled="busy" @click="deleteGroup(entry.group.id)">删除组</VBtn></div>
          </div>
          <div v-else class="rule-row" :class="{ nested: entry.location.groupId }">
            <div class="rule-index">{{ entry.number }}</div>
            <div class="rule-body"><div class="rule-cond">{{ conditionText(entry.rule) || "（没有匹配条件）" }}</div><div class="rule-def">{{ defineText(entry.rule) || "（没有覆写字段）" }}</div><div v-if="entry.rule.id" class="rule-id">id: {{ entry.rule.id }}</div></div>
            <div class="rule-ops"><VBtn variant="text" size="small" type="button" :disabled="busy || !canMoveRule(entry.location, -1)" @click="moveRule(entry.location, -1)">↑</VBtn><VBtn variant="text" size="small" type="button" :disabled="busy || !canMoveRule(entry.location, 1)" @click="moveRule(entry.location, 1)">↓</VBtn><VBtn variant="text" size="small" type="button" :disabled="busy" @click="openEdit(entry.location)">编辑</VBtn><VBtn color="error" variant="text" size="small" type="button" :disabled="busy" @click="deleteRule(entry.location)">删除</VBtn></div>
          </div>
        </template>
      </div>
      <div v-else class="status">（还没有用户规则）</div>
      <div v-if="editing !== null" class="rule-editor">
        <h4>{{ editing === "new" ? "新增规则" : "编辑规则" }}</h4>
        <label class="field rule-id-field"><span>规则 id（可选）<HelpTip text="用于 matched_rules 里标识来源。" /></span><VTextField v-model="draft.id" density="compact" variant="outlined" hide-details placeholder="例如 xai-grok" /></label>
        <label class="field"><span>所属分组</span><VSelect v-model="selectedGroup" :items="[{ title: '未分组', value: '' }, ...groupOptions]" item-title="title" item-value="value" density="compact" variant="outlined" hide-details /></label>
        <section class="rule-section">
          <h5>匹配</h5>
          <div class="switch-row"><VSwitch v-model="draft.match_all" density="compact" hide-details label="匹配所有模型" /></div>
          <label class="field"><span>包含</span><VTextField v-model="draft.match_contains" density="compact" variant="outlined" hide-details placeholder="例如 grok" /></label>
          <label class="field"><span>精确</span><VTextField v-model="draft.match_exact" density="compact" variant="outlined" hide-details placeholder="例如 grok-4.7" /></label>
          <label class="field"><span>渠道（可选）</span><VTextField v-model="draft.match_provider" density="compact" variant="outlined" hide-details placeholder="留空 = 所有渠道" /></label>
          <label class="field"><span>协议（可选）</span><VSelect v-model="draft.match_protocol" :items="protocolOptions" item-title="title" item-value="value" density="compact" variant="outlined" hide-details /></label>
          <div class="switch-row"><VSwitch v-model="draft.is_final" density="compact" hide-details label="命中后停止继续匹配" /></div>
        </section>
        <section class="rule-section">
          <h5>修改</h5>
          <label class="field"><span>品牌</span><VTextField v-model="draft.brand" density="compact" variant="outlined" hide-details placeholder="例如 xAI" /></label>
          <label class="field"><span>图标</span><VSelect v-model="draft.icon" :items="iconOptions" item-title="title" item-value="value" clearable density="compact" variant="outlined" hide-details placeholder="不设置" /></label>
          <div class="switch-row"><VSwitch v-model="thinkingOn" density="compact" hide-details label="覆写思考档位" /></div>
          <label v-if="thinkingOn" class="field"><span>思考档位</span><VSelect v-model="levels" :items="levelOptions" item-title="title" item-value="value" multiple density="compact" variant="outlined" hide-details /></label>
          <label class="field"><span>服务端工具</span><VSelect v-model="draft.server_tools" :items="serverToolOptions" item-title="title" item-value="value" multiple density="compact" variant="outlined" hide-details placeholder="不追加" /></label>
          <label class="field"><span>本地工具</span><VTextField v-model="draft.local_tools_text" density="compact" variant="outlined" hide-details placeholder="例如 roll_dice, lookup" /></label>
          <label class="field"><span>回传思考</span><VSelect v-model="draft.replay_mode" :items="[{ title: '不设置', value: '' }, { title: '始终回传', value: 'always' }, { title: '不回传', value: 'never' }, { title: '按启用工具数量自动判断（有工具时回传）', value: 'tools' }, { title: '按启用工具数量自动判断（有工具时不回传）', value: 'tools-never' }]" item-title="title" item-value="value" density="compact" variant="outlined" hide-details /></label>
        </section>
        <div class="rule-actions"><VBtn variant="text" size="small" type="button" :disabled="busy" @click="cancelEdit">取消</VBtn><VBtn color="primary" variant="flat" size="small" type="button" :disabled="busy" @click="saveDraft">完成编辑</VBtn></div>
      </div>
    </template>
    <div class="row rules-save"><VBtn color="primary" variant="flat" size="small" type="button" :loading="action === 'save' || applying" :disabled="busy || !ready" @click="saveAll">{{ isProvider ? "应用到渠道" : "保存全局规则" }}</VBtn><VBtn variant="text" size="small" type="button" :loading="action === 'reload'" :disabled="busy" @click="load('reload')">重新载入</VBtn><span class="status" role="status">{{ status }}</span></div>
  </section>
</template>

<style scoped>
.model-rules { margin-top: 1.2em; }
.rules-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 0.8em; }
.rules-create { display: flex; flex-wrap: wrap; gap: 0.25em; }
.group-editor { display: flex; align-items: center; flex-wrap: wrap; gap: 0.3em; margin-top: 0.45em; }
.group-editor :deep(.v-input) { flex: 1 1 12em; min-width: min(12em, 100%); }
.group-heading { display: flex; align-items: center; justify-content: space-between; gap: 0.3em; padding: 0.15em 0.2em; border-bottom: 1px solid var(--line); background: var(--md-surface-container-low); }
.group-heading strong { font-size: var(--md-type-label-large); font-weight: 600; }
.group-toggle { min-width: 0; justify-content: flex-start; text-transform: none; }
.group-heading span { color: var(--tg-hint); font-size: 0.85em; }
.rules-head h3 { margin: 0; }
.rules-head .hint { margin: 0.25em 0 0; max-width: 62em; }
.rules-subhead { display: flex; align-items: baseline; gap: 0.5em; flex-wrap: wrap; margin-top: 0.9em; }
.rules-subhead h4 { margin: 0; font-size: var(--md-type-title-small); font-weight: 500; }
.rule-badge { color: var(--md-on-surface-variant); font-size: 0.8em; border: 1px solid var(--md-outline-variant); border-radius: var(--md-shape-full); padding: 0 0.5em; line-height: 1.6; }
.rule-row.disabled { opacity: 0.55; }
.rule-row.builtin .rule-ops { min-width: 7em; }
.rule-list { margin-top: 0.6em; border-top: 1px solid var(--line); }
.rule-row { display: grid; grid-template-columns: 1.5em minmax(0, 1fr) auto; gap: 0.35em; align-items: center; padding: 0.3em 0; border-bottom: 1px solid var(--line); }
.rule-index { color: var(--tg-hint); font-variant-numeric: tabular-nums; text-align: right; }
.rule-cond { color: var(--md-on-surface); }
.rule-def { color: var(--tg-hint); font-size: 0.9em; margin-top: 0.15em; }
.rule-id { color: var(--tg-hint); font-size: 0.8em; margin-top: 0.15em; }
.rule-ops { display: flex; align-items: center; gap: 0.1em; flex-wrap: wrap; justify-content: flex-end; }
.rule-ops :deep(.v-btn) { min-width: 0; padding-inline: 0.35em; }
.rule-cond, .rule-def { display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; overflow: hidden; }
.rule-editor { margin-top: 0.9em; padding: 0.8em 0.9em; border: 1px solid var(--line); border-radius: var(--r-card); background: var(--md-surface-container-low); }
.rule-editor h4 { margin: 0 0 0.6em; font-size: var(--md-type-title-small); font-weight: 500; }
.rule-editor .field { grid-template-columns: 9em minmax(0, 1fr); gap: 0.5em; margin: 0.45em 0; }
.rule-id-field { margin-top: 0 !important; }
.rule-section { margin-top: 0.7em; padding-top: 0.55em; border-top: 1px solid var(--line); }
.rule-section h5 { margin: 0 0 0.25em; color: var(--tg-hint); font-size: 0.8em; font-weight: 600; letter-spacing: 0.04em; }
.switch-row { display: flex; align-items: center; min-height: 2.2em; }
.rule-actions { display: flex; justify-content: flex-end; gap: 0.4em; margin-top: 0.7em; }
.rules-save { margin-top: 0.7em; }
@media (max-width: 700px) {
  .group-heading { flex-wrap: wrap; }
  .group-heading .rule-ops { width: 100%; justify-content: flex-start; }
  .rule-row { grid-template-columns: 1.3em minmax(0, 1fr); }
  .rule-ops { grid-column: 1 / -1; justify-content: flex-start; }
  .rule-editor .field { grid-template-columns: 1fr; gap: 0.25em; }
}
</style>
