# 配置一键备份 / 恢复 设计文档

- 日期：2026-05-22
- 状态：Draft（待用户复核）
- 作者：Pengfei + Claude

## 1. 背景与目标

api-center 当前只有零散的"分组内密钥导出"和"请求日志 CSV 导出"两个能力（`internal/handler/key_handler.go:485`、`internal/handler/log_handler.go:51`），没有覆盖 SystemSetting / Group / GroupSubGroup / APIKey / ModelAlias 这套"业务配置"的整体备份与恢复入口。

本设计交付：

- 一键导出当前实例所有业务配置为单个加密文件 `.acb`。
- 一键从 `.acb` 文件恢复到任意实例（含跨实例迁移、跨 `ENCRYPTION_KEY` 环境）。
- Web UI 入口（系统设置页新增 Backup tab） + HTTP API 入口（供脚本 / CI 使用）。

**不在范围**：RequestLog、GroupHourlyStat、free_models CDN 缓存、数据库物理快照、增量备份。

## 2. 关键决策摘要

| 决策点 | 选择 |
|---|---|
| 备份范围 | SystemSetting + Group(非系统) + GroupSubGroup + APIKey + ModelAlias |
| 上游 key 处理 | 导出前用当前实例 `ENCRYPTION_KEY` 解密为明文写入备份；导入端用目标实例 `ENCRYPTION_KEY` 重新加密落库 |
| 整体加密 | 备份文件用用户输入的 backup password，Argon2id 派生密钥 + AES-GCM-256 加密 |
| 冲突策略 | UI 三选一：`merge`（默认 upsert）/ `skip` / `replace`（先清空再灌） |
| 入口 | Web UI（系统设置 → Backup tab）+ HTTP API `/api/admin/backup/*` |

## 3. 架构与代码布局

### 3.1 新增包 `internal/backup/`

| 文件 | 责任 |
|---|---|
| `backup.go` | 对外 `Service` 接口与构造函数 |
| `codec.go` | `.acb` 容器格式的 Seal / Open |
| `crypto.go` | Argon2id KDF + AES-GCM 封装 |
| `schema.go` | `BackupV1` 与各实体 DTO（payload schema 类型） |
| `exporter.go` | 读 DB → 组装 DTO → JSON marshal |
| `importer.go` | 解 JSON → 按 strategy 写库（单事务） |
| `service.go` | 装配，依赖 `*gorm.DB` 与 `encryption.Service`（仅用于 DB 字段重新加密） |

**显式不复用** `internal/encryption`：那一套是 DB 字段加密、密钥来自 `ENCRYPTION_KEY` 环境变量。备份文件加密是另一条链路，密钥来自用户输入的 backup password。两套独立，避免误绑定。

### 3.2 Handler 层

新增 `internal/handler/backup_handler.go`：

- `POST /api/admin/backup/export` — body `{"password": "..."}`，返回 `application/octet-stream`。
- `POST /api/admin/backup/preview` — multipart `{file, password}`，返回 JSON 报告 + `confirm_token`。
- `POST /api/admin/backup/import` — multipart `{file, password, strategy, confirm_token}`，返回 import report。

挂在 `internal/router/router.go` 现有 admin auth middleware 分组下（与 `/api/keys/export` 同级）。

### 3.3 前端

- 新组件：`web/src/components/v3/V3BackupPanel.vue`。
- 挂载点：在 `V3SystemSettings.vue`（或对应系统设置页）新增 `Backup` Tab。
- API 客户端：`web/src/api/backup.ts`（`exportBackup`、`previewBackup`、`importBackup`）。

UI 由两块卡片组成：
1. **导出**：密码输入框（含"生成 32 字符随机"按钮，密码强度提示） → 下载按钮。
2. **导入**：文件选择 + 密码 → 「预览」按钮 → 渲染预览报告 + 冲突明细 + 三选 radio → 「应用」按钮（`replace` 模式需输入 `DELETE` 二次确认）。

## 4. 文件格式

### 4.1 `.acb` 容器二进制布局

