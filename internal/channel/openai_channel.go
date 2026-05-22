package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	app_errors "autogateway/internal/errors"
	"autogateway/internal/models"
	"autogateway/internal/utils"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

func init() {
	Register("openai", newOpenAIChannel)
}

type OpenAIChannel struct {
	*BaseChannel
}

func newOpenAIChannel(f *Factory, group *models.Group) (ChannelProxy, error) {
	base, err := f.newBaseChannel("openai", group)
	if err != nil {
		return nil, err
	}

	return &OpenAIChannel{
		BaseChannel: base,
	}, nil
}

// ModifyRequest sets the Authorization header for the OpenAI service.
func (ch *OpenAIChannel) ModifyRequest(req *http.Request, apiKey *models.APIKey, group *models.Group) {
	req.Header.Set("Authorization", "Bearer "+apiKey.KeyValue)
}

// IsStreamRequest checks if the request is for a streaming response using the pre-read body.
func (ch *OpenAIChannel) IsStreamRequest(c *gin.Context, bodyBytes []byte) bool {
	if strings.Contains(c.GetHeader("Accept"), "text/event-stream") {
		return true
	}

	if c.Query("stream") == "true" {
		return true
	}

	type streamPayload struct {
		Stream bool `json:"stream"`
	}
	var p streamPayload
	if err := json.Unmarshal(bodyBytes, &p); err == nil {
		return p.Stream
	}

	return false
}

func (ch *OpenAIChannel) ExtractModel(c *gin.Context, bodyBytes []byte) string {
	// 兼容 JSON 与 multipart/form-data(audio/transcriptions 等需要文件上传的端点).
	return utils.ExtractRequestedModel(c.GetHeader("Content-Type"), bodyBytes)
}

// ValidateKey checks if the given API key is valid by making a chat completion request.
func (ch *OpenAIChannel) ValidateKey(ctx context.Context, apiKey *models.APIKey, group *models.Group) ValidateResult {
	upstreamURL := ch.getUpstreamURL()
	if upstreamURL == nil {
		return ValidateResult{Err: fmt.Errorf("no upstream URL configured for channel %s", ch.Name)}
	}

	// 走 JoinUpstreamURL 与代理路径同一拼接逻辑, baseUrl=/v1 + endpoint=/v1/chat/completions
	// 不会双 /v1.
	endpointURL, err := url.Parse(ch.ValidationEndpoint)
	if err != nil {
		return ValidateResult{Err: fmt.Errorf("failed to parse validation endpoint: %w", err)}
	}
	finalURL := ch.JoinUpstreamURL(upstreamURL, endpointURL.Path)
	finalURL.RawQuery = endpointURL.RawQuery
	reqURL := finalURL.String()

	// Use a minimal, low-cost payload for validation
	payload := gin.H{
		"model": ch.TestModel,
		"messages": []gin.H{
			{"role": "user", "content": "hi"},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ValidateResult{URL: reqURL, Err: fmt.Errorf("failed to marshal validation payload: %w", err)}
	}

	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, bytes.NewBuffer(body))
	if err != nil {
		return ValidateResult{URL: reqURL, Err: fmt.Errorf("failed to create validation request: %w", err)}
	}
	req.Header.Set("Authorization", "Bearer "+apiKey.KeyValue)
	req.Header.Set("Content-Type", "application/json")

	// Apply custom header rules if available
	if len(group.HeaderRuleList) > 0 {
		headerCtx := utils.NewHeaderVariableContext(group, apiKey)
		utils.ApplyHeaderRules(req, group.HeaderRuleList, headerCtx)
	}

	resp, err := ch.HTTPClient.Do(req)
	if err != nil {
		return ValidateResult{URL: reqURL, Err: fmt.Errorf("failed to send validation request: %w", err)}
	}
	defer resp.Body.Close()

	// Any 2xx status code indicates the key is valid.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return ValidateResult{IsValid: true, StatusCode: resp.StatusCode, URL: reqURL}
	}

	// For non-200 responses, parse the body to provide a more specific error reason.
	errorBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return ValidateResult{
			StatusCode: resp.StatusCode,
			URL:        reqURL,
			Err:        fmt.Errorf("key is invalid (status %d), but failed to read error body: %w", resp.StatusCode, readErr),
		}
	}

	parsedError := app_errors.ParseUpstreamError(errorBody)
	return ValidateResult{
		StatusCode: resp.StatusCode,
		URL:        reqURL,
		Err:        fmt.Errorf("[status %d at %s] %s", resp.StatusCode, reqURL, parsedError),
	}
}
