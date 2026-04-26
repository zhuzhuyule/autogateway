# V3 Mission Console — 实现状态与待办

> 对照设计源文件（`AutoGateway Admin v3.html` + `v3-shell.jsx` +
> `v3-keys-routing.jsx` + `styles-v3.css`，handoff 包归档于
> `/tmp/agdesign/myapi/`）逐项审计的差异清单。
>
> 状态约定：✅ 已实现 / 🟡 部分实现 / 🔶 Mock 数据 / ❌ 未实现 / 🔧 因后端
> 限制改造

---

## 1. 顶部 Chrome（Mission Console）

| 设计元素 | 状态 | 说明 |
|---|---|---|
| 品牌 + logo + `v0.4 · go 1.24` | 🟡 | 缺 `· go 1.24` 后缀 |
| Live 脉冲点 + `9/10` keys | 🟡 | 用真 key_count，但没有 "N/M" 形式（缺总配额） |
| Req/s | ✅ | 真值（来自 `/dashboard/stats` 的 rpm） |
| P50/P99 延迟 | ❌ | 后端无 latency 直方图 API |
| Err % | ✅ | 真值 |
| Up 时间 (`47d`) | ❌ | 后端无 uptime API |
| 端点徽标（点击复制） | ✅ | `window.location.host`，可点击复制 |
| Bell 通知按钮 | ❌ | 未实现 |
| Docs 按钮 | ❌ | 未实现 |
| Theme + Lang + Logout | ✅ | 设计稿没有，我们额外加的 |

---

## 2. 左侧 Icon Rail

| 设计元素 | 状态 | 说明 |
|---|---|---|
| 5 个主项 + 底部 Settings | 🔧 | 我们有 6 个：多了 `model-dedup`（真后端功能） |
| `keys` 项右上角 count 徽章 | ❌ | 设计有，我们没接（`GROUPS.filter(g=>!g.system).length`） |
| Hover tooltip | ✅ | `data-tip` |

---

## 3. Dashboard

### 3.1 KPI 卡片
| 设计 | 状态 | 说明 |
|---|---|---|
| Requests · 24h（含趋势） | ✅ | 真值 |
| **Active models** (`22 across 9 providers`) | 🔧 | 改成了 RPM · 10m，因为后端没有"活跃模型数"统计 |
| Active keys（含 invalid 数） | ✅ | 真值 |
| Error rate | ✅ | 真值 |
| Sparkline 折线图 | 🔶 | 静态假数据 `[4,6,5,8,12,9,11,14]` 等，后端无小时桶 API |

### 3.2 Top Models · 24h
| 设计 | 状态 | 说明 |
|---|---|---|
| 模型列表 + 排名 + tier chip | 🟡 | 用 `getGroupList` × `getGroupStats` 推算（按 group calls 平均分到 group 暴露的 models） |
| latency / err 副信息 | ❌ | 没有 per-model latency 数据，已隐藏 |
| Provider chip 堆叠 | ✅ | 通过 group name 反查 provider |
| 调用量 + 占比条 | 🟡 | 用 group 24h 总请求按模型平均分，**不是真 per-model 请求计数** |
| 趋势 sparkline | 🔶 | 静态 `[1,2,2,3,4,4,5,6]` |
| Channel filter 下拉 | ❌ | 未实现（需要后端按 channel 过滤） |
| `View catalog →` 跳转 | ✅ | 路由跳到 model-catalog |

### 3.3 Provider activity · 24h（热力图）
| 设计 | 状态 | 说明 |
|---|---|---|
| 6 行 × 48 格半小时桶 | 🔶 | **完全 Mock**：基于 provider 名 seed 生成确定性热度，不是真活动 |
| 红色 incident 格 | 🔶 | Hard-code 给 groq 的 31 号桶标红 |
| 图例 less / more / incident | ✅ | 静态展示 |
| **后端缺**：30 分钟桶的 per-provider 请求量/错误时间序列 API | | |

### 3.4 Endpoint quick-paste
| 设计 | 状态 |
|---|---|
| 3 个端点（openai / anthropic / gemini） | ✅ 用真 host |
| Copy AUTH_KEY 按钮 | ❌ 未实现（怕泄露） |
| 单 endpoint 复制 | ✅ |

### 3.5 Refresh / Export
| 设计 | 状态 |
|---|---|
| Refresh 按钮 | ✅ 真重拉 API |
| Export 24h | ✅ 下载 JSON snapshot（设计稿没说格式，我们选 JSON） |

### 3.6 24h 请求量折线图
| 状态：✅ 沿用旧 `LineChart.vue`，接 `/dashboard/chart` 真数据 |

---

## 4. Groups & Keys

