<script setup lang="ts">
// 一条工具调用（服务端或本地）的卡片。正文时间线（按 text_offset）与旧版
// “全部工具卡在正文上方”共用这一个组件，避免两套 summary/来源列表慢慢漂移。
import { computed, shallowRef } from "vue";
import { nativeToolLabel, sourceLabel } from "../utils/format.ts";
import { issueLabel } from "../utils/answerTranscript.ts";
import type { MessageTool } from "../api/types.ts";

const props = withDefaults(defineProps<{
  tool: MessageTool;
  inline?: boolean;
}>(), { inline: false });

const name = computed(() => nativeToolLabel(props.tool.tool_name));
const sources = computed(() => props.tool.sources ?? []);
const issue = computed(() => issueLabel(props.tool.status));
const expanded = shallowRef(false);
function onToggle(event: Event): void {
  expanded.value = (event.currentTarget as HTMLDetailsElement).open;
}
</script>

<template>
  <details class="tool-card" :class="{ 'tool-card-inline': inline }" @toggle="onToggle">
    <summary>
      <span class="tool-card-name">{{ name }}</span>
      <span v-if="tool.source" class="tool-card-source">{{ tool.source === "client" ? "本地" : "服务端" }}</span>
      <span v-if="issue" class="tool-issue">{{ issue }}</span>
      <span v-if="tool.queries?.length" class="tool-card-query">{{ tool.queries.join(" / ") }}</span>
      <span v-if="sources.length" class="tool-card-count">{{ sources.length }} 条来源</span>
    </summary>
    <template v-if="expanded">
      <div v-if="tool.arguments" class="tool-field"><strong>参数</strong><pre>{{ tool.arguments }}</pre></div>
      <div v-if="tool.result" class="tool-field"><strong>结果</strong><pre>{{ tool.result }}</pre></div>
      <a v-for="source in sources" :key="source.url" :href="source.url" target="_blank" rel="noopener noreferrer">
        {{ sourceLabel(source.url, source.title) }}
      </a>
    </template>
  </details>
</template>

<style scoped>
.tool-issue { color: var(--md-error); }
.tool-field { margin: 0.4em 0 0.4em 1em; }
.tool-field pre { white-space: pre-wrap; overflow-wrap: anywhere; margin: 0.2em 0; }
</style>
