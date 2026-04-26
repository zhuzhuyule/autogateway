# V3 Mission Console — 实现状态与待办

> 对照设计源文件（`AutoGateway Admin v3.html` + `v3-shell.jsx` +
> `v3-keys-routing.jsx` + `styles-v3.css`，handoff 包归档于
> `/tmp/agdesign/myapi/`）逐项审计的差异清单。
>
> 状态约定：✅ 已实现 / 🟡 部分实现 / 🔶 Mock 数据 / ❌ 未实现 / 🔧 因后端
> 限制改造

---

# 0. 进度总览（2026-04-26 更新）

| 阶段 | 范围 | 状态 | 提交 |
|---|---|---|---|
| 基线 | v3 视觉系统 + 6 个视图全量重构 | ✅ 完成 | `d549e9a` |
| polish-1 | i18n + Dashboard live data + modal chrome | ✅ 完成 | `a6c0116` |
| 文档 | V3_STATUS audit + roadmap | ✅ 完成 | `2d64b7d` |
| 文档 | README 加 Mission Console section | ✅ 完成 | `a563b6f` |
| **P0** | rail 徽章 / Provider line-up / 后端 key_count | ✅ 完成 | `2c5c76f` |
| **P0-1** | New Group 三步流程 | ✅ 完成 | `3d1035c` |
| **P1** | Top Models API / Catalog chips / Auto-routing 决策 | ✅ 完成 | `92c0d45` |
| **P2** | Docs / Drag-reorder / Inline notes / V3 LogTable | ✅ 完成 | `9de589b` |
| **P3** | sparkline / latency / Settings 4 卡 / Top Models channel filter / Copy AUTH_KEY / version | ❌ 未开始 | — |
| **P2-8** | 7 modal 模板深度 v3 改写 | ✅ 完成 | `3d69011` |
| **Model Routing 重写** | 删 `auto-routing` + 新 `model_aliases` 表 + SWRR + 429 cooldown | ✅ Phase 1-4 完成（待重启验证） | 待提交 |
| **后端** | latency / activity / version / models metadata API | ❌ 未开始 | — |

---

# 1. 顶部 Chrome（Mission Console）

| 设计元素 | 状态 | 说明 |
|---|---|---|
| 品牌 + logo + `v0.4 · go 1.24` | 🟡 | 缺 `· go 1.24` 后缀（需后端 `/system/version`） |
| Live 脉冲点 + `9/10` keys | 🟡 | 用真 key_count，但没有 "N/M" 形式（缺总配额概念） |
| Req/s | ✅ | 真值（来自 `/dashboard/stats` 的 rpm） |
| P50/P99 延迟 | ❌ | 后端无 latency 直方图 API |
| Err % | ✅ | 真值 |
| Up 时间 (`47d`) | ❌ | 后端无 uptime API |
| 端点徽标（点击复制） | ✅ | `window.location.host`，可点击复制 |
| Bell 通知按钮 | ❌ | 需要事件流后端 |
| **Docs 按钮** | ✅ | `9de589b`：链接 README on GitHub |
| Theme + Lang + Logout | ✅ | 设计稿没有，我们额外加的 |

---

# 2. 左侧 Icon Rail

| 设计元素 | 状态 | 说明 |
|---|---|---|
| 5 个主项 + 底部 Settings | 🔧 | 我们有 6 个：多了 `model-dedup`（真后端功能） |
| **`keys` 项右上角 count 徽章** | ✅ | `2c5c76f`：从 `/groups/list` 拉非系统组数 |
| Hover tooltip | ✅ | `data-tip` |

---

# 3. Dashboard

## 3.1 KPI 卡片
| 设计 | 状态 | 说明 |
|---|---|---|
| Requests · 24h（含趋势） | ✅ | 真值 |
| **Active models** (`22 across 9 providers`) | 🔧 | 改成了 RPM · 10m，因后端没有"活跃模型数"统计 |
| Active keys（含 invalid 数） | ✅ | 真值 |
| Error rate | ✅ | 真值 |
| Sparkline 折线图 | 🔶 | **P3-13**：静态数组，需接 `/dashboard/chart` 数据 |

