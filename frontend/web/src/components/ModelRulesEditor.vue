<script setup lang="ts">
// 模型定义规则：结构化编辑器（不是裸 JSON）。
//
// 规则是纯数据 override：匹配 raw 模型 id，覆盖 brand / icon / thinking_levels。
// global = 所有渠道共享；provider = 当前渠道自己的列表，应用在全局之后、优先。
import { computed, onMounted, reactive, ref, watch } from "vue";
import { VBtn, VSelect, VSwitch, VTextField } from "vuetify/components";
import { sharedApi } from "../utils/prefs.ts";
import { THINKING_LEVELS, thinkingLabel } from "../utils/thinking.ts";
import type { ModelDefineRule } from "../api/types.ts";

const props = withDefaults(defineProps<{
  scope?: "global" | "provider";
  rules?: ModelDefineRule[];
}>(), { scope: "global", rules: () => [] });
const emit = defineEmits<{ save: [rules: ModelDefineRule[]]; saved: [] }>();

const api = sharedApi();
const isProvider = computed(() => props.scope === "provider");
const heading = computed(() => isProvider.value ? "当前渠道的覆盖规则" : "全局模型定义规则");
const hint = computed(() => isProvider.value
  ? "只作用于当前渠道；应用在全局规则之后，同名字段会覆盖全局。修改后保存渠道生效。"
  : "所有渠道共享的默认规则；按列表顺序匹配原始模型 id，命中的字段覆盖前面的。");

const rules = ref<ModelDefineRule[]>([]);
const editing = ref<number | "new" | null>(null);
const draft = reactive({
  id: "",
  match_contains: "",
  match_exact: "",
  match_provider: "",
  is_final: false,
  brand: "",
  icon: "",
});
const thinkingOn = ref(false);
const levels = ref<string[]>([]);
const status = ref("");
const busy = ref(false);

const levelOptions = THINKING_LEVELS.map((value) => ({ title: thinkingLabel(value), value }));
const levelText = (value: string): string => thinkingLabel(value);

function cloneRules(source: ModelDefineRule[]): ModelDefineRule[] {
  return JSON.parse(JSON.stringify(source ?? [])) as ModelDefineRule[];
}

function resetDraft(): void {
  draft.id = "";
  draft.match_contains = "";
  draft.match_exact = "";
  draft.match_provider = "";
  draft.is_final = false;
  draft.brand = "";
  draft.icon = "";
  thinkingOn.value = false;
  levels.value = [];
}

async function load(): Promise<void> {
  editing.value = null;
  status.value = "";
  if (isProvider.value) {
    rules.value = cloneRules(props.rules);
    return;
  }
  busy.value = true;
  try {
    const result = await api.modelRules();
    rules.value = cloneRules(result.model_define_rules ?? []);
  } catch (e: unknown) {
    status.value = `读取规则失败：${e instanceof Error ? e.message : String(e)}`;
  }
  busy.value = false;
}

function openNew(): void {
  resetDraft();
  editing.value = "new";
  status.value = "";
}

function openEdit(index: number): void {
  const rule = rules.value[index];
  if (!rule) return;
  resetDraft();
  draft.id = rule.id ?? "";
  draft.match_contains = rule.match_contains ?? "";
  draft.match_exact = rule.match_exact ?? "";
  draft.match_provider = rule.match_provider ?? "";
  draft.is_final = rule.is_final === true;
  draft.brand = rule.brand ?? "";
  draft.icon = rule.icon ?? "";
  thinkingOn.value = Array.isArray(rule.thinking_levels);
  levels.value = [...(rule.thinking_levels ?? [])];
  editing.value = index;
  status.value = "";
}

function cancelEdit(): void {
  editing.value = null;
  status.value = "";
}

function buildRule(): ModelDefineRule | null {
  const matchContains = draft.match_contains.trim();
  const matchExact = draft.match_exact.trim();
  const matchProvider = draft.match_provider.trim();
  if (!matchContains && !matchExact && !matchProvider) {
    status.value = "至少填一个匹配条件（包含 / 精确 / 渠道）";
    return null;
  }
  const brand = draft.brand.trim();
  const icon = draft.icon.trim();
  if (!thinkingOn.value && !brand && !icon) {
    status.value = "至少填一个覆写字段（思考档位 / 品牌 / 图标）";
    return null;
  }
  const rule: ModelDefineRule = { is_final: draft.is_final };
  const id = draft.id.trim();
  if (id) rule.id = id;
  if (matchContains) rule.match_contains = matchContains;
  if (matchExact) rule.match_exact = matchExact;
  if (matchProvider) rule.match_provider = matchProvider;
  if (thinkingOn.value) rule.thinking_levels = [...levels.value];
  if (brand) rule.brand = brand;
  if (icon) rule.icon = icon;
  return rule;
}