### 4.1 左侧 Group Sidebar
| 设计 | 状态 |
|---|---|
| Search | ✅ |
| System aggregates 段 + lock icon | 🟡 用 `sys` chip 替代 lock icon |
| Custom groups 段 | ✅ |
| 行尾显示 key 数量 | 🟡 用 `api_keys.length`，**列表 API 不返回此字段，所以总是空** |
| 拖拽排序（旧 GroupList 有） | ❌ V3GroupSidebar 移除了 |
| 底部 New group / New aggregate 按钮 | ✅ |

### 4.2 Group Detail（标准分组）
| 设计 | 状态 |
|---|---|
| Avatar + display + lock chip | ✅ |
| channel chip + POST 端点 + Copy | ✅ |
| Get more keys 外链 | ✅ 仅匹配 `FREE_PROVIDERS` 时显示 |
| Edit / Copy / Delete 按钮 | ✅ |
| **4-cell stats**：Keys / 1h req / 24h req / Avg latency | 🟡 我们是 Keys / 24h / 7d / 30d req（**没有 1h，没有 avg latency，因为后端 stats API 只给 24h/7d/30d**） |
| Toolbar：Add key / Test all / Remove invalid / status filter / search | ✅ |
| Key 表格：mask / Status / 24h req / Failure 条 / Last used / actions | ✅ |
| Notes 内联编辑（旧 KeyTable 有） | ❌ V3GroupDetail 仅展示 |
| Pagination | ✅ |

### 4.3 ❌ New Group 三步流程（设计稿核心交互之一，**完全未实现**）
设计稿 `v3-keys-routing.jsx:NewGroupFlow`：

1. **Step indicator**（Pick provider → Get API key → Paste & activate）
2. **GetKey 高亮卡**（蓝色 callout）：选完 provider 后显示 `Get a free key from {Provider}` + 大按钮 `Open {Provider} →`，外链跳转
3. **Paste textarea**：粘贴 key（多行/逗号分隔），自动统计 N keys + Test only / Test & save 按钮
4. **Provider catalog grid**：10 个 provider 卡片（含 free tier 文案、host、`fast`/`high quota`/`multi-model` badge），每张卡有 `Use this` 和 `Get key` 双按钮

**当前实现**：复用旧 `GroupFormModal`（一个庞大的统一表单），完全没有引导式分步。

**用户原话**："注意添加 group 后，group 内容中体现获取 api key 的打开操作" — 这是用户明确强调的需求，**目前缺**。

### 4.4 聚合分组 Sub-group Table
| 设计 | 状态 |
|---|---|
| 工具栏：Add sub-group / search / status filter | ✅ |
| 表格：avatar + display + 权重条 + active/invalid + status | ✅ |
| 状态：active/disabled/unavailable 三色权重条 | ✅ |

---

## 5. Auto Routing — **最大的设计偏离**

### 5.1 顶部
| 设计 | 状态 |
|---|---|
| ROUTING ACTIVE/PASSTHROUGH 状态指示 | ✅ |
| Disable / Enable 按钮 | ✅ |
| Refresh | ✅ |

### 5.2 Threshold 卡
| 设计 | 状态 |
|---|---|
| 三档预设按钮（economy/balanced/performance） | ✅ |
| 0–20k token 可视化轴 + 双 stop 圆点 | ✅ |
| 数字输入备份 | ✅ 我们多加的 |

### 5.3 ❌ 三 Tier 卡片（**核心设计偏离**）

**设计稿的模型**：每个 tier 是一个 **fallback chain**，里面是 `[{provider, model}, ...]` 列表，第一个 primary，后面 fallback。可拖排序，可点 × 移除，点 `+ Add model` 弹抽屉。

**实际后端的模型**：`group_mapping: Record<modelName, { simple_group, medium_group, complex_group }>` —— 每个 client model 名单独配三档对应的**分组**。**不是 tier→model 列表，而是 model→tier→group**。

**当前实现**：每个 tier 卡里展示**所有已映射 model**（不是该 tier 的 model 链），每行是一个 `<NSelect>` 让你为该 model + 该 tier 选 group。语义和设计稿完全不同。

| 设计要素 | 状态 |
|---|---|
| Tier band + 编号 icon + title + rule | ✅ 视觉对了 |
| Model chain 列表 | ❌ 改成了 model 列表 + 三个 select |
| 拖拽 grip + 优先级排序 | ❌ |
| `+ Add model to {tier}` 按钮 | ❌ |
| 每 tier 底部 traffic % / avg ms / err % | ❌ 后端没有 per-tier 统计 |