| 偏移 | 长度 | 字段 | 值 |
|---|---|---|---|
| 0 | 4 | magic | ASCII `"ACB1"` |
| 4 | 1 | container_version | `0x01` |
| 5 | 1 | kdf_id | `0x01` = Argon2id |
| 6 | 1 | cipher_id | `0x01` = AES-GCM-256 |
| 7 | 1 | reserved | `0x00` |
| 8 | 16 | salt | `crypto/rand` |
| 24 | 12 | nonce | `crypto/rand` |
| 36 | 4 | aad_len (BE u32) | 通常等于 5 |
| 40 | aad_len | aad | 头部前 5 字节 `ACB1\x01`，作 GCM AAD 防降级 |
| 40+aad_len | rest | ciphertext\|\|gcm_tag | AES-GCM 输出（含 16-byte tag） |

**KDF 参数固定**：Argon2id `time=3, memory=64 MiB, threads=4, keyLen=32`。需要变更时升 `container_version`。

**Payload 编码**：原始 JSON（UTF-8，无压缩）。仅配置体量小（典型 < 200 KB），不引入压缩复杂度。

### 4.2 Payload Schema v1

```jsonc
{
  "schema_version": 1,
  "exported_at": "2026-05-22T10:00:00Z",
  "exported_by": "api-center <version>",
  "data": {
    "system_settings": [
      { "setting_key": "...", "setting_value": "...", "description": "..." }
    ],
    "groups": [
      {
        "name": "openai-main",
        "display_name": "...",
        "proxy_keys": "...",
        "description": "...",
        "group_type": "standard",
        "is_system": false,
        "system_role": "",
        "upstreams": [...],
        "validation_endpoint": "...",
        "channel_type": "openai",
        "sort": 0,
        "test_model": "gpt-4o-mini",
        "param_overrides": {...},
        "config": {...},
        "header_rules": [...],
        "model_redirect_rules": {...},
        "model_redirect_strict": false,
        "model_routing_mode": "passthrough",
        "exposed_models": [...],
        "blocked_models": [...]
      }
    ],
    "group_sub_groups": [
      { "parent_name": "default-openai", "sub_group_name": "openai-main", "weight": 1 }
    ],
    "api_keys": [
      {
        "group_name": "openai-main",
        "key_value": "<PLAINTEXT_UPSTREAM_KEY>",
        "status": "active",
        "notes": ""
      }
    ],
    "model_aliases": [
      {
        "alias": "gpt-fast",
        "group_name": "openai-main",
        "real_model": "gpt-4o-mini",
        "weight": 1,
        "priority": 100,
        "enabled": true,
        "is_reserved": false
      }
    ]
  }
}
```

### 4.3 字段映射规则

- **剔除字段**：`id`、`created_at`、`updated_at`、`request_count`、`failure_count`、`last_used_at`、`key_hash`、`key_count`、`available_models`。这些是运行时/统计字段，导入端重建。
- **外键用 name**：`api_keys.group_name`、`model_aliases.group_name`、`group_sub_groups.{parent_name, sub_group_name}`。Group.Name 是 unique（`internal/models/types.go:105`），跨实例 ID 不一致没影响。
- **`api_keys.key_value` 持明文**：导出时调 `encryption.Service.Decrypt`；导入时调 `encryption.Service.Encrypt` 并重算 `key_hash`。**目标实例 `ENCRYPTION_KEY` 可与源不同。**
- **系统分组（`is_system=true`）跳过导出**：default-openai/gemini/anthropic 等由启动流程 `EnsureSystemAggregates` seed，备份只会引起冲突。其下的 GroupSubGroup 关系**保留**（按 parent_name 查找）。
- **`system_settings.encryption_key` 在导入端跳过**（即使备份里含此字段也忽略）。原因：替换运行时 `ENCRYPTION_KEY` 会让现有 DB 中所有 `api_keys.key_value` 密文全部失效。

## 5. 导出流程

1. **鉴权**：复用现有 admin auth middleware。
2. **入参**：`{password: string}`。密码长度 < 8 时前端 warn 但不挡，后端无校验（用户主权）。
3. **数据采集**（单事务 `Repeatable Read`）：
   - `SystemSetting`：全表（含 `encryption_key` 原样导出，导入端会忽略）。
   - `Group`：`WHERE is_system = false`。
   - `APIKey`：`JOIN groups` 取 `group_name`；逐条 `encryption.Service.Decrypt(KeyValue)`，失败的 key 跳过 + 写入 `warnings`。
   - `ModelAlias`：`JOIN groups` 取 `group_name`；含 `is_reserved=true` 行。
   - `GroupSubGroup`：双 JOIN 解析两端 name；指向已被跳过的 standard group 时丢弃 + warn。
