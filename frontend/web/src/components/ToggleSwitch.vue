<script setup lang="ts">
// ToggleSwitch：开关滑块（TG 风）。v-model:boolean，带 150ms 滑块位移动画。
// 复用处：回传思考 / 输入预演 / 三能力 enabled（legend 里那三个）。
const props = defineProps<{ modelValue?: boolean }>();
const emit = defineEmits<{ "update:modelValue": [value: boolean] }>();
</script>

<template>
  <button
    type="button"
    class="toggle"
    :class="{ on: props.modelValue }"
    role="switch"
    :aria-checked="props.modelValue"
    @click="emit('update:modelValue', !props.modelValue)"
  >
    <span class="knob"></span>
  </button>
</template>
