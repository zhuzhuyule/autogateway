# FreeModels × api-center 统一架构设计

> **状态**: Draft, awaiting user review  
> **作者**: zac + Claude  
> **日期**: 2026-05-25  
> **影响仓库**: `github.com/zhuzhuyule/FreeModels`, `github.com/zhuzhuyule/autogateway` (api-center)

## Goal

把两个项目（FreeModels 元数据源 + api-center 网关）重新定位为经典的 **data plane / control plane** 分工，对齐 OpenAI `/v1/models` 标准契约，让任何第三方 OpenAI client 都能 drop-in 消费。趁两个项目都还没对外用户的窗口期，一次性把 schema 改干净，避免未来 breaking change 的迁移成本。

## 背景

两个项目长期独立演进，导致：

1. **modelId 风格不统一** — 14 个 provider 里 3 个 raw、11 个加 `<provider>/` 前缀，下游每次都要做剥前缀的活，OpenRouter `:free` 后缀踩过两次坑
2. **数据卫生差** — api-center 端 `ListModelIDsByProvider` 不过滤 `is_free`，导致 363 个付费 model 被错算成 free
3. **重复劳动** — api-center 内部 `parseUpstream` 重新建一遍 schema，跟 FreeModels 的 `toOpenAICompatible` 双重维护
4. **OpenAI 标准不严格** — FreeModels 输出已经接近 OpenAI 兼容，但 `id` 字段不是真正的 raw API id，每条 model 缺 `object: "model"` 和 `owned_by`

## 顶层愿景：data plane / control plane 分工

```
┌────────────────────────────────────────────────────────┐
│  Consumer Layer                                         │
│  LobeChat / NextChat / OneAPI / Dify / 自定义 OpenAI client │
└──────────────────────────┬─────────────────────────────┘
                           │ OpenAI /v1/* 协议
                           ▼
┌────────────────────────────────────────────────────────┐
│  Control Plane: api-center                              │
│  ├─ /v1/models               (聚合 + 白名单 + free 标签) │
│  ├─ /v1/chat/completions     (SWRR + 配额 + 鉴权)       │
│  └─ /api/*                   (管理 UI + group 维护)     │
└──────────────────────────┬─────────────────────────────┘
                           │ HTTP fetch + 6h 缓存
                           ▼
┌────────────────────────────────────────────────────────┐
│  Data Plane: FreeModels                                 │
│  ├─ /data/models.json                  (聚合 OpenAI-compat) │
│  ├─ /data/providers/<p>/models.json    (per-provider drop-in) │
│  ├─ /data/views/<view>/models.json     (过滤视图)        │
│  └─ /FreeModels/v1/models              (HTTP drop-in 端点) │
└────────────────────────────────────────────────────────┘
```

关键性质：
- **单向依赖**：api-center 消费 FreeModels，FreeModels 完全不知道 api-center 的存在
- **FreeModels 无运行时状态**：纯静态 JSON + CDN，任何第三方都能直接服务
- **职责分离**：FreeModels 只回答"这个 model 存在吗 / 怎么调 / 多少钱 / 有哪些能力"；api-center 只处理"实际转发 + 路由 + 鉴权 + 配额"

## 三条不可妥协的契约

设计的锚点，未来任何 PR 都不能动这三条。

### 契约一：OpenAI `/v1/models` 严格兼容

FreeModels 和 api-center 的所有 model 输出都必须满足：

```typescript
interface OpenAIModel {
  id: string;          // POST body.model 直接填的字符串（raw API id）
  object: "model";     // 字面量
  created: number;     // unix timestamp
  owned_by: string;    // provider id (FreeModels 内部 id)
  // 以上为 OpenAI 标准必填字段
  // 以下为扩展字段（OpenAI 标准允许加字段）
  is_free?: boolean;
  free_mechanism?: string;
  free_quota?: object;
  capabilities?: string[];
  // ... 其他元数据
}
```

任何标准 OpenAI client 不读扩展字段也能用；高级 client 可以按扩展字段做 UI 增强。

### 契约二：复合主键 `(owned_by, id)`

跨 provider 同名 model 是 **合理的**（同一个开源模型不同 host，如 Groq/Cerebras/SambaNova 都有 `llama-3.1-8b`）。任何"id 唯一"的假设都是错的。

