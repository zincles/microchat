import test from "node:test";
import assert from "node:assert/strict";
import {
  THINKING_LEVELS,
  thinkingLabel,
  thinkingLevelsForModel,
  thinkingOptions,
} from "../src/utils/thinking.ts";

test("思考档位：标签与选项顺序（默认在最前）", () => {
  assert.equal(thinkingLabel(""), "默认");
  assert.equal(thinkingLabel("xhigh"), "极高");
  assert.equal(thinkingLabel("max"), "最高");
  const opts = thinkingOptions(["low", "high"]);
  assert.deepEqual(opts.map((o) => o.value), ["", "low", "high"]);
  assert.deepEqual(opts.map((o) => o.label), ["默认", "低", "高"]);
  // 只有一档也要先给"默认"（清回模型默认是合法动作）
  assert.deepEqual(thinkingOptions(["off"]).map((o) => o.value), ["", "off"]);
});

test("思考档位：从模型清单取能力，缺项回全部七档", () => {
  const models = [
    { provider: "p", upstream_id: "claude-opus-5-5", thinking_levels: ["low", "medium", "high"] },
  ];
  assert.deepEqual(thinkingLevelsForModel(models, "p", "claude-opus-5-5"), ["low", "medium", "high"]);
  assert.deepEqual(thinkingLevelsForModel(models, "p", "not-in-list"), [...THINKING_LEVELS]);
  assert.deepEqual(thinkingLevelsForModel([], "p", "m"), [...THINKING_LEVELS]);
});
