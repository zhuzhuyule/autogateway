# 邀请制加入 + 网状发现（Invite-Based Mesh Join）设计

日期：2026-07-15
状态：设计待评审
作者：Pengfei + Claude
前置：建立在 [主从同步架构](2026-07-15-master-authoritative-sync-design.md) 之上

## 1. 背景与目标

当前主从同步（v2.6.0）组网靠**手动**:每加一个节点,要在两台机器上互相填 URL + 同一把 sync_key,再手动"设为主站"。节点多了很繁琐,且 per-peer key 要人肉约定。

**目标**:让加入像"扫码入群"一样简单,同时不牺牲安全:

1. **邀请链接**:节点(主站为主)生成一个带一次性 token 的链接。
2. **一键加入**:被邀请方用链接即可加入 —— 自动交换 URL/公钥/per-peer key、自动指向 master、自动发现全网其它节点。
3. **一次性**:token 用一次即失效,绑定这一次加入。
4. **内容共享**:加入后配置/密钥等自动同步,本机专属字段(proxy_url 等)仍各自保留(沿用现有 sync_policy)。
5. **可传播**:任意节点都能分享邀请;新节点从任意入口加入后都归并到同一个 master、被全网认识。

### 非目标（YAGNI）

- 不做完全去中心化的多 master / 自动选主(仍是单 master 权威, 见主从 spec)。
- 不做 NAT 打洞 / 中继(依赖 master 公网可达, 见 §7)。
- 不做子站之间的直连网状(星形足够, 子站只连 master)。

## 2. 核心概念

| 概念 | 说明 |
|---|---|
| **邀请 token** | 一次性随机凭证, 由某节点签发, 存本地, 带过期 + used 标记。 |
| **邀请链接** | `{issuer_url}/#/join?token={code}` —— 自包含 issuer 地址 + token。 |
| **join 握手** | 被邀方 POST `/api/sync/join`, 交换 URL/公钥, issuer 分配 per-peer key。 |
| **gossip 快照** | master 下发全量快照时**附带 peer 目录**(URL+公钥+指纹+is_master), follower 据此发现全网 + 认出 master。 |
| **master 传播** | peer 目录里带 `is_master` 标记, 新节点自动把它设为镜像源。 |

## 3. 加入流程（MVP：master 中心化 join）

**关键简化决策**:一次性 token 由 **master 签发**;任何节点(包括子站)都能**分享**这条链接,但链接始终指向 **master**,被邀方**直接 join master**。这样避免了"子站签发→主站信任→跨节点分发 key"的复杂链条,同时完全满足"连任意入口即入网、归并到 master"的诉求(子站只是分享二维码的人)。

```
① master A 在 UI 点「生成邀请链接」
   → A 本地存一条 invite_token(code, expires_at, used=false)
   → 链接: https://A.example.com/#/join?token=CODE   (A 必须是被邀方可达的地址, 见 §7)

② 新节点 N (或子站转发这条链接给 N) 打开链接 → UI 填好后 POST A/api/sync/join
   body: { token: CODE, my_url, my_public_key, my_fingerprint, my_name }

③ A 校验 token: 存在 && 未过期 && used=false; 否则 4xx
   A 生成一把 per-peer sync_key K (random)
   A 写 sync_peers: N(url, pubkey, sync_key=K, is_master=false)
   A 标记 token used=true, used_by=N.fingerprint
   A 返回(用 N 的公钥加密敏感部分):
     { sync_key: K,                       # N↔A 的 per-peer 钥匙
       master: {url, public_key, name},   # 谁是 master (就是 A 自己)
       peers: [ {url, public_key, fingerprint, is_master} ... ] }  # 全网目录

④ N 落库: sync_peers += A(url, pubkey, sync_key=K, is_master=true)
   N 设本机角色 = follower (node_is_slave=true)
   N 合并 peers 目录(去重, 见 §8) → connectionLoop 自动 dial 换公钥
   N 立即作 follower 镜像 A, 且认识全网

⑤ A 侧: N 已在 A 的 peer 目录里 → 下一轮下发快照时, 其它 follower 的 gossip 目录也带上 N
   → 全网都认识 N
```

**"子站邀请"如何满足**:子站 B 想邀 N,有两种都可用:
- **(推荐)** B 向 master A 请求一条邀请链接(B 已是网络成员, A 信任 B 的请求), 把链接转给 N。N join A。
- 或 B 直接把自己手里的 master 邀请链接转发给 N。
两种都让 N **直接 join master**, master 自然认识 N。无需跨节点分发 key。

## 4. 数据模型

**新表 `invite_tokens`**(本机, 不同步):
```go
type InviteToken struct {
    Code      string    `gorm:"primaryKey"` // 随机, URL-safe
    ExpiresAt time.Time `gorm:"index"`
    Used      bool      `gorm:"default:false"`
    UsedBy    string    // 被邀方指纹, 审计用
    UsedAt    *time.Time
    CreatedAt time.Time
}
```

**复用 `SyncPeer`**(已有 URL / SyncKey / PublicKeyX25519 / PinnedFingerprint / IsMaster)。join 落库直接填这些字段, 不新增列。

## 5. 鉴权与密钥交换

- **每对节点一把 per-peer sync_key**(维持你定的安全底线, 不搞全网共享)。
- key 由 **master 在 join 时生成**, 并用**被邀方公钥加密**回传(nacl/box, 复用 `EncryptPayloadFor`)。链路上不出现明文 key。
- token 本身是准入凭证 + 建立首个可信信道的一次性 secret; 用完即焚。
- 后续所有 pull/ws 沿用现有 per-peer 鉴权(`sync_peers.sync_key`)不变。
- 公钥交换沿用现有 ws 握手(hello 带 `public_key`, welcome 回 `my_public_key`); join 响应先带一份, 加速首次加密。

