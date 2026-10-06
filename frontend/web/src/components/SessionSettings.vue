<script setup>
// 会话设置弹窗：改名 / 换模型 / 换 Agent / 改会话提示词（PATCH /sessions/{id}，只发改过的格）。
// 由左栏条目 ☰ 打开，关丢弃（不保存不写）。确认删除走同一套 confirm（全屏中央）。
import { ref, onMounted, onUnmounted } from "vue";
import { createApi } from "../api/client.js";
import { groupModelsByProvider } from "../utils/format.js";

const props = defineProps({ sessionId: String });
const emit = defineEmits(["close", "notify", "renamed", "deleted"]);

const API = localStorage.getItem("mc_api") || "http://127.0.0.1:8787/api/v1";
const TOKEN = localStorage.getItem("mc_token") || "";
const api = createApi({ base: API, token: TOKEN });

const info = ref({});
const title = ref("");
const modelValue = ref("");
const agentValue = ref("");
const sysPrompt = ref("");
const sysPromptPh = ref("");
const modelGroups = ref([]);
const agents = ref([]);
const status = ref("");

async function load() {
  const sessions = await api.listSessions();
  const found = sessions.find((s) => s.id === props.sessionId) ?? {};
  info.value = found;
  title.value = found.title ?? "";
  modelValue.value = found.provider && found.model ? `${found.provider}|||${found.model}` : "";
  agentValue.value = found.agent_id ?? "";
  sysPrompt.value = found.system_prompt ?? "";
  sysPromptPh.value = found.system_prompt ? "" : "空=跟 Agent 走";
  const models = await api.models().catch(() => []);
  modelGroups.value = groupModelsByProvider(models).map((g) => ({
    provider: g.provider,
    items: g.items.map((m) => ({ value: `${g.provider}|||${m.upstream_id}`, label: m.name ?? m.upstream_id })),
  }));
  const data = await api.agents().catch(() => ({ agents: [] }));
  agents.value = (data.agents ?? []).map((a) => ({ id: a.id, label: a.name }));
}

async function save() {
  const body = {};
  if (title.value !== (info.value.title ?? "")) body.title = title.value;
  if (modelValue.value) {
    const sep = modelValue.value.indexOf("|||");
    const p = modelValue.value.slice(0, sep), m = modelValue.value.slice(sep + 3);
    if (p !== info.value.provider) body.provider = p;
    if (m !== info.value.model) body.model = m;
  }
  if (agentValue.value && agentValue.value !== info.value.agent_id) body.agent_id = agentValue.value;
  if (sysPrompt.value !== (info.value.system_prompt ?? "")) body.system_prompt = sysPrompt.value;
  if (!Object.keys(body).length) { status.value = "没改动"; return; }
  try {
    info.value = await api.patchSession(props.sessionId, body);
    status.value = "已保存";
    emit("renamed", info.value);
  } catch (e) { status.value = `保存失败：${e.message}`; }
}

onMounted(() => {
  load().catch((e) => { status.value = `载入失败：${e.message}`; });
  document.addEventListener("keydown", onKey);
});
onUnmounted(() => document.removeEventListener("keydown", onKey));
function onKey(e) {
  if (e.key === "Escape") emit("close");
}
</script>

<template>
  <div class="modal" @click.self="$emit('close')">
    <div class="modal-box settings-box">
      <div class="modal-head">
        <span>会话设置</span>
        <span class="head-btns">
          <button class="icon-btn" type="button" title="关闭（不保存不写）" @click="$emit('close')">
            ×
          </button>
        </span>
      </div>
      <div class="settings-cols">
        <section class="panel">
          <h3>名称</h3>
          <label class="field"><span>标题（空=还没起名）</span><input v-model="title" placeholder="空=还没起名" /></label>
          <h3>模型</h3>
          <label class="field"><span>渠道 / 模型</span><select v-model="modelValue">
            <optgroup v-for="g in modelGroups" :key="g.provider" :label="g.provider">
              <option v-for="m in g.items" :key="m.value" :value="m.value">{{ m.label }}</option>
            </optgroup>
          </select></label>
          <label class="field"><span>Agent</span><select v-model="agentValue">
            <option v-for="a in agents" :key="a.id" :value="a.id">{{ a.label }}</option>
          </select></label>
          <h3>提示词覆盖</h3>
          <label class="field-block"><span>会话提示词（空=跟 Agent 走）</span><textarea v-model="sysPrompt" :placeholder="sysPromptPh" rows="4"></textarea></label>
          <div class="row"><button type="button" class="primary" @click="save">保存</button><span class="status">{{ status }}</span></div>
        </section>
      </div>
    </div>
  </div>
</template>