## 3.2 Top Models · 24h
| 设计 | 状态 | 说明 |
|---|---|---|
| 模型列表 + 排名 + tier chip | ✅ | `92c0d45`：`/api/dashboard/top-models` API + fallback 推算 |
| latency / err 副信息 | ✅ | `92c0d45`：API 返回 `avg_ms` + `error_rate`，行内显示 |
| Provider chip 堆叠 | ✅ | API 返回 `groups[]`，前端反查 provider |
| 调用量 + 占比条 | ✅ | 真 per-model 计数 |
| 趋势 sparkline | 🔶 | **P3-13**：依然静态，需 hourly bucket |
| Channel filter 下拉 | ❌ | **P3-16**：未实现 |
| `View catalog →` 跳转 | ✅ | 路由跳到 model-catalog |

## 3.3 ❌→✅ Provider 模块（曾是 Mock 热力图）
| 设计 | 状态 | 说明 |
|---|---|---|
| ~~30min × 6 provider 热力图~~ | ❌ 已删除 | `2c5c76f`：完全 Mock 数据，已下线 |
| **Provider line-up 卡（替代）** | ✅ | `2c5c76f`：每行 = provider + 分组数 + 24h 请求量 + ok/warn/danger chip |
| 30min 桶热力图（设计稿原版） | ❌ 后端 | 等 `/api/dashboard/activity` 后端 |

## 3.4 Endpoint quick-paste
| 设计 | 状态 |
|---|---|
| 3 个端点（openai / anthropic / gemini） | ✅ 用真 host |
| Copy AUTH_KEY 按钮 | ❌ **P3-17** |
| 单 endpoint 复制 | ✅ |

## 3.5 Refresh / Export
| 设计 | 状态 |
|---|---|
| Refresh 按钮 | ✅ 真重拉 API |
| Export 24h | ✅ 下载 JSON snapshot |

## 3.6 24h 请求量折线图
| 状态：✅ 沿用旧 `LineChart.vue`，接 `/dashboard/chart` 真数据 |

---

# 4. Groups & Keys

## 4.1 左侧 Group Sidebar
| 设计 | 状态 |
|---|---|
| Search | ✅ |
| System aggregates 段 + lock icon | 🟡 用 `sys` chip 替代 lock icon |
| Custom groups 段 | ✅ |
| **行尾显示 key 数量** | ✅ `2c5c76f`：后端加 `KeyCount` 字段，聚合组累加子组 |
| **拖拽排序** | ✅ `9de589b`：HTML5 DnD + 持久化 + 失败回滚 |
| 底部 New group / New aggregate 按钮 | ✅ |

## 4.2 Group Detail（标准分组）
| 设计 | 状态 |
|---|---|
| Avatar + display + lock chip | ✅ |
| channel chip + POST 端点 + Copy | ✅ |
| Get more keys 外链 | ✅ 仅匹配 `FREE_PROVIDERS` 时显示 |
| Edit / Copy / Delete 按钮 | ✅ |
| **4-cell stats**：Keys / 1h req / 24h req / Avg latency | 🟡 我们是 Keys / 24h / 7d / 30d req（**缺 1h 与 avg latency**） |
| Toolbar：Add key / Test all / Remove invalid / status filter / search | ✅ |
| Key 表格：mask / Status / 24h req / Failure 条 / Last used / actions | ✅ |
| **Notes 内联编辑** | ✅ `9de589b`：铅笔 icon + Enter 保存 + Esc 取消 + 乐观更新 |
| Pagination | ✅ |

## 4.3 ✅ New Group 三步流程
- 状态：完成于 `3d1035c`，`V3NewGroupFlow.vue`
- 完整对齐设计稿：step indicator + provider catalog grid + GetKey callout + paste & test

## 4.4 聚合分组 Sub-group Table
| 设计 | 状态 |
|---|---|
| 工具栏：Add sub-group / search / status filter | ✅ |
| 表格：avatar + display + 权重条 + active/invalid + status | ✅ |
| 状态：active/disabled/unavailable 三色权重条 | ✅ |

---

# 5. Auto Routing → **整体重写为 Model Routing（决策已定）**

