# 视频异步任务队列 — 设计

- 日期: 2026-06-23
- 状态: 已确认设计, 待实现
- 关联: Playground 视频生成 (sendVideo)、keypool、store、P9 mesh 多实例

## 背景与问题

Playground 已支持视频生成 (agnes-video-v2.0), 但实测发现 agnes 的 `POST /v1/videos`
**名义异步、实为同步阻塞**: 它一直挂起到视频生成完才返回 (实测 6m22s, 含上游排队),
返回时 `status` 已是 `completed`、响应体直接带 `remixed_from_video_id` (视频 URL)。

当前前端按"异步立即返 task_id"实现, 导致:
1. POST 阻塞那几分钟界面空白无进度;
2. 前端 `await fetch(POST)` 把整个浏览器请求挂住, 5min 轮询窗口对慢视频不够 → 超时;
3. 关掉/刷新页面, 进行中的生成就"丢"了 (虽然 agnes 侧仍在跑)。

已做的临时修复 (POST 返回判 completed 直接用 + 进度提示 + 轮询窗口 5→15min) 让它能 work,
但根本问题是: **长耗时异步任务不该由前端同步等待**。本设计把视频生成改造成
**后端托管的异步任务队列**。

## 关键决策 (已与用户确认)

- **队列归属**: 后端持久化队列 + 后台 worker (真·后台, 关页面/刷新/重启都不丢, 可统一管理)。
- **资源落地**: 暂不下载, 直接存 agnes URL (实测 `expires_at: null`, URL 不过期; 下载到临时空间 YAGNI, 日后真遇到过期/防盗链再加)。
- **管理 UI**: 聊天内就地刷新 + 一个轻量队列面板。
- **任务操作**: 取消进行中、重试失败、删除记录。
- **刷新机制**: 前端每 30s 轮询 (仅在有进行中任务时)。
- **多实例协调**: 任务级租约 (非全局 leader)。
- **超时上限**: 15min。
- **历史清理**: 不主动清理, 靠手动删除 (后续可加 TTL)。

## 架构总览

```
前端 Playground          后端 video-task 子系统           agnes 上游
─────────────           ──────────────────────          ──────────
POST /api/video-tasks → [入队: pending] ──worker──→ POST /v1/videos (阻塞~6min)
  ← 立即返回 task_id                  │                    ↓ completed+url
聊天消息存 task_id                    └─ 更新 status/url   (或 queued→轮询GET)
30s 轮询 GET ←──────── [completed: video_url]
  → 消息自动变 <video>
```

核心: **worker 在后端扛 agnes 的阻塞 POST** (那 6 分钟由后端承担, 前端秒回),
完成后把 URL 写回任务表; 前端只负责发起 + 30s 轮询回填 + 队列面板管理。

## 数据模型 — 新表 `video_tasks`

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | string (主键) | 我们的 task_id (uuid) |
| `group_name` | string | 目标分组 |
| `model` | string | 模型名 |
| `prompt` | text | 提示词 |
| `params` | json | 发起参数 (num_frames, frame_rate 等) |
| `status` | string | `pending` / `running` / `completed` / `failed` / `canceled` |
| `upstream_task_id` | string | agnes 的 `task_70...` (异步路径填充) |
| `video_url` | string | 完成后的 agnes URL (不下载, 直接存) |
| `progress` | int | 0-100 |
| `error` | text | 失败信息 |
| `created_at` | time | 入队时间 |
| `started_at` | time | worker claim 并开始执行的时间 |
| `completed_at` | time | 终态时间 (completed/failed/canceled) |
| `lease_owner` | string | 当前持有该任务的实例标识 (多实例协调) |
| `lease_expires` | time | 租约过期时间 (实例崩溃后任务可被接管) |

索引: `status` (worker 取 pending)、`lease_expires` (回收过期租约)。

状态机:
```
pending ──claim──> running ──agnes completed──> completed
   │                  │
   │                  ├── agnes failed / 超时 / POST错误 ──> failed
   │                  │
   └── cancel ──> canceled <── cancel (running 时)
failed ──retry──> 新建一条 pending (复制 group/model/prompt/params)
```

