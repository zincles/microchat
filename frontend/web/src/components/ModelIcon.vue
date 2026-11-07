<script setup lang="ts">
// 模型家族小图标。后端只给封闭集合里的 slug（model_define_rules 的 icon），这里负责画；
// 不认识的 icon 用品牌首字兜底，绝不把 slug 当文字塞进列表。
import { computed } from "vue";

const props = withDefaults(defineProps<{ icon?: string | null; brand?: string | null }>(), {
  icon: "",
  brand: "",
});
const icon = computed(() => (props.icon || "").trim().toLowerCase());
const initial = computed(() => {
  const source = (props.brand || props.icon || "?").trim();
  return source ? source.slice(0, 1).toUpperCase() : "?";
});
</script>

<template>
  <span class="model-icon" :class="`mi-${icon || 'generic'}`" aria-hidden="true">
    <svg v-if="icon === 'gpt'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7">
      <circle cx="12" cy="12" r="8.2" />
      <path d="M12 3.8v16.4M3.8 12h16.4M6.3 6.3l11.4 11.4M17.7 6.3L6.3 17.7" stroke-width="1" opacity=".55" />
    </svg>
    <svg v-else-if="icon === 'gemini'" viewBox="0 0 24 24" fill="currentColor">
      <path d="M12 2.5c.7 4.7 4.3 8.3 9 9-4.7.7-8.3 4.3-9 9-.7-4.7-4.3-8.3-9-9 4.7-.7 8.3-4.3 9-9z" />
    </svg>
    <svg v-else-if="icon === 'grok'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round">
      <path d="M4 4l16 16M20 4L4 20" />
    </svg>
    <svg v-else-if="icon === 'claude'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round">
      <path d="M12 3v18M3 12h18M5.6 5.6l12.8 12.8M18.4 5.6L5.6 18.4" />
    </svg>
    <svg v-else-if="icon === 'deepseek'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round">
      <path d="M3 9c3-3 5 3 8 0s5 3 8 0M3 15c3-3 5 3 8 0s5 3 8 0" />
    </svg>
    <svg v-else-if="icon === 'mimo'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round">
      <path d="M5 19V5l7 9 7-9v14" />
    </svg>
    <svg v-else-if="icon === 'glm'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round">
      <path d="M19 8a7 7 0 1 0 0 8h-5" />
    </svg>
    <svg v-else-if="icon === 'kimi'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round">
      <path d="M6 4v16M18 4l-8 8 8 8" />
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
