<script setup lang="ts">
// 模型家族小图标。图标资源在 `public/model-icons/`（来自 models.dev 的 provider logo，
// 本地托管，不走外链）；用 CSS mask + 品牌色，亮/暗主题都能看。
import { computed } from "vue";
import { MODEL_ICON_IDS } from "../utils/modelIcons.ts";

const props = withDefaults(defineProps<{
  icon?: string | null;
  brand?: string | null;
  size?: "sm" | "md" | "lg";
}>(), {
  icon: "",
  brand: "",
  size: "md",
});
const icon = computed(() => (props.icon || "").trim().toLowerCase());
const known = computed(() => MODEL_ICON_IDS.includes(icon.value));
const initial = computed(() => {
  // 没有匹配到图标规则时**什么都不画**，别用问号占位：
  // 问号既不是品牌信息，也会在 flex 容器里被拉成一条。
  const source = (props.brand || "").trim();
  return source ? source.slice(0, 1).toUpperCase() : "";
});
</script>

<template>
  <span v-if="known" class="model-icon model-glyph" :class="[`mi-${icon}`, `size-${size}`]" aria-hidden="true"></span>
  <span v-else-if="initial" class="model-icon model-icon-letter" :class="`size-${size}`" aria-hidden="true">{{ initial }}</span>
</template>

<style scoped>
.model-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 1.5em;
  height: 1.5em;
  flex: none;
  color: var(--md-on-surface-variant, currentColor);
  font-size: 1em;
  font-weight: 700;
  line-height: 1;
}
.size-sm { width: 1.2em; height: 1.2em; }
.size-lg { width: 2em; height: 2em; }
.model-glyph {
  background-color: var(--model-icon-color, currentColor);
  -webkit-mask-repeat: no-repeat;
  mask-repeat: no-repeat;
  -webkit-mask-position: center;
  mask-position: center;
  -webkit-mask-size: contain;
  mask-size: contain;
}
.mi-gpt { --model-icon-color: #10a37f; -webkit-mask-image: url("/model-icons/gpt.svg"); mask-image: url("/model-icons/gpt.svg"); }
.mi-gemini { --model-icon-color: #4285f4; -webkit-mask-image: url("/model-icons/gemini.svg"); mask-image: url("/model-icons/gemini.svg"); }
.mi-grok { --model-icon-color: currentColor; -webkit-mask-image: url("/model-icons/grok.svg"); mask-image: url("/model-icons/grok.svg"); }
.mi-claude { --model-icon-color: #d97757; -webkit-mask-image: url("/model-icons/claude.svg"); mask-image: url("/model-icons/claude.svg"); }
.mi-deepseek { --model-icon-color: #4d6bfe; -webkit-mask-image: url("/model-icons/deepseek.svg"); mask-image: url("/model-icons/deepseek.svg"); }
.mi-mimo { --model-icon-color: #ff6900; -webkit-mask-image: url("/model-icons/mimo.svg"); mask-image: url("/model-icons/mimo.svg"); }
.mi-glm { --model-icon-color: #3b82f6; -webkit-mask-image: url("/model-icons/glm.svg"); mask-image: url("/model-icons/glm.svg"); }
.mi-kimi { --model-icon-color: #7c3aed; -webkit-mask-image: url("/model-icons/kimi.svg"); mask-image: url("/model-icons/kimi.svg"); }
.mi-longcat { --model-icon-color: #1f2937; -webkit-mask-image: url("/model-icons/longcat.svg"); mask-image: url("/model-icons/longcat.svg"); }
.mi-mistral { --model-icon-color: #ff7000; -webkit-mask-image: url("/model-icons/mistral.svg"); mask-image: url("/model-icons/mistral.svg"); }
.mi-qwen { --model-icon-color: #615ced; -webkit-mask-image: url("/model-icons/qwen.svg"); mask-image: url("/model-icons/qwen.svg"); }
.mi-minimax { --model-icon-color: #e73562; -webkit-mask-image: url("/model-icons/minimax.svg"); mask-image: url("/model-icons/minimax.svg"); }
.mi-tencent { --model-icon-color: #0052d9; -webkit-mask-image: url("/model-icons/tencent.svg"); mask-image: url("/model-icons/tencent.svg"); }
.mi-meta { --model-icon-color: #0866ff; -webkit-mask-image: url("/model-icons/meta.svg"); mask-image: url("/model-icons/meta.svg"); }
.mi-stepfun { --model-icon-color: #1677ff; -webkit-mask-image: url("/model-icons/stepfun.svg"); mask-image: url("/model-icons/stepfun.svg"); }
.mi-bytedance { --model-icon-color: #1664ff; -webkit-mask-image: url("/model-icons/bytedance.svg"); mask-image: url("/model-icons/bytedance.svg"); }
.model-icon-letter {
  border: 1px solid currentColor;
  border-radius: 50%;
}
</style>
