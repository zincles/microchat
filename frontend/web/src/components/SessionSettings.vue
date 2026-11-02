<script setup>
// 会话设置弹窗：改名 / 换模型 / 换 Agent / 改会话提示词（PATCH /sessions/{id}，只发改过的格）。
// 由左栏条目 ☰ 打开，关丢弃（不保存不写）。确认删除走同一套 confirm（全屏中央）。
import { ref, onMounted, onUnmounted } from "vue";
import { sharedApi } from "../utils/prefs.js";
import { groupModelsByProvider, modelKey, parseModelKey, modelOption, agentOption } from "../utils/format.js";
import { downloadJSON } from "../utils/download.js";

const props = defineProps({ sessionId: String });
const emit = defineEmits(["close", "renamed", "copy"]);

const api = sharedApi();

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
  modelValue.value = found.provider && found.model ? modelKey(found.provider, found.model) : "";
  agentValue.value = found.agent_id ?? "";
  sysPrompt.value = found.system_prompt ?? "";
  sysPromptPh.value = found.system_prompt ? "" : "留空 = 使用智能体的设置";
  const models = await api.models().catch(() => []);
  modelGroups.value = groupModelsByProvider(models).map((g) => ({
    provider: g.provider,
    items: g.items.map((m) => modelOption(m)),
  }));
  const data = await api.agents().catch(() => ({ agents: [] }));
  agents.value = (data.agents ?? []).map((a) => agentOption(a));
}

async function save() {
  const body = {};
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
    emit("renamed", info.value);
  } catch (e) { status.value = `保存失败：${e.message}`; }
}

// 导出对话（JSON，2026-10-11 用户定；2026-10-28 已到 v3）：角色/正文/思考 + 整张摘要金字塔
// + 这条会话绑的渠道/模型/Agent —— 浏览器直接落一个文件。
async function exportJSON() {
  try {
    const doc = await api.exportSession(props.sessionId);
    downloadJSON(doc, `${info.value.title || "新会话"}.json`);
    status.value = `已导出 ${doc.messages.length} 条`;
  } catch (e) { status.value = `导出失败：${e.message}`; }
}

// 创建副本（2026-11-01 用户定：「接下来给前端补上复制对话的办法，在 对话 的配置界面里允许创建副本」）。
//
// 为什么这个按钮值得有：后端 `POST /sessions/{id}/copy` **一直**是完整实现的
// （新会话 + 消息与摘要一并复制、摘要铸新 id、指针重映射、世界状态不复制），
// 但 web 界面从来没给过入口 —— 于是"线性会话里想分岔"只能靠命令行。
//
// 复制完**进新会话**（emit copy）：副本的意义就是接着在它上面往下写；
// 停在原会话里会让人以为"点了没反应"（复制出来的那条在左栏最上面，但视线在弹窗里）。
async function copySession() {
  status.value = "复制中…";
  try {
    const fresh = await api.copySession(props.sessionId);
    emit("copy", fresh);
  } catch (e) { status.value = `复制失败：${e.message}`; }
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
    <div class="modal-box compact">
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
          <label class="field"><span>标题（留空 = 自动起名）</span><input v-model="title" placeholder="留空 = 自动起名" /></label>
          <h3>模型</h3>
          <label class="field"><span>渠道和模型</span><select v-model="modelValue">
            <optgroup v-for="g in modelGroups" :key="g.provider" :label="g.provider">
              <option v-for="m in g.items" :key="m.value" :value="m.value">{{ m.label }}</option>
            </optgroup>
          </select></label>
          <label class="field"><span>智能体</span><select v-model="agentValue">
            <option v-for="a in agents" :key="a.id" :value="a.id">{{ a.label }}</option>
          </select></label>
          <h3>自定义提示词</h3>
          <label class="field-block"><span>这条会话的系统提示词（留空 = 使用智能体的设置）</span><textarea v-model="sysPrompt" :placeholder="sysPromptPh" rows="4"></textarea></label>
          <div class="row">
            <button type="button" class="primary" @click="save">保存</button>
            <button type="button" @click="exportJSON" title="导出这条对话（含全部消息、思考过程和摘要，以及使用的渠道、模型和智能体）">导出对话（JSON）</button>
            <span class="status">{{ status }}</span>
          </div>
          <h3>创建副本</h3>
          <p class="hint">
            把这条对话<b>整份复制</b>成一条新会话（消息、思考过程和已有的摘要都带上；对话里的状态不复制 —— 它会由正文重新算出来）。
            复制完直接进入副本，可以接着往下写。想从某条对话分出另一条线，就用它。
          </p>
          <div class="row">
            <button type="button" @click="copySession" title="复制成一条新会话（副本）">创建副本</button>
          </div>
        </section>
      </div>
    </div>
  </div>
</template>
