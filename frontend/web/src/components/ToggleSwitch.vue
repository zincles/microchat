<script setup>
// ToggleSwitch：开关滑块（TG 风）。v-model:boolean，带 150ms 滑块位移动画。
// 复用处：回传思考 / 输入预演 / 三能力 enabled（legend 里那三个）。
const props = defineProps({ modelValue: Boolean });
const emit = defineEmits(["update:modelValue"]);
</script>

<template>
  <button
    type="button"
    class="toggle"
    :class="{ on: modelValue }"
    role="switch"
    :aria-checked="modelValue"
    @click="$emit('update:modelValue', !modelValue)"
  >
    <span class="knob"></span>
  </button>
</template>
