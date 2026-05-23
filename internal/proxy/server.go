// Package proxy provides high-performance OpenAI multi-key proxy server
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"autogateway/internal/channel"
	"autogateway/internal/config"
	"autogateway/internal/encryption"
	app_errors "autogateway/internal/errors"
	"autogateway/internal/keypool"
	"autogateway/internal/models"
	"autogateway/internal/response"
	"autogateway/internal/router_engine"
	"autogateway/internal/services"
	"autogateway/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// ProxyServer represents the proxy server
type ProxyServer struct {
	keyProvider       *keypool.KeyProvider
	groupManager      *services.GroupManager
	subGroupManager   *services.SubGroupManager
	settingsManager   *config.SystemSettingsManager
	channelFactory    *channel.Factory
	requestLogService *services.RequestLogService
	aliasService      *services.AliasService
	selector          *router_engine.Selector
	encryptionSvc     encryption.Service
}

// NewProxyServer creates a new proxy server
func NewProxyServer(
	keyProvider *keypool.KeyProvider,
	groupManager *services.GroupManager,
	subGroupManager *services.SubGroupManager,
	settingsManager *config.SystemSettingsManager,
	channelFactory *channel.Factory,
	requestLogService *services.RequestLogService,
	aliasService *services.AliasService,
	selector *router_engine.Selector,
	encryptionSvc encryption.Service,
) (*ProxyServer, error) {
	return &ProxyServer{
		keyProvider:       keyProvider,
		groupManager:      groupManager,
		subGroupManager:   subGroupManager,
		settingsManager:   settingsManager,
		channelFactory:    channelFactory,
		requestLogService: requestLogService,
		aliasService:      aliasService,
		selector:          selector,
		encryptionSvc:     encryptionSvc,
	}, nil
}

// extractRequestedModel 从请求体中粗解析 "model" 字段;OpenAI/Anthropic/Gemini 三种 channel 都用这个字段.
// 兼容 JSON(chat/completions 等)与 multipart/form-data(audio/transcriptions 等需文件上传的端点).
// 解析失败或不存在 → 返回空字符串(等价于不做模型过滤).
func extractRequestedModel(contentType string, body []byte) string {
	return utils.ExtractRequestedModel(contentType, body)
}

// HandleProxy is the main entry point for proxy requests, refactored based on the stable .bak logic.
func (ps *ProxyServer) HandleProxy(c *gin.Context) {
	startTime := time.Now()
	groupName := c.Param("group_name")

	originalGroup, err := ps.groupManager.GetGroupByName(groupName)
	if err != nil {
		response.Error(c, app_errors.ParseDBError(err))
		return
	}

	// 先读 body 一次,以便:(1) 模型感知的子分组路由,(2) 后续 applyParamOverrides 复用.
	// 中间件可能已经读过 body 并 Restore (如 router_engine),所以 c.Request.Body 总是可读.
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logrus.Errorf("Failed to read request body: %v", err)
		response.Error(c, app_errors.NewAPIError(app_errors.ErrBadRequest, "Failed to read request body"))
		return
	}
	c.Request.Body.Close()
	requestedModel := extractRequestedModel(c.GetHeader("Content-Type"), bodyBytes)

	// 模型感知的 sub-group 选择(聚合分组才走;请求 model 为空时退化为普通 SWRR)
	subGroupName, err := ps.subGroupManager.SelectSubGroupForModel(originalGroup, requestedModel)
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"aggregate_group": originalGroup.Name,
			"requested_model": requestedModel,
			"error":           err,
		}).Error("Failed to select sub-group from aggregate")
		response.Error(c, app_errors.NewAPIError(app_errors.ErrNoKeysAvailable, "No available sub-groups"))
		return
	}

	group := originalGroup
	if subGroupName != "" {
		group, err = ps.groupManager.GetGroupByName(subGroupName)
		if err != nil {
			response.Error(c, app_errors.ParseDBError(err))
			return
		}
	} else if originalGroup.GroupType == "aggregate" && requestedModel != "" {
		// 聚合层契约: 没有任何 sub-group 声明能 serve 此 model → 直接 404,
		// 不允许退化到聚合自身 (aggregate group 没 upstreams, 也不该假设
		// 任意 sub-group 能透传该 model).
		logrus.WithFields(logrus.Fields{
			"aggregate_group": originalGroup.Name,
			"requested_model": requestedModel,
		}).Warn("Model not served by any sub-group; refusing to proxy")
		response.Error(c, app_errors.NewAPIError(app_errors.ErrResourceNotFound,
			fmt.Sprintf("model %q is not served by any provider in aggregate %q", requestedModel, originalGroup.Name)))
		return
	}

	channelHandler, err := ps.channelFactory.GetChannel(group)
	if err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, fmt.Sprintf("Failed to get channel for group '%s': %v", groupName, err)))
		return
	}

	finalBodyBytes, err := ps.applyParamOverrides(bodyBytes, group)
	if err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, fmt.Sprintf("Failed to apply parameter overrides: %v", err)))
		return
	}

	isStream := channelHandler.IsStreamRequest(c, bodyBytes)

	// 聚合分组的 failover 状态:已尝试过的子分组(防止重复)
	attemptedSubGroups := make(map[string]bool)
	if originalGroup.GroupType == "aggregate" && group != originalGroup {
		attemptedSubGroups[group.Name] = true
	}

	ps.executeRequestWithRetry(c, channelHandler, originalGroup, group, finalBodyBytes, isStream, startTime, 0, attemptedSubGroups, requestedModel)
}

