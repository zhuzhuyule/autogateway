# 模型路由系统 - 技术设计文档

## 1. 技术架构

### 1.1 系统组件

```
┌─────────────────────────────────────────────────────────────┐
│                        Router                                │
│  ┌──────────┐  ┌──────────────┐  ┌────────────────────┐    │
│  │AutoRouter│  │ AliasRouter  │  │   ExactRouter       │    │
│  └────┬─────┘  └──────┬───────┘  └─────────┬──────────┘    │
│       │               │                    │                │
│       └───────────────┼────────────────────┘                │
│                       ▼                                     │
│              ┌─────────────────┐                           │
│              │  ModelPool       │                           │
│              │  (fast/standard/slow)                        │
│              └─────────────────┘                           │
└─────────────────────────────────────────────────────────────┘
```

### 1.2 数据流

```
请求 → Router → [是 auto?] → Yes → AutoRouter → 按 token 选 pool
                    │
                    No → AliasRouter → 查 alias_mapping
                              │
                              ├─ 找到别名 → 轮询匹配模型
                              └─ 没找到 → ExactRouter → 用模型名查找
```

## 2. 后端设计

### 2.1 数据库 Schema

```sql
-- 模型元数据表
CREATE TABLE model_metadata (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    real_model VARCHAR(255) NOT NULL UNIQUE,
    provider VARCHAR(50) NOT NULL,
    capabilities TEXT,  -- JSON array: ["vision", "tools"]
    is_active BOOLEAN DEFAULT true,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- 别名表
CREATE TABLE model_aliases (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    model_id INTEGER NOT NULL,
    alias VARCHAR(255) NOT NULL,
    priority INTEGER DEFAULT 100,
    level VARCHAR(20) DEFAULT 'standard',
    FOREIGN KEY (model_id) REFERENCES model_metadata(id),
    UNIQUE(alias, model_id)
);

-- Key 绑定表
CREATE TABLE key_model_bindings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    key_id INTEGER NOT NULL,
    model_id INTEGER NOT NULL,
    alias_id INTEGER,
    is_enabled BOOLEAN DEFAULT true,
    priority INTEGER DEFAULT 100,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (key_id) REFERENCES api_keys(id),
    FOREIGN KEY (model_id) REFERENCES model_metadata(id),
    FOREIGN KEY (alias_id) REFERENCES model_aliases(id)
);

-- 索引
CREATE INDEX idx_aliases_alias ON model_aliases(alias);
CREATE INDEX idx_aliases_level ON model_aliases(level);
CREATE INDEX idx_bindings_key ON key_model_bindings(key_id);
```

### 2.2 API 设计

#### 模型管理

| Method | Endpoint | 说明 |
|--------|----------|------|
| GET | `/models` | 获取所有模型 |
| GET | `/models/{id}` | 获取单个模型 |
| POST | `/models` | 创建模型 |
| PUT | `/models/{id}` | 更新模型 |
| DELETE | `/models/{id}` | 删除模型 |

#### 别名管理

| Method | Endpoint | 说明 |
|--------|----------|------|
| GET | `/models/{id}/aliases` | 获取模型的所有别名 |
| POST | `/models/{id}/aliases` | 为模型添加别名 |
| DELETE | `/aliases/{id}` | 删除别名 |
| GET | `/aliases` | 获取所有别名映射 |

#### Key 绑定

| Method | Endpoint | 说明 |
|--------|----------|------|
| GET | `/keys/{id}/bindings` | 获取 Key 的模型绑定 |
| PUT | `/keys/{id}/bindings` | 更新 Key 的模型绑定 |
| GET | `/keys/{id}/available-models` | 获取 Key 可用的模型列表 |

#### 路由

| Method | Endpoint | 说明 |
|--------|----------|------|
| POST | `/route` | 路由请求（内部使用） |

### 2.3 路由算法