> **决策结果（2026-04-26）**：废弃旧 `auto-routing` `group_mapping` schema，改为
> 「**别名表 + auto 关键字 + SWRR 轮询 + 429 cooldown**」三合一架构。**硬切，无弃用窗口**。
> 详见下文 **§13 Model Routing 重写设计**。
>
> 本节描述的"Auto Routing 视图"将被删除，替换为：
> - `V3AliasesView.vue` —— 别名管理主页（含 `auto-simple/auto-medium/auto-complex` 锁定行）
> - `V3RoutingThresholds.vue` —— 极简阈值设定卡（仅 token 阈值 + enabled 开关）

## 当前 5.x 实现状态（即将作废）

| 视图 | 状态 |
|---|---|
| 5.1 顶部 ROUTING ACTIVE / Disable | ✅ 当前实现，将随重写删除 |
| 5.2 Threshold 卡 | ✅ 当前实现，将拆出来作 V3RoutingThresholds 独立卡 |
| 5.3 Tier 卡片（per-model→tier→group） | 🗑️ 即将整页删除 |
| 5.4 Route Tester | 🗑️ 重写后保留同名按钮，逻辑改为查 alias 决策树 |

---

# 6. Model Catalog

| 设计 | 状态 |
|---|---|
| viewhead + search + Re-discover | 🟡 用 Refresh |
| h1 entries 计数 + `/openai/v1/models` 路径 | 🟡 缺路径展示 |
| Provider 头像 | ✅ |
| 模型名 + tier chip | ✅ |
| **tools / vision 能力 chip** | ✅ `92c0d45`：模型 id 启发式推断 |
| **ctx 上下文长度 chip** | ✅ `92c0d45`：模型 id 启发式推断 |
| Provider name + host | 🟡 我们展示 owned_by + groups |
| Spec：speed | ❌ 后端无 |
| Price / 1M | ❌ 后端无 pricing 数据 |
| Tier filter pills | ✅ |
| Free only 切换 | ✅ |
| Provider filter | ✅ |

---

# 7. Logs

| 设计 | 状态 |
|---|---|
| viewhead 简化筛选 | ✅ `9de589b`：v3-search × 3 + 2 select + Refresh + Export |
| h1 + "live tail" meta | ✅ |
| **日志行布局**（time / status / type / ms / path · model · group） | ✅ `9de589b`：v3-log-row 网格 |
| **详情模态**（v3-card + 完整字段 + 请求体 / 错误代码块） | ✅ |
| Stream/Non-stream 标签 | 🟡 字段读了，未单独显示 chip |
| **复杂列管理**（旧 LogTable 有：列显示/隐藏/导出 CSV） | 🟡 部分：导出有，列管理没复刻 |

> 旧 `LogTable.vue` 1300+ 行未删，留作回归备份。新 `V3LogTable.vue` 直接挂在 `Logs.vue` 上。

---

# 8. Settings

| 设计 | 状态 |
|---|---|
| **4 张固定分类卡** | 🔧 用真后端返回的 categories（动态） — **P3-15** 可选改回固定 4 卡 |
| 行：label + sub + value | ✅ |
| Rotate key 按钮 | ❌ 后端无 |
| Allow management UI 开关 | 🟡 取决于后端 config |
| AES-256-GCM 加密标识 | ❌ 静态文案 |

---

# 9. Login

| 设计 | 状态 |
|---|---|
| 设计稿无登录页 | — |
| v3 风格登录卡 | ✅ |
| `· go 1.24` 后缀 | ❌ **P3-18** 需 `/system/version` 后端 |

---

# 10. Modals（KeyCreate / GroupForm / 等 7 个）

| 状态 | 说明 |
|---|---|
| 视觉 chrome（head/body/footer 间距、border、surface-2 footer） | ✅ `a6c0116`：`.n-modal .n-card` 全局 override |
| Naive UI 主题（颜色/字号/圆角） | ✅ `a6c0116`：themeOverrides 覆盖到 v3 token |
| **内部 markup 深度对齐设计稿（step + grid + chip）** | ❌ **P2-8** 未做（约 1 天） |
| 业务逻辑 | ✅ 完整保留 |

---

# Mock 数据清单（已大幅缩减）

