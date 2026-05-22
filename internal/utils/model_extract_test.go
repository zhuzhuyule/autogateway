package utils

import (
	"bytes"
	"mime/multipart"
	"testing"
)

func buildMultipartBody(t *testing.T, fields map[string]string) (string, []byte) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatalf("write field %s: %v", k, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return w.FormDataContentType(), buf.Bytes()
}

func TestExtractRequestedModel_JSONHappyPath(t *testing.T) {
	got := ExtractRequestedModel("application/json", []byte(`{"model":"gpt-4o"}`))
	if got != "gpt-4o" {
		t.Errorf("expected gpt-4o, got %q", got)
	}
}

func TestExtractRequestedModel_JSONMissingModel(t *testing.T) {
	got := ExtractRequestedModel("application/json", []byte(`{"messages":[]}`))
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestExtractRequestedModel_EmptyBody(t *testing.T) {
	if got := ExtractRequestedModel("application/json", nil); got != "" {
		t.Errorf("expected empty for nil body, got %q", got)
	}
	if got := ExtractRequestedModel("multipart/form-data; boundary=xyz", []byte{}); got != "" {
		t.Errorf("expected empty for empty body, got %q", got)
	}
}

func TestExtractRequestedModel_MultipartHappyPath(t *testing.T) {
	ct, body := buildMultipartBody(t, map[string]string{
		"model":           "whisper-1",
		"response_format": "json",
	})
	if got := ExtractRequestedModel(ct, body); got != "whisper-1" {
		t.Errorf("expected whisper-1, got %q", got)
	}
}

func TestExtractRequestedModel_MultipartMissingModel(t *testing.T) {
	ct, body := buildMultipartBody(t, map[string]string{
		"response_format": "json",
	})
	if got := ExtractRequestedModel(ct, body); got != "" {
		t.Errorf("expected empty when no model field, got %q", got)
	}
}

func TestExtractRequestedModel_MultipartContentTypeCaseInsensitive(t *testing.T) {
	ct, body := buildMultipartBody(t, map[string]string{"model": "whisper-1"})
	upper := "MULTIPART/FORM-DATA" + ct[len("multipart/form-data"):]
	if got := ExtractRequestedModel(upper, body); got != "whisper-1" {
		t.Errorf("expected case-insensitive content-type match, got %q", got)
	}
}

func TestExtractRequestedModel_MultipartCorruptedBody(t *testing.T) {
	ct := "multipart/form-data; boundary=----xyz"
	// 完全损坏的 body — 应静默返回空串,而不是 panic 或抛错.
	if got := ExtractRequestedModel(ct, []byte("not a valid multipart body")); got != "" {
		t.Errorf("expected empty for corrupted body, got %q", got)
	}
}

func TestExtractRequestedModel_MultipartMissingBoundary(t *testing.T) {
	// boundary 缺失时,标准库会报错;我们应静默返回空串.
	if got := ExtractRequestedModel("multipart/form-data", []byte("any")); got != "" {
		t.Errorf("expected empty when boundary missing, got %q", got)
	}
}

func TestExtractRequestedModel_UnknownContentTypeWithJSONBody(t *testing.T) {
	// 没有 Content-Type 头时,只要 body 是合法 JSON 也应能提到 model
	// (兼容老逻辑:客户端不一定总传 Content-Type)
	if got := ExtractRequestedModel("", []byte(`{"model":"gpt-4o"}`)); got != "gpt-4o" {
		t.Errorf("expected gpt-4o from JSON body without content-type, got %q", got)
	}
}
