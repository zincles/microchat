<script setup lang="ts">
import { computed } from "vue";
import type { ChatRow, ChatRowMessage, ChatRowSummary } from "../utils/chatrows.ts";
import { transcriptItems, type BubblePresentation } from "../utils/answerTranscript.ts";
import AnswerProcess from "./AnswerProcess.vue";

const props = defineProps<{ rows: ChatRow[]; liveText: string; windowFrom: number; rerollingId?: string | null }>();
defineSlots<{
  summary(props: { row: ChatRowSummary; readingKey: string }): unknown;
  message(props: { row: ChatRowMessage; presentation: BubblePresentation; readingKey?: string }): unknown;
}>();
const items = computed(() => transcriptItems(props.rows, props.windowFrom, props.rerollingId));
const liveHasBody = computed(() => !!props.liveText.trim());
</script>

<template>
  <template v-for="item in items" :key="item.key">
    <AnswerProcess v-if="item.kind === 'answer'" :group="item" :live-has-body="liveHasBody" :rerolling-id="rerollingId"
      :data-reading-key="item.rows[0].msg.messageId || item.rows[0].key">
      <template #default="step"><slot name="message" v-bind="step" /></template>
    </AnswerProcess>
    <slot v-else-if="item.kind === 'summary'" name="summary" :row="item" :reading-key="item.key" />
    <slot v-else name="message" :row="item" :reading-key="item.msg.messageId || item.key"
      :presentation="{ copyAnswer: true, candidate: !!rerollingId && rerollingId === item.msg.messageId }" />
  </template>
</template>
