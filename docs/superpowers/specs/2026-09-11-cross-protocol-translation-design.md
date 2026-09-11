# 跨协议转译:整体评估与实施方案

> 触发:V2EX《micro-one-api 协议转换: Chat ↔ Responses ↔ Messages》(https://www.v2ex.com/t/1241256)
> 对应 Roadmap:`README.md:305` `- [ ] 跨 channel 协议翻译(OpenAI ↔ Anthropic ↔ Gemini)`
> 状态:**待评审,未写任何生产代码**
> 日期:2026-09-11

---

## 0. 一句话结论

**值得做,影响中等偏大但完全可控,并且可以 100% 增量交付。**

关键 mitigator 有三条,任何一条单独成立都足以让风险可控,三条叠加则基本无回归风险:

1. **不映射 = 零行为变化**。转译只在命中映射节点时触发,现有所有分组、所有流量路径的代码行数不变。
2. **P0 零生产接入**。纯函数包 + 测试,独立合入、独立回滚。
3. **协议配对由配置声明,不由运行时推断**。避免"看起来能转就转"导致的静默错配。

规模参照:项目现有 Go 生产代码 **30,674 行** / 测试 **8,256 行**(52 个测试文件)。

| 范围 | 预估新增 | 占比 |
|---|---|---|
| P0 + P1(非流式全链路) | ~1,500-2,000 行(含测试) | ~5% |
| 全量 P0-P3(含流式 + Gemini + Responses) | ~4,000-5,000 行 | ~13% |

---

## 1. 要解决什么

### 1.1 场景(已与用户对齐)

> 客户端说某种协议(Anthropic / Gemini / …),网关接住 → 转成上游节点听得懂的协议 → 把回包翻回客户端原来那种协议的结构。**请求什么结构,就返回什么结构。客户端什么都不用改。**

### 1.2 这其实是在提升"存量资产利用率",不是新增能力

这是本项目最值得强调的一点:内置 10 家免费 Provider(Groq / Cerebras / OpenRouter / Together / Mistral / Cohere / GitHub Models / HF Router …)**全部是 OpenAI 兼容协议**。今天这批 Key 池只能服务 OpenAI 客户端;Anthropic 客户端、Gemini 客户端想吃这池子,吃不到。

加了转译,**同一批 Key 同时服务三种客户端,不新增任何 Provider 成本**。这是把已经建好的池子变现,不是去建新池子。

### 1.3 三条补强

1. **不需要新增「兼容模式接口」** —— 复用现有 `/openai/*`、`/anthropic/*`、`/gemini/*` 前缀,改的是聚合的成员构成,不是路由表。
2. **转译解决「结构」,不自动解决「模型名」** —— 协议转好了,`claude-*` 在 Groq 上仍然不存在。网关仍必须回答"这个请求由哪个真实模型 serve"。
3. **不止 body,URL 路径也要改** —— Gemini 模型名在路径里(`/v1beta/models/{model}:generateContent`),Anthropic 与 OpenAI 端点不同(`/v1/messages` ↔ `/v1/chat/completions`)。**转换点必须在 `BuildUpstreamURL` 之前。**

---

## 2. 参考来源与适用边界

micro-one-api 的三层结构(`apicompat` 纯函数层 / `adaptor` 按渠道注册 / `server` 路由降级链)与我们高度同构 —— 我们已有 `internal/usage`、`internal/pricing`、`internal/errtriage` 这类无依赖纯函数包,风格可以直接对齐。

| 文章项 | 价值 | 我们的处理 |
|---|---|---|
| hub-and-spoke 拓扑方法论 | ★★★★★ | **方法照搬,结论反转**(见 §4.1) |
| 防御性规范化清单 | ★★★★★ | 直接抄(§4.3) |
| 流式状态机 + 幂等 `Finalize*` | ★★★★☆ | 照搬,但需改造现有 stream_integrity(§5.2) |
| usage 双向桶投影 | ★★★★☆ | 我们已有一半,补逆投影(§5.5) |
| 协议能力错误 → 换渠道 | ★★★☆☆ | 映射到 `classifier.go` 新 Category(§5.4) |
| 兼容性矩阵测试 | ★★★☆☆ | 照搬 `failover_characterization_test.go` 风格 |
| OAuth 订阅渠道 / `store:false` / `metadata.user_id` | ✕ | 不适用,我们是纯 API Key 池 |
| Responses WS 升级 / web_search 合成 | ✕ | 排到最后或不做 |

---

## 3. 现状盘点(代码级)

| 能力 | 现状 | 位置 |
|---|---|---|
| 协议识别 | 靠 URL 前缀 + `channel_type`,无 body 级识别 | `router/router.go:344-351` |
| body 转换 | **完全没有** | `channel/channel.go:24-56` |
| 流式转发 | 字节级透传 + header-hold + 截断检测 | `proxy/stream_integrity.go:47` |
| usage 归一化 | **已跨协议**,四套字段收敛到包容桶 | `usage/usage.go:74-88` |
| 聚合成员资格 | 按 `channel_type` 强约束 | `services/aggregate_group_service.go:83` |
| 子分组自动挂载 | `channel_type` → 系统 role 硬映射 | `models/types.go:101` |
| 候选路由 | `ModelCandidate` 无协议维度 | `services/model_resolver.go:15` |
| 错误分类 | 5 类 | `errors/classifier.go:16-28` |
| 已注册 channel | openai / openai-response / anthropic / gemini | `channel/factory.go` |

---

## 4. 方案选型

### 4.1 hub 选 Chat,不选 Responses

文章选 Responses 当 hub,因其上游多为 Codex/Claude OAuth 订阅渠道。**我们的子分组绝大多数是 `channel_type=openai`** —— 照抄会让主路径变成 `Chat→Responses→Chat` 两次白转换。

按文章自己的判据("hub 选能无损承载其他协议 + 主流量零转换的那个"),hub 应是 **ChatCompletions**:

| 方向 | hub=Chat | hub=Responses |
|---|---|---|
| Chat → Chat(**主流量**) | **0 次** | 2 次 |
| Anthropic → Chat | 1 次 | 1 次 |
| Anthropic ↔ Responses | 2 次 | 1 次 |
| 新增第 5 种协议 | 2 条边 | 2 条边 |

代价:Chat 表达不了 `cache_control`(Responses 同样丢,不是差距)。

### 4.2 协议配对:映射节点(推荐)

| | A · 运行时开关 | B · 万能聚合 | **C · 映射节点(推荐)** |
|---|---|---|---|
| 做法 | 聚合加 `allow_cross_protocol`,候选池纳入异协议组 | 新建 `/v1/*` 聚合 | 管理员显式声明"把源组映射进目标聚合" |
| 动 `ValidateSubGroups` | 是 | 否 | **否** |
| 动 `ModelResolver` | 是 | 否 | **否** |
| 模型名问题 | 运行时推断,易静默错配 | 同 A | **声明即解决** |
| 可解释性 | 差 | 中 | **好**(面板可见"3 原生 + 2 映射") |
| 复用 SWRR/熔断/EWMA | 需改造 | 需改造 | **白拿**(映射节点就是子分组) |
| 回归风险 | 中 | 中 | **低** |

C 的连带好处:自动挂载规则不用动,不会一建 Groq 组就被 Anthropic 客户端打爆。

**实现载体**:新建 `group_mirrors` 表,而非给 `GroupSubGroup` 加列。后者是"真实子分组"语义,backup / 同步 / UI 到处要判断;新表更干净,只需在 `SubGroupManager` 构建 selector 时把镜像行 merge 成 `subGroupItem`(名字用 `openai-pool@gemini` 避免撞名)。

### 4.3 防御性规范化(直接抄)

| 规范化 | 防的上游报错 |
|---|---|
| 空 tool 参数补 `{}` | OpenAI 系 400 |
| 空 tool 输出补 `"(empty)"` | Anthropic 空 content |
| schema 兜底 `{"type":"object","properties":{}}` | OpenAI 400 / Anthropic 422 |
| **Anthropic tool_use / tool_result 配对修复 + user/assistant 交替** | Anthropic 400 |
| Chat→Anthropic 补默认 `max_tokens` | Anthropic 必填 |

配对修复对我们比对 micro-one-api **更刚需**:failover 会跨渠道切子分组(`proxy/server.go:591-655`),一次重试可能把同一会话从 anthropic 组切到 openai 组,重排后的历史必须结构合法。

---

## 5. 影响面评估(逐系统)

### 5.1 转发主链路 —— 影响**低**

- 请求转换插在 `proxy/server.go:325` 前(子分组已选定、`BuildUpstreamURL` 之前)
- 响应转换插在 `proxy/response_handlers.go:20`
- **触发条件**:仅当「入站协议 ≠ 目标节点协议」。不映射的节点完全不进这条路径

### 5.2 流式 —— 影响**中→高**,但可延后

现有 `stream_integrity.go` 是字节级透传,会撞三处:

| 现有行为 | 冲突 | 处理 |
|---|---|---|
| 不解析 SSE | 转换器必须解析 + 重序列化 | 做成 reader 管道插在 `flushAndStream` 前 |
| header-hold 等原始首帧 | 需等**转换后**首帧 | hold 点后移 |
| 截断只认 `[DONE]`/`finish_reason` | 终止符协议相关 | 分协议:Anthropic `message_stop`、Responses `response.completed` |
| 断流 → `truncated=true` | 客户端收不到终止符会挂 | `Finalize*` 合成终止符,保留 `truncated=true` 供观测 |

**若目标是 Claude Code / Cline / Cursor,流式是刚需** —— 这类客户端几乎全流式,只做非流式等于没做。那样 P2 要并入 P1。

### 5.3 路由与选路 —— 影响**低**

映射节点就是普通子分组,`ModelResolver`、`SubGroupManager`、P4 候选池**均不需要改**。这是方案 C 最大的收益。

### 5.4 密钥池 / 限流 / 熔断 —— 影响**中**,有一个必须显式处理的坑

`keyProvider.SelectKey(group.ID, …)`(`server.go:316`)按 `group.ID` 记账,而密钥属于源分组。

> **账本归源分组,熔断归目标聚合。**

映射节点被选中时,key 选择**必须传源分组 ID**,否则同一把 Key 会在「原生聚合」和「映射聚合」各有一份 rpm/rpd 账本,**合起来突破上游真实限额**。反之熔断 / 延迟 EWMA / 子分组冷却记在目标聚合(那是选择上下文)。这条要写进代码注释,不能靠人记。

另:`classifier.go` 需新增"协议能力错误"Category,且**必须不 `ShouldFailFast()`**(`server.go:662`),否则永远换不到原生支持的渠道。

### 5.5 usage 与成本 —— 影响**低**

`internal/usage` 已把四套字段收敛到包容桶(`prompt` 含缓存 + 独立 `CachedPromptTokens`),等于文章的 `ProjectOpenAI`。**只缺逆投影 `SplitInclusive`**:转成 Anthropic 响应时要把包容桶拆回 `input_tokens`(不含缓存)+ `cache_read_input_tokens`,否则客户端用量显示与我们的成本账都错。一个纯函数的事,**投入产出比最高的一项**。

### 5.6 数据层 —— 影响**中**(四个注册点,一个都不能漏)

新增 `group_mirrors` 实体必须同时落地:

1. `models` + `internal/db/migrations/vN_N_N_*.go`,并在 **`app.go` 的 master 与 slave 两个分支都调用**(见 `v2_7_0_InviteTokens` 的注释:Slave 不跑 AutoMigrate)
2. `SyncPayload`(`services/sync_service.go:73`)+ 合并逻辑 + `sync_policy.go` 字段白名单
3. `backup/schema.go` 的 `DataV1` + exporter + importer
4. 管理 API(`/api/groups/{id}/mirrors` CRUD)

漏任何一个都会表现为"多机部署时映射只在主节点生效"或"备份还原后映射丢失"。

### 5.7 管理面板 —— 影响**中**

分组详情新增「映射节点」区块(列出源组、模型映射表、健康度)+ 映射编辑弹窗。约 3-6 个 Vue 组件的小改动。

### 5.8 测试 —— 影响**中**(正面)

新增 `internal/apicompat` 矩阵测试(入站 × 上游 × 流式 golden)。项目已有 52 个测试文件、8,256 行测试,`failover_characterization_test.go` 的 characterization 风格可直接照搬。

### 5.9 影响汇总

| 子系统 | 影响 | 是否阻塞 P0/P1 |
|---|---|---|
| 转发主链路 | 低 | 否 |
| 路由 / 选路 | 低 | 否 |
| usage / 成本 | 低 | 否 |
| 密钥池 / 限流 / 熔断 | 中 | P1 需显式处理 |
| 数据层(迁移/同步/备份) | 中 | P1 |
| 管理面板 | 中 | P1(可延后一版) |
| 流式 | 中→高 | 仅当要服务 Claude Code/Cline |

**总体评级:中等偏大,可控。**

---

## 6. 分期计划

### 最小 MVP(建议先做这个,再决定要不要上 P0-P3)

> 相对 §4.2 的修正:**MVP 走「方案 A + alias 显式映射」,不建 `group_mirrors` 表。**
> 理由是三个发现让方案 A 的风险消失了、成本却低一个量级,见下。

**范围:只做「Anthropic 进 → OpenAI 出」一条链路。**

| 做 | 不做 |
|---|---|
| Anthropic ↔ Chat 请求/响应转换 | Gemini、Responses |
| 流式双向(**必做**) | 反向(OpenAI 客户端 → Anthropic 上游) |
| `internal/usage.SplitInclusive` | 新表 / migration / 备份 / 同步 |
| 8-10 个 golden fixture | 新的管理面板页面 |

**三个让成本骤降的关键点:**

1. **不建新表 —— "channel 不一致"本身就是标记。**
   放宽 `aggregate_group_service.go:83`,允许把 openai 组加进 anthropic 聚合;运行时 `sg.ChannelType != agg.ChannelType` 即判定"需要转译"。**零 migration、零 `SyncPayload` 改动、零 backup 改动、零新 UI。**
   而且这比 `group_mirrors` 方案**更正确**:节点就是子分组本身,`SelectKey(group.ID)` 天然用自己的 ID 记账 —— §5.4 那个"账本归源"的坑**自动消失**。

2. **模型名复用现有 `model_aliases`,零新增概念。**
   alias 已有 `alias → RealModel + GroupID`,且**已经在同步、已经在备份、已经有 UI**。给 `claude-sonnet-4-5` 建一条 alias 指向 openai 组的真实模型即可。

3. **自动挂载不受影响。** `AutoJoinSystemAggregate` 按 `SystemRoleForChannelType` 只挂同类型,不会有"一建 Groq 组就被 Anthropic 客户端打爆"的意外。

**一个必须加的护栏(否则会静默错配):**

> 异协议子分组**只允许通过 alias 命中进入候选池**;raw 字符串匹配与 family 匹配阶段必须排除它们。

否则 `claude-*` 没配 alias 时会走 `SelectSubGroupForModel` 的全量 SWRR 兜底(`subgroup_manager.go:67` 的 graceful degrade),请求被静默打到 openai 组、拿 Llama 的回答当 Claude 返回。这是方案 A 唯一真实的风险,加这一条就没了。

**工作量**:非流式打通 2-3 天,+ 流式 3-4 天,合计约 **1 周**。
**更小的验证版**:只做 `internal/apicompat` + 测试(不发流量),1 天,用来判断值不值得继续。

**已知可接受的粗糙处**(留给后续迭代):
- `/anthropic/v1/models` 并集会混入 openai 子分组的原始模型名,应只输出 alias 名
- 管理面板的异协议子分组行没有"转译"标记
- 转换失败的 400 会按现有逻辑计入 Key 熔断(应单独打标)

### P0 —— 纯函数层(零生产接入)

```
internal/apicompat/
  types.go                       Chat / Anthropic DTO
  anthropic_to_chat.go           请求 + 响应(非流式)
  chat_to_anthropic.go           请求 + 响应(非流式),含配对修复
  normalize.go                   §4.3 防御性规范化
  compatibility_matrix_test.go   入站 × 上游 × 非流式 矩阵
  fixtures/                      golden 用例
```

同时在 `internal/usage` 补 `SplitInclusive`。

**验收**:`go test ./internal/apicompat/...` 全绿;用 Playground 录 20-30 组真实 body 做 golden。
**风险:零。** 与方案选型无关,现在就能动。

### P1 —— 映射 + 非流式接入

- `group_mirrors` 表 + 四处注册(§5.6)
- 请求/响应转换 hook(§5.1)
- 账本归源 / 熔断归目标(§5.4)
- `/v1/models` 并集输出映射后的模型名

**验收**:`/anthropic/v1/messages` 能经映射节点打到 openai 子分组并正确返回;**不配置任何映射时,行为与今天逐字节一致**。

### P2 —— 流式双向

先做单向(Anthropic 客户端 → openai 上游),跑稳再做反向。

### P3 —— Gemini / Responses 边

各一条到 hub 的边,复用 P0-P2 全部基建。

---

## 7. 风险与对策

| 风险 | 对策 |
|---|---|
| 破坏现有纯透传 | 转换仅在协议不匹配时触发;不映射即零变化 |
| 成本/用量算错 | P0 就补 `SplitInclusive` + 双向往返测试 |
| 日志口径混淆 | `RequestLog.Model` 取自转换前 body(客户端视角),需确认不与上游视角混用 |
| 源组失效拖垮目标聚合 | 优雅降级(跳过 + warn)+ tombstone |
| 转译后上游 400 被误算到 Key 头上 | 转译路径失败单独打标,先不参与 Key 熔断 |
| 流式工作量超预期 | P0 先落地,用真实数据评估后面值不值得做 |

---

## 8. 替代方案(如果只想花一半力气)

**只做单向 `Anthropic → OpenAI` 非流式 + 流式**。理由:Claude Code / Anthropic SDK 是最大的需求方,而我们的池子几乎全是 OpenAI 兼容。砍掉 Gemini / Responses / 反向链路,工作量约减半,覆盖 ~80% 的实际价值。

---

## 9. 待决策

1. hub 选 Chat(本文论证)还是 Responses(若把 Codex/Claude Code 列为一等公民,结论可能变)
2. 目标客户端是否包含 Claude Code / Cline / Cursor → **决定 P2 流式是否并入 P1**
3. 是否只做 Anthropic → OpenAI 单向(§8)
4. 映射模型名是复用 alias 机制还是独立映射表
5. Gemini 边的优先级
