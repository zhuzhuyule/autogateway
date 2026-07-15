# 邀请制加入 + 树形级联同步（Invite-Based Tree Sync）设计

日期：2026-07-15（2026-07-16 按"树形单父"反馈重写）
状态：设计待评审
作者：Pengfei + Claude
前置：建立在 [主从同步架构](2026-07-15-master-authoritative-sync-design.md) 之上

## 1. 背景与目标

当前主从同步（v2.6.0）组网靠**手动**:每加一个节点,要在两台机器上互填 URL + 同一把 sync_key,再手动"设为主站"。节点多了繁琐。

**目标**:让加入像"扫码入群"一样简单,并支持**树形组网**:

1. **邀请链接**:任意节点生成一个带一次性 token 的链接。
2. **一键加入**:被邀方用链接即可加入 —— 自动交换 URL/公钥/per-peer key,自动把**邀请者设为自己的父节点(镜像源)**。
3. **树形拓扑**:每个节点**只有一个父节点**(它 join 的那个),但可以有**多个子节点**(它邀请进来的)。数据从根(权威 master)逐级级联下去。
4. **一次性**:token 用一次即失效,绑定这一次加入。
5. **内容共享**:加入后配置/密钥自动从父节点同步下来,本机专属字段(proxy_url 等)各自保留(沿用现有 sync_policy)。

### 为什么树形比"星形 + gossip"简单

上一版设想"所有节点归并到根 master + gossip 全网发现",最难的三块是:跨节点分发 per-peer key、gossip 环路/去重、多 master 仲裁。**树形把这些全部消掉**:

- 每个节点只跟**父**打交道(join 谁就镜像谁),不需要认识全网;
- key 只在"子↔父"这一对之间交换(join 时),永远不用跨节点分发;
- 没有 gossip,没有全网目录,没有 master 仲裁 —— 拓扑就是加入时自然长出来的树。

### 非目标（YAGNI）

- 不做全网发现 / gossip(每个节点只认识父 + 自己的子)。
- 不做多父 / 冗余路径(严格单父)。
- 不做 NAT 打洞 / 中继(父必须对子可达, 见 §6)。

## 2. 核心概念

| 概念 | 说明 |
|---|---|
| **父节点** | 一个节点 join 的对象, 也是它的**镜像源**(唯一)。复用现有 `SyncPeer.is_master=true`(语义收窄为"我的父")。 |
| **子节点** | 一个节点邀请进来的节点。父把子存为普通 peer(用于鉴权子的 pull)。 |
| **根节点** | 没有父的节点 = 权威 master。整棵树的数据源头。 |
| **邀请 token** | 一次性随机凭证, 由父签发, 存本地, 带过期 + used 标记。 |
| **邀请链接** | `{inviter_url}/#/join?token={code}` —— 自包含父地址 + token。 |
| **join** | 被邀方 POST `/api/sync/join`, 与父交换 URL/公钥, 父分配 per-peer key, 子把父设为镜像源。 |

```
        A  (根 = 权威 master, 无父)
       / \
      B   C   (B、C 的父都是 A, 各自镜像 A)
     / \
    D   E     (D、E 的父是 B, 镜像 B = 间接镜像 A)
```

## 3. 加入流程

```
① 父 P 在 UI 点「生成邀请链接」
   → P 本地存一条 invite_token(code, expires_at, used=false)
   → 链接: https://P.addr/#/join?token=CODE   (P 必须对被邀方可达, 见 §6)

② 新节点 N 打开链接 → UI 确认 → POST P/api/sync/join
   body: { token, my_url, my_public_key, my_fingerprint, my_name }

③ P 校验 token: 存在 && 未过期 && used=false(事务内置 used, 防并发双用); 否则 4xx
   P 生成 per-peer sync_key K (random)
   P 写 sync_peers: N(url, pubkey, sync_key=K, is_master=false)   # N 是 P 的子
   P 标记 token used=true, used_by=N.fingerprint
   P 返回(用 N 的公钥加密 K): { sync_key: K, parent: {url, public_key, name} }

④ N 落库: sync_peers += P(url, pubkey, sync_key=K, is_master=true)  # P 是 N 的父/镜像源
   N 设本机角色 = follower(node_is_slave=true)
   N 的 doPull 只镜像 is_master 的 peer = P → N 立即镜像 P
   (P 若自己也是某节点的子, 数据从根一路级联到 N)

⑤ N 之后也能点「生成邀请链接」邀请自己的子节点, 递归长出子树
```

**级联天然成立**:P 作为 follower,被子 N 拉取时照常提供全量快照(现有 `PullEndpoint` 不看角色) —— 这一点在主从架构里**已经实现**,树形直接复用。

## 4. 单父约束 & 换父

