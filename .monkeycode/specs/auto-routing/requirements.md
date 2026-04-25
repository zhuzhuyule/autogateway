# Auto 路由功能需求规格

> 基于 EARS 规范编写

## 1. 范围

本文档描述 Auto 复杂度路由功能的详细需求，包括功能需求、性能需求和接口需求。

## 2. 引用

- SPEC.md - 完整规格文档
- design.md - 技术设计规格

## 3. 术语

| 术语 | 定义 |
|------|------|
| 复杂度级别 | 请求的复杂程度，分为 simple/medium/complex 三级 |
| Token | NLP 中的基本语义单元 |
| 路由 | 将请求转发到目标分组的过程 |

## 4. 功能需求

### 4.1 复杂度分类

#### UR-AUTO-001: Token 估算

系统 SHALL 支持根据请求内容估算 token 数量。

**描述:**
- 分析请求体中的 messages 字段
- 遍历每个 message 的 content
- 根据文本长度估算 token 数量
- 返回总 token 数量

**验收标准:**
- [ ] 能够解析 string 类型的 content
- [ ] 能够解析多模态类型的 content (含 image_url)
- [ ] 估算结果与实际 token 数量的误差在可接受范围内
- [ ] 能够处理空消息和空 content 的边界情况

#### UR-AUTO-002: 复杂度级别判断

系统 SHALL 支持根据预定义规则判断请求的复杂度级别。

**描述:**
- Simple: token < 2000, 无 tools, 无 vision
- Medium: token 2000-8000 或 有 tools 或 message > 10
- Complex: token > 8000 或 有 vision 或 tools > 3

**验收标准:**
- [ ] 能够正确判断 Simple 级别
- [ ] 能够正确判断 Medium 级别
- [ ] 能够正确判断 Complex 级别
- [ ] 优先级: Complex > Medium > Simple

#### UR-AUTO-003: Tools 检测

系统 SHALL 支持检测请求中是否包含 tools 或 function_call。

**描述:**
- 检测请求体中的 tools 字段
- 检测 chat completions 中的 function_call
- 统计 tool 数量

**验收标准:**
- [ ] 能够检测 tools 数组的存在
- [ ] 能够统计 tool 数量
- [ ] 能够检测 function_call 的存在

#### UR-AUTO-004: Vision 检测

系统 SHALL 支持检测请求中是否包含图像内容。

**描述:**
- 检测多模态 content 中的 image_url 或 image 类型
- 标记请求包含 vision 能力

**验收标准:**
- [ ] 能够检测 image_url 类型
- [ ] 能够检测 image 类型
- [ ] 能够正确处理混合内容（文本+图片）

### 4.2 路由配置

#### UR-AUTO-010: 路由规则配置

系统 SHALL 支持通过 Web UI 配置路由规则。

**描述:**
- 启用/禁用 Auto 路由
- 配置 Simple/Complex 阈值
- 配置模型映射关系 (逻辑模型 → 分组)
- 配置回退策略

**验收标准:**
- [ ] 能够启用/禁用 Auto 路由
- [ ] 能够修改阈值参数
- [ ] 能够配置模型映射
- [ ] 配置保存到 system_settings 表
- [ ] 配置变更后自动生效

#### UR-AUTO-011: 配置验证

系统 SHALL 支持验证配置的有效性。

**描述:**
- 检查目标分组是否存在
- 检查回退分组是否有效
- 提供配置校验反馈

**验收标准:**
- [ ] 检验目标分组存在性
- [ ] 检验回退分组存在性
- [ ] 给出明确的错误提示

### 4.3 路由执行

#### UR-AUTO-020: 请求拦截

系统 SHALL 支持拦截 chat completions 请求。

**描述:**
- 中间件拦截所有 `/chat/completions` 请求
- 仅处理 POST 请求
- 其他请求透传

**验收标准:**
- [ ] 拦截 chat completions 请求
- [ ] 透传非 chat completions 请求
- [ ] 透传 GET 请求

#### UR-AUTO-021: 路由决策

