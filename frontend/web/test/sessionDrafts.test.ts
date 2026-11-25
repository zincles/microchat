import assert from "node:assert/strict";
import test from "node:test";
import { createSessionDrafts } from "../src/utils/sessionDrafts.ts";

test("会话与尚未建库的草稿按 ID 隔离，草稿不跨页面", () => {
  const drafts = createSessionDrafts();
  drafts.set("新会话", "  还没发\n");
  drafts.set("A", "甲");
  assert.equal(drafts.get("B"), "");
  drafts.set("B", "乙");
  assert.equal(drafts.get("A"), "甲");
  assert.equal(drafts.get("新会话"), "  还没发\n");
  assert.equal(createSessionDrafts().get("A"), "");
});

test("受理或回滚只动提交时所属的空位，回滚保留原始空白", () => {
  const drafts = createSessionDrafts();
  drafts.set("A", "  甲\n");
  const first = drafts.take("A");
  assert.equal(first.text, "  甲\n");
  assert.equal(drafts.get("A"), "");
  drafts.set("B", "乙");
  first.reject();
  assert.equal(drafts.get("A"), "  甲\n");
  assert.equal(drafts.get("B"), "乙");
  const second = drafts.take("A");
  second.accept();
  second.reject();
  assert.equal(drafts.get("A"), "");
});

test("请求中又打字、主动清空或删除会话：迟到的响应不会抹字或复活旧草稿", () => {
  const drafts = createSessionDrafts();
  for (const settle of ["accept", "reject"] as const) {
    drafts.set("A", "已提交");
    const sent = drafts.take("A");
    drafts.set("A", "下一句");
    sent[settle]();
    assert.equal(drafts.get("A"), "下一句");
  }
  const erased = drafts.take("A");
  drafts.set("A", "改写");
  drafts.set("A", "");
  erased.reject();
  assert.equal(drafts.get("A"), "");
  drafts.set("A", "不应复活");
  const deleted = drafts.take("A");
  drafts.forget("A");
  deleted.reject();
  assert.equal(drafts.get("A"), "");
});