| 项 | 状态 |
|---|---|
| ~~Provider activity 热力图（30min × provider 桶）~~ | ✅ 已下线（替换为 line-up） |
| ~~Top Models 调用量推算~~ | ✅ 已接真 API（API 失败时回落推算） |
| ~~Top Models 趋势 sparkline~~ | 🔶 仍 Mock（每行小条），等 hourly bucket |
| Dashboard KPI sparkline | 🔶 仍 Mock，**P3-13** 待接 `/dashboard/chart` |
| Chrome `N/M` keys | 🟡 缺 M 部分（无配额概念） |
| Auto Routing tier footer 流量% / latency / err | ❌ 未做（等后端 per-tier 统计） |

---

# 后端工作清单

按价值排序，每个 API 解锁前端的具体功能：

| 后端 API | 解锁前端 | 优先级 |
|---|---|---|
| `GET /dashboard/timeseries?bucket=1h` | KPI sparkline + Top Models 趋势 | P3-13 |
| `GET /dashboard/latency` | Chrome P50/P99 + Group Detail Avg latency | P3-14 |
| `GET /system/version` | Brand `· go 1.24` 后缀 | P3-18 |
| `GET /system/uptime` | Chrome `Up 47d` | 暂不用 |
| `GET /dashboard/activity?window=24h` | 设计稿原版 30min 热力图（如果仍想要） | 中 |
| `models list 加 tools/vision/context/pricing 字段` | 替换前端启发式 | 中 |
| `groups list 加 latency_p50 / latency_p99` | Group Detail KV stats 加 latency | 中 |
| **Auto-routing schema 重设计** | 设计稿的 chain + drawer | 高（**leadership 决策**） |
| `GET /dashboard/top-models` | 已实现 `92c0d45` | ✅ |
| `groups list 加 key_count` | 已实现 `2c5c76f` | ✅ |
| `groups/list 扩字段(channel/group_type/is_system)` | 已实现 `2c5c76f` | ✅ |

---

# 详细 To-Do List

## 🚨 阻塞性

- [ ] **重启 Go 后端**让 P0-4 + P1-6 的 schema/handler 生效
  - 影响：rail 角标、Group 侧栏行尾 key 数、Top Models 真数据
  - 命令：`go run main.go` 或 `docker compose restart`

## ✅ 已完成

### 基线
- [x] v3 设计 token CSS（`assets/design-v3.css`，600+ 行）
- [x] Layout 全新 chrome + 56px icon rail + 移动端 drawer
- [x] Dashboard / AutoRouting / Keys / ModelCatalog / ModelDedup / Logs / Settings / Login 视图重构
- [x] Naive UI 全局主题覆盖到 v3 token
- [x] 三语言（zh-CN / en-US / ja-JP）i18n 完整覆盖
- [x] V3GroupSidebar / V3GroupDetail / V3SubGroupTable / V3Sparkline / V3NewGroupFlow / V3LogTable 组件
- [x] 删除 `BaseInfoCard.vue` / `NavBar.vue`（旧组件）

### P0
- [x] **P0-1** New Group 三步流程（`V3NewGroupFlow.vue`）
- [x] **P0-2** 隐藏 Mock 热力图，替换为 Provider line-up（真数据）
- [x] **P0-3** Rail keys 数徽章
- [x] **P0-4** 后端 `KeyCount` 字段 + `/groups/list` 字段扩展

### P1
- [x] **P1-5** Auto Routing 数据模型决策文档（推荐选项 A，等团队确认）
- [x] **P1-6** `GET /api/dashboard/top-models` API + 前端接入
- [x] **P1-7** Model Catalog 启发式 capability chips（tools / vision / ctx）

### P2
- [x] **P2-9** V3 LogTable + 详情模态
- [x] **P2-10** 自定义分组拖拽排序
- [x] **P2-11** Key Notes 内联编辑
- [x] **P2-12** Chrome Docs 按钮

## 📋 待办

### P2（剩余）
- [ ] ~~**P2-8** 7 个 Modal 模板深度 v3 改写~~ ✅ 已完成 `3d69011`
  - `KeyCreateDialog.vue` — paste textarea + step indicator
  - `KeyDeleteDialog.vue` — 二次确认布局
  - `GroupFormModal.vue` — 大量 form section，最难，约半天
  - `GroupCopyModal.vue` — radio + name input
  - `AggregateGroupModal.vue` — sub-group 选择器
  - `AddSubGroupModal.vue` — group + weight 列表
  - `EditSubGroupWeightModal.vue` — slider + number

