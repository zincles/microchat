<script setup lang="ts">
// 模型定义规则：全局的纯数据 override 集合。
//
// 这里故意先用 JSON 编辑器（规则是高级配置，字段还会长）；后端的
// `PUT /config/model-rules` 会先校验再落盘，所以错误不会写坏配置。
import { computed, onMounted, ref, watch } from "vue";
import { VBtn } from "vuetify/components";
import { sharedApi } from "../utils/prefs.ts";
import { THINKING_LEVELS } from "../utils/thinking.ts";
import type { ModelDefineRule } from "../api/types.ts";

const props = withDefaults(defineProps<{
  // global：读写 config.model_define_rules；provider：当前渠道自己的覆盖列表（父组件负责保存）。
  scope?: "global" | "provider";
  rules?: ModelDefineRule[];
}>(), { scope: "global", rules: () => [] });

const api = sharedApi();
const text = ref("[]");
const status = ref("");
const busy = ref(false);
const isProvider = computed(() => props.scope === "provider");
const heading = computed(() => isProvider.value ? "当前渠道的覆盖规则" : "全局模型定义规则");
const hint = computed(() => isProvider.value
  ? "只作用于当前渠道；应用在全局规则之后，同名字段会覆盖全局。保存渠道后生效。"
  : "所有渠道共享的默认规则；按数组顺序匹配原始模型 id，命中的字段覆盖前面的。");

function formatRules(rules: unknown[]): string {
  return JSON.stringify(rules, null, 2);
}

async function load(): Promise<void> {
  if (isProvider.value) {
    text.value = formatRules(props.rules ?? []);
    status.value = "";
    return;
  }
  busy.value = true;
  status.value = "";
  try {
    const result = await api.modelRules();
    text.value = formatRules(result.model_define_rules ?? []);
  } catch (e: unknown) {
    status.value = `读取规则失败：${e instanceof Error ? e.message : String(e)}`;
  }
  busy.value = false;
}

async function save(): Promise<void> {
  status.value = "";
  let parsed: unknown;
  try {
    parsed = JSON.parse(text.value.trim() || "[]");
  } catch (e: unknown) {
    status.value = `不是合法 JSON：${e instanceof Error ? e.message : String(e)}`;
    return;
  }
  if (!Array.isArray(parsed)) {
    status.value = "规则必须是数组";
    return;
  }
  if (isProvider.value) {
    emit("save", parsed as ModelDefineRule[]);
    status.value = "已应用到渠道（保存渠道后生效）";
    return;
  }
  busy.value = true;
  try {
    const result = await api.saveModelRules({ model_define_rules: parsed as ModelDefineRule[] });
    text.value = formatRules(result.model_define_rules ?? []);
    status.value = "已保存";
    emit("saved");
  } catch (e: unknown) {
    status.value = `保存失败：${e instanceof Error ? e.message : String(e)}`;
  }
  busy.value = false;
}

const emit = defineEmits<{ save: [rules: ModelDefineRule[]]; saved: [] }>();
watch(() => props.rules, (rules) => {
  if (isProvider.value) text.value = formatRules(rules ?? []);
}, { deep: true });
onMounted(() => { load().catch(() => {}); });
</script>

<template>
  <section class="model-rules">
    <h3>{{ heading }}</h3>
    <p class="hint">
      {{ hint }} 只写 <code>brand</code> / <code>icon</code> / <code>thinking_levels</code>，
      不执行代码，也不改请求协议。思考档位只能是
      <code>{{ THINKING_LEVELS.join(" / ") }}</code>。
    </p>
    <textarea v-model="text" class="rules-json" rows="16" spellcheck="false" aria-label="模型定义规则 JSON"></textarea>
    <div class="row">
      <VBtn color="primary" variant="flat" size="small" type="button" :disabled="busy" @click="save">{{ isProvider ? "应用到渠道" : "保存全局规则" }}</VBtn>
      <VBtn variant="text" size="small" type="button" :disabled="busy" @click="load">重新载入</VBtn>
      <span class="status">{{ status }}</span>
    </div>
  </section>
</template>

<style scoped>
.rules-json {
  width: 100%;
  box-sizing: border-box;
  min-height: 16em;
  resize: vertical;
  font: inherit;
  font-family: var(--app-font-mono, ui-monospace, monospace);
  font-size: 0.82em;
  line-height: 1.45;
  padding: 0.65em 0.75em;
  border: 1px solid var(--md-outline);
  border-radius: var(--md-shape-xs);
  background: var(--md-surface-container-high);
  color: var(--md-on-surface);
}
.rules-json:focus { outline: 2px solid var(--md-primary); outline-offset: -1px; }
</style>
