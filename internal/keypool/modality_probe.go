package keypool

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	neturl "net/url"
	"time"

	"autogateway/internal/channel"
	app_errors "autogateway/internal/errors"
	"autogateway/internal/models"
	"autogateway/internal/ratelimit"
)

// testModalityConnectivity 对 TTS / ASR 模型发一次针对性探活。不走 chat 的
// ValidateKey, 而是复用 ChannelProxy 接口原语(BuildUpstreamURL / ModifyRequest /
// GetHTTPClient)按模态构造请求 —— 与 video_task_upstream 发非-chat 上游请求同一手法。
//
// 约束: 仅适用于 OpenAI 兼容 channel 的音频端点(/v1/audio/speech、
// /v1/audio/transcriptions)。判定标准:
//   - 2xx → 可达(IsValid=true)。
//   - ASR 的 400 类"请求错"(我们的静音测试音频常被拒为 too short/invalid) 归因为
//     "端点+key 可达, 只是测试音频不合规" → 仍判 IsValid=true, 但在 Error 里注明。
//   - 其余非 2xx → 不可达(IsValid=false), 带上游解析错误。
func (s *KeyValidator) testModalityConnectivity(group *models.Group, modelName, modality string) (*ModelTestResult, error) {
	apiKey, err := s.keypoolProvider.SelectKey(group.ID, ratelimit.Limits{})
	if err != nil {
		return nil, err
	}
	if group.EffectiveConfig.AppUrl == "" {
		group.EffectiveConfig = s.SettingsManager.GetEffectiveConfig(group.Config)
	}

	groupCopy := *group
	groupCopy.TestModel = modelName
	ch, err := s.channelFactory.BuildChannel(&groupCopy)
	if err != nil {
		return nil, fmt.Errorf("failed to build channel for group %s: %w", group.Name, err)
	}

	reqPath, body, contentType, err := buildModalityRequest(modality, modelName)
	if err != nil {
		return nil, err
	}

	reqURL, err := ch.BuildUpstreamURL(&neturl.URL{Path: reqPath}, group.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to build upstream url: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(group.EffectiveConfig.KeyValidationTimeoutSeconds)*time.Second)
	defer cancel()

	out := &ModelTestResult{Model: modelName, URL: reqURL}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	ch.ModifyRequest(req, apiKey, &groupCopy) // 注入 Authorization 等

	startedAt := time.Now()
	resp, err := ch.GetHTTPClient().Do(req)
	out.DurationMs = time.Since(startedAt).Milliseconds()
	if err != nil {
		out.Error = err.Error()
		s.recordTestLog(apiKey, &groupCopy, channel.ValidateResult{StatusCode: 0, URL: reqURL}, out.DurationMs, out.Error)
		return out, nil
	}
	defer resp.Body.Close()

	out.StatusCode = resp.StatusCode
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		out.IsValid = true
	default:
		parsed := app_errors.ParseUpstreamError(respBody)
		// ASR 的静音测试音频常被上游判为 request_error(too short / invalid audio),
		// 这恰恰证明端点与 key 是可达的 —— 判为可达, 只在 Error 里注明原因。
		if modality == "asr" && app_errors.Classify(resp.StatusCode, parsed).ShouldFailFast() {
			out.IsValid = true
			out.Error = fmt.Sprintf("reachable (upstream rejected test audio: %s)", parsed)
		} else {
			out.Error = fmt.Sprintf("[status %d] %s", resp.StatusCode, parsed)
		}
	}

	s.recordTestLog(apiKey, &groupCopy, channel.ValidateResult{IsValid: out.IsValid, StatusCode: out.StatusCode, URL: reqURL}, out.DurationMs, out.Error)
	return out, nil
}

// buildModalityRequest 按模态返回上游端点路径、请求体、Content-Type。
func buildModalityRequest(modality, modelName string) (path string, body io.Reader, contentType string, err error) {
	switch modality {
	case "tts":
		payload, mErr := json.Marshal(map[string]any{
			"model": modelName,
			"input": "hi",
			"voice": "alloy",
		})
		if mErr != nil {
			return "", nil, "", mErr
		}
		return "/v1/audio/speech", bytes.NewReader(payload), "application/json", nil
	case "asr":
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, cErr := mw.CreateFormFile("file", "probe.wav")
		if cErr != nil {
			return "", nil, "", cErr
		}
		if _, wErr := fw.Write(silentWav()); wErr != nil {
			return "", nil, "", wErr
		}
		_ = mw.WriteField("model", modelName)
		_ = mw.WriteField("response_format", "json")
		if cErr := mw.Close(); cErr != nil {
			return "", nil, "", cErr
		}
		return "/v1/audio/transcriptions", &buf, mw.FormDataContentType(), nil
	default:
		return "", nil, "", fmt.Errorf("unsupported modality %q", modality)
	}
}

// silentWav 运行时生成一个极小的 mono 8kHz 16-bit PCM 静音 WAV(约 0.1s),
// 作为 ASR 探活的测试音频。无需外部 fixture 文件。
func silentWav() []byte {
	const sampleRate = 8000
	const numSamples = 800 // 0.1s
	dataSize := numSamples * 2

	var buf bytes.Buffer
	buf.WriteString("RIFF")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(36+dataSize))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(16))            // fmt chunk size
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))             // PCM
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))             // mono
	_ = binary.Write(&buf, binary.LittleEndian, uint32(sampleRate))    // sample rate
	_ = binary.Write(&buf, binary.LittleEndian, uint32(sampleRate*2))  // byte rate (mono*16bit)
	_ = binary.Write(&buf, binary.LittleEndian, uint16(2))             // block align
	_ = binary.Write(&buf, binary.LittleEndian, uint16(16))            // bits per sample
	buf.WriteString("data")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(dataSize))
	buf.Write(make([]byte, dataSize)) // 静音采样
	return buf.Bytes()
}
