# Auto 路由技术设计规格

## 1. 概述

本文档描述 Auto 复杂度路由功能的详细技术设计方案。

## 2. 代码结构

```
backend/internal/
├── autoroute/
│   ├── complexity.go    # 复杂度分类器
│   ├── middleware.go   # 路由中间件
│   ├── config.go       # 配置管理
│   └── types.go        # 类型定义
├── handler/
│   └── model_catalog_handler.go  # 模型目录 Handler
└── services/
    └── model_dedup_service.go    # 去重服务
```

## 3. 核心组件设计

### 3.1 类型定义 (types.go)

```go
package autoroute

// ComplexityLevel 复杂度级别
type ComplexityLevel string

const (
    Simple  ComplexityLevel = "simple"
    Medium  ComplexityLevel = "medium"
    Complex ComplexityLevel = "complex"
)

// RequestAnalysis 请求分析结果
type RequestAnalysis struct {
    EstimatedTokens int             `json:"estimated_tokens"`
    HasTools        bool            `json:"has_tools"`
    HasVision       bool            `json:"has_vision"`
    ToolCount       int             `json:"tool_count"`
    MessageCount    int             `json:"message_count"`
    MaxMsgLength    int             `json:"max_msg_length"`
    Level           ComplexityLevel `json:"level"`
}

// Classifier 配置
type ClassifierConfig struct {
    SimpleTokenThreshold  int `json:"simple_threshold"`
    ComplexTokenThreshold int `json:"complex_threshold"`
    ToolComplexityWeight  int `json:"tool_complexity_weight"`
    VisionComplexityWeight int `json:"vision_complexity_weight"`
}

// RouteConfig 路由配置
type RouteConfig struct {
    Enabled          bool                           `json:"enabled"`
    SimpleThreshold  int                             `json:"simple_threshold"`
    ComplexThreshold int                             `json:"complex_threshold"`
    GroupMapping     map[string]GroupComplexityMapping `json:"group_mapping"`
}

// GroupComplexityMapping 分组映射
type GroupComplexityMapping struct {
    SimpleGroup  string `json:"simple_group"`
    MediumGroup  string `json:"medium_group"`
    ComplexGroup string `json:"complex_group"`
}

// FallbackStrategy 回退策略
type FallbackStrategy struct {
    PrimaryGroup   string `json:"primary_group"`
    FallbackGroup  string `json:"fallback_group"`
    FallbackGroup2 string `json:"fallback_group2"`
}
```

### 3.2 复杂度分类器 (complexity.go)

**职责:**
- 解析请求体 JSON
- 估算 token 数量
- 检测 tools 和 vision
- 判断复杂度级别

**关键算法:**

```go
// Token 估算
// 中文: 1 token ≈ 1.5 字符
// 英文: 1 token ≈ 4 字符
// 混合内容取中间值
func estimateTokens(runeCount int) int {
    return runeCount * 4 / 5
}

// 复杂度分级
func (c *Classifier) classifyLevel(a *RequestAnalysis) ComplexityLevel {
    // 优先判断复杂条件
    if a.EstimatedTokens > c.ComplexTokenThreshold || a.HasVision || a.ToolCount > 3 {
        return Complex
    }
    // 中等条件
    if a.EstimatedTokens > c.SimpleTokenThreshold || a.ToolCount > 0 || a.MessageCount > 10 {
        return Medium
    }
    return Simple
}
```

**线程安全:** Classifier 是无状态的，可以安全并发使用。

### 3.3 路由中间件 (middleware.go)

**职责:**
- 拦截 chat completions 请求
- 调用分类器分析请求
- 根据映射选择目标分组
- 处理回退逻辑
- 重写 group_name 参数

**流程:**

```go
func Middleware(classifier *Classifier, configProvider func() *RouteConfig) gin.HandlerFunc {
    return func(c *gin.Context) {
        config := configProvider()
        if !config.Enabled {
            c.Next()
            return
        }

        if !isChatCompletions(c.Request.URL.Path) {
            c.Next()
            return
        }

        bodyBytes, err := io.ReadAll(c.Request.Body)
        if err != nil {
            c.Next()
            return
        }
        c.Request.Body.Close()

        analysis, err := classifier.Analyze(bodyBytes)
        if err != nil {
            logrus.WithError(err).Warn("Auto route: failed to analyze complexity")
            analysis = &RequestAnalysis{Level: Medium}
        }

        currentGroup := c.Param("group_name")
        mapping, found := config.GroupMapping[currentGroup]
        if !found {
            c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
            c.Next()
            return
        }

        targetGroup := selectTargetGroup(mapping, analysis.Level)
        if targetGroup == "" {
            c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
            c.Next()
            return
        }

        if !isGroupAvailable(targetGroup) {
            targetGroup = getFallbackGroup(mapping, analysis.Level)
        }

        c.Params = setParam(c.Params, "group_name", targetGroup)
        c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
        c.Next()
    }
}
```

