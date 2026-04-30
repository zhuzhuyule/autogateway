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
