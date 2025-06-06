// Package proxy 高性能OpenAI多密钥代理服务器
// @author OpenAI Proxy Team
// @version 2.0.0
package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"openai-multi-key-proxy/internal/config"
	"openai-multi-key-proxy/internal/keymanager"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// ProxyServer 代理服务器
type ProxyServer struct {
	keyManager   *keymanager.KeyManager
	httpClient   *http.Client
	upstreamURL  *url.URL
	requestCount int64
	startTime    time.Time
}

// NewProxyServer 创建新的代理服务器
func NewProxyServer() (*ProxyServer, error) {
	// 解析上游URL
	upstreamURL, err := url.Parse(config.AppConfig.OpenAI.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("解析上游URL失败: %w", err)
	}

	// 创建密钥管理器
	keyManager := keymanager.NewKeyManager(config.AppConfig.Keys.FilePath)
	if err := keyManager.LoadKeys(); err != nil {
		return nil, fmt.Errorf("加载密钥失败: %w", err)
	}

	// 创建高性能HTTP客户端
	transport := &http.Transport{
		MaxIdleConns:          config.AppConfig.Performance.MaxSockets,
		MaxIdleConnsPerHost:   config.AppConfig.Performance.MaxFreeSockets,
		MaxConnsPerHost:       0, // 无限制，避免连接池瓶颈
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    config.AppConfig.Performance.DisableCompression,
		ForceAttemptHTTP2:     true,
		WriteBufferSize:       config.AppConfig.Performance.BufferSize,
		ReadBufferSize:        config.AppConfig.Performance.BufferSize,
	}

	// 配置 Keep-Alive
	if !config.AppConfig.Performance.EnableKeepAlive {
		transport.DisableKeepAlives = true
	}

	httpClient := &http.Client{
		Transport: transport,
		// 移除全局超时，使用更细粒度的超时控制
		// Timeout:   time.Duration(config.AppConfig.OpenAI.Timeout) * time.Millisecond,
	}

	return &ProxyServer{
		keyManager:  keyManager,
		httpClient:  httpClient,
		upstreamURL: upstreamURL,
		startTime:   time.Now(),
	}, nil
}

// SetupRoutes 设置路由
func (ps *ProxyServer) SetupRoutes() *gin.Engine {
	// 设置Gin模式
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()

	// 自定义日志中间件
	router.Use(ps.loggerMiddleware())

	// 恢复中间件
	router.Use(gin.Recovery())

	// CORS中间件
	if config.AppConfig.CORS.Enabled {
		router.Use(ps.corsMiddleware())
	}

	// 认证中间件（如果启用）
	if config.AppConfig.Auth.Enabled {
		router.Use(ps.authMiddleware())
	}

	// 管理端点
	router.GET("/health", ps.handleHealth)
	router.GET("/stats", ps.handleStats)
	router.GET("/blacklist", ps.handleBlacklist)
	router.GET("/reset-keys", ps.handleResetKeys)

	// 代理所有其他请求
	router.NoRoute(ps.handleProxy)

	return router
}

// corsMiddleware CORS中间件
func (ps *ProxyServer) corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := "*"
		if len(config.AppConfig.CORS.AllowedOrigins) > 0 && config.AppConfig.CORS.AllowedOrigins[0] != "*" {
			origin = strings.Join(config.AppConfig.CORS.AllowedOrigins, ",")
		}

		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
		c.Header("Access-Control-Max-Age", "86400")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusOK)
			return
		}

		c.Next()
	}
}