## 6. Gossip Peer 发现 + Master 传播

**最小侵入做法**:不新增独立 gossip 协议, 把 peer 目录**搭在现有 pull 快照上**。

- master 的 `ExportSnapshot` 额外带一个 `peers` 目录字段(URL+公钥+指纹+is_master, **不含 sync_key**)。
- follower `ApplySnapshot` 时:
  - 对目录里**本地没有**的 peer → 新增 `sync_peers`(URL+公钥, sync_key 暂空);
  - `is_master` 的那个 → 设为本地镜像源(若与当前不一致按 §8 仲裁);
  - connectionLoop 自动 dial 新 peer 换公钥。
- **sync_key 不进 gossip 目录**(安全): follower 发现新 peer 但没有它的 key —— 星形拓扑下 follower **只需要 master 的 key**(join 时已拿到), 不需要和其它 follower 直连, 所以缺 key 无碍。目录仅用于"认识全网 + 认出 master"。

> 若将来要子站互连(网状冗余), 才需要 gossip 加密分发 per-peer key —— 列为进阶, 本 spec 不做。

## 7. 网络前提（内网/外网）

**硬约束:master 必须是所有 follower 可达的地址(通常公网域名/IP)。** 因为同步永远是 **follower 主动出站**:

- follower → master:HTTP pull(出站) + ws dial 握手(出站);
- master **从不主动回连** follower(follower 忽略 ws push、只自己 pull)。

因此:

| 拓扑 | 可行 |
|---|---|
| master 公网 + follower 任意 NAT/内网 | ✅(现状 HK+本地/mini 即是) |
| master 内网 + follower 跨网 | ❌ 需端口映射/打洞(非目标) |
| follower ↔ follower 直连 | ⚠️ NAT 下不可靠, 星形不需要 |

**邀请链接里的 issuer/master URL 必须填公网可达地址**。UI 生成链接时用 `app_url`(本机对外地址)。

## 8. 冲突 / 环路 / 一次性

- **token 一次性**:`used` 标记 + 过期时间; join 事务内 `SELECT ... WHERE used=false` 后立即置 used, 防并发双用。定期清理过期 token。
- **peer 目录去重**:按 **指纹(公钥 fingerprint)** 唯一, 其次 URL; 已存在则更新地址/公钥, 不重复插入 → 天然防 gossip 环路风暴。
- **master 唯一性仲裁**:正常只有一个 `is_master`。若目录里出现多个(脑裂), 规则 **指纹字典序最小者胜**(确定性、无需协调); follower 按此选唯一 master, 并把其余 `is_master` 视为普通 peer。
- **自我保护**:目录里不把"自己"加成 peer(按本机指纹过滤)。

## 9. 组件设计

| 文件 | 动作 | 职责 |
|---|---|---|
| `internal/services/invite_service.go` | 新建 | 生成/校验/消费 invite token; 清理过期 |
| `internal/handler/sync_handler.go` | 改 | `GenerateInvite`(签发)、`JoinEndpoint`(被邀方 POST 加入) |
| `internal/services/sync_service.go` | 改 | `SyncPayload` 加 `Peers` 目录; ExportSnapshot 填充; ApplySnapshot 合并目录 + 仲裁 master |
| `internal/models/types.go` | 改 | 新增 `InviteToken` |
| `internal/db/migrations/` | 新建 | 建 invite_tokens 表(Master + Slave 分支都要跑, 记取 v2.5.31 教训) |
| `web/.../PeerSyncPanel.vue` | 改 | 「生成邀请链接」按钮(master) + 「用链接加入」入口 + 复制链接 |
| 前端路由 `/#/join` | 新建 | 解析 token, 展示确认页, 调 join |

保持文件聚焦:invite 逻辑独立成 `invite_service.go`, 不塞进已很大的 sync_service。

## 10. 兼容 / 过渡

- 现有手动加 peer + 设主站**保留**(邀请是叠加的便捷路径, 不替换)。
- gossip 目录字段 `Peers` 为新增可选, 老节点忽略即可; 建议同批升级。
- migration 记取教训:**Master 和 Slave 分支都要建 invite_tokens 表**(app.go 的 Slave 分支不跑 AutoMigrate)。

## 11. 测试策略

- **单测**:token 一次性(并发双用只成一次)、过期拒绝; join 落库正确(双方 sync_peers + key); ApplySnapshot 合并目录去重 + 多 master 仲裁(指纹最小胜) + 不把自己加成 peer。
- **集成**:三节点脚本 —— A 签发→B join→C 用 B 转发的链接 join A→校验三方 peer 目录一致、都指向 A、数据 68 一致。
- **安全**:join 响应里 sync_key 必须加密(明文不出现); token 复用被拒。

## 12. 开放问题 / 后续

- **子站独立签发 + 跨节点 key 分发**(真正去中心化邀请):需要 gossip 加密传 per-peer key + 信任传递, 复杂度高, 本 spec 用"master 中心化 join + 子站转发链接"规避。若未来有"master 不常在线、子站需自主拉人"的硬需求再做。
- **邀请链接是否编码 master 公钥指纹**(防被邀方被 MITM 引到假 master):建议编码进链接, join 时校验 —— 列为实现细节。
- **邀请审计/吊销**:invite_tokens 保留 used_by 指纹便于审计; 手动吊销未用 token 可后加。
