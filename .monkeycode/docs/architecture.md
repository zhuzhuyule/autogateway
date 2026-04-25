# 架构设计

## 整体架构

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
└──────────────────────────┬──────────────────────────────────┘
                            │ 重写 group_name 参数
                            ▼
┌─────────────────────────────────────────────────────────────┐
│                      GPT-Load (底座，已有)                    │
│                                                              │
│  ┌─────────────────────────────────────────────────────┐     │
│  │            聚合分组 "deepseek-lite" (simple 级别)     │     │
│  │  ├── 子分组A: DeepSeek 官方 (权重 500)               │     │
│  │  ├── 子分组B: OpenRouter (权重 300)                  │     │
│  │  └── 子分组C: Azure (权重 200)                        │     │
│  └─────────────────────────────────────────────────────┘     │
└──────────────────────────┬──────────────────────────────────┘
                            ▼
┌─────────────────────────────────────────────────────────────┐
│                       上游 API Providers                      │
│              OpenAI / Anthropic / Google / Azure / OpenRouter │
└─────────────────────────────────────────────────────────────┘
```

## 核心模块

### 1. Auto 路由中间件 (autoroute)

位置: `backend/internal/autoroute/`

```
autoroute/
├── complexity.go    # 复杂度分类器
├── middleware.go    # 路由中间件
└── config.go        # 配置管理
```

**复杂度分类算法:**

```go
type RequestAnalysis struct {
    EstimatedTokens int    // 估算 token 数
    HasTools        bool   // 是否有 tools
    HasVision       bool   // 是否有 vision
    ToolCount       int    // tools 数量
    MessageCount    int    // 消息数量
    MaxMsgLength    int    // 最大消息长度
    Level           ComplexityLevel  // simple | medium | complex
}
```

**分类规则:**

```go
func classifyLevel(a *RequestAnalysis) ComplexityLevel {
    // 复杂条件优先
    if a.EstimatedTokens > 8000 || a.HasVision || a.ToolCount > 3 {
        return Complex
    }
    // 中等条件
    if a.EstimatedTokens > 2000 || a.ToolCount > 0 || a.MessageCount > 10 {
        return Medium
    }
    return Simple
}
```

### 2. 统一模型目录 (model_catalog_handler)

位置: `backend/internal/handler/model_catalog_handler.go`

```go
type ModelCatalogHandler struct {
    groupManager *services.GroupManager
    cache        *sync.Map      // 缓存
    cacheTTL     time.Duration  // TTL
}
```

**功能:**
- 遍历所有分组收集模型
- 按逻辑模型名去重
- 返回 OpenAI 兼容格式
- 缓存支持

### 3. 模型去重服务 (model_dedup_service)

位置: `backend/internal/services/model_dedup_service.go`

```go
type ModelDedupService struct {
    groupManager *GroupManager
}

type DedupSuggestion struct {
    ModelName               string   // 模型名
    SourceGroups            []string // 提供该模型的分组列表
    SuggestedAggregateName string   // 建议的聚合分组名
}
```

## 路由优先级

| 优先级 | 规则类型 | 说明 |
|--------|----------|------|
| 1 | 精确匹配 | 用户显式指定分组，直接路由 |
| 2 | 模型名映射 | Auto 路由的 mapping 配置 |
| 3 | 聚合分组兜底 | 无映射时走聚合分组 |

## 回退策略

```go
type FallbackStrategy struct {
    PrimaryGroup   string  // 主目标
    FallbackGroup  string  // 一级回退
    FallbackGroup2 string  // 二级回退
}
```

当目标分组不可用时，按以下顺序尝试：
1. 原目标分组
2. 同级别其他配置分组
3. 中等复杂度分组
4. 简单复杂度分组

## 数据流

```
请求进入
    │
    ▼
中间件拦截
    │
    ├── 读取请求体
    │
    ├── 复杂度分析
    │   ├── Token 估算
    │   ├── Tools 检测
    │   └── Vision 检测
    │
    ├── 查找映射
    │
    ├── 目标分组选择
    │
    └── group_name 重写
            │
            ▼
    后续 Handler 处理
    (与原有逻辑一致)
```

## 性能考量

| 操作 | 开销 |
|------|------|
| JSON 解析 | < 0.1ms |
| Token 估算 | < 0.05ms |
| 分类判断 | < 0.01ms |
| **总计** | **< 0.2ms** |

对比 GPT-Load 代理开销 (1-5ms)，额外开销可忽略。

详见 [SPEC.md](../../SPEC.md)
