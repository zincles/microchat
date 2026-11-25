<script setup lang="ts">
// 设置目录首页：MD3 单列设置列表（2026-11-22 用户定「像 Android 平板的设置」）。
// 每行 = 图标 + 标题/说明 + 右箭头；八项依次排列，不做磁贴。
import { VBtn } from "vuetify/components";
import { ICONS } from "../assets/icons.ts";
import type { SettingsSection } from "../utils/format.ts";

defineProps<{ sections: SettingsSection[] }>();
const emit = defineEmits<{ select: [id: string] }>();

const chevron = ICONS.chevronRight;
function iconOf(id: string): string {
  return ICONS[id as keyof typeof ICONS] ?? ICONS.settings;
}
</script>

<template>
  <section id="settings-home" class="panel">
    <ul class="settings-list settings-home-list" aria-label="设置类别">
      <li v-for="s in sections" :key="s.id" class="settings-list-row">
        <VBtn type="button" variant="text" class="settings-list-entry settings-home-row" :aria-label="s.label" @click="emit('select', s.id)">
          <span class="settings-home-icon" aria-hidden="true" v-html="iconOf(s.icon)"></span>
          <span class="settings-list-copy">
            <strong>{{ s.label }}</strong>
            <span class="settings-list-note">{{ s.desc }}</span>
          </span>
          <span class="settings-list-chevron" aria-hidden="true" v-html="chevron"></span>
        </VBtn>
      </li>
    </ul>
  </section>
</template>
