# 推荐模型清单（Recommended Models）设计

> 「双轨推荐体系」的轨道一（人工写死、保证可用）。轨道二（真实使用数据投票展示）后续单独做。
> 纯前端功能，无后端 / DB 改动。

## 背景

用户担心从外部 registry 自动获取的模型「准不准、能不能用」。结论：免费性无法自动正向验证（只能 registry `isFree` 静态标记 + 402 负信号），可用性靠阶段 A/D 被动学习 + 按需实测。务实方案是**双轨**：
- **轨道一（本设计）**：一份人工写死、开发者验证过稳定可用的「推荐模型」清单，保证开箱有可用模型。
- 轨道二（后续）：从 RequestLog 聚合真实成功率/延迟，可视化「数据投票」排行。

现状：`web/src/data/freeProviders.ts` 的 `FREE_MODELS`（`FreeModel[]`，带 `providerId/modelId/tier`）已经是**手动维护的免费精选清单**——推荐标记直接加在它上面最自然。`bootstrapExposedModels(provider, testModel)`（同文件 1247）创建 group 时用它 + `provider.models` 判免费的，合成默认 `exposed_models`。

## 目标

1. 在 `FREE_MODELS` 标记一批「推荐」模型（开发者验证稳定可用）。
2. UI 给推荐模型加 ⭐徽标，Model Catalog 加「仅推荐」筛选。
3. 新建 group 时可优先暴露推荐模型（默认行为可控，不限制 available_models 全集）。

## 非目标（YAGNI）

- 轨道二「投票数据展示」（RequestLog 聚合 + 排行）—— 后续单独 spec。
- 自动推荐（成功率高自动升推荐）—— 后续。
- 后端 / DB 改动 —— 推荐是 `freeProviders.ts` 的前端静态元数据，不进 DB。
- auto 路由分档强制只用推荐 —— 本期只影响创建时的 exposed 默认值，不改路由逻辑。

## 设计

### 1. 数据：FreeModel 加 recommended 标记

`FreeModel` interface 加 `recommended?: boolean`。在 `FREE_MODELS` 给**开发者有把握、主流稳定**的模型标 `recommended: true`（如 `llama-3.3-70b`、`gemini-2.5-flash`、`gpt-oss-120b` 这类）。只标确有把握的，宁缺毋滥（推荐 = 保证可用的承诺）。

新增 helper：
```ts
// recommendedModelIds 返回推荐模型 id 集合；providerId 给定则只取该 provider 的。
export function recommendedModelIds(providerId?: string): Set<string>
// isRecommended(providerId, modelId) 单点判定（UI 徽标用）
export function isRecommended(providerId: string, modelId: string): boolean
```

### 2. UI：⭐徽标 + 筛选

- **Model Catalog**（`views/ModelCatalog.vue`）：推荐模型显示 ⭐ 徽标；新增「仅推荐」筛选 pill（与现有 free-only / tier filter 同款交互）。
- **创建流程 / 分组详情**（`V3NewGroupFlow.vue` / `V3GroupDetail.vue` 的模型列表）：推荐模型带 ⭐。
- 复用现有 badge 组件风格（`common/SpeedBadge.vue` 等），不新造设计语言。

### 3. 创建 group：默认优先推荐

`bootstrapExposedModels` 增强（不破坏现有签名，加可选参数）：
```ts
export function bootstrapExposedModels(
  provider: FreeProvider | undefined,
  selectedTestModel?: string,
  recommendedOnly?: boolean,   // 新增：true 时只返回推荐子集
): string[]
```
- `recommendedOnly` 为 true 且该 provider 有推荐模型 → 只返回推荐子集（+ selectedTestModel）。
- 否则保持现有逻辑（全部判免费的）。
- `available_models` **不变**（仍全集，能调全部）；只影响 `exposed_models` 默认值。
- 创建流程 UI 加一个开关「仅暴露推荐模型」（默认开 or 关，见下决策），开 → 传 `recommendedOnly=true`。用户随时可在分组配置里改 exposed。

> 默认开关取「默认开启（仅推荐）」：契合「开箱即用、保证可用」的目标；用户想要全部时一键关掉。

## 错误处理 / 边界

- provider 无任何推荐模型时，`recommendedOnly=true` 回退为现有全集逻辑（不返回空 exposed，避免 group 无可用模型）。
- registry 动态模型不在推荐清单里 → 不带 ⭐，正常显示（推荐是叠加标记，非过滤）。

## 测试

- 前端类型检查 `npm run type-check` 通过。
- `recommendedModelIds`/`isRecommended`/`bootstrapExposedModels(recommendedOnly)` 若项目有前端单测框架则补表驱动用例；无则人工核对 + 类型保证。
- 人工核对：FREE_MODELS 标记语法正确；ModelCatalog 筛选/徽标渲染；创建流程开关生效（exposed 随开关变化）。

## 验收标准

- `npm run build`（前端）无类型错误；`go build ./...` 不受影响（纯前端）。
- FREE_MODELS 里推荐模型带 ⭐，Model Catalog「仅推荐」筛选可用。
- 新建 group「仅暴露推荐模型」开关默认开 → exposed_models 为推荐子集；关 → 全集；available_models 始终全集。
- 无推荐模型的 provider 不会因开关变成空 exposed。

## 影响面

- 改 `web/src/data/freeProviders.ts`（FreeModel 加字段 + FREE_MODELS 标记 + helper + bootstrapExposedModels）、`web/src/views/ModelCatalog.vue`（徽标+筛选）、`web/src/components/v3/V3NewGroupFlow.vue`（开关+传参）、可能 `V3GroupDetail.vue`（徽标）。
- 无后端、无 DB、无 i18n（徽标文案少量，按现有 locale 模式加）。
