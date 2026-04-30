package channel

import (
	"context"
	"autogateway/internal/models"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
)

// ValidateResult is the outcome of a ValidateKey upstream call. It carries
// enough metadata for callers to construct a request_logs entry (visible in
// the Logs live-tail UI with errorContains filter). URL must not contain
// secrets — Gemini takes care to capture the URL before appending ?key=...
type ValidateResult struct {
	IsValid    bool
	StatusCode int    // upstream HTTP status; 0 if the call failed before getting a response
	URL        string // final upstream URL, secrets stripped
	Err        error
}

// ChannelProxy defines the interface for different API channel proxies.
type ChannelProxy interface {
	// BuildUpstreamURL constructs the target URL for the upstream service.
	BuildUpstreamURL(originalURL *url.URL, groupName string) (string, error)

	// IsConfigStale checks if the channel's configuration is stale compared to the provided group.
	IsConfigStale(group *models.Group) bool

	// GetHTTPClient returns the client for standard requests.
	GetHTTPClient() *http.Client

	// GetStreamClient returns the client for streaming requests.
	GetStreamClient() *http.Client

	// ModifyRequest allows the channel to add specific headers or modify the request
	ModifyRequest(req *http.Request, apiKey *models.APIKey, group *models.Group)

	// IsStreamRequest checks if the request is for a streaming response,
	IsStreamRequest(c *gin.Context, bodyBytes []byte) bool

	// ExtractModel extracts the model name from the request.
	ExtractModel(c *gin.Context, bodyBytes []byte) string

	// ValidateKey checks if the given API key is valid and returns the
	// upstream call metadata so callers (KeyValidator) can persist a
	// RequestLog row visible in the live tail UI.
	ValidateKey(ctx context.Context, apiKey *models.APIKey, group *models.Group) ValidateResult

	// ApplyModelRedirect applies model redirection based on the group's redirect rules.
	ApplyModelRedirect(req *http.Request, bodyBytes []byte, group *models.Group) ([]byte, error)

	// TransformModelList transforms the model list response based on redirect rules.
	TransformModelList(req *http.Request, bodyBytes []byte, group *models.Group) (map[string]any, error)
}
