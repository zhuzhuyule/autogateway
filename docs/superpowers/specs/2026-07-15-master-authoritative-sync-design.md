# 主从同步架构（Master-Authoritative Sync）设计

日期：2026-07-15
状态：设计待评审
作者：Pengfei + Claude

## 1. 背景与根因

当前 mesh 同步是**对称双向 P2P + LWW-per-record**：

- 每个节点都是权威，都能改、都能推、都能拉。
- 冲突解决靠 `effectiveTime = max(updatedAt, deletedAt)` 比时间戳（Last-Write-Wins）。
- 合并按业务键 `(group_name, key_hash)` 等做匹配。

这套机制在生产中反复产生**稳定的不一致（分裂态）**，v2.5.20~25 连发 6 轮补丁仍未根治。2026-07-15 定位到一个典型链条：

1. 历史遗留**重复 group**：`agnes` 同时存在墓碑记录（id=20）和活记录（id=24）。
2. 同一 API key 因此有多条记录，分属不同 `group_id`，但**共享 key_hash**。
3. v2.5.25 的 `V2_5_25_CleanupOrphanKeys` 迁移用 `deleted_at = now()` 软删"指向墓碑 group 的孤儿 key"，产生一个**"当前时刻的墓碑"**。
4. mesh 同步按 `(group_name, key_hash)` 匹配，**分不清 group 20 还是 24**。这个 now 墓碑经 `(agnes, <hash>)` 同步到其它节点，LWW（03:51 > 07-14）**覆盖了对方指向活 group 的合法 key**。
5. 结果：mini 上 `agnes`/`xfyun` 的活 key 全被误删；三节点活 key 数 59/47/68，永不收敛。

**根本病灶**：多主 + LWW + 业务键匹配 + 任何一处写 `now` 都可能变成"向全网广播删除"。补丁越多、写 now 的点越多，污染面越大。这是架构问题，不是又一个 bug。

## 2. 目标

- **根治一致性**：消除"分裂态"，让非 master 节点在同步范围内**恒等于 master**。
- **主从权威**：设唯一 master 为权威源；follower 镜像 master。
- **细粒度控制**：master 集中定义"哪些不同步"，默认全同步，master 新增配置自动纳入。
- **可选上行迁移**：follower 可手动、选择性地把本地变更迁移到 master。
- **止血合一**：新架构的第一次运行即完成三节点拉平，不需单独的一次性修复脚本。

### 非目标（YAGNI）

- 不做多 master / 选主 / 一致性协议（Raft 等）。单 master 足够。
- 不做 follower 各自定制的排除规则（排除规则集中在 master）。
- 不做字段级的可视化编辑 UI 的复杂形态；先给一个清单式配置。

## 3. 角色模型

复用已存在的进程级标识 `IS_MASTER`（`ServerConfig.IsMaster`，环境变量）。

| 角色 | IS_MASTER | 行为 |
|---|---|---|
| **master** | true | 唯一权威源。向 follower 提供全量快照；接收 follower 的**手动迁移** push 并合并进权威；**不**自动 pull/接收 follower 的自动同步。 |
| **follower** | false（默认） | 配置一个指向 master 的 `SyncPeer`；定期 pull master 全量快照并**镜像替换**本地；默认**不自动**外推；支持手动迁移 push 给 master。 |

- master = **HK VPS**（公网、常在线、当前数据最健康）。
- follower = 本地开发机、mini（及未来任意新增节点）。

`SyncPeer.Role`（'server'/'client'）保持不变，仅表示 WS 连接方向，与 master/follower 正交。

## 4. 同步机制

### 4.1 master → follower：全量快照镜像（废弃 LWW）

follower 侧把现有 `pullOnePeer → ProcessPayload(LWW)` 换成 `pullOnePeer → ApplySnapshot(mirror)`：

1. follower GET master `/api/sync/pull`（master 返回**全量快照**，不再按 since 增量；含同步策略，见 §5）。
2. follower 对每个**在同步范围内**的类别，用 master 快照**镜像替换**本地：
   - master 有、follower 无 → 新建；
   - master 有、follower 有 → 用 master 覆盖（**排除字段除外**，见 §5）；
   - master 无、follower 有 → **软删**（镜像语义：本地多出来的、master 不认的记录删掉）。
3. 不比较任何时间戳。master 即真值。分裂态在定义上不可能出现。

数据量级只有几十条 group/key，全量快照无带宽压力。master→follower 也可保留 WS push 做即时下发（可选优化；pull 兜底已足够）。

**镜像删除的边界**：仅对"同步范围内的类别"执行"master 无则删"。被排除的类别 / 字段、以及 follower 本地自治的东西，不参与镜像删除。

### 4.2 follower → master：手动选择性迁移

复用现有 `PreviewPushPayload` + `PushPeer`：

1. follower 用户在 UI 预览本地相对 master 的差异，勾选要迁移的记录。
2. push 给 master；master 侧 `ProcessPayload` 以 **upsert 语义**接受（把这些记录并入 master 权威；不因 follower 未发的记录而删 master 的东西）。
3. master 下一轮快照把结果分发给所有 follower。

默认 follower 不自动 push（`pushLoop` 在 follower 角色下不因本地变更自动触发；仅手动迁移时调用）。

