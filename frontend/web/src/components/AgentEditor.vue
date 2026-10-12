<script setup>
// Agent 编辑器（设置视图 Agent tab 内的二级页，列表 ⇄ 详情就地切换）：新建 / 改 id·名 / 提示词 / 三能力开关 / 设默认 / 删除。
// isNew（boolean）：无 agent 快照=新建（空表单）；有=编辑（带入快照，保存后父重拉，id 变了也对得上）。关即销毁。
import { ref, computed, onMounted, onUnmounted } from "vue";
import { sharedApi } from "../utils/prefs.js";
import { ABILITY_IDS, buildAbilitiesPatch, modelKey, parseModelKey, modelLabel } from "../utils/format.js";
import { assemblePrompt, missingSegments } from "../utils/prompt.js";
import { buildCartridge } from "../utils/cartridge.js";
import { downloadJSON } from "../utils/download.js";
import ToggleSwitch from "./ToggleSwitch.vue";

const props = defineProps({
  agent: { type: Object, default: null },
  isDefault: { type: Boolean, default: false },
  template: { type: Object, default: null }, // 新建模板 {name, system_prompt, prepend_state, abilities}
  segments: { type: Array, default: () => [] }, // 段库（picker 用；随 GET /agents 一起给）
  ask: { type: Function, default: null }, // 全局确认框（删除前先摊开）
});
const emit = defineEmits(["close", "saved"]);

const api = sharedApi();
const isNew = !props.agent;
const aid = ref(props.agent?.id ?? "");
const name = ref(props.agent?.name ?? props.template?.name ?? "");
const prompt = ref(props.agent?.system_prompt ?? props.template?.system_prompt ?? "");
const firstMsg = ref(props.agent?.first_message ?? props.template?.first_message ?? "");
const rows = ref({});
const prepend = ref(!!(props.agent?.prepend_state ?? props.template?.prepend_state));
const displayMode = ref(props.agent?.display_mode ?? props.template?.display_mode ?? "chat");
// 追加段（顺序 = 拼接顺序）。段库在 `props.segments`；拼装预览走纯函数（与后端同规则）。
const segs = ref([...(props.agent?.segments ?? props.template?.segments ?? [])]);
const segPick = ref("");
const segText = (id) => props.segments.find((s) => s.id === id)?.text ?? "";
const segMissing = (id) => !props.segments.some((s) => s.id === id);
const assembled = computed(() => assemblePrompt(prompt.value, segs.value, props.segments));
const missing = computed(() => missingSegments(segs.value, props.segments));
function addSeg() {
  if (segPick.value && !segs.value.includes(segPick.value)) segs.value.push(segPick.value);
  segPick.value = "";
}
function moveSeg(i, d) {
  const j = i + d;
  if (j < 0 || j >= segs.value.length) return;
  const list = segs.value;
  [list[i], list[j]] = [list[j], list[i]];
}
function dropSeg(i) { segs.value.splice(i, 1); }
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
      await api.createAgent({ name: name.value.trim(), system_prompt: prompt.value, first_message: firstMsg.value, segments: segs.value, abilities: toPatch(), prepend_state: prepend.value || undefined, display_mode: displayMode.value === "roleplay" ? "roleplay" : undefined });
    } else {
      const body = { system_prompt: prompt.value, first_message: firstMsg.value, segments: segs.value, abilities: toPatch() };
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

// 导出卡带（2026-10-11 用户定）：**只导 agent 自己** —— 用到的段**拼平**进 system_prompt、
// 不留引用（免除依赖，方便再分发；导入后要不要拆段由人后续处理）。
function exportCartridge() {
  const doc = buildCartridge({
    name: name.value.trim() || "未命名",
    system_prompt: prompt.value,
    segments: segs.value,
    first_message: firstMsg.value,
    abilities: toPatch(),
    prepend_state: prepend.value,
    display_mode: displayMode.value,
  }, props.segments);
  downloadJSON(doc, `${name.value.trim() || "agent"}.card.json`);
  status.value = "已导出卡带（段已拼平）";
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
    <label class="field-block"><span>系统提示词（base）</span><textarea v-model="prompt" rows="6" placeholder="system_prompt"></textarea></label>
    <label class="field-block"><span>首段对话（新会话的第一条 assistant 消息；空 = 不启用）</span><textarea v-model="firstMsg" rows="3" placeholder="例如：你好，我是……（可带 &lt;state&gt; 块设初始状态）"></textarea></label>
    <h3>提示词段（追加在 base 后面，顺序 = 拼接顺序）</h3>
    <div v-if="!segs.length" class="status">（还没有段 —— 从下面挑一个加进来）</div>
    <div v-for="(sid, i) in segs" :key="sid" class="seg-row" :class="{ missing: segMissing(sid) }">
      <button type="button" class="icon-btn" title="上移" :disabled="i === 0" @click="moveSeg(i, -1)">↑</button>
      <button type="button" class="icon-btn" title="下移" :disabled="i === segs.length - 1" @click="moveSeg(i, 1)">↓</button>
      <span class="seg-id">{{ sid }}<template v-if="segMissing(sid)"> ⚠ 库里没有</template></span>
      <span class="seg-preview">{{ segText(sid).slice(0, 48) }}</span>
      <button type="button" class="icon-btn danger" title="移除（不删库里的段）" @click="dropSeg(i)">✕</button>
    </div>
    <div class="row">
      <select v-model="segPick" title="从段库挑一个">
        <option value="">从段库挑一个…</option>
        <option v-for="s in props.segments" :key="s.id" :value="s.id" :disabled="segs.includes(s.id)">
          {{ s.id }}{{ s.builtin ? "（内置）" : "" }}{{ s.imported ? "（导入）" : "" }} —— {{ s.text.slice(0, 30) }}
        </option>
      </select>
      <button type="button" :disabled="!segPick" @click="addSeg">添加段</button>
      <span v-if="missing.length" class="status">引用了库里没有的段：{{ missing.join("、") }}（保存会被后端拒）</span>
    </div>
    <details class="seg-assembled">
      <summary>拼装预览（{{ assembled.length }} 字）</summary>
      <pre>{{ assembled }}</pre>
    </details>
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
      <button type="button" @click="exportCartridge" title="导出 agent 卡带（JSON；段拼平进提示词、不留引用）">导出卡带</button>
      <button v-if="!isNew && !isDefault" type="button" @click="makeDefault">设为默认</button>
      <button v-if="!isNew" type="button" class="danger" @click="remove">删除</button>
      <span class="status">{{ status }}</span>
    </div>
  </div>
</template>
