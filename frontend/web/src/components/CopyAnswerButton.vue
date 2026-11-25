<script setup lang="ts">
import { onUnmounted, shallowRef } from "vue";
import { VBtn } from "vuetify/components";
import { ICONS } from "../assets/icons.ts";
import { copyMarkdownBody, writeClipboard } from "../utils/answerCopy.ts";
import type { MacroMap } from "../utils/macros.ts";

const props = defineProps<{ body: string; macros: MacroMap }>();
const feedback = shallowRef("");
let timer: ReturnType<typeof setTimeout> | undefined;
let active = true;
onUnmounted(() => { active = false; clearTimeout(timer); });
async function copy(): Promise<void> {
  let message: string;
  try {
    await writeClipboard(copyMarkdownBody(props.body, props.macros));
    message = "已复制";
  } catch {
    message = "复制失败，请手动选取正文";
  }
  if (!active) return;
  feedback.value = message;
  clearTimeout(timer);
  timer = setTimeout(() => { feedback.value = ""; }, 2400);
}
</script>

<template>
  <VBtn icon variant="text" size="small" title="复制回答" aria-label="复制回答" @click="copy" v-html="ICONS.copy" />
  <span role="status" class="copy-feedback">{{ feedback }}</span>
</template>

<style scoped>
.copy-feedback { font-size: var(--md-type-label-small); color: var(--md-on-surface-variant); }
.copy-feedback:empty { position: absolute; }
</style>
