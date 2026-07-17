package errors

import "strings"

// Category 是上游错误的归因分类。它回答两个运行时问题:
//   - 这次失败该不该计到 key 头上(CountsAgainstKey)——错则好 key 被误熔断;
//   - 值不值得 failover / 是否该快速失败(ShouldFailFast)。
//
// 分类只看「状态码 + 上游错误文本」,是纯函数。规则判不出的落到
// CategoryUnknown,由上层(opt-in 时)交给 LLM 兜底二次判定。
type Category int

const (
	// CategoryUnknown 规则判不出。保守起见按「计入 key」处理(保持历史行为),
	// 同时这是 LLM 兜底介入的信号。
	CategoryUnknown Category = iota
	// CategoryKeyError key/账号本身的问题:认证失败、无权限、额度耗尽、欠费、封号。
	// 换 key 可能好转,且该计入 key 失败并最终熔断。
	CategoryKeyError
	// CategoryRequestError 客户端请求本身不合法:格式错误、参数非法、超上下文长度、
	// 模型不存在。换任何 key 都会同样失败 → 不计入 key,且值得快速失败不 failover。
	CategoryRequestError
	// CategoryRateLimited 触发限流(429 / 每分钟配额 / 上游过载)。瞬态,
	// 应冷却而非拉黑,不计入 key。
	CategoryRateLimited
	// CategoryProviderError 上游自身故障(5xx)。不是 key 的锅,不计入
	// (否则一次上游抖动会把整池 key 全熔断);仍可 failover 换 key/子分组。
	CategoryProviderError
)

// keyErrorSubstrings:命中即判 key/账号问题,优先级高于状态码
// (有些上游把 401 语义塞进 400/429 的 body)。全部小写。
var keyErrorSubstrings = []string{
	"invalid api key",
	"incorrect api key",
	"invalid_api_key",
	"api key not valid",
	"api key expired",
	"invalid authentication",
	"authentication fail",
	"no auth credentials",
	"permission denied",
	"permission_error",
	"insufficient quota",
	"insufficient_quota",
	"exceeded your current quota",
	"billing",
	"account is not active",
	"account has been suspended",
	"access token",
}

// rateLimitedSubstrings:命中即判限流/过载(瞬态,不计入 key)。
// 注意:必须在 keyErrorSubstrings 之后判,否则「insufficient quota」里的
// quota 会被误当限流。
var rateLimitedSubstrings = []string{
	"rate limit",
	"too many requests",
	"resource has been exhausted",
	"requests per minute",
	"rpm limit",
	"tpm limit",
	"overloaded",
}

// requestErrorSubstrings:命中即判客户端请求错(不计入 key,值得快速失败)。
// 在状态码兜底之前判,主要用于状态码缺失(传输错误 statusCode==0)的场景。
var requestErrorSubstrings = []string{
	"format not recognis", // recognised / recognized
	"invalid audio",
	"could not be decoded",
	"failed to decode",
	"please reduce the length",
	"reduce the length of the messages",
	"maximum context length",
	"context_length_exceeded",
	"string too long",
	"invalid image",
	"invalid 'messages'",
	"invalid request",
	"unsupported",
	"is not a valid",
	"must be one of",
	"invalid parameter",
}

// Classify 对一次上游失败归因。statusCode==0 表示无 HTTP 响应(传输错误)。
// 判定顺序:高置信度文本子串(可覆盖状态码)→ 状态码兜底 → Unknown。
func Classify(statusCode int, msg string) Category {
	m := strings.ToLower(msg)

	if containsAny(m, keyErrorSubstrings) {
		return CategoryKeyError
	}
	if containsAny(m, rateLimitedSubstrings) {
		return CategoryRateLimited
	}
	if containsAny(m, requestErrorSubstrings) {
		return CategoryRequestError
	}

	switch {
	case statusCode == 401, statusCode == 403, statusCode == 402:
		return CategoryKeyError
	case statusCode == 429:
		return CategoryRateLimited
	case statusCode == 400, statusCode == 404, statusCode == 413, statusCode == 422:
		// 400=Bad Request 语义即「请求本身不合法」——客户端的锅,不该计 key。
		// 这正是「audio ... Format not recognised」误拉黑好 key 的根治点。
		return CategoryRequestError
	case statusCode >= 500:
		return CategoryProviderError
	default:
		// 0(传输错误)、408、3xx 等 → 交给上层保守/LLM 兜底。
		return CategoryUnknown
	}
}

// CountsAgainstKey 报告该分类是否应计入 key 的 failure_count(累积到阈值熔断)。
// 只有 KeyError 和 Unknown(保守)计入;请求错/限流/上游故障都不该怪 key。
func (c Category) CountsAgainstKey() bool {
	return c == CategoryKeyError || c == CategoryUnknown
}

// ShouldFailFast 报告是否该跳过 failover 直接把错误回给客户端。
// 只有 RequestError:换 key 也注定同样失败,重试纯属浪费。
func (c Category) ShouldFailFast() bool {
	return c == CategoryRequestError
}

// String 便于日志/响应头输出。
func (c Category) String() string {
	switch c {
	case CategoryKeyError:
		return "key_error"
	case CategoryRequestError:
		return "request_error"
	case CategoryRateLimited:
		return "rate_limited"
	case CategoryProviderError:
		return "provider_error"
	default:
		return "unknown"
	}
}

// ParseCategory 把分类的规范字符串(见 String)解析回 Category,兼容子串包含
// (LLM 可能回 "request_error." 或 "the category is key_error")。ok=false 表示无法识别。
func ParseCategory(s string) (Category, bool) {
	l := strings.ToLower(s)
	switch {
	case strings.Contains(l, "key_error"):
		return CategoryKeyError, true
	case strings.Contains(l, "request_error"):
		return CategoryRequestError, true
	case strings.Contains(l, "rate_limited"):
		return CategoryRateLimited, true
	case strings.Contains(l, "provider_error"):
		return CategoryProviderError, true
	case strings.Contains(l, "unknown"):
		return CategoryUnknown, true
	default:
		return CategoryUnknown, false
	}
}

func containsAny(haystackLower string, subs []string) bool {
	for _, s := range subs {
		if strings.Contains(haystackLower, s) {
			return true
		}
	}
	return false
}
