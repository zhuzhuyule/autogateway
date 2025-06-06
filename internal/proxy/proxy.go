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
		// 只在非生产环境或调试模式下记录详细日志
		if gin.Mode() == gin.ReleaseMode {
			// 生产模式下只记录错误和关键信息
			c.Next()
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

		// 简化日志输出，减少格式化开销
		logrus.Infof("%s %s - %d - %v%s", method, fullPath, statusCode, latency, keyInfo)
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

	// 直接使用请求体，避免完整读取到内存
	var requestBody io.Reader = c.Request.Body
	var contentLength int64 = c.Request.ContentLength

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
		logrus.Errorf("代理请求失败: %v (响应时间: %v)", err, responseTime)

		// 异步记录失败
		go ps.keyManager.RecordFailure(keyInfo.Key, err)

		c.JSON(http.StatusBadGateway, gin.H{
			"error": gin.H{
				"message":   "代理请求失败: " + err.Error(),
				"type":      "proxy_error",
				"code":      "request_failed",
				"timestamp": time.Now().Format(time.RFC3339),
			},
		})
		return
	}
	defer resp.Body.Close()

	responseTime := time.Since(startTime)

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

	// 流式复制响应体（零拷贝）
	_, err = io.Copy(c.Writer, resp.Body)
	if err != nil {
		logrus.Errorf("复制响应体失败: %v (响应时间: %v)", err, responseTime)
	}
}

// Close 关闭代理服务器
func (ps *ProxyServer) Close() {
	if ps.keyManager != nil {
		ps.keyManager.Close()
	}
}