// executeRequestWithRetry is the core recursive function for handling requests and retries.
// attemptedSubGroups 在聚合分组场景下记录已经尝试过的子分组,用于跨子分组 failover.
// requestedModel 在切换到下一个子分组时仍按 model 过滤候选.
func (ps *ProxyServer) executeRequestWithRetry(
	c *gin.Context,
	channelHandler channel.ChannelProxy,
	originalGroup *models.Group,
	group *models.Group,
	bodyBytes []byte,
	isStream bool,
	startTime time.Time,
	retryCount int,
	attemptedSubGroups map[string]bool,
	requestedModel string,
) {
	cfg := group.EffectiveConfig

	apiKey, err := ps.keyProvider.SelectKey(group.ID)
	if err != nil {
		logrus.Errorf("Failed to select a key for group %s on attempt %d: %v", group.Name, retryCount+1, err)
		ps.markRoutingCandidate(c, http.StatusServiceUnavailable)
		response.Error(c, app_errors.NewAPIError(app_errors.ErrNoKeysAvailable, err.Error()))
		ps.logRequest(c, originalGroup, group, nil, startTime, http.StatusServiceUnavailable, err, isStream, "", channelHandler, bodyBytes, models.RequestTypeFinal)
		return
	}

	upstreamURL, err := channelHandler.BuildUpstreamURL(c.Request.URL, originalGroup.Name)
	if err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, fmt.Sprintf("Failed to build upstream URL: %v", err)))
		return
	}

	var ctx context.Context
	var cancel context.CancelFunc
	if isStream {
		ctx, cancel = context.WithCancel(c.Request.Context())
	} else {
		timeout := time.Duration(cfg.RequestTimeout) * time.Second
		ctx, cancel = context.WithTimeout(c.Request.Context(), timeout)
	}
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, c.Request.Method, upstreamURL, bytes.NewReader(bodyBytes))
	if err != nil {
		logrus.Errorf("Failed to create upstream request: %v", err)
		response.Error(c, app_errors.ErrInternalServer)
		return
	}
	req.ContentLength = int64(len(bodyBytes))

	req.Header = c.Request.Header.Clone()

	// Clean up client auth key
	req.Header.Del("Authorization")
	req.Header.Del("X-Api-Key")
	req.Header.Del("X-Goog-Api-Key")

	// Apply model redirection
	finalBodyBytes, err := channelHandler.ApplyModelRedirect(req, bodyBytes, group)
	if err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrBadRequest, err.Error()))
		ps.logRequest(c, originalGroup, group, apiKey, startTime, http.StatusBadRequest, err, isStream, upstreamURL, channelHandler, bodyBytes, models.RequestTypeFinal)
		return
	}

	// Update request body if it was modified by redirection
	if !bytes.Equal(finalBodyBytes, bodyBytes) {
		req.Body = io.NopCloser(bytes.NewReader(finalBodyBytes))
		req.ContentLength = int64(len(finalBodyBytes))
	}

	channelHandler.ModifyRequest(req, apiKey, group)

	// Apply custom header rules
	if len(group.HeaderRuleList) > 0 {
		headerCtx := utils.NewHeaderVariableContextFromGin(c, group, apiKey)
		utils.ApplyHeaderRules(req, group.HeaderRuleList, headerCtx)
	}

	var client *http.Client
	if isStream {
		client = channelHandler.GetStreamClient()
		req.Header.Set("X-Accel-Buffering", "no")
	} else {
		client = channelHandler.GetHTTPClient()
	}

	resp, err := client.Do(req)
	if resp != nil {
		defer resp.Body.Close()
	}

	if err == nil && resp != nil && resp.StatusCode >= 200 && resp.StatusCode < 400 &&
		shouldValidateJSONSuccess(c.Request.URL.Path, isStream) {
		if validationErr := validateJSONSuccessResponse(resp); validationErr != nil {
			err = validationErr
		}
	}

	// Unified error handling for retries. 404 is normally passed through for
	// direct requests, but alias-routed candidates should still be marked as
	// failed so future alias selections can avoid unsupported destinations.
	isRetryableHTTPError := resp != nil && resp.StatusCode >= 400 &&
		(resp.StatusCode != http.StatusNotFound || ps.hasRoutingCandidate(c))
	if err != nil || isRetryableHTTPError {
		if err != nil && app_errors.IsIgnorableError(err) {
			logrus.Debugf("Client-side ignorable error for key %s, aborting retries: %v", utils.MaskAPIKey(apiKey.KeyValue), err)
			ps.logRequest(c, originalGroup, group, apiKey, startTime, 499, err, isStream, upstreamURL, channelHandler, bodyBytes, models.RequestTypeFinal)
			return
		}

		var statusCode int
		var errorMessage string
		var parsedError string

		if err != nil {
			statusCode = 500
			errorMessage = err.Error()
			parsedError = errorMessage
			logrus.Debugf("Request failed (attempt %d/%d) for key %s: %v", retryCount+1, cfg.MaxRetries, utils.MaskAPIKey(apiKey.KeyValue), err)
		} else {
			// HTTP-level error (status >= 400)
			statusCode = resp.StatusCode
			errorBody, readErr := io.ReadAll(resp.Body)
			if readErr != nil {
				logrus.Errorf("Failed to read error body: %v", readErr)
				errorBody = []byte("Failed to read error body")
			}

			errorBody = handleGzipCompression(resp, errorBody)
			errorMessage = string(errorBody)
			parsedError = app_errors.ParseUpstreamError(errorBody)
			logrus.Debugf("Request failed with status %d (attempt %d/%d) for key %s. Parsed Error: %s", statusCode, retryCount+1, cfg.MaxRetries, utils.MaskAPIKey(apiKey.KeyValue), parsedError)
		}

		ps.markRoutingCandidate(c, statusCode)

		// 使用解析后的错误信息更新密钥状态
		ps.keyProvider.UpdateStatus(apiKey, group, false, parsedError)

		// 当前子分组的 retry 用尽后,如果是聚合分组,尝试切换到下一个候选子分组(跨 sub-group failover)
		subGroupExhausted := retryCount >= cfg.MaxRetries
		canFailover := subGroupExhausted &&
			originalGroup.GroupType == "aggregate" &&
			!c.Writer.Written() // 已经向客户端写过数据(stream first byte)就不能再切

		if canFailover {
			if attemptedSubGroups == nil {
				attemptedSubGroups = make(map[string]bool)
			}
			attemptedSubGroups[group.Name] = true
			// 当前子分组累计失败 → 通知熔断器
			ps.subGroupManager.RecordSubGroupResult(originalGroup.ID, group.Name, false)
			nextName, selErr := ps.subGroupManager.SelectSubGroupForModelExcluding(originalGroup, requestedModel, attemptedSubGroups)
			if selErr == nil && nextName != "" && nextName != group.Name {
				if nextGroup, ge := ps.groupManager.GetGroupByName(nextName); ge == nil && nextGroup != nil {
					if nextChannel, ce := ps.channelFactory.GetChannel(nextGroup); ce == nil {
						logrus.WithFields(logrus.Fields{
							"aggregate_group": originalGroup.Name,
							"from_sub_group":  group.Name,
							"to_sub_group":    nextName,
							"requested_model": requestedModel,
							"reason":          parsedError,
						}).Info("Aggregate failover: switching sub-group")
						// 该子分组的失败也算一次最终,但请求整体继续
						ps.logRequest(c, originalGroup, group, apiKey, startTime, statusCode, errors.New(parsedError), isStream, upstreamURL, channelHandler, bodyBytes, models.RequestTypeRetry)
						// 重新做 param overrides(不同子分组可能有不同 overrides)
						nextBody, oerr := ps.applyParamOverrides(bodyBytes, nextGroup)
						if oerr != nil {
							nextBody = bodyBytes
						}
						ps.executeRequestWithRetry(c, nextChannel, originalGroup, nextGroup, nextBody, isStream, startTime, 0, attemptedSubGroups, requestedModel)
						return
					}
				}
			}
		}

		// 判断是否为最后一次尝试
		isLastAttempt := subGroupExhausted
		requestType := models.RequestTypeRetry
		if isLastAttempt {
			requestType = models.RequestTypeFinal
		}

		ps.logRequest(c, originalGroup, group, apiKey, startTime, statusCode, errors.New(parsedError), isStream, upstreamURL, channelHandler, bodyBytes, requestType)

		// 如果是最后一次尝试,直接返回错误,不再递归
		if isLastAttempt {
			// 该子分组的最终失败 → 通知熔断器(failover 路径已记录,不会重复:这里走的是 standard 直连或 aggregate 全部 sub-group 都尝试过)
			if originalGroup.GroupType == "aggregate" && group.Name != originalGroup.Name {
				ps.subGroupManager.RecordSubGroupResult(originalGroup.ID, group.Name, false)
			}
			var errorJSON map[string]any
			if err := json.Unmarshal([]byte(errorMessage), &errorJSON); err == nil {
				c.JSON(statusCode, errorJSON)
			} else {
				response.Error(c, app_errors.NewAPIErrorWithUpstream(statusCode, "UPSTREAM_ERROR", errorMessage))
			}
			return
		}

		ps.executeRequestWithRetry(c, channelHandler, originalGroup, group, bodyBytes, isStream, startTime, retryCount+1, attemptedSubGroups, requestedModel)
		return
	}

	// ps.keyProvider.UpdateStatus(apiKey, group, true) // 请求成功不再重置成功次数，减少IO消耗
	logrus.Debugf("Request for group %s succeeded on attempt %d with key %s", group.Name, retryCount+1, utils.MaskAPIKey(apiKey.KeyValue))
	ps.markRoutingCandidate(c, resp.StatusCode)

	// 通知熔断器:该子分组本次请求成功(若是聚合请求)
	if originalGroup.GroupType == "aggregate" && group.Name != originalGroup.Name {
		ps.subGroupManager.RecordSubGroupResult(originalGroup.ID, group.Name, true)
	}

	// Check if this is a model list request (needs special handling)
	if shouldInterceptModelList(c.Request.URL.Path, c.Request.Method) {
		ps.handleModelListResponse(c, resp, group, channelHandler)
	} else {
		for key, values := range resp.Header {
			for _, value := range values {
				c.Header(key, value)
			}
		}
		c.Status(resp.StatusCode)

		if isStream {
			ps.handleStreamingResponse(c, resp)
		} else {
			ps.handleNormalResponse(c, resp)
		}
	}

	ps.logRequest(c, originalGroup, group, apiKey, startTime, resp.StatusCode, nil, isStream, upstreamURL, channelHandler, bodyBytes, models.RequestTypeFinal)
}