- 聚合视图 `data/models.json` 里允许 `id` 重复，只要 `(owned_by, id)` 唯一
- 内部所有 `Map<id, model>` 改为 `Map<provider+":"+id, model>` 或等价复合键
- UI 选择器按复合键工作，展示 "llama-3.1-8b via Groq" / "llama-3.1-8b via Cerebras"

### 契约三：Provider 元数据上提到 `providers` map

per-provider 唯一的字段（`apiBaseUrl`, `channelType`, `priceCurrency`, `priceUnit`）不重复存在每条 model 上。已经做了，写进契约锁定。

## 改造路径（5 个 Phase）

每个 Phase 都是独立可上线的，可以随时停下来。

### P0 — api-center 紧急 bug 修复

**范围**：`internal/services/freemodels_registry.go` + `internal/services/group_service.go`

**改动**：
- `ListModelIDsByProvider(providerID)` 加 `freeOnly bool` 参数
- `computeFreeModelIDs` 调 free-only 版本
- 立刻消除 363 个付费 model 污染 `free_models`

**为什么独立**：不依赖 FreeModels 任何改动，是个 5 分钟的 bug fix，但用户能看见的数据正确性提升最大。

### P1 — FreeModels schema 一次到位

**范围**：`scripts/hub/types.ts`, `aggregator.ts`, `family.ts`, `enhancer.ts`，所有 14 个 provider 插件

**改动**：
- `ExtendedModelObject.id` 改为 raw API id —— 定义为"POST body.model 直接填的字符串，跟上游 `/v1/models` 返回的 id 完全一致"
- 具体剥除规则：剥掉 `<freemodels-provider-id>/` 前缀（如 `bigmodel/`, `groq/`, `xinghuo/`），**不剥** `<owner>/` 前缀（如 OpenRouter 的 `qwen/`、NVIDIA 的 `bytedance/`），**保留** OpenRouter `:free` 后缀、Cloudflare `@cf/` 前缀（调 API 必需）
- 每条 model 输出补 `object: "model"` + `owned_by` 字段
- aggregator 去重键改为 `(provider, id)` 复合键
- per-provider JSON 文件本来就是 raw id，零改动；聚合视图是主要工作
- README / docs 加一节："任一 JSON drop-in 当 OpenAI `/v1/models` 用"

**剥前缀对照表（14 个 provider）**：

| Provider | 当前聚合视图 id | 改造后 id | 说明 |
|---|---|---|---|
| `bigmodel` | `bigmodel/glm-4-flash-250414` | `glm-4-flash-250414` | 剥单一前缀 |
| `cerebras` | `cerebras/llama3.1-8b` | `llama3.1-8b` | 剥单一前缀 |
| `cloudflare` | `cloudflare/@cf/openai/gpt-oss-120b` | `@cf/openai/gpt-oss-120b` | 剥 `cloudflare/`，保留 `@cf/` |
| `cohere` | `cohere/c4ai-aya-expanse-32b` | `c4ai-aya-expanse-32b` | 剥单一前缀 |
| `gitee` | `jina-clip-v1` | `jina-clip-v1` | **已 raw**，无改动 |
| `github` | `github/AI21-Jamba-1-5-Large` | `AI21-Jamba-1-5-Large` | 剥单一前缀 |
| `google` | `google/gemini-2.5-flash-lite` | `gemini-2.5-flash-lite` | 剥单一前缀 |
| `groq` | `groq/openai/gpt-oss-20b` | `openai/gpt-oss-20b` | 剥 `groq/`，保留 `openai/` owner |
| `longcat` | `longcat/LongCat-Flash-Chat` | `LongCat-Flash-Chat` | 剥单一前缀 |
| `nvidia` | `bytedance/seed-oss-36b-instruct` | `bytedance/seed-oss-36b-instruct` | **已 raw**，无改动 |
| `openrouter` | `qwen/qwen3-coder:free` | `qwen/qwen3-coder:free` | **已 raw**，无改动（保留 `:free`） |
| `sambanova` | `sambanova/llama3-8b` | `llama3-8b` | 剥单一前缀 |
| `xingchen` | `xingchen/xop35qwen2b` | `xop35qwen2b` | 剥单一前缀 |
| `xinghuo` | `xinghuo/lite` | `lite` | 剥单一前缀 |

**Breaking 影响**：所有 CDN 消费方会感知 schema 变化，但目前只有 api-center 一个已知消费方（同步切）。

