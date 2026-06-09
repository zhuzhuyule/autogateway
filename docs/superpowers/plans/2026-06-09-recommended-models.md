# 推荐模型清单 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`).

**Goal:** 在 `FREE_MODELS` 标记一批开发者验证稳定的「推荐」模型，UI 给推荐模型加 ⭐徽标，Model Catalog 加「仅推荐」筛选。纯展示，不动任何创建 group 逻辑，零回归。

**Architecture:** `FreeModel` 加 `recommended?: boolean` + `FREE_MODELS` 标记 + `isRecommended`/`recommendedModelIds` helper（Task 1）→ ModelCatalog 徽标+筛选 + 创建/详情列表 ⭐展示（Task 2）。纯前端，无后端/DB。

**Tech Stack:** Vue 3 + TS。

**Spec:** `docs/superpowers/specs/2026-06-09-recommended-models-design.md`

---

## File Structure

- **Modify** `web/src/data/freeProviders.ts` — `FreeModel` 加字段 + `FREE_MODELS` 标记 + 两个 helper。
- **Modify** `web/src/views/ModelCatalog.vue` — ⭐徽标 + 「仅推荐」筛选 pill。
- **Modify** `web/src/components/v3/V3NewGroupFlow.vue` / `V3GroupDetail.vue` — 模型列表 ⭐展示（按现有列表渲染处）。

---

## Task 1: 数据标记 + helper

**Files:** Modify `web/src/data/freeProviders.ts`

- [ ] **Step 1: 读现状**

Read `web/src/data/freeProviders.ts`：`FreeModel` interface 定义、`FREE_MODELS` 数组（条目结构 `{providerId, modelId, tier, ...}`）、`isFree` helper 的位置/风格（新 helper 照它放）。

- [ ] **Step 2: FreeModel 加字段**

`FreeModel` interface 加：
```ts
  /** 开发者验证过稳定可用, 在 UI 标 ⭐推荐; 仅标确有把握的 */
  recommended?: boolean;
```

- [ ] **Step 3: FREE_MODELS 标记推荐**

给 `FREE_MODELS` 里**主流、稳定、开发者有把握**的模型条目加 `recommended: true`。挑选原则：各 provider 的旗舰/稳定主力款，宁缺毋滥。建议标记（若条目存在）：`llama-3.3-70b`(groq/cerebras)、`gemini-2.5-flash`/`gemini-2.5-flash-lite`(google)、`gpt-oss-120b`、`qwen-2.5-72b`/`qwen3-32b` 类、`command-r-plus`(cohere)、`deepseek-r1`/`deepseek-v3`(openrouter free) —— 以 FREE_MODELS 里**实际存在的条目**为准，不存在的不要新增条目，只给已有的加标记。不确定是否稳定的不标。

- [ ] **Step 4: 加 helper**

在 `isFree` 附近加：
```ts
// recommendedModelIds 返回推荐模型 id 集合; 给定 providerId 则只取该 provider 的。
export function recommendedModelIds(providerId?: string): Set<string> {
  const s = new Set<string>();
  for (const m of FREE_MODELS) {
    if (m.recommended && (!providerId || m.providerId === providerId)) {
      s.add(m.modelId);
    }
  }
  return s;
}

// isRecommended 单点判定 (UI 徽标用)。
export function isRecommended(providerId: string, modelId: string): boolean {
  return FREE_MODELS.some(
    (m) => m.recommended && m.providerId === providerId && m.modelId === modelId,
  );
}
```

- [ ] **Step 5: 验证**

Run: `cd web && npm run type-check`（或 `npm run build`）→ 无类型错误。人工核对 FREE_MODELS 标记语法（逗号/字段）。

- [ ] **Step 6: Commit**

```bash
git add web/src/data/freeProviders.ts
git commit -m "✨ feat(web): FreeModel recommended 标记 + FREE_MODELS 精选 + helper"
```

---

## Task 2: UI ⭐徽标 + 仅推荐筛选

**Files:** Modify `web/src/views/ModelCatalog.vue`、`web/src/components/v3/V3NewGroupFlow.vue`、`web/src/components/v3/V3GroupDetail.vue`

- [ ] **Step 1: 读现状**

Read `web/src/views/ModelCatalog.vue`：看现有筛选 pill（free-only / tier filter）怎么实现、模型行/卡片怎么渲染徽标（free 徽标等）、数据怎么拿到 providerId+modelId。Read `V3NewGroupFlow.vue`/`V3GroupDetail.vue` 模型列表渲染处。

- [ ] **Step 2: ModelCatalog ⭐徽标**

在模型行/卡片渲染处，用 `isRecommended(providerId, modelId)` 判定，为 true 的加 ⭐ 徽标（复用现有徽标样式，如 free 徽标旁边）。import `isRecommended` from freeProviders。

- [ ] **Step 3: ModelCatalog「仅推荐」筛选**

照现有 free-only pill 的实现，加一个「仅推荐」筛选状态 + pill；开启时过滤出 `isRecommended` 为 true 的模型。筛选为空时显示正常空态。

- [ ] **Step 4: 创建/详情列表 ⭐展示**

在 `V3NewGroupFlow.vue` 和 `V3GroupDetail.vue` 的模型列表渲染处，同样用 `isRecommended` 给推荐模型加 ⭐（纯展示，不改任何选择/exposed 逻辑）。若这两处模型列表无现成 providerId 上下文，按可得信息判定（providerId 通常是当前 group 的 provider）；拿不到 providerId 时可降级为不显示 ⭐（不报错）。

- [ ] **Step 5: 验证**

Run: `cd web && npm run type-check && npm run build` → 无错误。人工核对：Catalog ⭐徽标 + 仅推荐筛选渲染正常；创建/详情列表 ⭐ 显示；**确认未改动任何 exposed_models/available_models/创建提交逻辑**（git diff 检查 V3NewGroupFlow 的 ensureGroupExists / bootstrapExposedModels 调用未变）。

- [ ] **Step 6: Commit**

```bash
git add web/src/views/ModelCatalog.vue web/src/components/v3/V3NewGroupFlow.vue web/src/components/v3/V3GroupDetail.vue
git commit -m "✨ feat(web): Model Catalog ⭐推荐徽标 + 仅推荐筛选 + 列表展示"
```

---

## Task 3: 收尾

- [ ] **Step 1** `cd web && npm run build` + `go build ./...`（确认后端不受影响）全绿。
- [ ] **Step 2** 对照 spec 验收：⭐徽标 + 仅推荐筛选可用；创建逻辑零回归（diff 确认 bootstrapExposedModels/ensureGroupExists 未动）。
- [ ] **Step 3** 合并：finishing-a-development-branch（分支 `feat/recommended-models`）。

---

## Self-Review 记录

- **Spec coverage**：recommended 标记+helper→Task1；⭐徽标+仅推荐筛选+列表展示→Task2；不动创建逻辑（spec 范围限定）→Task2 Step5 显式 diff 确认。✅
- **Placeholder scan**：helper 代码完整；FREE_MODELS 具体标哪些条目让实现者按"已存在+稳定"现场判定（数据决策，给了原则）；ModelCatalog 徽标/筛选按现有 free-only 模式对齐（行号/结构现场 Read）。
- **Type consistency**：`recommended?: boolean`、`recommendedModelIds(providerId?)→Set<string>`、`isRecommended(providerId, modelId)→boolean` 各 Task 一致。✅
- **零回归**：纯展示，Task2 Step5 显式要求 diff 确认创建逻辑未动。✅