func (ps *ProxyServer) markRoutingCandidate(c *gin.Context, statusCode int) {
	if ps.selector == nil {
		return
	}
	raw, ok := c.Get("router_engine.candidate")
	if !ok {
		return
	}
	candidate, ok := raw.(*router_engine.Candidate)
	if !ok || candidate == nil {
		return
	}
	ps.selector.MarkResponse(*candidate, statusCode)
}

func (ps *ProxyServer) hasRoutingCandidate(c *gin.Context) bool {
	raw, ok := c.Get("router_engine.candidate")
	if !ok {
		return false
	}
	candidate, ok := raw.(*router_engine.Candidate)
	return ok && candidate != nil
}

// logRequest is a helper function to create and record a request log.
func (ps *ProxyServer) logRequest(
	c *gin.Context,
	originalGroup *models.Group,
	group *models.Group,
	apiKey *models.APIKey,
	startTime time.Time,
	statusCode int,
	finalError error,
	isStream bool,
	upstreamAddr string,
	channelHandler channel.ChannelProxy,
	bodyBytes []byte,
	requestType string,
) {
	if ps.requestLogService == nil {
		return
	}

	var requestBodyToLog, userAgent string

	if group.EffectiveConfig.EnableRequestBodyLogging {
		requestBodyToLog = utils.TruncateString(string(bodyBytes), 65000)
		userAgent = c.Request.UserAgent()
	}

	duration := time.Since(startTime).Milliseconds()

	logEntry := &models.RequestLog{
		GroupID:      group.ID,
		GroupName:    group.Name,
		IsSuccess:    finalError == nil && statusCode < 400,
		SourceIP:     c.ClientIP(),
		StatusCode:   statusCode,
		RequestPath:  utils.TruncateString(c.Request.URL.String(), 500),
		Duration:     duration,
		UserAgent:    userAgent,
		RequestType:  requestType,
		IsStream:     isStream,
		UpstreamAddr: utils.TruncateString(upstreamAddr, 500),
		RequestBody:  requestBodyToLog,
	}

	// Set parent group
	if originalGroup != nil && originalGroup.GroupType == "aggregate" && originalGroup.ID != group.ID {
		logEntry.ParentGroupID = originalGroup.ID
		logEntry.ParentGroupName = originalGroup.Name
	}

	if channelHandler != nil && bodyBytes != nil {
		logEntry.Model = channelHandler.ExtractModel(c, bodyBytes)
	}

	if apiKey != nil {
		// 加密密钥值用于日志存储
		encryptedKeyValue, err := ps.encryptionSvc.Encrypt(apiKey.KeyValue)
		if err != nil {
			logrus.WithError(err).Error("Failed to encrypt key value for logging")
			logEntry.KeyValue = "failed-to-encryption"
		} else {
			logEntry.KeyValue = encryptedKeyValue
		}
		// 添加 KeyHash 用于反查
		logEntry.KeyHash = ps.encryptionSvc.Hash(apiKey.KeyValue)
	}

	if finalError != nil {
		logEntry.ErrorMessage = finalError.Error()
	}

	if err := ps.requestLogService.Record(logEntry); err != nil {
		logrus.Errorf("Failed to record request log: %v", err)
	}
}
