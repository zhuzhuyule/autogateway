# 兼容性分析

## 与 GPT-Load 现有能力的关系

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

## 兼容性测试矩阵

| 场景 | 预期行为 | 兼容性 |
|------|----------|--------|
| 不启用 Auto 路由 | 与现有行为完全一致 | ✅ |
| 启用 Auto 路由，配置映射 | 按映射规则路由 | ✅ |
| 启用 Auto 路由，无映射 | 透传，不修改 | ✅ |
| 使用模型重定向 | 与 Auto 路由独立工作 | ✅ |
| 使用聚合分组 | 正常配合 | ✅ |
| 现有客户端 | 无需修改 | ✅ |
| 直接调用 `/v1/models` | 返回统一模型列表 | ✅ |
| 调用 `/proxy/:group/v1/models` | 返回该分组的模型 | ✅ |

## 潜在冲突点分析

### 1. 路由注册顺序

**冲突:** 注册 `/v1/models` 在 `/proxy/:group_name` 之前

**分析:**
```
GET /v1/models        → 统一模型目录 Handler
GET /proxy/gpt-4o/v1/chat/completions → Proxy Handler
```

**结论:** 无冲突，路由注册顺序正确。

### 2. group_name 参数重写

**冲突:** 中间件重写 `c.Params("group_name")` 可能影响后续 Handler

**分析:**
```go
// 中间件中
c.Params = setParam(c.Params, "group_name", targetGroup)

// 后续 Handler 中
groupName := c.Param("group_name")  // 获取到的是重写后的值
```

**结论:** 这是预期行为，后续 Handler 使用重写后的值进行代理。

### 3. 聚合分组内的模型重定向

**冲突:** Auto路由重写 group_name 后，模型重定向是否仍生效？

**分析:**
```
Auto路由: gpt-4o → gpt-4o-pro (聚合分组)
          │
          ▼
聚合分组 gpt-4o-pro 内部:
├── 子分组A: model_redirect {"gpt-4o": "gpt-4o-2024-05-13"}
└── 子分组B: model_redirect {"gpt-4o": "gpt-4o"}
```

**结论:** 不受影响，模型重定向在聚合分组选择后生效。

## 向后兼容性

### 完全向后兼容的场景

| 场景 | 说明 |
|------|------|
| 不配置 Auto 路由 | 系统行为与原 GPT-Load 完全一致 |
| 不调用 `/v1/models` | 不影响现有客户端 |
| 不使用模型去重功能 | 现有分组配置不受影响 |

### 需要注意的场景

| 场景 | 注意事项 |
|------|----------|
| 启用 Auto 路由 | 确保映射配置正确 |
| 使用统一模型目录 | 客户端可能看到新模型列表 |
| 现有 API 密钥 | 无需修改，继续使用 |

## 与上游版本同步策略

### 不修改的核心文件

以下文件**不修改**，保持与上游 GPT-Load 同步：

- `internal/proxy/server.go` — 代理核心逻辑
- `internal/services/subgroup_manager.go` — 聚合分组选择
- `internal/keypool/provider.go` — 密钥轮询
- `internal/channel/` — 渠道适配层
- `internal/services/aggregate_group_service.go` — 聚合分组管理

### 新增独立模块

新增代码独立成包，不污染原有代码结构：

```
internal/
├── autoroute/          # 新增: Auto路由模块
│   ├── complexity.go
│   ├── middleware.go
│   └── config.go
├── handler/
│   └── model_catalog_handler.go  # 新增: 模型目录 Handler
└── services/
    └── model_dedup_service.go    # 新增: 去重服务
```

## 版本兼容性

| 组件 | 依赖版本 | 说明 |
|------|----------|------|
| GPT-Load | v2.x | 核心依赖 |
| Go | 1.21+ | 语言版本 |
| Gin | latest | Web 框架 |
| Vue | 3.x | 前端框架 |

详见 [SPEC.md](../../SPEC.md)
