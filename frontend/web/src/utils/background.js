// 自定义背景图（设置 → 界面 tab）：URL 或本地上传的图片。
// 住 localStorage.mc_bg（JSON：{kind:"url"|"file", value, width?, height?}）；没设 = 纯平底。
// 图层是 body::before（main.css），这边只负责把它挂上去（--mc-bg-image）——
// URL 由**浏览器直接加载**（CSS 背景不受 CORS 限制）；上传的图片先压再存，不然 5MB 配额很快就满。
const KEY = "mc_bg";
const MAX_EDGE = 1920; // 长边上限（1920px JPEG 一般 300–700KB，塞得进 localStorage）
const QUALITY = 0.85;
const KEEP_AS_IS = 400 * 1024; // 本来就小而长边不超限：原样存（保 PNG 透明、GIF 首帧之类）

export function loadBackground() {
  try {
    const v = JSON.parse(localStorage.getItem(KEY) ?? "null");
    if (!v || (v.kind !== "url" && v.kind !== "file")) return null;
    if (typeof v.value !== "string" || !v.value) return null;
    return v;
  } catch {
    return null; // 落灰的坏值当没设（不是错）
  }
}

// 写库；配额满会把异常抛给调用方（那边降档重压后重试）。
export function saveBackground(bg) {
  if (!bg) localStorage.removeItem(KEY);
  else localStorage.setItem(KEY, JSON.stringify(bg));
}

// 挂/摘图层：值用 JSON.stringify 包成带引号的 CSS 字符串（URL 里的怪字符不至于把声明撕坏）。
export function applyBackground() {
  const bg = loadBackground();
  const el = document.documentElement;
  if (bg) el.style.setProperty("--mc-bg-image", `url(${JSON.stringify(bg.value)})`);
  else el.style.removeProperty("--mc-bg-image");
}

export function describeBackground(bg) {
  if (!bg) return "未设（纯平底）";
  if (bg.kind === "url") {
    const u = bg.value;
    return `URL：${u.length > 64 ? u.slice(0, 61) + "…" : u}`;
  }
  const kb = Math.round(bg.value.length / 1365); // dataURL ≈ 4/3 长度 ⇒ 回来要 ×3/4
  const dim = bg.width && bg.height ? ` ${bg.width}×${bg.height}` : "";
  return `已上传${dim}（≈${kb} KB）`;
}

// 图片文件 → 可存进 localStorage 的一条（必要时缩到长边 maxEdge 并存 JPEG）。
export async function fileToBackground(file, { maxEdge = MAX_EDGE, quality = QUALITY } = {}) {
  const direct = await new Promise((ok, no) => {
    const r = new FileReader();
    r.onload = () => ok(r.result);
    r.onerror = () => no(new Error("读文件失败"));
    r.readAsDataURL(file);
  });
  const img = await new Promise((ok, no) => {
    const i = new Image();
    i.onload = () => ok(i);
    i.onerror = () => no(new Error("这个文件不是浏览器认的图片"));
    i.src = direct;
  });
  if (!img.width || !img.height) throw new Error("认不出图片尺寸");
  const long = Math.max(img.width, img.height);
  if (long <= maxEdge && file.size <= KEEP_AS_IS) {
    return { kind: "file", value: direct, width: img.width, height: img.height };
  }
  const scale = Math.min(1, maxEdge / long);
  const w = Math.max(1, Math.round(img.width * scale));
  const h = Math.max(1, Math.round(img.height * scale));
  const c = document.createElement("canvas");
  c.width = w;
  c.height = h;
  c.getContext("2d").drawImage(img, 0, 0, w, h);
  return { kind: "file", value: c.toDataURL("image/jpeg", quality), width: w, height: h };
}
