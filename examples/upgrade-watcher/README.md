# Remote Upgrade Watcher

让 AutoGateway 节点能通过同步 mesh 的 UI 远程触发自身或对端的版本升级。

## 为什么需要它?

容器内的 AutoGateway 进程**不能重启自己** — Docker 安全模型决定了:
- 进程在容器隔离里,没有 docker daemon 的访问权限
- 把 `/var/run/docker.sock` 挂进主容器 = 等于把宿主机 root 权限交给容器(严重 anti-pattern)

所以远程升级走"**信号文件 + 宿主机 watcher**"的两层架构:

```
容器内 Go 进程 → 写 /app/data/.upgrade-request
                              ↓ (./data 已经挂载到宿主机)
宿主机 watcher (systemd 或独立容器) → docker compose pull && up -d
```

主容器零特权,只多写一个文件;升级权限隔离在 watcher 里。

## 两种部署方式

任选其一:

### 方式 1: systemd watcher (推荐裸机 / VM)

```bash
# 1. 把脚本和 service 文件放到正确位置
sudo cp upgrader.sh /opt/autogateway/upgrader.sh
sudo chmod +x /opt/autogateway/upgrader.sh
sudo cp autogateway-upgrader.service /etc/systemd/system/

# 2. 启用并启动 watcher
sudo systemctl daemon-reload
sudo systemctl enable --now autogateway-upgrader

# 3. 看日志确认 watcher 跑起来了
sudo journalctl -u autogateway-upgrader -f
```

调整 `autogateway-upgrader.service` 里的 `AUTOGATEWAY_DIR` 为你实际的 compose 目录。

### 方式 2: 伴随容器 (推荐 docker-compose 用户)

在原 `docker-compose.yml` 旁追加 watcher 容器:

```bash
docker compose \
  -f docker-compose.yml \
  -f examples/upgrade-watcher/docker-compose.with-upgrader.yml \
  up -d
```

只有 watcher 容器挂了 docker.sock,**主容器仍然零特权**。

## 验证

升级请求一旦触发,watcher 会在 `data/.upgrade-log` 里追加事件:

```
2026-05-26T16:23:01+00:00 watcher started, polling /opt/autogateway/data/.upgrade-request every 10s
2026-05-26T16:25:42+00:00 signal detected: target=v2.5.0 by=peer-prod-1
2026-05-26T16:26:18+00:00 upgrade succeeded: target=v2.5.0 by=peer-prod-1
```

如果用户在 UI 点了升级但本节点没部署 watcher,信号文件会一直在,UI 会在 60s 后提示"对端可能未部署 watcher"。

## 安全考量

| 风险 | 缓解 |
|---|---|
| watcher 被欺骗执行任意命令 | 进程侧严格校验 semver + 拒绝降级,信号文件内容受信 |
| 信号文件被宿主机其他用户篡改 | 文件由容器写,权限 0o600;watcher 只读 target_version,grep 不解析 shell |
| 升级期间断连丢同步 | 5min HTTP pull 兜底 + last_synced_at 续传,自动追平 |
| 攻击者注入旧版本利用 CVE | 进程侧硬限制:不允许 target ≤ current(`compareSemver` 拒绝) |

## 撤销 / 卸载

```bash
# 方式 1
sudo systemctl disable --now autogateway-upgrader
sudo rm /etc/systemd/system/autogateway-upgrader.service /opt/autogateway/upgrader.sh

# 方式 2
docker compose -f docker-compose.with-upgrader.yml down
```

撤销后,UI 上点升级仍会写信号文件,但不会被任何人消费 — 用户会看到"watcher 未部署"提示。
