import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
import { VitePWA } from "vite-plugin-pwa";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

// microchat web：Vue 3 单文件组件。契约见 AGENTS.md API 表（只调 /api/v1）。
//
// 前端**监听地址的配置文件就是这儿**（改地址只改这一处；跑的时候直接 `./node_modules/.bin/vite`，
// **别走 `npm exec`** —— npm 会把 --port/--host 吃成它自己的配置，vite 收不到，踩过）。
// Docker 部署时这些由容器/compose 定，本文件按默认用即可。
//
// ── 后端地址（唯一权威 = 后端自己的配置文件）────────────────────────────────
// 浏览器**永远同源**：/api 由这里代理给后端 —— 单容器部署（见根目录 docker-compose.yml）
// 与 dev 都走这条路，所以谁都不用在前端填地址。代理目标读的是后端自己的
// data/config/config.json（server.host/port；`0.0.0.0` 当连接地址换成 127.0.0.1）；
// 文件不在（首次启动、还没生成）⇒ 默认 127.0.0.1:8787。目录跟后端同一套环境变量可覆盖。
// 改后端端口 ⇒ 这个文件不用动，代理自动跟着变（重启前端生效）。
function backendOrigin() {
  const here = path.dirname(fileURLToPath(import.meta.url));
  const dataDir = process.env.MICROCHAT_DATA_DIR || path.resolve(here, "../../data");
  const configDir = process.env.MICROCHAT_CONFIG_DIR || path.join(dataDir, "config");
  try {
    const cfg = JSON.parse(fs.readFileSync(path.join(configDir, "config.json"), "utf8"));
    const host = cfg?.server?.host && cfg.server.host !== "0.0.0.0" ? cfg.server.host : "127.0.0.1";
    const port = Number(cfg?.server?.port) || 8787;
    return `http://${host}:${port}`;
  } catch {
    return "http://127.0.0.1:8787";
  }
}
const apiProxy = { "/api": { target: backendOrigin() } };

export default defineConfig({
  plugins: [
    vue(),
    // PWA（2026-10-11 用户点名）：可"安装为应用"。三件事 —— manifest / 图标 / 一个最小 SW。
    //   · SW **只预缓存壳**（HTML/JS/CSS/图标）：/api 一概不碰（数据必须现取；别写
    //     runtimeCaching 去接它，接了就是串数据）。
    //   · 安装的前提在**来源**：HTTPS 或 localhost。局域网 http://<IP>:8788 **装不了**
    //     （连 SW 都没有：不是安全上下文）—— 平板/内网要用得给前端上 TLS（反代终结），
    //     云端形态（Cloudflare 一类）天然满足。详见 README。
    //   · 只在 build/preview 生效（dev 不开 SW：免得开发时吃到旧缓存）。
    VitePWA({
      // **prompt**（2026-10-20 用户定，从 `autoUpdate` 改过来）—— 为什么改：
      // `autoUpdate` 会在新 SW `activated` 时**自己** `location.reload()`：看着"省事"，
      // 实则**静默**——你要是不巧正没看那个标签页，刷新就悄悄发生了；更糟的是它只保证
      // "最终会更新"，**不告诉你此刻看的是旧的**。2026-10-20 就因此白排查一轮：
      // 服务器上明明是新的（三份 bundle 逐字节相同），界面上却看不到新加的选项，
      // 最后靠"清站点数据"才发现是 SW 缓存的旧壳 —— README 里那句"再刷一次就是新的"
      // 说的正是它，但那句**救不了不知道自己该刷新的人**。
      // 现在：检测到新版本 ⇒ `onNeedRefresh` 弹一条 toast（「有新版本，点这里刷新」），
      // 由**用户点**了才 `updateServiceWorker()`（发 skipWaiting → controlling → reload）。
      // 于是"我看到的是哪一版"变成一件**说得出口**的事，而不是靠猜。
      registerType: "prompt",
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
  // dev 用 5173、preview 用 8788：**8788 是部署口**（docker 容器发布它）——两者分开，
  // 同一台机器上"dev 服务器 + 容器"才能并存（实测踩过：都用 8788 ⇒ 容器起不来，端口被自家 dev 占着）。
  server: { host: "0.0.0.0", port: 5173, strictPort: true, proxy: apiProxy },
  preview: { host: "0.0.0.0", port: 8788, strictPort: true, proxy: apiProxy },
});
