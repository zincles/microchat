import { shallowReactive } from "vue";

export interface DraftReceipt {
  /** 保留空白的原输入，供未受理时还原。 */
  text: string;
  accept(): void;
  reject(): void;
}

/** 仅活在这一页。键是会话 ID，包括尚未建会话时预先分配的 ID。 */
export function createSessionDrafts() {
  const slots = shallowReactive(new Map<string, string | symbol>());

  function get(id: string | null): string {
    const value = id && slots.get(id);
    return typeof value === "string" ? value : "";
  }

  function set(id: string | null, text: string): void {
    if (!id) return;
    if (text) slots.set(id, text);
    else slots.delete(id);
  }

  function take(id: string): DraftReceipt {
    const text = get(id);
    // 每次提交占一个独立的空位。后续输入（包括又删空）、删除会话、再次提交都会释放它。
    const pending = Symbol();
    slots.set(id, pending);
    return {
      text,
      accept() { if (slots.get(id) === pending) slots.delete(id); },
      reject() { if (slots.get(id) === pending) set(id, text); },
    };
  }

  function forget(id: string): void { slots.delete(id); }
  return { get, set, take, forget };
}
