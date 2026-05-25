# P4 智能路由 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 api-center aggregate 模式真正实现"用户填一个 model 名 → 跨 provider 自动路由"。基于 [P4 spec](../specs/2026-05-25-p4-smart-routing-design.md)。

**Architecture:** proxy 入口加解析层 (alias > family > raw 三层链) → 候选池 → 现有 SWRR + fallback (不动). FreeModels family 数据需要先清理 (P4.0).

**Tech Stack:** Go + Gin (api-center backend), Vue 3 + TS (UI), TypeScript (FreeModels)

---

## P4.0: FreeModels canonicalizeFamily 清理

### Task P4.0.1: Family.ts 修复 size 丢失与 license 噪音

**Files:**
- Modify: `scripts/hub/family.ts` (函数 `canonicalizeFamily` / `deriveFamilyFromSlug`)

**Root cause 分析:**
- `canonicalizeFamily` line 205 用 `>=` 让 nameResult 平手抢赢 idResult
- cerebras `gpt-oss-120b` 的 `name="OpenAI GPT OSS"` 缺 size, 但 score 跟 `gpt-oss-120b` 平手 → 丢 120b
- OpenRouter 的 name `"OpenAI: GPT-OSS 120B (free)"` 含 license 噪音 `(free)` 没被剥
- Cloudflare modelId `@cf/openai/gpt-oss-120b` 的 `@cf/` 不在 PROVIDER_PREFIXES, 被替换成 `-` 留在 family 里

**改动 (4 处):**

- [ ] **Step 1: family score 加 hasSize 维度, idResult 含 size 时永远优先**

修改 `familyScore` (line 123-129):

```typescript
function familyScore(family: string): number {
  let score = 0;
  if (/\d+\.\d+/.test(family)) score += 3;   // 版本号带点
  if (/-\d+b\b/i.test(family)) score += 5;    // ★ 含 size suffix (-120b, -8b 等)
  if (/-/.test(family)) score += 1;
  if (/^[a-z0-9]+$/.test(family)) score -= 2;
  return score;
}
```

含 size 加 +5 让任何带 size 的 idResult 永远赢过 nameResult。

- [ ] **Step 2: 把 `(free)` / `(beta)` / `(preview)` license 噪音剥掉**

`deriveFamilyFromSlug` 在 stripProviderPrefix 之前加:

```typescript
// 剥 license / channel 标记 (放 in_free / free_mechanism 字段)
id = id.replace(/[\s\-_]?\((free|beta|preview|trial)\)/gi, '');
id = id.replace(/:(free|beta|preview)\b/gi, '');
```

- [ ] **Step 3: 把 `@cf/` 视为 Cloudflare path namespace, 剥掉**

修改 `stripProviderPrefix` (line 80-88):

```typescript
function stripProviderPrefix(id: string): string {
  // Cloudflare 特殊: @cf/ 是其 Workers AI 的 path namespace, 不是 owner
  id = id.replace(/^@cf\//i, '');
  for (const prefix of PROVIDER_PREFIXES) {
    const re = new RegExp(`^${prefix}/`, 'i');
    if (re.test(id)) {
      return id.replace(re, '');
    }
  }
  return id;
}
```

- [ ] **Step 4: 改 `>=` 为 `>`，让平手时 idResult 赢**

`canonicalizeFamily` line 205: `>=` → `>`

(配合 Step 1 的 size 加分，绝大多数 case 是 idResult 赢)

- [ ] **Step 5: 跑 sync + 抽检验收**

```bash
cd /Users/zac/code/github/FreeModels
npm run typecheck && npm run sync-models -- --no-notify
```

抽检 4 个 family:

```bash
jq '[.data[] | select(.model_family == "gpt-oss-120b")] | length' data/models.json
# 期望 ≥ 6 (cerebras + cloudflare + gitee + groq + nvidia + sambanova; openrouter 可能仍 :free 后缀, 如有则 7)

jq '[.data[] | select(.model_family == "gpt-oss-20b")] | length' data/models.json
# 期望 = 4 (cloudflare + groq + nvidia + openrouter)

jq '[.data[] | select(.model_family == "llama-3.3-70b-instruct" or .model_family == "llama-3.3-70b")] | group_by(.model_family) | map({f: .[0].model_family, n: length})' data/models.json
# 期望 cross-provider 聚合: 4+ 个候选共享 family
```

- [ ] **Step 6: 必要时加 family-overrides.json**

如果 Step 5 抽检发现某些 case Step 1-4 自动规则覆盖不到，在 `scripts/hub/family-overrides.json` 加 manual override。优先用 `patterns` (regex) 而非 `exact` (具体 id)，覆盖面更大。

- [ ] **Step 7: 生成 docs + commit + push**