### P3 锦上添花
- [ ] **P3-13** KPI sparkline + Top Models 趋势接真数据
  - 后端：`GET /dashboard/timeseries?bucket=1h&days=1`
  - 前端：替换 `fakeSpark` 数组
  - 估时：后端 1d + 前端 0.5d
- [ ] **P3-14** Chrome P50/P99 + Group Detail avg latency
  - 后端：聚合 `request_logs.duration_ms` 的 percentile（SQLite 用 NTILE）
  - 估时：后端 1d + 前端 0.5d
- [ ] **P3-15** Settings 改为固定 4 卡布局（按设计稿）
  - 把后端动态 categories 映射到 Authentication / Storage / Server / Health & limits 四组
  - 估时：半天
- [ ] **P3-16** Top Models channel filter 下拉
  - 前端按 channel 过滤已加载的 top models（不需要后端）
  - 估时：1 小时
- [ ] **P3-17** Copy AUTH_KEY 按钮 + 二次确认
  - Endpoint 卡里加按钮，confirm 后从 localStorage 复制
  - 估时：1 小时
- [ ] **P3-18** Login + Chrome 加 `go 1.24` 版本后缀
  - 后端：`GET /system/version` 返回 `{ go_version, app_version, commit }`
  - 估时：后端 0.5d + 前端 10 分钟

### 长线
- [x] **Model Routing 重写（决策已定，硬切）** —— 详细方案见 §13；实施进度：
  - [x] Phase 1：`model_aliases` GORM model + AliasService CRUD + AliasHandler + 路由注册 + reserved 别名 seed
  - [x] Phase 2：`internal/router_engine/` 包含 SWRR + 429 cooldown 选择器 + middleware；删 `internal/autoroute/` 包；删 `AutoRouteHandler`；移除 `AutoRoutingConfig` 字段；drop `auto_routing_config` 列
  - [x] Phase 3：新建 `V3AliasesView.vue`（别名管理 + 阈值卡 + 三个 reserved 锁定显示）+ `aliases.ts` API client；删 `AutoRouting.vue`；router 加 redirect；rail 切换到 `aliases`；i18n 三语言全套
  - [x] Phase 4：`selector_test.go` 覆盖 SWRR 分布 / 同权重优先级排序 / cooldown bump+reset / tier 选择
  - ⚠️ **待 user 重启 Go 后端验证编译** — Go 代码按现有 patterns 写，前端 vue-tsc + vite build 均通过
- [ ] **后端 Bell 通知系统**（事件流：key invalid / quota exhausted / upstream down）—— 2–3 天
- [ ] 跨 channel 协议翻译（OpenAI ↔ Anthropic ↔ Gemini）
- [ ] 用量配额、预算告警、按 Key 计费
- [ ] OAuth / SSO 登录

---

# §13 Model Routing 重写设计（已定案，硬切）

> **2026-04-26 决策**：废弃 `internal/autoroute/` + `group_mapping` 整套，改为
> **「别名表 + auto 关键字 + SWRR + 429 cooldown」三合一架构**。无弃用窗口，
> 直接 drop 旧表 / 删旧代码 / 删旧 UI。
>
> 该决策替代了原 P1-5 文档列出的选项 A/B/C，相当于"选项 D"。

## 13.1 路由决策树

```
请求 model 字段
    │
    ├─ "auto"
    │     └─ 估算 token → 选 tier (simple / medium / complex)
    │           └─ 查保留别名 auto-{tier} → 走 §13.4 SWRR 选择器
    │
    ├─ 命中 model_aliases.alias
    │     └─ 取所有匹配 (group, real_model) → 走 §13.4 SWRR 选择器
    │
    └─ 未命中别名
          └─ 跨所有 group 查 available_models 里的精确名 → 走 §13.4 SWRR 选择器
```

