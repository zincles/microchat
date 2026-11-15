<script setup lang="ts">
// 一个端口一行：Handle + 可读标签 + 可选元信息 + 可选删除键。
//
// 以前每个节点各自把 Handle 绝对定位在行内，标签只靠旁边一段裸文字；
// LLM 的 Prompt 行、join 的输入输出、output 的三个入口因此各漂各的。
// 这里是唯一实现：Handle 贴着节点边缘，标签跟着它走；有正文的端口把正文放在同一行下面。
import { computed } from "vue";
import { Handle, Position } from "@vue-flow/core";
import { VBtn } from "vuetify/components";
import type { FlowPortType } from "../utils/flow.ts";

const props = withDefaults(defineProps<{
  id: string;
  type: FlowPortType;
  side?: "left" | "right";
  label: string;
  meta?: string;
  rootClass?: string;
  removable?: boolean;
  removeDisabled?: boolean;
  removeTitle?: string;
}>(), {
  side: "left",
  meta: "",
  rootClass: "",
  removable: false,
  removeDisabled: false,
  removeTitle: "移除这个端口",
});

const emit = defineEmits<{ remove: [] }>();
const handleType = computed(() => props.side === "right" ? "source" : "target");
const position = computed(() => props.side === "right" ? Position.Right : Position.Left);
</script>

<template>
  <div
    class="flow-port-row"
    :class="[rootClass, side === 'right' ? 'is-right' : 'is-left', { 'has-body': $slots.default }]"
    :data-port-id="id"
    :title="`端口：${id}`"
  >
    <Handle :id="id" :type="handleType" :position="position" :class="`flow-port-${type}`" />
    <div class="flow-port-head">
      <span class="flow-port-label">{{ label }}</span>
      <span v-if="meta" class="flow-port-meta">{{ meta }}</span>
      <slot name="meta" />
      <VBtn
        v-if="removable"
        icon
        variant="outlined"
        size="x-small"
        type="button"
        class="flow-port-del nodrag nopan"
        :disabled="removeDisabled"
        :title="removeTitle"
        :aria-label="removeTitle"
        @click.stop="emit('remove')"
      >−</VBtn>
    </div>
    <div v-if="$slots.default" class="flow-port-body">
      <slot />
    </div>
  </div>
</template>
