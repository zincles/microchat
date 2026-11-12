import { strict as assert } from "node:assert";
import { test } from "node:test";
import {
  isJsonContainer,
  jsonEntries,
  jsonKind,
  jsonPreview,
  parseJsonText,
} from "../src/utils/json.ts";

test("JSON 阅读器：解析容忍 BOM，坏文件带原因", () => {
  assert.deepEqual(parseJsonText("\uFEFF{\"a\":1}"), { a: 1 });
  assert.equal(parseJsonText("null"), null);
  assert.throws(() => parseJsonText("{\"a\":}"), /JSON 解析失败/);
});

test("JSON 阅读器：数组按索引、对象按原顺序给子项", () => {
  assert.deepEqual(jsonEntries([10, 20]), [
    { key: "0", value: 10, index: true },
    { key: "1", value: 20, index: true },
  ]);
  assert.deepEqual(jsonEntries({ b: 2, a: 1 }), [
    { key: "b", value: 2, index: false },
    { key: "a", value: 1, index: false },
  ]);
  assert.deepEqual(jsonEntries("标量"), []);
});

test("JSON 阅读器：类型与单行预览", () => {
  assert.equal(jsonKind(null), "null");
  assert.equal(jsonKind([]), "array");
  assert.equal(jsonKind({}), "object");
  assert.equal(jsonKind("x"), "string");
  assert.equal(jsonKind(3), "number");
  assert.equal(jsonKind(false), "boolean");
  assert.equal(isJsonContainer(null), false);
  assert.equal(isJsonContainer([]), true);

  assert.equal(jsonPreview("a\nb"), "\"a\\nb\"");
  assert.equal(jsonPreview([1, 2, 3]), "Array(3)");
  assert.equal(jsonPreview({ a: 1, b: 2 }), "Object(2)");
  assert.equal(jsonPreview(null), "null");
});

test("JSON 阅读器：超长字符串先闭合引号再省略", () => {
  const preview = jsonPreview("x".repeat(300), 10);
  assert.equal(preview, "\"xxxxxxxxxx\"…");
});