// authMiddleware 认证中间件
func (ps *ProxyServer) authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 管理端点不需要认证
		if strings.HasPrefix(c.Request.URL.Path, "/health") ||
			strings.HasPrefix(c.Request.URL.Path, "/stats") ||
			strings.HasPrefix(c.Request.URL.Path, "/blacklist") ||
			strings.HasPrefix(c.Request.URL.Path, "/reset-keys") {
			c.Next()
			return
		}

		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{
					"message":   "未提供认证信息",
					"type":      "authentication_error",
					"code":      "missing_authorization",
					"timestamp": time.Now().Format(time.RFC3339),
				},
			})
			c.Abort()
			return
		}

		if !strings.HasPrefix(authHeader, "Bearer ") {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{
					"message":   "认证格式错误",
					"type":      "authentication_error",
					"code":      "invalid_authorization_format",
					"timestamp": time.Now().Format(time.RFC3339),
				},
			})
			c.Abort()
			return
		}

		token := authHeader[7:] // 移除 "Bearer " 前缀
		if token != config.AppConfig.Auth.Key {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{
					"message":   "认证失败",
					"type":      "authentication_error",
					"code":      "invalid_authorization",
					"timestamp": time.Now().Format(time.RFC3339),
				},
			})
			c.Abort()
			return
		}

		c.Next()
	}
}

// loggerMiddleware 高性能日志中间件
func (ps *ProxyServer) loggerMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 检查是否启用请求日志
		if !config.AppConfig.Log.EnableRequest {
			// 不记录请求日志，只处理请求
			c.Next()
			// 只记录错误
			if c.Writer.Status() >= 400 {
				logrus.Errorf("Error %d: %s %s", c.Writer.Status(), c.Request.Method, c.Request.URL.Path)
			}
			return
		}

		start := time.Now()
		path := c.Request.URL.Path
		raw := c.Request.URL.RawQuery

		// 处理请求
		c.Next()

		// 计算响应时间
		latency := time.Since(start)

		// 获取基本信息
		method := c.Request.Method
		statusCode := c.Writer.Status()

		// 构建完整路径（避免字符串拼接）
		fullPath := path
		if raw != "" {
			fullPath = path + "?" + raw
		}

		// 获取密钥信息（如果存在）
		keyInfo := ""
		if keyIndex, exists := c.Get("keyIndex"); exists {
			if keyPreview, exists := c.Get("keyPreview"); exists {
				keyInfo = fmt.Sprintf(" - Key[%v] %v", keyIndex, keyPreview)
			}
		}

		// 获取重试信息（如果存在）
		retryInfo := ""
		if retryCount, exists := c.Get("retryCount"); exists {
			retryInfo = fmt.Sprintf(" - Retry[%d]", retryCount)
		}

		// 根据状态码选择日志级别
		if statusCode >= 500 {
			logrus.Errorf("%s %s - %d - %v%s%s", method, fullPath, statusCode, latency, keyInfo, retryInfo)
		} else if statusCode >= 400 {
			logrus.Warnf("%s %s - %d - %v%s%s", method, fullPath, statusCode, latency, keyInfo, retryInfo)
		} else {
			logrus.Infof("%s %s - %d - %v%s%s", method, fullPath, statusCode, latency, keyInfo, retryInfo)
		}
	}
}

// handleHealth 健康检查处理器
func (ps *ProxyServer) handleHealth(c *gin.Context) {
	uptime := time.Since(ps.startTime)
	stats := ps.keyManager.GetStats()
	requestCount := atomic.LoadInt64(&ps.requestCount)

	response := gin.H{
		"status":       "healthy",
		"uptime":       fmt.Sprintf("%.0fs", uptime.Seconds()),
		"requestCount": requestCount,
		"keysStatus": gin.H{
			"total":       stats.TotalKeys,
			"healthy":     stats.HealthyKeys,
			"blacklisted": stats.BlacklistedKeys,
		},
		"timestamp": time.Now().Format(time.RFC3339),
	}

	c.JSON(http.StatusOK, response)
}

// handleStats 统计信息处理器
func (ps *ProxyServer) handleStats(c *gin.Context) {
	uptime := time.Since(ps.startTime)
	stats := ps.keyManager.GetStats()
	requestCount := atomic.LoadInt64(&ps.requestCount)

	response := gin.H{
		"server": gin.H{
			"uptime":       fmt.Sprintf("%.0fs", uptime.Seconds()),
			"requestCount": requestCount,
			"startTime":    ps.startTime.Format(time.RFC3339),
			"version":      "2.0.0",
		},
		"keys":      stats,
		"timestamp": time.Now().Format(time.RFC3339),
	}

	c.JSON(http.StatusOK, response)
}

