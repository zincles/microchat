// 运行时冒烟：把 App.vue 真的**跑一遍**（SSR 渲染），抓"构建与契约测试都看不见"的错。
//
// 为什么要有这条：2026-10-29 我把右栏删掉时，顺手删了 `mqlRight` 的声明，却漏了底部
// `onMounted`/`onUnmounted` 里两处 `mqlRight?.addEventListener(...)` —— 构建通过、94 条契约
// 全绿，浏览器一打开就 `ReferenceError: mqlRight is not defined`，整页白。
// JS 里"用了没声明的名字"是**运行时**才报的，静态检查和字面匹配都抓不住。
//
// ⚠ **覆盖范围要说清**（否则就是给自己假信心）：
//   ✅ 模块顶层 + 组件 `setup()` 里抛的错（含 import 期、响应式初始化、computed 首次求值）
//   ❌ `onMounted` / `onUnmounted` / `watch` 回调里的错 —— **SSR 不执行挂载钩子**
//      （踩到的正是这一档：`mqlRight` 在 onMounted 里）。那一档由契约测试里
//      "删掉的声明不许再被引用"那张**名字清单**兜着（见 contract.test.js）。
// 所以两者是互补的，别删其中任何一个。
import { execFileSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import assert from "node:assert/strict";

const here = dirname(fileURLToPath(import.meta.url));
const root = join(here, "..");
const viteBin = join(root, "node_modules", "vite", "bin", "vite.js");
const outFile = join(root, "node_modules", ".cache", "mc-ssr", "ssr-entry.js");

test("App 能真的跑起来（SSR 冒烟，抓 setup 期的 ReferenceError）", { skip: !existsSync(viteBin) && "没装 node_modules" }, async () => {
  // 1) 用最小 SSR 配置把 App 编译成能 import 的 JS（配置里换掉了 PWA 的虚拟模块）
  //
  // ⚠ 构建失败时**把 vite 的输出一起抛出来**：`stdio: "pipe"` 会把它吞掉，
  // 于是测试只报一句"构建失败"，人得自己再手跑一遍才知道为什么 —— 那等于没有报错信息。
  try {
    execFileSync(process.execPath, [viteBin, "build", "--config", "test/vite.ssr.config.js", "--ssr", "test/ssr-entry.js"], {
      cwd: root, stdio: "pipe",
    });
  } catch (e) {
    const out = [e.stdout, e.stderr].filter(Boolean).map((b) => b.toString()).join("\n");
    assert.fail(`SSR 构建失败（多半是 import 链坏了 / 少了模块）：\n${out.slice(-2000)}`);
  }

  // 2) 真的渲染一遍 —— 这里抛什么就是浏览器里会抛什么（除了挂载钩子，见文件头）
  const mod = await import(`file://${outFile}?t=${Date.now()}`);
  const html = await mod.run();
  assert.ok(typeof html === "string" && html.length > 0, "渲染出来是空的 —— setup 没跑完");
});