### P2 — api-center 切到新 schema

**范围**：`internal/services/freemodels_registry.go`, `internal/services/group_service.go`, `web/src/api/freemodels.ts`, `web/src/data/freeProviders.ts`

**改动**：
- `parseUpstream` 处理新字段（`object`, `owned_by`）
- 删除所有 strip-prefix / `:free` 后缀剥除 / lowercase 归一化 hack
- `computeFreeModelIDs` 简化为直接 string 求交集（6 行以内）
- `byProvMod` 索引 key 保持不变（本来就是 `(provider, modelID)`），但 modelID 内容已经是 raw API id
- 前端 `freemodels.ts` / `freeProviders.ts` 删除残留归一化代码

### P3 — FreeModels CDN `/v1/models` HTTP 端点

**范围**：FreeModels 部署配置（Cloudflare Worker / GitHub Pages route / Vercel rewrite）

**改动**：
- 在 `ofind.cn/FreeModels/v1/models` 加路由，指向 `data/models.json`
- 加 OpenAI 兼容响应头（`Content-Type: application/json`, CORS）
- 文档示例：`LobeChat 配置 BaseURL = https://ofind.cn/FreeModels/v1` 直接能浏览全网免费模型

**战略意义**：把 FreeModels 从"数据集"升级成"服务"，第三方零代码可接入。

### P4 — 跨 provider 智能路由（独立 sub-project）

**范围**：api-center aggregate group 增强

**改动**：
- 利用 FreeModels 的 `model_family` / `aliases` 字段
- 用户请求 `llama-3.1-8b` 时，aggregate group 自动从 Groq/Cerebras/SambaNova 选最优 provider（按延迟、配额剩余、价格）

**为什么独立**：这是个独立的能力扩展，不依赖前 4 个 phase 的 schema 改动。需要单独 brainstorming + spec。本文档仅作为入口提及，不展开。

## 预留的扩展位

设计原则：**所有未来扩展走"加字段"路线，永远不删字段、不改字段含义**。

| 扩展方向 | 怎么扩 | 是否 breaking |
|---|---|---|
| 新增 provider | 加一个 plugin | 否 |
| 新增能力字段（如 `supports_audio`） | 加字段不删字段 | 否 |
| 新增过滤视图（如 `/views/tool-use-only`） | 现有视图机制 | 否 |
| 模型能力评测结果 | `data[].benchmark` 子对象 | 否 |
| 配额追踪 / Webhook 通知 | api-center 侧新增订阅机制 | 否 |
| 多语言模型描述 | `description.zh` / `description.en` | 小破坏（现有字段是 string，需要 v2） |

## YAGNI 边界（明确不做的事）

- **不做** FreeModels 的运行时鉴权 / 配额追踪 — 那是 api-center 的职责
- **不做** api-center 内部维护"硬编码模型属性"清单 — 所有元数据来自 FreeModels
- **不做** `callable_id` 这种二次抽象字段 — 直接用 `id` = raw API id
- **不做** 删除/重命名旧字段 — 加字段是免费的，删字段是 breaking 的
- **不做** P4 的智能路由（本 spec 不覆盖，单独 sub-project）

## 当前状态 snapshot

**api-center working tree（未提交）**：
- 11 个 modified files（之前对话 Phase 1+2 `free_models` 持久化的产物）
- 这些改动跟本 spec 的 P2 方向一致，但需要按新 schema 重做。建议：把这部分改动作为 P2 的起点，调整成新 schema 后一起提交

**FreeModels working tree**：clean

**已部署状态**：
- FreeModels：`https://ofind.cn/FreeModels/data/models.json` 持续更新
- api-center：本地 dev mode（air hot-reload），192.168.3.3 跟本地同步，192.168.3.10 滞后

## 执行约束

- 用户已确认 commit to main（两个 repo 都没对外用户，breaking 改动免费）
- 不创建 worktree（用户偏好直接在 main 上动）
- 每个 phase 完成后单独 commit，不混合
- P3 部署改动需要用户提供 ofind.cn 的部署能力（或退化为出 PR 让用户手动 deploy）

## 不在范围（Out of scope）

- 商业化 / 付费 tier
- v1/v2 schema 并存方案
- FreeModels 数据源的扩展（新增 provider）
- api-center 的非元数据相关功能（鉴权、UI 重构等）
