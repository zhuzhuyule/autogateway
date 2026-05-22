package utils

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"strings"
)

// ExtractRequestedModel 从请求体中粗解析 "model" 字段.
//
// 适用于所有 OpenAI 兼容 channel(OpenAI/Anthropic/Gemini)的路由前置判断与日志.
// 解析顺序:
//  1. 总是先尝试 JSON 解析(chat/completions、embeddings、images/generations 等主流端点).
//  2. 若 JSON 失败且 Content-Type 是 multipart/form-data,再用 mime/multipart 回退
//     (audio/transcriptions、audio/translations、images/edits 等需要文件上传的端点).
//
// 任何解析失败都静默返回空串——上层会退化为"不按 model 过滤路由"的兜底路径.
func ExtractRequestedModel(contentType string, body []byte) string {
	if len(body) == 0 {
		return ""
	}

	// 1) JSON 优先(覆盖绝大多数请求,且不依赖 Content-Type 头).
	var probe struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &probe); err == nil && probe.Model != "" {
		return probe.Model
	}

	// 2) multipart/form-data 回退:HTTP header 不区分大小写,做小写比较.
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "multipart/form-data") {
		if v := extractModelFromMultipart(contentType, body); v != "" {
			return v
		}
	}

	return ""
}

// extractModelFromMultipart 在 multipart/form-data body 里查找 name="model" 的表单字段.
// 顺序读取各 part,命中即返回,避免把后续的大音频/图片 part 也加载进内存.
// 任何解析错误都返回空串.
func extractModelFromMultipart(contentType string, body []byte) string {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ""
	}
	boundary, ok := params["boundary"]
	if !ok || boundary == "" {
		return ""
	}

	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := reader.NextPart()
		if err != nil {
			// io.EOF 或损坏 body,均返回空串.
			return ""
		}
		if part.FormName() != "model" {
			_ = part.Close()
			continue
		}
		// 找到 model 字段——读完该 part 即返回,不再消费后续 part.
		buf, readErr := io.ReadAll(part)
		_ = part.Close()
		if readErr != nil {
			return ""
		}
		return string(buf)
	}
}
