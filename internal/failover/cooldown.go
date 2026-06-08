package failover

import (
	"strings"
	"time"
)

// CooldownClass 冷却性质分类。
type CooldownClass int

const (
	ClassNone        CooldownClass = iota // 不做 model 级冷却（400/401/403 等）
	ClassTransient                        // RPM/TPM 瞬时限流，秒级恢复
	ClassExhausted                        // RPD/TPD 日额耗尽，长隔离
	ClassPayment                          // 402 余额/额度不足
	ClassServerError                      // 5xx / network，故障升级
)

// Classify 从 HTTP 状态码 + 上游错误文本推断冷却性质。纯函数。
// 约定：statusCode == 0 表示网络错误（无 HTTP 响应）。
func Classify(statusCode int, parsedError string) CooldownClass {
	low := strings.ToLower(parsedError)

	// 计费/额度类优先（部分 provider 用 429/400 表达余额耗尽）
	if statusCode == 402 ||
		strings.Contains(low, "insufficient") ||
		strings.Contains(low, "payment") ||
		strings.Contains(low, "billing") ||
		strings.Contains(low, "exceeded your current quota") {
		return ClassPayment
	}

	if statusCode == 429 {
		if strings.Contains(low, "per day") ||
			strings.Contains(low, "per-day") ||
			strings.Contains(low, "/day") ||
			strings.Contains(low, "daily") ||
			strings.Contains(low, "rpd") ||
			strings.Contains(low, "tpd") ||
			strings.Contains(low, "requests per day") ||
			strings.Contains(low, "tokens per day") {
			return ClassExhausted
		}
		return ClassTransient
	}

	if statusCode >= 500 || statusCode == 0 {
		return ClassServerError
	}

	return ClassNone
}

// CooldownPolicy 各类冷却的时长参数。用 DefaultCooldownPolicy 构造。
type CooldownPolicy struct {
	TransientDefault time.Duration // 无 Retry-After 时的瞬时冷却
	TransientMax     time.Duration // Retry-After 上限 clamp
	ExhaustedDefault time.Duration // 无 Retry-After 时的耗尽冷却
	PaymentCooldown  time.Duration // 402 冷却
	ServerBase       time.Duration // 5xx 升级阶梯基数
	ServerMax        time.Duration // 5xx 升级阶梯上限
}

func DefaultCooldownPolicy() CooldownPolicy {
	return CooldownPolicy{
		TransientDefault: 90 * time.Second,
		TransientMax:     10 * time.Minute,
		ExhaustedDefault: 6 * time.Hour,
		PaymentCooldown:  24 * time.Hour,
		ServerBase:       30 * time.Second,
		ServerMax:        5 * time.Minute,
	}
}

// Decide 返回本次冷却时长，以及该次是否计入升级计数（hitCount）。
// hitCount 仅 ClassServerError 使用，做指数升级；其余 class 时长由错误性质直接决定。
func (p CooldownPolicy) Decide(class CooldownClass, retryAfter time.Duration, hitCount int) (dur time.Duration, countsTowardEscalation bool) {
	switch class {
	case ClassTransient:
		if retryAfter > 0 {
			if retryAfter > p.TransientMax {
				return p.TransientMax, false
			}
			return retryAfter, false
		}
		return p.TransientDefault, false
	case ClassExhausted:
		if retryAfter > 0 {
			return retryAfter, false
		}
		return p.ExhaustedDefault, false
	case ClassPayment:
		return p.PaymentCooldown, false
	case ClassServerError:
		shift := hitCount
		if shift > 4 {
			shift = 4
		}
		d := p.ServerBase << uint(shift)
		if d > p.ServerMax {
			d = p.ServerMax
		}
		return d, true
	default:
		return 0, false
	}
}
