<script setup lang="ts">
// 助手（Agent）选择入口（2026-11-10 用户定：从顶栏挪到输入栏上方，最左侧）。
// 只负责头像 + 选人菜单；真正的 PATCH 由 App 的 applyAgentValue 做。
import { computed, ref } from "vue";
import type { Agent } from "../api/types.ts";

const props = withDefaults(defineProps<{
  agents?: Agent[];
  modelValue?: string;
}>(), { agents: () => [], modelValue: "" });
const emit = defineEmits<{ select: [id: string] }>();
const open = ref(false);

interface AgentItem { id: string; label: string }
const items = computed<AgentItem[]>(() => {
  // FlowAgent 的运行时还没接线：可以编辑，但先不出现在会话的助手切换器里，
  // 免得选了一个不会按图执行的 Agent。已有会话若曾被绑定，仍把它显示为“当前”。
  const list: AgentItem[] = (props.agents ?? [])
    .filter((agent) => agent.kind !== "flow")
    .map((agent) => ({ id: agent.id, label: agent.name || agent.id }));
  if (props.modelValue && !list.some((agent) => agent.id === props.modelValue)) {
    const current = (props.agents ?? []).find((agent) => agent.id === props.modelValue);
    list.unshift({ id: props.modelValue, label: `${current?.name || props.modelValue}${current?.kind === "flow" ? "（FlowAgent）" : "（不在列表）"}` });
  }
  return list;
});
const current = computed(() => items.value.find((agent) => agent.id === props.modelValue)?.label ?? (props.modelValue || "助手"));
const initial = computed(() => Array.from(current.value.trim())[0] ?? "?");
const hue = computed(() => {
  let value = 0;
  for (const char of String(props.modelValue || "")) value = (value * 31 + (char.codePointAt(0) ?? 0)) % 360;
  return value;
});
const dotStyle = computed(() => ({ background: `hsl(${hue.value} 42% 46%)` }));
function pick(id: string): void { open.value = false; emit("select", id); }
</script>

<template>
  <span class="agent-anchor" @keydown.esc="open = false">
    <button
      id="agent-btn"
      class="agent-btn"
      type="button"
      :style="dotStyle"
      :title="`当前助手：${current}（点击切换）`"
      aria-label="切换 Agent"
      :aria-expanded="open ? 'true' : 'false'"
      @click="open = !open"
    >{{ initial }}</button>
    <template v-if="open">
      <div class="agent-menu-backdrop" @click="open = false"></div>
      <div class="agent-menu agent-picker" role="menu">
        <button
          v-for="agent in items"
          :key="agent.id"
          type="button"
          class="agent-item"
          :class="{ sel: agent.id === modelValue }"
          @click="pick(agent.id)"
        >
          <span class="agent-dot" :style="{
            background: `hsl(${(() => { let v = 0; for (const ch of String(agent.id)) v = (v * 31 + (ch.codePointAt(0) ?? 0)) % 360; return v; })()} 42% 46%)`,
          }">{{ Array.from(agent.label.trim())[0] ?? "?" }}</span>
          <span class="agent-name">{{ agent.label }}</span>
          <span v-if="agent.id === modelValue" class="agent-check">✓</span>
        </button>
        <p v-if="!items.length" class="agent-empty">没有可选的 Agent</p>
      </div>
    </template>
  </span>
</template>
