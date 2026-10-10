import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

// microchat web：Vue 3 单文件组件。契约见 AGENTS.md API 表（只调 /api/v1）。
//
// 前端**监听地址的配置文件就是这儿**（改地址只改这一处；跑的时候直接 `./node_modules/.bin/vite`，
// **别走 `npm exec`** —— npm 会把 --port/--host 吃成它自己的配置，vite 收不到，踩过）。
// Docker 部署时这些由容器/compose 定，本文件按默认用即可。
export default defineConfig({
  plugins: [vue()],
  resolve: { alias: { "vue": "vue/dist/vue.esm-bundler.js" } },
  server: { host: "0.0.0.0", port: 8788, strictPort: true },
  preview: { host: "0.0.0.0", port: 8788, strictPort: true },
});
