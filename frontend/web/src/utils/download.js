// 下载一个 JSON 对象为文件（会话导出 / agent 卡带共用）。
// 走 Blob + 临时 <a>：API 要 Authorization 头 ⇒ 裸链接不行，必须先把内容取回内存。
export function downloadJSON(doc, name) {
  const blob = new Blob([JSON.stringify(doc, null, 2)], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = String(name ?? "download.json").replace(/[\\/:*?"<>|]/g, "_");
  a.click();
  URL.revokeObjectURL(url);
}
