// 配置验证脚本
package main

import (
	"fmt"
	"reflect"

	"github.com/joho/godotenv"
	"github.com/sirupsen/logrus"

	"gpt-load/internal/config"
)

func main() {
	// 加载测试配置
	if err := godotenv.Load("test_config.env"); err != nil {
		logrus.Warnf("无法加载测试配置文件: %v", err)
	}

	// 加载配置
	cfg, err := config.LoadConfig()
	if err != nil {
		logrus.Fatalf("配置加载失败: %v", err)
	}

	fmt.Println("🔍 配置验证报告")
	fmt.Println("=" * 50)

	// 验证服务器配置
	fmt.Printf("📡 服务器配置:\n")
	fmt.Printf("   Host: %s\n", cfg.Server.Host)
	fmt.Printf("   Port: %d\n", cfg.Server.Port)
	fmt.Println()

	// 验证密钥配置
	fmt.Printf("🔑 密钥配置:\n")
	fmt.Printf("   文件路径: %s\n", cfg.Keys.FilePath)
	fmt.Printf("   起始索引: %d\n", cfg.Keys.StartIndex)
	fmt.Printf("   黑名单阈值: %d\n", cfg.Keys.BlacklistThreshold)
	fmt.Println()

	// 验证 OpenAI 配置
	fmt.Printf("🤖 OpenAI 配置:\n")
	fmt.Printf("   Base URL: %s\n", cfg.OpenAI.BaseURL)
	fmt.Printf("   超时时间: %dms\n", cfg.OpenAI.Timeout)
	fmt.Println()

	// 验证认证配置
	fmt.Printf("🔐 认证配置:\n")
	fmt.Printf("   启用状态: %t\n", cfg.Auth.Enabled)
	if cfg.Auth.Enabled {
		fmt.Printf("   密钥长度: %d\n", len(cfg.Auth.Key))
	}
	fmt.Println()

	// 验证 CORS 配置
	fmt.Printf("🌐 CORS 配置:\n")
	fmt.Printf("   启用状态: %t\n", cfg.CORS.Enabled)
	fmt.Printf("   允许来源: %v\n", cfg.CORS.AllowedOrigins)
	fmt.Println()

	// 验证性能配置
	fmt.Printf("⚡ 性能配置:\n")
	fmt.Printf("   最大连接数: %d\n", cfg.Performance.MaxSockets)
	fmt.Printf("   最大空闲连接数: %d\n", cfg.Performance.MaxFreeSockets)
	fmt.Printf("   Keep-Alive: %t\n", cfg.Performance.EnableKeepAlive)
	fmt.Printf("   禁用压缩: %t\n", cfg.Performance.DisableCompression)
	fmt.Printf("   缓冲区大小: %d bytes\n", cfg.Performance.BufferSize)
	fmt.Println()

	// 验证日志配置
	fmt.Printf("📝 日志配置:\n")
	fmt.Printf("   日志级别: %s\n", cfg.Log.Level)
	fmt.Printf("   日志格式: %s\n", cfg.Log.Format)
	fmt.Printf("   文件日志: %t\n", cfg.Log.EnableFile)
	if cfg.Log.EnableFile {
		fmt.Printf("   文件路径: %s\n", cfg.Log.FilePath)
	}
	fmt.Println()

	// 检查配置完整性
	fmt.Printf("✅ 配置完整性检查:\n")
	checkConfigCompleteness(cfg)

	fmt.Println("🎉 配置验证完成！")
}

func checkConfigCompleteness(cfg *config.Config) {
	v := reflect.ValueOf(cfg).Elem()
	t := reflect.TypeOf(cfg).Elem()

	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		fieldType := t.Field(i)

		if field.Kind() == reflect.Struct {
			checkStruct(field, fieldType.Name)
		}
	}
}

func checkStruct(v reflect.Value, name string) {
	t := v.Type()

	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		fieldType := t.Field(i)

		// 检查字段是否为零值
		if field.IsZero() && fieldType.Name != "Enabled" {
			fmt.Printf("   ⚠️  %s.%s 为零值\n", name, fieldType.Name)
		} else {
			fmt.Printf("   ✅ %s.%s 已配置\n", name, fieldType.Name)
		}
	}
}
