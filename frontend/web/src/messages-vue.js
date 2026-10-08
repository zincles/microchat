// messages-vue 桥：Vue 消息列（挂 #messages-vue）。
// renderHistory/addMessage/pollTurn 的增量改调这里；流式更新走 ref 暴露。
import { createApp, ref, h, Fragment } from "vue";
import MessageBubble from "./components/MessageBubble.vue";

export const vueMessages = ref([]);
export const vueStream = ref({ id: null, text: "" });
export const vueDisplayMode = ref("chat");
const app = createApp({
  setup() {
    function onSave(mid, kind, value) {
      window.dispatchEvent(new CustomEvent("mc-edit-save", { detail: { mid, kind, value } }));
    }
    function onDelete(mid, isLast) {
      window.dispatchEvent(new CustomEvent("mc-delete", { detail: { mid, isLast } }));
    }
    function onRefresh() {
      window.dispatchEvent(new CustomEvent("mc-refresh"));
    }
    return { vueMessages, vueStream, vueDisplayMode, onSave, onDelete, onRefresh };
  },
  render(ctx) {
    const lastId = [...vueMessages.value].reverse().find((m) => m.messageId)?.messageId;
    // Fragment 裹多根（v-for 气泡列）：占位 #messages-vue 本体保留，无需 anchor。
    return h(
      Fragment,
      null,
      vueMessages.value.map((m) =>
        h(MessageBubble, {
          key: m.id ?? "stream",
          who: m.who,
          role: m.role,
          content: m.stream ? vueStream.value.text : m.content,
          reasoning: m.stream ? (vueStream.value.reasoning ?? m.reasoning) : m.reasoning,
          reasoningMs: m.reasoningMs,
          messageId: m.messageId,
          isLast: !!m.messageId && m.messageId === lastId,
          displayMode: vueDisplayMode.value,
          onSave: (...a) => ctx.onSave(...a),
          onDelete: (...a) => ctx.onDelete(...a),
          onRefresh: (...a) => ctx.onRefresh(...a),
        }),
      ),
    );
  },
});
app.mount("#messages-vue");