function saveDraft(): void {
  const rule = buildRule();
  if (!rule) return;
  if (editing.value === "new") rules.value.push(rule);
  else if (typeof editing.value === "number") rules.value.splice(editing.value, 1, rule);
  editing.value = null;
  status.value = "已修改；点下面的保存按钮写回";
}

function deleteRule(index: number): void {
  rules.value.splice(index, 1);
  if (editing.value === index) editing.value = null;
  else if (typeof editing.value === "number" && editing.value > index) editing.value -= 1;
  status.value = "已删除；点下面的保存按钮写回";
}

function moveRule(index: number, delta: number): void {
  const target = index + delta;
  if (target < 0 || target >= rules.value.length) return;
  const copy = [...rules.value];
  [copy[index], copy[target]] = [copy[target], copy[index]];
  rules.value = copy;
  if (editing.value === index) editing.value = target;
  status.value = "已调整顺序；点下面的保存按钮写回";
}

async function saveAll(): Promise<void> {
  if (editing.value !== null) {
    status.value = "先完成正在编辑的规则";
    return;
  }
  status.value = "";
  if (isProvider.value) {
    emit("save", cloneRules(rules.value));
    status.value = "已应用到渠道";
    return;
  }
  busy.value = true;
  try {
    const result = await api.saveModelRules({ model_define_rules: cloneRules(rules.value) });
    rules.value = cloneRules(result.model_define_rules ?? []);
    status.value = "已保存";
    emit("saved");
  } catch (e: unknown) {
    status.value = `保存失败：${e instanceof Error ? e.message : String(e)}`;
  }
  busy.value = false;
}

function conditionText(rule: ModelDefineRule): string {
  const bits: string[] = [];
  if (rule.match_contains) bits.push(`包含 ${rule.match_contains}`);
  if (rule.match_exact) bits.push(`等于 ${rule.match_exact}`);
  if (rule.match_provider) bits.push(`渠道 ${rule.match_provider}`);
  return bits.join(" · ");
}

function defineText(rule: ModelDefineRule): string {
  const bits: string[] = [];
  if (Array.isArray(rule.thinking_levels)) {
    bits.push(rule.thinking_levels.length ? `思考 ${rule.thinking_levels.map(levelText).join("/")}` : "思考 清空");
  }
  if (rule.brand) bits.push(`品牌 ${rule.brand}`);
  if (rule.icon) bits.push(`图标 ${rule.icon}`);
  if (rule.is_final) bits.push("命中即停");
  return bits.join(" · ");
}

watch(() => props.rules, (next) => {
  // 渠道规则由父组件持有；保存后父组件会回写，编辑器跟着刷新。
  if (isProvider.value && editing.value === null) rules.value = cloneRules(next);
}, { deep: true });
onMounted(() => { load().catch(() => {}); });
</script>

