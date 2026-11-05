// microchat web 入口：挂 App.vue（接线全在 App 里）。契约见 AGENTS.md API 表。
import { createApp } from "vue";
import App from "./App.vue";
import "katex/dist/katex.min.css";
import { installRipple } from "./utils/ripple.ts";

createApp(App).mount("#app");
// MD3 波纹：事件委托装一次（新按钮/列表行自动生效，不需要逐个接线）。
installRipple();