4. **打包**：组装 `BackupV1` 结构 → `json.Marshal` → `codec.Seal(payload, password)`。
5. **响应**：
   - `Content-Type: application/octet-stream`
   - `Content-Disposition: attachment; filename="api-center-backup-<host>-<YYYYMMDD-HHMMSS>.acb"`
     - `<host>` 取自 `SystemSetting.app_url`，去掉协议与非字母数字字符，fallback `unknown`。
   - body 流式 `io.Copy`，不全量驻留。
6. **审计**：handler 入出口各一行 `logrus.Info`，含 `event=backup.export user=<adminID> bytes=<n>`。**不**记录 password。

## 6. 导入流程

UI 两步：**上传 + 预览** → **确认应用**。

### 6.1 Preview 接口

`POST /api/admin/backup/preview`，multipart `{file, password}`。**只解密 + 解 JSON，不写库。**

响应：

```jsonc
{
  "schema_version": 1,
  "exported_at": "...",
  "exported_by": "...",
  "counts": { "system_settings": N, "groups": N, "api_keys": N, "model_aliases": N, "group_sub_groups": N },
  "conflicts": {
    "groups": ["openai-main", "..."],
    "api_keys_by_hash": 12,
    "aliases": 5,
    "system_settings": ["app_url", "..."]
  },
  "will_delete_if_replace": {
    "groups": N, "api_keys": N, "model_aliases": N, "group_sub_groups": N
  },
  "warnings": ["..."],
  "confirm_token": "<random-256bit-hex>"
}
```

`confirm_token` 10 分钟有效，一次性使用，存储在内存中（重启失效，符合预期）。

### 6.2 Import 接口

`POST /api/admin/backup/import`，multipart `{file, password, strategy, confirm_token}`，`strategy ∈ {merge, skip, replace}`。

**事务边界**：整个导入跑在一个 `db.Transaction(...)`，任意一张表失败全回滚。

**冲突解析（按业务键，不按 ID）**：

| 实体 | 业务键 | merge | skip | replace |
|---|---|---|---|---|
| SystemSetting | `setting_key` | UPSERT `setting_value/description` | 已存在则跳过 | 同 merge（不清空 system_settings） |
| Group (standard) | `name` | UPSERT 所有非系统字段；`is_system` 强制 false | 已存在则跳过（并跳过其下 keys/aliases） | **先**删除所有 `is_system=false` 的 Group + 级联 APIKey/Alias，再插入 |
| APIKey | `(group_id, key_hash)` | UPSERT `status/notes`；新增按业务键插 | 已存在跳过 | 由 Group 级联清掉 |
| ModelAlias | `(group_id, alias, real_model)` | UPSERT `weight/priority/enabled` | 已存在跳过 | 由 Group 级联清掉 |
| GroupSubGroup | `(group_id, sub_group_id)` | UPSERT `weight` | 已存在跳过 | 删完重建 |

**所有策略都生效的硬规则**：

- 跳过 payload 中 `is_system=true` 的 group。
- 跳过 `setting_key = "encryption_key"` 的 SystemSetting。
- `api_keys.key_value` 入库前用当前实例 `encryption.Service.Encrypt` + 重算 `key_hash`。
- `replace` 模式需 UI 二次确认（必须输入 `DELETE` 文本才能提交）。

**导入后处理**（顺序敏感）：

1. 调用 `aggregateGroupService.BackfillSystemAggregates`，让新建的 standard group 自动挂回系统聚合（default-openai/gemini/anthropic）。
2. 调用 `groupManager.Invalidate()`（`internal/services/group_manager.go:204`），让所有节点失效本地 group 缓存、下次请求重新从 DB 加载。
3. 如果 SystemSetting 有变更，调用 `settingsManager` 对应的 invalidate 路径（参考 `internal/config/system_settings.go:230`）。
4. 不重启进程。

