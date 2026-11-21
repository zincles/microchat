<script setup lang="ts">
// 设置目录首页（2026-11-20 用户定「一步到位全量重排」）。
// 顶层不再是一排 tab —— 首页是一张目录卡；点进去才显示对应内容。
// 手机上不用再把六个 tab 挤进 390px，桌面也少一层“先猜哪个 tab”的负担。
import { ICONS } from "../assets/icons.ts";
import type { SettingsSection } from "../utils/format.ts";

defineProps<{ sections: SettingsSection[] }>();
const emit = defineEmits<{ select: [id: string] }>();

function iconOf(id: string): string {
  return ICONS[id as keyof typeof ICONS] ?? ICONS.settings;
}
</script>

<template>
  <section id="settings-home" class="panel">
    <p class="settings-lead">选一块要调的地方；每页只管自己的事，不再把所有设置摊在一条长页里。</p>
    <div class="settings-home-grid">
      <button
        v-for="s in sections"
        :key="s.id"
        type="button"
        class="settings-home-card"
        :aria-label="s.label"
        @click="emit('select', s.id)"
      >
        <span class="settings-home-icon" aria-hidden="true" v-html="iconOf(s.icon)"></span>
        <span class="settings-home-title">{{ s.label }}</span>
        <span class="settings-home-desc">{{ s.desc }}</span>
      </button>
    </div>
  </section>
</template>
