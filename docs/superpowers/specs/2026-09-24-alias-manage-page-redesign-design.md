# 模型别名「管理」页重构设计

> **状态**: 待评审
> **日期**: 2026-09-24
> **范围**: `/aliases?tab=manage` 的信息架构与交互 + 配套后端小改动
> **前置**: 快速整理 tab 的 spec 已把「管理 tab 交互重设计」列为 followup（`2026-05-02-alias-quick-setup-design.md` §9）

## 0. 一句话

把这一页的主轴从 **auto 的三个档位** 换成 **别名本身**：一张别名列表 + 一个候选池抽屉 + 一个 auto 面板；顺手修掉三个「让数字说谎」的缺陷。

## 1. 现状病灶（逐条带代码出处）

| # | 病灶 | 证据 |
|---|---|---|
| 1 | **主轴错位**：页面按 `simple/medium/complex` 三个保留别名分栏，但这只是别名系统的一个特例。自定义别名（`hermes`、`xop35qwen2b`…）被当成候选塞进档位栏。标题写「61 条映射」而非「N 个别名」，说明它按 DB 行思考 | `AliasManageTab.vue` 2403 行；`internal/models/types.go:195-214` 唯一索引是 `(alias, group_id, real_model)` |
| 2 | **合法重复看起来像 bug**：同一模型同时属于 medium 和 complex 是设计允许的；同名模型来自两个分组（NVIDIA NIM / 智谱 AI）在「不可用」区并排出现，界面不做区分，用户只能猜 | 唯一索引三元组；`AliasCandidateList.vue:76-81` |
| 3 | **「实际分流」永远是 0**（真缺陷）：日志落的是**请求名**，别名路由时即别名本身（`request_helpers.go:21-28` 的 `originalModelFor` 优先取改写前名字，`server.go:817` 用它填 `RequestLog.Model`）；而前端按 `${group_id}::${real_model}` 建索引、抽屉也按 `real_model` 查（`AliasManageTab.vue:456`、`AliasEditDrawer.vue:125`）。键口径不一致 → 这个接口存在的目的（`dashboard_handler.go:405-407` 注释）恰好是回答「这条候选分到多少流量」，但它答不出来 |
| 4 | **权重口径两套**：DB 整数 weight 只是 SWRR 输入，真实选路还要乘 provider 先验 × Thompson 采样 × 延迟 EWMA（`router_engine/selector.go:177-204`）。列表上显示的 `share` 只是 `weight/sum`（`AliasManageTab.vue:636-647`），既不是配置占比也不是真实占比 |
| 5 | **假动作**：「修复」只做 `router.push` 跳到密钥页，用户还得自己点「+加入」（`AliasManageTab.vue:582-588`）；「路由生效中」开关存了也读了，但 `PickForAuto` 与 middleware 从不查它（`selector.go:280,305`）—— 关掉之后 auto 照样选档 |
| 6 | **后端术语泄漏**：「未暴露」= `group.exposed_models` 的实现细节；provider 名靠子串猜（`inferProvider`，`AliasManageTab.vue:383-399`） |
| 7 | **一件事 5 个界面**：4 种视图模式（卡片/表格/分栏/健康）+ 密钥页 800 行的 `ModelAliasModal.vue`；默认值还不一致（管理页 `weight=100/priority=100`，密钥弹窗 `100/0`，后端再把 0 改回 100，`alias_service.go:223`） |

## 2. 目标与非目标

**目标**

- 别名是页面主对象；档位是别名的一个属性
- 页面上每个数字都能解释自己是什么口径
- 异常状态就地处置，不做跨页编排
- auto 规则一屏看懂，且开关**真的**影响选路
- 组件按职责拆开，`AliasManageTab` 退化成容器

**非目标**

- 不改 SWRR / Thompson 采样 / 冷却策略本身
- 不改 `model_aliases` 表结构（无迁移）
- 不动「快速整理」tab 的家族选择流
- 不动密钥页的探活与模型卡

## 3. 信息架构

按用户真实动作分四层，只有前两层占据主视野：

