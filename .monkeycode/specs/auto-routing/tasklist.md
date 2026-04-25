# Auto 路由功能任务清单

> 项目周期: 6-8 周
> 状态: 待开始

## Phase 1: Auto 复杂度路由 (Week 1-3)

### Week 1: 核心逻辑开发

- [ ] **T-001**: 创建 `internal/autoroute/types.go`
  - 定义 ComplexityLevel 类型
  - 定义 RequestAnalysis 结构体
  - 定义 ClassifierConfig 结构体
  - 定义 RouteConfig 结构体
  - 定义 GroupComplexityMapping 结构体
  - 定义 FallbackStrategy 结构体

- [ ] **T-002**: 开发 `internal/autoroute/complexity.go`
  - 实现 `NewClassifier` 构造函数
  - 实现 `Analyze` 方法 (请求分析)
  - 实现 `classifyLevel` 方法 (分级判断)
  - 实现 `estimateTokens` 函数 (token 估算)
  - 实现 `extractTextContent` 函数 (内容提取)

- [ ] **T-003**: 编写复杂度分类器单元测试
  - 测试 Simple 级别判断
  - 测试 Medium 级别判断
  - 测试 Complex 级别判断
  - 测试边界条件 (空请求、无效 JSON)
  - 测试 Token 估算准确性

- [ ] **T-004**: 开发 `internal/autoroute/middleware.go`
  - 实现 `Middleware` 中间件函数
  - 实现 `isChatCompletions` 函数
  - 实现 `selectTargetGroup` 函数
  - 实现 `getFallbackGroup` 函数
  - 实现 `isGroupAvailable` 函数
  - 实现 `setParam` 函数

- [ ] **T-005**: 编写中间件单元测试
  - 测试路由拦截
  - 测试路由透传
  - 测试分组选择
  - 测试回退逻辑

### Week 2: 集成与配置

- [ ] **T-006**: 开发 `internal/autoroute/config.go`
  - 实现 `ConfigManager` 结构体
  - 实现 `Load` 方法 (从数据库加载)
  - 实现 `Save` 方法 (保存到数据库)
  - 实现 `GetConfig` 方法 (获取配置)
  - 实现 `Validate` 方法 (配置验证)

- [ ] **T-007**: 集成中间件到路由
  - 修改 `internal/router/router.go`
  - 注册 Auto 路由中间件
  - 配置中间件参数

- [ ] **T-008**: 实现回退策略
  - 更新 middleware.go
  - 实现多级回退逻辑
  - 添加回退日志

- [ ] **T-009**: 配置管理 API
  - 实现配置读取 API
  - 实现配置写入 API
  - 实现配置验证 API

- [ ] **T-010**: Web API 路由注册
  - 注册 `/api/auto-routing/config` 路由
  - 注册 `/api/auto-routing/test` 路由

### Week 3: 前端与测试

- [ ] **T-011**: 开发 Auto 路由配置页面
  - `web/src/views/auto-routing/index.vue`
  - 启用/禁用开关
  - 阈值配置表单
  - 模型映射表格
  - 回退策略配置

- [ ] **T-012**: 开发配置测试功能
  - 请求体输入框
  - 分析结果展示
  - 目标分组预览

- [ ] **T-013**: 前端 API 集成
  - `web/src/api/auto-routing.ts`
  - 配置读取/保存
  - 路由测试调用

- [ ] **T-014**: 集成测试
  - 端到端测试
  - 性能测试
  - 边界条件测试

- [ ] **T-015**: 文档编写
  - 使用文档
  - API 文档
  - 部署文档

---

## Phase 2: 统一模型目录 (Week 4)

### Week 4

- [ ] **T-020**: 开发模型目录 Handler
  - `internal/handler/model_catalog_handler.go`
  - 实现 `ListModels` 方法
  - 实现缓存机制
  - 实现去重逻辑

