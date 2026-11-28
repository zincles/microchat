import type { MarkdownType } from "../api/types.ts";
import type { ICONS } from "../assets/icons.ts";

type SettingsPage = "chat" | "provider" | "agent" | "prompt" | "appearance" | "session" | "update" | "tavern" | "runner";

type SettingsPageDefinition = {
  id: SettingsPage;
  label: string;
  desc: string;
  icon: keyof typeof ICONS;
};

type SettingsCategoryDefinition = {
  label: string;
  pages: SettingsPageDefinition[];
};

export const SETTINGS_GROUPS: SettingsCategoryDefinition[] = [
  { label: "用户配置", pages: [
    { id: "chat", label: "对话", desc: "新会话默认、上下文、压缩与自动标题", icon: "chat" },
    { id: "provider", label: "渠道与模型规则", desc: "上游接口、密钥与全局模型规则", icon: "server" },
    { id: "agent", label: "智能体", desc: "人设、工作流与能力配置", icon: "bot" },
    { id: "prompt", label: "提示词片段", desc: "可复用的提示词片段库", icon: "notebookPen" },
  ] },
  { label: "外观", pages: [
    { id: "appearance", label: "外观与输入", desc: "主题风格、亮暗、字体、输入与渲染", icon: "themeAuto" },
  ] },
  { label: "数据", pages: [
    { id: "session", label: "会话", desc: "查看会话、导入与导出记录", icon: "list" },
  ] },
  { label: "系统设置", pages: [
    { id: "update", label: "关于与更新", desc: "品牌、应用版本与 PWA 缓存", icon: "refresh" },
  ] },
  { label: "外部", pages: [
    { id: "tavern", label: "酒馆", desc: "SillyTavern 预设与聊天记录", icon: "spark" },
    { id: "runner", label: "Runner", desc: "外部执行机连接与授权入口", icon: "server" },
  ] },
];

export const SETTINGS_SECTIONS = SETTINGS_GROUPS.flatMap((group) => group.pages);

export interface SettingsLocation {
  page: SettingsPage;
  anchor?: string;
}


// 固定设置的搜索入口。anchor 对应 setting-*；Markdown 分项使用后端发来的清单。
const FIELDS = [
  ["chat", "default-provider", "默认渠道", "服务商 接口 provider"],
  ["chat", "default-model", "默认模型", "新建对话 模型 model"],
  ["chat", "default-agent", "默认智能体", "新建对话 默认助手 人设 agent"],
  ["appearance", "theme-style", "界面风格", "主题 MD3 Material 3 Miuix MIUI HyperOS"],
  ["appearance", "theme", "颜色模式", "主题 颜色 配色 暗色 深色 亮色 浅色 自动 跟随系统 theme"],
  ["appearance", "scale", "界面缩放", "字号 字体大小 放大 缩小 scale"],
  ["appearance", "font-body", "标准字体", "正文字体 font"],
  ["appearance", "font-mono", "等宽字体", "代码块 等宽 font mono"],
  ["appearance", "background", "背景图", "壁纸 上传 图片 background"],
  ["appearance", "chrome-hide", "滚动时收起聊天控件和输入栏", "悬浮控件 触屏 手机 布局 自动隐藏 顶栏 底栏"],
  ["appearance", "send-mode", "发送方式", "回车 Enter Shift 换行 发送键"],
  ["appearance", "input-preview", "输入时预览", "请求预览 发送内容 载荷"],
  ["appearance", "command-menu", "命令菜单", "/ 斜杠命令 快捷命令"],
  ["appearance", "user-name", "对话中的称呼", "用户名 用户名称 昵称 你的名字 宏 user_name"],
  ["chat", "context-length", "上下文长度", "token 输入预算 模型上下文"],
  ["chat", "history-window", "首屏加载条数", "历史消息 分页 弱网 加载更多"],
  ["chat", "auto-compact", "自动压缩", "自动摘要 历史 compact"],
  ["chat", "compact-blocks", "每次压缩块数", "摘要 对话块"],
  ["chat", "compact-trigger", "压缩触发阈值", "触发线 token 自动摘要"],
  ["chat", "title-chars", "自动标题字数", "标题最多字数 名称 长度"],
  ["chat", "title-model", "标题模型", "起标题 全局标题模型"],
  ["appearance", "markdown-enabled", "解析 Markdown 格式", "文本格式 开关 禁用 渲染 原文"],
  ["provider", "global-model-rules", "全局模型规则", "内建 模型定义 推理等级 服务端工具 历史思考 回传思考过程 reasoning"],
  ["session", "import-json", "导入 MicroChat 对话", "会话 聊天记录 json"],
  ["tavern", "import-preset", "导入酒馆预设", "SillyTavern preset JSON"],
  ["tavern", "import-st", "导入酒馆聊天记录", "SillyTavern 会话 JSONL"],
  ["update", "auto-update", "自动更新", "GitHub 每 5 分钟 检查更新"],
  ["update", "pwa", "前端缓存（PWA）", "强制刷新 Service Worker 安装版"],
  ["update", "brand-name", "品牌名称", "应用名称 标题"],
  ["update", "brand-icon", "品牌图标", "应用图标 logo"],
  ["update", "welcome-text", "空白草稿欢迎语", "首页 问候 文案"],
	["runner", "runner", "Runner 接入", "外部执行机 Codex Claude Code Pi OMP"],
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
  for (const type of markdown) add("appearance", type.name, `${type.id} Markdown 格式`, `markdown-${type.id}`);
  for (const section of SETTINGS_SECTIONS) add(section.id, section.label, "");
  return matches.sort((left, right) => left.score - right.score).map(({ entry }) => entry);
}
