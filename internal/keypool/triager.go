package keypool

import (
	"context"

	"autogateway/internal/models"
)

// ErrorTriager 是「LLM 错误归因兜底」的消费侧接口。当 Tier 1 规则
// (app_errors.Classify)判不出(CategoryUnknown)且分组 opt-in 时,
// KeyProvider 用它做二次判定:这次失败到底该不该计到 key 头上。
//
// 用接口而非直接依赖 errtriage 具体实现,是为了打破 keypool ↔ errtriage
// 的导入循环(errtriage 需要 *KeyProvider 取 SelectKey)——与 validator.go
// 的 RequestLogRecorder 同一手法。具体实现在 internal/errtriage,
// 经 App.Start 里的 SetTriager 后置注入。
type ErrorTriager interface {
	// ShouldCountAgainstKey 判定该失败是否应计入 key 失败。
	// ok=false 表示无法判定(未配置 / 调用失败 / 超时),调用方须回退到保守策略。
	ShouldCountAgainstKey(ctx context.Context, group *models.Group, statusCode int, message string) (count bool, ok bool)
}

// SetTriager 后置注入 LLM 归因兜底实现。传 nil 或从不调用即禁用 Tier 2。
func (p *KeyProvider) SetTriager(t ErrorTriager) {
	p.triager = t
}
