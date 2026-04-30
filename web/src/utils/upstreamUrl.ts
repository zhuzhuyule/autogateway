// 上游 baseUrl 规范化:统一让 baseUrl 以版本段 (/vN, /vNbeta) 结尾,
// 这样 refresh-models 自动拼 /models, dedupeVersionSegment 处理双 /v1,
// 聚合分组的 endpoint 一致性校验也能命中默认 /v1/chat/completions.
//
// 规则 (按顺序):
//   1. 输入末尾是 "#" → 用户显式跳过自动追加, 仅去掉 "#"
//   2. 已经匹配 /v\d+(beta\d*)?$ → 不变
//   3. 末尾是 /api → 追加 /v1 (openrouter / 各种 *-api 命名)
//   4. 其他情况 → 兜底追加 /v1
//
// 例:
//   normalizeUpstreamUrl("https://openrouter.ai/api")
//     → "https://openrouter.ai/api/v1"
//   normalizeUpstreamUrl("https://api.openai.com")
//     → "https://api.openai.com/v1"
//   normalizeUpstreamUrl("https://api.openai.com/v1")
//     → "https://api.openai.com/v1"
//   normalizeUpstreamUrl("https://open.bigmodel.cn/api/paas/v4")
//     → "https://open.bigmodel.cn/api/paas/v4"
//   normalizeUpstreamUrl("https://my.host/foo#")
//     → "https://my.host/foo"

const VERSION_TAIL_RE = /\/v\d+(?:beta\d*)?$/;

export function normalizeUpstreamUrl(raw: string): string {
  const trimmed = (raw || "").trim();
  if (!trimmed) {
    return "";
  }

  // 仅 http(s) 才规范化, 否则原样返回 (避免误改 ws://, file:// 等)
  if (!/^https?:\/\//i.test(trimmed)) {
    return trimmed;
  }

  // 1) "#" 转义: 去掉 "#", 不再追加版本
  if (trimmed.endsWith("#")) {
    return trimmed.slice(0, -1).replace(/\/+$/, "");
  }

  // 去尾斜杠后判断
  const stripped = trimmed.replace(/\/+$/, "");

  // 2) 已带 /vN
  if (VERSION_TAIL_RE.test(stripped)) {
    return stripped;
  }

  // 3) 末尾 /api → /api/v1
  if (/\/api$/i.test(stripped)) {
    return stripped + "/v1";
  }

  // 4) 兜底
  return stripped + "/v1";
}

// 用于 UI 预览: 把 baseUrl + 相对 endpoint 拼成最终上游路径,
// 不实际触发后端的 dedupeVersionSegment, 但模拟其行为给用户直观反馈.
export function previewUpstreamPath(rawBaseUrl: string, endpoint: string): string {
  const base = normalizeUpstreamUrl(rawBaseUrl);
  if (!base) {
    return "";
  }
  const ep = (endpoint || "").trim();
  if (!ep) {
    return base;
  }
  // 模拟 dedupeVersionSegment: 如果 base 末段是 /vN 且 endpoint 开头也是同一段, 去重
  const tailMatch = base.match(VERSION_TAIL_RE);
  if (tailMatch) {
    const tail = tailMatch[0]; // 如 "/v1"
    if (ep === tail) {
      return base;
    }
    if (ep.startsWith(tail + "/")) {
      return base + ep.slice(tail.length);
    }
  }
  if (!ep.startsWith("/")) {
    return base + "/" + ep;
  }
  return base + ep;
}
