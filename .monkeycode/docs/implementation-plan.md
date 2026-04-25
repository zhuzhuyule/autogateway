# 实施计划

## 项目周期

总工期: **6-8 周**

| Phase | 内容 | 工期 |
|-------|------|------|
| Phase 1 | Auto 复杂度路由 | 2-3 周 |
| Phase 2 | 统一模型目录 | 1 周 |
| Phase 3 | 模型去重建议 | 1 周 |
| Phase 4 | 优化与上线 | 1-2 周 |

---

## Phase 1: Auto 复杂度路由

### 目标
实现基于请求复杂度的自动路由功能。

### 任务分解

#### Week 1: 核心逻辑开发

| 任务 | 交付物 | 依赖 |
|------|--------|------|
| 复杂度分类器开发 | `internal/autoroute/complexity.go` | 无 |
| 分类器单元测试 | 测试用例 | 分类器 |
| 路由中间件开发 | `internal/autoroute/middleware.go` | 分类器 |
| 中间件单元测试 | 测试用例 | 中间件 |

#### Week 2: 集成与配置

| 任务 | 交付物 | 依赖 |
|------|--------|------|
| 配置管理开发 | `internal/autoroute/config.go` | 无 |
| 中间件集成 | `router.go` 修改 | 中间件 |
| 配置读写逻辑 | system_settings 读写 | 配置管理 |
| 回退策略实现 | middleware.go 更新 | 中间件 |

#### Week 3: 前端与测试

| 任务 | 交付物 | 依赖 |
|------|--------|------|
| Auto 路由配置页面 | Vue 页面 | 后端 API |
| 配置测试 | 手动测试 | 配置页面 |
| 集成测试 | 测试用例 | 全部组件 |
| 文档编写 | 使用文档 | 功能完成 |

**Phase 1 交付物:**
- `internal/autoroute/complexity.go`
- `internal/autoroute/middleware.go`
- `internal/autoroute/config.go`
- `web/src/views/auto-routing/index.vue`
- 单元测试 + 集成测试

---

## Phase 2: 统一模型目录

### 目标
提供统一的 `/v1/models` API，聚合所有分组模型。

### 任务分解

#### Week 4

| 任务 | 交付物 | 依赖 |
|------|--------|------|
| 模型目录 Handler 开发 | `handler/model_catalog_handler.go` | 无 |
| 缓存机制实现 | Handler 内部缓存 | Handler |
| 路由注册 | router.go 修改 | Handler |
| Web UI 页面 | Vue 页面 | 后端 API |
| 集成测试 | 测试用例 | 全部组件 |

**Phase 2 交付物:**
- `internal/handler/model_catalog_handler.go`
- `web/src/views/model-catalog/index.vue`
- 集成测试

---

## Phase 3: 模型去重建议

### 目标
智能发现被多个分组提供的相同模型，提供一键聚合功能。

### 任务分解

#### Week 5

| 任务 | 交付物 | 依赖 |
|------|--------|------|
| 去重建议服务 | `services/model_dedup_service.go` | 无 |
| 聚合建议服务 | `services/aggregate_suggestion.go` | 去重服务 |
| Web UI 建议页面 | Vue 页面 | 服务 |
| 一键创建 API | REST API | 服务 |
| 一键创建 UI | Vue 组件 | API |

**Phase 3 交付物:**
- `internal/services/model_dedup_service.go`
- `internal/services/aggregate_suggestion.go`
- `web/src/views/model-dedup/index.vue`
- 一键创建功能

---

## Phase 4: 优化与上线

### 目标
性能优化、监控完善、灰度发布。

### 任务分解

#### Week 6: 性能与监控

| 任务 | 交付物 | 依赖 |
|------|--------|------|
| 缓存优化 | 缓存层完善 | Phase 1-3 |
| 监控指标 | Metrics | Phase 1-3 |
| 日志规范 | 日志字段标准化 | Phase 1-3 |

#### Week 7: 测试与部署

| 任务 | 交付物 | 依赖 |
|------|--------|------|
| 完整测试 | 测试报告 | Phase 1-4 |
| 文档完善 | API 文档 | 功能完成 |
| 灰度发布方案 | 部署文档 | 测试完成 |
| 上线 | 生产环境 | 灰度验证 |

---

## 代码改动清单

### 新增文件 (约 1070 行)

| 文件 | 行数 |
|------|------|
| `internal/autoroute/complexity.go` | ~150 |
| `internal/autoroute/middleware.go` | ~150 |
| `internal/autoroute/config.go` | ~80 |
| `internal/handler/model_catalog_handler.go` | ~120 |
| `internal/services/model_dedup_service.go` | ~80 |
| `internal/services/aggregate_suggestion.go` | ~60 |
| `web/src/views/auto-routing/index.vue` | ~250 |
| `web/src/views/model-catalog/index.vue` | ~150 |
| `web/src/views/model-dedup/index.vue` | ~180 |
| **合计** | **~1070** |

### 修改文件 (约 170 行)

| 文件 | 改动 |
|------|------|
| `internal/router/router.go` | +30 行 |
| `internal/config/system_settings.go` | +40 行 |
| `internal/middleware/` | +20 行 |
| `web/src/router/index.ts` | +15 行 |
| `web/src/layout/Sidebar.vue` | +15 行 |
| `web/src/api/` | +50 行 |
| **合计** | **~170** |

### 不修改的文件

- `internal/proxy/server.go`
- `internal/services/subgroup_manager.go`
- `internal/keypool/provider.go`
- `internal/channel/`
- `internal/services/aggregate_group_service.go`

---

## 里程碑

| 里程碑 | 日期 | 交付内容 |
|--------|------|----------|
| M1 | Week 1 | 复杂度分类器 + 中间件核心逻辑 |
| M2 | Week 2 | 中间件集成 + 配置管理 |
| M3 | Week 3 | Web UI + Phase 1 完成 |
| M4 | Week 4 | 模型目录完成 |
| M5 | Week 5 | 去重建议完成 |
| M6 | Week 7 | 全部功能 + 测试 + 文档 |

---

## 风险与应对

| 风险 | 影响 | 应对措施 |
|------|------|---------|
| Token 估算不准确 | 路由误判 | 提供手动覆盖；持续优化 |
| 中间件增加延迟 | 性能下降 | 开销 < 0.2ms，可忽略 |
| 与上游版本冲突 | 合并困难 | 核心文件不修改 |
| 配置复杂度高 | 上手难 | Web UI 向导 |

详见 [SPEC.md](../../SPEC.md)
