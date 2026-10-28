<script setup>
// 上下文工作区（2026-10-29 用户定）：一次会话分**两块**——「聊天」与「上下文」。
// 顶栏右端那颗两段开关切换（就落在绿环左边），聊天区的消息列与输入行整个让位给它。
//
// **现在里面是空的**（用户原话：「这个上下文工作区里面暂时不妨东西」）。
// 空着不等于什么都不画：一块全白的区域分不清"还没做"和"加载失败了"，
// 所以这里立一块牌子说清它是什么、将来装什么、现在为什么空。
// 等真往里面放东西（压缩金字塔的可视化、状态表、摘要时间线…）时，
// 把这个占位整块换掉即可 —— 外壳（滚动、留白、宽度）不用动。
defineProps({
  // hasSession：草稿态（还没建会话）时没有上下文可谈 —— 那句话要说，别让人以为坏了。
  hasSession: { type: Boolean, default: false },
});
</script>

<template>
  <section id="context-workspace" aria-label="上下文工作区">
    <div class="ws-inner">
      <div class="ws-empty">
        <p class="ws-empty-title">上下文工作区</p>
        <p class="ws-empty-note">
          这块地方留给<strong>上下文本身</strong>：压缩出来的前情提要、它们的层级关系、
          当前世界状态、这一发真发出去的载荷 —— 那些"构成上下文"的东西，
          而不是"对话本身"。
        </p>
        <p class="ws-empty-note ws-empty-todo">现在还没往里放东西。左边那颗开关随时切回「聊天」，对话不受影响。</p>
        <p v-if="!hasSession" class="ws-empty-note ws-empty-draft">
          这是新对话（草稿）：还没有任何上下文可看。
        </p>
      </div>
    </div>
  </section>
</template>
