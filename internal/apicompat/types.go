// Package apicompat 提供 LLM 协议之间的纯函数转换。
//
// 当前实现 Anthropic Messages (/v1/messages) 与 OpenAI ChatCompletions
// (/v1/chat/completions) 的双向转换。设计约定:
//
//  1. 本包不依赖 gin / net/http / 数据库,只吃 []byte 和结构体,便于单测与
//     后续被任何转发层复用。
//  2. 转换拓扑是 hub-and-spoke,hub 是 ChatCompletions —— 因为 AutoGateway
//     的绝大多数上游子分组是 channel_type=openai,以 Chat 为枢纽可以让主流
//     量 (Chat→Chat) 保持零转换。新增第三种协议只需实现到 Chat 的两条边。
//  3. 所有转换都是"尽力而为 + 防御性规范化":上游协议的不变式(如 Anthropic
//     的 tool_use/tool_result 必须配对、user/assistant 必须交替)由 normalize.go
//     负责修复,因为网关看到的客户端历史是不可控的。
package apicompat

// ---------- Anthropic Messages ----------

// AnthropicRequest 是 POST /v1/messages 的请求体。
type AnthropicRequest struct {
	Model         string               `json:"model"`
	System        any                  `json:"system,omitempty"` // string 或 []AnthropicContentBlock
	Messages      []AnthropicMessage   `json:"messages"`
	MaxTokens     int                  `json:"max_tokens,omitempty"`
	Temperature   *float64             `json:"temperature,omitempty"`
	TopP          *float64             `json:"top_p,omitempty"`
	TopK          *int                 `json:"top_k,omitempty"`
	StopSequences []string             `json:"stop_sequences,omitempty"`
	Stream        bool                 `json:"stream,omitempty"`
	Tools         []AnthropicTool      `json:"tools,omitempty"`
	ToolChoice    *AnthropicToolChoice `json:"tool_choice,omitempty"`
	Thinking      *AnthropicThinking   `json:"thinking,omitempty"`
	Metadata      *AnthropicMetadata   `json:"metadata,omitempty"`
}

// AnthropicMetadata 原样透传。OAuth / 官方客户端判定依赖 user_id,
// 转换过程中不得改写(本项目暂无 OAuth 渠道,但结构体保留以保持兼容)。
type AnthropicMetadata struct {
	UserID string `json:"user_id,omitempty"`
}

// AnthropicThinking 是扩展思考配置。
type AnthropicThinking struct {
	Type         string `json:"type"` // "enabled" | "disabled"
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

// AnthropicTool 是 Anthropic 侧的工具定义。
type AnthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
}

// AnthropicToolChoice 是工具选择策略。
// Type 取 "auto" | "any" | "tool" | "none",Name 仅在 Type=="tool" 时有意义。
type AnthropicToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

// AnthropicMessage 是 Anthropic 侧的一条消息。Content 可以是纯字符串,
// 也可以是 []AnthropicContentBlock(历史里携带 tool_use / tool_result 时)。
type AnthropicMessage struct {
	Role    string `json:"role"` // "user" | "assistant"
	Content any    `json:"content"`
}

// AnthropicContentBlock 是 Anthropic 的内容块。请求与响应共用同一结构,
// 靠 Type 区分语义。
type AnthropicContentBlock struct {
	Type string `json:"type"` // text | image | tool_use | tool_result | thinking

	// text / thinking
	Text      string `json:"text,omitempty"`
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`

	// image
	Source *AnthropicImageSource `json:"source,omitempty"`

	// tool_use
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`

	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   any    `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

// AnthropicImageSource 是 Anthropic 图片块的 source 字段。
type AnthropicImageSource struct {
	Type      string `json:"type"` // "base64" | "url"
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

// AnthropicResponse 是 POST /v1/messages 的非流式响应体。
type AnthropicResponse struct {
	ID           string                  `json:"id"`
	Type         string                  `json:"type"` // "message"
	Role         string                  `json:"role"` // "assistant"
	Model        string                  `json:"model"`
	Content      []AnthropicContentBlock `json:"content"`
	StopReason   string                  `json:"stop_reason,omitempty"`
	StopSequence string                  `json:"stop_sequence,omitempty"`
	Usage        AnthropicUsage          `json:"usage"`
}

// AnthropicUsage 是 Anthropic 的**互斥桶**口径:input_tokens 不含缓存,
// 缓存读/写是独立字段。与 OpenAI 的包容桶(prompt_tokens 含缓存)不同,
// 双向转换时必须投影,见 usage.Usage。
type AnthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

// ---------- OpenAI ChatCompletions ----------

// ChatRequest 是 POST /v1/chat/completions 的请求体。
type ChatRequest struct {
	Model               string             `json:"model"`
	Messages            []ChatMessage      `json:"messages"`
	MaxTokens           *int               `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int               `json:"max_completion_tokens,omitempty"`
	Temperature         *float64           `json:"temperature,omitempty"`
	TopP                *float64           `json:"top_p,omitempty"`
	Stop                any                `json:"stop,omitempty"` // string 或 []string
	Stream              bool               `json:"stream,omitempty"`
	StreamOptions       *ChatStreamOptions `json:"stream_options,omitempty"`
	Tools               []ChatTool         `json:"tools,omitempty"`
	ToolChoice          any                `json:"tool_choice,omitempty"`
	ReasoningEffort     string             `json:"reasoning_effort,omitempty"`
}

