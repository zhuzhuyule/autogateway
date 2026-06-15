# FreeModels 官方目录校验层 — 设计

日期: 2026-06-15
状态: 设计已批准,待实现

## 背景与问题

页面(ModelCatalog / Playground / V3GroupDetail)显示的模型列表与 Free 标志,
真实来源是后端 FreeModels Registry —— `internal/services/freemodels_registry.go`
周期性(每 6h)拉取上游 `https://ofind.cn/FreeModels/data/models.json`
(项目 `zhuzhuyule/FreeModels`),前端 `web/src/api/freemodels.ts` 经
`/api/freemodels/registry` 消费。前端静态表 `web/src/data/freeProviders.ts`
仅在 registry **miss 时**作为 fallback。

**问题**:上游 ofind 数据不与各 provider 官方目录校验,存在"幽灵模型"。
2026-06-15 实测,ofind 给 SambaNova 列了 **9 个模型且全部 `is_free=true`**,
而 SambaNova 官方 `/v1/models`(公开、免 key)**只有 6 个**:

| ofind registry (9, 全 is_free=true) | 官方目录(6) | 判定 |
|---|---|---|
| Meta-Llama-3.3-70B-Instruct / DeepSeek-V3.1 / DeepSeek-V3.2 / MiniMax-M2.7 / gpt-oss-120b / gemma-4-31B-it | ✅ | 正常 |
| Llama-4-Maverick-17B-128E-Instruct | ❌ | 官方已下线 |
| gemma-3-12b-it | ❌ | 已被取代 |
| gemma-4-31b-32 | ❌ | 官方不存在的可疑 ID |

结果:页面显示这些不存在的模型并打 Free 标志,用户调用必然 model-not-found(不通),
且 Free 标志整体可信度受损。注:SambaNova 本身是**账户档级免费(免费 tier 30 RPM,
限额内不计费),无 per-model 免费/付费之分**,官方 6 个模型在免费 tier 下都可调。

## 目标

在唯一数据源(registry)处,用各 provider 官方目录校验并**剔除**目录外的模型,
让所有 registry 消费方(UI + 路由先验)一致地只看到真实存在的模型。

## 非目标(YAGNI)

- 需 key 才能拉 `/v1/models` 的 provider 校验(借 group key)—— 推迟,先只做免 key 公开端点。
- 校验开关 / 允许清单的设置 UI —— 用代码常量,不做配置面。
- 重新定义 Free 标志语义(per-model vs 账户级)—— 单独议题,本设计不含。
- 前端改动 —— 本设计纯后端;前端无需改即可受益。

## 方案

在后端 registry 刷新链路插入一个隔离、可测的"官方目录校验层"。

### 数据流(在 `fetchAndStore` 现有链路插一步)

```
parseUpstream(归一化 snake_case→camelCase)
   → 【catalog validator 过滤】          ← 新增
   → replaceIndex(建索引 / 对外服务)
   → saveToDisk(磁盘缓存)
```

过滤在建索引与落盘**之前**完成,因此对外服务的 registry、磁盘缓存、冷启动
全部是已净化数据;ModelCatalog、Playground、路由先验等所有消费方自动一致。

### 新单元 `internal/services/catalog_validator.go`

设计为隔离、可独立测试的两部分:

1. **纯函数(无网络,核心逻辑)**
   ```
   filterByOfficialCatalog(
       models []FreeModelMeta,
       official map[string]map[string]bool, // providerId → {lower(modelId): true}
   ) []FreeModelMeta
   ```
   - 对**允许清单内**且 `official` 有非空集合的 provider:丢弃 `lower(modelId)`
     不在集合中的模型。
   - 允许清单外的 provider:原样透传(零行为变化)。
   - `official` 中某 provider 缺失或集合为空:该 provider 原样透传(降级,不删)。

2. **网络取数(可注入 fake 测试)**
   ```
   fetchOfficialCatalog(ctx, baseURL string) (map[string]bool, error)
   ```
   - GET `{baseURL}/models`,解析 OpenAI 标准 `{data:[{id}]}`,返回 `lower(id)` 集合。
   - 复用 `freeModelsHTTPTimeout` 与 registry 的 httpClient 风格。

3. **编排**(在 `fetchAndStore` 内,`parseUpstream` 后调用)
   - 遍历允许清单,对每个 provider 派生端点 → 拉官方目录 → 汇成 `official` map
     → 调 `filterByOfficialCatalog` → 替换 `env.Models`。

### 允许清单与端点派生

- 后端常量:`catalogValidatedProviders = []string{"sambanova"}`(代码常量;新增 provider
  = 加一行,且需先确认其 `/v1/models` 确为公开免 key)。
- 端点 = `env.ProviderMeta[providerId].APIBaseURL` + `/models`
  (SambaNova 即 `https://api.sambanova.ai/v1/models`)。
  `APIBaseURL` 缺失则跳过该 provider。

### 失败与安全降级(关键)

- 官方端点超时 / 非 2xx / 解析错 → **跳过该 provider 的过滤,保留 registry 原样**。
  绝不因瞬时网络问题丢数据。
- 官方目录解析出 **0 个 id**(可能是 200 的验证码 / HTML)→ 视同失败,跳过过滤,
  防止误删整个 provider 的模型。
- 仅当官方集合**非空**时才执行剔除。
- 校验仅作用于允许清单内 provider;清单外 provider 永不受影响。

## 测试

- **纯函数表驱动**(无网络):
  - registry 9 个 SambaNova + 官方 6 个 → 结果 6,幽灵 3 个(Maverick / gemma-3-12b-it /
    gemma-4-31b-32)被删。
  - 官方集合为空 → 不删(降级)。
  - provider 不在允许清单 → 原样。
  - 允许清单内但 `official` 无该 provider 条目 → 原样(降级)。
- **编排测试**:注入 fake fetcher(含一个返回 error、一个返回空集合的用例),
  验证 `fetchAndStore` 在失败时不丢数据。无真实网络依赖。

## 预期效果

刷新后 SambaNova 在 registry 仅剩官方 6 个;页面上 `Llama-4-Maverick` 等连同
其 Free 标志一并消失,对所有 registry 消费方一致生效。其他 provider 行为不变。

## 关联

- 已先行修正前端 fallback 表 `web/src/data/freeProviders.ts` 的 SambaNova 条目
  (删 2 个失效 ID、补 `gemma-4-31B-it`),与本校验层互补(fallback 与主源都准)。
- 数据源背景见记忆 `reference_sambanova_catalog`、`reference_yangmao_free_tiers`。
