<script setup lang="ts">
// 模型家族小图标。图标资源在 `public/model-icons/`（来自 models.dev 的 provider logo，
// 本地托管，不走外链）；用 CSS mask 让它们继承 currentColor，亮/暗主题都能看。
import { computed } from "vue";
import { MODEL_ICON_IDS } from "../utils/modelIcons.ts";

const props = withDefaults(defineProps<{ icon?: string | null; brand?: string | null }>(), {
  icon: "",
  brand: "",
});
const icon = computed(() => (props.icon || "").trim().toLowerCase());
const known = computed(() => MODEL_ICON_IDS.includes(icon.value));
const initial = computed(() => {
  const source = (props.brand || props.icon || "?").trim();
  return source ? source.slice(0, 1).toUpperCase() : "?";
});
</script>

<template>
  <span v-if="known" class="model-icon model-glyph" :class="`mi-${icon}`" aria-hidden="true"></span>
  <span v-else class="model-icon model-icon-letter" aria-hidden="true">{{ initial }}</span>
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
}
.model-glyph {
  background-color: currentColor;
  -webkit-mask-repeat: no-repeat;
  mask-repeat: no-repeat;
  -webkit-mask-position: center;
  mask-position: center;
  -webkit-mask-size: contain;
  mask-size: contain;
}
.mi-gpt { -webkit-mask-image: url("/model-icons/gpt.svg"); mask-image: url("/model-icons/gpt.svg"); }
.mi-gemini { -webkit-mask-image: url("/model-icons/gemini.svg"); mask-image: url("/model-icons/gemini.svg"); }
.mi-grok { -webkit-mask-image: url("/model-icons/grok.svg"); mask-image: url("/model-icons/grok.svg"); }
.mi-claude { -webkit-mask-image: url("/model-icons/claude.svg"); mask-image: url("/model-icons/claude.svg"); }
.mi-deepseek { -webkit-mask-image: url("/model-icons/deepseek.svg"); mask-image: url("/model-icons/deepseek.svg"); }
.mi-mimo { -webkit-mask-image: url("/model-icons/mimo.svg"); mask-image: url("/model-icons/mimo.svg"); }
.mi-glm { -webkit-mask-image: url("/model-icons/glm.svg"); mask-image: url("/model-icons/glm.svg"); }
.mi-kimi { -webkit-mask-image: url("/model-icons/kimi.svg"); mask-image: url("/model-icons/kimi.svg"); }
.model-icon-letter {
  width: 100%;
  height: 100%;
  border: 1px solid currentColor;
  border-radius: 50%;
}
</style>
