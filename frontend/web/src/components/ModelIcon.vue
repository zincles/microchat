<script setup lang="ts">
// 模型家族小图标。后端只给 slug（model_define_rules 的 icon），这里负责把它画出来；
// 不认识的 icon 用品牌首字兜底，绝不把 slug 当文字塞进列表。
import { computed } from "vue";

const props = withDefaults(defineProps<{ icon?: string | null; brand?: string | null }>(), {
  icon: "",
  brand: "",
});
const icon = computed(() => {
  const raw = (props.icon || "").trim().toLowerCase();
  // 规则里写 x-ai / open-ai 也能落到同一枚图标（id 的写法不统一是常态）。
  if (raw === "x-ai" || raw === "xai") return "xai";
  if (raw === "open-ai" || raw === "openai") return "openai";
  return raw;
});
const initial = computed(() => {
  const source = (props.brand || props.icon || "?").trim();
  return source ? source.slice(0, 1).toUpperCase() : "?";
});
</script>

<template>
  <span class="model-icon" :class="`mi-${icon || 'generic'}`" aria-hidden="true">
    <svg v-if="icon === 'xai'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round">
      <path d="M4 4l16 16M20 4L4 20" />
    </svg>
    <svg v-else-if="icon === 'openai'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7">
      <circle cx="12" cy="12" r="8.2" />
      <path d="M12 3.8v16.4M3.8 12h16.4M6.3 6.3l11.4 11.4M17.7 6.3L6.3 17.7" stroke-width="1" opacity=".55" />
    </svg>
    <span v-else class="model-icon-letter">{{ initial }}</span>
  </span>
</template>

<style scoped>
.model-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 1.15em;
  height: 1.15em;
  flex: none;
  color: var(--md-on-surface-variant, currentColor);
  font-size: 0.78em;
  font-weight: 700;
  line-height: 1;
  border-radius: 50%;
}
.model-icon svg { width: 100%; height: 100%; }
.model-icon-letter {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 100%;
  height: 100%;
  border: 1px solid currentColor;
  border-radius: 50%;
}
</style>
