<script setup lang="ts">
// 会话设置弹窗：改名 / 换模型 / 换 Agent / 改会话提示词（PATCH /sessions/{id}，只发改过的格）。
// 由左栏条目的「选项」浮板打开，关丢弃（不保存不写）。创建副本 / 导出 / 删除
// 也都住在那块浮板里（2026-11-04 用户定：面板里只留设置本身）。
import { ref, computed, watch } from "vue";
import { VBtn, VTextField, VSelect, VTextarea } from "vuetify/components";
import { sharedApi } from "../utils/prefs.ts";
import { groupModelsByProvider, modelKey, parseModelKey, modelOption, agentOption } from "../utils/format.ts";
import ModalShell from "./ModalShell.vue";
import RequestLoading from "./RequestLoading.vue";
import type { ModelChoice } from "../utils/format.ts";
import type { Session, UpdateSessionRequest } from "../api/types.ts";

const props = defineProps<{ sessionId: string }>();
const emit = defineEmits<{ close: []; renamed: [session: Session] }>();

const api = sharedApi();

const info = ref<Session | null>(null);
const title = ref("");
const modelValue = ref("");
const agentValue = ref("");
const sysPrompt = ref("");
const sysPromptPh = ref("");
const modelGroups = ref<Array<{ provider: string; items: ModelChoice[] }>>([]);
const agents = ref<Array<{ id: string; label: string; kind?: string }>>([]);
const status = ref("");
const loading = ref(true);
const loadError = ref("");
const saving = ref(false);
let loadSeq = 0;
const modelChoices = computed(() => modelGroups.value.flatMap((g) => g.items.map((m) => ({
  label: `${g.provider} / ${m.label}`,
  value: m.value,
}))));
const agentChoices = computed(() => agents.value
  .map((a) => ({ label: a.label, value: a.id })));

async function load(): Promise<void> {
  const sid = props.sessionId;
  const current = ++loadSeq;
  loading.value = true;
  saving.value = false;
  loadError.value = "";
  status.value = "";
  info.value = null;
  try {
    const [sessions, models, data] = await Promise.all([api.listSessions(), api.models(), api.agents()]);
    if (current !== loadSeq || sid !== props.sessionId) return;
    const found = sessions.find((s) => s.id === sid);
    if (!found) throw new Error("找不到这条会话");
    title.value = found.title;
    modelValue.value = found.provider && found.model ? modelKey(found.provider, found.model) : "";
    agentValue.value = found.agent_id ?? "";
    sysPrompt.value = found.system_prompt ?? "";
    sysPromptPh.value = found.system_prompt ? "" : "留空 = 使用智能体的设置";
    modelGroups.value = groupModelsByProvider(models).map((g) => ({ provider: g.provider, items: g.items.map((m) => modelOption(m)) }));
    agents.value = (data.agents ?? []).map((a) => ({ ...agentOption(a), kind: a.kind }));
    info.value = found;
  } catch (e: unknown) {
    if (current === loadSeq) loadError.value = `载入失败：${e instanceof Error ? e.message : String(e)}`;
  } finally {
    if (current === loadSeq) loading.value = false;
  }
}

async function save(): Promise<void> {
  const previous = info.value;
  if (!previous || previous.id !== props.sessionId || loading.value || saving.value) return;
  const current = loadSeq;
  const body: UpdateSessionRequest = {};
  if (title.value !== (previous.title ?? "")) body.title = title.value;
  if (modelValue.value) {
    const parsed = parseModelKey(modelValue.value);
    if (parsed) {
      if (parsed.provider !== previous.provider) body.provider = parsed.provider;
      if (parsed.model !== previous.model) body.model = parsed.model;
    }
  }
  if (agentValue.value && agentValue.value !== previous.agent_id) body.agent_id = agentValue.value;
  if (sysPrompt.value !== (previous.system_prompt ?? "")) body.system_prompt = sysPrompt.value;
  if (!Object.keys(body).length) { status.value = "没改动"; return; }
  saving.value = true;
  status.value = "";
  try {
    const saved = await api.patchSession(previous.id, body);
    if (current !== loadSeq) return;
    info.value = saved;
    status.value = "已保存";
    emit("renamed", saved);
  } catch (e: unknown) {
    if (current === loadSeq) status.value = `保存失败：${e instanceof Error ? e.message : String(e)}`;
  } finally {
    if (current === loadSeq) saving.value = false;
  }
}

watch(() => props.sessionId, load, { immediate: true });
</script>

<template>
  <ModalShell title="会话设置" @close="emit('close')">
    <RequestLoading v-if="loading" label="正在加载会话设置…" />
    <div v-else-if="loadError">
      <p class="status" role="alert">{{ loadError }}</p>
      <VBtn variant="text" color="primary" size="small" type="button" @click="load">重试</VBtn>
    </div>
    <div v-else class="settings-cols">
      <section class="panel">
        <h3>名称</h3>
        <label class="field"><span>标题（留空 = 自动起名）</span><VTextField v-model="title" placeholder="留空 = 自动起名" density="compact" variant="outlined" hide-details /></label>
        <h3>模型</h3>
        <label class="field"><span>渠道和模型</span><VSelect v-model="modelValue" :items="modelChoices" item-title="label" item-value="value" density="compact" variant="outlined" hide-details /></label>
        <label class="field"><span>智能体</span><VSelect v-model="agentValue" :items="agentChoices" item-title="label" item-value="value" density="compact" variant="outlined" hide-details /></label>
        <h3>自定义提示词</h3>
        <label class="field-block"><span>这条会话的系统提示词（留空 = 使用智能体的设置）</span><VTextarea v-model="sysPrompt" :placeholder="sysPromptPh" rows="4" density="compact" variant="outlined" hide-details /></label>
        <div class="status" role="status">{{ status }}</div>
      </section>
    </div>
    <template #actions>
      <VBtn color="primary" variant="flat" size="small" type="button" :loading="saving" :disabled="!info || saving" @click="save">保存</VBtn>
    </template>
  </ModalShell>
</template>
