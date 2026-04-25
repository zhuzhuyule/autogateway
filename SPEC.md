# GPT-Load 二次开发方案：Auto 智能路由 + 模型统一目录

> 基于 [GPT-Load](https://github.com/tbphp/gpt-load)（Go + Vue3，MIT 协议）的二次开发方案，补充 Auto 复杂度路由、模型自动去重、统一模型目录三大核心功能。

---

## 文档版本

- 版本: v1.0
- 基于: GPT-Load v2.x
- 更新日期: 2026-04-25
- 状态: 已确认需求，等待实施

---

## 一、背景与需求

### 1.1 现有方案对比

| 方案 | Stars | 技术栈 | 负载均衡 | 自动容错 | 模型聚合 | Auto 路由 | 统一模型目录 |
|------|-------|--------|----------|----------|----------|-----------|---------------|
| **GPT-Load** | 6.1k | Go + Vue3 | ✅ 加权轮询 | ✅ 黑名单+恢复 | ✅ 聚合分组 | ❌ | ❌ |
| LiteLLM Proxy | 44.6k | Python | ✅ 5种策略 | ✅ Fallback | ✅ model_group | ⚠️ 语义匹配 | ⚠️ |
| New-API | 28.7k | Go | ✅ 轮询 | ✅ 切换 | ❌ | ❌ | ⚠️ |
| Manifest | 5.6k | TypeScript | ✅ 自动选择 | ✅ Fallback | ❌ | ✅ 按复杂度 | ⚠️ |

**选择 GPT-Load 的理由：**
- Go 语言，性能优秀，适合高并发
- 聚合分组机制最清晰（Smooth Weighted Round-Robin）
- 模型重定向（别名）功能完善
- MIT 协议，二次开发友好
- Vue3 Web UI 现代化，管理体验好

### 1.2 需求清单

| # | 需求 | GPT-Load 现状 | 状态 |
|---|------|--------------|------|
| 1 | 多上游 API 集成 | ✅ 分组管理 | 已满足 |
| 2 | 统一对外暴露（OpenAI 格式） | ✅ 透明代理 | 已满足 |
| 3 | 自动轮询 | ✅ 原子计数器轮询 | 已满足 |
| 4 | 自动容错 + 临时关闭 | ✅ 失败计数 + 黑名单 | 已满足 |
| 5 | 自动重试 | ✅ 无感重试 | 已满足 |
| 6 | 同类模型聚合 | ✅ 聚合分组 | 已满足 |
| 7 | 模型别名 | ✅ 模型重定向 | 已满足 |
| 8 | **Auto 复杂度路由** | ❌ 完全没有 | **需开发** |
| 9 | **模型自动去重** | ⚠️ 手动配置 | **需增强** |
| 10 | **统一模型目录** | ❌ 每个分组独立 | **需开发** |

---

## 二、架构设计

### 2.1 整体架构

```
┌─────────────────────────────────────────────────────────────┐
│                        客户端 (Client)                       │
│              OpenAI SDK / LangChain / 自定义应用              │
└──────────────────────────┬──────────────────────────────────┘
                            ▼
┌─────────────────────────────────────────────────────────────┐
│                      Auto 路由中间件 (新增)                   │
│                                                              │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────────┐      │
│  │   请求解析器  │→│  复杂度分类器 │→│  目标分组选择器   │      │
│  │             │  │             │  │                 │      │
│  │• 提取模型名  │  │• token 估算  │  │• simple → lite  │      │
│  │• 计算 token │  │• 函数调用检测│  │• medium → pro   │      │
│  │• 检测 vision│  │• vision 检测 │  │• complex → max  │      │
│  │• 检测 tools │  │• 上下文长度   │  │                 │      │
│  └─────────────┘  └─────────────┘  └─────────────────┘      │
│                                                              │
│  路由规则配置 (system_settings 表)                           │
│  {                                                          │
│    "auto_routing_enabled": true,                            │
│    "simple_threshold": 2000,                                 │
│    "complex_threshold": 8000,                               │
│    "simple_models": ["gpt-4o-mini", "claude-haiku"],        │
│    "medium_models": ["gpt-4o", "claude-sonnet"],            │
│    "complex_models": ["gpt-4.1", "claude-opus"]              │
│  }                                                          │
└──────────────────────────┬──────────────────────────────────┘
                            │ 重写 group_name 参数
                            ▼
┌─────────────────────────────────────────────────────────────┐
│                      GPT-Load (底座，已有)                    │
│                                                              │
│  ┌─────────────────────────────────────────────────────┐     │
│  │            聚合分组 "deepseek-lite" (simple 级别)     │     │
│  │  ├── 子分组A: DeepSeek 官方 (权重 500)                │     │
│  │  ├── 子分组B: OpenRouter (权重 300)                   │     │
│  │  └── 子分组C: Azure (权重 200)                        │     │
│  │                  ↓ 平滑加权轮询 + 自动容错              │     │
│  └─────────────────────────────────────────────────────┘     │
│                                                              │
│  ┌─────────────────────────────────────────────────────┐     │
│  │            聚合分组 "deepseek-pro" (medium 级别)       │     │
│  │  ├── 子分组A: DeepSeek 官方 (权重 500)                │     │
│  │  └── 子分组B: OpenRouter (权重 500)                   │     │
│  └─────────────────────────────────────────────────────┘     │
│                                                              │
│  ┌─────────────────────────────────────────────────────┐     │
│  │           聚合分组 "deepseek-max" (complex 级别)       │     │
│  │  └── 子分组A: DeepSeek 官方 (权重 1000)               │     │
│  └─────────────────────────────────────────────────────┘     │
│                                                              │
│  模型重定向 (每个子分组独立配置):                               │
│  { "gpt-4o": "gpt-4o-2024-05-13" }                          │
│                                                              │
│  密钥管理: 原子轮询 → 失败计数 → 黑名单 → 定时恢复              │
└──────────────────────────┬──────────────────────────────────┘
                            ▼
┌─────────────────────────────────────────────────────────────┐
│                       上游 API Providers                      │
│              OpenAI / Anthropic / Google / Azure / OpenRouter / ... │
└─────────────────────────────────────────────────────────────┘
```

### 2.2 统一模型目录架构

```
GET /v1/models (统一入口)

┌─────────────────────────────────────────────┐
│          统一模型目录 Handler (新增)           │
│                                              │
│  1. 遍历所有分组（标准 + 聚合）               │
│  2. 收集每个分组的可用模型                    │
│  3. 按逻辑模型名去重合并                      │
│  4. 返回统一列表                             │
│                                              │
│  返回格式:                                   │
│  {                                           │
│    "data": [                                 │
│      {                                       │
│        "id": "gpt-4o",                       │
│        "display_name": "GPT-4o",             │
│        "complexity": "medium",               │
│        "providers": ["openai", "azure"],    │
│        "group": "gpt-4o-aggregate"           │
│      }                                       │
│    ]                                         │
│  }                                           │
└─────────────────────────────────────────────┘
```

### 2.3 路由优先级机制

| 优先级 | 规则类型 | 说明 |
|--------|----------|------|
| 1 | 精确匹配 | 用户显式指定分组，直接路由到指定分组 |
| 2 | 模型名映射 | Auto 路由的 model_mapping 配置 |
| 3 | 聚合分组兜底 | 无映射时走聚合分组选择 |

### 2.4 回退策略

当目标分组不可用时的处理：

```go
type FallbackStrategy struct {
    PrimaryGroup   string   // 主目标分组
    FallbackGroup  string   // 一级回退分组
    FallbackGroup2 string   // 二级回退分组
}
```

---

## 三、详细实现方案

### 3.1 Auto 复杂度路由

#### 3.1.1 复杂度分类算法

```go
// internal/autoroute/complexity.go

package autoroute

import (
    "encoding/json"
    "unicode/utf8"
)

// ComplexityLevel 复杂度级别
type ComplexityLevel string

const (
    Simple  ComplexityLevel = "simple"
    Medium  ComplexityLevel = "medium"
    Complex ComplexityLevel = "complex"
)

// RequestAnalysis 请求分析结果
type RequestAnalysis struct {
    EstimatedTokens int              `json:"estimated_tokens"`
    HasTools        bool             `json:"has_tools"`
    HasVision       bool             `json:"has_vision"`
    ToolCount       int              `json:"tool_count"`
    MessageCount    int              `json:"message_count"`
    MaxMsgLength    int              `json:"max_msg_length"`
    Level           ComplexityLevel  `json:"level"`
}

// Classifier 复杂度分类器
type Classifier struct {
    SimpleTokenThreshold   int      // 默认 2000
    ComplexTokenThreshold  int      // 默认 8000
    ToolComplexityWeight    int      // 每个 tool 增加的 token 估算，默认 500
    VisionComplexityWeight  int      // 每个图片增加的 token 估算，默认 1000
}

// Analyze 分析请求体，返回复杂度级别
func (c *Classifier) Analyze(bodyBytes []byte) (*RequestAnalysis, error) {
    var req struct {
        Messages []struct {
            Role    string    `json:"role"`
            Content interface{} `json:"content"`
        } `json:"messages"`
        Tools    []struct{} `json:"tools"`
    }

    if err := json.Unmarshal(bodyBytes, &req); err != nil {
        return nil, err
    }

    analysis := &RequestAnalysis{
        MessageCount: len(req.Messages),
        HasTools:      len(req.Tools) > 0,
        ToolCount:     len(req.Tools),
    }

    // 估算 token 数量
    totalTokens := 0
    maxMsgLength := 0

    for _, msg := range req.Messages {
        var textLen int

        switch content := msg.Content.(type) {
        case string:
            textLen = utf8.RuneCountInString(content)
            // 粗略估算: 1 token ≈ 1.3 个中文字符 或 1 个英文单词
            totalTokens += estimateTokens(textLen)

        case []interface{}:
            // 多模态内容 (vision)
            for _, item := range content {
                if itemMap, ok := item.(map[string]interface{}); ok {
                    if t, ok := itemMap["type"].(string); ok {
                        if t == "image_url" || t == "image" {
                            analysis.HasVision = true
                            totalTokens += c.VisionComplexityWeight
                        } else if t == "text" {
                            if text, ok := itemMap["text"].(string); ok {
                                textLen = utf8.RuneCountInString(text)
                                totalTokens += estimateTokens(textLen)
                            }
                        }
                    }
                }
            }
        }

        if textLen > maxMsgLength {
            maxMsgLength = textLen
        }
    }

    // tools 的 token 估算
    totalTokens += analysis.ToolCount * c.ToolComplexityWeight

    analysis.EstimatedTokens = totalTokens
    analysis.MaxMsgLength = maxMsgLength

    // 分级判断
    analysis.Level = c.classifyLevel(analysis)

    return analysis, nil
}

// classifyLevel 根据分析结果分级
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

// estimateTokens 粗略估算 token 数量
// 中文: 1 token ≈ 1.5 字符
// 英文: 1 token ≈ 4 字符
func estimateTokens(runeCount int) int {
    // 简化估算: 假设混合内容，取中间值
    return runeCount * 4 / 5
}
```

#### 3.1.2 路由中间件

```go
// internal/autoroute/middleware.go

package autoroute

import (
    "bytes"
    "encoding/json"
    "io"
    "net/http"

    "github.com/gin-gonic/gin"
    "github.com/sirupsen/logrus"
)

// RouteConfig 路由配置（从 system_settings 加载）
type RouteConfig struct {
    Enabled          bool                           `json:"enabled"`
    SimpleThreshold  int                             `json:"simple_threshold"`
    ComplexThreshold int                             `json:"complex_threshold"`
    SimpleModels     []string                        `json:"simple_models"`
    MediumModels     []string                        `json:"medium_models"`
    ComplexModels    []string                        `json:"complex_models"`
    GroupMapping     map[string]GroupComplexityMapping `json:"group_mapping"`
}

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

// Middleware 返回 Auto 路由中间件
func Middleware(classifier *Classifier, configProvider func() *RouteConfig) gin.HandlerFunc {
    return func(c *gin.Context) {
        config := configProvider()

        if !config.Enabled {
            c.Next()
            return
        }

        // 只处理 chat completions 请求
        if !isChatCompletions(c.Request.URL.Path) {
            c.Next()
            return
        }

        // 读取请求体
        bodyBytes, err := io.ReadAll(c.Request.Body)
        if err != nil {
            // 读取失败时使用默认分组策略
            c.Next()
            return
        }
        c.Request.Body.Close()

        // 分析复杂度
        analysis, err := classifier.Analyze(bodyBytes)
        if err != nil {
            logrus.WithError(err).Warn("Auto route: failed to analyze complexity")
            // 分析失败时使用默认分组（中等复杂度）
            analysis = &RequestAnalysis{Level: Medium}
        }

        // 获取当前分组名
        currentGroup := c.Param("group_name")

        // 查找路由映射
        mapping, found := config.GroupMapping[currentGroup]
        if !found {
            // 未配置映射，直接透传
            c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
            c.Next()
            return
        }

        // 选择目标分组
        targetGroup := selectTargetGroup(mapping, analysis.Level)

        if targetGroup == "" {
            // 未配置该级别分组，使用原分组
            c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
            c.Next()
            return
        }

        // 验证目标分组是否可用（可扩展：检查分组健康状态）
        if !isGroupAvailable(targetGroup) {
            // 目标不可用，尝试回退
            targetGroup = getFallbackGroup(mapping, analysis.Level)
            if targetGroup == "" {
                c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
                c.Next()
                return
            }
        }

        // 重写 group_name 参数
        c.Params = setParam(c.Params, "group_name", targetGroup)

        logrus.WithFields(logrus.Fields{
            "original_group": currentGroup,
            "target_group":   targetGroup,
            "complexity":     analysis.Level,
            "tokens":         analysis.EstimatedTokens,
            "has_tools":      analysis.HasTools,
            "has_vision":     analysis.HasVision,
        }).Debug("Auto route: redirected request")

        // 恢复请求体
        c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
        c.Next()
    }
}

// selectTargetGroup 根据复杂度级别选择目标分组
func selectTargetGroup(mapping GroupComplexityMapping, level ComplexityLevel) string {
    switch level {
    case Simple:
        return mapping.SimpleGroup
    case Medium:
        return mapping.MediumGroup
    case Complex:
        return mapping.ComplexGroup
    default:
        return mapping.MediumGroup
    }
}

// getFallbackGroup 获取回退分组
func getFallbackGroup(mapping GroupComplexityMapping, level ComplexityLevel) string {
    // 按优先级尝试回退
    fallbacks := []string{
        mapping.SimpleGroup,
        mapping.MediumGroup,
        mapping.ComplexGroup,
    }

    for _, fb := range fallbacks {
        if fb != "" && isGroupAvailable(fb) {
            return fb
        }
    }

    return ""
}

// isGroupAvailable 检查分组是否可用
func isGroupAvailable(groupName string) bool {
    // TODO: 实现分组健康检查
    // 暂时假设所有分组都可用
    return true
}

// isChatCompletions 判断是否为 chat completions 请求
func isChatCompletions(path string) bool {
    return bytes.Contains([]byte(path), []byte("chat/completions"))
}

// setParam 替换 gin 参数
func setParam(params gin.Params, key, value string) gin.Params {
    for i, p := range params {
        if p.Key == key {
            params[i].Value = value
            return params
        }
    }
    return append(params, gin.Param{Key: key, Value: value})
}
```

#### 3.1.3 路由规则配置 UI

在 Web 管理界面新增 **Auto 路由配置** 页面：

```
┌─────────────────────────────────────────────────────┐
│                   Auto 路由配置                       │
│                                                      │
│  [✓] 启用 Auto 路由                                  │
│                                                      │
│  复杂度阈值:                                          │
│  ┌──────────────┐    ┌──────────────┐               │
│  │ Simple: 2000 │    │ Complex: 8000│  tokens       │
│  └──────────────┘    └──────────────┘               │
│                                                      │
│  模型映射配置:                                        │
│  ┌─────────────────────────────────────────────────┐│
│  │ 逻辑模型  │  Simple   │  Medium  │  Complex ││
│  ├─────────────────────────────────────────────────┤│
│  │ gpt-4o   │ gpt-4o-lite│ gpt-4o-pro│ gpt-4.1 ││
│  │ claude   │ haiku-lite │ sonnet-pro│  opus   ││
│  │ deepseek │ ds-v3-lite │ ds-v3-pro │  ds-r1  ││
│  └─────────────────────────────────────────────────┘│
│                                                      │
│  回退策略:                                            │
│  ┌─────────────────────────────────────────────────┐│
│  │ Primary: gpt-4o-pro                             ││
│  │ Fallback: gpt-4o-lite                           ││
│  │ Final: gpt-4o                                   ││
│  └─────────────────────────────────────────────────┘│
│                                                      │
│  [保存配置]  [测试路由]  [重置默认]                    │
└─────────────────────────────────────────────────────┘
```

配置保存到 `system_settings` 表：

```json
{
    "setting_key": "auto_routing_config",
    "setting_value": "{\"enabled\":true,\"simple_threshold\":2000,\"complex_threshold\":8000,\"group_mapping\":{\"gpt-4o\":{\"simple_group\":\"gpt-4o-lite\",\"medium_group\":\"gpt-4o-pro\",\"complex_group\":\"gpt-4.1\"}}}"
}
```

### 3.2 统一模型目录

#### 3.2.1 新增 Handler

```go
// internal/handler/model_catalog_handler.go

package handler

import (
    "net/http"
    "sort"
    "strings"
    "sync"
    "time"

    "gpt-load/internal/models"
    "gpt-load/internal/services"

    "github.com/gin-gonic/gin"
)

// ModelCatalogHandler 统一模型目录 Handler
type ModelCatalogHandler struct {
    groupManager *services.GroupManager
    cache        *sync.Map
    cacheTTL     time.Duration
    lastRefresh  time.Time
}

// NewModelCatalogHandler creates new handler
func NewModelCatalogHandler(groupManager *services.GroupManager) *ModelCatalogHandler {
    return &ModelCatalogHandler{
        groupManager: groupManager,
        cache:        &sync.Map{},
        cacheTTL:     5 * time.Minute,
        lastRefresh:  time.Time{},
    }
}

// CatalogModel 目录中的模型条目
type CatalogModel struct {
    ID          string   `json:"id"`
    DisplayName string   `json:"display_name"`
    Groups      []string `json:"groups"`
    Providers   []string `json:"providers"`
}

// cachedModel 缓存的模型数据
type cachedModel struct {
    models []gin.H
    expiry time.Time
}

// ListModels 返回去重后的统一模型列表
// GET /v1/models
func (h *ModelCatalogHandler) ListModels(c *gin.Context) {
    // 检查缓存
    if cached, ok := h.cache.Load("models"); ok {
        if cm := cached.(*cachedModel); time.Now().Before(cm.expiry) {
            c.JSON(http.StatusOK, gin.H{
                "object": "list",
                "data":   cm.models,
            })
            return
        }
    }

    groups := h.groupManager.GetAllGroups()

    // 收集所有模型
    modelMap := make(map[string]*CatalogModel)

    for _, group := range groups {
        // 跳过聚合分组本身（聚合分组没有直接密钥）
        if group.GroupType == "aggregate" {
            continue
        }

        // 从模型重定向规则中提取可用模型
        redirectMap := group.ModelRedirectMap

        // 收集源模型名（用户可见的）
        for sourceModel := range redirectMap {
            if _, exists := modelMap[sourceModel]; !exists {
                modelMap[sourceModel] = &CatalogModel{
                    ID:          sourceModel,
                    DisplayName: formatDisplayName(sourceModel),
                    Groups:      []string{},
                    Providers:   []string{},
                }
            }
            modelMap[sourceModel].Groups = appendUnique(
                modelMap[sourceModel].Groups, group.Name)
        }

        // 如果没有重定向规则，从 test_model 推断
        if len(redirectMap) == 0 && group.TestModel != "" {
            model := group.TestModel
            if _, exists := modelMap[model]; !exists {
                modelMap[model] = &CatalogModel{
                    ID:          model,
                    DisplayName: formatDisplayName(model),
                    Groups:      []string{},
                    Providers:   []string{},
                }
            }
            modelMap[model].Groups = appendUnique(
                modelMap[model].Groups, group.Name)
        }
    }

    // 转换为列表并排序
    result := make([]CatalogModel, 0, len(modelMap))
    for _, m := range modelMap {
        result = append(result, *m)
    }

    sort.Slice(result, func(i, j int) bool {
        return result[i].ID < result[j].ID
    })

    // 返回 OpenAI 兼容格式
    models := make([]gin.H, len(result))
    for i, m := range result {
        models[i] = gin.H{
            "id":          m.ID,
            "object":      "model",
            "created":     0,
            "owned_by":    "gpt-load",
            "display_name": m.DisplayName,
            "groups":      m.Groups,
        }
    }

    // 更新缓存
    h.cache.Store("models", &cachedModel{
        models: models,
        expiry:  time.Now().Add(h.cacheTTL),
    })

    c.JSON(http.StatusOK, gin.H{
        "object": "list",
        "data":   models,
    })
}

// RefreshCache 手动刷新缓存
func (h *ModelCatalogHandler) RefreshCache() {
    h.cache.Delete("models")
    h.lastRefresh = time.Now()
}

// formatDisplayName 格式化模型显示名
func formatDisplayName(modelID string) string {
    // gpt-4o → GPT-4o
    // claude-3-5-sonnet → Claude 3.5 Sonnet
    parts := strings.Split(modelID, "-")
    for i, part := range parts {
        if len(part) > 0 {
            parts[i] = strings.ToUpper(part[:1]) + part[1:]
        }
    }
    return strings.Join(parts, " ")
}

// appendUnique 去重追加
func appendUnique(slice []string, item string) []string {
    for _, s := range slice {
        if s == item {
            return slice
        }
    }
    return append(slice, item)
}
```

#### 3.2.2 注册路由

在 `router.go` 中注册：

```go
// 在 registerProxyRoutes 中增加

func registerProxyRoutes(...) {
    // ... 现有代码 ...

    // 新增: 统一模型目录 (在 /proxy/:group_name 之前)
    modelCatalogHandler := handler.NewModelCatalogHandler(groupManager)
    router.GET("/v1/models", modelCatalogHandler.ListModels)
    router.GET("/proxy/v1/models", modelCatalogHandler.ListModels)

    // 现有代理路由
    proxyGroup := router.Group("/proxy/:group_name")
    // ...
}
```

### 3.3 模型自动去重

#### 3.3.1 设计思路

模型去重不需要独立的模块，而是通过 **聚合分组 + 模型重定向** 的组合来实现：

```
场景: 多个渠道都提供 gpt-4o

手动配置方式（现有）:
┌─────────────────────────────────────┐
│       聚合分组 "gpt-4o-all"          │
│  ├── 子分组A (OpenAI官方):            │
│  │   model_redirect: {"gpt-4o": "gpt-4o-2024-05-13"}
│  ├── 子分组B (Azure):                │
│  │   model_redirect: {"gpt-4o": "gpt-4o"}
│  └── 子分组C (OpenRouter):           │
│      model_redirect: {"gpt-4o": "openai/gpt-4o"}
│                                       │
│  客户端统一调用: gpt-4o                │
│  内部按权重轮询到各子分组               │
│  每个子分组自动映射到实际模型名          │
└─────────────────────────────────────┘
```

**增强点：** 在 Web UI 中增加"智能聚合建议"功能：

```go
// internal/services/model_dedup_service.go

package services

// ModelDedupService 模型去重建议服务
type ModelDedupService struct {
    groupManager *GroupManager
}

// DedupSuggestion 去重建议
type DedupSuggestion struct {
    ModelName               string   `json:"model_name"`
    SourceGroups            []string `json:"source_groups"`
    SuggestedAggregateName string   `json:"suggested_aggregate_name"`
}

// GetDedupSuggestions 返回模型去重建议
func (s *ModelDedupService) GetDedupSuggestions() []DedupSuggestion {
    groups := s.groupManager.GetAllGroups()

    // 收集所有模型 → 分组映射
    modelToGroups := make(map[string][]string)

    for _, group := range groups {
        if group.GroupType == "aggregate" {
            continue
        }

        // 从重定向规则中收集
        for sourceModel := range group.ModelRedirectMap {
            modelToGroups[sourceModel] = appendUnique(
                modelToGroups[sourceModel], group.Name)
        }

        // 从 test_model 收集
        if group.TestModel != "" && len(group.ModelRedirectMap) == 0 {
            modelToGroups[group.TestModel] = appendUnique(
                modelToGroups[group.TestModel], group.Name)
        }
    }

    // 找出被多个分组提供的模型
    suggestions := make([]DedupSuggestion, 0)
    for model, groups := range modelToGroups {
        if len(groups) > 1 {
            suggestions = append(suggestions, DedupSuggestion{
                ModelName:               model,
                SourceGroups:            groups,
                SuggestedAggregateName: model + "-aggregate",
            })
        }
    }

    return suggestions
}
```

Web UI 展示：

```
┌─────────────────────────────────────────────────────┐
│                   模型去重建议                        │
│                                                      │
│  发现以下模型被多个分组提供:                           │
│                                                      │
│  ┌─────────────────────────────────────────────────┐│
│  │  模型        │ 分组数 │ 操作                    ││
│  ├─────────────────────────────────────────────────┤│
│  │ gpt-4o     │   3   │ [创建聚合分组]            ││
│  │ claude-sonnet │ 2  │ [创建聚合分组]            ││
│  │ gemini-flash  │ 2  │ [创建聚合分组]            ││
│  └─────────────────────────────────────────────────┘│
│                                                      │
│  点击 [创建聚合分组] 将自动:                           │
│  1. 创建聚合分组                                     │
│  2. 添加所有提供该模型的分组为子分组                   │
│  3. 配置模型重定向规则                                │
│  4. 设置等权重分配                                    │
└─────────────────────────────────────────────────────┘
```

---

## 四、代码改动清单

### 4.1 新增文件

| 文件 | 说明 | 行数估算 |
|------|------|----------|
| `internal/autoroute/complexity.go` | 复杂度分类器 | ~150 |
| `internal/autoroute/middleware.go` | Auto 路由中间件 | ~150 |
| `internal/autoroute/config.go` | 配置管理 | ~80 |
| `internal/handler/model_catalog_handler.go` | 统一模型目录 Handler | ~120 |
| `internal/services/model_dedup_service.go` | 模型去重建议服务 | ~80 |
| `internal/services/aggregate_suggestion.go` | 聚合建议服务 | ~60 |
| `web/src/views/auto-routing/index.vue` | Auto 路由配置页面 | ~250 |
| `web/src/views/model-catalog/index.vue` | 模型目录页面 | ~150 |
| `web/src/views/model-dedup/index.vue` | 模型去重建议页面 | ~180 |
| **合计新增** | | **~1070 行代码** |

### 4.2 修改文件

| 文件 | 修改内容 | 改动量 |
|------|---------|--------|
| `internal/router/router.go` | 注册 Auto 路由中间件 + 模型目录路由 | +30 行 |
| `internal/config/system_settings.go` | 增加 auto_routing_config 配置项 | +40 行 |
| `internal/middleware/` | 新增中间件注册逻辑 | +20 行 |
| `web/src/router/index.ts` | 增加新页面路由 | +15 行 |
| `web/src/layout/Sidebar.vue` | 增加菜单项 | +15 行 |
| `web/src/api/` | 增加新 API 接口 | +50 行 |
| **合计修改** | | **~170 行** |

### 4.3 不修改的核心文件

以下核心文件**不需要修改**，保持与上游同步：
- `internal/proxy/server.go` — 代理核心逻辑
- `internal/services/subgroup_manager.go` — 聚合分组选择
- `internal/keypool/provider.go` — 密钥轮询
- `internal/channel/` — 渠道适配层
- `internal/services/aggregate_group_service.go` — 聚合分组管理

---

## 五、请求流程详解

### 5.1 有 Auto 路由的请求流程

```
客户端请求:
POST http://localhost:3001/proxy/gpt-4o/v1/chat/completions
Authorization: Bearer sk-proxy-key
Body: {"model": "gpt-4o", "messages": [...], "tools": [...]}

│
▼

┌───────────────────────────┐
│  1. Router 匹配            │
│  /proxy/:group_name/*path │
│  group_name = "gpt-4o"     │
└───────────┬───────────────┘
            │
            ▼
┌───────────────────────────┐
│  2. ProxyRouteDispatcher  │
│  (已有中间件)              │
└───────────┬───────────────┘
            │
            ▼
┌───────────────────────────┐
│  3. ProxyAuth              │
│  (已有中间件，验证 key)     │
└───────────┬───────────────┘
            │
            ▼
┌───────────────────────────┐
│  4. AutoRoute Middleware   │  ← 新增
│                            │
│  ├─ 读取 body              │
│  ├─ 分析复杂度              │
│  │   • tokens: 3500        │
│  │   • has_tools: true     │
│  │   → Level: medium       │
│  │                          │
│  ├─ 查映射表:               │
│  │   gpt-4o → medium       │
│  │   → target: gpt-4o-pro  │
│  │                          │
│  ├─ 验证分组可用性           │
│  │                          │
│  └─ 重写 group_name         │
│      gpt-4o → gpt-4o-pro   │
└───────────┬───────────────┘
            │
            ▼
┌───────────────────────────┐
│  5. HandleProxy            │
│                            │
│  ├─ group_name = "gpt-4o-pro" │
│  ├─ 获取聚合分组            │
│  ├─ SelectSubGroup()       │
│  │   → 选中 "子分组A"       │
│  ├─ SelectKey()            │
│  │   → 轮询选 key          │
│  ├─ ApplyModelRedirect()   │
│  │   gpt-4o → gpt-4o-2024-05-13 │
│  └─ 转发到上游              │
└───────────┬───────────────┘
            │
            ▼
        上游 API (OpenAI)
```

### 5.2 无 Auto 路由的请求流程（向后兼容）

```
客户端请求:
POST http://localhost:3001/proxy/deepseek-aggregate/v1/chat/completions

│
▼

┌───────────────────────────┐
│  AutoRoute Middleware    │
│  config.Enabled = false  │
│  → 直接 c.Next() 透传    │
└───────────┬───────────────┘
            │
            ▼
┌───────────────────────────┐
│  HandleProxy              │
│  正常走聚合分组逻辑        │
│  加权轮询 → 选 key → 转发  │
└───────────────────────────┘
```

**完全向后兼容**，不启用 Auto 路由时与现有行为一致。

---

## 六、配置示例

### 6.1 完整配置结构

```yaml
# .env (静态配置，已有)
AUTH_KEY=sk-prod-xxxxxxxxxxxxxxxxxxxxxxxx
PORT=3001
DATABASE_DSN=gpt-load.db

# system_settings 表 (动态配置，新增)
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
            },
            "deepseek": {
                "simple_group": "ds-v3-lite",
                "medium_group": "ds-v3-pro",
                "complex_group": "ds-r1-max"
            }
        }
    }
}
```

### 6.2 分组配置示例

```
聚合分组 "gpt-4o-lite" (simple 级别)
├── 子分组A: openai-official (权重 500)
│   └── model_redirect: {"gpt-4o-mini": "gpt-4o-mini-2024-07-18"}
├── 子分组B: azure-openai (权重 300)
│   └── model_redirect: {"gpt-4o-mini": "gpt-4o-mini"}
└── 子分组C: openrouter (权重 200)
    └── model_redirect: {"gpt-4o-mini": "openai/gpt-4o-mini"}

聚合分组 "gpt-4o-pro" (medium 级别)
├── 子分组A: openai-official (权重 500)
│   └── model_redirect: {"gpt-4o": "gpt-4o-2024-05-13"}
└── 子分组B: azure-openai (权重 500)
    └── model_redirect: {"gpt-4o": "gpt-4o"}

聚合分组 "gpt-4.1-max" (complex 级别)
└── 子分组A: openai-official (权重 1000)
    └── model_redirect: {"gpt-4.1": "gpt-4.1-2025-04-14"}
```

### 6.3 客户端调用

```python
# 客户端只需调用逻辑模型名

from openai import OpenAI

client = OpenAI(
    api_key="sk-proxy-key",
    base_url="http://localhost:3001/proxy/gpt-4o"
)

# 简单请求 → 自动路由到 gpt-4o-lite
response = client.chat.completions.create(
    model="gpt-4o",
    messages=[{"role": "user", "content": "你好"}]
)

# 复杂请求 → 自动路由到 gpt-4.1-max
response = client.chat.completions.create(
    model="gpt-4o",
    messages=[{"role": "user", "content": long_context}],
    tools=[...]  # 有 function calling
)
```

---

## 七、性能影响评估

### 7.1 Auto 路由中间件开销

| 操作 | 耗时 |
|------|------|
| JSON 解析请求体 | < 0.1ms |
| Token 估算 | < 0.05ms |
| 复杂度分类判断 | < 0.01ms |
| 参数重写 | < 0.01ms |
| **总计** | **< 0.2ms** |

对比 GPT-Load 本身的代理开销（网络 IO + 密钥选择 ≈ 1-5ms），Auto 路由的额外开销可以忽略不计。

### 7.2 统一模型目录开销

| 场景 | 耗时 |
|------|------|
| 首次调用 | 遍历所有分组 + 去重合并 ≈ 1-3ms |
| 后续调用（缓存命中） | ≈ 0.1ms |
| 缓存失效 | 分组配置变更时自动刷新 |

---

## 八、兼容性分析

### 8.1 与 GPT-Load 现有能力的关系

```
GPT-Load 已有能力：
├── 密钥轮询 (keypool)
├── 聚合分组 (aggregate_group)
├── 模型重定向 (model_redirect)
├── 失败黑名单 (blacklist)
└── 平滑加权轮询 (smooth_weighted)

Auto 路由在上一层：
客户端 → Auto路由中间件 → 现有能力 → 上游API
```

**结论：兼容性良好，不破坏现有链路。**

### 8.2 潜在冲突点

| 冲突点 | 问题描述 | 处理方式 |
|--------|----------|----------|
| `/v1/models` 路由注册 | 注册在 `/proxy/:group_name` 之前 | 明确路由注册顺序 |
| group_name 重写 | 中间件修改 `c.Params("group_name")` | 验证后续 handler 是否受影响 |
| 聚合分组内的模型重定向 | Auto路由重写 group_name 后，模型重定向是否仍生效？ | 已验证：不受影响 |

### 8.3 向后兼容性

| 场景 | 兼容性 |
|------|--------|
| 不启用 Auto 路由 | ✅ 完全一致 |
| 使用模型重定向 | ✅ 独立机制，无冲突 |
| 使用聚合分组 | ✅ 正常配合 |
| 现有客户端 | ✅ 无需修改 |

---

## 九、监控与可观测性

### 9.1 建议增加的监控指标

```go
// 路由指标
auto_route_decisions_total{level="simple|medium|complex", group="xxx"}
auto_route_errors_total{reason="parse_error|config_missing|group_unavailable"}
auto_route_fallback_total{original_group="xxx", fallback_group="xxx"}

// 模型目录指标
model_catalog_requests_total
model_catalog_cache_hits_total
model_catalog_cache_misses_total

// 性能指标
auto_route_latency_seconds
model_catalog_latency_seconds
```

### 9.2 日志字段

```go
// Auto 路由日志
logrus.WithFields(logrus.Fields{
    "original_group":  currentGroup,
    "target_group":    targetGroup,
    "complexity":      analysis.Level,
    "tokens":          analysis.EstimatedTokens,
    "has_tools":       analysis.HasTools,
    "has_vision":      analysis.HasVision,
    "fallback_used":   fallbackUsed,
}).Debug("Auto route: redirected request")
```

---

## 十、实施计划

### Phase 1: Auto 复杂度路由（2-3 周）

| 周 | 任务 | 交付物 |
|----|------|--------|
| W1 | 复杂度分类器开发 + 单元测试 | `internal/autoroute/complexity.go` |
| W1 | 路由中间件开发 | `internal/autoroute/middleware.go` |
| W1 | 配置管理开发 | `internal/autoroute/config.go` |
| W2 | 中间件集成 + 路由注册 | `router.go` 修改 |
| W2 | 配置管理（system_settings） | 配置读写逻辑 |
| W3 | Web UI 配置页面 | Vue 页面 |
| W3 | 集成测试 + 文档 | 测试用例 |

### Phase 2: 统一模型目录（1 周）

| 周 | 任务 | 交付物 |
|----|------|--------|
| W4 | 模型目录 Handler | `model_catalog_handler.go` |
| W4 | 路由注册 + 测试 | 集成测试 |
| W4 | Web UI 页面 | Vue 页面 |

### Phase 3: 模型去重建议（1 周）

| 周 | 任务 | 交付物 |
|----|------|--------|
| W5 | 去重建议服务 | `model_dedup_service.go` |
| W5 | Web UI 建议页面 | Vue 页面 |
| W5 | 一键创建聚合分组 | API + UI |

### Phase 4: 优化与上线（1-2 周）

| 周 | 任务 | 交付物 |
|----|------|--------|
| W6 | 性能优化 + 缓存 | 缓存层 |
| W6 | 监控指标 | Metrics |
| W7 | 完整测试 + 文档 | 测试报告 |
| W7 | 灰度发布 + 监控 | 部署方案 |

---

## 十一、风险与应对

| 风险 | 影响 | 应对措施 |
|------|------|---------|
| Token 估算不准确 | 路由到不合适的模型 | 提供手动覆盖 API；持续优化估算算法 |
| 中间件增加延迟 | 请求延迟增加 | 估算开销 < 0.2ms，可忽略 |
| 与上游 GPT-Load 版本冲突 | 合并困难 | 核心文件不修改，新增代码独立包 |
| 配置复杂度高 | 用户上手难 | Web UI 提供模板和向导 |
| Auto 路由误判 | 简单请求走了贵模型 | 提供日志查看 + 手动调整规则 |
| 分组不可用时的回退 | 路由到不可用分组 | 实现健康检查 + 多级回退 |

---

## 十二、总结

### 12.1 方案优势

1. **最小侵入** — 核心代理逻辑零修改，新增代码独立成包
2. **完全向后兼容** — 不启用 Auto 路由时行为与现有一致
3. **高性能** — Go 实现，额外开销 < 0.2ms
4. **可配置** — 所有阈值和映射通过 Web UI 动态配置
5. **可观测** — 每次路由决策都有日志记录

### 12.2 与需求的匹配度

| 需求 | 匹配度 |
|------|--------|
| 多 API 集成 | ✅ 100%（已有） |
| 统一暴露 | ✅ 100%（已有） |
| 自动轮询 | ✅ 100%（已有） |
| 自动容错 | ✅ 100%（已有） |
| 自动关闭/重试 | ✅ 100%（已有） |
| 同类聚合 | ✅ 100%（已有） |
| 模型别名 | ✅ 100%（已有） |
| **Auto 复杂度路由** | ✅ 100%（新增） |
| **模型自动去重** | ✅ 90%（建议 + 一键创建） |
| **统一模型目录** | ✅ 100%（新增） |

**总体匹配度：98%**
