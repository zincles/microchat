<script setup lang="ts">
// 高级压缩 / 修洞压缩（2026-11-05 用户定：上下文工作区第四、第五个 tab）。
//
// 两个 tab 共用这一个组件：`repair` 只是把请求里的 repair 打开。真正怎么切块、有没有洞、
// 能不能压，全由后端 plan 算；这里只负责输入参数、预览、运行与轮询整体状态。
//
// 一条硬口径：整体只有 running / done / failed / canceled；每块的 state 只是进度，
// 不是"这一块成功就算整体成功"。
import { ref, computed, watch, onUnmounted } from "vue";
import { VBtn, VTextField } from "vuetify/components";
import { sharedApi } from "../utils/prefs.ts";
import type { AdvancedChunk, AdvancedJob, AdvancedPreview, AdvancedRequest } from "../api/types.ts";

const props = withDefaults(defineProps<{
  sessionId?: string;
  repair?: boolean;
}>(), {
  sessionId: "",
  repair: false,
});
const emit = defineEmits<{ done: [] }>();
const api = sharedApi();

const begin = ref("");
const end = ref("");
const step = ref(1);
const concurrency = ref(2);
const attempts = ref(5);
const preview = ref<AdvancedPreview | null>(null);
const job = ref<AdvancedJob | null>(null);
const error = ref("");
const busy = ref(false);
let pollTimer: ReturnType<typeof setTimeout> | null = null;
let disposed = false;
let lastDoneJob = "";
let pollFailures = 0;

function requestBody(): AdvancedRequest {
  const body: AdvancedRequest = {
    step_blocks: Number(step.value) || 1,
    concurrency: Number(concurrency.value) || 2,
    max_attempts: Number(attempts.value) || 5,
    repair: !!props.repair,
  };
  // 修洞允许空范围（= 扫全部）；高级压缩必须两端都给。
  if (String(begin.value).trim() !== "") body.begin_idx = Number(begin.value);
  if (String(end.value).trim() !== "") body.end_idx = Number(end.value);
  return body;
}

