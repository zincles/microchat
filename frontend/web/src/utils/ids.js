// ids.js —— 客户端铸 id。
//
// ⚠ **HACK（刻意，全项目唯一）**：id 一向由服务端铸（消息 / 摘要 / agent），客户端能铸的只有
// **草稿的会话 id** 这一枚 —— 理由只有"预演 = 真发"这一条：草稿里会话还不存在，但预演那一发的
// 请求头上（`x-opencode-session` 那类）就得有 `sessions.id`；先在客户端铸好，预演用它、
// 发送时交给 `POST /sessions` 落同一个值，两份请求才逐字节一致。后端侧的对口子见
// `core/internal/server/server.go` 的 `CreateSessionReq`（形状校验 + 撞车 409）。
// **别把这里扩散成"客户端都能铸 id"**。
//
// 形状必须与后端 `uuid.NewV7()` 逐字节同构：48 位毫秒 + version 7 + variant + 随机位。
export function uuidv7(now = Date.now()) {
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  let ts = BigInt(now);
  for (let i = 5; i >= 0; i--) {
    bytes[i] = Number(ts & 0xffn);
    ts >>= 8n;
  }
  bytes[6] = (bytes[6] & 0x0f) | 0x70; // version 7
  bytes[8] = (bytes[8] & 0x3f) | 0x80; // variant 10xx
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}