<template>
  <section class="model-rules">
    <div class="rules-head">
      <div>
        <h3>{{ heading }}</h3>
        <p class="hint">{{ hint }}</p>
      </div>
      <VBtn color="primary" variant="flat" size="small" type="button" @click="openNew">新增规则</VBtn>
    </div>

    <div v-if="rules.length" class="rule-list">
      <div v-for="(rule, index) in rules" :key="`${index}-${rule.id ?? ''}`" class="rule-row">
        <div class="rule-index">{{ index + 1 }}</div>
        <div class="rule-body">
          <div class="rule-cond">{{ conditionText(rule) || "（没有匹配条件）" }}</div>
          <div class="rule-def">{{ defineText(rule) || "（没有覆写字段）" }}</div>
          <div v-if="rule.id" class="rule-id">id: {{ rule.id }}</div>
        </div>
        <div class="rule-ops">
          <VBtn variant="text" size="small" type="button" @click="moveRule(index, -1)">上移</VBtn>
          <VBtn variant="text" size="small" type="button" @click="moveRule(index, 1)">下移</VBtn>
          <VBtn variant="text" size="small" type="button" @click="openEdit(index)">编辑</VBtn>
          <VBtn color="error" variant="text" size="small" type="button" @click="deleteRule(index)">删除</VBtn>
        </div>
      </div>
    </div>
    <div v-else class="status">（还没有规则）</div>

    <div v-if="editing !== null" class="rule-editor">
      <h4>{{ editing === "new" ? "新增规则" : "编辑规则" }}</h4>
      <div class="rule-form">
        <label class="field"><span>规则 id（可选）</span><VTextField v-model="draft.id" density="compact" variant="outlined" hide-details placeholder="例如 xai-grok" /></label>
        <label class="field"><span>匹配：包含</span><VTextField v-model="draft.match_contains" density="compact" variant="outlined" hide-details placeholder="例如 grok" /></label>
        <label class="field"><span>匹配：精确</span><VTextField v-model="draft.match_exact" density="compact" variant="outlined" hide-details placeholder="例如 grok-4.7" /></label>
        <label class="field"><span>匹配：渠道（可选）</span><VTextField v-model="draft.match_provider" density="compact" variant="outlined" hide-details placeholder="例如 xai；留空 = 所有渠道" /></label>
        <label class="field"><span>品牌</span><VTextField v-model="draft.brand" density="compact" variant="outlined" hide-details placeholder="例如 xAI" /></label>
        <label class="field"><span>图标</span><VTextField v-model="draft.icon" density="compact" variant="outlined" hide-details placeholder="例如 xai / openai" /></label>
      </div>
      <div class="rule-switches">
        <VSwitch v-model="thinkingOn" density="compact" hide-details label="覆写思考档位" />
        <VSwitch v-model="draft.is_final" density="compact" hide-details label="命中后停止继续匹配" />
      </div>
      <label v-if="thinkingOn" class="field"><span>思考档位</span>
        <VSelect v-model="levels" :items="levelOptions" item-title="title" item-value="value" multiple density="compact" variant="outlined" hide-details />
      </label>
      <p v-if="thinkingOn" class="hint">一个都不选 = 明确清空（例如图像模型）。</p>
      <div class="rule-actions">
        <VBtn variant="text" size="small" type="button" @click="cancelEdit">取消</VBtn>
        <VBtn color="primary" variant="flat" size="small" type="button" @click="saveDraft">完成编辑</VBtn>
      </div>
    </div>

    <div class="row rules-save">
      <VBtn color="primary" variant="flat" size="small" type="button" :disabled="busy" @click="saveAll">{{ isProvider ? "应用到渠道" : "保存全局规则" }}</VBtn>
      <VBtn variant="text" size="small" type="button" :disabled="busy" @click="load">重新载入</VBtn>
      <span class="status">{{ status }}</span>
    </div>
  </section>
</template>

<style scoped>
.model-rules { margin-top: 1.2em; }
.rules-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 0.8em; }
.rules-head h3 { margin: 0; }
.rules-head .hint { margin: 0.25em 0 0; max-width: 62em; }
.rule-list { margin-top: 0.6em; border-top: 1px solid var(--line); }
.rule-row { display: grid; grid-template-columns: 2em minmax(0, 1fr) auto; gap: 0.6em; align-items: center; padding: 0.55em 0; border-bottom: 1px solid var(--line); }
.rule-index { color: var(--tg-hint); font-variant-numeric: tabular-nums; text-align: right; }
.rule-cond { color: var(--md-on-surface); }
.rule-def { color: var(--tg-hint); font-size: 0.9em; margin-top: 0.15em; }
.rule-id { color: var(--tg-hint); font-size: 0.8em; margin-top: 0.15em; }
.rule-ops { display: flex; align-items: center; gap: 0.1em; flex-wrap: wrap; justify-content: flex-end; }
.rule-editor { margin-top: 0.9em; padding: 0.8em 0.9em; border: 1px solid var(--line); border-radius: var(--r-card); background: var(--md-surface-container-low); }
.rule-editor h4 { margin: 0 0 0.6em; font-size: var(--md-type-title-small); font-weight: 500; }
.rule-form { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0.4em 0.8em; }
.rule-switches { display: flex; gap: 1.2em; flex-wrap: wrap; margin: 0.5em 0 0.2em; }
.rule-actions { display: flex; justify-content: flex-end; gap: 0.4em; margin-top: 0.6em; }
.rules-save { margin-top: 0.7em; }
@media (max-width: 700px) {
  .rule-row { grid-template-columns: 1.6em minmax(0, 1fr); }
  .rule-ops { grid-column: 1 / -1; justify-content: flex-start; }
  .rule-form { grid-template-columns: 1fr; }
}
</style>
