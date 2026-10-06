// sessionbar-vue 桥：SessionBar.vue 挂 #sessionbar-vue（aside 由组件画，id 照旧）。
import { createApp, h } from "vue";
import SessionBar from "./components/SessionBar.vue";

let barRef = null;

export function mountSessionBar(handlers) {
  const app = createApp({
    render() {
      return h(SessionBar, {
        ref: (el) => { barRef = el; },
        onSelect: handlers.onSelect,
        onClose: handlers.onClose,
        onNew: handlers.onNew,
        "onOpen-settings": handlers.onOpenSettings,
      });
    },
  });
  app.mount("#sessionbar-vue");
}

export const sessionBar = {
  setSessions(list, cur) { barRef?.setSessions(list, cur); },
};
