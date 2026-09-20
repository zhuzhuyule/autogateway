package keypool

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
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

// imageProbeTimeout is the floor for image-generation probes: producing an
// image is slow by nature and must not be killed by the short key-validation
// timeout. Larger configured values win.
const imageProbeTimeout = 5 * time.Minute

// redPixelPng encodes a 1x1 solid-red PNG at runtime as the vision probe
// input. Same rationale as silentWav: no external fixture needed.
func redPixelPng() []byte {
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&buf, img); err != nil {
		// Encoding an in-memory image never fails; the empty fallback only
		// degrades the probe request body.
		return nil
	}
	return buf.Bytes()
}

// testModalityConnectivity probes TTS / ASR / image-generation / vision
// models with a targeted request. Instead of chat's ValidateKey it reuses
// ChannelProxy primitives (BuildUpstreamURL / ModifyRequest / GetHTTPClient)
// and builds the request per modality, same approach as video_task_upstream.
//
// Constraint: only OpenAI-compatible channels are covered (/v1/audio/speech,
// /v1/audio/transcriptions, /v1/images/generations, and /v1/chat/completions
// with image_url content). Verdict:
//   - 2xx -> reachable (IsValid=true).
//   - ASR 400-class "request errors" (our silent test audio is often rejected
//     as too short/invalid) prove the endpoint and key are reachable -> still
//     IsValid=true, with the reason noted in Error.
//   - any other non-2xx -> unreachable (IsValid=false), with the parsed
//     upstream error.
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

	// Generating a real image takes 30s+ routinely, so the generic
	// key-validation timeout (20s by default) would falsely fail it;
	// raise the floor to imageProbeTimeout for this modality only.
	timeoutSec := group.EffectiveConfig.KeyValidationTimeoutSeconds
	if modality == "image" && timeoutSec < int(imageProbeTimeout/time.Second) {
		timeoutSec = int(imageProbeTimeout / time.Second)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
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
	case "image":
		// Image-generation probe: minimal prompt, single image. Size uses the
		// OpenAI baseline 1024x1024: most compatible providers only accept
		// the standard values, and custom small sizes tend to 400.
		payload, mErr := json.Marshal(map[string]any{
			"model":  modelName,
			"prompt": "a tiny red circle on white background",
			"n":      1,
			"size":   "1024x1024",
		})
		if mErr != nil {
			return "", nil, "", mErr
		}
		return "/v1/images/generations", bytes.NewReader(payload), "application/json", nil
	case "vision":
		// Vision-input probe: a chat/completions turn carrying one solid-color
		// image, verifying the model accepts multimodal image_url content
		// (text-only models fail here with 400, which is the point).
		payload, mErr := json.Marshal(map[string]any{
			"model": modelName,
			"messages": []any{map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "What color is this image? Answer in one word."},
					map[string]any{"type": "image_url", "image_url": map[string]any{
						"url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(redPixelPng()),
					}},
				},
			}},
			"max_tokens": 8,
		})
		if mErr != nil {
			return "", nil, "", mErr
		}
		return "/v1/chat/completions", bytes.NewReader(payload), "application/json", nil
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
