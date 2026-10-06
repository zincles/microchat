import PayloadBar from "./components/PayloadBar.vue";
import { mount } from "./mount.js";

let inst = null;

export function mountPayloadBar() {
  inst = mount(PayloadBar, "#payloadbar-vue");
}

export const payloadBar = {
  setWire(w) { inst?.call("setWire", w); },
  setTables(t) { inst?.call("setTables", t); },
};