// handleBlacklist 黑名单处理器
func (ps *ProxyServer) handleBlacklist(c *gin.Context) {
	blacklistInfo := ps.keyManager.GetBlacklistDetails()
	c.JSON(http.StatusOK, blacklistInfo)
}

// handleResetKeys 重置密钥处理器
func (ps *ProxyServer) handleResetKeys(c *gin.Context) {
	result := ps.keyManager.ResetKeys()
	c.JSON(http.StatusOK, result)
}

// handleProxy 代理请求处理器
func (ps *ProxyServer) handleProxy(c *gin.Context) {
	startTime := time.Now()

	// 增加请求计数
	atomic.AddInt64(&ps.requestCount, 1)

	// 检查是否为流式请求
	isStreamRequest := strings.Contains(c.GetHeader("Accept"), "text/event-stream") ||
		strings.Contains(c.Request.URL.RawQuery, "stream=true") ||
		strings.Contains(c.Request.Header.Get("Content-Type"), "text/event-stream")

	var bodyBytes []byte
	var requestBody io.Reader = c.Request.Body

	// 为了支持重试，需要缓存请求体（包括流式请求）
	if c.Request.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(c.Request.Body)
		if err != nil {
			logrus.Errorf("读取请求体失败: %v", err)
			c.JSON(http.StatusBadRequest, gin.H{
				"error": gin.H{
					"message":   "读取请求体失败",
					"type":      "request_error",
					"code":      "invalid_request_body",
					"timestamp": time.Now().Format(time.RFC3339),
				},
			})
			return
		}
		requestBody = strings.NewReader(string(bodyBytes))
	}

	// 执行带重试的请求
	ps.executeRequestWithRetry(c, startTime, bodyBytes, requestBody, isStreamRequest, 0)
}

