package errors

// IsUnCounted 报告某个上游错误消息是否「不该计入 key 失败」。
//
// 它现在只是 Classify 的薄封装(仅凭消息文本、无状态码)。保留此函数是为了
// 向后兼容既有调用方;新代码应直接用 Classify(statusCode, msg) 拿到完整归因,
// 状态码是比消息文本强得多的信号。
func IsUnCounted(errorMsg string) bool {
	if errorMsg == "" {
		return false
	}
	return !Classify(0, errorMsg).CountsAgainstKey()
}
