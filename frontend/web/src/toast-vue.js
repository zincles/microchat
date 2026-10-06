// toast-vue 桥：把 Toast.vue 挂到 #toast-vue，旧 toast() 改调它。
// 旧 #toasts 盒子留着（空着不画），CSS 不动。
import { createApp } from "vue";
import Toast from "./components/Toast.vue";

const app = createApp(Toast);
const vm = app.mount("#toast-vue");

export function toastVue(text, kind) {
  vm.push(text, kind);
}
