# P4: 跨 Provider 智能路由设计

> **状态**: Draft, user 全权授权推进  
> **作者**: zac + Claude  
> **日期**: 2026-05-25  
> **影响仓库**: `github.com/zhuzhuyule/FreeModels`, `github.com/zhuzhuyule/autogateway`  
> **前置 spec**: [2026-05-25 FreeModels × api-center 统一架构](./2026-05-25-freemodels-api-center-unification-design.md)（P0-P3 已完成）

## Goal

把 FreeModels 的 `model_family` 字段引入 api-center aggregate 路由决策，实现"用户填一个 model 名（如 `llama-3.3-70b`），api-center 自动从所有支持该模型的 provider 中选最优转发，失败 fallback 下一个"——OpenRouter 的核心能力，但全部接免费源。

## 背景与现状

### Aggregate 模式已经实现的（不需要重做）

代码梳理（详见 `internal/services/subgroup_manager.go` + `internal/proxy/server.go`）：

- ✅ 自动发现候选 sub-group（按 `available_models` ∪ `exposed_models`）
- ✅ SWRR 加权轮询（权重来源 `GroupSubGroup.Weight`）
- ✅ 跨 sub-group failover（`server.go:291-327`），重试计数归零
- ✅ 熔断器：3 次连续失败 → 30 秒冷却
- ✅ 活跃 key 感知

### 关键缺口（这是 P4 要补的）

**raw API id 不同的同一个模型，aggregate 不会自动合并。**

举例：用户填 `llama-3.3-70b`，但实际各 provider 的 raw id 是：

| Provider | 真实 raw API id |
|---|---|
| Groq | `llama-3.3-70b-versatile` |
| NVIDIA | `meta/llama-3.3-70b-instruct` |
| SambaNova | `Meta-Llama-3.3-70B-Instruct` |
| Cerebras | `llama-3.3-70b` |

今天 aggregate 路由字符串匹配 `llama-3.3-70b` → **只命中 Cerebras 一家**。跨 provider 能力名存实亡。

### 用户原本通过 alias 系统人工解决

`internal/models/types.go:159` 的 `ModelAlias` 表结构 `(alias, group_id, real_model)` 三元组，**同一 alias 下多行就是天然的跨 sub-group 候选池**。但需要 admin 手工配 alias，覆盖面有限。

### P4 的核心思路

把 FreeModels 自动识别的 `model_family` 作为 **alias 系统的自动补充**，三层优先级链：

| 优先级 | 来源 | 谁创建 |
|---|---|---|
| 1️⃣ Alias 表 | 人工声明 | admin UI 配 |
| 2️⃣ FreeModels Family | 自动识别 | FreeModels 同步 |
| 3️⃣ Raw ID 字符串匹配 | 现有逻辑 | 上游 `/v1/models` |

冲突场景被 alias 优先解决：用户填的名字如果同时是 alias 又是某 provider 的 raw id，走 alias 路径（管理员意图最强）。

## 三条契约

### 契约一：解析层在 proxy 入口，subgroup_manager 不动

新增的"family/alias 解析层"放在 `proxy/server.go` 收到请求后的最前面。把用户 `body.model` 解析成 `[(sub_group_id, real_raw_id), ...]` 候选池，剩下交给现有 `subgroup_manager.SelectSubGroupForModel`。

好处：现有 SWRR / fallback / 熔断 一行不改。

### 契约二：转发时改写 body.model 为 real_raw_id

候选 `(sub_group, real_raw_id)` 被选中后，转发到上游时必须把 `body.model` 改成 `real_raw_id`（OpenRouter 不会因为我们填 `llama-3.3-70b` 而帮你猜成 `llama-3.3-70b-versatile`，必须填准）。复用现有 `ModelRedirect` 逻辑。

### 契约三：Family 命名 = 架构 + 规模 + 变体，不含 provider / license / 量化

Family 是"用户可互换使用而不感知质量差"的等价类。粒度判断：