// ChatStreamOptions 是流式选项。IncludeUsage 让上游在流尾多发一个 usage 帧。
type ChatStreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// ChatTool 是 OpenAI 侧的工具定义。
type ChatTool struct {
	Type     string       `json:"type"` // "function"
	Function ChatFunction `json:"function"`
}

// ChatFunction 是工具的函数签名。Parameters 即 JSON Schema。
type ChatFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// ChatMessage 是 OpenAI 侧的一条消息。
type ChatMessage struct {
	Role string `json:"role"` // system | user | assistant | tool
	// Content 可以是 string,也可以是 []ChatContentPart(多模态)。
	Content any `json:"content"`

	Name string `json:"name,omitempty"`

	// assistant 携带工具调用时
	ToolCalls []ChatToolCall `json:"tool_calls,omitempty"`

	// role=tool 时回指对应的调用
	ToolCallID string `json:"tool_call_id,omitempty"`

	// ReasoningContent 是各家对"思考过程"的事实标准字段名。
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

// ChatContentPart 是多模态 content 的一个分片。
type ChatContentPart struct {
	Type     string        `json:"type"` // "text" | "image_url"
	Text     string        `json:"text,omitempty"`
	ImageURL *ChatImageURL `json:"image_url,omitempty"`
}

// ChatImageURL 是图片分片的 image_url 字段。
type ChatImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// ChatToolCall 是 assistant 消息里的一次工具调用。
type ChatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"` // "function"
	Function ChatFunctionCall `json:"function"`
}

// ChatFunctionCall 是工具调用的函数名与参数(参数是 JSON 字符串)。
type ChatFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ChatResponse 是 POST /v1/chat/completions 的非流式响应体。
type ChatResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []ChatChoice `json:"choices"`
	Usage   *ChatUsage   `json:"usage,omitempty"`
}

// ChatChoice 是一个候选回复。
type ChatChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason,omitempty"`
}

// ChatUsage 是 OpenAI 的**包容桶**口径:prompt_tokens 已含缓存,
// 缓存部分是 prompt_tokens_details.cached_tokens 子集。
type ChatUsage struct {
	PromptTokens        int                      `json:"prompt_tokens"`
	CompletionTokens    int                      `json:"completion_tokens"`
	TotalTokens         int                      `json:"total_tokens"`
	PromptTokensDetails *ChatPromptTokensDetails `json:"prompt_tokens_details,omitempty"`
}

// ChatPromptTokensDetails 是 prompt_tokens 的细分。
type ChatPromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

// ---------- 协议标识 ----------

// Format 标识一个请求/响应所属的协议。与 internal/channel 的 channel_type
// 不是同一个东西:channel_type 描述上游节点,Format 描述线上数据的形状。
// 一个 openai 节点收到 anthropic 入站请求时,两者就不同 —— 这正是需要转译的信号。
type Format string

const (
	FormatChatCompletions Format = "chat_completions"
	FormatAnthropic       Format = "anthropic_messages"
)

// String 返回 Format 的字符串形式。
func (f Format) String() string { return string(f) }

// NeedsTranslation 报告从 src 到 dst 是否需要协议转换。
// 空值视为"未知/无需转换",保证未接入转换的路径永远走零转换快路径。
func NeedsTranslation(src, dst Format) bool {
	if src == "" || dst == "" {
		return false
	}
	return src != dst
}
