// 命令解析（输入框 / 开头用；与网络无关，纯函数）。
// 命令解析："/name arg1 arg2" → {name, args}；非 / 开头 → null。
export function parseCommand(input) {
  if (!input.startsWith("/")) return null;
  const parts = input.slice(1).trim().split(/\s+/).filter(Boolean);
  if (!parts.length) return { name: "", args: [] };
  return { name: parts[0], args: parts.slice(1) };
}

// 命令表：name → 说明（面板里 /name 右边小字；未知命令回这张表的键）。
export const COMMAND_DESC = {
  resume: "选会话进入",
  model: "换模型（顶栏下拉）",
  providers: "看渠道列表",
  agents: "看 Agent 列表",
  compact: "压缩 N 块（空=默认块数）",
  reroll: "重摇尾条回复（进模式选版）",
  switch: "切重摇版本（switch <n>）",
  stop: "停掉这轮生成",
  resend: "重发历史（再跑一轮）",
  new: "新建会话并进入",
  delete: "删整条会话（再建一条）",
  cut: "删一条及之后（先预览）",
  edit: "改消息正文/思考（edit <n> [思考]）",
  editsum: "手改摘要正文",
};

export const COMMANDS = Object.keys(COMMAND_DESC);

// 前缀过滤（/ 开头输入用；空名回全部）。
export function filterCommands(name) {
  const q = (name ?? "").toLowerCase();
  return COMMANDS.filter((c) => c.startsWith(q));
}
