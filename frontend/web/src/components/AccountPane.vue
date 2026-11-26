<script setup lang="ts">
import { VBtn } from "vuetify/components";

const emit = defineEmits<{ back: [] }>();
</script>

<template>
  <section class="account-pane" aria-labelledby="account-title">
    <header>
      <VBtn variant="text" @click="emit('back')">返回对话</VBtn>
      <h2 id="account-title">账户</h2>
    </header>
    <p>MicroChat 当前为单用户自托管应用，不提供登录或账户凭据。</p>
  </section>
</template>

<style scoped>
.account-pane { flex: 1 1 auto; min-width: 0; width: min(100%, 52rem); margin: 0 auto; padding: clamp(1rem, 4vw, 2rem); color: var(--md-on-surface); }
.account-pane header { display: flex; align-items: center; gap: 1rem; }
.account-pane h2 { margin: 0; font-size: var(--md-type-title-large); font-weight: 500; }
.account-pane p { color: var(--md-on-surface-variant); }
</style>
