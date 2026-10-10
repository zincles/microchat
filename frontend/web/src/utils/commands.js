// 命令解析 + 命令表（输入框 / 开头用；与网络无关，纯函数）。
// 命令解析："/name arg1 arg2" → {name, args}；非 / 开头 → null。
export function parseCommand(input) {
  if (!input.startsWith("/")) return null;
  const parts = input.slice(1).trim().split(/\s+/).filter(Boolean);
  if (!parts.length) return { name: "", args: [] };
  return { name: parts[0], args: parts.slice(1) };
}

// 命令表：name → {desc, needsSession?}。**唯一一份**：
// 面板（/ 浮层）从这里取说明，App 的执行器按同一份键分发、按 needsSession 拦草稿。
// 表里有的命令 App 必须有执行器（App 侧启动时对账，漏了会 console.error，别静默）。
export const COMMAND_META = {
  resume: { desc: "选会话进入" },
  model: { desc: "换模型（顶栏下拉）" },
  providers: { desc: "看渠道列表" },
  agents: { desc: "看 Agent 列表" },
  compact: { desc: "压缩 N 块（空=默认块数）", needsSession: true },
  reroll: { desc: "重摇尾条回复（进模式选版）", needsSession: true },
  switch: { desc: "切重摇版本（switch <n>）", needsSession: true },
  stop: { desc: "停掉这轮生成", needsSession: true },
  resend: { desc: "重发历史（再跑一轮）", needsSession: true },
  new: { desc: "新建会话并进入" },
  delete: { desc: "删整条会话（再建一条）", needsSession: true },
  cut: { desc: "删一条及之后（先预览）", needsSession: true },
  edit: { desc: "改消息正文/思考（edit <n> [思考]）", needsSession: true },
  editsum: { desc: "手改摘要正文", needsSession: true },
};

// 前缀过滤（/ 开头输入用；空名回全部）：返回 [{name, desc}]（面板直接渲染它）。
export function filterCommands(name) {
  const q = (name ?? "").toLowerCase();
  return Object.entries(COMMAND_META)
    .filter(([n]) => n.startsWith(q))
    .map(([n, m]) => ({ name: n, desc: m.desc }));
}
