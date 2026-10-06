// session-settings-vue 桥：SessionSettings.vue 按需挂载（左栏条目 ☰ 打开，关即销毁）。
// renamed ⇒ 刷左栏 + 顶栏（当前会话）；deleted 不走这里（关闭走全屏确认，见 main.js onClose）。
import { createApp, h } from "vue";
import SessionSettings from "./components/SessionSettings.vue";

export function openSessionSettings(sessionId, handlers) {
  const host = document.createElement("div");
  host.id = "session-settings-host";
  document.getElementById("app").appendChild(host);
  const app = createApp({
    render() {
      return h(SessionSettings, {
        sessionId,
        onClose: () => { app.unmount(); host.remove(); },
        onNotify: handlers.onNotify,
        onRenamed: (info) => handlers.onRenamed(info),
      });
    },
  });
  app.mount(host);
}
