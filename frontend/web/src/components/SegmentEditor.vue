<script setup>
// 提示词段编辑器（设置 →「提示词」tab 内的二级页，列表 ⇄ 详情就地切换）：
// id（改名 = 所有 agent 的引用一起搬）/ 正文 / 被谁引用 / 删除（有引用 ⇒ 后端拒，先摘引用）。
import { ref, onMounted, onUnmounted } from "vue";
import { sharedApi } from "../utils/prefs.ts";

const props = defineProps({
  segment: { type: Object, default: null }, // null = 新建
  agents: { type: Array, default: () => [] },
  ask: { type: Function, default: null }, // 全局确认框（删除前先摊开）
});
const emit = defineEmits(["close", "saved"]);

const api = sharedApi();
const isNew = !props.segment;
const isBuiltin = !!props.segment?.builtin; // 内置：只读、删不掉 —— 要改走"复制为自定义"
const id = ref(props.segment?.id ?? "");
const text = ref(props.segment?.text ?? "");
const copyID = ref(""); // 复制为自定义用的新 id
const status = ref("");
const refs = () => props.agents.filter((a) => (a.segments ?? []).includes(props.segment?.id ?? "")).map((a) => a.name || a.id);

async function save() {
  const sid = id.value.trim();
  if (!sid) { status.value = "给个 id"; return; }
  try {
    if (isNew) {
      await api.createSegment({ id: sid, text: text.value });
    } else {
      const body = {};
      if (sid !== props.segment.id) body.new_id = sid;
      if (text.value !== props.segment.text) body.text = text.value;
      if (Object.keys(body).length) await api.patchSegment(props.segment.id, body);
    }
    emit("saved");
    emit("close");
  } catch (e) { status.value = `保存失败：${e.message}`; }
}

async function remove() {
  const users = refs();
  if (props.ask && !(await props.ask(users.length
    ? `「${props.segment.id}」还被 ${users.join("、")} 用着 —— 先把引用摘掉再删。`
    : `删除段「${props.segment.id}」？不可逆。`))) return;
  try {
    await api.deleteSegment(props.segment.id);
    emit("saved");
    emit("close");
  } catch (e) { status.value = `删除失败：${e.message}`; }
}

// 复制为自定义：内置只读，改不了 —— 把正文原样复制成一个用户段（新 id），接着编它。
async function copyToCustom() {
  const sid = copyID.value.trim();
  if (!sid) { status.value = "给副本起个 id"; return; }
  try {
    await api.createSegment({ id: sid, text: props.segment.text });
    emit("saved");
    emit("close");
  } catch (e) { status.value = `复制失败：${e.message}`; }
}

onMounted(() => document.addEventListener("keydown", onKey));
onUnmounted(() => document.removeEventListener("keydown", onKey));
function onKey(e) { if (e.key === "Escape") emit("close"); }
</script>

<template>
  <div class="detail-pane">
    <div class="panel-head">
      <button class="icon-btn" type="button" title="返回列表（Esc）" @click="$emit('close')">←</button>
      <span class="status">{{ isNew ? "新建提示词片段" : isBuiltin ? `内置片段「${props.segment.id}」（只读）` : `编辑「${props.segment.id}」${props.segment.imported ? "（导入）" : ""}` }}</span>
    </div>
    <label class="field"><span>名称（改名会同步所有引用）</span><input v-model="id" :readonly="isBuiltin" placeholder="例如：状态语法 / 数学公式 / 联网规则" /></label>
    <label class="field-block"><span>内容（会原样追加到提示词末尾）</span><textarea v-model="text" rows="12" :readonly="isBuiltin" placeholder="这段提示词的内容…"></textarea></label>
    <div v-if="!isNew" class="status">谁在用：{{ refs().join("、") || "（没人用）" }}</div>
    <div v-if="isBuiltin" class="row">
      <span class="status">内置片段随应用提供：只读、删不掉。想改就复制一份：</span>
      <input v-model="copyID" placeholder="新片段的名称" />
      <button type="button" class="primary" @click="copyToCustom">复制一份来改</button>
    </div>
    <div v-else class="row">
      <button type="button" class="primary" @click="save">保存</button>
      <button v-if="!isNew" type="button" class="danger" @click="remove">删除</button>
    </div>
    <div class="row"><span class="status">{{ status }}</span></div>
  </div>
</template>
