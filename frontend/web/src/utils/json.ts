// JSON 阅读器的纯数据/格式化逻辑（无 DOM；契约测试直引这里）。
//
// 只服务“看”：不修改、不重排键。解析失败时抛出**带原因**的错误，
// 不要悄悄退回 `{}` —— 那等于告诉用户这个文件是空的，而它可能只是坏了。

export type JsonPrimitive = string | number | boolean | null;
export type JsonObject = { [key: string]: JsonValue };
export type JsonValue = JsonPrimitive | JsonValue[] | JsonObject;

export type JsonKind = "object" | "array" | "string" | "number" | "boolean" | "null";

export interface JsonEntry {
  key: string;
  value: JsonValue;
  index: boolean;
}

export function isJsonContainer(value: JsonValue): value is JsonObject | JsonValue[] {
  return typeof value === "object" && value !== null;
}

export function jsonKind(value: JsonValue): JsonKind {
  if (value === null) return "null";
  if (Array.isArray(value)) return "array";
  switch (typeof value) {
    case "string": return "string";
    case "number": return "number";
    case "boolean": return "boolean";
    default: return "object";
  }
}

export function jsonEntries(value: JsonValue): JsonEntry[] {
  if (Array.isArray(value)) {
    return value.map((item, index) => ({ key: String(index), value: item, index: true }));
  }
  if (isJsonContainer(value)) {
    return Object.entries(value).map(([key, item]) => ({ key, value: item, index: false }));
  }
  return [];
}

// jsonPreview：单行预览。字符串保留 JSON 引号与转义（否则换行会把一行撑爆）；
// 超长时先截原文再序列化，保证引号闭合，末尾的省略号表示“后面还有”。
export function jsonPreview(value: JsonValue, max = 240): string {
  switch (jsonKind(value)) {
    case "string": {
      const text = value as string;
      if (text.length <= max) return JSON.stringify(text);
      return `${JSON.stringify(text.slice(0, max))}…`;
    }
    case "array":
      return `Array(${(value as JsonValue[]).length})`;
    case "object":
      return `Object(${Object.keys(value as JsonObject).length})`;
    case "null":
      return "null";
    case "boolean":
    case "number":
      return String(value);
  }
}

// parseJsonText：容忍 UTF-8 BOM（上传口允许它）；坏文件把原始原因带出来。
export function parseJsonText(text: string): JsonValue {
  const clean = text.charCodeAt(0) === 0xFEFF ? text.slice(1) : text;
  try {
    return JSON.parse(clean) as JsonValue;
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    throw new Error(`JSON 解析失败：${message}`);
  }
}