### 5.4 ❌ 模型选择抽屉 `ModelPickerDrawer`（**完全未实现**）
设计稿的右滑抽屉：
- 顶部 tier 色条 + 标题
- Search input
- 6 个 filter pills：All N / Matches {tier} ✓ / Free / Fast / Tools / Vision
- 模型按 provider 分组列表
- 每行：provider avatar + model id + ctx/speed/price/tools/vision 元信息 + tier chip + 点击 / + 按钮加入 chain

**未实现的根本原因**：后端数据模型不支持 per-tier model list。要做这个抽屉先要后端加一组 API，把 `simple_models[] / medium_models[] / complex_models[]` 作为新的配置存储。

### 5.5 Route Tester
| 设计 | 状态 |
|---|---|
| 请求体 code box | ✅ |
| Run test 按钮 | ✅ 接 `/auto-routing/test` |
| Decision：Routed tier / Estimated tokens / Tool count / Has vision | ✅ |
| **Try order**（按 chain 顺序展示 fallback） | ❌ 没有 chain 概念 |
| **Reason** 文案（如 `tokens > N → complex`） | ❌ |

---

## 6. Model Catalog

| 设计 | 状态 |
|---|---|
| viewhead + search + Re-discover | 🟡 用 Refresh |
| h1 entries 计数 + `/openai/v1/models` 路径 | 🟡 缺路径展示 |
| Provider 头像 | ✅ |
| 模型名 + tier chip | ✅ |
| **tools / vision 能力 chip** | ❌ 后端 `/api/models` 不返回这些 |
| Provider name + host | 🟡 我们展示 owned_by + groups |
| **Spec：ctx + speed** | ❌ 后端没有 |
| **Price / 1M（FREE for $0）** | ❌ 后端没有 pricing 数据 |
| Tier filter pills | ✅ |
| Free only 切换 | ✅ |
| Provider filter | ✅ |

---

## 7. Logs

| 设计 | 状态 |
|---|---|
| viewhead 简化筛选 (All groups / All status / search) | 🔧 沿用旧 `LogTable.vue`（功能更丰富：parent_group / key / model / date range / error_contains / request_type 全部过滤齐） |
| h1 + "live tail · N of total" meta | 🟡 显示 "live tail" |
| 日志行布局：time / status / method / ms / path · model · group / tier | 🔧 旧表格用 NDataTable + 自定义列管理 + 详情 modal，未按设计行布局重写 |

> 旧 `LogTable.vue` 1300+ 行业务逻辑（filter / pagination / 列管理 / 详情查看 /
> stream 标记）功能远超设计稿，重写性价比低。视觉通过全局 Naive UI v3
> 主题继承。

---

## 8. Settings

| 设计 | 状态 |
|---|---|
| 4 张固定分类卡（Authentication / Storage / Server / Health & limits） | 🔧 用真后端返回的 categories（动态数量、动态字段） |
| 行：label + sub + value | ✅ |
| **Rotate key 按钮** | ❌ 后端无 rotate API |
| **Allow management UI 开关** | 🟡 如果后端配置里有就显示，否则没有 |
| AES-256-GCM 加密标识 | ❌ 静态文案 |

---

## 9. Login

| 设计 | 状态 | 说明 |
|---|---|---|
| 设计稿无登录页 | — | 我们额外做的，使用 v3 风格 |

---

## 10. Modals（KeyCreate / GroupForm / 等 7 个）

| 状态 | 说明 |
|---|---|
| 视觉 | 🟡 通过 `design-v3.css` 里 `.n-modal .n-card` 全局 override 拿到 v3 head/body/footer 间距、边框、surface-2 footer |
| 内部 markup | 未重写（NCard + NSpace + NForm 默认布局） |
| 业务逻辑 | ✅ 完整保留 |

要做"像素级 v3"还需要逐个改 template，工作量约 8000 行模板的修订。

---

# Mock 数据清单

按"假到什么程度"排序：

| Mock 项 | 程度 | 影响 | 修复路径 |
|---|---|---|---|
| Dashboard Provider activity 热力图 | 🔴 完全假 | 视觉装饰，会误导 | 后端加 `/dashboard/activity` 30min×provider 桶 |
| Dashboard KPI sparkline | 🔴 完全假 | 仅装饰小线条 | 后端加 hourly bucket，或用现有 `/dashboard/chart` 数据 |
| Dashboard Top Models 调用量 | 🟡 推算 | 数字不准（按 model 数平均分） | 后端加 per-model 请求计数 |
| Dashboard Top Models 趋势 sparkline | 🔴 完全假 | 同上 | 同上 |
| Chrome Live `N/M` keys | 🟡 缺 M | 显示 keys 总数，没有"配额" | 没有"配额"概念可言，去掉 /M 即可 |
| Group Sidebar 行尾 key 数 | 🟡 永远 0 | groups list API 不返回 api_keys | 后端在 list API 加 key_count，或前端为每个 group 拉 stats（贵） |
| Auto Routing tier footer 流量% / latency / err | ❌ 未做 | 设计稿有，我们没显示 | 后端加 per-tier 统计 |

