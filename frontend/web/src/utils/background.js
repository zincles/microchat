// 自定义背景图（设置 → 外观）。
//
// 2026-11-01：随「所有设置均存储在服务器上」一起搬走 —— 从前是一枚 data URL 躺在
// `localStorage.mc_bg`（一张压过的 JPEG 也能到 700KB，5MB 配额说满就满）。
// 现在：
//   - 元信息（`kind` / `value` / `version`）在服务器的 `prefs.json` 里，归 `syncPrefs` 管；
//   - 上传的那张图落 `data/prefs/background.bin`，走 `PUT/GET/DELETE /config/prefs/background`。
// 于是提供了一个额外的好处：**换设备也在**（从前换个浏览器就没了）。
//
// 本文件只负责三件事：① 把图压到能上"服务器"的大小；② 上传/删除；③ 把它挂到 CSS 上。
import { sharedApi, prefsSnapshot } from "./prefs.js";

const MAX_EDGE = 1920; // 长边上限（1920px JPEG 一般 300–700KB —— 服务器的上限是 4MB，够）
const QUALITY = 0.85;
const KEEP_AS_IS = 400 * 1024; // 本来就小而长边不超限：原样传（保 PNG 透明、GIF 首帧之类）

// currentBackground：当前的背景设置（`{}` = 没设）。
export function currentBackground() {
  const bg = prefsSnapshot().background ?? {};
  if (bg.kind !== "url" && bg.kind !== "file") return null;
  return bg;
}

// backgroundCssUrl：这条背景该用的**真实地址**。
//
// `url` 档就是用户填的地址；`file` 档拼到我们自己的端点上，并带 `v=` 破缓存
// —— 图换了但路径不变，只有版本号能让浏览器重取（见后端 `getBackground` 的 ETag 那段）。
export function backgroundCssUrl(bg) {
  if (!bg) return "";
  if (bg.kind === "url") return bg.value;
  // 只认 file：坏 kind（既不 url 也不 file）当没设 —— 别拼出一个指向不知道哪儿的地址。
  if (bg.kind === "file") return sharedApi().backgroundUrl(bg.version);
  return "";
}

// 挂/摘图层：值用 JSON.stringify 包成带引号的 CSS 字符串（URL 里的怪字符不至于把声明撕坏）。
export function applyBackground() {
  const url = backgroundCssUrl(currentBackground());
  const el = document.documentElement;
  if (url) el.style.setProperty("--mc-bg-image", `url(${JSON.stringify(url)})`);
  else el.style.removeProperty("--mc-bg-image");
}

export function describeBackground(bg) {
  if (!bg) return "未设置（纯色背景）";
  if (bg.kind === "url") {
    const u = bg.value ?? "";
    return `图片地址：${u.length > 64 ? u.slice(0, 61) + "…" : u}`;
  }
  const dim = bg.width && bg.height ? `${bg.width}×${bg.height}` : "已上传";
  return `已上传 ${dim}`;
}

// fileToBlob：图片文件 → 压好的 Blob（必要时缩到长边 maxEdge、转 JPEG）。
//
// 为什么要压：服务器上限 4MB，而手机拍的照片动辄 5–8MB；压过之后一般 300–700KB。
// 为什么返回 Blob 而不是 data URL：现在它是**要传上去的字节流**，不是要塞进 localStorage 的字符串。
export async function fileToBlob(file, { maxEdge = MAX_EDGE, quality = QUALITY } = {}) {
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
    // 原样传：保住 PNG 透明 / 动图首帧这些"压一下就没了"的性质。
    return { blob: file, width: img.width, height: img.height };
  }
  const scale = Math.min(1, maxEdge / long);
  const w = Math.max(1, Math.round(img.width * scale));
  const h = Math.max(1, Math.round(img.height * scale));
  const canvas = document.createElement("canvas");
  canvas.width = w;
  canvas.height = h;
  canvas.getContext("2d").drawImage(img, 0, 0, w, h);
  const blob = await new Promise((ok, no) => {
    canvas.toBlob((b) => (b ? ok(b) : no(new Error("压缩失败"))), "image/jpeg", quality);
  });
  return { blob, width: w, height: h };
}

// uploadBackground：压好传上去（后端回登记好的 `Background`；前端随后要**重新同步偏好**才能拿到它）。
export async function uploadBackground(file) {
  const { blob } = await fileToBlob(file);
  return sharedApi().uploadBackground(blob);
}

// clearBackground：删掉服务器上那张（`url` 档也走这里 —— 后端只删文件、清元信息）。
export async function clearBackground() {
  return sharedApi().deleteBackground();
}