| 包含 | 排除 |
|---|---|
| ✅ 架构名 (`gpt-oss`, `llama-3`, `qwen3`) | ❌ provider 前缀 (`@cf-openai-`, `openai:-`) |
| ✅ 参数规模 (`120b`, `20b`, `70b`) | ❌ license 后缀 (`(free)`, `:free`) |
| ✅ 关键功能变体 (`safeguard`, `instruct`, `vision`) | ❌ 量化级别 (`fp8`, `int4`) → 放 `quantization` 字段 |
| ✅ 主版本号 (`llama-3.3`) | ❌ 部署区域 / 价格层 |

**判断标准**：两个 model 同 family ⟺ 它们能力等价，用户互换不感知质量差。
- ✅ Groq 的 `gpt-oss-120b` 和 NVIDIA 的 `gpt-oss-120b` → 同 family
- ❌ Cerebras 的 `gpt-oss-120b` 和 Groq 的 `gpt-oss-20b` → 不同 family（规模差 6 倍）
- ❌ `gpt-oss-20b` 和 `gpt-oss-safeguard-20b` → 不同 family（safeguard 是分类器不是 chat）

## 4 个 Sub-Phase

### P4.0: FreeModels canonicalizeFamily 清理（前置必做）

**Why first:** family 命名不准的话 P4.1 路由准确度 = 0。

**Files:**
- Modify: `scripts/hub/family.ts` (函数 `canonicalizeFamily`, 244 行)
- Modify: `scripts/hub/family-overrides.json` (按需加 manual override)

**当前问题（gpt-oss 系列 12 条数据，4 条错归）：**
- Cerebras `gpt-oss-120b` → 归到 `gpt-oss`（丢 size，跟 20b 会混）
- Cloudflare `@cf/openai/gpt-oss-120b` → 归到 `@cf-openai-gpt-oss-120b`（带 provider 前缀，被孤立）
- OpenRouter `openai/gpt-oss-120b:free` → 归到 `openai:-gpt-oss-120b-(free)`（带前缀+后缀，被孤立）

**改动：**
- 重写 `canonicalizeFamily(rawID, name)`：先剥所有 provider/license 前后缀，再按"架构 + size + variant"规则化
- 规则：必须包含 size suffix（120b/20b/70b/8b/3b 等数字+b）
- 量化级别（fp8/int4）从 family 名剥离，存 `quantization` 字段（FreeModels schema 已有该字段）
- `family-overrides.json` 处理 LLM 推断不准的特殊 case

**验收：**
- `gpt-oss-120b` family ≥ 6 个候选（cerebras + cloudflare + gitee + groq + nvidia + openrouter + sambanova）
- `gpt-oss-20b` family = 4 个候选（cloudflare + groq + nvidia + openrouter）
- `gpt-oss-safeguard-20b` family = 1 个候选（groq 独家）
- `llama-3.3-70b-instruct` family ≥ 4 个候选

### P4.1: api-center 入口解析层

**Why second:** P4.0 完成后，FreeModels family 数据可信，可以做路由集成。

**Files:**
- Modify: `internal/services/freemodels_registry.go` (新增 `ListByFamily(family string) []FreeModelMeta` 方法 + 复合索引 `byFamily`)
- Modify: `internal/services/alias_service.go` (新增 `LookupAlias(name string) ([]AliasCandidate, error)` 方法，供 proxy 层调用)
- Modify: `internal/proxy/server.go` (在入口收到请求后加解析层)
- Modify: `internal/services/subgroup_manager.go` (扩展 `SelectSubGroupForModel` 接受预解析的候选池, 或新增 `SelectFromCandidates`)

**新增解析层伪代码：**