| 动作 | 落点 |
|---|---|
| 定义：一个名字 → 一组候选 | 别名列表 + 候选池抽屉 |
| 诊断：能不能用、24h 打到哪 | 列表行内状态 + 抽屉候选状态 |
| 调权：候选间倾向 | 抽屉内滑杆 + 拖拽排序（沿用现有抽屉的三个设计决定） |
| auto：档位与阈值 | 顶部一个可折叠面板，不是页面骨架 |

### 3.1 页面骨架

```
模型别名 · 24 个别名 / 61 条候选      [搜索]  [全部 | 有问题(6) | auto 档位 | provider ▾]   [+ 新建别名]

┌ auto 智能路由 ──────────────────────────────────────────────────────────────── ▾ ┐
│ 选路方式: ( ○ 关闭，一律走「简单」档 · ◉ 按请求长度选档 )          [Economy] [Balanced] [Perf] │
│ 简单 ≤ 2,000 tok ┊ 中等 ≤ 8,000 tok ┊ 复杂 > 8,000 tok        (候选: 8 / 17 / 9)│
└──────────────────────────────────────────────────────────────────────────────────┘

别名              角色          候选  24h 请求   错误率   p50     状态
hermes            auto·中等       3    1,204      0.4%    1.9s   ● 全部可用
xop35qwen2b       —               1       12      0%      2.4s   ⚠ 1 个候选所在分组未公开此模型   [公开]
minimax-m2.5      auto·中等       4        —       —       —     ⚠ 2 个候选跨档位复用
```

- 一行 = 一个别名，不再一行 = 一条候选
- 「有问题(n)」筛选器取代独立的「健康」视图
- 分组开关（按 provider 分组 / 不分组）取代独立的「分栏」视图
- 卡片视图删除：它提供的信息量是列表的子集
- auto 的阈值滑杆与三档预设（现有 `presetList`，`AliasManageTab.vue:995-1008`）语义不变，只是从「页面骨架」降级为一个可折叠面板

### 3.2 候选池抽屉（点行展开）

```
hermes                                    auto·中等   [从档位移除 ⌄]   [复制]  [删除别名]
──────────────────────────────────────────────────────────────────────────────────
候选（拖拽排序 = tie-break 顺序）        配置占比   24h 实测占比    状态
 groq        openai/gpt-oss-120b    ▓▓▓▓░░░ 55%  ▓▓▓░░░░ 48%   ● 可用         ⏤ ⌫
 nvidia-nim  meta/llama-3.3-70b     ▓▓▓░░░░░ 30%  ▓▓▓▓░░ 52%   ⚠ 未公开       [公开] ⏤ ⌫
 cerebras    llama-3.3-70b          ▓▓░░░░░░ 15%  ░░░░░░░░  0%   ⚠ 黑名单       [解除] ⏤ ⌫
──────────────────────────────────────────────────────────────────────────────────
+ 添加候选:  [分组 ▾]  [模型 ▾]  [加入]
24h: 1,204 次 · 错误率 0.4% · p50 1.9s · 折算成本 $0.31
```

三处口径明确：

- **配置占比** = 归一化后的 weight（就是你写的）
- **24h 实测占比** = 真实发生（见 §5.2 的口径修正）
- 两者并排，差得远就是「算法在替你调」，tooltip 说明乘了哪些因子（provider 先验 / 成功率采样 / 延迟）

## 4. 交互规则

