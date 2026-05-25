# FreeModels × api-center 统一架构 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 FreeModels 改造成严格 OpenAI `/v1/models` 兼容的数据源（id = raw API id, 复合主键去重），让 api-center 零归一化消费，并通过 `ofind.cn/FreeModels/v1/models` HTTP 端点对外服务。

**Architecture:** data plane (FreeModels) 出标准 schema → control plane (api-center) 拉缓存 + 转发 → consumer (LobeChat 等 OpenAI client) 直接消费。详见 [spec](../specs/2026-05-25-freemodels-api-center-unification-design.md)。

**Tech Stack:** Go 1.24 + Gin + GORM (api-center); TypeScript + Node (FreeModels); Cloudflare Worker / Pages route (P3 部署)

---

## P0: api-center 紧急 bug 修复

**Why first:** 不依赖任何外部改动，10 分钟搞定，立刻消除 363 个付费 model 污染 `free_models` 的问题。

### Task P0.1: ListModelIDsByProvider 加 freeOnly 过滤

**Files:**
- Modify: `internal/services/freemodels_registry.go` (函数 `ListModelIDsByProvider`, 当前 line 527-552)
- Modify: `internal/services/group_service.go` (函数 `computeFreeModelIDs`, 当前 line 483-536; 函数 `fallbackModelsFromRegistry`, 当前 line 542-569)

- [ ] **Step 1: 改 ListModelIDsByProvider 签名**

把 `freemodels_registry.go:527` 的方法签名改为带 `freeOnly bool` 参数：

```go
// ListModelIDsByProvider 返回该 provider 在 registry 中的所有 bare modelId.
// freeOnly=true 只返回 is_free=true 的子集 (用于 group.free_models 计算).
// freeOnly=false 返回该 provider 全部 (用于上游 /v1/models 404 兜底).
// providerId 大小写不敏感.
func (r *FreeModelsRegistry) ListModelIDsByProvider(providerID string, freeOnly bool) []string {
    if providerID == "" {
        return nil
    }
    target := strings.ToLower(providerID)
    r.mu.RLock()
    defer r.mu.RUnlock()
    out := make([]string, 0, 16)
    prefix := target + "/"
    for _, m := range r.envelope.Models {
        if strings.ToLower(m.Provider) != target {
            continue
        }
        if freeOnly && !m.IsFree {
            continue
        }
        id := m.ModelID
        if strings.HasPrefix(strings.ToLower(id), prefix) {
            id = id[len(prefix):]
        }
        if id != "" {
            out = append(out, id)
        }
    }
    return out
}
```

- [ ] **Step 2: 改 computeFreeModelIDs 调用方传 freeOnly=true**

`group_service.go:502`：`regList := registry.ListModelIDsByProvider(providerID)` → `regList := registry.ListModelIDsByProvider(providerID, true)`

- [ ] **Step 3: 改 fallbackModelsFromRegistry 传 freeOnly=false**

`group_service.go:564`：`ids := s.freeModelsRegistry.ListModelIDsByProvider(providerID)` → `ids := s.freeModelsRegistry.ListModelIDsByProvider(providerID, false)`

理由：fallback 场景是"上游 `/v1/models` 404 端点不存在"，我们要 best-effort 拿回该 provider 的所有 model（不只 free），后续 group.AvailableModels 才完整。

- [ ] **Step 4: 编译 + 触发一次 refresh 验证**

```bash
cd /Users/zac/code/github/api-center && go build ./... 2>&1 | head -20
```

期望：无编译错误。

通过管理 UI 或 API 触发 OpenRouter group 的 refresh-models：

```bash
# 假设 OpenRouter group id = 1 (按实际 id 替换)
curl -X POST http://localhost:8080/api/groups/1/refresh-models -H "Cookie: <admin session>"
```

检查日志输出：`computeFreeModelIDs: starting match` 行的 `registry_n` 应该比之前少（因为只算 free 的）。

- [ ] **Step 5: DB 验证 + commit**

```bash
sqlite3 data/api-center.db "SELECT name, json_array_length(free_models) FROM groups WHERE channel_type='openai' ORDER BY id;"
```

期望：OpenRouter 仍然是 24-25 个（恰好都是 free，所以不变），但其他 provider 如 NVIDIA / Gitee 数字应该明显下降（因为去除了付费污染）。

```bash
git add internal/services/freemodels_registry.go internal/services/group_service.go
git commit -m "🐛 fix(freemodels): ListModelIDsByProvider 加 freeOnly 过滤..."
```

---

## P1: FreeModels schema 改造

**Why second:** 是后续所有 phase 的前置。需要在 `/Users/zac/code/github/FreeModels` 仓库工作。

