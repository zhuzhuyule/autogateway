package proxy

import (
	app_errors "autogateway/internal/errors"
	"autogateway/internal/models"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/sirupsen/logrus"
)

func (ps *ProxyServer) applyParamOverrides(bodyBytes []byte, group *models.Group) ([]byte, error) {
	if len(group.ParamOverrides) == 0 || len(bodyBytes) == 0 {
		return bodyBytes, nil
	}

	var requestData map[string]any
	if err := json.Unmarshal(bodyBytes, &requestData); err != nil {
		logrus.Warnf("failed to unmarshal request body for param override, passing through: %v", err)
		return bodyBytes, nil
	}

	if isNestedOverrides(group.ParamOverrides) {
		modelName, _ := requestData["model"].(string)
		applyNested(requestData, group.ParamOverrides, modelName)
	} else {
		for key, value := range group.ParamOverrides {
			requestData[key] = value
		}
	}

	return json.Marshal(requestData)
}

// injectStreamUsage 给 OpenAI chat/completions 流式请求注入
// stream_options.include_usage=true, 让上游在流尾多发一个 usage 帧 —— 否则
// 客户端 (Cursor/Cline 等) 不主动传 include_usage 时, OpenAI 格式流式请求根本
// 拿不到 token 用量, ①成本可观测性对这部分流量会系统性少算。
//
// 仅对含 "messages" 的请求体生效 (chat), 避免波及 embeddings 等其它 OpenAI 端点。
// 尊重客户端显式设置: 已带 include_usage (无论真假) 则原样不动。调用方负责
// 只在 isStream && ChannelType=="openai" && 开关开启时调用。
//
// 代价: 注入的 usage 帧也会透传给客户端 —— 严格假设每帧都有 choices[0] 的老旧
// 客户端可能不兼容, 故该行为由 group 级 ForceStreamUsage 开关控制, 默认关闭。
func injectStreamUsage(bodyBytes []byte) []byte {
	if len(bodyBytes) == 0 {
		return bodyBytes
	}
	var obj map[string]any
	if err := json.Unmarshal(bodyBytes, &obj); err != nil {
		return bodyBytes
	}
	if _, isChat := obj["messages"]; !isChat {
		return bodyBytes
	}
	if so, ok := obj["stream_options"].(map[string]any); ok {
		if _, has := so["include_usage"]; has {
			return bodyBytes // 客户端已显式设置, 不覆盖
		}
		so["include_usage"] = true
	} else {
		obj["stream_options"] = map[string]any{"include_usage": true}
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return bodyBytes
	}
	return out
}

// isNestedOverrides reports whether every value in the override map is itself
// a JSON object — that's the marker for the {"*": {...}, "model-id": {...}}
// shape. Any non-object value collapses us back to the legacy flat shape.
func isNestedOverrides(o map[string]any) bool {
	if len(o) == 0 {
		return false
	}
	for _, v := range o {
		if _, ok := v.(map[string]any); !ok {
			return false
		}
	}
	return true
}

func applyNested(requestData, overrides map[string]any, modelName string) {
	if star, ok := overrides["*"].(map[string]any); ok {
		for k, v := range star {
			requestData[k] = v
		}
	}
	if modelName == "" {
		return
	}
	if specific, ok := overrides[modelName].(map[string]any); ok {
		for k, v := range specific {
			requestData[k] = v
		}
	}
}

func shouldValidateJSONSuccess(path string, isStream bool) bool {
	if isStream {
		return false
	}
	return strings.Contains(path, "chat/completions") ||
		strings.Contains(path, "messages") ||
		strings.Contains(path, "generateContent")
}

func validateJSONSuccessResponse(resp *http.Response) error {
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read upstream success response: %w", err)
	}
	resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	bodyBytes = handleGzipCompression(resp, bodyBytes)
	if json.Valid(bodyBytes) {
		return nil
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "unknown"
	}
	return fmt.Errorf("upstream returned non-JSON success response: status=%d content_type=%s", resp.StatusCode, contentType)
}

// logUpstreamError provides a centralized way to log errors from upstream interactions.
func logUpstreamError(context string, err error) {
	if err == nil {
		return
	}
	if app_errors.IsIgnorableError(err) {
		logrus.Debugf("Ignorable upstream error in %s: %v", context, err)
	} else {
		logrus.Errorf("Upstream error in %s: %v", context, err)
	}
}

// handleGzipCompression checks for gzip encoding and decompresses the body if necessary.
func handleGzipCompression(resp *http.Response, bodyBytes []byte) []byte {
	if resp.Header.Get("Content-Encoding") == "gzip" {
		reader, gzipErr := gzip.NewReader(bytes.NewReader(bodyBytes))
		if gzipErr != nil {
			logrus.Warnf("Failed to create gzip reader for error body: %v", gzipErr)
			return bodyBytes
		}
		defer reader.Close()

		decompressedBody, readAllErr := io.ReadAll(reader)
		if readAllErr != nil {
			logrus.Warnf("Failed to decompress gzip error body: %v", readAllErr)
			return bodyBytes
		}
		return decompressedBody
	}
	return bodyBytes
}
