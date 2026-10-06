// composer-vue 桥：Composer.vue 挂载。
// main.js 传 handlers 进来（onInputText/onKeydown/onSubmit），组件事件直调它们。
import { createApp, h } from "vue";
import Composer from "./components/Composer.vue";

let composerRef = null;

export function mountComposer(handlers) {
  const app = createApp({
    render() {
      return h(Composer, {
        ref: (el) => { composerRef = el; },
        "onInput-text": handlers.onInputText,
        onKeydown: handlers.onKeydown,
        onSubmit: handlers.onSubmit,
      });
    },
  });
  // #composer-vue 只是占位：先在它后面建空 div 作挂载点，挂完把占位删掉。
  // 根 form 成为 chat-col 的直接子项 —— 与旧 form 同位，absolute/bottom 锚点不断。
  // 占位 #composer-vue 留着：删它会撞 Vue scheduler 的 insertBefore（HierarchyRequestError）。
  // 定位无碍：#composer 相对 #chat-col absolute，中间多一层 div 不影响 bottom 锚点。
  app.mount("#composer-vue");
}

export function composerClear() { composerRef?.clear(); }
export function composerSetStatus(t, over) { composerRef?.setStatus(t, over); }
export function composerSubmit() { composerRef?.submit(); }