async function loadPreview(): Promise<void> {
  if (!props.sessionId) return;
  busy.value = true;
  error.value = "";
  try {
    preview.value = await api.advancedCompactPreview(props.sessionId, requestBody());
    job.value = null;
  } catch (e: unknown) {
    preview.value = null;
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}

async function start(): Promise<void> {
  if (!props.sessionId || job.value?.state === "running") return;
  busy.value = true;
  error.value = "";
  try {
    job.value = await api.advancedCompact(props.sessionId, requestBody());
    lastDoneJob = "";
    pollFailures = 0;
    poll();
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}

function poll(): void {
  clearTimeout(pollTimer ?? undefined);
  if (!job.value?.id || disposed) return;
  pollTimer = setTimeout(async () => {
    try {
      const next = await api.advancedCompactStatus(props.sessionId, job.value!.id);
      if (disposed) return;
      pollFailures = 0;
      job.value = next;
      if (next.state === "running") { poll(); return; }
      if (next.state === "done" && lastDoneJob !== next.id) {
        lastDoneJob = next.id;
        emit("done");
      }
    } catch (e: unknown) {
      if (disposed) return;
      // 单拍失败不判死刑：网络抖动/切后台时继续问；连续多次失败才停。
      pollFailures += 1;
      error.value = e instanceof Error ? e.message : String(e);
      if (pollFailures < 5) poll();
    }
  }, 800);
}

async function cancel(): Promise<void> {
  if (!job.value?.id || job.value.state !== "running") return;
  try {
    job.value = await api.advancedCompactCancel(props.sessionId, job.value.id);
    poll();
  } catch (e: unknown) { error.value = e instanceof Error ? e.message : String(e); }
}

watch(() => props.sessionId, () => {
  clearTimeout(pollTimer ?? undefined);
  preview.value = null;
  job.value = null;
  error.value = "";
});
onUnmounted(() => { disposed = true; clearTimeout(pollTimer ?? undefined); });

const doneChunks = computed(() => (job.value?.chunks ?? []).filter((c) => c.state === "done").length);
const failedChunks = computed(() => (job.value?.chunks ?? []).filter((c) => c.state === "failed").length);
const totalChunks = computed(() => job.value?.chunks?.length ?? 0);
const runningChunk = computed(() => (job.value?.chunks ?? []).find((c) => c.state === "running"));
const progressPct = computed(() => totalChunks.value ? Math.round((doneChunks.value / totalChunks.value) * 100) : 0);
const stateText = (state: AdvancedChunk["state"] | AdvancedJob["state"] | string): string => ({
  ready: "可压缩", pending: "等待中", running: "正在压缩", done: "已完成", failed: "失败", canceled: "已取消",
}[state] ?? state);
</script>

<template>
  <div class="adv-pane">
    <div class="adv-config">
      <label class="field"><span>{{ repair ? "检查范围（留空 = 整条对话）" : "消息范围（从第几条到第几条）" }}</span>
        <span class="adv-range">
          <VTextField v-model="begin" class="adv-input" type="number" min="1" placeholder="begin" aria-label="起始消息序号" density="compact" variant="outlined" hide-details />
          <span>–</span>
          <VTextField v-model="end" class="adv-input" type="number" min="1" placeholder="end" aria-label="结束消息序号" density="compact" variant="outlined" hide-details />
        </span>
      </label>
      <label class="field"><span>每次压缩的对话段数</span><VTextField v-model="step" class="adv-input" type="number" min="1" max="64" density="compact" variant="outlined" hide-details /></label>
      <label class="field"><span>同时处理数量</span><VTextField v-model="concurrency" class="adv-input" type="number" min="1" max="8" density="compact" variant="outlined" hide-details /></label>
      <label class="field"><span>每段最多尝试次数</span><VTextField v-model="attempts" class="adv-input" type="number" min="1" max="10" density="compact" variant="outlined" hide-details /></label>
      <div class="row">
        <VBtn variant="tonal" size="small" :disabled="busy" @click="loadPreview">{{ repair ? "检查待补区间" : "查看压缩方案" }}</VBtn>
        <VBtn color="primary" variant="flat" size="small" :disabled="busy || !preview || !preview.total_requests" @click="start">开始压缩</VBtn>
        <VBtn v-if="job?.state === 'running'" color="error" variant="text" size="small" @click="cancel">取消任务</VBtn>
        <span class="status">{{ error }}</span>
      </div>
    </div>

    <p v-if="!props.sessionId" class="empty">（尚未创建对话，暂时没有可压缩内容。）</p>
    <p v-else-if="!preview && !job" class="empty">{{ repair ? "点「检查待补区间」看看有哪些内容还没压缩" : "填写消息范围后点「查看压缩方案」；这一步不会发送请求" }}</p>

    <template v-if="preview">
      <div class="adv-summary">
        <span>共 {{ preview.total_requests }} 个分段</span>
        <span>{{ preview.total_blocks }} 个对话段 · {{ preview.total_messages }} 条消息</span>
        <span>同时处理 {{ preview.concurrency }} 段 · 每段最多尝试 {{ preview.max_attempts }} 次</span>
      </div>
      <div v-if="preview.holes?.length" class="adv-holes">
        <b>未覆盖区间</b>
        <span v-for="hole in preview.holes" :key="`${hole.from_idx}-${hole.to_idx}`" class="adv-hole">
          第 {{ hole.from_idx }}–{{ hole.to_idx }} 条
        </span>
        <p class="hint">{{ repair ? "这些区间会在补齐任务中压缩。" : "存在未覆盖区间，不能直接开始分段压缩；请先到「补齐空档」处理。" }}</p>
      </div>
      <p v-if="!preview.total_requests" class="empty">没有可压缩的内容{{ repair ? "（当前没有未覆盖区间）" : "" }}</p>
      <ol v-else class="adv-chunks">
        <li v-for="chunk in preview.chunks" :key="chunk.index" class="adv-chunk">
          <span class="adv-dot" />
          <span class="adv-idx">#{{ chunk.index + 1 }}</span>
          <span>第 {{ chunk.from_idx }}–{{ chunk.to_idx }} 条</span>
          <span class="adv-meta">{{ chunk.blocks }} 个对话段 · {{ chunk.messages }} 条消息</span>
          <span class="adv-state">{{ stateText(chunk.state) }}</span>
        </li>
      </ol>
    </template>

    <template v-if="job">
      <div class="adv-summary">
        <b>本次任务：{{ job.state === "running" ? "进行中" : stateText(job.state) }}</b>
        <span>已完成 {{ doneChunks }}/{{ totalChunks }} 段</span>
        <span v-if="runningChunk">正在处理第 {{ runningChunk.from_idx }}–{{ runningChunk.to_idx }} 条</span>
        <span v-if="failedChunks" class="adv-error">失败 {{ failedChunks }} 段</span>
        <span v-if="job.error" class="adv-error">{{ job.error }}</span>
      </div>
      <div class="adv-progress" :aria-label="`已完成 ${progressPct}%`"><i :style="{ width: progressPct + '%' }" /></div>
      <ol class="adv-chunks">
        <li v-for="chunk in job.chunks" :key="chunk.index" class="adv-chunk">
          <span class="adv-dot" :class="chunk.state" />
          <span class="adv-idx">#{{ chunk.index + 1 }}</span>
          <span>第 {{ chunk.from_idx }}–{{ chunk.to_idx }} 条</span>
          <span class="adv-meta">{{ chunk.blocks }} 个对话段 · {{ chunk.messages }} 条消息</span>
          <span class="adv-state">{{ stateText(chunk.state) }}<template v-if="chunk.attempts > 1"> ×{{ chunk.attempts }}</template></span>
        </li>
      </ol>
    </template>
  </div>
</template>
