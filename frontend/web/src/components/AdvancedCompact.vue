<script setup>
// 高级压缩 / 修洞压缩（2026-11-05 用户定：上下文工作区第四、第五个 tab）。
//
// 两个 tab 共用这一个组件：`repair` 只是把请求里的 repair 打开。真正怎么切块、有没有洞、
// 能不能压，全由后端 plan 算；这里只负责输入参数、预览、运行与轮询整体状态。
//
// 一条硬口径：整体只有 running / done / failed / canceled；每块的 state 只是进度，
// 不是"这一块成功就算整体成功"。
import { ref, watch, onUnmounted } from "vue";
import { sharedApi } from "../utils/prefs.js";

const props = defineProps({
  sessionId: { type: String, default: "" },
  repair: { type: Boolean, default: false },
});
const emit = defineEmits(["done"]);
const api = sharedApi();

const begin = ref("");
const end = ref("");
const step = ref(1);
const concurrency = ref(2);
const attempts = ref(5);
const preview = ref(null);
const job = ref(null);
const error = ref("");
const busy = ref(false);
let pollTimer = null;
let disposed = false;
let lastDoneJob = "";

function requestBody() {
  const body = {
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

async function loadPreview() {
  if (!props.sessionId) return;
  busy.value = true;
  error.value = "";
  try {
    preview.value = await api.advancedCompactPreview(props.sessionId, requestBody());
    job.value = null;
  } catch (e) {
    preview.value = null;
    error.value = e.message;
  } finally {
    busy.value = false;
  }
}

async function start() {
  if (!props.sessionId || job.value?.state === "running") return;
  busy.value = true;
  error.value = "";
  try {
    job.value = await api.advancedCompact(props.sessionId, requestBody());
    lastDoneJob = "";
    poll();
  } catch (e) {
    error.value = e.message;
  } finally {
    busy.value = false;
  }
}

function poll() {
  clearTimeout(pollTimer);
  if (!job.value?.id || disposed) return;
  pollTimer = setTimeout(async () => {
    try {
      const next = await api.advancedCompactStatus(props.sessionId, job.value.id);
      if (disposed) return;
      job.value = next;
      if (next.state === "running") { poll(); return; }
      if (next.state === "done" && lastDoneJob !== next.id) {
        lastDoneJob = next.id;
        emit("done");
      }
    } catch (e) {
      if (!disposed) error.value = e.message;
    }
  }, 800);
}

async function cancel() {
  if (!job.value?.id || job.value.state !== "running") return;
  try {
    job.value = await api.advancedCompactCancel(props.sessionId, job.value.id);
    poll();
  } catch (e) { error.value = e.message; }
}

watch(() => props.sessionId, () => {
  clearTimeout(pollTimer);
  preview.value = null;
  job.value = null;
  error.value = "";
});
onUnmounted(() => { disposed = true; clearTimeout(pollTimer); });

const stateText = (state) => ({
  ready: "可压", pending: "排队", running: "生成中", done: "完成", failed: "失败", canceled: "取消",
}[state] ?? state);
</script>

<template>
  <div class="adv-pane">
    <div class="adv-config">
      <label class="field"><span>{{ repair ? "扫描范围（留空 = 全部）" : "消息区间（1-based）" }}</span>
        <span class="adv-range">
          <input v-model="begin" type="number" min="1" placeholder="begin" aria-label="begin_idx" />
          <span>–</span>
          <input v-model="end" type="number" min="1" placeholder="end" aria-label="end_idx" />
        </span>
      </label>
      <label class="field"><span>每块压几个 block</span><input v-model="step" type="number" min="1" max="64" /></label>
      <label class="field"><span>并发请求数</span><input v-model="concurrency" type="number" min="1" max="8" /></label>
      <label class="field"><span>单块最多尝试</span><input v-model="attempts" type="number" min="1" max="10" /></label>
      <div class="row">
        <button type="button" :disabled="busy" @click="loadPreview">{{ repair ? "扫描空洞" : "预览计划" }}</button>
        <button type="button" class="primary" :disabled="busy || !preview || !preview.total_requests" @click="start">开始</button>
        <button v-if="job?.state === 'running'" type="button" class="danger" @click="cancel">取消</button>
        <span class="status">{{ error }}</span>
      </div>
    </div>

    <p v-if="!props.sessionId" class="empty">（草稿态没有可压缩的会话）</p>
    <p v-else-if="!preview && !job" class="empty">{{ repair ? "点「扫描空洞」看看有没有需要补的区间" : "填区间后点「预览计划」；只会算，不会发请求" }}</p>

    <template v-if="preview">
      <div class="adv-summary">
        <span>共 {{ preview.total_requests }} 次请求</span>
        <span>{{ preview.total_blocks }} 块 / {{ preview.total_messages }} 条</span>
        <span>并发 {{ preview.concurrency }} · 单块最多 {{ preview.max_attempts }} 次</span>
      </div>
      <div v-if="preview.holes?.length" class="adv-holes">
        <b>压缩空洞</b>
        <span v-for="hole in preview.holes" :key="`${hole.from_idx}-${hole.to_idx}`" class="adv-hole">
          第 {{ hole.from_idx }}–{{ hole.to_idx }} 条
        </span>
        <p class="hint">{{ repair ? "这些区间会被修洞任务压缩。" : "存在空洞，不能直接跑高级压缩；先去「修洞压缩」补齐。" }}</p>
      </div>
      <p v-if="!preview.total_requests" class="empty">没有可压的 chunk{{ repair ? "（没有空洞）" : "" }}</p>
      <ol v-else class="adv-chunks">
        <li v-for="chunk in preview.chunks" :key="chunk.index" class="adv-chunk">
          <span class="adv-dot" />
          <span class="adv-idx">#{{ chunk.index + 1 }}</span>
          <span>第 {{ chunk.from_idx }}–{{ chunk.to_idx }} 条</span>
          <span class="adv-meta">{{ chunk.blocks }} 块 · {{ chunk.messages }} 条</span>
          <span class="adv-state">{{ stateText(chunk.state) }}</span>
        </li>
      </ol>
    </template>

    <template v-if="job">
      <div class="adv-summary">
        <b>作业 {{ job.state === "running" ? "进行中" : stateText(job.state) }}</b>
        <span>{{ job.chunks.filter((c) => c.state === "done").length }}/{{ job.chunks.length }} 块完成</span>
        <span v-if="job.error" class="adv-error">{{ job.error }}</span>
      </div>
      <ol class="adv-chunks">
        <li v-for="chunk in job.chunks" :key="chunk.index" class="adv-chunk">
          <span class="adv-dot" :class="chunk.state" />
          <span class="adv-idx">#{{ chunk.index + 1 }}</span>
          <span>第 {{ chunk.from_idx }}–{{ chunk.to_idx }} 条</span>
          <span class="adv-meta">{{ chunk.blocks }} 块 · {{ chunk.messages }} 条</span>
          <span class="adv-state">{{ stateText(chunk.state) }}<template v-if="chunk.attempts > 1"> ×{{ chunk.attempts }}</template></span>
        </li>
      </ol>
    </template>
  </div>
</template>
