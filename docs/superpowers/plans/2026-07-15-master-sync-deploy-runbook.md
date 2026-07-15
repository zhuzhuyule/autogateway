# 主从同步 v2.5.26 部署止血 Runbook

日期：2026-07-15  · master = HK VPS · 黄金数据 = 信任 HK 当前数据

## ⚠️ 关键:角色由 `IS_SLAVE` 环境变量决定(不是 IS_MASTER)

```
IsMaster = !IS_SLAVE   (internal/config/manager.go:74)
```

- **默认(不设 IS_SLAVE)= master**。这就是为什么现状三节点都是 master。
- **follower 必须显式设 `IS_SLAVE=true`**,否则节点仍是 master、不会镜像、退化成旧的对称 LWW。

| 节点 | 角色 | IS_SLAVE | 行为 |
|---|---|---|---|
| HK (154.12.85.84) | master | 不设 / false | 权威源;接收 follower 手动迁移;向 follower 提供全量快照 |
| 本地 | follower | **true** | 每分钟 pull HK 全量快照 → 镜像;不接收 push;不自动上传 |
| mini (192.168.3.10) | follower | **true** | 同上 |

## 部署顺序

### 1. HK (master) — 先升级权威源

```bash
# .env 确认没有 IS_SLAVE(或 IS_SLAVE=false)
sshpass -p '***' ssh root@154.12.85.84 'cd /root/autogateway && docker compose pull && docker compose up -d'
```

### 2. 本地 (follower)

```bash
# .env 加一行: IS_SLAVE=true
docker compose pull && docker compose up -d
```
确认已有指向 HK 的 peer(对称 mesh 时期应已配);无则在 PeerSyncPanel 添加。

### 3. mini (follower)

```bash
ssh macmini 'zsh -lc "cd /Users/zac/autogateway && echo IS_SLAVE=true >> .env && docker compose pull && docker compose up -d"'
```
（若 .env 已有 IS_SLAVE 行,改成 true 而不是 append,避免重复行。）

## 验证(部署 + 等 1-2 分钟 follower pull 后)

三节点跑同样 SQL,期望完全一致(都等于 HK):

```sql
SELECT count(*) FROM api_keys WHERE deleted_at IS NULL;                       -- 三节点相等
SELECT g.name, count(*) FROM api_keys k JOIN `groups` g ON g.id=k.group_id
  WHERE k.deleted_at IS NULL GROUP BY g.name ORDER BY g.name;                 -- per-group 一致
```

- follower 启动日志应有 `pulled and merged changes from peer HK`(ApplySnapshot 镜像)。
- follower 不应再出现 agnes/xfyun 分裂(镜像后随 HK)。

## 回滚

- follower 去掉 `IS_SLAVE=true` 重启 → 退回 master(对称 LWW 旧行为)。
- 镜像是全量替换,follower 本地数据会被 HK 覆盖;回滚不恢复被覆盖的本地独有数据
  (这些应在部署前用手动迁移推给 HK 保留 —— 本次选"信任 HK",不额外保留)。
