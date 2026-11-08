// 侧栏 / 工作区 / 滑动抽屉。
//
// 2026-10-10 移动端：同一对按钮两种身份 —— 宽屏是布局里的栏（collapsed = 收起），
// 窄屏是覆盖式抽屉（collapsed = 关着）。窄屏一律默认关、不读不写偏好。
//
// 2026-10-29：右侧载荷栏搬进上下文工作区 ⇒ 只剩左抽屉；`pickDrag` 仍保留通用形状，
// 但输入永远是"没有右抽屉、也没开着"。
//
// 手势判定全在 utils/swipe.ts（纯函数）；这里只碰 DOM：跟手期间写内联 transform
// （transition 先掐掉，并用 watchDrag 守住 —— Vuetify 每次重渲染都会把内联 transform
// 写回 translateX(0px)，不守就会被覆写成一帧"完全打开"），松手清内联、按结果落
// collapsed 类 —— CSS 的 200ms 过渡完成吸附。
import { ref } from "vue";
import { barLeftHidden, saveBarLeftHidden } from "../utils/prefs.ts";
import { classifyAxis, pickDrag, dragProgress, resolveRelease } from "../utils/swipe.ts";
import type { Axis, Drag } from "../utils/swipe.ts";

const BP = { leftDrawer: 700 };

interface SidePanelOptions {
  isOverlayOpen: () => boolean;
  onWorkspaceContext: () => void;
}

interface PanelDrag {
  x0: number;
  y0: number;
  axis: Axis | null;
  pick: Drag | null;
  el: HTMLElement | null;
  scrim: HTMLElement | null;
  w: number;
  p: number;
  lastT: number;
  v: number;
  tf: string;                      // 跟手期间要钉住的内联 transform（百分比串）
  watch: MutationObserver | null;  // 盯住它不被重渲染覆写（见 watchDrag）
}

