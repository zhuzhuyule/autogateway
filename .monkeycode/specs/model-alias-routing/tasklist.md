# 模型路由系统 - 任务清单

## Phase 1: 基础数据模型

### 后端任务

- [ ] 创建 `model_metadata` 数据库表
- [ ] 创建 `model_aliases` 数据库表
- [ ] 实现 `GET /models` API
- [ ] 实现 `GET /models/{id}` API
- [ ] 实现 `POST /models` API
- [ ] 实现 `PUT /models/{id}` API
- [ ] 实现 `DELETE /models/{id}` API
- [ ] 实现 `GET /models/{id}/aliases` API
- [ ] 实现 `POST /models/{id}/aliases` API
- [ ] 实现 `DELETE /aliases/{id}` API
- [ ] 实现 `GET /aliases` API

### 前端任务

- [ ] 创建 `ModelTable` 组件
- [ ] 创建 `ModelForm` 组件
- [ ] 创建 `ModelDetail` 组件
- [ ] 创建模型管理页面 `/settings/models`
- [ ] 创建别名管理 UI

### 测试任务

- [ ] 编写模型 CRUD 单元测试
- [ ] 编写别名 CRUD 单元测试

## Phase 2: Key 绑定

### 后端任务

- [ ] 创建 `key_model_bindings` 数据库表
- [ ] 实现 `GET /keys/{id}/bindings` API
- [ ] 实现 `PUT /keys/{id}/bindings` API
- [ ] 实现 `GET /keys/{id}/available-models` API
- [ ] 更新 Key 详情页，添加模型绑定入口

### 前端任务

- [ ] 创建 `BindingSelector` 组件
- [ ] 创建 Key 模型绑定页面
- [ ] 创建 Level 标签组件 `LevelBadge`
- [ ] 创建能力标签组件 `CapabilityChip`

### 测试任务

- [ ] 编写 Key 绑定 API 单元测试

## Phase 3: 路由逻辑

### 后端任务

- [ ] 实现 `selectLevelByToken` 函数
- [ ] 实现 `findByAlias` 函数
- [ ] 实现 `findByExactName` 函数
- [ ] 实现 `selectByPriority` 函数
- [ ] 实现主路由函数 `route`
- [ ] 实现故障转移逻辑
- [ ] 更新网关路由中间件，集成新路由

### 测试任务

- [ ] 编写路由算法单元测试
- [ ] 编写故障转移集成测试
- [ ] 手动测试 Auto Routing
- [ ] 手动测试别名路由
- [ ] 手动测试准确路由

## Phase 4: 增强功能

### 后端任务

- [ ] 添加优先级配置接口
- [ ] 添加使用统计接口
- [ ] 添加模型健康检查

### 前端任务

- [ ] 添加优先级拖拽排序
- [ ] 添加使用统计展示
- [ ] 添加模型搜索/筛选

### 测试任务

- [ ] 端到端测试完整流程

## 估算工时

| Phase | 后端 | 前端 | 测试 | 总计 |
|-------|------|------|------|------|
| Phase 1 | 2d | 1d | 0.5d | 3.5d |
| Phase 2 | 1d | 1d | 0.5d | 2.5d |
| Phase 3 | 2d | 0.5d | 1d | 3.5d |
| Phase 4 | 1d | 1d | 0.5d | 2.5d |
| **总计** | **6d** | **3.5d** | **2.5d** | **12d** |

## 里程碑

| 里程碑 | 完成标准 |
|--------|---------|
| M1 | Phase 1 + Phase 2 完成，基础路由可工作 |
| M2 | Phase 3 完成，Auto Routing + 别名路由可工作 |
| M3 | Phase 4 完成，所有功能就绪 |