### 3.4 配置管理 (config.go)

**职责:**
- 从 system_settings 表加载配置
- 提供配置变更回调
- 验证配置有效性

```go
type ConfigManager struct {
    db           *sql.DB
    cache        *sync.RWMutex
    config       *RouteConfig
    lastLoadTime time.Time
}

func (m *ConfigManager) Load() error {
    // 从 system_settings 表加载
}

func (m *ConfigManager) GetConfig() *RouteConfig {
    m.cache.RLock()
    defer m.cache.RUnlock()
    return m.config
}

func (m *ConfigManager) Validate(cfg *RouteConfig) error {
    // 验证配置有效性
}
```

## 4. 中间件集成

### 4.1 路由注册

```go
// router.go

func registerProxyRoutes(r *gin.Engine) {
    // ... 现有代码 ...

    // Auto 路由中间件
    classifier := autoroute.NewClassifier(&autoroute.ClassifierConfig{
        SimpleTokenThreshold:  2000,
        ComplexTokenThreshold: 8000,
        ToolComplexityWeight:  500,
        VisionComplexityWeight: 1000,
    })

    configProvider := func() *autoroute.RouteConfig {
        return configManager.GetConfig()
    }

    r.Use(autoroute.Middleware(classifier, configProvider))

    // 模型目录路由
    modelCatalogHandler := handler.NewModelCatalogHandler(groupManager)
    r.GET("/v1/models", modelCatalogHandler.ListModels)
}
```

## 5. 错误处理

| 错误场景 | 处理方式 |
|----------|----------|
| JSON 解析失败 | 透传，使用默认分级 |
| 配置缺失 | 透传 |
| 分组不可用 | 使用回退分组 |
| 所有分组不可用 | 透传，使用原分组 |

## 6. 日志规范

```go
// 路由日志 (Debug 级别)
logrus.WithFields(logrus.Fields{
    "original_group": currentGroup,
    "target_group":   targetGroup,
    "complexity":     analysis.Level,
    "tokens":         analysis.EstimatedTokens,
    "has_tools":      analysis.HasTools,
    "has_vision":     analysis.HasVision,
}).Debug("Auto route: redirected request")

// 错误日志 (Warn 级别)
logrus.WithError(err).Warn("Auto route: failed to analyze complexity")

// 回退日志 (Info 级别)
logrus.WithFields(logrus.Fields{
    "original": targetGroup,
    "fallback": fallbackGroup,
}).Info("Auto route: using fallback group")
```

## 7. 监控指标

```go
// 使用 Prometheus metrics
var (
    autoRouteDecisions = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "auto_route_decisions_total",
            Help: "Total number of auto route decisions",
        },
        []string{"level", "group"},
    )

    autoRouteErrors = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "auto_route_errors_total",
            Help: "Total number of auto route errors",
        },
        []string{"reason"},
    )

    autoRouteLatency = prometheus.NewHistogram(
        prometheus.HistogramOpts{
            Name:    "auto_route_latency_seconds",
            Help:    "Auto route latency in seconds",
            Buckets: prometheus.DefBuckets,
        },
    )
)
```

## 8. 配置数据结构

```json
{
    "auto_routing_config": {
        "enabled": true,
        "simple_threshold": 2000,
        "complex_threshold": 8000,
        "tool_complexity_weight": 500,
        "vision_complexity_weight": 1000,
        "group_mapping": {
            "gpt-4o": {
                "simple_group": "gpt-4o-lite",
                "medium_group": "gpt-4o-pro",
                "complex_group": "gpt-4.1-max"
            },
            "claude": {
                "simple_group": "claude-haiku-lite",
                "medium_group": "claude-sonnet-pro",
                "complex_group": "claude-opus-max"
            }
        }
    }
}
```

## 9. 单元测试策略

### 9.1 复杂度分类器测试

```go
func TestClassifier_Analyze(t *testing.T) {
    tests := []struct {
        name     string
        body     string
        expected ComplexityLevel
    }{
        {
            name:     "simple request",
            body:     `{"messages":[{"role":"user","content":"hello"}]}`,
            expected: Simple,
        },
        {
            name:     "request with tools",
            body:     `{"messages":[{"role":"user","content":"hello"}],"tools":[{"name":"func1"}]}`,
            expected: Medium,
        },
        {
            name:     "complex request with vision",
            body:     `{"messages":[{"role":"user","content":"describe","type":"image_url"}]}`,
            expected: Complex,
        },
    }
    // ...
}
```

### 9.2 中间件测试

```go
func TestMiddleware_Redirect(t *testing.T) {
    // Setup
    // Execute
    // Assert
}
```

## 10. 性能优化

### 10.1 已有的优化

- 分类器无状态，可并发使用
- 中间件直接修改 gin.Params，不创建新对象
- 使用 `io.NopCloser` 复用 bodyBytes

### 10.2 潜在优化点

- 配置缓存，减少数据库查询
- 热点请求的路由结果缓存
- 异步日志写入