```typescript
// 路由决策
async function route(request: {
  model: string;
  token_count: number;
  key: string;
}): Promise<{ real_model: string; provider: string }> {

  // 1. Auto Routing: 模型名为 "auto"
  if (request.model === 'auto') {
    const level = selectLevelByToken(request.token_count);
    return selectModelFromLevel(level, request.key);
  }

  // 2. 别名路由
  const aliasMappings = await findByAlias(request.model);
  if (aliasMappings.length > 0) {
    return selectByPriority(aliasMappings);
  }

  // 3. 准确路由
  const exactModels = await findByExactName(request.model);
  if (exactModels.length > 0) {
    return selectByPriority(exactModels);
  }

  throw new Error(`No model found for: ${request.model}`);
}

// 按 token 量选择 level
function selectLevelByToken(tokenCount: number): string {
  if (tokenCount < 2000) return 'fast';
  if (tokenCount < 8000) return 'standard';
  return 'slow';
}

// 按优先级选择
function selectByPriority(models: ModelRef[]): ModelRef {
  return models.sort((a, b) => a.priority - b.priority)[0];
}
```

## 3. 前端设计

### 3.1 页面结构

```
/settings/models                    # 模型管理
  ├── ModelList                    # 模型列表
  ├── ModelForm                    # 创建/编辑模型
  └── ModelDetail                  # 模型详情（含别名管理）

/keys/{id}/model-bindings          # Key 模型绑定
  └── BindingForm                  # 绑定配置
```

### 3.2 组件设计

| 组件 | 说明 |
|------|------|
| ModelTable | 模型列表展示 |
| ModelForm | 创建/编辑模型表单 |
| AliasTag | 别名标签展示 |
| BindingSelector | Key 模型绑定选择器 |
| LevelBadge | Level 标签（fast/standard/slow） |
| CapabilityChip | 能力标签（vision/tools） |

### 3.3 状态管理

```typescript
// stores/models.ts
interface ModelsState {
  models: ModelMetadata[];
  aliases: AliasMapping[];
  bindings: KeyModelBinding[];
  loading: boolean;
}

// actions
async function fetchModels(): Promise<void>;
async function createModel(data: CreateModelDTO): Promise<ModelMetadata>;
async function updateModel(id: number, data: UpdateModelDTO): Promise<void>;
async function deleteModel(id: number): Promise<void>;
async function addAlias(modelId: number, alias: string): Promise<void>;
async function updateKeyBindings(keyId: number, bindings: Binding[]): Promise<void>;
```

## 4. 实现步骤

### Phase 1: 基础数据模型

1. 创建数据库表
2. 实现模型 CRUD API
3. 实现别名 CRUD API
4. 创建基础前端页面

### Phase 2: Key 绑定

1. 实现 Key 模型绑定 API
2. 实现 Key 绑定前端 UI
3. 集成到 Key 管理页面

### Phase 3: 路由逻辑

1. 实现 Auto Routing 算法
2. 实现别名路由算法
3. 实现准确路由算法
4. 单元测试

### Phase 4: 增强功能

1. 优先级配置 UI
2. 故障转移逻辑
3. 使用统计

## 5. 配置项

```yaml
# config.yaml
routing:
  auto_routing:
    thresholds:
      fast: 2000      # token < 2000 → fast
      standard: 8000  # token < 8000 → standard
    levels:
      fast:
        - qwen-3
        - gemini-2-flash
      standard:
        - gpt-4
        - claude-3-5-sonnet
      slow:
        - gpt-4o
        - claude-3-opus
  fallback:
    enabled: true
    max_retries: 3
```

## 6. 错误处理

| 错误码 | 说明 | 处理 |
|--------|------|------|
| MODEL_NOT_FOUND | 模型不存在 | 返回 404 |
| NO_AVAILABLE_MODEL | 没有可用模型 | 返回 500，触发告警 |
| ALIAS_CONFLICT | 别名冲突 | 返回 400 |
| KEY_NOT_BOUND | Key 未绑定模型 | 返回 400 |
| ROUTE_FAILED | 路由失败 | 尝试故障转移 |

## 7. 监控指标

| 指标 | 说明 |
|------|------|
| route_requests_total | 路由请求总数 |
| route_by_level | 按 level 分组的路由数 |
| route_failures | 路由失败数 |
| model_usage | 各模型使用量 |
| alias_usage | 各别名使用量 |