```bash
npm run generate-docs
git add scripts/hub/family.ts scripts/hub/family-overrides.json data/ docs/
git commit -m "🐛 fix(family): 修 size 丢失 / license 噪音 / @cf 命名空间..."
git pull --rebase && git push origin master
```

等 sync-pages CI 跑完 (~3 分钟) + Fastly TTL (~10 分钟), CDN 生效。

---

## P4.1: api-center 入口解析层

**前置:** P4.0 部署完成, api-center 重启拉到新 family 数据.

### Task P4.1.1: FreeModelsRegistry 加 ListByFamily

**Files:**
- Modify: `internal/services/freemodels_registry.go` (struct `FreeModelsRegistry` 加 `byFamily` 索引, 加 `ListByFamily` 方法)

**改动:**

- [ ] **Step 1: 加 byFamily 索引字段**

`FreeModelsRegistry` struct 加:
```go
byFamily map[string][]*FreeModelMeta // family name → 候选 model 列表
```

- [ ] **Step 2: replaceIndex 构建 byFamily**

```go
byFamily := make(map[string][]*FreeModelMeta)
for i := range env.Models {
    m := &env.Models[i]
    if m.ModelFamily != "" {
        byFamily[strings.ToLower(m.ModelFamily)] = append(byFamily[strings.ToLower(m.ModelFamily)], m)
    }
}
```

(假设 `FreeModelMeta` 已有 `ModelFamily` 字段, 没有的话先加, json tag `model_family`)

- [ ] **Step 3: 加 ListByFamily 方法**

```go
// ListByFamily 返回该 family 下所有 (provider, raw_id) 候选.
// family 名大小写不敏感. 返回空切片表示无候选, 不返回 nil.
func (r *FreeModelsRegistry) ListByFamily(family string) []FreeModelMeta {
    if family == "" {
        return nil
    }
    r.mu.RLock()
    defer r.mu.RUnlock()
    metas := r.byFamily[strings.ToLower(family)]
    out := make([]FreeModelMeta, 0, len(metas))
    for _, m := range metas {
        out = append(out, *m)
    }
    return out
}
```

### Task P4.1.2: alias_service 暴露 LookupAlias 给 proxy 用

**Files:**
- Modify: `internal/services/alias_service.go`

**改动:**

- [ ] **Step 1: 加 LookupAlias 方法**

```go
type AliasCandidate struct {
    GroupID    uint
    GroupName  string
    RealModel  string
    Weight     int
    Priority   int
}

// LookupAlias 返回 alias 表中该名字下的所有 enabled candidates.
// 调用方负责按 (priority, weight) 排序选择.
func (s *AliasService) LookupAlias(ctx context.Context, aliasName string) ([]AliasCandidate, error) {
    // 查 model_aliases WHERE alias = ? AND enabled = true
    // JOIN groups ON groups.id = group_id 拿 group name
}
```

### Task P4.1.3: proxy 入口加解析层

**Files:**
- Modify: `internal/proxy/server.go` (在 ChatCompletions handler 入口最前面)

**改动:**

- [ ] **Step 1: 实现 resolveModelCandidates**

```go
// 在 server.go 加方法
func (s *Server) resolveModelCandidates(ctx context.Context, group *models.Group, userModel string) ([]ModelCandidate, error) {
    // 只对 aggregate group 启用智能解析 (standard group 直接用 userModel)
    if group.GroupType != "aggregate" {
        return nil, nil
    }
    // 1. Alias 表 (人工声明, 最高优先级)
    if cands, err := s.aliasSvc.LookupAlias(ctx, userModel); err == nil && len(cands) > 0 {
        return aliasToModelCandidates(cands), nil
    }
    // 2. FreeModels Family (自动识别)
    if metas := s.freeReg.ListByFamily(userModel); len(metas) > 0 {
        return familyToCandidates(group, metas, s.subGroupMgr), nil
    }
    // 3. nil 表示走 fallback (现有 raw id 字符串匹配)
    return nil, nil
}
```

`familyToCandidates`: 把 family metadata 映射到本 aggregate 下实际存在的 sub-group:
- 对每个 `meta`, 查 aggregate 的 sub-groups, 找 `channel_type` 或 host 匹配 `meta.Provider` 的 sub-group
- 输出 `[(SubGroupID, meta.ModelID)]`

- [ ] **Step 2: 在 ChatCompletions handler 插入解析层调用**

```go
// 收到请求后, 先解析:
candidates, _ := s.resolveModelCandidates(ctx, group, requestedModel)
if len(candidates) > 0 {
    // 走候选池模式 (新)
    return s.executeWithCandidates(ctx, group, candidates, body, c)
}
// 否则走现有 subgroup_manager.SelectSubGroupForModel (老路径)
```

### Task P4.1.4: 转发时改写 body.model

**Files:**
- Modify: `internal/proxy/server.go` (`executeRequestWithRetry` 或转发体内)

