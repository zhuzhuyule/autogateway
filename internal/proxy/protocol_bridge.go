package proxy

import (
	"strings"

	"autogateway/internal/apicompat"

	"github.com/sirupsen/logrus"
)

// translation 描述一次转发需要的协议转换。零值表示不转换。
//
// 判据很简单:**入站协议 ≠ 目标节点协议**。不匹配的节点(绝大多数)完全不进
// 这条路径,行为与接入转换层之前逐字节一致。
type translation struct {
	needed bool
	from   apicompat.Format
	to     apicompat.Format
}

// formatForChannelType 把上游节点的 channel_type 映射成它说的协议。
// 未知类型返回空 —— 空值参与 NeedsTranslation 判定永远为 false,即"不转换",
// 保证新接入的 channel 类型不会因为不在表里就被误转。
func formatForChannelType(channelType string) apicompat.Format {
	switch channelType {
	case "openai":
		return apicompat.FormatChatCompletions
	case "anthropic":
		return apicompat.FormatAnthropic
	default:
		return ""
	}
}

// detectInboundFormat 从请求路径推断客户端使用的协议。
// 用路径而不是 Content-Type 判断,因为三个前缀路由 (/openai /anthropic /gemini)
// 已经把协议钉在 URL 上了,而 body 里的字段是重叠的。
func detectInboundFormat(path string) apicompat.Format {
	switch {
	case strings.Contains(path, "/messages"):
		return apicompat.FormatAnthropic
	case strings.Contains(path, "/chat/completions"):
		return apicompat.FormatChatCompletions
	default:
		return ""
	}
}

// planTranslation 决定这次转发要不要转、怎么转。
//
// 当前只实现 Anthropic ⇄ ChatCompletions 这一对。其它组合(gemini /
// openai-response)一律不转 —— 宁可让请求失败,也不要返回一个形状不对的响应。
func planTranslation(path, channelType string) translation {
	from := detectInboundFormat(path)
	to := formatForChannelType(channelType)
	if !apicompat.NeedsTranslation(from, to) {
		return translation{}
	}
	switch {
	case from == apicompat.FormatAnthropic && to == apicompat.FormatChatCompletions:
		return translation{needed: true, from: from, to: to}
	case from == apicompat.FormatChatCompletions && to == apicompat.FormatAnthropic:
		return translation{needed: true, from: from, to: to}
	default:
		return translation{}
	}
}

// upstreamPath 给出目标协议对应的规范路径。
//
// 这一步不可省:转换不只是换 body。Anthropic 打 /v1/messages,Chat 打
// /v1/chat/completions,Gemini 甚至把模型名编在路径里。路径要在
// BuildUpstreamURL 之前改写,否则会拿着旧协议的路径去拼上游地址。
func (t translation) upstreamPath(path string) string {
	if !t.needed {
		return path
	}
	switch t.to {
	case apicompat.FormatChatCompletions:
		return "/v1/chat/completions"
	case apicompat.FormatAnthropic:
		return "/v1/messages"
	default:
		return path
	}
}

// convertRequest 转换请求体。
//
// 转换失败时**返回原 body 让请求原样转发**,不返回错误中断:转换层是为"让
// 更多请求能成功"而存在的,不该成为新的失败源。原样转发最坏结果是上游报
// 400,而中断会让本来能成功的请求也失败。
func (t translation) convertRequest(body []byte) []byte {
	if !t.needed || len(body) == 0 {
		return body
	}
	var (
		out []byte
		err error
	)
	switch {
	case t.from == apicompat.FormatAnthropic && t.to == apicompat.FormatChatCompletions:
		out, err = apicompat.AnthropicRequestToChatJSON(body)
	case t.from == apicompat.FormatChatCompletions && t.to == apicompat.FormatAnthropic:
		out, err = apicompat.ChatRequestToAnthropicJSON(body)
	default:
		return body
	}
	if err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"from": t.from,
			"to":   t.to,
		}).Warn("protocol translation failed, forwarding request unchanged")
		return body
	}
	logrus.WithFields(logrus.Fields{
		"from":      t.from,
		"to":        t.to,
		"in_bytes":  len(body),
		"out_bytes": len(out),
	}).Debug("translated request body")
	return out
}

// convertResponse 把上游响应翻回客户端请求的协议形状。
// 与 convertRequest 一样,失败时原样返回。
func (t translation) convertResponse(body []byte) []byte {
	if !t.needed || len(body) == 0 {
		return body
	}
	var (
		out []byte
		err error
	)
	// 注意方向与 convertRequest 相反:响应是**上游协议 → 客户端协议**,
	// 也就是 to → from。from=anthropic / to=chat 表示客户端说 Anthropic、
	// 上游是 Chat,所以这里要把 Chat 响应翻成 Anthropic 响应。
	switch {
	case t.from == apicompat.FormatAnthropic && t.to == apicompat.FormatChatCompletions:
		out, err = apicompat.ChatResponseToAnthropicJSON(body)
	case t.from == apicompat.FormatChatCompletions && t.to == apicompat.FormatAnthropic:
		out, err = apicompat.AnthropicResponseToChatJSON(body)
	default:
		return body
	}
	if err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"from": t.to,
			"to":   t.from,
		}).Warn("protocol translation failed, returning upstream response unchanged")
		return body
	}
	return out
}
