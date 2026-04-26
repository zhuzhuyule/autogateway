# 模型路由系统 - 需求文档

## 1. 概述与目标

本文档描述 AutoGateway 的智能模型路由系统，支持三种路由模式：

1. **Auto Routing**：针对 `auto` 模型的特殊路由，根据 token 量自动选择 fast/standard/slow pool
2. **别名路由**：根据别名映射找到所有同名模型，轮询调用
3. **准确路由**：无别名时，直接用模型名查找，轮询所有同名模型

## 2. 路由逻辑

### 2.1 路由矩阵

| 请求模型 | 路由类型 | 路由策略 |
|---------|---------|---------|
| `auto` | **Auto Routing** | 按 token 量自动选 fast/standard/slow pool |
| `gpt-4` (有别名) | **别名路由** | 用别名匹配，轮询所有同名模型 |
| `claude-3-5-sonnet` | **别名路由** | 如果有别名就走别名，没有就走准确名 |
| `qwen-3` (无别名) | **准确路由** | 直接用模型名查找，轮询所有同名模型 |

### 2.2 路由流程

```
收到请求
    │
    ├─ 模型名 == "auto"?
    │    └─ 是 → Auto Routing
    │              ├─ token < 2000  → fast pool
    │              ├─ token 2000-8000 → standard pool
    │              └─ token > 8000   → slow pool
    │
    └─ 否 → 别名路由
              ├─ 查 alias_mapping 表
              │    ├─ 找到 → 轮询匹配到的所有模型
              │    └─ 没找到 → 直接用模型名查找，轮询所有同名模型
```

## 3. 数据结构

### 3.1 模型元数据

```typescript
interface ModelMetadata {
  id: number;
  real_model: string;      // 真实模型名: "qwen-3"
  aliases: string[];       // 别名列表: ["gpt-4", "fast-gpt"]
  levels: string[];        // 所属 pool: ["fast", "standard"]
  provider: string;        // 来源: "openai", "anthropic", "google"
  capabilities: string[]; // 能力: ["vision", "tools"]
  is_active: boolean;      // 是否启用
  created_at: string;
  updated_at: string;
}
```

### 3.2 别名映射

```typescript
interface AliasMapping {
  id: number;
  alias: string;          // 别名: "gpt-4"
  models: ModelRef[];      // 映射到的模型列表
  created_at: string;
  updated_at: string;
}

interface ModelRef {
  real_model: string;     // 真实模型名
  priority: number;       // 优先级（数字越小越优先）
  level: string;          // 所属 level
}
```

### 3.3 Level Pool

| Level | 说明 | 示例模型 |
|-------|------|---------|
| `fast` | 快速/低成本模型 | qwen-3, gpt-4-turbo, gemini-2-flash |
| `standard` | 标准模型 | gpt-4, claude-3-5-sonnet |
| `slow` | 高性能模型 | gpt-4o, claude-3-opus |

### 3.4 Key 模型绑定

```typescript
interface KeyModelBinding {
  id: number;
  key_id: number;         // Key ID
  real_model: string;     // 真实模型名
  alias: string;          // 别名（可空）
  level: string;          // 所属 level
  is_enabled: boolean;    // 是否启用
  priority: number;       // 优先级
  created_at: string;
  updated_at: string;
}
```

## 4. 功能清单

### 4.1 P0 - 核心功能

| 功能 | 说明 |
|------|------|
| 模型管理 | 增删改查模型元数据（真实名、别名、level、provider） |
| 别名配置 | 为模型配置别名映射，一个别名可映射多个模型 |
| Key 绑定模型 | Key 选择可用的模型列表 |
| Auto Routing | auto 模型按 token 量路由到 fast/standard/slow pool |
| 别名路由 | 别名模型轮询匹配到的所有模型 |
| 准确路由 | 无别名时直接路由到同名模型 |

### 4.2 P1 - 增强功能

| 功能 | 说明 |
|------|------|
| Level 分组 | fast/standard/slow 分组管理 |
| 优先级配置 | 模型绑定优先级，用于轮询顺序 |
| 故障转移 | 主模型失败时自动切换到备用模型 |

### 4.3 P2 - 扩展功能

| 功能 | 说明 |
|------|------|
| 能力标签 | vision/tools 等能力标签 |
| 模型搜索 | 按 provider、level、capability 筛选 |
| 使用统计 | 各模型调用量统计 |

## 5. 用户交互流程

### 5.1 创建/编辑模型

1. 用户进入模型管理页面
2. 填写模型信息：真实模型名、别名、level、provider
3. 保存后模型进入可用池

### 5.2 Key 绑定模型

1. 用户进入 Key 管理页面
2. 选择要配置的 Key
3. 从模型列表中选择该 Key 可用的模型
4. 保存后该 Key 只能访问绑定的模型

### 5.3 路由调用

1. 收到请求，提取模型名
2. 判断是否是 `auto` 模型
3. 如果是，走 Auto Routing 逻辑
4. 如果不是，查别名映射或直接用模型名查找
5. 按优先级/轮询策略选择一个模型执行

## 6. 示例

### 6.1 别名路由示例

```
配置:
  - qwen-3      别名: ["gpt-4"]
  - gpt-4-turbo 别名: ["gpt-4"]

请求 "gpt-4" (fast level):
  → 匹配: [qwen-3, gpt-4-turbo]
  → 轮询: qwen-3 → gpt-4-turbo → qwen-3...
```

### 6.2 Auto Routing 示例

```
配置:
  - fast pool:     [qwen-3, gemini-2-flash]
  - standard pool: [gpt-4, claude-3-5-sonnet]
  - slow pool:    [gpt-4o, claude-3-opus]

请求 "auto" + 2000 tokens:
  → token < 2000 → fast pool
  → 返回: qwen-3 (第一轮) / gemini-2-flash (第二轮)

请求 "auto" + 20000 tokens:
  → token > 8000 → slow pool
  → 返回: gpt-4o (第一轮) / claude-3-opus (第二轮)
```

## 7. 待确认问题

1. **Level 数量**：固定 3 个（fast/standard/slow）还是可扩展？
2. **别名数量**：一个模型可以有多个别名吗？
3. **Key 绑定方式**：用户手动选择还是系统自动分配？
4. **模型来源**：模型列表是系统预设还是用户添加？
5. **Token 阈值**：fast/standard/slow 的边界值是否可配置？

## 8. 后续计划

| Phase | 内容 |
|-------|------|
| Phase 1 | 基础路由（别名 + 准确路由）+ 模型管理 UI |
| Phase 2 | Auto Routing（token 量判断）+ Level 分组 |
| Phase 3 | 优先级配置 + 故障转移 |
| Phase 4 | 使用统计 + 能力标签 |
