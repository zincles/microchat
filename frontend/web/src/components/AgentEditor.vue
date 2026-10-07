<script setup>
// Agent 编辑器（二级弹窗，盖在设置窗口之上）：新建 / 改 id·名 / 提示词 / 三能力开关 / 设默认 / 删除。
// mode: "create"（空表单）/ "edit"（带入 agent 快照，保存后父重拉，id 变了也对得上）。关即销毁。
import { ref, onMounted, onUnmounted } from "vue";
import { createApi } from "../api/client.js";
import { ABILITY_IDS, buildAbilitiesPatch } from "../utils/format.js";
import ToggleSwitch from "./ToggleSwitch.vue";

const props = defineProps({
  agent: { type: Object, default: null },
  isDefault: { type: Boolean, default: false },
  template: { type: Object, default: null }, // 新建模板 {name, system_prompt, prepend_state, abilities}
});
const emit = defineEmits(["close", "saved"]);

const API = localStorage.getItem("mc_api") || "http://127.0.0.1:8787/api/v1";
const TOKEN = localStorage.getItem("mc_token") || "";
const api = createApi({ base: API, token: TOKEN });
const isNew = !props.agent;
const aid = ref(props.agent?.id ?? "");
const name = ref(props.agent?.name ?? props.template?.name ?? "");
const prompt = ref(props.agent?.system_prompt ?? props.template?.system_prompt ?? "");
const rows = ref({});
const prepend = ref(!!(props.agent?.prepend_state ?? props.template?.prepend_state));
const modelOptions = ref([]);
const status = ref("");

function fillRows(a) {
  const ab = a?.abilities ?? {};
  const out = {};
  for (const id of ABILITY_IDS) {
    const one = ab[id] ?? {};
    const both = one.provider && one.model ? `${one.provider}/${one.model}` : "";
    out[id] = { enabled: one.enabled !== false, model: both, prompt: one.prompt ?? "" };
  }
  rows.value = out;
}
fillRows(props.agent ?? props.template);

function toPatch() {
  const out = {};
  for (const id of ABILITY_IDS) {
    const r = rows.value[id] ?? {};
    const slash = (r.model ?? "").indexOf("/");
    out[id] = {
      enabled: !!r.enabled,
      provider: slash < 0 ? "" : r.model.slice(0, slash),
      model: slash < 0 ? "" : r.model.slice(slash + 1),
      prompt: r.prompt ?? "",
    };
  }
  return buildAbilitiesPatch(out);
}

async function save() {
  if (!name.value.trim()) { status.value = "给个名字"; return; }
  try {
    if (isNew) {
      await api.createAgent({ name: name.value.trim(), system_prompt: prompt.value, abilities: toPatch(), prepend_state: prepend.value || undefined });
    } else {
      const body = { system_prompt: prompt.value, abilities: toPatch() };
      if (!!prepend.value !== !!props.agent.prepend_state) body.prepend_state = prepend.value;
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
      value: `${m.provider}/${m.upstream_id}`,
      label: `${m.provider}/${m.name ?? m.upstream_id}`,
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
  <div class="modal submodal" @click.self="$emit('close')">
    <div class="modal-box">
      <div class="modal-head">
        <span>{{ isNew ? "新建 Agent" : `编辑 ${props.agent.name}` }}{{ !isNew && isDefault ? "（默认）" : "" }}</span>
        <span class="head-btns">
          <button class="icon-btn" type="button" title="关闭（不保存不写）" @click="$emit('close')">
            ×
          </button>
        </span>
      </div>
      <div class="settings-cols">
        <section class="panel">
          <h3>基本</h3>
          <label class="field"><span>id（改即重命名，搬引用）</span><input v-model="aid" :disabled="isNew" placeholder="新建时由后端生成" /></label>
          <label class="field"><span>名称</span><input v-model="name" placeholder="人格名" /></label>
          <label class="field-block"><span>系统提示词</span><textarea v-model="prompt" rows="6" placeholder="system_prompt"></textarea></label>
          <label class="field"><span>句首贴当前状态（RP 类需要）</span><ToggleSwitch v-model="prepend" /></label>
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
        </section>
      </div>
    </div>
  </div>
</template>