**保留别名（reserved）**：`auto-simple` / `auto-medium` / `auto-complex`
- UI 置顶 + 加锁，不能改名/删除，只能编辑成员
- 路由命中 `model="auto"` 必经此别名
- 别名为空 → 503 + 文案「请先在别名页配置 auto-{tier} 池」

## 13.2 数据模型

```sql
CREATE TABLE model_aliases (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    alias       VARCHAR(255) NOT NULL,        -- "gpt-4" / "auto-simple" / etc
    group_id    INTEGER NOT NULL,
    real_model  VARCHAR(255) NOT NULL,        -- group 暴露的真名 "qwen-3"
    weight      INTEGER NOT NULL DEFAULT 1,   -- SWRR 权重
    priority    INTEGER NOT NULL DEFAULT 100, -- 同权重时排序;数字越小越先
    enabled     BOOLEAN NOT NULL DEFAULT 1,
    is_reserved BOOLEAN NOT NULL DEFAULT 0,   -- auto-* 锁定行
    created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(alias, group_id, real_model),
    FOREIGN KEY (group_id) REFERENCES groups(id) ON DELETE CASCADE
);
CREATE INDEX idx_aliases_alias ON model_aliases(alias, enabled);
```

**Migration（硬切）**：
```sql
-- drop 旧表/列
DROP TABLE IF EXISTS auto_routing_config;        -- 整表删
-- 或者保留 thresholds：
-- ALTER TABLE auto_routing_config DROP COLUMN group_mapping;
```
保留 `simple_threshold` / `complex_threshold` / `enabled` 三字段（搬到新表 `routing_settings` 或留在原表去掉 mapping 列），其他字段砍掉。

**初始化时插入 3 个 reserved 行（empty 成员）**：
```sql
-- 这只是占位，让 UI 知道有这三个保留别名
INSERT INTO model_aliases (alias, group_id, real_model, is_reserved, enabled)
VALUES
  ('auto-simple',  0, '', 1, 1),
  ('auto-medium',  0, '', 1, 1),
  ('auto-complex', 0, '', 1, 1);
```
（实际成员通过新增 enabled=1 + group_id>0 的行表达；上面这 3 行作为"锁定占位"标记）

## 13.3 SWRR + 429 Cooldown 选择器

```go
// internal/router/selector.go

type Candidate struct {
    GroupID    uint
    RealModel  string
    Weight     int
    Priority   int
}

func PickCandidate(alias string) (Candidate, error) {
    cands := loadCandidates(alias)              // weight + priority + enabled
    alive := filterCooldown(cands)              // 跳过 cooldown 期内的 (group, model)
    if len(alive) == 0 {
        alive = cands                           // 全挂了就放开重试
    }
    return swrr(alive), nil                     // 平滑加权轮询
}

func MarkResponse(c Candidate, status int) {
    key := fmt.Sprintf("%d:%s", c.GroupID, c.RealModel)
    if status == 429 {
        cooldownStore.Set(key, expBackoff(key)) // 60s, 120s, 240s, 480s, 5min cap
    } else if status >= 200 && status < 400 {
        cooldownStore.Reset(key)                // 成功就清零退避计数
    }
}
```

- **Cooldown 粒度**：`(group_id, real_model)` —— 同 model 在不同 group 独立计数
- **指数退避**：60s 起 ×2，封顶 5 分钟，5 次连续 429 后清零并重试
- **存储**：内存 + Redis（如配置）。重启清空（简单优先）

## 13.4 API

| Method | Endpoint | 说明 |
|---|---|---|
| GET | `/api/aliases` | 全部别名（含 reserved） |
| GET | `/api/aliases/{alias}` | 单个别名 + 候选列表 |
| POST | `/api/aliases` | 新建别名行 `{alias, group_id, real_model, weight?, priority?}` |
| PUT | `/api/aliases/{id}` | 更新 weight / priority / enabled |
| DELETE | `/api/aliases/{id}` | 删除别名行（reserved 行 group_id=0 无法删，需先移除成员） |
| GET | `/api/routing/settings` | `{enabled, simple_threshold, complex_threshold}` |
| PUT | `/api/routing/settings` | 同上 |
| POST | `/api/routing/test` | 仍提供测试器，决策按 §13.1 跑 |

旧端点 `/api/auto-routing/*` **全部删除**，无重定向。

