// 思考档位（Session 级）：统一七档 + 中文标签 + 模型能力裁剪。
//
// 唯一权威在**后端** `providers.ThinkingLevels`（`GET /models` 的 `thinking_levels`）；
// 这里只负责：标签、给选择器加"默认"、以及从模型清单里取当前模型的能力。
// 别在前端按模型名再写一份能力表 —— 两边一分岔，界面就会摆出上游 422 的选项。
export const THINKING_LEVELS = [
  "off",
  "minimal",
  "low",
  "medium",
  "high",
  "xhigh",
  "max",
] as const;

export type ThinkingLevel = (typeof THINKING_LEVELS)[number];

const LABELS: Record<string, string> = {
  off: "关闭",
  minimal: "最低",
  low: "低",
  medium: "中",
  high: "高",
  xhigh: "极高",
  max: "最高",
};

export interface ThinkingOption {
  value: string;
  label: string;
}

export function thinkingLabel(value?: string | null): string {
  if (!value) return "默认";
  return LABELS[value] ?? value;
}

// thinkingOptions：选择器的完整选项 = "默认"（空串，跟随模型默认）+ 模型能力集里的档位。
export function thinkingOptions(levels?: readonly string[] | null): ThinkingOption[] {
  const supported = new Set(levels ?? THINKING_LEVELS);
  const out: ThinkingOption[] = [{ value: "", label: "默认" }];
  for (const level of THINKING_LEVELS) {
    if (supported.has(level)) out.push({ value: level, label: LABELS[level] ?? level });
  }
  return out;
}

// thinkingLevelsForModel：从 `GET /models` 的清单里取当前模型能收的档位。
// 清单里没有（模型下架 / 还没加载）⇒ 回全部七档；后端仍会按同一套能力表收口。
export function thinkingLevelsForModel(
  models: ReadonlyArray<{ provider: string; upstream_id: string; thinking_levels?: string[] }>,
  provider?: string | null,
  model?: string | null,
): string[] {
  if (!provider || !model) return [...THINKING_LEVELS];
  const hit = models.find((m) => m.provider === provider && m.upstream_id === model);
  const levels = hit?.thinking_levels;
  return levels && levels.length ? [...levels] : [...THINKING_LEVELS];
}
