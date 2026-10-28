<script setup>
// 上下文工作区（2026-10-29 用户定）：一次会话分**两块**——「聊天」与「上下文」。
// 顶栏右端那颗两段开关切换（就落在绿环左边），聊天区的消息列与输入行整个让位给它。
//
// 2026-10-29 第二轮（用户：「要不我们把右侧栏那个上下文给挪过来？」）：**右侧载荷栏整块搬进来了**。
// 那一栏从前的三 tab（载荷 / 状态 / 压缩）现在住在这儿，一个不少；栏本身连同它的 [◨] 开关、
// 窄屏抽屉、`mc_bar_right_hidden` 偏好一起删掉 —— 别再长回来（契约测试钉着）。
//
// 为什么搬：那三条 tab 讲的全是"上下文由什么构成"（真发出去的请求、世界状态的现演、
// 压缩金字塔的体积账），而右侧那根 340px 的窄栏本来就是它们的临时住处。工作区一开，
// 它们有了正经地方，也就不必再挤在栏里和对话抢宽度。
import { ref } from "vue";
import PayloadBar from "./PayloadBar.vue";

const props = defineProps({
  // hasSession：草稿态（还没建会话）时没有上下文可谈 —— 那句话要说，别让人以为坏了。
  hasSession: { type: Boolean, default: false },
  // defaultBlocks：chat.compact_blocks（压缩面板空输入框的占位提示；原样转给面板）。
  defaultBlocks: { type: Number, default: null },
});
// tab / compact / compact-payload：面板那三件事照旧往上报 ——
// 压缩树要"看着才拉全量"（App 的 treeVisible），压缩与预演也得让 App 去发请求。
const emit = defineEmits(["tab", "compact", "compact-payload"]);

// inner：真正的面板。四个 setter 由 App 通过本组件的 expose 一路打到它身上
// （载荷 / 状态表 / 压缩树 / 压缩那一发的载荷）。
const inner = ref(null);

defineExpose({
  setWire: (w) => inner.value?.setWire(w),
  setTables: (t) => inner.value?.setTables(t),
  setTree: (t) => inner.value?.setTree(t),
  setCompactWire: (w) => inner.value?.setCompactWire(w),
});
</script>

<template>
  <section id="context-workspace" aria-label="上下文工作区">
    <!-- 草稿态：还没会话 ⇒ 面板里全是空话。明说一句，别让人以为面板坏了。
         （不给"面板照样渲染"的选择：那三条 tab 在草稿里确实一个字都没有。） -->
    <p v-if="!props.hasSession" class="ws-draft-note">
      这是新对话（草稿）：发第一句之后，这里会有这一发真发出去的请求、当前世界状态、压缩金字塔。
    </p>
    <PayloadBar
      ref="inner"
      :default-blocks="props.defaultBlocks"
      @tab="(t) => emit('tab', t)"
      @compact="(n) => emit('compact', n)"
      @compact-payload="(n) => emit('compact-payload', n)"
    />
  </section>
</template>
