// mount.js —— 桥共用：mount(Component, selector, props) 直挂占位，返回组件实例 ref。
// 七个 *-vue.js 桥只剩"调谁+传什么"，createApp/h boilerplate 只此一处。
import { createApp, h } from "vue";

export function mount(Component, selector, props = {}) {
  let inst = null;
  const app = createApp({
    render() {
      return h(Component, {
        ref: (el) => { inst = el; },
        ...props,
      });
    },
  });
  app.mount(selector);
  return {
    get() { return inst; },
    call(name, ...args) { return inst?.[name]?.(...args); },
  };
}
