// payloadbar-vue 桥：PayloadBar.vue 挂 #payloadbar-vue（aside 由组件画，id 照旧）。
import { createApp, h } from "vue";
import PayloadBar from "./components/PayloadBar.vue";

let barRef = null;

export function mountPayloadBar() {
  const app = createApp({
    render() {
      return h(PayloadBar, { ref: (el) => { barRef = el; } });
    },
  });
  app.mount("#payloadbar-vue");
}

export const payloadBar = {
  setWire(w) { barRef?.setWire(w); },
};
