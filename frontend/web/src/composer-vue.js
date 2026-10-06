// composer-vue 桥：Composer.vue 挂 #composer-vue（占位留着：删它撞 Vue scheduler）。
// main.js 传 handlers 进来（onInputText/onKeydown/onSubmit），组件事件直调它们。
import Composer from "./components/Composer.vue";
import { mount } from "./mount.js";

let inst = null;

export function mountComposer(handlers) {
  inst = mount(Composer, "#composer-vue", {
    "onInput-text": handlers.onInputText,
    onKeydown: handlers.onKeydown,
    onSubmit: handlers.onSubmit,
    onStop: handlers.onStop,
    onMenu: handlers.onMenu,
  });
}

export function composerClear() { inst?.call("clear"); }
export function composerSetStatus(t, over) { inst?.call("setStatus", t, over); }
export function composerSubmit() { inst?.call("submit"); }
export function composerSetRunning(v) { inst?.call("setRunning", v); }