// executeRequestWithRetry 执行带重试的请求
func (ps *ProxyServer) executeRequestWithRetry(c *gin.Context, startTime time.Time, bodyBytes []byte, requestBody io.Reader, isStreamRequest bool, retryCount int) {
	// 获取密钥信息
	keyInfo, err := ps.keyManager.GetNextKey()
	if err != nil {
		logrus.Errorf("获取密钥失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"message":   "服务器内部错误: " + err.Error(),
				"type":      "server_error",
				"code":      "no_keys_available",
				"timestamp": time.Now().Format(time.RFC3339),
			},
		})
		return
	}

	// 设置密钥信息到上下文（用于日志）
	c.Set("keyIndex", keyInfo.Index)
	c.Set("keyPreview", keyInfo.Preview)

	// 设置重试信息到上下文
	if retryCount > 0 {
		c.Set("retryCount", retryCount)
	}

	// 准备请求体和内容长度
	var contentLength int64
	if len(bodyBytes) > 0 {
		// 使用缓存的请求体（支持重试）
		requestBody = strings.NewReader(string(bodyBytes))
		contentLength = int64(len(bodyBytes))
	}

	// 构建上游请求URL
	targetURL := *ps.upstreamURL
	targetURL.Path = c.Request.URL.Path
	targetURL.RawQuery = c.Request.URL.RawQuery

	// 创建带超时的上下文
	timeout := time.Duration(config.AppConfig.OpenAI.Timeout) * time.Millisecond
	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
	defer cancel()

	// 创建上游请求，直接使用原始请求体进行流式传输
	req, err := http.NewRequestWithContext(
		ctx,
		c.Request.Method,
		targetURL.String(),
		requestBody,
	)
	if err == nil && contentLength > 0 {
		req.ContentLength = contentLength
	}
	if err != nil {
		logrus.Errorf("创建上游请求失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"message":   "创建上游请求失败",
				"type":      "proxy_error",
				"code":      "request_creation_failed",
				"timestamp": time.Now().Format(time.RFC3339),
			},
		})
		return
	}

	// 复制请求头
	for key, values := range c.Request.Header {
		if key != "Host" {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
	}

	// 设置认证头
	req.Header.Set("Authorization", "Bearer "+keyInfo.Key)

	// 发送请求
	resp, err := ps.httpClient.Do(req)
	if err != nil {
		responseTime := time.Since(startTime)

		// 记录失败日志
		if retryCount > 0 {
			logrus.Warnf("重试请求失败 (第 %d 次): %v (响应时间: %v)", retryCount, err, responseTime)
		} else {
			logrus.Warnf("请求失败: %v (响应时间: %v)", err, responseTime)
		}

		// 异步记录失败
		go ps.keyManager.RecordFailure(keyInfo.Key, err)

		// 检查是否可以重试
		if retryCount < config.AppConfig.Keys.MaxRetries {
			logrus.Infof("准备重试请求 (第 %d/%d 次)", retryCount+1, config.AppConfig.Keys.MaxRetries)
			ps.executeRequestWithRetry(c, startTime, bodyBytes, nil, isStreamRequest, retryCount+1)
			return
		}

		// 达到最大重试次数
		c.JSON(http.StatusBadGateway, gin.H{
			"error": gin.H{
				"message":   fmt.Sprintf("代理请求失败 (已重试 %d 次): %s", retryCount, err.Error()),
				"type":      "proxy_error",
				"code":      "request_failed",
				"timestamp": time.Now().Format(time.RFC3339),
			},
		})
		return
	}
	defer resp.Body.Close()

	responseTime := time.Since(startTime)

	// 检查HTTP状态码是否需要重试
	// 429 (Too Many Requests) 和 5xx 服务器错误都需要重试
	if (resp.StatusCode == 429 || resp.StatusCode >= 500) && retryCount < config.AppConfig.Keys.MaxRetries {
		// 记录失败日志
		if retryCount > 0 {
			logrus.Warnf("重试请求返回错误 %d (第 %d 次) (响应时间: %v)", resp.StatusCode, retryCount, responseTime)
		} else {
			logrus.Warnf("请求返回错误 %d (响应时间: %v)", resp.StatusCode, responseTime)
		}

		// 异步记录失败
		go ps.keyManager.RecordFailure(keyInfo.Key, fmt.Errorf("HTTP %d", resp.StatusCode))

		// 关闭当前响应
		resp.Body.Close()

		logrus.Infof("准备重试请求 (第 %d/%d 次)", retryCount+1, config.AppConfig.Keys.MaxRetries)
		ps.executeRequestWithRetry(c, startTime, bodyBytes, nil, isStreamRequest, retryCount+1)
		return
	}

	// 异步记录统计信息（不阻塞响应）
	go func() {
		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			ps.keyManager.RecordSuccess(keyInfo.Key)
		} else if resp.StatusCode >= 400 {
			ps.keyManager.RecordFailure(keyInfo.Key, fmt.Errorf("HTTP %d", resp.StatusCode))
		}
	}()

	// 复制响应头
	for key, values := range resp.Header {
		for _, value := range values {
			c.Header(key, value)
		}
	}

	// 设置状态码
	c.Status(resp.StatusCode)

	// 优化流式响应传输
	if isStreamRequest {
		// 流式响应：启用实时刷新
		if flusher, ok := c.Writer.(http.Flusher); ok {
			// 使用优化的缓冲区进行流式复制
			buf := make([]byte, config.AppConfig.Performance.BufferSize)
			for {
				n, err := resp.Body.Read(buf)
				if n > 0 {
					_, writeErr := c.Writer.Write(buf[:n])
					if writeErr != nil {
						logrus.Errorf("写入流式响应失败: %v", writeErr)
						break
					}
					flusher.Flush() // 立即刷新到客户端
				}
				if err != nil {
					if err != io.EOF {
						logrus.Errorf("读取流式响应失败: %v", err)
					}
					break
				}
			}
		} else {
			// 降级到标准复制
			_, err = io.Copy(c.Writer, resp.Body)
			if err != nil {
				logrus.Errorf("复制流式响应失败: %v", err)
			}
		}
	} else {
		// 非流式响应：使用标准零拷贝
		_, err = io.Copy(c.Writer, resp.Body)
		if err != nil {
			logrus.Errorf("复制响应体失败: %v (响应时间: %v)", err, responseTime)
		}
	}
}

// Close 关闭代理服务器
func (ps *ProxyServer) Close() {
	if ps.keyManager != nil {
		ps.keyManager.Close()
	}
}