1. **就地处置**：候选状态是「分组未公开此模型」时，行内按钮直接调后端完成公开（§5.1），成功后状态原地翻转；不再跳页。「黑名单」同理走已有的 block/unblock。
2. **auto 开关语义**（已与用户确认）：关闭 = `model="auto"` 的请求**固定走 `simple` 档**（不是报错、不是透传）。`simple` 无可用候选时沿用现有「无候选」错误。关闭时跳过 sticky session 读写（没有选档动作可粘）。
3. **跨档位复用显式化**：同一 `(group, model)` 被多个档位引用是合法的，列表行打「跨档位」标记，抽屉头部给「从本档位移除」。不隐藏。
4. **拖拽 = priority**：沿用现有抽屉的做法（priority 只是 SWRR 平票时的 tie-break，界面上不再摆数字输入框骗人）。
5. **默认值单一来源**：`DEFAULT_WEIGHT = 100` / `DEFAULT_PRIORITY = 100` 只定义一次，管理页与密钥弹窗共用；后端 `0 → 100` 兜底保留。
6. **文案去术语**：`未暴露` → `该分组未公开此模型`；`修复` → `公开`；provider 名不再由前端子串猜（`inferProvider`，`AliasManageTab.vue:383-399`）—— 分组上没有 provider 字段，可用的是 `Group.ChannelType`（前端已拿到）与后端已有的 host 反查（`AliasSuggestionService.resolveProviderID`，`alias_suggestion_service.go:180`）。本期用 **channel_type 作为 provider 标签**，缺失时回退到分组显示名，并在 tooltip 里说明是回退；不改 `Group` 结构。

## 5. 后端改动

### 5.1 `POST /api/aliases/expose`

入参 `{alias, group_id, real_model}`。事务内：取分组 → 若 `model_routing_mode == "specified"` 且 `real_model ∉ exposed_models` 则追加 → 保存。幂等：已公开返回 `already_ok`。passthrough 模式直接返回 `not_needed`（黑名单里则返回明确错误，让前端提示「先从黑名单移除」）。

替代现在前端那套「跳页 + 手点 + 期望用户找回来」的编排（`AliasManageTab.vue:295-357` 里那段前端补 `exposed_models` 的逻辑一并下沉）。

### 5.2 流量口径修正（让「实测占比」不再是 0）

日志只有 `(group_id, 请求名)`，没有落改写后的真实模型，所以别名路由的候选无法按 `real_model` 归因。两步走：

- **本次（P1）**：前端把索引键从 `${group_id}::${real_model}` 改成 `${group_id}::${alias}`。这在「一个分组在一个别名里只贡献一条候选」时是精确的（绝大多数情况）；同分组多候选时退化为分组级合计，界面上如实标注「按分组归因」。
- **后续（不在本期排期内）**：`RequestLog` 增 `ResolvedModel` 列，落改写后的真实模型，届时按 `(group_id, resolved_model)` 精确归因，日志页也能显示「请求名 → 实际模型」。必须写成显式 migration 并在主从两个分支都注册 —— 照 `internal/db/migrations/v2_8_2_RequestLogUsageColumns.go` 的先例（AutoMigrate 只在 Master 分支跑，Slave 不跑，靠 AutoMigrate 补列会让 Slave 的日志**静默全部不落库**）。本次不做，避免把重构和迁移绑在一起。

### 5.3 `PickForAuto` 接上开关

`internal/router_engine/selector.go` 的 `PickForAuto`（:404-413）现在完全不读 `cfg.Enabled`。改为：`Enabled` 为 false 时直接 `return s.PickByAlias(ctx, ReservedAlias(TierSimple))`，为 true 时保持现有阈值逻辑。粘性会话（`middleware.go:77-156`，`StickyTTL` 30 分钟）在关闭时跳过读写 —— 没有「选档」这个动作可粘。补单测覆盖两条分支 + sticky 跳过。

### 5.4 不新增查询接口，只补一列

别名列表的 24h 请求数 / p50 / token / 折算成本由前端把 `/api/dashboard/model-timings`（按请求名聚合，已返回 `calls/avg_ms/tokens/cost_usd`）与 `/api/aliases` join 得出；「实测占比」用 `ModelTraffic` 的 `calls`。61 条量级没必要为它加新端点。

唯一的后端补充：`ModelTimings` 的 SELECT 加 `SUM(CASE WHEN is_success THEN 0 ELSE 1 END) as errors` 并在 `ModelTiming` 上暴露 `errors`/`error_rate`（与 `TopModels` 同一口径，`dashboard_handler.go:293`）。原因是 `TopModels` 带 `limit`（上限 50，按调用量截断），别名全量列表不能靠它取错误率；`ModelTimings` 本来就是「无 LIMIT 的轻量版」，加这一列是它的职责所在。

