// 模型图标是**封闭集合**：规则里只允许这些 id，界面只出这些选项。
// 新增品牌要同时改后端 config.ModelIconIDs（契约测试钉着两边一致）。
export interface ModelIconOption {
  value: string;
  label: string;
}

export const MODEL_ICON_OPTIONS: ModelIconOption[] = [
  { value: "gpt", label: "GPT" },
  { value: "gemini", label: "Gemini" },
  { value: "grok", label: "Grok" },
  { value: "claude", label: "Claude" },
  { value: "deepseek", label: "DeepSeek" },
  { value: "mimo", label: "MiMo" },
  { value: "glm", label: "GLM" },
  { value: "kimi", label: "Kimi" },
  { value: "longcat", label: "LongCat" },
  { value: "mistral", label: "Mistral" },
  { value: "qwen", label: "Qwen" },
  { value: "minimax", label: "MiniMax" },
  { value: "tencent", label: "Tencent" },
  { value: "meta", label: "Meta" },
  { value: "stepfun", label: "StepFun" },
  { value: "bytedance", label: "ByteDance" },
];

export const MODEL_ICON_IDS = MODEL_ICON_OPTIONS.map((option) => option.value);
