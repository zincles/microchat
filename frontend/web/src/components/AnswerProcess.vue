<script setup lang="ts">
import { computed, shallowRef, useId } from "vue";
import { VBtn } from "vuetify/components";
import { ICONS } from "../assets/icons.ts";
import { answerSteps, processLabel, type AnswerGroup, type AnswerStep } from "../utils/answerTranscript.ts";

const props = defineProps<{ group: AnswerGroup; liveHasBody: boolean; rerollingId?: string | null }>();
defineSlots<{ default(step: AnswerStep): unknown }>();
const expanded = shallowRef(false);
const detailId = useId();
const label = computed(() => processLabel(props.group, props.rerollingId));
const action = computed(() => expanded.value ? "收起过程" : "查看过程");
const live = computed(() => {
  const last = props.group.rows[props.group.rows.length - 1].msg;
  return last.stream || (!!props.rerollingId && last.messageId === props.rerollingId);
});
const steps = computed(() => answerSteps(props.group, expanded.value, props.liveHasBody, props.rerollingId));
</script>

<template>
  <section class="answer-process" aria-label="一轮回答">
    <VBtn class="process-toggle" variant="text" size="small" :aria-label="`${label} · ${action}`" :aria-live="live ? 'polite' : undefined"
      :aria-expanded="expanded" :aria-controls="detailId" @click="expanded = !expanded">
      <span class="process-chevron" :class="{ expanded }" aria-hidden="true" v-html="ICONS.chevronRight"></span>
      <span>{{ label }}</span><span class="process-action"> · {{ action }}</span>
    </VBtn>
    <div :id="detailId" class="answer-steps">
      <template v-for="step in steps" :key="step.row.key"><slot v-bind="step" /></template>
    </div>
  </section>
</template>

<style scoped>
.answer-process { min-width: 0; }
.process-toggle { max-width: 100%; height: auto; min-height: 2.5em; padding-inline: 0.35em; color: var(--md-on-surface-variant); text-transform: none; letter-spacing: normal; }
.process-toggle :deep(.v-btn__content) { white-space: normal; flex-wrap: wrap; justify-content: flex-start; text-align: start; }
.process-chevron { display: inline-flex; align-self: center; }
.process-chevron.expanded { transform: rotate(90deg); }
.process-action { color: var(--md-primary); white-space: pre; }
.answer-steps { display: flex; flex-direction: column; gap: 0.5em; }
</style>
