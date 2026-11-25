import assert from "node:assert/strict";
import test from "node:test";
import { createSSRApp, defineComponent, effectScope, h } from "vue";
import { renderToString } from "@vue/server-renderer";
import { provideChatMenus, useChatMenu } from "../src/composables/useChatMenu.ts";

test("换选择器后，旧菜单的延迟关闭或卸载不会关掉当前菜单", async () => {
  const Pickers = defineComponent({ setup() {
    const toolbar = effectScope();
    const agent = toolbar.run(() => useChatMenu("agent"))!;
    const model = useChatMenu("model");
    agent.value = true;
    model.value = true;
    assert.equal(agent.value, false);
    agent.value = false;
    toolbar.stop();
    return () => h("section", model.value ? "选择模型" : "无菜单");
  } });
  const app = createSSRApp(defineComponent({ setup() {
    provideChatMenus();
    return () => h(Pickers);
  } }));
  assert.match(await renderToString(app), /选择模型/);
});
