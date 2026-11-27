<script setup lang="ts">
// 提示词段编辑器（设置 →「提示词」tab 内的二级页，列表 ⇄ 详情就地切换）：
// id（改名 = 所有 agent 的引用一起搬）/ 正文 / 被谁引用 / 删除（有引用 ⇒ 后端拒，先摘引用）。
import { VBtn, VTextField, VTextarea } from "vuetify/components";
import { ref, shallowRef, onMounted, onUnmounted } from "vue";
import { sharedApi } from "../utils/prefs.ts";
import SettingsBack from "./SettingsBack.vue";
import type { Agent, Segment, UpdateSegmentRequest } from "../api/types.ts";

interface EditableSegment extends Segment {
  builtin?: boolean;
}

const props = withDefaults(defineProps<{
	segment?: EditableSegment | null;
	agents?: Agent[];
	ask?: ((text: string) => Promise<boolean>) | null;
	conflict?: boolean;
}>(), {
	segment: null,
	agents: () => [],
	ask: null,
	conflict: false,
});
const emit = defineEmits<{ close: []; saved: [rename?: { oldId: string; newId: string }]; reload: [] }>();

const api = sharedApi();
const isNew = !props.segment;
const isBuiltin = !!props.segment?.builtin; // 内置：只读、删不掉 —— 要改走"复制为自定义"
const id = ref(props.segment?.id ?? "");
const text = ref(props.segment?.text ?? "");
const copyID = ref(""); // 复制为自定义用的新 id
const status = ref("");
const action = shallowRef<"save" | "confirm-delete" | "delete" | "copy" | null>(null);
let active = true;
const refs = (): string[] => props.agents
  .filter((agent) => (agent.segments ?? []).includes(props.segment?.id ?? "")
    || (agent.latest_prompt_segments ?? []).includes(props.segment?.id ?? "")
    || Object.values(agent.abilities ?? {}).some((ability) => (ability.segments ?? []).includes(props.segment?.id ?? "")))
  .map((agent) => agent.name || agent.id);

async function save(): Promise<void> {
	if (action.value) return;
	if (props.conflict) { status.value = "此片段已在其他位置更改或删除，请先重新加载。"; return; }
	const sid = id.value.trim();
	if (!sid) { status.value = "给个 id"; return; }
	action.value = "save";
	try {
		let rename: { oldId: string; newId: string } | undefined;
		if (isNew) {
			await api.createSegment({ id: sid, text: text.value });
		} else {
			const body: UpdateSegmentRequest = {};
			if (sid !== props.segment!.id) {
				body.new_id = sid;
				rename = { oldId: props.segment!.id, newId: sid };
			}
			if (text.value !== props.segment!.text) body.text = text.value;
			if (Object.keys(body).length) await api.patchSegment(props.segment!.id, body);
		}
		finishEdit(rename);
	} catch (e: unknown) {
		status.value = `保存失败：${e instanceof Error ? e.message : String(e)}`;
	} finally {
		action.value = null;
	}
}

async function remove(): Promise<void> {
	const segment = props.segment;
	if (!segment || isBuiltin || action.value) return;
	if (props.conflict) { status.value = "此片段已在其他位置更改或删除，请先重新加载。"; return; }
	const users = refs();
	action.value = "confirm-delete";
	try {
		if (props.ask && !(await props.ask(users.length
			? `「${segment.id}」还被 ${users.join("、")} 用着 —— 先把引用摘掉再删。`
			: `删除段「${segment.id}」？不可逆。`))) return;
		if (!active) return;
		if (props.conflict) { status.value = "此片段已在其他位置更改或删除，请先重新加载。"; return; }
		action.value = "delete";
		await api.deleteSegment(segment.id);
		finishEdit();
	} catch (e: unknown) {
		status.value = `删除失败：${e instanceof Error ? e.message : String(e)}`;
	} finally {
		action.value = null;
	}
}

async function copyToCustom(): Promise<void> {
	if (action.value) return;
	if (props.conflict) { status.value = "此片段已在其他位置更改或删除，请先重新加载。"; return; }
	const sid = copyID.value.trim();
	if (!sid) { status.value = "给副本起个 id"; return; }
	action.value = "copy";
	try {
		await api.copySegment(props.segment!.id, sid);
		finishEdit();
	} catch (e: unknown) {
		status.value = `复制失败：${e instanceof Error ? e.message : String(e)}`;
	} finally {
		action.value = null;
	}
}

function finishEdit(rename?: { oldId: string; newId: string }): void {
  if (!active) return;
  emit("saved", rename);
  emit("close");
}

onMounted(() => document.addEventListener("keydown", onKey));
onUnmounted(() => {
  active = false;
  document.removeEventListener("keydown", onKey);
});
function onKey(e: KeyboardEvent): void { if (e.key === "Escape" && !action.value) emit("close"); }
</script>

<template>
  <div class="detail-pane">
    <div class="panel-head">
      <SettingsBack :disabled="!!action" @back="emit('close')" />
      <span class="status">{{ isNew ? "新建提示词片段" : isBuiltin ? `内置片段「${props.segment!.id}」（只读）` : `编辑「${props.segment!.id}」${props.segment!.imported ? "（导入）" : ""}` }}</span>
    </div>
	<div v-if="conflict" class="status segment-conflict" role="alert">
		<span>此片段已在其他位置更改或删除。当前草稿仍保留。</span>
		<VBtn variant="text" size="small" type="button" @click="emit('reload')">重新加载</VBtn>
	</div>
    <label class="field"><span>名称（改名会同步所有引用）</span><VTextField density="compact" variant="outlined" hide-details v-model="id" :readonly="isBuiltin || !!action" placeholder="例如：状态语法 / 数学公式 / 联网规则" /></label>
  <label class="field-block"><span>内容（在引用它的提示词位置原样使用）</span><VTextarea v-model="text" rows="12" :readonly="isBuiltin || !!action" placeholder="这段提示词的内容…" density="compact" variant="outlined" hide-details /></label>
    <div v-if="!isNew" class="status">谁在用：{{ refs().join("、") || "（没人用）" }}</div>
    <div v-if="props.segment?.preset_id" class="status">来源：ST 预设「{{ props.segment.preset_name || props.segment.preset_id }}」</div>
    <div v-if="isBuiltin" class="row">
      <span class="status">内置片段随应用提供：只读、删不掉。想改就复制一份：</span>
      <VTextField density="compact" variant="outlined" hide-details v-model="copyID" :readonly="!!action" placeholder="新片段的名称" />
		<VBtn color="primary" variant="flat" size="small" type="button" :loading="action === 'copy'" :disabled="!!action || conflict" @click="copyToCustom">复制一份来改</VBtn>
    </div>
    <div v-else class="row">
		<VBtn color="primary" variant="flat" size="small" type="button" :loading="action === 'save'" :disabled="!!action || conflict" @click="save">保存</VBtn>
		<span class="action-note">保存后，引用它的智能体从下一轮开始使用新文本。</span>
		<VBtn color="error" variant="text" size="small" v-if="!isNew" type="button" :loading="action === 'delete'" :disabled="!!action || conflict" @click="remove">删除</VBtn>
    </div>
    <div class="row"><span class="status">{{ status }}</span></div>
  </div>
</template>