**返回报告**：

```jsonc
{
  "applied": { "system_settings": N, "groups": N, "api_keys": N, "model_aliases": N, "group_sub_groups": N },
  "skipped": { "system_settings": N, "groups": N, "api_keys": N, "model_aliases": N, "group_sub_groups": N },
  "warnings": ["..."],
  "elapsed_ms": N
}
```

## 7. 错误处理

| 失败 | HTTP | error code |
|---|---|---|
| 密码错 / 文件被改 | 400 | `INVALID_PASSWORD_OR_CORRUPTED` |
| 容器 magic/version 不认 | 400 | `UNSUPPORTED_BACKUP_FORMAT` |
| payload schema_version 高于本实例支持 | 400 | `UNSUPPORTED_SCHEMA_VERSION`（附本端支持版本号） |
| JSON 字段缺失 / 类型错 | 400 | `MALFORMED_PAYLOAD`（附首个错误字段路径） |
| `confirm_token` 失效（>10 min 或已用过） | 409 | `STALE_PREVIEW` |
| 单条记录写入失败 | 500 | `IMPORT_FAILED`（含失败业务键，全事务回滚） |
| 单 key Decrypt 失败（导出侧） | 200 | 跳过 + warning，不整体失败 |

**不做**：retry / 断点续传 / 分片上传。

## 8. 测试策略

### 8.1 单元测试 (`internal/backup/*_test.go`)

- `codec_test.go`：Seal/Open round-trip；密码错；magic 错；container_version 错；header 截断；nonce 随机性（10000 次无重复）。
- `crypto_test.go`：Argon2id 同 salt + 同密码必得相同 key；不同密码必不同。
- `schema_test.go`：每张表 DB row ↔ DTO 双向无损 round-trip。
- `importer_test.go`：内存 sqlite + AutoMigrate，跑 `merge / skip / replace` 三种策略各一次的 golden-table 断言。覆盖冲突矩阵每一格。
- `service_test.go`：full E2E —— seed DB → Export → wipe → Import → 对比关键表。

### 8.2 Handler 测试 (`backup_handler_test.go`)

`httptest.NewRecorder` 覆盖：401 unauthorized / 400 bad password / 400 stale token / 200 happy path。

### 8.3 前端

组件单测覆盖：冲突 radio 联动；`replace` 二次确认弹窗的 `DELETE` 校验；preview → import 的 `confirm_token` 透传。

### 8.4 手工 smoke

开发实例 export → 干净实例 import → 用代理打一个真实请求验证 key 能用、alias 命中。

## 9. 安全说明

- backup password 仅出现在 multipart body，不入 access log、不入 logrus、不入审计。
- AES-GCM tag 在解密时自动校验，密码错或文件改动均会拒绝。
- Argon2id 参数（time=3, memory=64 MiB）确保暴力破解 password 成本高。
- 备份文件本质上是"全实例上游 key 的明文集合"——UI 文案需强调"妥善保管，丢失即等于上游 key 全部泄漏"。
- 导入页置顶 banner：「**导入前务必先对当前实例做一次备份**」+ 一键备份当前实例。

## 10. 兼容性与演进

- `container_version` 与 `schema_version` 双轨。容器格式（加密/封装）变更升 container；payload 字段变更升 schema。
- 旧版 schema 在新实例可识别并升级（importer 内部按 version 分支）；新 schema 在旧实例直接拒绝（`UNSUPPORTED_SCHEMA_VERSION`）。
- 未来若加入 RequestLog / GroupHourlyStat 备份，在 `data` 节点下加新键即可，旧实例忽略未知键即可向后兼容（importer 需明确 ignore-unknown-fields 行为）。

## 11. 显式不做的事

- 不做"全量备份含日志"。日志量级数量级不同，需要的是 DB 物理备份或 log shipping。
- 不做"自动定时备份"。本期纯交互；定时备份留给运维侧（cron + curl HTTP API）。
- 不做"备份分片 / 多文件"。配置体量小，没必要。
- 不做"备份签名 / 公私钥"。AES-GCM 已含完整性校验；多一层密钥管理负担没收益。
- 不做"导入时灰度 / dry-run 仅预览不解密"。preview 接口已经是完整 dry-run。
