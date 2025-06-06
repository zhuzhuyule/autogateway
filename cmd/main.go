// Package main OpenAI多密钥代理服务器主入口
// @author OpenAI Proxy Team
// @version 2.0.0
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"openai-multi-key-proxy/internal/config"
	"openai-multi-key-proxy/internal/proxy"

	"github.com/sirupsen/logrus"
)

func main() {
	// 加载配置
	cfg, err := config.LoadConfig()
	if err != nil {
		logrus.Fatalf("加载配置失败: %v", err)
	}

	// 配置日志
	setupLogger(cfg)

	// 显示启动信息
	displayStartupInfo(cfg)

	// 创建代理服务器
	proxyServer, err := proxy.NewProxyServer()
	if err != nil {
		logrus.Fatalf("❌ 创建代理服务器失败: %v", err)
	}
	defer proxyServer.Close()

	// 设置路由
	router := proxyServer.SetupRoutes()

	// 创建HTTP服务器，优化超时配置
	server := &http.Server{
		Addr:           fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler:        router,
		ReadTimeout:    60 * time.Second,  // 增加读超时，支持大文件上传
		WriteTimeout:   300 * time.Second, // 增加写超时，支持流式响应
		IdleTimeout:    120 * time.Second, // 增加空闲超时，复用连接
		MaxHeaderBytes: 1 << 20,           // 1MB header limit
	}

	// 启动服务器
	go func() {
		logrus.Infof("🚀 OpenAI 多密钥代理服务器启动成功")
		logrus.Infof("📡 服务地址: http://%s:%d", cfg.Server.Host, cfg.Server.Port)
		logrus.Infof("📊 统计信息: http://%s:%d/stats", cfg.Server.Host, cfg.Server.Port)
		logrus.Infof("💚 健康检查: http://%s:%d/health", cfg.Server.Host, cfg.Server.Port)
		logrus.Infof("🔄 重置密钥: http://%s:%d/reset-keys", cfg.Server.Host, cfg.Server.Port)
		logrus.Infof("🚫 黑名单查询: http://%s:%d/blacklist", cfg.Server.Host, cfg.Server.Port)
		logrus.Info("")

		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logrus.Fatalf("❌ 服务器启动失败: %v", err)
		}
	}()

	// 等待中断信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logrus.Info("🛑 收到关闭信号，正在优雅关闭服务器...")

	// 优雅关闭
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logrus.Errorf("❌ 服务器关闭失败: %v", err)
	} else {
		logrus.Info("✅ 服务器已优雅关闭")
	}
}

// displayStartupInfo 显示启动信息
func displayStartupInfo(cfg *config.Config) {
	logrus.Info("🚀 OpenAI 多密钥代理服务器 v2.0.0 (Go版本)")
	logrus.Info("")

	// 显示配置
	config.DisplayConfig(cfg)
	logrus.Info("")
}

// setupLogger 配置日志系统
func setupLogger(cfg *config.Config) {
	// 设置日志级别
	level, err := logrus.ParseLevel(cfg.Log.Level)
	if err != nil {
		logrus.Warnf("无效的日志级别 '%s'，使用默认级别 info", cfg.Log.Level)
		level = logrus.InfoLevel
	}
	logrus.SetLevel(level)

	// 设置日志格式
	switch cfg.Log.Format {
	case "json":
		logrus.SetFormatter(&logrus.JSONFormatter{
			TimestampFormat: "2006-01-02 15:04:05",
		})
	default:
		logrus.SetFormatter(&logrus.TextFormatter{
			FullTimestamp:   true,
			ForceColors:     true,
			TimestampFormat: "2006-01-02 15:04:05",
		})
	}

	// 配置文件日志
	if cfg.Log.EnableFile {
		// 创建日志目录
		if err := os.MkdirAll(filepath.Dir(cfg.Log.FilePath), 0755); err != nil {
			logrus.Warnf("创建日志目录失败: %v", err)
		} else {
			// 打开日志文件
			file, err := os.OpenFile(cfg.Log.FilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
			if err != nil {
				logrus.Warnf("打开日志文件失败: %v", err)
			} else {
				// 同时输出到控制台和文件
				logrus.SetOutput(io.MultiWriter(os.Stdout, file))
			}
		}
	}
}