## 5. 细粒度控制：master 集中排除清单

master 维护一份**同步策略（sync policy）**，定义"哪些不同步"。默认空（= 全同步）；master 新增任何字段/类别，因不在排除清单中，**默认自动同步**。

### 5.1 存储

新增一个 hidden 的 `SystemSetting` 项 `sync_policy`（JSON）。仅 master 有意义，随快照下发给 follower 执行。

```json
{
  "excludedCategories": [],
  "excludedFields": {
    "group":   ["proxy_url"],
    "setting": ["app_url", "proxy_url", "sync_enabled", "sync_key"]
  }
}
```

- `excludedCategories`：整类不同步（取值 `group` / `subgroup` / `key` / `alias` / `setting`）。
- `excludedFields`：某类别里的某些字段不同步（follower 保留本地值）。
- **默认预置本机专属字段**：`group.proxy_url`、`setting.app_url` / `proxy_url` / `sync_*`。这些每机不同，必须本地自治。用户可在 master UI 增删。

### 5.2 应用

- **导出侧（master）**：ExportSnapshot 时不裁剪（快照仍是全量），把 policy 一并带上，由 follower 执行——保证 follower 拿到全量真值 + 规则，规则变更即时生效。
- **合并侧（follower）**：ApplySnapshot 时按 policy：
  - `excludedCategories` 命中的类别整体跳过（不覆盖、不镜像删除）。
  - `excludedFields` 命中的字段：用本地值覆盖 master 值后再落库（等价"保留本地"）。扩展现有 `preserveLocalProxyURL` 为通用的 `preserveExcludedFields`。

## 6. 组件设计

| 组件 | 变更 | 职责 |
|---|---|---|
| `SyncService.ExportSnapshot`（新） | 新增 | 全量导出五类实体 + sync_policy。master 用。 |
| `SyncService.ApplySnapshot`（新） | 新增 | 镜像替换：按 policy 覆盖/新建/软删。follower 用。取代 follower 路径上的 `ProcessPayload`。 |
| `SyncService.ProcessPayload` | 保留 | 仅 master 接收 follower 手动迁移 push 时用（upsert 语义，不镜像删除）。 |
| `SyncPeerManager.pullOnePeer` | 改 | follower：调 ApplySnapshot 而非 ProcessPayload。 |
| `SyncPeerManager.pushLoop` | 改 | follower 角色下不因本地变更自动触发；master 角色下向 follower 广播快照（可选）。 |
| `syncPolicy`（新） | 新增 | policy 的读写 + 默认预置 + 字段裁剪 helper。 |
| `V2_5_25` 迁移 | 移除 | 有害的 now 墓碑源头。follower 靠 §7 镜像修复存量污染。 |

保持文件聚焦：新逻辑放独立文件（如 `sync_snapshot.go`、`sync_policy.go`），不再把 `sync_service.go` 继续堆大。

## 7. 止血 / 迁移路径

新架构第一次运行即止血。步骤：

1. 发版（含移除 V2_5_25、新增 snapshot/policy、角色分支）。
2. **确立 master 黄金数据**：HK 作 master。核对 HK 的 `agnes`/`xfyun` 等是否正确；若本地更全，用一次 follower→master 手动迁移把本地正确数据并入 HK。
3. HK 置 `IS_MASTER=true`；本地、mini 置 `IS_MASTER=false` 并各配一个指向 HK 的 peer。
4. follower 首次 pull → 全量镜像 HK → 三节点在同步类别上恒等于 HK，污染墓碑被镜像覆盖清除。
5. 验证：三节点活 key 数一致、`agnes`/`xfyun` 恢复、无孤儿。

## 8. 兼容性与过渡

- **旧 peer**：过渡期 master 仍能接收旧版 follower 的 LWW push（ProcessPayload 保留）。建议同批升级三节点，避免混合模式。
- **schema hash 握手**：sync_policy 属新增 SystemSetting，纳入 schema hash；同批升级即可。
- **回滚**：主从逻辑以 `IS_MASTER` 分支，回滚为全 false 时退化为"只 pull 不镜像删除"的保守行为（需保留一个 feature 开关，默认开）。

## 9. 测试策略

- **单测**：
  - ApplySnapshot 镜像三情形（新建 / 覆盖 / 多余软删）。
  - policy：excludedCategories 整类跳过；excludedFields 保留本地（proxy_url 不被覆盖）。
  - 分裂态复现→镜像后收敛（构造"本地活、master 墓碑"→镜像后本地随 master 删；反之随 master 活）。
  - master 接收迁移 push 为 upsert，不误删 master 数据。
- **集成**：三节点脚本模拟 pull-mirror 一轮后活 key 数一致。
- **回归**：现有 `sync_service_test.go` / `sync_apikey_merge_test.go` 中 LWW 相关用例，迁移到"master 接收迁移 push"语义或标注为过渡期路径。

## 10. 开放问题

- master→follower 是否需要 WS 即时 push，还是纯 pull（1min）够用？倾向先纯 pull，简单；后续按需加。
- follower 本地未迁移的新增记录会被镜像删除——是否需要在 UI 明确提示"本地改动需手动迁移否则会被 master 覆盖"？倾向加一个提示。
