import assert from "node:assert/strict";
import test from "node:test";
import { filterSessionTitles, groupSessions } from "../src/utils/sessionGroups.ts";

const now = new Date(2026, 10, 25, 12).getTime();
const older = new Date(2026, 8, 3, 12).getTime();

test("标题查找忽略大小写与首尾空白，不查 ID 等隐藏字段", () => {
  const list = [
    { id: "old", title: "Alpha 日记", updated_at: older },
    { id: "alpha", title: "其他", updated_at: now },
    { id: "new", title: "会议 ALPHA", updated_at: now },
    { id: "copy", title: "会议 ALPHA", updated_at: now },
  ];
  const matches = filterSessionTitles(list, "  LpHa  ");
  assert.deepEqual(groupSessions(matches, now).map((g) => [g.label, g.items.map((s) => s.id)]), [
    ["今天", ["new", "copy"]], ["更久", ["old"]],
  ]);
  assert.equal(filterSessionTitles(list, "  "), list);
  assert.deepEqual(filterSessionTitles(list, "不存在"), []);
});

test("收藏始终置顶且不重复进入日期组", () => {
  const sessions = [
    { id: "old", title: "old", updated_at: older, favorite: false },
    { id: "favorite", title: "favorite", updated_at: older, favorite: true },
    { id: "today", title: "today", updated_at: now, favorite: false },
  ];
  assert.deepEqual(groupSessions(sessions, now).map((group) => [group.label, group.items.map((session) => session.id)]), [
    ["收藏", ["favorite"]], ["今天", ["today"]], ["更久", ["old"]],
  ]);
});

test("未命名记录可按显示的标题查找；刷新后的改名和删除立即反映在结果中", () => {
  const list = [{ id: "empty", title: "" }, { id: "blank", title: "   " }, { id: "missing" }];
  assert.deepEqual(filterSessionTitles(list, "新会话").map((s) => s.id), ["empty", "blank", "missing"]);
  const refreshed = [{ ...list[1], title: "新标题" }, list[0]];
  assert.deepEqual(filterSessionTitles(refreshed, "会话").map((s) => s.id), ["empty"]);
  assert.deepEqual(filterSessionTitles(refreshed, "新标题").map((s) => s.id), ["blank"]);
});