- [ ] **T-021**: 路由注册
  - 修改 `internal/router/router.go`
  - 注册 `/v1/models` 路由
  - 注册 `/proxy/v1/models` 路由

- [ ] **T-022**: 开发模型目录页面
  - `web/src/views/model-catalog/index.vue`
  - 模型列表展示
  - 分组信息展示
  - 提供者信息展示

- [ ] **T-023**: 前端 API 集成
  - `web/src/api/model-catalog.ts`
  - 模型列表获取
  - 缓存刷新

- [ ] **T-024**: 集成测试
  - 模型列表 API 测试
  - 缓存机制测试
  - 去重逻辑测试

---

## Phase 3: 模型去重建议 (Week 5)

### Week 5

- [ ] **T-030**: 开发去重建议服务
  - `internal/services/model_dedup_service.go`
  - 实现 `GetDedupSuggestions` 方法
  - 实现模型收集逻辑
  - 实现分组分析逻辑

- [ ] **T-031**: 开发聚合建议服务
  - `internal/services/aggregate_suggestion.go`
  - 实现聚合分组创建建议
  - 实现权重分配建议

- [ ] **T-032**: 开发去重建议页面
  - `web/src/views/model-dedup/index.vue`
  - 建议列表展示
  - 分组来源展示
  - 一键创建按钮

- [ ] **T-033**: 开发一键创建功能
  - API: 创建聚合分组
  - API: 配置模型重定向
  - UI: 创建确认对话框

- [ ] **T-034**: 集成测试
  - 去重建议功能测试
  - 一键创建功能测试

---

## Phase 4: 优化与上线 (Week 6-7)

### Week 6: 性能与监控

- [ ] **T-040**: 缓存优化
  - 配置缓存
  - 模型目录缓存
  - 热点数据缓存

- [ ] **T-041**: 监控指标
  - Prometheus metrics
  - 路由决策计数
  - 路由错误计数
  - 路由延迟

- [ ] **T-042**: 日志规范化
  - 统一日志格式
  - 日志字段标准化
  - 日志级别规范

### Week 7: 测试与部署

- [ ] **T-050**: 完整测试
  - 单元测试
  - 集成测试
  - E2E 测试
  - 性能测试

- [ ] **T-051**: 文档完善
  - 完整 API 文档
  - 部署文档
  - 运维文档

- [ ] **T-052**: 灰度发布
  - 制定灰度策略
  - 监控灰度指标
  - 问题回滚方案

- [ ] **T-053**: 正式上线
  - 生产环境部署
  - 监控告警
  - 交付文档

---

## 任务统计

| Phase | 任务数 | 预计工时 |
|-------|--------|----------|
| Phase 1 | 15 | 3 周 |
| Phase 2 | 5 | 1 周 |
| Phase 3 | 5 | 1 周 |
| Phase 4 | 8 | 2 周 |
| **总计** | **33** | **7 周** |

---

## 依赖关系

```
T-001 ─┬─ T-002 ─ T-003 ─ T-004 ─ T-005
       │                          │
       └─ T-006 ─ T-007 ─ T-008 ─ T-009 ─ T-010
                                              │
T-011 ─ T-012 ─ T-013 ─ T-014 ─ T-015 ◄──────┘

T-020 ─ T-021 ─ T-022 ─ T-023 ─ T-024

T-030 ─ T-031 ─ T-032 ─ T-033 ─ T-034

T-040 ─ T-041 ─ T-042 ─ T-050 ─ T-051 ─ T-052 ─ T-053
```

---

## 里程碑

| 里程碑 | 日期 | 关键任务 |
|--------|------|----------|
| M1 | Week 1 末 | T-001 ~ T-005 完成 |
| M2 | Week 2 末 | T-006 ~ T-010 完成 |
| M3 | Week 3 末 | Phase 1 完成 |
| M4 | Week 4 末 | Phase 2 完成 |
| M5 | Week 5 末 | Phase 3 完成 |
| M6 | Week 7 末 | 全部完成 |