## 13.5 前端结构

| 视图 | 说明 |
|---|---|
| `/aliases` → `V3AliasesView.vue` | 别名管理主页（**新增 rail 入口**） |
| `/auto-routing` → 删 `AutoRouting.vue` | 改为指向 `/aliases?tab=auto` 的轻量 tab |
| 旧 `AutoRouting.vue` | 🗑️ 删除 |

`V3AliasesView.vue` 布局（参考设计稿 v3-keys-routing.jsx 的 ModelPickerDrawer）：
- 顶部：搜索 + status filter + 「+ New alias」按钮
- 主表：每行 = 一个 alias，展开后看候选模型 chip 列表（带权重条）
- reserved 三行 (auto-*) 置顶 + 加锁图标
- 编辑别名行 → 弹 drawer：选 group → 选 model → 设 weight + priority
- 顶部小卡：阈值轴 + enabled 开关（精简版 `V3RoutingThresholds.vue`）

## 13.6 完整工时

| Phase | 内容 | 工时 |
|---|---|---|
| 1 | `model_aliases` 表 + GORM model + migration（含 drop 旧表）+ CRUD API | 2 天 |
| 2 | `internal/router/` 选择器（SWRR + cooldown）+ middleware；删 `internal/autoroute/` | 2 天 |
| 3 | `V3AliasesView.vue` + `V3RoutingThresholds.vue`；删 `AutoRouting.vue` | 2 天 |
| 4 | 测试（Go test for selector + 路由集成 + 前端 e2e）+ V3_STATUS 关闭这一章 | 半天 |
| **合计** | | **~6.5 天** |

## 13.7 与设计稿（spec）的差异

| 项 | spec 提议 | 我们最终方案 | 理由 |
|---|---|---|---|
| Tier 命名 | `fast/standard/slow` | `simple/medium/complex` | 沿用现有项目语义 |
| `model_metadata` 表 | 新建 | ❌ 不建，复用 `Group.AvailableModels` | 避免双重模型注册表 |
| `key_model_bindings` 表 | 新建 | ❌ 不建，复用 `Group.proxy_keys` | 已有 key 隔离机制 |
| `model_aliases` 表 | 新建 | ✅ 新建（**spec 唯一被采纳的新表**） | 设计稿核心承载 |
| 优先级排序 | `priority asc` | `weight + priority`（同权重看 priority） | 同时支持加权轮询和强制顺序 |
| 错误处理 | 未定 | SWRR + 429 cooldown 指数退避 | 业界标准做法 |
| `auto` 关键字 | spec 提议 | ✅ 采纳，但用 reserved alias 实现 | 0 特殊代码路径 |

## 13.8 决策意见对外解释

- **为什么不渐进**：`group_mapping` 现在没有真实用户数据，"硬切"工作量 < "硬切 + 兼容层"工作量 / 2
- **为什么不三表**：spec 的 `model_metadata` + `key_model_bindings` 与现有 `Group` + `proxy_keys` 强重复，会造成双源 of truth
- **为什么 reserved alias 而不是单独 tier 池字段**：保持选择器代码路径单一，所有路由都走 SWRR

---

# 当前可立即用的功能 ✓

- 完整登录、主题切换、语言切换、Docs 跳转
- Dashboard 4 KPI（真数据）+ Top Models（真 per-model 调用量）+ Provider line-up + Endpoint 复制 + 24h 折线 + Refresh + Export JSON
- Groups & Keys 完整 CRUD + 拖拽排序 + Notes 内联编辑 + Get more keys 外链
- **New Group 三步引导流程**（10 个免费 provider + 一键打开 key 页 + 粘贴测试 + 异步导入）
- 聚合分组 V3SubGroupTable（权重条 + 三色 status）
- Auto Routing 阈值配置 + per-model→tier→group 映射 + 路由测试器
- Model Catalog 浏览 + tier/free/provider 三档过滤 + 启发式 tools/vision/ctx chips
- Model Dedup 建议 + 一键聚合
- **V3 Logs**（v3-log-row 网格 + 详情 modal + 多筛选 + 导出 CSV）
- Settings 完整配置
- 所有 modal 通过全局 Naive UI 主题继承 v3 配色
