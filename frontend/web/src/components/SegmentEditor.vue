<script setup lang="ts">
// 提示词段编辑器（设置 →「提示词」tab 内的二级页，列表 ⇄ 详情就地切换）：
// id（改名 = 所有 agent 的引用一起搬）/ 正文 / 被谁引用 / 删除（有引用 ⇒ 后端拒，先摘引用）。
import { VBtn, VTextField, VTextarea } from "vuetify/components";
import { ref, onMounted, onUnmounted } from "vue";
import { sharedApi } from "../utils/prefs.ts";
import type { Agent, Segment, UpdateSegmentRequest } from "../api/types.ts";

interface EditableSegment extends Segment {
  builtin?: boolean;
}

const props = withDefaults(defineProps<{
  segment?: EditableSegment | null;
  agents?: Agent[];
  ask?: ((text: string) => Promise<boolean>) | null;
}>(), {
  segment: null,
  agents: () => [],
  ask: null,
});
const emit = defineEmits<{ close: []; saved: [] }>();

const api = sharedApi();
const isNew = !props.segment;
const isBuiltin = !!props.segment?.builtin; // 内置：只读、删不掉 —— 要改走"复制为自定义"
const id = ref(props.segment?.id ?? "");
const text = ref(props.segment?.text ?? "");
const copyID = ref(""); // 复制为自定义用的新 id
const status = ref("");
const refs = (): string[] => props.agents
  .filter((a) => (a.segments ?? []).includes(props.segment?.id ?? ""))
  .map((a) => a.name || a.id);

async function save(): Promise<void> {
  const sid = id.value.trim();
  if (!sid) { status.value = "给个 id"; return; }
  try {
    if (isNew) {
      await api.createSegment({ id: sid, text: text.value });
    } else {
      const body: UpdateSegmentRequest = {};
      if (sid !== props.segment!.id) body.new_id = sid;
      if (text.value !== props.segment!.text) body.text = text.value;
      if (Object.keys(body).length) await api.patchSegment(props.segment!.id, body);
    }
    emit("saved");
    emit("close");
  } catch (e: unknown) {
    status.value = `保存失败：${e instanceof Error ? e.message : String(e)}`;
  }
}

async function remove(): Promise<void> {
  const users = refs();
  if (props.ask && !(await props.ask(users.length
    ? `「${props.segment!.id}」还被 ${users.join("、")} 用着 —— 先把引用摘掉再删。`
    : `删除段「${props.segment!.id}」？不可逆。`))) return;
  try {
    await api.deleteSegment(props.segment!.id);
    emit("saved");
    emit("close");
  } catch (e: unknown) {
    status.value = `删除失败：${e instanceof Error ? e.message : String(e)}`;
  }
}

// 复制为自定义：内置只读，改不了 —— 把正文原样复制成一个用户段（新 id），接着编它。
async function copyToCustom(): Promise<void> {
  const sid = copyID.value.trim();
  if (!sid) { status.value = "给副本起个 id"; return; }
  try {
    await api.createSegment({ id: sid, text: props.segment!.text });
    emit("saved");
    emit("close");
  } catch (e: unknown) {
    status.value = `复制失败：${e instanceof Error ? e.message : String(e)}`;
  }
}

onMounted(() => document.addEventListener("keydown", onKey));
onUnmounted(() => document.removeEventListener("keydown", onKey));
function onKey(e: KeyboardEvent): void { if (e.key === "Escape") emit("close"); }
</script>

<template>
  <div class="detail-pane">
    <div class="panel-head">
      <VBtn icon variant="text" size="small" type="button" title="返回列表（Esc）" @click="emit('close')">←</VBtn>
      <span class="status">{{ isNew ? "新建提示词片段" : isBuiltin ? `内置片段「${props.segment!.id}」（只读）` : `编辑「${props.segment!.id}」${props.segment!.imported ? "（导入）" : ""}` }}</span>
    </div>
    <label class="field"><span>名称（改名会同步所有引用）</span><VTextField density="compact" variant="outlined" hide-details v-model="id" :readonly="isBuiltin" placeholder="例如：状态语法 / 数学公式 / 联网规则" /></label>
    <label class="field-block"><span>内容（会原样追加到提示词末尾）</span><VTextarea v-model="text" rows="12" :readonly="isBuiltin" placeholder="这段提示词的内容…" density="compact" variant="outlined" hide-details /></label>
    <div v-if="!isNew" class="status">谁在用：{{ refs().join("、") || "（没人用）" }}</div>
    <div v-if="props.segment?.preset_id" class="status">来源：ST 预设「{{ props.segment.preset_name || props.segment.preset_id }}」</div>
    <div v-if="isBuiltin" class="row">
      <span class="status">内置片段随应用提供：只读、删不掉。想改就复制一份：</span>
      <VTextField density="compact" variant="outlined" hide-details v-model="copyID" placeholder="新片段的名称" />
      <VBtn color="primary" variant="flat" size="small" type="button" @click="copyToCustom">复制一份来改</VBtn>
    </div>
    <div v-else class="row">
      <VBtn color="primary" variant="flat" size="small" type="button" @click="save">保存</VBtn>
      <span class="action-note">保存后，引用它的智能体从下一轮开始使用新文本。</span>
      <VBtn color="error" variant="text" size="small" v-if="!isNew" type="button" @click="remove">删除</VBtn>
    </div>
    <div class="row"><span class="status">{{ status }}</span></div>
  </div>
</template>
