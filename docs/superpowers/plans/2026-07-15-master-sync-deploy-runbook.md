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

## 2026-07-15 实战经验(部署踩坑记录)

止血最终成功:三节点收敛 68 / agnes 4 / xfyun 6 / 孤儿 0。踩过的坑,后来者注意:

1. **follower 只镜像唯一 master**(v2.5.29):对称 mesh 遗留多 peer 时,老逻辑 follower
   镜像所有 peer → pull 到空/错误 peer 就把自己清空。现在只镜像 is_master=true 的 peer。
   **部署后必须在 UI(或 DB)把 master(HK) peer 标 is_master**,否则 follower 不镜像任何。
2. **mini 用内联 `environment:`,不读 .env**:IS_SLAVE 要加到 docker-compose.yml 的
   environment 块。核对 `docker exec autogateway printenv IS_SLAVE`。
3. **手动加 peer 缺公钥 → 解密失败**:正常应走 UI AddPeer(ws 握手自动换公钥)。手动
   INSERT 的 peer 要把对端 `public_key_x25519` 也填上(可从已握手成功的节点 sync_peers 拷)。
4. **is_master 列**:AutoMigrate 对已存在表加 bool 列偶尔漏(本地/mini 漏、HK 没漏),
   V2_5_30 显式 AddColumn 兜底。
5. **角色/master 热切**(v2.5.29):UI「本站角色」开关切 master/follower、peer「设为主站」
   选 master,立即生效不重启。
6. **ApplySnapshot 匹配按索引类型分治**:partial-unique(group/key)活匹配;全局 unique
   (alias/subgroup/setting)Unscoped 匹配;alias 业务键含 group_id 且要 remap。