---

# Roadmap — 剩余应该做的事

按价值 × 工作量排序。

## P0 — 用户已明确需求 / 视觉破口

1. **Group 创建三步流程**（用户原话点名）
   - 工作量：1–2 天
   - 改动：新建 `V3NewGroupFlow.vue`，复用 `keysApi.createGroup`
   - 替代当前的 `GroupFormModal` 入口（保留作 Edit 路径）
   - 包含：step indicator + provider catalog grid + GetKey callout + paste & test

2. **删除 Mock 热力图 / 改为真活动数据**
   - 选项 A（快）：直接隐藏 Provider activity 卡，去掉误导
   - 选项 B（正路）：后端加 `/dashboard/activity` 30min 桶，前端接

3. **Group Sidebar 行尾 key 数**
   - 后端在 `getGroups()` 返回里加 `key_count` 字段（聚合时算 sub_groups 总和）
   - 前端把 `g.api_keys?.length` 换成 `g.key_count`

4. **Rail keys 项的红色 count 徽章**
   - 已有数据，加个 `<span class="v3-rail__count">` 即可（设计 css 已支持）

## P1 — 设计稿核心交互缺失

5. **Auto Routing 重新设计数据模型**
   - 决定：要么改后端配置存储为 per-tier model list（`{simple_models[], medium_models[], complex_models[]}`），实现设计稿的 chain + drawer
   - 要么放弃设计稿的 chain 模型，把当前实现讲清楚（per-model→tier→group）
   - 工作量：前者 3–5 天（含后端），后者只是文案 + 移除占位

6. **Top Models 接真 per-model 调用数**
   - 后端加 `/dashboard/top-models?window=24h` API 返回 `[{model, calls, providers, avg_ms, err}]`
   - 前端去掉 group 分摊推算逻辑

7. **Model Catalog 字段补齐**
   - 后端 `/api/models` 返回里加 `tools` / `vision` / `context` / `pricing_per_1m` 等元信息
   - 前端展示 spec + price 列，符合设计稿

## P2 — 视觉/体验完善

8. **Modal templates 全量 v3 改写**
   - 7 个 modal 内部 markup 改成 v3-card head/body/footer 模式
   - 估时 1 天

9. **LogTable 行布局对齐设计稿**
   - 把 NDataTable 换成 v3-log-row 网格
   - 估时 1–2 天（需要保留所有筛选/分页/列管理）

10. **Group 列表拖拽排序**
    - 旧 `GroupList` 有，`V3GroupSidebar` 砍了。补回来
    - 估时 半天

11. **Key Notes 内联编辑**
    - V3GroupDetail 当前只展示，旧 KeyTable 有 hover 编辑
    - 估时 半天

12. **Chrome Bell + Docs 按钮**
    - Bell：通知系统（需要后端事件流，暂不做）
    - Docs：外链到 README，半天

## P3 — 锦上添花

13. KPI sparkline 接 `/dashboard/chart` 数据
14. KV stats 加上 P50/P99 latency（后端 prometheus 集成）
15. Settings 4 卡固定分组（按设计稿强制布局，覆盖动态 categories）
16. Top Models channel 过滤下拉
17. Copy AUTH_KEY 按钮（带二次确认避免泄露）
18. Login 页 brand 加 `· go 1.24` 后缀（需要后端 version API）

---

# 后端工作清单

前端能做的有限，最大瓶颈是后端缺以下 API：

```
GET  /dashboard/activity?window=24h   → 30min × provider 桶（修复热力图）
GET  /dashboard/top-models?window=24h → 真 per-model 调用统计
GET  /dashboard/timeseries            → KPI sparkline 数据
GET  /dashboard/latency               → P50/P99（chrome 顶栏）
GET  /system/uptime                   → up 时间
GET  /system/version                  → go 版本

groups list API 加字段：key_count, latency_p50, latency_p99
models list API 加字段：tools, vision, context, pricing_per_1m

auto-routing 配置 schema 重设计：从 model_mapping → tier_models（如果走设计稿的 chain 路）
```

---

# 当前可立即用的功能 ✓

- 完整登录、主题切换、语言切换
- Dashboard 4 KPI（真数据）+ Endpoint 复制 + 24h 请求量折线
- Groups & Keys 完整 CRUD（通过沿用的 modals）
- 聚合分组管理（V3SubGroupTable）
- Auto Routing 阈值配置 + per-model→tier→group 映射 + 路由测试
- Model Catalog 浏览 + 过滤
- Model Dedup 建议 + 一键聚合
- Logs 完整查看 + 详情
- Settings 完整配置