```go
// resolveModelCandidates: 把用户的 body.model 解析成具体候选池
// 三层优先级 alias > family > raw, 第一个命中即返回
func (s *Server) resolveModelCandidates(ctx context.Context, aggregateGroup *models.Group, userModel string) []ModelCandidate {
    // 1. Alias 表 (最高优先级, admin 显式声明)
    if candidates := s.aliasSvc.LookupAlias(ctx, userModel); len(candidates) > 0 {
        return candidates // [{SubGroupID, RealModel}, ...]
    }
    // 2. FreeModels Family (自动识别)
    if metas := s.freeReg.ListByFamily(userModel); len(metas) > 0 {
        return s.familyToCandidates(aggregateGroup, metas) // 映射到本 aggregate 下的 sub-group
    }
    // 3. Raw ID 字符串匹配 (兜底, 现有逻辑)
    return nil // nil 表示让 subgroup_manager 走原来的逻辑
}
```

**转发时改写 body.model:** 候选选中后，把 request body 的 `model` 字段改成 `candidate.RealModel`，复用现有 `ModelRedirect` 实现。

### P4.2: family → alias 自动建议

**Why third:** 让 admin 看见 family 命中的候选，可以一键 promote 成显式 alias（持久化决策、覆盖未来 family 数据变化）。

**Files:**
- Modify: `internal/services/alias_suggestion_service.go` (新增 family 数据源, 当前应该已有 alias 字符串相似度 / 其他启发式)
- Modify: `internal/handler/alias_suggestion_handler.go` (返回 family-based suggestion)

**新增 suggestion 类型：**

```go
type FamilySuggestion struct {
    FamilyName     string                  // "llama-3.3-70b"
    Candidates     []FamilyCandidate       // [{ProviderName, SubGroupID, RealModelID}, ...]
    ConfidenceTier string                  // "high" | "medium" | "low" 基于 candidates 数量 + family 命名稳定性
    ProposedAlias  string                  // 推荐 alias 名 (默认 = family name)
}
```

### P4.3: UI 一键采纳建议

**Files:**
- Modify: `web/src/components/aliases/AliasManageTab.vue` (alias 管理页加"系统建议"区块)
- Modify: `web/src/api/aliases.ts` (调 P4.2 的 suggestion 端点)

**UI:** alias 管理 tab 顶部加一个"系统检测到 N 个未配 alias 的 family"折叠区，展开后每行一个建议，admin 点"采纳"即生成对应 alias 行。

## 工作量预估

| 子任务 | 工作量 |
|---|---|
| P4.0 family 清理 | ~80 行 TS + 数据 override，~3 小时 |
| P4.1 解析层 + family 索引 + body 改写 | ~150 行 Go，~3 小时 |
| P4.2 family suggestion 服务 | ~80 行 Go，~1.5 小时 |
| P4.3 UI 一键采纳 | ~80 行 Vue，~1.5 小时 |
| **合计** | **~390 行 + 9 小时** |

## 不在范围（Out of scope）

- ❌ **429 vs 5xx 分类处理** —— 留 P5（现有熔断器够用，配额感知是优化）
- ❌ **延迟感知动态权重** —— 留 P5
- ❌ **主动健康探针** —— 留 P5
- ❌ **streaming 中途失败 retry** —— 复杂且收益小，永远不做（标准做法是直接返错）
- ❌ **跨 family 智能升降级**（"用户填 llama-3.3-70b 但 70b 全炸了，自动降到 8b"）—— 不做，质量差异太大用户预期会被破坏

## 当前状态 snapshot

- ✅ P0-P3 已完成，api-center main / FreeModels master 都已 push 到 origin
- ✅ FreeModels v1/models 端点已部署（gh-pages sync-pages workflow 完成）
- ✅ api-center 已切到新 schema，free_models 持久化正确
- ⬜ FreeModels family 命名仍是老的（P4.0 要清理）
- ⬜ alias 系统跟 aggregate 路由独立运行，未联动（P4.1 要打通）

## 执行约束

- 用户已全权授权推进，不再 review 每个决策
- 两个 repo 都直接 commit 到 main/master（窗口期内 breaking 改动免费原则）
- 每个 Phase 完成后单独 commit，不混合
- P4.0 完成后 push FreeModels → 等 sync-pages 部署完 → 验证 api-center 拉到新 family 数据 → 再开 P4.1
