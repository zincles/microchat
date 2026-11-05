// 任务面板（`GET /tasks`）的纯函数：一眼看出"哪条会话此刻有活"。
//
// 左栏的加载环吃它（2026-11-05 用户定：「如果一个对话有正在运行的 TASK，那么这个对话在
// 左侧对话列表里，就应该有些反应」）。判断必须只在这一处 —— 顶栏环心那个数字、任务面板
// 的分组、左栏的行内环，谁要"在不在跑"都从这里推，别各自 filter 一遍（三处的时钟与
// 对"什么算在跑"的理解一旦分岔，同一时刻会显示两个答案）。

// busySessionIds：此刻有任务在跑的会话 id（去重、无顺序语义）。
//
// 三个来源，缺一个就会"明明在跑却不亮"：
//   - 原子任务：`session` 有值、`finished_at` 没值。生成 / 重摇 / 压缩 / 分段压缩的每个 chunk /
//     起标题都带会话 id；`refresh_models` 没有会话 ⇒ 天然不进来（它不绑任何对话）。
//   - 队列：队列自己也是一段在跑的工作，`current` 是它此刻踩着的那条会话。
//     服务端在两步之间会短暂地没有原子任务（上一发刚收尾、下一发还没挂号）—— 只看任务
//     会让左栏在队列里一闪一闪。
//   - 已完成的不算：`finished_at` 有值就是收尾了（面板还会把它列一阵子，那是给人看的账，
//     不是"还在跑"）。**别拿 outcome 判**：`取消` / `中断` 也是完成，而 `进行中` 才是活的。
export function busySessionIds(board) {
  const ids = new Set();
  for (const task of board?.tasks ?? []) {
    if (!task?.finished_at && task?.session) ids.add(task.session);
  }
  for (const queue of board?.queues ?? []) {
    if (!queue?.finished_at && queue?.current?.session_id) ids.add(queue.current.session_id);
  }
  return [...ids];
}
