// settings-vue 桥：SettingsModal.vue 挂载（modal 由组件画，id 照旧）。
// main.js 只调 open/close；保存逻辑全在组件内。
import { createApp, h, ref } from "vue";
import SettingsModal from "./components/SettingsModal.vue";

const openState = ref(false);
let notifyFn = null;

export function mountSettings(handlers) {
  notifyFn = handlers.onNotify;
  const app = createApp({
    setup() {
      return { openState };
    },
    render() {
      return openState.value
        ? h(SettingsModal, {
            onClose: () => { openState.value = false; },
            onNotify: notifyFn,
          })
        : null;
    },
  });
  // 旧 #settings 元素还在 index 里：删掉，modal 由组件画（id=settings 照旧）。
  document.getElementById("settings")?.remove();
  const host = document.createElement("div");
  host.id = "settings-vue-host";
  document.getElementById("app").appendChild(host);
  app.mount(host);
  return {
    open() { openState.value = true; },
  };
}