**改动:**

- [ ] **Step 1: 复用 ModelRedirect 改写逻辑**

候选选中后, 在构建 upstream request body 时把 `body.model` 设为 `candidate.RealModel`. 这跟现有 `ModelRedirectMap` 行为本质相同, 复用同一段改写代码.

- [ ] **Step 2: 验证: stream 也能正确改写**

streaming response 不受影响 (只改 request body), 但要确认 stream first chunk 之前的 retry 也能换 candidate.

### Task P4.1.5: 集成测试 + commit

- [ ] go build, go vet 全绿
- [ ] 启动 api-center, 在某个 aggregate group (包含 Groq + NVIDIA + SambaNova) 上调用 `POST /v1/chat/completions {"model": "llama-3.3-70b", ...}`. 期望日志显示候选池 ≥ 3, 转发时 body.model 被改写
- [ ] 模拟 Groq 限速 (mock 或临时改 weight 为 0): 应自动 fallback 到 NVIDIA, body.model 改成 NVIDIA 的 raw id
- [ ] commit + push

---

## P4.2: family → alias 自动建议

### Task P4.2.1: alias_suggestion_service 加 family 数据源

**Files:**
- Modify: `internal/services/alias_suggestion_service.go`
- Modify: `internal/handler/alias_suggestion_handler.go`

**改动:**

- [ ] **Step 1: 新增 FamilySuggestion 类型 + 生成器**

```go
type FamilySuggestion struct {
    FamilyName     string           `json:"family_name"`
    Candidates     []FamilyCandidate `json:"candidates"`
    ConfidenceTier string           `json:"confidence_tier"` // high/medium/low
    ProposedAlias  string           `json:"proposed_alias"`
}

func (s *AliasSuggestionService) GenerateFamilySuggestions(ctx context.Context, aggregateGroupID uint) ([]FamilySuggestion, error) {
    // 1. 列出 aggregate 下所有 sub-group 的 available_models
    // 2. 对每个 model, 查 FreeModelsRegistry 的 family
    // 3. 按 family 聚合, 过滤掉 candidates < 2 的 (单候选无意义)
    // 4. 过滤掉已经有 alias 的 family (避免重复建议)
    // 5. ConfidenceTier: candidates >= 4 → high, >= 2 → medium
}
```

- [ ] **Step 2: handler 暴露端点**

`GET /api/groups/:id/alias-suggestions` 返回 `[]FamilySuggestion`.

- [ ] **Step 3: 复用现有"采纳"接口**

admin 采纳时调用现有的 `POST /api/aliases` 批量创建. 不需要新增写接口.

---

## P4.3: UI 一键采纳建议

### Task P4.3.1: AliasManageTab 加建议区块

**Files:**
- Modify: `web/src/components/aliases/AliasManageTab.vue`
- Modify: `web/src/api/aliases.ts`

- [ ] **Step 1: api.ts 加 fetchSuggestions**

```ts
export async function fetchFamilySuggestions(groupId: number): Promise<FamilySuggestion[]> {
    const res = await http.get(`/api/groups/${groupId}/alias-suggestions`);
    return res.data;
}
```

- [ ] **Step 2: AliasManageTab 顶部加折叠面板**

UI: "系统检测到 N 个未配 alias 的 family ▼", 展开后每行:
- family name + 候选 provider 数 + confidence chip
- "采纳" 按钮 (批量创建 alias 行) + "忽略" 按钮 (本地记 dismiss)

- [ ] **Step 3: 采纳后刷新 alias 列表**

调 batch create endpoint, 成功后刷新当前 alias 表 + suggestions 列表.

### Task P4.3.2: 集成测试 + commit

- [ ] 前端 typecheck 全绿
- [ ] 浏览器手动测: aggregate group 有跨 provider model → suggestions 出现 → 采纳 → alias 表多出新行 → 路由立刻按新 alias 走
- [ ] commit + push

---

## 验收标准 (整个 P4)

1. ✅ 用户在 LobeChat 配 BaseURL = api-center, 填 `model: "llama-3.3-70b"` → 实际从 Groq/NVIDIA/SambaNova/Cerebras 中智能选一家转发, 失败自动 fallback
2. ✅ admin 在 UI 上看到 "系统建议把 llama-3.3-70b 配为 alias", 一键采纳后 alias 表多 4 行
3. ✅ 现有 model_redirect_map 配置不受影响, 仍按显式映射工作 (优先级 alias > family > raw)
4. ✅ FreeModels `gpt-oss-120b` family 至少 6 个跨 provider 候选

## 不在范围

- 429 vs 5xx 区分 (留 P5)
- 延迟感知动态权重 (留 P5)
- 主动健康探针 (留 P5)
- streaming 中途失败 retry (永远不做)
