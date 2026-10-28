<script setup>
// Agent 编辑器（设置视图 Agent tab 内的二级页，列表 ⇄ 详情就地切换）：新建 / 改 id·名 / 提示词 / 三能力开关 / 设默认 / 删除。
// isNew（boolean）：无 agent 快照=新建（空表单）；有=编辑（带入快照，保存后父重拉，id 变了也对得上）。关即销毁。
import { ref, computed, onMounted, onUnmounted } from "vue";
import { sharedApi } from "../utils/prefs.js";
import { ABILITY_IDS, ABILITY_TEMPLATE_SEGMENT, buildAbilitiesPatch, modelKey, parseModelKey, modelLabel } from "../utils/format.js";
import { assemblePrompt, abilityPrompt } from "../utils/prompt.js";
import { buildCartridge } from "../utils/cartridge.js";
import { downloadJSON } from "../utils/download.js";
import ToggleSwitch from "./ToggleSwitch.vue";
import SegmentList from "./SegmentList.vue";

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
// 追加段（顺序 = 拼接顺序）。段库在 `props.segments`；挑 / 排 / 删与预览都在
// `SegmentList` 里（与能力那块**同一个组件** —— 用户 2026-10-28：「不协调」）。
const segs = ref([...(props.agent?.segments ?? props.template?.segments ?? [])]);
const assembled = computed(() => assemblePrompt(prompt.value, segs.value, props.segments));
const modelOptions = ref([]);
const status = ref("");

// —— 能力里的提示词段（2026-10-28 用户定："用于 Compact 的提示词段"也要能自由组合）——
// 编辑器就是 `SegmentList`（与上面 agent 级**同一个组件**：同一个段库、同一条拼装规则、
// 同一套文案）。这里只剩"这一发拼出来是什么"和"要不要提醒一句"。
// 这能力**真正会发出去**的提示词（模板 + 段）。模板 = 你填的覆盖 ?: 那条内置模板段。
// 压缩的默认模板 2026-10-28 搬进段库（`压缩提示词`）⇒ 前端终于能把它显示全了
// （从前它在 Go 的常量里，预览只能显示"段"，不敢报总量）。
function abilityAssembled(id) {
  const r = rows.value[id] ?? {};
  return abilityPrompt(r.prompt, r.segments ?? [], props.segments, ABILITY_TEMPLATE_SEGMENT[id]);
}
// 这个能力有没有"模板"（覆盖或内置段）：没有 ⇒ 预览要提醒"这一发只有你挑的段"
function abilityHasTemplate(id) {
  const r = rows.value[id] ?? {};
  if (String(r.prompt ?? "").trim()) return true;
  const seg = ABILITY_TEMPLATE_SEGMENT[id];
  return !!seg && !!(r.segments ?? []).includes(seg);
}
// 段清单上面那一行说明（`SegmentList` 的 hint）：只在"用户可能误判"的地方说一句。
function abilitySegHint(id) {
  const template = ABILITY_TEMPLATE_SEGMENT[id];
  const r = rows.value[id] ?? {};
  if (!template) {
    return "这个能力还没有流程（judge 的调用点还没写）—— 段会存进配置，等流程接上才生效。";
  }
  if (!(r.segments ?? []).length) return `空着 = 只用内置段「${template}」（可在段库里查看 / 复制成自定义）。`;
  if (!abilityHasTemplate(id)) return `⚠ 没挑「${template}」也没填覆盖 ⇒ 这一发只有你挑的段，没有流程说明。`;
  return "";
}

function fillRows(a) {
  const ab = a?.abilities ?? {};
  const out = {};
  for (const id of ABILITY_IDS) {
    const one = ab[id] ?? {};
    const both = one.provider && one.model ? modelKey(one.provider, one.model) : "";
    // segments：能力的追加段（2026-10-28）。老配置没有这一格 ⇒ 空数组（不是 undefined，
    // 否则模板里 `rows[id].segments.length` 会在老数据上炸）。
    out[id] = { enabled: one.enabled !== false, model: both, prompt: one.prompt ?? "", segments: [...(one.segments ?? [])] };
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
      segments: [...(r.segments ?? [])],
    };
  }
  return buildAbilitiesPatch(out);
}

async function save() {
  if (!name.value.trim()) { status.value = "给个名字"; return; }
  try {
    if (isNew) {
      await api.createAgent({ name: name.value.trim(), system_prompt: prompt.value, first_message: firstMsg.value, segments: segs.value, abilities: toPatch(), prepend_state: prepend.value || undefined });
    } else {
      const body = { system_prompt: prompt.value, first_message: firstMsg.value, segments: segs.value, abilities: toPatch() };
      if (!!prepend.value !== !!props.agent.prepend_state) body.prepend_state = prepend.value;
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
    <SegmentList
      v-model:ids="segs"
      :segments="props.segments"
      :assembled="assembled"
      heading="提示词段（追加在 base 后面，顺序 = 拼接顺序）" />
    <label class="field"><span>句首贴当前状态（RP 类需要）</span><ToggleSwitch v-model="prepend" /></label>
    <h3>能力（开关 + 模型 / 提示词 / 提示词段，空=默认）</h3>
    <fieldset v-for="id in ABILITY_IDS" :key="id" class="ability-block">
      <legend><ToggleSwitch v-model="rows[id].enabled" /> {{ id }}</legend>
      <label class="field"><span>Model</span><select v-model="rows[id].model">
        <option value="">跟会话走</option>
        <option v-for="m in modelOptions" :key="m.value" :value="m.value">{{ m.label }}</option>
      </select></label>
      <label class="field-block"><span>提示词覆盖（空=默认模板）</span><textarea v-model="rows[id].prompt" rows="3"></textarea></label>
      <!-- 提示词段：追加在（覆盖 ?: 默认）模板**后面**，顺序 = 这里的顺序。
           改了模板不必把段里的字抄进来 —— 抄进来就是两份，改一处忘一处。
           编辑器就是 agent 级那一个 `SegmentList`（同一个组件 ⇒ 不会各长一样）。 -->
      <SegmentList
        v-model:ids="rows[id].segments"
        :segments="props.segments"
        :assembled="abilityAssembled(id)"
        :hint="abilitySegHint(id)"
        heading="提示词段（追加在模板后面，顺序 = 拼接顺序）" />
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
