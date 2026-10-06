// topbar-vue 桥：TopBar.vue 挂 #topbar-vue（header 由组件画，id 照旧）。
// main.js 传 handlers（toggle 左右栏 / select-model / select-agent），回填经 ref 暴露。
import { createApp, h } from "vue";
import TopBar from "./components/TopBar.vue";

let barRef = null;

export function mountTopBar(handlers) {
  const app = createApp({
    render() {
      return h(TopBar, {
        ref: (el) => { barRef = el; },
        "onToggle-left": handlers.onToggleLeft,
        "onToggle-right": handlers.onToggleRight,
        "onSelect-model": handlers.onSelectModel,
        "onSelect-agent": handlers.onSelectAgent,
      });
    },
  });
  app.mount("#topbar-vue");
}

export const topBar = {
  setConn(t) { barRef?.setConn(t); },
  setSession(info) { barRef?.setSession(info); },
  setModels(groups, cur) { barRef?.setModels(groups, cur); },
  setAgents(list, cur) { barRef?.setAgents(list, cur); },
};
