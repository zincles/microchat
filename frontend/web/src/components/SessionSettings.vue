<script setup lang="ts">
// 会话设置弹窗：改名 / 换模型 / 换 Agent / 改会话提示词（PATCH /sessions/{id}，只发改过的格）。
// 由左栏条目的「选项」浮板打开，关丢弃（不保存不写）。创建副本 / 导出 / 删除
// 也都住在那块浮板里（2026-11-04 用户定：面板里只留设置本身）。
import { ref, computed, onMounted, onUnmounted } from "vue";
import { VBtn, VTextField, VSelect, VTextarea } from "vuetify/components";
import { sharedApi } from "../utils/prefs.ts";
import { groupModelsByProvider, modelKey, parseModelKey, modelOption, agentOption } from "../utils/format.ts";
import type { ModelChoice } from "../utils/format.ts";
import type { Session, UpdateSessionRequest } from "../api/types.ts";

const props = defineProps<{ sessionId: string }>();
const emit = defineEmits<{ close: []; renamed: [session: Session] }>();

const api = sharedApi();

const info = ref<Partial<Session>>({});
const title = ref("");
const modelValue = ref("");
const agentValue = ref("");
const sysPrompt = ref("");
const sysPromptPh = ref("");
const modelGroups = ref<Array<{ provider: string; items: ModelChoice[] }>>([]);
const agents = ref<Array<{ id: string; label: string }>>([]);
const status = ref("");
const modelChoices = computed(() => modelGroups.value.flatMap((g) => g.items.map((m) => ({
  label: `${g.provider} / ${m.label}`,
  value: m.value,
}))));
const agentChoices = computed(() => agents.value.map((a) => ({ label: a.label, value: a.id })));

async function load(): Promise<void> {
  const sessions = await api.listSessions();
  const found = sessions.find((s) => s.id === props.sessionId);
  info.value = found ?? {};
  title.value = found?.title ?? "";
  modelValue.value = found?.provider && found?.model ? modelKey(found.provider, found.model) : "";
  agentValue.value = found?.agent_id ?? "";
  sysPrompt.value = found?.system_prompt ?? "";
  sysPromptPh.value = found?.system_prompt ? "" : "留空 = 使用智能体的设置";
  const models = await api.models().catch(() => []);
  modelGroups.value = groupModelsByProvider(models).map((g) => ({
    provider: g.provider,
    items: g.items.map((m) => modelOption(m)),
  }));
  const data = await api.agents().catch(() => ({ default_agent: "", agents: [] }));
  agents.value = (data.agents ?? []).map((a) => agentOption(a));
}

async function save(): Promise<void> {
  const body: UpdateSessionRequest = {};
  if (title.value !== (info.value.title ?? "")) body.title = title.value;
  if (modelValue.value) {
    const parsed = parseModelKey(modelValue.value);
    if (parsed) {
      if (parsed.provider !== info.value.provider) body.provider = parsed.provider;
      if (parsed.model !== info.value.model) body.model = parsed.model;
    }
  }
  if (agentValue.value && agentValue.value !== info.value.agent_id) body.agent_id = agentValue.value;
  if (sysPrompt.value !== (info.value.system_prompt ?? "")) body.system_prompt = sysPrompt.value;
  if (!Object.keys(body).length) { status.value = "没改动"; return; }
  try {
    info.value = await api.patchSession(props.sessionId, body);
    status.value = "已保存";
    emit("renamed", info.value as Session);
  } catch (e: unknown) { status.value = `保存失败：${e instanceof Error ? e.message : String(e)}`; }
}

onMounted(() => {
  load().catch((e: unknown) => { status.value = `载入失败：${e instanceof Error ? e.message : String(e)}`; });
  document.addEventListener("keydown", onKey);
});
onUnmounted(() => document.removeEventListener("keydown", onKey));
function onKey(e: KeyboardEvent): void {
  if (e.key === "Escape") emit("close");
}
</script>

<template>
  <div class="modal" @click.self="emit('close')">
    <div class="modal-box compact">
      <div class="modal-head">
        <span>会话设置</span>
        <span class="head-btns">
          <VBtn icon variant="text" size="small" type="button" title="关闭（不保存不写）" @click="emit('close')">×</VBtn>
        </span>
      </div>
      <div class="settings-cols">
        <section class="panel">
          <h3>名称</h3>
          <label class="field"><span>标题（留空 = 自动起名）</span><VTextField v-model="title" placeholder="留空 = 自动起名" density="compact" variant="outlined" hide-details /></label>
          <h3>模型</h3>
          <label class="field"><span>渠道和模型</span><VSelect v-model="modelValue" :items="modelChoices" item-title="label" item-value="value" density="compact" variant="outlined" hide-details /></label>
          <label class="field"><span>智能体</span><VSelect v-model="agentValue" :items="agentChoices" item-title="label" item-value="value" density="compact" variant="outlined" hide-details /></label>
          <h3>自定义提示词</h3>
          <label class="field-block"><span>这条会话的系统提示词（留空 = 使用智能体的设置）</span><VTextarea v-model="sysPrompt" :placeholder="sysPromptPh" rows="4" density="compact" variant="outlined" hide-details /></label>
          <div class="row">
            <VBtn color="primary" variant="flat" size="small" type="button" @click="save">保存</VBtn>
            <span class="status">{{ status }}</span>
          </div>
        </section>
      </div>
    </div>
  </div>
</template>