- 每个节点**至多一个** `is_master=true` 的 peer(父)。DB 层不强制,应用层保证:join 成功时先把已有 `is_master` 清掉,再设新父(即 **join 新父 = 换父**,原镜像关系解除)。
- 换父是允许的(节点可以重新挂到树的另一处);切换即时生效(下一轮 doPull 拉新父)。
- UI 上「本站角色」开关与「设为主站」按钮沿用 v2.6.0 的联动逻辑;join 只是把"换父"自动化。

## 5. 鉴权与密钥交换

- **每对(子,父)一把 per-peer sync_key**(维持你定的安全底线, 无全网共享)。
- key 由**父在 join 时生成**, 用**被邀方公钥加密**回传(nacl/box, 复用 `EncryptPayloadFor`), 明文不上链路。
- token 是准入凭证 + 建立首个可信信道的一次性 secret; 用完即焚。
- 之后 pull/ws 沿用现有 per-peer 鉴权(`sync_peers.sync_key`)不变。
- 公钥交换:join 响应先带父的公钥加速首次加密; 常规仍靠现有 ws 握手(hello/welcome)兜底刷新。

## 6. 网络前提（内网/外网）

**约束:每个父必须对它的子可达。** 因为同步永远是**子主动出站**拉父(HTTP pull + ws dial 握手),父从不回连子。

树形比星形**更灵活**:不要求所有节点都能到根 master,只要求**每一级的父对其子可达**。所以:

- 一个内网里的子挂到同内网的父 → OK(都内网);
- 跨网的子挂到公网的父 → OK(子出站);
- 子挂到内网的、自己够不到的父 → ❌(需端口映射, 非目标)。

**邀请链接里的 inviter URL 必须填被邀方可达的地址**(UI 用本机 `app_url`;跨公网时填公网域名)。

## 7. 数据模型

**新表 `invite_tokens`**(本机, 不同步):
```go
type InviteToken struct {
    Code      string     `gorm:"primaryKey"` // 随机 URL-safe
    ExpiresAt time.Time  `gorm:"index"`
    Used      bool       `gorm:"default:false"`
    UsedBy    string     // 被邀方指纹, 审计
    UsedAt    *time.Time
    CreatedAt time.Time
}
```
**复用 `SyncPeer`**(URL / SyncKey / PublicKeyX25519 / PinnedFingerprint / IsMaster), join 直接填, 不新增列。`IsMaster=true` 语义收窄为"这是我的父"。

## 8. 组件设计

| 文件 | 动作 | 职责 |
|---|---|---|
| `internal/services/invite_service.go` | 新建 | 生成/校验/消费 invite token; 清理过期 |
| `internal/handler/sync_handler.go` | 改 | `GenerateInvite`(签发链接) + `JoinEndpoint`(被邀方加入, 交换 key + 落 peer) |
| `internal/models/types.go` | 改 | 新增 `InviteToken` |
| `internal/db/migrations/` | 新建 | 建 invite_tokens 表 —— **Master + Slave 分支都要跑**(记取 v2.5.31 教训: Slave 分支不跑 AutoMigrate) |
| `web/.../PeerSyncPanel.vue` | 改 | 「生成邀请链接」按钮 + 复制 + 「用链接加入」入口 |
| 前端路由 `/#/join` | 新建 | 解析 token → 确认页 → 调 join → 提示成功 |

`invite_service.go` 独立, 不塞进已很大的 sync_service。

## 9. 兼容 / 过渡

- 现有手动加 peer + 设主站**保留**(邀请是叠加的便捷路径)。
- 单父约束是应用层软约束, 不改 schema, 老节点无影响。
- migration 教训:invite_tokens 表 **Master 和 Slave 分支都要建**。

## 10. 测试策略

- **单测**:token 一次性(并发双用只成一次)、过期拒绝; join 落库正确(子↔父双方 sync_peers + 同一把 key, 父在子侧 is_master=true); 换父(join 新父清掉旧 is_master)。
- **集成**:三节点树 —— A 邀 B(B 挂 A) → B 邀 C(C 挂 B) → 校验 C 的数据 == A(级联), 每级 sync_peers 关系正确, C 只认识 B(父)不认识 A。
- **安全**:join 响应里 sync_key 必须加密(明文不出现); token 复用被拒。

## 11. 开放问题 / 后续

- **邀请链接编码父公钥指纹**(防被邀方被 MITM 引到假父):建议编码进链接, join 时校验 —— 实现细节。
- **邀请审计/吊销**:invite_tokens 留 used_by 指纹; 手动吊销未用 token 可后加。
- **孤儿子树**(父长期掉线):子保留最后一次镜像, 不丢数据; 可手动换父挂到别处。不做自动重挂。
