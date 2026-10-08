// microchat web 入口：挂 App.vue（接线全在 App 里）。契约见 AGENTS.md API 表。
import { createApp } from "vue";
import App from "./App.vue";
import "katex/dist/katex.min.css";

createApp(App).mount("#app");
