// 位置只使用会话行的稳定标识，不把可因折叠、压缩或重摇而变化的数组下标当作锚。
export type ReadingPosition =
  | { kind: "bottom" }
  | { kind: "row"; key: string; offset: number; top: number }
  | { kind: "offset"; top: number };

const STICK_TO_BOTTOM_PX = 48;

export function readingPaneVisible(el: HTMLElement | null): el is HTMLElement {
  return !!el && el.clientHeight > 0 && el.clientWidth > 0
    && el.checkVisibility?.({ visibilityProperty: true }) !== false;
}

export function atReadingTail(el: HTMLElement): boolean {
  return el.scrollHeight - el.clientHeight - el.scrollTop <= STICK_TO_BOTTOM_PX;
}

export function captureReadingPosition(el: HTMLElement | null): ReadingPosition | null {
  if (!readingPaneVisible(el)) return null;
  const top = el.scrollTop;
  if (atReadingTail(el)) return { kind: "bottom" };
  const viewport = el.getBoundingClientRect();
  for (const row of el.querySelectorAll<HTMLElement>("[data-reading-key]")) {
    const key = row.dataset.readingKey;
    const rect = row.getBoundingClientRect();
    if (key && rect.height > 0 && rect.bottom > viewport.top && rect.top < viewport.bottom) {
      return { kind: "row", key, offset: rect.top - viewport.top, top };
    }
  }
  return { kind: "offset", top };
}

export function applyReadingPosition(el: HTMLElement | null, position: ReadingPosition): void {
  if (!readingPaneVisible(el)) return;
  const max = Math.max(0, el.scrollHeight - el.clientHeight);
  if (position.kind === "bottom") { el.scrollTop = max; return; }
  let target = position.top;
  if (position.kind === "row") {
    for (const row of el.querySelectorAll<HTMLElement>("[data-reading-key]")) {
      if (row.dataset.readingKey !== position.key) continue;
      const rect = row.getBoundingClientRect();
      if (rect.height > 0) {
        target = el.scrollTop + rect.top - el.getBoundingClientRect().top - position.offset;
        break;
      }
    }
  }
  // 行已被删去、收进摘要或折叠 ⇒ 仍尽量保留原来的像素位置。
  el.scrollTop = Math.max(0, Math.min(target, max));
}
