import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
import { VitePWA } from "vite-plugin-pwa";

// microchat web：Vue 3 单文件组件。契约见 AGENTS.md API 表（只调 /api/v1）。
//
// 前端**监听地址的配置文件就是这儿**（改地址只改这一处；跑的时候直接 `./node_modules/.bin/vite`，
// **别走 `npm exec`** —— npm 会把 --port/--host 吃成它自己的配置，vite 收不到，踩过）。
// Docker 部署时这些由容器/compose 定，本文件按默认用即可。
export default defineConfig({
  plugins: [
    vue(),
    // PWA（2026-10-11 用户点名）：可"安装为应用"。三件事 —— manifest / 图标 / 一个最小 SW。
    //   · SW **只预缓存壳**（HTML/JS/CSS/图标）：/api 一概不碰（数据必须现取；且 API 在
    //     另一个来源（8787），SW 天生管不着 —— 别写 runtimeCaching 去接它，接了就是串数据）。
    //   · 安装的前提在**来源**：HTTPS 或 localhost。局域网 http://<IP>:8788 **装不了**
    //     （连 SW 都没有：不是安全上下文）—— 平板/内网要用得给前端上 TLS（反代终结），
    //     云端形态（Cloudflare 一类）天然满足。详见 README。
    //   · 只在 build/preview 生效（dev 不开 SW：免得开发时吃到旧缓存）。
    VitePWA({
      registerType: "autoUpdate",
      manifest: {
        name: "MicroChat",
        short_name: "MicroChat",
        description: "轻量 SillyTavern 替代（RPG 向）",
        lang: "zh-CN",
        start_url: ".",
        display: "standalone",
        background_color: "#0e1621",
        theme_color: "#0e1621",
        icons: [
          { src: "pwa-192.png", sizes: "192x192", type: "image/png" },
          { src: "pwa-512.png", sizes: "512x512", type: "image/png" },
          { src: "pwa-maskable-512.png", sizes: "512x512", type: "image/png", purpose: "maskable" },
        ],
      },
      workbox: {
        globPatterns: ["**/*.{js,css,html,svg,png,woff2,woff,ttf}"],
        navigateFallback: "index.html",
        cleanupOutdatedCaches: true,
      },
    }),
  ],
  resolve: { alias: { "vue": "vue/dist/vue.esm-bundler.js" } },
  server: { host: "0.0.0.0", port: 8788, strictPort: true },
  preview: { host: "0.0.0.0", port: 8788, strictPort: true },
});
