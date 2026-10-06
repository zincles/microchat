import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

// microchat web：Vue 3 单文件组件。契约见 AGENTS.md API 表（只调 /api/v1）。
export default defineConfig({
  plugins: [vue()],
  resolve: { alias: { "vue": "vue/dist/vue.esm-bundler.js" } },
  server: { port: 5173, strictPort: true, host: "127.0.0.1" },
});
