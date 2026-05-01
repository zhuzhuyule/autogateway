# FreeModels ↔ api-center Provider 关联对齐 + 瘦身实施计划

**Goal:** 让 FreeModels 注册新 provider 后,api-center 自动认得(host 反查从 `apiBaseUrl` 派生),顺手砍掉 models.json 9% 冗余。

**Architecture:** FreeModels `ProviderMeta` 加 `apiBaseUrl` 等几个 per-provider 字段;model 上重复字段(`object` / `model_id` / `owned_by` / `price_currency` / `price_unit`)删掉。api-center registry 解析新 schema,反查从本地 `upstreamHosts[]` 改成"registry 优先 + 本地 fallback"。

**Tech Stack:** TypeScript (FreeModels 同步脚本) + Go (api-center 后端 registry) + Vue3 (api-center 前端 freemodels.ts / freeProviders.ts)

---

## Phase 1 — FreeModels 改 schema + 重生成

**仓库:** `/Users/zac/code/github/FreeModels`

### Task 1.1: ProviderMeta 加字段

**Files:** `scripts/hub/types.ts`

```ts
export interface ProviderMeta {
  name: string;
  displayName: string;
  website?: string;
  logoUrl?: string;
  apiBaseUrl?: string;                                // 新增: api-center 反查锚点
  channelType?: 'openai' | 'anthropic' | 'gemini';    // 新增: api-center 直接读
  priceCurrency?: 'USD' | 'CNY';                      // 新增: 从 model 上提
  priceUnit?: 'per_million_tokens';                   // 新增: 从 model 上提
}
```

### Task 1.2: 9 家 provider 填值

**Files:** `scripts/hub/aggregator.ts`(或各 provider 同步脚本里产 ProviderMeta 的位置)

填表(待跑前对一遍 baseUrl):

| provider   | apiBaseUrl                                              | channelType | priceCurrency |
|------------|---------------------------------------------------------|-------------|---------------|
| groq       | `https://api.groq.com/openai/v1`                        | openai      | USD           |
| cerebras   | `https://api.cerebras.ai/v1`                            | openai      | USD           |
| gitee      | `https://ai.gitee.com/v1`                               | openai      | CNY           |
| google     | `https://generativelanguage.googleapis.com/v1beta`      | gemini      | USD           |
| longcat    | `https://api.longcat.chat/openai/v1`                    | openai      | CNY           |
| nvidia     | `https://integrate.api.nvidia.com/v1`                   | openai      | USD           |
| openrouter | `https://openrouter.ai/api/v1`                          | openai      | USD           |
| xunfei     | `https://maas-api.cn-huabei-1.xf-yun.com/v1`            | openai      | CNY           |
| bigmodel   | `https://open.bigmodel.cn/api/paas/v4`                  | openai      | CNY           |

### Task 1.3: 删 model 上冗余字段

**Files:** `scripts/hub/aggregator.ts` / `enhancer.ts` 输出阶段

从最终 `EnhancedModelData`(写入 `data/views/all/models.json` 和各 provider models.json)删除:

- `object` (全局常量 "model")
- `model_id` (100% ≡ `id`)
- `owned_by` (100% ≡ `provider`)
- `price_currency` (上提到 ProviderMeta)
- `price_unit` (上提到 ProviderMeta)

### Task 1.4: 重生成 + 校验

```bash
cd /Users/zac/code/github/FreeModels
npm run sync          # 或对应的同步脚本
ls -la data/views/all/models.json
# 校验体积下降 ~9%(基线 725KB → 预期 ~660KB)
jq '.providerMeta.groq' data/models.json   # 看新字段写入
jq '.data[0]' data/models.json             # 看冗余字段已删
```

---

## Phase 2 — api-center 吃新 schema + 改反查

**仓库:** `/Users/zac/code/github/api-center`

### Task 2.1: 后端 registry 加 ProviderMeta 解析

**Files:** `internal/services/freemodels_registry.go`

- 删除 `ExtendedModelObject.ModelID` / `OwnedBy` 字段(本来就是冗余备用)
- `RawEnvelope` 解析时多吃一个 `providers: map[string]ProviderMeta`
- 新增类型:
  ```go
  type ProviderMetaInfo struct {
      Name         string `json:"name"`
      DisplayName  string `json:"displayName"`
      APIBaseUrl   string `json:"apiBaseUrl"`
      ChannelType  string `json:"channelType"`
      PriceCurrency string `json:"priceCurrency"`
      PriceUnit    string `json:"priceUnit"`
  }
  ```
- registry 内部存 `byHost map[string]string`(host → providerId)在 reload 时构建
- 新方法:`LookupProviderByHost(host string) (string, *ProviderMetaInfo, bool)`
- HTTP handler `/api/freemodels/registry` 返回结构里多带 `providerMeta`

### Task 2.2: 前端类型 + 适配器

**Files:** `web/src/api/freemodels.ts`

- `CachedEnvelope` 加 `providerMeta?: Record<string, ProviderMeta>` 字段
- 暴露:
  ```ts
  export function lookupProviderIdByHost(host: string): string | undefined
  export function getProviderApiBaseUrl(providerId: string): string | undefined
  ```

### Task 2.3: 反查从 registry 优先

**Files:** `web/src/data/freeProviders.ts:661-682`

`findProviderByUpstreamUrl` 改成两段:

1. 先 `lookupProviderIdByHost(host)` → 命中就用 registry 那边的 displayName / channelType,本地 entry 有就 merge,没有就合成最小 FreeProvider
2. 没命中再走原本地 `upstreamHosts[]` 反查
3. 都没命中返回 undefined

注意:**保留** `freeProviders.ts` 完整内容(FreeModels 没覆盖的 13 家全靠它)。`upstreamHosts[]` 字段不删,只是降级为 fallback。

### Task 2.4: 端到端校验

```bash
cd /Users/zac/code/github/api-center
go build ./...                                  # 后端编译
cd web && pnpm run build                        # 前端类型检查 + 构建
```

手动验证:
- 浏览器硬刷,看 OpenAI 系统聚合分组数 < 1018(之前的 fix 仍生效)
- 看 groq / openrouter / google 子分组的 `matchedProvider` 还在(`signupUrl` / `docsUrl` 链接还在)
- 在新建分组流程填 `https://api.cerebras.ai/v1`(本地 entry 已有)和 `https://api.groq.com/openai/v1` 都能识别

### Task 2.5: 提交

每个 phase 一个 commit,信息覆盖"为什么"——provider 关联事实键迁移到 FreeModels,长期减少手动同步工作量。

---

## 风险 / 回滚

- FreeModels schema 改动是 additive,字段删除影响读旧字段的消费方 → 你只有 api-center 一个消费方,可控
- api-center 这边保留本地 `freeProviders.ts` 完整,registry 失败时整个网关仍然可工作(fallback 到旧路径)
- 两 phase 各自独立,FreeModels 没发完前 api-center phase 2 不会出错(registry 没新字段就走 fallback,等同今天行为)

## 不在本次范围

- FreeModels 补 13 家国内 provider(siliconflow / llm7 / modelscope / kilo 等)— 数据补全工作,跟关联机制无关
- 删 `description` / `created` 等可疑字段 — 怀疑无用但跟主线无关
- `freeProviders.ts.upstreamHosts` 字段彻底退役 — 等 FreeModels 覆盖 100% 后再说