系统 SHALL 支持根据配置执行路由决策。

**描述:**
- 根据模型名查找映射
- 根据复杂度选择目标分组
- 处理回退逻辑

**验收标准:**
- [ ] 正确查找模型映射
- [ ] 正确选择目标分组
- [ ] 正确处理回退
- [ ] 分组不可用时使用回退

#### UR-AUTO-022: 参数重写

系统 SHALL 支持重写 group_name 参数。

**描述:**
- 将原始 group_name 替换为目标分组
- 保持其他参数不变

**验收标准:**
- [ ] group_name 被正确重写
- [ ] 其他参数保持不变
- [ ] 后续 Handler 能获取到新值

#### UR-AUTO-023: 向后兼容

系统 SHALL 支持在未配置映射时透传请求。

**描述:**
- 如果模型没有配置映射
- 如果 Auto 路由未启用
- 请求直接透传

**验收标准:**
- [ ] 无映射时透传
- [ ] Auto 路由禁用时透传
- [ ] 透传行为与原有逻辑一致

### 4.4 监控与日志

#### UR-AUTO-030: 路由日志

系统 SHALL 支持记录路由决策日志。

**描述:**
- 记录原始分组和目标分组
- 记录复杂度分析结果
- 记录路由决策原因

**验收标准:**
- [ ] 记录 original_group
- [ ] 记录 target_group
- [ ] 记录 complexity level
- [ ] 记录 tokens 估算值
- [ ] 记录 has_tools, has_vision

#### UR-AUTO-031: 错误日志

系统 SHALL 支持记录路由错误。

**描述:**
- 记录解析失败错误
- 记录配置缺失错误
- 记录分组不可用错误

**验收标准:**
- [ ] 记录解析错误
- [ ] 记录配置错误
- [ ] 记录回退发生

#### UR-AUTO-032: 监控指标

系统 SHALL 支持暴露路由相关指标。

**描述:**
- 路由决策计数
- 路由错误计数
- 路由延迟

**验收标准:**
- [ ] auto_route_decisions_total
- [ ] auto_route_errors_total
- [ ] auto_route_latency_seconds

## 5. 非功能需求

### 5.1 性能需求

| 指标 | 要求 |
|------|------|
| 额外延迟 | < 0.2ms |
| 吞吐量 | 不低于原有系统 |
| 并发安全 | 线程安全 |

### 5.2 可用性需求

| 指标 | 要求 |
|------|------|
| 错误处理 | 解析失败时透传 |
| 回退机制 | 分组不可用时回退 |
| 配置热更新 | 无需重启 |

### 5.3 兼容性需求

| 项目 | 要求 |
|------|------|
| GPT-Load 版本 | v2.x |
| Go 版本 | 1.21+ |
| 向后兼容 | 完全兼容 |

## 6. 接口需求

### 6.1 Web UI 接口

#### API-AUTO-001: 保存配置

```
POST /api/auto-routing/config
Body: {
    "enabled": boolean,
    "simple_threshold": number,
    "complex_threshold": number,
    "group_mapping": {...}
}
Response: { "success": boolean }
```

#### API-AUTO-002: 获取配置

```
GET /api/auto-routing/config
Response: {
    "enabled": boolean,
    "simple_threshold": number,
    "complex_threshold": number,
    "group_mapping": {...}
}
```

#### API-AUTO-003: 测试路由

```
POST /api/auto-routing/test
Body: {
    "group_name": string,
    "request_body": {...}
}
Response: {
    "target_group": string,
    "analysis": {
        "tokens": number,
        "has_tools": boolean,
        "has_vision": boolean,
        "level": string
    }
}
```

## 7. 边界条件

| 条件 | 预期行为 |
|------|----------|
| 空请求体 | 使用默认值，level = Medium |
| 无效 JSON | 透传请求，记录错误日志 |
| 分组不存在 | 使用回退分组 |
| 所有分组不可用 | 透传，使用原分组 |
| 配置缺失 | 透传请求 |