**执行前必读：** `scripts/hub/aggregator.ts`, `scripts/hub/enhancer.ts`, `scripts/hub/family.ts`, `scripts/hub/types.ts` —— 先理解当前 id 拼接逻辑（plugin 返回 raw modelId，何处加 `<provider>/` 前缀），再动手。

### Task P1.1: types.ts 把 OpenAI 标准字段加回来

**Files:**
- Modify: `scripts/hub/types.ts` (interface `OpenAIModelObject`, `ExtendedModelObject`)
- Modify: `scripts/hub/aggregator.ts` (函数 `toOpenAICompatible` 或等价处)

**内容：**
- `OpenAIModelObject` 加 `object: "model"` 和 `owned_by: string` 字段
- `ExtendedModelObject.id` 注释明确："raw API id, 即 POST body.model 直接填的字符串"
- `toOpenAICompatible` 在每条 model 上输出 `object: "model"` + `owned_by: <provider>`

**验收：**
- `npm run build && cat data/models.json | jq '.data[0] | {id, object, owned_by, created}'` 输出每个字段都有值

### Task P1.2: 14 个 provider 插件统一 id 风格（按对照表）

**Files:**
- 11 个需要剥前缀的 plugin: `scripts/hub/providers/{bigmodel,cerebras,cloudflare,cohere,github,google,groq,longcat,sambanova,xingchen,xinghuo}/index.ts`
- 3 个已经 raw 的 plugin (不动): `gitee`, `nvidia`, `openrouter`

**规则（必须严格按 spec 对照表执行）：**
- 剥 `<freemodels-provider-id>/` 前缀（如 `bigmodel/glm-4-flash-250414` → `glm-4-flash-250414`）
- **不剥** owner 前缀（OpenRouter 的 `qwen/`、NVIDIA 的 `bytedance/`、Groq 的 `openai/` 保留）
- **保留** OpenRouter `:free` 后缀、Cloudflare `@cf/` 前缀

**做法（推荐方式）：**
- 选项 A: 修改每个 plugin 的 `modelId` 生成逻辑，从源头出 raw id
- 选项 B: 在 aggregator/enhancer 里统一处理（依赖 `family.ts` 已有的 `stripProviderPrefix` 函数）

建议走选项 B（改动集中，回滚容易），但要审 `stripProviderPrefix` 现有规则是否完全匹配 spec 对照表，不匹配的 case 加 override。

**验收：**
- `cat data/models.json | jq '.data[] | select(.owned_by=="bigmodel") | .id' | head -5` 输出无 `bigmodel/` 前缀
- `cat data/models.json | jq '.data[] | select(.owned_by=="openrouter") | .id' | head -5` 输出含 `:free` 后缀
- `cat data/models.json | jq '.data[] | select(.owned_by=="cloudflare") | .id' | head -5` 输出含 `@cf/` 前缀

### Task P1.3: aggregator 去重键改为复合主键

**Files:**
- Modify: `scripts/hub/aggregator.ts` (去重逻辑, 当前 line 138 附近"Deduplicated ... conflicting record(s)")

**改动：** 把所有 `Map<modelId, X>` 改成 `Map<provider+":"+modelId, X>`，允许跨 provider 同名 model 共存。

**验收：**
- 改造前 `wc -l data/models.json` vs 改造后行数应该 **持平或略增**（不应该减少；减少意味着合法的跨 provider model 被错误去重了）
- `cat data/models.json | jq '.data | length'` 数量应该接近 673（当前总数）

### Task P1.4: family.ts 跨 provider 别名识别按复合键

**Files:**
- Modify: `scripts/hub/family.ts` (任何按 id 做 family 识别的逻辑)

**改动：** family 识别本来就跨 provider，但内部数据结构如果有 `Map<id, family>` 这种假设 id 唯一的代码，改为 `Map<provider+id, family>` 或显式按 `(provider, id)` 元组索引。

**验收：**
- `cat data/models.json | jq '.data | group_by(.model_family) | map({family: .[0].model_family, n: length}) | sort_by(-.n) | .[:10]'` 跨 provider 家族（如 `qwen3-coder`、`llama-3-8b`）应该有多条来自不同 provider 的成员

### Task P1.5: 验证 + docs 更新 + commit

- [ ] 跑 `npm run build && npm run generate-docs`，确认 README 自动更新表格无回归
- [ ] 跑 `npm test`（如果有测试套件），全绿
- [ ] README 加一段："任一 JSON drop-in 当 OpenAI /v1/models 用"，含 LobeChat / NextChat 配置示例
- [ ] commit 到 FreeModels main：`✨ feat(schema): 严格 OpenAI /v1/models 兼容 + 复合主键去重 (breaking)`

---

## P2: api-center 切到新 schema

**Why third:** 等 P1 改完 FreeModels CDN 部署完成后再做。

