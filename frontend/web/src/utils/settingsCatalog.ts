import type { MarkdownType } from "../api/types.ts";
import type { ICONS } from "../assets/icons.ts";

// 与设置视图外层的窗口顺序一致，返回目录与进入子页的方向才一致。
export const SETTINGS_GROUPS = [
  { label: "常用设置", pages: [
    { id: "defaults", label: "新会话默认", desc: "默认渠道、模型和智能体", icon: "messagePlus" },
    { id: "appearance", label: "外观", desc: "主题、字号、字体与背景", icon: "themeAuto" },
    { id: "input", label: "输入与发送", desc: "回车行为、输入时预览与称呼", icon: "send" },
  ] },
  { label: "对话与模型", pages: [
    { id: "context", label: "上下文与压缩", desc: "历史加载、上下文长度和压缩阈值", icon: "sliders" },
    { id: "generation", label: "生成与标题", desc: "自动标题模型与历史思考回传", icon: "brain" },
    { id: "markdown", label: "消息渲染", desc: "Markdown 总开关与各类格式", icon: "notebookPen" },
    { id: "provider", label: "渠道与模型规则", desc: "上游接口、密钥与全局模型规则", icon: "server" },
  ] },
  { label: "数据与维护", pages: [
    { id: "session", label: "会话", desc: "查看会话、导入与导出记录", icon: "list" },
    { id: "agent", label: "智能体", desc: "人设、工作流与能力配置", icon: "bot" },
    { id: "prompt", label: "提示词片段", desc: "可复用的提示词片段库", icon: "notebookPen" },
    { id: "tavern", label: "酒馆", desc: "SillyTavern 预设与聊天记录", icon: "spark" },
    { id: "update", label: "更新", desc: "版本、自动更新与 PWA 缓存", icon: "refresh" },
  ] },
] as const;

type SettingsPage = (typeof SETTINGS_GROUPS)[number]["pages"][number]["id"];
export const SETTINGS_SECTIONS = SETTINGS_GROUPS.flatMap<{ id: SettingsPage; label: string; desc: string; icon: keyof typeof ICONS }>((group) => group.pages);

export interface SettingsLocation {
  page: SettingsPage;
  anchor?: string;
}

// 固定设置的搜索入口。anchor 对应 setting-*；Markdown 分项使用后端发来的清单。
const FIELDS = [
  ["defaults", "default-provider", "默认渠道", "服务商 接口 provider"],
  ["defaults", "default-model", "默认模型", "新建对话 模型 model"],
  ["defaults", "default-agent", "默认智能体", "新建对话 默认助手 人设 agent"],
  ["appearance", "theme", "主题", "颜色 配色 暗色 深色 亮色 浅色 跟随系统 theme"],
  ["appearance", "scale", "界面缩放", "字号 字体大小 放大 缩小 scale"],
  ["appearance", "font-body", "标准字体", "正文字体 font"],
  ["appearance", "font-mono", "等宽字体", "代码块 等宽 font mono"],
  ["appearance", "background", "背景图", "壁纸 上传 图片 background"],
  ["appearance", "chrome-hide", "滚动时收起顶栏和底栏", "触屏 手机 布局 自动隐藏"],
  ["input", "send-mode", "发送方式", "回车 Enter Shift 换行 发送键"],
  ["input", "input-preview", "输入时预览", "请求预览 发送内容 载荷"],
  ["input", "command-menu", "命令菜单", "/ 斜杠命令 快捷命令"],
  ["input", "user-name", "对话中的称呼", "用户名 用户名称 昵称 你的名字 宏 user_name"],
  ["context", "context-length", "上下文长度", "token 输入预算 模型上下文"],
  ["context", "history-window", "首屏加载条数", "历史消息 分页 弱网 加载更多"],
  ["context", "auto-compact", "自动压缩", "自动摘要 历史 compact"],
  ["context", "compact-blocks", "每次压缩块数", "摘要 对话块"],
  ["context", "compact-trigger", "压缩触发阈值", "触发线 token 自动摘要"],
  ["generation", "title-chars", "自动标题字数", "标题最多字数 名称 长度"],
  ["generation", "title-model", "标题模型", "起标题 全局标题模型"],
  ["generation", "replay-reasoning", "回传思考过程", "推理 思考历史 reasoning"],
  ["markdown", "markdown-enabled", "解析 Markdown 格式", "文本格式 开关 禁用 渲染 原文"],
  ["provider", "global-model-rules", "全局模型规则", "内建 模型定义 推理等级 服务端工具"],
  ["session", "import-json", "导入 MicroChat 对话", "会话 聊天记录 json"],
  ["tavern", "import-preset", "导入酒馆预设", "SillyTavern preset JSON"],
  ["tavern", "import-st", "导入酒馆聊天记录", "SillyTavern 会话 JSONL"],
  ["update", "auto-update", "自动更新", "GitHub 每 5 分钟 检查更新"],
  ["update", "pwa", "前端缓存（PWA）", "强制刷新 Service Worker 安装版"],
] as const satisfies readonly (readonly [SettingsPage, string, string, string])[];

type SearchEntry = SettingsLocation & { label: string; desc: string; icon: keyof typeof ICONS };

export function searchSettings(query: string, markdown: readonly Pick<MarkdownType, "id" | "name">[]): SearchEntry[] {
  const phrase = query.trim().normalize("NFKC").toLowerCase();
  if (!phrase) return [];
  const terms = phrase.split(/\s+/);
  const matches: Array<{ score: number; entry: SearchEntry }> = [];
  const sections = new Map(SETTINGS_SECTIONS.map((page) => [page.id, page]));
  function add(page: SettingsPage, label: string, keywords: string, anchor?: string): void {
    const section = sections.get(page)!;
    const title = label.normalize("NFKC").toLowerCase();
    const haystack = `${title} ${keywords} ${anchor ? section.label : section.desc}`.normalize("NFKC").toLowerCase();
    if (!terms.every((term) => haystack.includes(term))) return;
    const score = title === phrase ? 0 : title.includes(phrase) ? 1 : anchor ? 2 : 3;
    matches.push({ score, entry: { page, anchor, label, desc: anchor ? section.label : section.desc, icon: section.icon } });
  }
  for (const [page, anchor, label, keywords] of FIELDS) add(page, label, keywords, anchor);
  for (const type of markdown) add("markdown", type.name, `${type.id} Markdown 格式`, `markdown-${type.id}`);
  for (const section of SETTINGS_SECTIONS) add(section.id, section.label, "");
  return matches.sort((left, right) => left.score - right.score).map(({ entry }) => entry);
}
