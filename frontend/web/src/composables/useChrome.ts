// 滚动隐藏上下栏：DOM 接线 + 两个 CSS 变量。
//
// 判据全在 utils/chrome.ts（纯函数）；这里只喂数字、写 `--chrome-top` / `--chrome-bottom`，
// CSS 只做 translateY ⇒ 不碰布局。只在触屏（pointer: coarse）生效。
import { resetChrome as resetChromeState, stepChrome } from "../utils/chrome.ts";
import type { ChromeState } from "../utils/chrome.ts";
import { chromeHideOn } from "../utils/prefs.ts";

const CHROME_JUMP = 200; // 一次跳这么多 ⇒ 不是手指，是切会话/贴底 ⇒ 重置，别把栏收走

export function useChrome() {
  let chrome: ChromeState = resetChromeState();
  const coarsePointer = window.matchMedia?.("(pointer: coarse)") ?? null;

  function applyChrome(): void {
    const root = document.documentElement.style;
    root.setProperty("--chrome-top", `${Math.round(chrome.top)}px`);
    root.setProperty("--chrome-bottom", `${Math.round(chrome.bottom)}px`);
  }

  function resetChrome(el?: HTMLElement | null): void {
    chrome = resetChromeState(el?.scrollTop ?? 0);
    applyChrome();
  }

  // 判据在 utils/chrome.ts（纯函数、契约测试钉着）；这里只喂数字 + 写两个 CSS 变量。
  function onMessagesScroll(): void {
    const el = document.querySelector<HTMLElement>("#messages-vue");
    if (!el) return;
    if (!chromeHideOn() || !coarsePointer?.matches) {
      if (chrome.top || chrome.bottom) resetChrome(el);
      else chrome.last = el.scrollTop;
      return;
    }
    const y = el.scrollTop;
    const dy = y - chrome.last;
    if (Math.abs(dy) > CHROME_JUMP) { resetChrome(el); return; }
    chrome = stepChrome(chrome, {
      dy,
      scrollTop: y,
      headerH: document.querySelector<HTMLElement>("#chat-col > header")?.offsetHeight ?? 40,
      composerH: document.querySelector<HTMLElement>("#composer")?.offsetHeight ?? 64,
    });
    applyChrome();
  }

  return { applyChrome, resetChrome, onMessagesScroll };
}