export function useSidePanels({ isOverlayOpen, onWorkspaceContext }: SidePanelOptions) {
  // leftHidden：左栏收没收的**响应式镜像**（真实状态仍是 DOM 类 + 服务器偏好）。
  const leftHidden = ref(false);
  const workspace = ref<"chat" | "context">("chat");
  const mqlLeft = window.matchMedia?.(`(max-width: ${BP.leftDrawer}px)`) ?? null;
  let panelDrag: PanelDrag | null = null;

  function narrowLeft(): boolean { return !!mqlLeft?.matches; }

  function applySideToggle(barID: string, hidden: boolean): void {
    document.getElementById(barID)?.classList.toggle("collapsed", hidden);
    if (barID === "sessions-bar") leftHidden.value = hidden;
  }

  function applySideStates(): void {
    applySideToggle("sessions-bar", narrowLeft() ? true : barLeftHidden());
  }

  function toggleLeft(): void {
    const bar = document.getElementById("sessions-bar");
    const hidden = !bar?.classList.contains("collapsed");
    if (!narrowLeft()) saveBarLeftHidden(hidden);
    applySideToggle("sessions-bar", hidden);
  }

  // 窄屏里"人去了别处"就得把左抽屉收回去（选会话 / 开新对话）。
  function closeLeftDrawer(): void { if (narrowLeft()) applySideToggle("sessions-bar", true); }

  // 工作区（2026-10-29）：**不存 localStorage**（这是"这一下要看哪块"，不是外观偏好）。
  // 换会话也不重置：人正在对照两份上下文时，切会话不该把他弹出工作区。
  function selectWorkspace(next: string): void {
    workspace.value = next === "context" ? "context" : "chat";
    if (workspace.value === "context") onWorkspaceContext();
  }

  function swipeBlocked(target: EventTarget | null): boolean {
    if (isOverlayOpen()) return true;
    if (document.querySelector("#settings-view") || document.querySelector(".modal")) return true;
    // 抽屉自己身上 ⇒ 一律接管（pre 也不例外）："把抽屉拖走"在这儿就该能用。
    const el = target as Element | null;
    if (el?.closest?.("#sessions-bar")) return false;
    return !!el?.closest?.("textarea, input, select, pre, .palette, .picker, .modal, #settings-view");
  }

  function paintDrag(d: PanelDrag, p: number): void {
    if (!d.el) return;
    const off = d.pick?.bar === "left" ? -102 : 102; // 与 CSS 的 collapsed 位移一致
    d.tf = `translateX(${off * (1 - p)}%)`;
    d.el.style.transform = d.tf;
    if (d.scrim) d.scrim.style.opacity = String(p);
  }

  // watchDrag：跟手期间**守住**内联 transform。
  //
  // VNavigationDrawer 每次重渲染都会把根元素的 `style.transform` 写回
  // `translateX(0px)`（layoutItemStyles 的"开着"位置）。SessionBar 的任意一次重渲染
  // 都会把 VNavigationDrawer 一起重渲染 —— 包括 applySideToggle 里那一次
  // `leftHidden.value = false`（拖动开始必然要改它，否则类绑定会把 collapsed 又补回来）。
  // 不管住的话：第一枚 touchmove 先写好跟手位移 → Vue 微任务把它覆写成完全打开 →
  // 这一帧画出来就是"抽屉整块闪开"，下一枚 move 才补回位移（真机上的闪一下）。
  //
  // MutationObserver 回调在渲染前跑：被覆写就立刻写回，不会有可见帧。
  // 自己写回触发的那轮记录比对后不再写，不会成环。
  function watchDrag(d: PanelDrag): void {
    if (!d.el || d.watch) return;
    const mo = new MutationObserver(() => {
      if (panelDrag !== d || !d.el) return;
      if (d.el.style.transform !== d.tf) d.el.style.transform = d.tf;
    });
    d.watch = mo;
    mo.observe(d.el, { attributes: true, attributeFilter: ["style"] });
  }

  function onTouchStart(e: TouchEvent): void {
    // 上一手势若没走 settle（异常路径）也要把看守摘掉，别让观察器挂着不放。
    panelDrag?.watch?.disconnect();
    panelDrag = null;
    if (e.touches.length !== 1 || swipeBlocked(e.target)) return;
    const t = e.touches[0];
    panelDrag = {
      x0: t.clientX, y0: t.clientY, axis: null, pick: null,
      el: null, scrim: null, w: 0, p: 0, lastT: Date.now(), v: 0,
      tf: "", watch: null,
    };
  }

  function onTouchMove(e: TouchEvent): void {
    const d = panelDrag;
    if (!d || e.touches.length !== 1) return;
    const t = e.touches[0];
    const dx = t.clientX - d.x0;
    const dy = t.clientY - d.y0;
    if (!d.axis) {
      const axis = classifyAxis(dx, dy);
      if (!axis) return;
      if (axis === "v") { panelDrag = null; return; } // 归浏览器滚列表
      const left = document.getElementById("sessions-bar");
      const pick = pickDrag({
        dx,
        leftDrawer: narrowLeft(),
        // 右栏已经没了 ⇒ 永远"没有右抽屉、也没开着"。
        rightDrawer: false,
        leftOpen: !!left && !left.classList.contains("collapsed"),
        rightOpen: false,
      });
      if (!pick) { panelDrag = null; return; }
      d.axis = axis; // ⚠ 必须记住：不然每次 touchmove 都重进这里
      d.pick = pick;
      d.p = pick.base; // 起点开度（开=0 / 关=1）；别动 d.lastT（第一枚 move 的 dt≈0）
      d.el = left;
      d.scrim = document.getElementById("scrim-left");
      d.w = d.el?.getBoundingClientRect().width || 300;
      if (d.el) d.el.style.transition = "none";
      if (d.scrim) d.scrim.style.transition = "none";
      watchDrag(d); // 先盯住：下面的 applySideToggle 会触发重渲染，会覆写内联 transform
      applySideToggle("sessions-bar", false); // 拖起来先亮出来，位置全由内联管（下面马上写）
    }
    e.preventDefault(); // 这手势归我们了：别让页面跟着滚
    if (!d.pick) return;
    const p = dragProgress(d.pick.base, dx, d.w, d.pick.bar);
    const now = Date.now();
    const dt = now - d.lastT;
    if (dt > 0) d.v = (p - d.p) / dt; // 开度速度（朝开为正）
    d.lastT = now;
    d.p = p;
    paintDrag(d, p);
  }

  function settleDrag(useVelocity: boolean): void {
    const d = panelDrag;
    panelDrag = null;
    if (!d || !d.axis || !d.pick || !d.el) return;
    // 先摘看守再清内联：否则"清空 transform"那一下会被观察器当成覆写、又写回去。
    d.watch?.disconnect();
    d.watch = null;
    // 速度样本要有**保鲜期**：甩完停住手再松（>120ms 没有新 move）就按位置吸附。
    const fresh = Date.now() - d.lastT <= 120;
    const open = resolveRelease(d.p, useVelocity && fresh ? d.v : 0) === "open";
    d.el.style.transition = ""; // 先还 CSS 过渡，再清内联 —— 吸附动画由它完成
    d.el.style.transform = "";
    if (d.scrim) { d.scrim.style.transition = ""; d.scrim.style.opacity = ""; }
    applySideToggle("sessions-bar", !open);
  }

  function onTouchEnd(): void { settleDrag(true); }
  function onTouchCancel(): void { settleDrag(false); }

  function install(): void {
    applySideStates();
    document.addEventListener("touchstart", onTouchStart, { passive: true });
    document.addEventListener("touchmove", onTouchMove, { passive: false });
    document.addEventListener("touchend", onTouchEnd, { passive: true });
    document.addEventListener("touchcancel", onTouchCancel, { passive: true });
    mqlLeft?.addEventListener?.("change", applySideStates);
  }
  function uninstall(): void {
    panelDrag?.watch?.disconnect();
    panelDrag = null;
    document.removeEventListener("touchstart", onTouchStart);
    document.removeEventListener("touchmove", onTouchMove);
    document.removeEventListener("touchend", onTouchEnd);
    document.removeEventListener("touchcancel", onTouchCancel);
    mqlLeft?.removeEventListener?.("change", applySideStates);
  }

  return { leftHidden, workspace, selectWorkspace, applySideToggle, toggleLeft, closeLeftDrawer, install, uninstall };
}