## 6. 前端组件拆分

```
AliasManageTab.vue          容器: 数据加载 + 布局 + 抽屉开合   目标 < 300 行 (现 2403)
├─ AutoRoutingPanel.vue     新增: auto 开关 + 阈值 + 预设 + 三档摘要
├─ AliasList.vue            新增: 列表 + 搜索 + 筛选 + 分组开关
├─ AliasDetailDrawer.vue    AliasEditDrawer.vue **重命名并扩展**（不新建第二套抽屉）: 候选池 + 双口径占比 + 就地处置 + 24h 摘要
└─ StatePill / MetricCell / AliasCandidateList   复用
```

**删除**：`AliasTableView.vue`(399)、`AliasSplitView.vue`(391)、`AliasHealthView.vue`(366)、卡片视图、`alias-view-mode` 这个 localStorage 键（连同读写）。能力被「列表 + 筛选器 + 分组开关」覆盖。

**状态**：新增 `web/src/services/aliases.ts`（composable，与仓库现有 `services/auth.ts` 一致，不引入新状态库）持有 aliases / groups / timings / traffic 与派生的行模型，供管理页与密钥弹窗共用。

**密钥页**：`ModelAliasModal.vue` 保留入口（就地给模型起别名是高频动作），但内部改为复用 `AliasDetailDrawer`，不再是第二套候选编辑 UI。

## 7. i18n

新 key 落在既有 `aliases.*` 命名空间下（`aliases.list.*` / `aliases.drawer.*` / `aliases.auto.*`），三语齐全，命名沿用现有层级；删除随视图一起消失的 key（`aliases.view*`、健康视图相关）。实现时先读 `web/src/locales/zh-CN.ts` 对应段落再插，逐个 key 验证被引用。

## 8. 边界情况

- **别名候选跨档位**：列表行标记 + 抽屉「从本档位移除」，不做静默去重
- **`simple` 池为空且 auto 关闭**：沿用现有「无候选」错误，不新造错误码
- **公开失败（分组不存在 / 模型在黑名单）**：`/aliases/expose` 返回明确原因，前端就地提示，不回滚列表状态
- **traffic 接口取不到**：实测占比列整列隐藏（沿用现有 `catch` 的降级思路），不显示 0% 冒充数据
- **同分组多候选**：实测占比 tooltip 标注「按分组归因」
- **旧 localStorage**：`alias-view-mode` 直接废弃不迁移；`alias-suggest-dismissed-families` 保留
- **61 → 24 行的规模假设**：若别名数增长到需要分页，再加分页；当前不做

## 9. 分期

| 阶段 | 内容 | 量级 |
|---|---|---|
| P1 | 别名列表 + 详情抽屉 + 流量口径修正(§5.2 前端部分) + 就地公开(§5.1) + `ModelTimings` 补 errors 列(§5.4) + 默认值统一 + 文案去术语 | 1.5 天 |
| P2 | auto 面板 + `PickForAuto` 真接开关(§5.3) + 单测 | 1 天 |
| P3 | 密钥弹窗复用抽屉 + 删除已不可达的旧视图文件与 i18n 死 key | 0.5 天 |

每阶段独立可交付、可回滚；P1 不依赖 P2/P3。旧三视图（表格/分栏/健康）与卡片视图在 P1 就被新列表取代、从渲染路径上摘掉（不再可达），P3 才物理删除文件与随之失效的 i18n key —— 这样 P1 的 diff 里不含大段删除，回滚面更小。

## 10. 验收

- `go test ./...` 覆盖：`/aliases/expose` 幂等与两种模式、`PickForAuto` 开关两分支
- `npm run type-check` 通过；改动文件 eslint 错误数不高于改前基线
- 真实数据 CDP 冒烟（1280×900）：列表行数 = 别名数、抽屉双口径非 0、点「公开」后状态翻转且 `group.exposed_models` 落库、auto 开关关闭后 `model="auto"` 请求打到 simple 池（看日志）
- 主观：不再需要理解「暴露 / group_id / 档位」才能完成「给 hermes 加一个候选」