**执行前必读：** 需要清理 working tree 里的 11 个 P1+P2 改动（之前对话产物）。**做法：** 先 `git stash` 暂存，新 schema 实现完后对比改动重新整合（不直接复用旧改动，因为 schema 变了）。

### Task P2.1: parseUpstream 适配新 schema

**Files:**
- Modify: `internal/services/freemodels_registry.go` (struct `upstreamModel`, 函数 `parseUpstream`)

**改动：**
- `upstreamModel` 加 `Object string \`json:"object"\`` 和 `OwnedBy string \`json:"owned_by"\`` 字段
- `parseUpstream` 现在 `id` 已经是 raw API id，不需要任何归一化
- `replaceIndex` 删除 "剥前缀重复索引一次" 那段逻辑（line 380-391）—— 新 schema 下 id 就是 raw，没有前缀可剥

### Task P2.2: computeFreeModelIDs 简化为直接 string 求交集

**Files:**
- Modify: `internal/services/group_service.go` (函数 `computeFreeModelIDs`, 当前 line 483-536)

**改动：** 整个函数应该简化到 ~10 行以内：

```go
func computeFreeModelIDs(group *models.Group, upstreamIDs []string, registry *FreeModelsRegistry) []string {
    if registry == nil || len(upstreamIDs) == 0 {
        return []string{}
    }
    providerID := resolveProviderID(group, registry)
    freeSet := registry.FreeIDSetByProvider(providerID) // 新增 helper, 返回 map[string]struct{}
    out := make([]string, 0, len(upstreamIDs))
    for _, id := range upstreamIDs {
        if _, ok := freeSet[id]; ok {
            out = append(out, id)
        }
    }
    return out
}
```

- 删除所有 `strings.ToLower` 归一化（新 schema 大小写跟上游严格一致）
- 删除所有 sample log 调试代码

### Task P2.3: 前端清理

**Files:**
- Modify: `web/src/api/freemodels.ts`
- Modify: `web/src/data/freeProviders.ts`
- Modify: `web/src/components/v3/V3GroupDetail.vue`

**改动：**
- 删除所有本地 `isFree()` 算法 / `:free` 启发式 / pricing=0 推断
- 直接读 `group.free_models` 数组判断 free 标签
- 删除 `window.__orDebug` 全局调试代码

### Task P2.4: 集成测试 + commit

- [ ] `go build ./... && go vet ./...` 全绿
- [ ] 重启 api-center，触发 OpenRouter / Gitee / NVIDIA 三个 provider 的 refresh-models
- [ ] 检查 DB `free_models` 数量符合预期（OpenRouter 应该是 25, Gitee 应该是 39 等，跟 FreeModels README 表格一致）
- [ ] commit 到 api-center main：`♻️ refactor(freemodels): 切到新 schema, 删除所有归一化 hack`

---

## P3: FreeModels CDN /v1/models HTTP 端点

**Why last:** 让第三方零代码可接入是战略升级，等 P1/P2 跑通再做。

**执行前确认：** 跟用户确认 `ofind.cn/FreeModels/` 的部署方式（Cloudflare Pages / GitHub Pages / Vercel / 自建 nginx）。

### Task P3.1: 加路由 /FreeModels/v1/models → data/models.json

具体改法看部署方式：

**Cloudflare Pages**: `_redirects` 文件加 `/FreeModels/v1/models /FreeModels/data/models.json 200`

**GitHub Pages**: 用 `_config.yml` redirects 或在 `data/` 下软链一份

**Cloudflare Worker**: 写一个 worker 在请求路径匹配时 fetch `data/models.json` 转发

加 OpenAI 兼容响应头：
- `Content-Type: application/json`
- `Access-Control-Allow-Origin: *` (CORS, 让浏览器 client 能直接 fetch)
- `Cache-Control: public, max-age=3600` (1h 缓存)

### Task P3.2: 验证 OpenAI client drop-in

- [ ] 用 curl 验证 `curl https://ofind.cn/FreeModels/v1/models | jq '.object'` 输出 `"list"`
- [ ] 在 LobeChat 或 NextChat 配置一个新 model provider，BaseURL = `https://ofind.cn/FreeModels/v1`，验证 model 选择器能列出 673 个 model
- [ ] README 加一节"OpenAI Client 直接接入"，提供 LobeChat / NextChat / OneAPI 配置截图

### Task P3.3: commit + 公告

- [ ] commit 到 FreeModels main：`✨ feat(api): 加 /FreeModels/v1/models OpenAI 兼容 HTTP 端点`

---

## 不在范围

- **P4 智能路由**: 单独 sub-project，需要独立 brainstorming + spec。本 plan 不覆盖
- **删除旧字段**: 任何 deprecated 字段保留，加字段不删字段
- **v1/v2 并存方案**: 没有外部用户，直接 breaking
- **商业化**: 不考虑
