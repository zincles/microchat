<script setup lang="ts">
// 单个模型的用户列编辑（显示名 / 上下文覆盖 / 采样参数）。
//
// 这不是 model_define_rules（家族默认），而是精确到 (provider, upstream_id) 的覆盖；
// 后端 PATCH /models 早就有，前端从前没有入口。
import { computed, ref } from "vue";
import { VBtn, VTextField, VTextarea } from "vuetify/components";
import { sharedApi } from "../utils/prefs.ts";
import { formatTokens } from "../utils/format.ts";
import ModelIcon from "./ModelIcon.vue";
import ModalShell from "./ModalShell.vue";
import type { ModelView } from "../api/types.ts";

const props = defineProps<{ provider: string; model: ModelView }>();
const emit = defineEmits<{ close: []; saved: [model: ModelView] }>();

const api = sharedApi();
const displayName = ref(props.model.display_name ?? "");
const contextOverride = ref(props.model.context_override == null ? "" : String(props.model.context_override));
const paramsText = ref(JSON.stringify(props.model.params ?? {}, null, 2));
const status = ref("");
const busy = ref(false);
const title = computed(() => props.model.name || props.model.upstream_id);

function close(): void { emit("close"); }
function contextLabel(): string {
  if (props.model.context_length == null) return "上游没报上下文长度";
  return `上游上下文：${formatTokens(props.model.context_length)}`;
}

async function save(): Promise<void> {
  status.value = "";
  const rawContext = contextOverride.value.trim();
  let context: number | null = null;
  if (rawContext) {
    const value = Number(rawContext);
    if (!Number.isInteger(value) || value <= 0) {
      status.value = "上下文覆盖必须是正整数，或留空";
      return;
    }
    context = value;
  }
  let params: unknown;
  try {
    params = JSON.parse(paramsText.value.trim() || "{}");
  } catch (e: unknown) {
    status.value = `参数不是合法 JSON：${e instanceof Error ? e.message : String(e)}`;
    return;
  }
  if (params === null || typeof params !== "object" || Array.isArray(params)) {
    status.value = "参数必须是 JSON 对象";
    return;
  }
  busy.value = true;
  try {
    const updated = await api.setModel({
      provider: props.provider,
      upstream_id: props.model.upstream_id,
      display_name: displayName.value.trim() || null,
      context_override: context,
      params,
    });
    emit("saved", updated);
  } catch (e: unknown) {
    status.value = `保存失败：${e instanceof Error ? e.message : String(e)}`;
  }
  busy.value = false;
}
</script>

<template>
  <ModalShell :width="600" :max-width="600" @close="close">
    <template #title>
      <span class="model-edit-title"><ModelIcon :icon="model.icon" :brand="model.brand" />{{ title }}</span>
    </template>
    <p class="hint model-edit-id">{{ model.upstream_id }} · {{ provider }}</p>
    <label class="field"><span>显示名</span><VTextField v-model="displayName" density="compact" variant="outlined" hide-details placeholder="留空 = 用上游名 / 自动美化" /></label>
    <label class="field"><span>上下文覆盖</span><VTextField v-model="contextOverride" type="number" min="1" density="compact" variant="outlined" hide-details placeholder="留空 = 跟随上游" /></label>
    <p class="hint">{{ contextLabel() }}；这里填了就以这里为准。</p>
    <label class="field"><span>采样参数（JSON）</span>
      <VTextarea v-model="paramsText" rows="8" variant="outlined" hide-details spellcheck="false" />
    </label>
    <p class="hint">模型刷新不会覆盖这三格；但如果这个模型从上游消失，整行（连带覆盖）会被删掉。</p>
    <template #actions>
      <span class="status">{{ status }}</span>
      <VBtn variant="text" size="small" @click="close">取消</VBtn>
      <VBtn color="primary" variant="flat" size="small" :disabled="busy" @click="save">保存</VBtn>
    </template>
  </ModalShell>
</template>

<style scoped>
.model-edit-title { display: inline-flex; align-items: center; gap: 0.45em; min-width: 0; }
.model-edit-id { margin: -0.15em 0 0.9em; word-break: break-all; }
</style>
