# GPT-Load 二次开发：Auto 智能路由

基于 GPT-Load 的二次开发项目，实现 Auto 复杂度路由、统一模型目录和模型自动去重功能。

## 项目概述

本项目在 GPT-Load 原有能力基础上，新增以下核心功能：

| 功能 | 说明 | 状态 |
|------|------|------|
| Auto 复杂度路由 | 根据请求复杂度自动选择合适模型 | 待开发 |
| 统一模型目录 | 聚合所有分组模型，提供统一 API | 待开发 |
| 模型自动去重 | 智能发现并建议聚合重复模型 | 待开发 |

## 核心能力

### Auto 复杂度路由

根据请求特征（token 数量、tools、vision 等）自动将请求路由到合适的模型分组：

```
简单请求 (simple): token < 2000, 无 tools → 使用 lite 模型
中等请求 (medium): token 2000-8000 或有 tools → 使用 pro 模型
复杂请求 (complex): token > 8000 或有 vision 或 tools > 3 → 使用 max 模型
```

### 统一模型目录

提供 `/v1/models` 端点，聚合所有分组中的模型，统一对外暴露：

```json
{
    "object": "list",
    "data": [
        {
            "id": "gpt-4o",
            "display_name": "GPT-4o",
            "groups": ["openai-official", "azure"],
            "providers": ["openai", "azure"]
        }
    ]
}
```

### 模型自动去重

智能发现被多个分组提供的相同模型，提供一键聚合建议。

## 技术架构

```
客户端 → Auto路由中间件 → 统一模型目录 → GPT-Load 底座 → 上游API
```

### 目录结构

```
.
├── SPEC.md                    # 完整规格文档
├── README.md                  # 本文件
├── .monkeycode/
│   ├── MEMORY.md              # 记忆文件
│   ├── docs/                  # 项目文档
│   │   ├── overview.md        # 需求概述
│   │   ├── architecture.md    # 架构设计
│   │   ├── compatibility.md    # 兼容性分析
│   │   └── implementation-plan.md  # 实施计划
│   └── specs/
│       └── auto-routing/      # Auto路由功能规格
│           ├── requirements.md # EARS需求规格
│           ├── design.md      # 技术设计
│           └── tasklist.md    # 任务清单
├── backend/                   # 后端代码 (待创建)
│   └── internal/
│       ├── autoroute/         # Auto路由模块
│       └── handler/           # HTTP处理模块
└── web/                       # 前端代码 (待创建)
    └── src/
        └── views/
            ├── auto-routing/  # Auto路由配置页面
            ├── model-catalog/  # 模型目录页面
            └── model-dedup/   # 模型去重页面
```

## 快速开始

### 前提条件

- Go 1.21+
- Node.js 18+
- GPT-Load v2.x

### 开发计划

| Phase | 内容 | 工期 |
|-------|------|------|
| Phase 1 | Auto 复杂度路由 | 2-3 周 |
| Phase 2 | 统一模型目录 | 1 周 |
| Phase 3 | 模型去重建议 | 1 周 |
| Phase 4 | 优化与上线 | 1-2 周 |

详见 [实施计划](./.monkeycode/docs/implementation-plan.md)

## 相关资源

- [完整规格文档](./SPEC.md)
- [需求规格](./.monkeycode/specs/auto-routing/requirements.md)
- [技术设计](./.monkeycode/specs/auto-routing/design.md)
- [GPT-Load 原始项目](https://github.com/tbphp/gpt-load)

## 协议

基于 MIT 协议的开源项目。
