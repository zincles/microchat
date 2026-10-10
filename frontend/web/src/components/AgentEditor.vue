<script setup>
// Agent 编辑器（设置视图 Agent tab 内的二级页，列表 ⇄ 详情就地切换）：新建 / 改 id·名 / 提示词 / 三能力开关 / 设默认 / 删除。
// isNew（boolean）：无 agent 快照=新建（空表单）；有=编辑（带入快照，保存后父重拉，id 变了也对得上）。关即销毁。
import { ref, onMounted, onUnmounted } from "vue";
import { sharedApi } from "../utils/prefs.js";
import { ABILITY_IDS, buildAbilitiesPatch, modelKey, parseModelKey, modelLabel } from "../utils/format.js";
import ToggleSwitch from "./ToggleSwitch.vue";

const props = defineProps({
  agent: { type: Object, default: null },
  isDefault: { type: Boolean, default: false },
  template: { type: Object, default: null }, // 新建模板 {name, system_prompt, prepend_state, abilities}
  ask: { type: Function, default: null }, // 全局确认框（删除前先摊开）
});
const emit = defineEmits(["close", "saved"]);

const api = sharedApi();
const isNew = !props.agent;
const aid = ref(props.agent?.id ?? "");
const name = ref(props.agent?.name ?? props.template?.name ?? "");
const prompt = ref(props.agent?.system_prompt ?? props.template?.system_prompt ?? "");
const rows = ref({});
const prepend = ref(!!(props.agent?.prepend_state ?? props.template?.prepend_state));
const displayMode = ref(props.agent?.display_mode ?? props.template?.display_mode ?? "chat");
const modelOptions = ref([]);
const status = ref("");

function fillRows(a) {
  const ab = a?.abilities ?? {};
  const out = {};
  for (const id of ABILITY_IDS) {
    const one = ab[id] ?? {};
    const both = one.provider && one.model ? modelKey(one.provider, one.model) : "";
    out[id] = { enabled: one.enabled !== false, model: both, prompt: one.prompt ?? "" };
  }
  rows.value = out;
}
fillRows(props.agent ?? props.template);

function toPatch() {
  const out = {};
  for (const id of ABILITY_IDS) {
    const r = rows.value[id] ?? {};
    const parsed = parseModelKey(r.model ?? "");
    out[id] = {
      enabled: !!r.enabled,
      provider: parsed?.provider ?? "",
      model: parsed?.model ?? "",
      prompt: r.prompt ?? "",
    };
  }
  return buildAbilitiesPatch(out);
}

async function save() {
  if (!name.value.trim()) { status.value = "给个名字"; return; }
  try {
    if (isNew) {
      await api.createAgent({ name: name.value.trim(), system_prompt: prompt.value, abilities: toPatch(), prepend_state: prepend.value || undefined, display_mode: displayMode.value === "roleplay" ? "roleplay" : undefined });
    } else {
      const body = { system_prompt: prompt.value, abilities: toPatch() };
      if (!!prepend.value !== !!props.agent.prepend_state) body.prepend_state = prepend.value;
      if ((displayMode.value || "chat") !== (props.agent.display_mode || "chat")) body.display_mode = displayMode.value;
      if (name.value.trim() !== props.agent.name) body.name = name.value.trim();
      if (aid.value.trim() && aid.value.trim() !== props.agent.id) body.new_id = aid.value.trim();
      await api.patchAgent(props.agent.id, body);
    }
    emit("saved");
    emit("close");
  } catch (e) { status.value = `保存失败：${e.message}`; }
}

async function makeDefault() {
  if (isNew) return;
  try {
    await api.patchAgent(props.agent.id, { make_default: true });
    emit("saved");
    emit("close");
  } catch (e) { status.value = `设默认失败：${e.message}`; }
}

async function remove() {
  if (isNew) return;
  if (props.ask && !(await props.ask(`删除 Agent「${props.agent.name || props.agent.id}」？不可逆。`))) return;
  try {
    await api.deleteAgent(props.agent.id);
    emit("saved");
    emit("close");
  } catch (e) { status.value = `删除失败：${e.message}`; }
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
function onKey(e) {
  if (e.key === "Escape") emit("close");
}
</script>

<template>
  <!-- 二级页（2026-10-10 起不再是弹窗）：嵌在设置视图的 Agent tab 里，列表 ⇄ 详情就地切换。 -->
  <div class="detail-pane">
    <div class="panel-head">
      <button class="icon-btn" type="button" title="返回列表（Esc）" @click="$emit('close')">←</button>
      <span class="status">{{ isNew ? "新建 Agent" : `编辑「${props.agent.name}」` }}{{ !isNew && isDefault ? "（默认）" : "" }}</span>
    </div>
    <h3>基本</h3>
    <label class="field"><span>id（改即重命名，搬引用）</span><input v-model="aid" :disabled="isNew" placeholder="新建时由后端生成" /></label>
    <label class="field"><span>名称</span><input v-model="name" placeholder="人格名" /></label>
    <label class="field-block"><span>系统提示词</span><textarea v-model="prompt" rows="6" placeholder="system_prompt"></textarea></label>
    <label class="field"><span>句首贴当前状态（RP 类需要）</span><ToggleSwitch v-model="prepend" /></label>
    <label class="field"><span>显示格式</span><select v-model="displayMode">
      <option value="chat">聊天软件（双边气泡）</option>
      <option value="roleplay">RolePlay（用户气泡+AI 纯文本）</option>
    </select></label>
    <h3>能力（开关 + 模型/提示词覆盖，空=默认）</h3>
    <fieldset v-for="id in ABILITY_IDS" :key="id" class="ability-block">
      <legend><ToggleSwitch v-model="rows[id].enabled" /> {{ id }}</legend>
      <label class="field"><span>Model</span><select v-model="rows[id].model">
        <option value="">跟会话走</option>
        <option v-for="m in modelOptions" :key="m.value" :value="m.value">{{ m.label }}</option>
      </select></label>
      <label class="field-block"><span>提示词覆盖（空=默认模板）</span><textarea v-model="rows[id].prompt" rows="3"></textarea></label>
    </fieldset>
    <div class="row">
      <button type="button" class="primary" @click="save">保存</button>
      <button v-if="!isNew && !isDefault" type="button" @click="makeDefault">设为默认</button>
      <button v-if="!isNew" type="button" class="danger" @click="remove">删除</button>
      <span class="status">{{ status }}</span>
    </div>
  </div>
</template>
