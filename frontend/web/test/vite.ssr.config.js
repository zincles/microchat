// SSR 冒烟专用的最小 vite 配置：只留 vue 插件与那条 `vue` 别名。
//
// 为什么不复用 vite.config.js：那儿挂着 PWA 插件（它要产物目录、要 HTML 入口），
// 在 `--ssr` 模式下会出来搅局；而这条冒烟只需要"把 SFC 编译成能 import 的 JS"。
// 别名必须与主配置一致 —— 不一致的话 SSR 跑的是另一份 vue，测出来的东西就不是线上那份。
import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

export default defineConfig({
  plugins: [vue()],
  // ⚠ 别名要用**精确匹配**（正则 `^vue$`）：字符串形式是**前缀**匹配，
  // 会把 `vue/server-renderer` 也改写成 `…esm-bundler.js/server-renderer`（那文件不存在）。
  // 主配置里没露这个坑，是因为线上那条链从来没人 import `vue/server-renderer`。
  resolve: {
    alias: [
      { find: /^vue$/, replacement: "vue/dist/vue.esm-bundler.js" },
      // `virtual:pwa-register` 是 vite-plugin-pwa 的**虚拟模块**（构建时才有）。
      // 这条冒烟不装 Service Worker ⇒ 换成 test/pwa-stub.js 里那份空实现。
      { find: /^virtual:pwa-register$/, replacement: "/home/zincles/microchat/frontend/web/test/pwa-stub.js" },
    ],
  },
    // ⚠ 产物必须落在**项目内**：SSR 默认把 `vue` / `@vue/server-renderer` 当 external，
  // 产物在 /tmp 里 Node 就解析不到它们（"Cannot find package 'vue'"）。
  // `node_modules/.cache` 已经在 gitignore 里，也不污染仓库。
  build: { ssr: true, outDir: "node_modules/.cache/mc-ssr", emptyOutDir: true, minify: false },
  logLevel: "warn",
});