## REST API

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/video-tasks` | body `{group, model, prompt, params}` → `{task_id, status}` (秒回) |
| GET | `/api/video-tasks?ids=a,b,c` | 批量查状态 (前端 30s 轮询用) |
| GET | `/api/video-tasks?status=&page=&page_size=` | 分页列表 (队列面板用) |
| GET | `/api/video-tasks/:id` | 单任务状态 |
| POST | `/api/video-tasks/:id/cancel` | 取消进行中任务 |
| POST | `/api/video-tasks/:id/retry` | 重试失败任务 (新建 pending) |
| DELETE | `/api/video-tasks/:id` | 删除任务记录 |

鉴权: 复用 Playground 现有的 auth_key 机制 (与 `/proxy` 一致), 不引入新的用户体系
(本项目为单人自部署网关, 任务表全局可见)。

## 后端 worker (关键)

### 任务获取 — 任务级租约 (而非全局 leader)
worker 轮询 pending 任务时, 用 store 锁原子地 `claim` 一个任务:
写入 `lease_owner` (本实例标识) + `lease_expires` (now + 租约时长, 如 20min)。
claim 成功才执行。

**为什么是任务级租约而非全局 leader**: P9 mesh 多实例部署下, 若两个实例同时
POST 同一任务, 会**重复生成视频 + 重复扣 key 额度**。任务级租约保证同一任务
只被一个实例执行; 且某实例崩溃时, 其租约到期后任务可被其它实例重新 claim,
比"单 leader 挂了全停"更鲁棒。

### 执行流程
1. claim 成功 → `status=running`, `started_at=now`。
2. keypool 取该 group 的可用 key。
3. POST agnes `/v1/videos` (阻塞):
   - 返回 `completed` + url → 直接写 `video_url`, `status=completed`。
   - 返回 `queued`/`in_progress` → 存 `upstream_task_id`, 进入 GET 轮询阶段。
4. GET 轮询阶段: 周期 GET agnes 任务状态, 更新 `progress`;
   `completed` → `video_url`; `failed` → `error`+`status=failed`。
5. 超时上限 15min → `status=failed` (error="timeout")。

### 并发
goroutine 池同时处理多个任务 (每个 agnes POST 阻塞 ~6min, 不并发会串行堵死)。
池大小可配置 (默认如 4)。

### 取消 / 重试 / 删除
- **取消**: 置 `status=canceled`。worker 执行循环中检测到 canceled 即停止 GET 轮询、
  不再回填。已发出的 agnes POST 无法撤回 (agnes 侧可能仍生成), 但我方不再消费结果。
- **重试**: 基于失败任务复制 group/model/prompt/params 新建一条 `pending` (原记录保留)。
- **删除**: 直接删行 (建议禁止删除 `running` 任务, 或删除即视为取消)。

### 租约续约
长任务执行期间, worker 周期性续 `lease_expires`, 避免执行中租约到期被别的实例抢走。

## 前端改造

### ChatMessage
新增字段 `videoTaskId?: string`, 随 localStorage 持久化 (刷新后仍能凭它恢复关联)。

### sendVideo 改造
从"阻塞 POST agnes + 前端轮询"改为:
1. `POST /api/video-tasks {group, model, prompt, params}` → 拿到 `task_id`。
2. 写入消息: `videoTaskId=task_id`, `content="已提交, 排队中…"`, `phase="generating"`。
3. 立即结束 (不再 await 生成)。

### 全局轮询 (回填)
Playground 顶层挂一个 30s `setInterval`:
1. 扫所有 session 的消息, 收集 `phase==="generating"` 且有 `videoTaskId` 的。
2. 若集合非空 → 批量 `GET /api/video-tasks?ids=...`。
3. 回填对应消息: `completed` → `content="![](url)"` (renderMarkdown 转 `<video>`)、
   `failed` → 错误文案、`progress` → 更新进度文案。
4. **仅在有进行中任务时轮询**; Page Visibility API: 后台标签页降频或暂停。

### 轻量队列面板
抽屉/弹窗形式:
- 调 `GET /api/video-tasks?status=&page=` 列出任务: status / prompt 摘要 / 模型 / 耗时。
- 行内操作: 取消 (running)、重试 (failed)、删除。
- 状态过滤 + 分页。

## 错误处理

| 场景 | 处理 |
|---|---|
| POST agnes 网络错误 | `status=failed` + error, 可重试 |
| key 耗尽 / 取不到 key | `status=failed` + error (或保持 pending 等待, 取保守的 failed 可重试) |
| agnes 返回 failed | `status=failed` + 上游 error |
| 超过 15min | `status=failed`, error="timeout" |
| 用户取消 running | `status=canceled`, worker 停止回填 |
| 实例崩溃 (执行中) | 租约到期后任务被其它实例重新 claim 执行 |
| 前端轮询单次失败 | 跳过本轮, 下个周期重试 (不影响后端任务) |

## 测试策略

- **状态机单测** (task_service 风格): pending→running→completed/failed/canceled 各转换、
  非法转换拒绝。
- **任务级租约并发安全** (对应 P9 约束): 模拟两实例同时 claim 同一任务, 断言只有一个成功;
  租约过期后可被接管。
- **worker 轮询逻辑** (mock agnes): 阻塞返 completed 路径、queued→GET 轮询路径、
  failed 路径、超时路径。
- **API handler 测试**: 各端点 + 鉴权。
- **前端回填逻辑**: 扫描+批量查+回填, 仅有 pending 时轮询, 可见性降频。

## 非目标 (YAGNI, 明确不做)

- 不下载视频到本地临时空间 / 不做静态文件 serve (URL 不过期)。
- 不做多用户 / 任务归属隔离 (单人自部署, 任务全局可见)。
- 不做历史任务自动清理 TTL (先手动删除)。
- 不做全局 leader 选举 (用任务级租约替代)。
- 图片生成暂不纳入队列 (图片是即时同步的, 无此问题)。
