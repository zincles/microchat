import { strict as assert } from "node:assert";
import { test } from "node:test";
import { macroMap, restoreMacros, stashMacros } from "../src/utils/macros.ts";
import { renderMarkdown } from "../src/utils/markdown.ts";

const allOn = () => true;

test("宏：已知宏换成 chip，悬停保留原文", () => {
  const { text, tokens } = stashMacros("你好 {{ user_name }}", macroMap("Alice", "旁白"));
  assert.equal(text, "你好 \u0000MACRO0\u0000");
  const html = restoreMacros(text, tokens);
  assert.match(html, /class="macro-chip"/);
  assert.match(html, /title="原始宏：\{\{user_name\}\}"/);
  assert.match(html, />Alice</);
});

test("宏：未知宏原样保留，不递归展开", () => {
  const { text, tokens } = stashMacros("{{getvar::x}} {{user_name}}", macroMap("{{user}}", "旁白"));
  assert.equal(text, "{{getvar::x}} \u0000MACRO0\u0000");
  assert.equal(tokens.length, 1);
  assert.equal(tokens[0].value, "{{user}}");
});

test("宏：值里的 HTML 会被转义", () => {
  const { text, tokens } = stashMacros("{{user_name}}", macroMap("<b>X</b>", "旁白"));
  const html = restoreMacros(text, tokens);
  assert.match(html, /&lt;b&gt;X&lt;\/b&gt;/);
  assert.ok(!html.includes("<b>X</b>"));
});

test("宏：markdown 渲染后仍是安全 chip", () => {
  const html = renderMarkdown("你好 **{{user_name}}**", {
    enabled: true,
    typeOn: allOn,
    macros: macroMap("A<B", "旁白"),
  });
  assert.match(html, /<strong>/);
  assert.match(html, /class="macro-chip"/);
  assert.match(html, /A&lt;B/);
});
