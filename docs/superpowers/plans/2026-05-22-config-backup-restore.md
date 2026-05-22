# 配置一键备份 / 恢复 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 给 api-center 增加"一键导出全部业务配置 → 加密 .acb 文件 → 任意实例一键恢复"的能力，含 Web UI + HTTP API。

**Architecture:** 新建 `internal/backup/` 包封装 ACB 容器（Argon2id + AES-GCM）和 schema v1 payload。`internal/handler/backup_handler.go` 暴露 `export/preview/import` 三个接口走现有 admin auth。前端在 `views/Settings.vue` 顶部增加 BackupCard 组件。

**Tech Stack:** Go 1.24 / Gin / GORM / `golang.org/x/crypto/argon2` (已在依赖中) / Vue 3 + Naive UI / TypeScript

**Spec:** [`docs/superpowers/specs/2026-05-22-config-backup-restore-design.md`](../specs/2026-05-22-config-backup-restore-design.md)

**Project conventions observed:**
- Module name: `autogateway`（import 路径 `autogateway/internal/...`）
- DI: `go.uber.org/dig`，每个新组件在 `internal/container/container.go` 注册
- Handler 单文件结构（参考 `internal/handler/alias_handler.go`），构造函数 `NewXxxHandler(deps...) *XxxHandler`
- 路由：受保护接口加在 `internal/router/router.go` `registerProtectedAPIRoutes` 函数里
- 测试用 sqlite in-memory：`gorm.Open(sqlite.Open(":memory:"), ...)`
- 提交消息风格：`✨ feat(...)：...` / `🔧 fix(...)：...` / `📝 docs(...)：...`

---

## File Structure

**Create (Go):**
- `internal/backup/crypto.go` — Argon2id KDF + AES-GCM seal/open 原语
- `internal/backup/crypto_test.go`
- `internal/backup/codec.go` — ACB 容器（magic + header + AAD + ciphertext）
- `internal/backup/codec_test.go`
- `internal/backup/schema.go` — `BackupV1` 及实体 DTO
- `internal/backup/exporter.go` — DB → DTO 单事务采集
- `internal/backup/exporter_test.go`
- `internal/backup/importer.go` — DTO → DB（merge/skip/replace 三策略）
- `internal/backup/importer_test.go`
- `internal/backup/service.go` — `Service` 接口装配 + preview token store
- `internal/backup/service_test.go`
- `internal/handler/backup_handler.go` — HTTP 入口（export / preview / import）
- `internal/handler/backup_handler_test.go`

**Modify (Go):**
- `internal/container/container.go` — Provide `backup.Service` + `*handler.BackupHandler`
- `internal/router/router.go` — 注册 `/api/admin/backup/*` 三个路由

**Create (Frontend):**
- `web/src/api/backup.ts` — API 客户端
- `web/src/components/v3/V3BackupCard.vue` — 备份/恢复卡片组件

**Modify (Frontend):**
- `web/src/views/Settings.vue` — 顶部挂载 `V3BackupCard`

**Create (Docs):**
- 无 —— 后续随实现完成时更新 spec 状态字段（最后一步）

---

### Task 1: 加密原语 `internal/backup/crypto.go`

**Files:**
- Create: `internal/backup/crypto.go`
- Test: `internal/backup/crypto_test.go`

- [ ] **Step 1: 写失败测试 — round-trip & 错误密码**

```go
// internal/backup/crypto_test.go
package backup

import (
	"bytes"
	"testing"
)

func TestSealAndOpen_RoundTrip(t *testing.T) {
	plaintext := []byte("hello backup world")
	password := "correct-horse-battery-staple"

	box, err := Seal(plaintext, password)
	if err != nil {
		t.Fatalf("Seal failed: %v", err)
	}

	got, err := Open(box, password)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, plaintext)
	}
}

func TestOpen_WrongPassword(t *testing.T) {
	box, err := Seal([]byte("secret"), "right-password")
	if err != nil {
		t.Fatal(err)
	}

	_, err = Open(box, "wrong-password")
	if err == nil {
		t.Fatal("expected error on wrong password, got nil")
	}
}

func TestSeal_RandomSaltAndNonce(t *testing.T) {
	pt := []byte("x")
	a, _ := Seal(pt, "p")
	b, _ := Seal(pt, "p")
	if bytes.Equal(a.Salt[:], b.Salt[:]) {
		t.Fatal("salt must be random per Seal")
	}
	if bytes.Equal(a.Nonce[:], b.Nonce[:]) {
		t.Fatal("nonce must be random per Seal")
	}
}
```

- [ ] **Step 2: 跑测试，确认失败**

Run: `go test ./internal/backup/ -run TestSeal -v`
Expected: `FAIL: undefined Seal / Open / EncryptedBox`

- [ ] **Step 3: 实现最小可通过的代码**

```go
// internal/backup/crypto.go
package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"

	"golang.org/x/crypto/argon2"
)

const (
	saltLen   = 16
	nonceLen  = 12
	keyLen    = 32
	argonTime = 3
	argonMem  = 64 * 1024 // 64 MiB
	argonPar  = 4
)

// EncryptedBox 是一次 Seal 的全部输出，由 codec 负责落盘 / 还原。
type EncryptedBox struct {
	Salt       [saltLen]byte
	Nonce      [nonceLen]byte
	AAD        []byte
	Ciphertext []byte // 含 GCM tag
}

// DeriveKey 用 Argon2id 从 password 派生 32 字节对称密钥。
func DeriveKey(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, argonTime, argonMem, argonPar, keyLen)
}

// Seal 把 plaintext 用 password 加密为 EncryptedBox。
func Seal(plaintext []byte, password string) (*EncryptedBox, error) {
	box := &EncryptedBox{}
	if _, err := rand.Read(box.Salt[:]); err != nil {
		return nil, fmt.Errorf("salt: %w", err)
	}
	if _, err := rand.Read(box.Nonce[:]); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}

	key := DeriveKey(password, box.Salt[:])
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes: %w", err)
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}

	box.AAD = []byte{'A', 'C', 'B', '1', 0x01} // matches header magic+version
	box.Ciphertext = aead.Seal(nil, box.Nonce[:], plaintext, box.AAD)
	return box, nil
}

// Open 验证 AAD/tag 并解出 plaintext。密码错或 ciphertext 篡改返回错误。
func Open(box *EncryptedBox, password string) ([]byte, error) {
	key := DeriveKey(password, box.Salt[:])
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes: %w", err)
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	pt, err := aead.Open(nil, box.Nonce[:], box.Ciphertext, box.AAD)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return pt, nil
}
```

- [ ] **Step 4: 跑测试，确认通过**

Run: `go test ./internal/backup/ -v`
Expected: `PASS` for all three test cases.

- [ ] **Step 5: 提交**

```bash
git add internal/backup/crypto.go internal/backup/crypto_test.go
git commit -m "$(cat <<'EOF'
✨ feat(backup): Argon2id + AES-GCM 加密原语

新增 internal/backup 包，提供 Seal/Open + EncryptedBox，作为
.acb 容器格式的底层加密层。AAD 绑定 magic+version 防降级。

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: ACB 容器格式 `internal/backup/codec.go`

**Files:**
- Create: `internal/backup/codec.go`
- Test: `internal/backup/codec_test.go`

- [ ] **Step 1: 写失败测试**

```go
// internal/backup/codec_test.go
package backup

import (
	"bytes"
	"testing"
)

func TestEncodeDecode_RoundTrip(t *testing.T) {
	plaintext := []byte(`{"hello":"world"}`)
	password := "p4ssw0rd"

	blob, err := EncodeContainer(plaintext, password)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !bytes.HasPrefix(blob, []byte("ACB1")) {
		t.Fatalf("magic missing")
	}

	got, err := DecodeContainer(blob, password)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round-trip mismatch")
	}
}

func TestDecodeContainer_BadMagic(t *testing.T) {
	bad := bytes.Repeat([]byte{0}, 80)
	_, err := DecodeContainer(bad, "x")
	if err == nil || err.Error() == "" {
		t.Fatal("expected bad-magic error")
	}
}

func TestDecodeContainer_UnsupportedVersion(t *testing.T) {
	blob, _ := EncodeContainer([]byte("x"), "x")
	blob[4] = 0x99 // tamper container_version
	_, err := DecodeContainer(blob, "x")
	if err == nil {
		t.Fatal("expected unsupported version error")
	}
}

func TestDecodeContainer_Truncated(t *testing.T) {
	blob, _ := EncodeContainer([]byte("x"), "x")
	_, err := DecodeContainer(blob[:30], "x")
	if err == nil {
		t.Fatal("expected truncation error")
	}
}
```

- [ ] **Step 2: 跑测试**

Run: `go test ./internal/backup/ -run TestEncode -v ; go test ./internal/backup/ -run TestDecode -v`
Expected: FAIL with undefined symbols.

- [ ] **Step 3: 实现**

```go
// internal/backup/codec.go
package backup

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	magicLen        = 4
	headerFixedLen  = 8 // magic(4) + ver(1) + kdf(1) + cipher(1) + reserved(1)
	currentVersion  = 0x01
	kdfArgon2id     = 0x01
	cipherAESGCM256 = 0x01
)

var magic = [4]byte{'A', 'C', 'B', '1'}

// EncodeContainer 用 Seal 加密 plaintext 并组装成 .acb 字节流。
func EncodeContainer(plaintext []byte, password string) ([]byte, error) {
	box, err := Seal(plaintext, password)
	if err != nil {
		return nil, err
	}

	out := make([]byte, 0, headerFixedLen+saltLen+nonceLen+4+len(box.AAD)+len(box.Ciphertext))
	out = append(out, magic[:]...)
	out = append(out, currentVersion, kdfArgon2id, cipherAESGCM256, 0x00)
	out = append(out, box.Salt[:]...)
	out = append(out, box.Nonce[:]...)

	aadLen := make([]byte, 4)
	binary.BigEndian.PutUint32(aadLen, uint32(len(box.AAD)))
	out = append(out, aadLen...)
	out = append(out, box.AAD...)
	out = append(out, box.Ciphertext...)
	return out, nil
}

// DecodeContainer 解析 .acb 字节流并解密。
func DecodeContainer(blob []byte, password string) ([]byte, error) {
	if len(blob) < headerFixedLen+saltLen+nonceLen+4 {
		return nil, errors.New("acb: truncated header")
	}
	if string(blob[0:4]) != string(magic[:]) {
		return nil, errors.New("acb: bad magic")
	}
	ver := blob[4]
	if ver != currentVersion {
		return nil, fmt.Errorf("acb: unsupported container version %d", ver)
	}
	if blob[5] != kdfArgon2id {
		return nil, fmt.Errorf("acb: unsupported kdf id %d", blob[5])
	}
	if blob[6] != cipherAESGCM256 {
		return nil, fmt.Errorf("acb: unsupported cipher id %d", blob[6])
	}
	off := headerFixedLen

	box := &EncryptedBox{}
	copy(box.Salt[:], blob[off:off+saltLen])
	off += saltLen
	copy(box.Nonce[:], blob[off:off+nonceLen])
	off += nonceLen

	if len(blob) < off+4 {
		return nil, errors.New("acb: truncated aad_len")
	}
	aadLen := int(binary.BigEndian.Uint32(blob[off : off+4]))
	off += 4
	if len(blob) < off+aadLen {
		return nil, errors.New("acb: truncated aad")
	}
	box.AAD = blob[off : off+aadLen]
	off += aadLen
	box.Ciphertext = blob[off:]

	return Open(box, password)
}
```

- [ ] **Step 4: 跑测试**

Run: `go test ./internal/backup/ -v`
Expected: all PASS.

- [ ] **Step 5: 提交**

```bash
git add internal/backup/codec.go internal/backup/codec_test.go
git commit -m "$(cat <<'EOF'
✨ feat(backup): .acb 容器格式 Encode/Decode

固定 24+ 字节头部 (magic ACB1 + version + kdf + cipher + reserved +
salt 16B + nonce 12B + aad_len + aad)。版本/魔数/截断均报错。

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Payload Schema `internal/backup/schema.go`

**Files:**
- Create: `internal/backup/schema.go`

无单测（纯类型定义，由 exporter/importer 测试间接覆盖）。

- [ ] **Step 1: 创建 schema 文件**

```go
// internal/backup/schema.go
package backup

import "time"

// CurrentSchemaVersion 是当前 BackupV1 payload 的版本号。
const CurrentSchemaVersion = 1

// BackupV1 是 .acb 解密后的 JSON 顶层结构。
type BackupV1 struct {
	SchemaVersion int       `json:"schema_version"`
	ExportedAt    time.Time `json:"exported_at"`
	ExportedBy    string    `json:"exported_by"`
	Data          DataV1    `json:"data"`
}

// DataV1 包含所有被备份的实体集合。
type DataV1 struct {
	SystemSettings []SystemSettingDTO `json:"system_settings"`
	Groups         []GroupDTO         `json:"groups"`
	GroupSubGroups []GroupSubGroupDTO `json:"group_sub_groups"`
	APIKeys        []APIKeyDTO        `json:"api_keys"`
	ModelAliases   []ModelAliasDTO    `json:"model_aliases"`
}

type SystemSettingDTO struct {
	SettingKey   string `json:"setting_key"`
	SettingValue string `json:"setting_value"`
	Description  string `json:"description"`
}

type GroupDTO struct {
	Name                string                 `json:"name"`
	DisplayName         string                 `json:"display_name"`
	ProxyKeys           string                 `json:"proxy_keys"`
	Description         string                 `json:"description"`
	GroupType           string                 `json:"group_type"`
	IsSystem            bool                   `json:"is_system"`
	SystemRole          string                 `json:"system_role"`
	Upstreams           any                    `json:"upstreams"`
	ValidationEndpoint  string                 `json:"validation_endpoint"`
	ChannelType         string                 `json:"channel_type"`
	Sort                int                    `json:"sort"`
	TestModel           string                 `json:"test_model"`
	ParamOverrides      map[string]any         `json:"param_overrides"`
	Config              map[string]any         `json:"config"`
	HeaderRules         any                    `json:"header_rules"`
	ModelRedirectRules  map[string]any         `json:"model_redirect_rules"`
	ModelRedirectStrict bool                   `json:"model_redirect_strict"`
	ModelRoutingMode    string                 `json:"model_routing_mode"`
	ExposedModels       any                    `json:"exposed_models"`
	BlockedModels       any                    `json:"blocked_models"`
}

type GroupSubGroupDTO struct {
	ParentName   string `json:"parent_name"`
	SubGroupName string `json:"sub_group_name"`
	Weight       int    `json:"weight"`
}

type APIKeyDTO struct {
	GroupName string `json:"group_name"`
	KeyValue  string `json:"key_value"` // PLAINTEXT
	Status    string `json:"status"`
	Notes     string `json:"notes"`
}

type ModelAliasDTO struct {
	Alias      string `json:"alias"`
	GroupName  string `json:"group_name"`
	RealModel  string `json:"real_model"`
	Weight     int    `json:"weight"`
	Priority   int    `json:"priority"`
	Enabled    bool   `json:"enabled"`
	IsReserved bool   `json:"is_reserved"`
}

// Strategy 是 importer 的冲突处理策略。
type Strategy string

const (
	StrategyMerge   Strategy = "merge"
	StrategySkip    Strategy = "skip"
	StrategyReplace Strategy = "replace"
)

// ParseStrategy 校验并归一化 strategy 字符串。
func ParseStrategy(s string) (Strategy, bool) {
	switch Strategy(s) {
	case StrategyMerge, StrategySkip, StrategyReplace:
		return Strategy(s), true
	}
	return "", false
}
```

- [ ] **Step 2: 编译通过**

Run: `go build ./internal/backup/...`
Expected: no errors.

- [ ] **Step 3: 提交**

```bash
git add internal/backup/schema.go
git commit -m "$(cat <<'EOF'
✨ feat(backup): payload schema v1 与 Strategy 枚举

BackupV1 + 5 个实体 DTO。外键全部用 name (group_name / parent_name /
sub_group_name) 而非 id，跨实例迁移友好。

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Exporter `internal/backup/exporter.go`

**Files:**
- Create: `internal/backup/exporter.go`
- Test: `internal/backup/exporter_test.go`

- [ ] **Step 1: 写测试 helper + 失败测试**

```go
// internal/backup/exporter_test.go
package backup

import (
	"context"
	"testing"
	"time"

	"autogateway/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// newTestDB 启动 sqlite in-memory 并 AutoMigrate 所有备份相关表。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.SystemSetting{},
		&models.Group{},
		&models.GroupSubGroup{},
		&models.APIKey{},
		&models.ModelAlias{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

// fakeEncSvc 模拟 encryption.Service：明文 ↔ "enc:"+明文。
type fakeEncSvc struct{}

func (fakeEncSvc) Encrypt(s string) (string, error) { return "enc:" + s, nil }
func (fakeEncSvc) Decrypt(s string) (string, error) {
	if len(s) < 4 || s[:4] != "enc:" {
		return "", &fakeDecryptErr{}
	}
	return s[4:], nil
}

type fakeDecryptErr struct{}

func (*fakeDecryptErr) Error() string { return "fake decrypt fail" }

func TestExport_HappyPath(t *testing.T) {
	db := newTestDB(t)
	// seed 1 standard group + 1 system aggregate + key + alias
	g := models.Group{Name: "openai-main", ChannelType: "openai", TestModel: "gpt-4o-mini", Upstreams: datatypes.JSON("[]")}
	if err := db.Create(&g).Error; err != nil {
		t.Fatal(err)
	}
	sysG := models.Group{Name: "default-openai", ChannelType: "openai", TestModel: "gpt-4o-mini", IsSystem: true, GroupType: "aggregate", Upstreams: datatypes.JSON("[]")}
	if err := db.Create(&sysG).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.APIKey{GroupID: g.ID, KeyValue: "enc:sk-real-key", KeyHash: "h1", Status: "active"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.ModelAlias{Alias: "gpt-fast", GroupID: g.ID, RealModel: "gpt-4o-mini", Weight: 1, Priority: 100, Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.GroupSubGroup{GroupID: sysG.ID, SubGroupID: g.ID, Weight: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.SystemSetting{SettingKey: "app_url", SettingValue: "https://example.com"}).Error; err != nil {
		t.Fatal(err)
	}

	exp := NewExporter(db, fakeEncSvc{}, "test-1.0")
	got, warns, err := exp.Export(context.Background())
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	if got.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("schema_version: got %d", got.SchemaVersion)
	}
	if len(got.Data.Groups) != 1 || got.Data.Groups[0].Name != "openai-main" {
		t.Errorf("expected 1 standard group, got %+v", got.Data.Groups)
	}
	if len(got.Data.APIKeys) != 1 || got.Data.APIKeys[0].KeyValue != "sk-real-key" {
		t.Errorf("expected decrypted plaintext key, got %+v", got.Data.APIKeys)
	}
	if len(got.Data.GroupSubGroups) != 1 || got.Data.GroupSubGroups[0].ParentName != "default-openai" {
		t.Errorf("group_sub_groups: got %+v", got.Data.GroupSubGroups)
	}
	if got.ExportedAt.IsZero() || time.Since(got.ExportedAt) > time.Minute {
		t.Errorf("exported_at not stamped")
	}
	_ = warns
}

func TestExport_DecryptFailureProducesWarning(t *testing.T) {
	db := newTestDB(t)
	g := models.Group{Name: "x", ChannelType: "openai", TestModel: "m", Upstreams: datatypes.JSON("[]")}
	db.Create(&g)
	// missing "enc:" prefix → decrypt fails
	db.Create(&models.APIKey{GroupID: g.ID, KeyValue: "raw-no-prefix", KeyHash: "h", Status: "active"})

	exp := NewExporter(db, fakeEncSvc{}, "v")
	got, warns, err := exp.Export(context.Background())
	if err != nil {
		t.Fatalf("Export must not fail on per-key decrypt error: %v", err)
	}
	if len(got.Data.APIKeys) != 0 {
		t.Errorf("undecryptable key must be skipped, got %+v", got.Data.APIKeys)
	}
	if len(warns) == 0 {
		t.Error("expected at least 1 warning")
	}
}
```

- [ ] **Step 2: 跑测试**

Run: `go test ./internal/backup/ -run TestExport -v`
Expected: FAIL (`NewExporter` / `Exporter` undefined).

- [ ] **Step 3: 实现**

```go
// internal/backup/exporter.go
package backup

import (
	"context"
	"fmt"
	"time"

	"autogateway/internal/encryption"
	"autogateway/internal/models"

	"gorm.io/gorm"
)

// Exporter 从数据库构建 BackupV1。
type Exporter struct {
	db      *gorm.DB
	enc     encryption.Service
	version string
}

func NewExporter(db *gorm.DB, enc encryption.Service, version string) *Exporter {
	return &Exporter{db: db, enc: enc, version: version}
}

// Export 在单事务里读全部相关表。返回 (payload, warnings, error)。
// per-row decryption failures 记 warning 而不整体失败。
func (e *Exporter) Export(ctx context.Context) (*BackupV1, []string, error) {
	out := &BackupV1{
		SchemaVersion: CurrentSchemaVersion,
		ExportedAt:    time.Now().UTC(),
		ExportedBy:    e.version,
	}
	var warnings []string

	err := e.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. SystemSettings — 全表
		var ss []models.SystemSetting
		if err := tx.Find(&ss).Error; err != nil {
			return fmt.Errorf("read system_settings: %w", err)
		}
		out.Data.SystemSettings = make([]SystemSettingDTO, 0, len(ss))
		for _, s := range ss {
			out.Data.SystemSettings = append(out.Data.SystemSettings, SystemSettingDTO{
				SettingKey: s.SettingKey, SettingValue: s.SettingValue, Description: s.Description,
			})
		}

		// 2. Groups (standard only)
		var groups []models.Group
		if err := tx.Where("is_system = ?", false).Find(&groups).Error; err != nil {
			return fmt.Errorf("read groups: %w", err)
		}
		nameByID := make(map[uint]string, len(groups))
		out.Data.Groups = make([]GroupDTO, 0, len(groups))
		for _, g := range groups {
			nameByID[g.ID] = g.Name
			out.Data.Groups = append(out.Data.Groups, groupModelToDTO(&g))
		}

		// 系统聚合的 name → ID 也要收，给 GroupSubGroups 用
		var sysGroups []models.Group
		if err := tx.Where("is_system = ?", true).Find(&sysGroups).Error; err != nil {
			return fmt.Errorf("read sys groups: %w", err)
		}
		for _, g := range sysGroups {
			nameByID[g.ID] = g.Name
		}

		// 3. APIKeys (只导出 standard group 下的)
		standardIDs := make([]uint, 0, len(groups))
		for _, g := range groups {
			standardIDs = append(standardIDs, g.ID)
		}
		if len(standardIDs) > 0 {
			var keys []models.APIKey
			if err := tx.Where("group_id IN ?", standardIDs).Find(&keys).Error; err != nil {
				return fmt.Errorf("read api_keys: %w", err)
			}
			out.Data.APIKeys = make([]APIKeyDTO, 0, len(keys))
			for _, k := range keys {
				plain, derr := e.enc.Decrypt(k.KeyValue)
				if derr != nil {
					warnings = append(warnings, fmt.Sprintf("api_key id=%d decrypt failed, skipped", k.ID))
					continue
				}
				out.Data.APIKeys = append(out.Data.APIKeys, APIKeyDTO{
					GroupName: nameByID[k.GroupID],
					KeyValue:  plain,
					Status:    k.Status,
					Notes:     k.Notes,
				})
			}
		}

		// 4. ModelAliases (只导出 standard group 下的)
		if len(standardIDs) > 0 {
			var aliases []models.ModelAlias
			if err := tx.Where("group_id IN ?", standardIDs).Find(&aliases).Error; err != nil {
				return fmt.Errorf("read model_aliases: %w", err)
			}
			out.Data.ModelAliases = make([]ModelAliasDTO, 0, len(aliases))
			for _, a := range aliases {
				out.Data.ModelAliases = append(out.Data.ModelAliases, ModelAliasDTO{
					Alias:      a.Alias,
					GroupName:  nameByID[a.GroupID],
					RealModel:  a.RealModel,
					Weight:     a.Weight,
					Priority:   a.Priority,
					Enabled:    a.Enabled,
					IsReserved: a.IsReserved,
				})
			}
		}

		// 5. GroupSubGroups —— 两端必须 name 可解析；标准侧必须是被导出过的 standard
		standardSet := make(map[uint]struct{}, len(groups))
		for _, g := range groups {
			standardSet[g.ID] = struct{}{}
		}
		var subs []models.GroupSubGroup
		if err := tx.Find(&subs).Error; err != nil {
			return fmt.Errorf("read group_sub_groups: %w", err)
		}
		for _, s := range subs {
			parentName, ok1 := nameByID[s.GroupID]
			subName, ok2 := nameByID[s.SubGroupID]
			if !ok1 || !ok2 {
				warnings = append(warnings, fmt.Sprintf("group_sub_group %d→%d name unresolved, skipped", s.GroupID, s.SubGroupID))
				continue
			}
			if _, isStd := standardSet[s.SubGroupID]; !isStd {
				// 只有 standard 在 sub 侧的关系才被备份
				warnings = append(warnings, fmt.Sprintf("group_sub_group sub %s not standard, skipped", subName))
				continue
			}
			out.Data.GroupSubGroups = append(out.Data.GroupSubGroups, GroupSubGroupDTO{
				ParentName: parentName, SubGroupName: subName, Weight: s.Weight,
			})
		}

		return nil
	})

	if err != nil {
		return nil, warnings, err
	}
	return out, warnings, nil
}

func groupModelToDTO(g *models.Group) GroupDTO {
	dto := GroupDTO{
		Name:                g.Name,
		DisplayName:         g.DisplayName,
		ProxyKeys:           g.ProxyKeys,
		Description:         g.Description,
		GroupType:           g.GroupType,
		IsSystem:            false, // export 端永远抹平
		SystemRole:          g.SystemRole,
		ValidationEndpoint:  g.ValidationEndpoint,
		ChannelType:         g.ChannelType,
		Sort:                g.Sort,
		TestModel:           g.TestModel,
		ParamOverrides:      g.ParamOverrides,
		Config:              g.Config,
		ModelRedirectRules:  g.ModelRedirectRules,
		ModelRedirectStrict: g.ModelRedirectStrict,
		ModelRoutingMode:    g.ModelRoutingMode,
	}
	if g.Upstreams != nil {
		dto.Upstreams = jsonRaw(g.Upstreams)
	}
	if g.HeaderRules != nil {
		dto.HeaderRules = jsonRaw(g.HeaderRules)
	}
	if g.ExposedModels != nil {
		dto.ExposedModels = jsonRaw(g.ExposedModels)
	}
	if g.BlockedModels != nil {
		dto.BlockedModels = jsonRaw(g.BlockedModels)
	}
	return dto
}

// jsonRaw 把 datatypes.JSON ([]byte) 转成 any 以便 json.Marshal 原样输出。
func jsonRaw(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	// 用 json.RawMessage 让上层 marshal 时按原始结构输出
	rm := make([]byte, len(b))
	copy(rm, b)
	return jsonRawMessage(rm)
}

type jsonRawMessage []byte

func (m jsonRawMessage) MarshalJSON() ([]byte, error) {
	if len(m) == 0 {
		return []byte("null"), nil
	}
	return []byte(m), nil
}
```

- [ ] **Step 4: 跑测试**

Run: `go test ./internal/backup/ -v`
Expected: all PASS.

- [ ] **Step 5: 提交**

```bash
git add internal/backup/exporter.go internal/backup/exporter_test.go
git commit -m "$(cat <<'EOF'
✨ feat(backup): Exporter 单事务从 DB 构建 BackupV1

跳过 is_system 分组；逐 key 解密为明文写入 payload；解密失败的 key
转 warning 而非整体失败；GroupSubGroup 两端均做 name 解析。

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Importer `internal/backup/importer.go`

**Files:**
- Create: `internal/backup/importer.go`
- Test: `internal/backup/importer_test.go`

- [ ] **Step 1: 写测试 — merge/skip/replace 三策略**

```go
// internal/backup/importer_test.go
package backup

import (
	"context"
	"testing"

	"autogateway/internal/models"
)

func sampleBackup() *BackupV1 {
	return &BackupV1{
		SchemaVersion: 1,
		ExportedBy:    "test",
		Data: DataV1{
			SystemSettings: []SystemSettingDTO{
				{SettingKey: "app_url", SettingValue: "https://imported"},
			},
			Groups: []GroupDTO{
				{Name: "openai-main", ChannelType: "openai", TestModel: "gpt-4o-mini", GroupType: "standard"},
			},
			APIKeys: []APIKeyDTO{
				{GroupName: "openai-main", KeyValue: "sk-new", Status: "active"},
			},
			ModelAliases: []ModelAliasDTO{
				{Alias: "fast", GroupName: "openai-main", RealModel: "gpt-4o-mini", Weight: 1, Priority: 100, Enabled: true},
			},
		},
	}
}

func TestImport_MergeIntoEmptyDB(t *testing.T) {
	db := newTestDB(t)
	imp := NewImporter(db, fakeEncSvc{})
	rep, err := imp.Import(context.Background(), sampleBackup(), StrategyMerge)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if rep.Applied.Groups != 1 || rep.Applied.APIKeys != 1 {
		t.Errorf("applied counts: %+v", rep.Applied)
	}
	var got []models.APIKey
	db.Find(&got)
	if len(got) != 1 || got[0].KeyValue != "enc:sk-new" {
		t.Errorf("key should be re-encrypted, got %+v", got)
	}
	if got[0].KeyHash == "" {
		t.Errorf("key_hash should be filled")
	}
}

func TestImport_MergeUpdatesExistingGroup(t *testing.T) {
	db := newTestDB(t)
	db.Create(&models.Group{Name: "openai-main", ChannelType: "openai", TestModel: "old", DisplayName: "Old"})

	bk := sampleBackup()
	bk.Data.Groups[0].DisplayName = "New"
	bk.Data.Groups[0].TestModel = "gpt-4o-mini"

	imp := NewImporter(db, fakeEncSvc{})
	if _, err := imp.Import(context.Background(), bk, StrategyMerge); err != nil {
		t.Fatal(err)
	}
	var g models.Group
	db.Where("name = ?", "openai-main").First(&g)
	if g.DisplayName != "New" || g.TestModel != "gpt-4o-mini" {
		t.Errorf("merge should overwrite fields: %+v", g)
	}
}

func TestImport_SkipPreservesExisting(t *testing.T) {
	db := newTestDB(t)
	db.Create(&models.Group{Name: "openai-main", ChannelType: "openai", TestModel: "keep-me", DisplayName: "Keep"})

	imp := NewImporter(db, fakeEncSvc{})
	rep, err := imp.Import(context.Background(), sampleBackup(), StrategySkip)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Skipped.Groups != 1 {
		t.Errorf("expected 1 group skipped, got %+v", rep.Skipped)
	}
	var g models.Group
	db.Where("name = ?", "openai-main").First(&g)
	if g.TestModel != "keep-me" {
		t.Errorf("skip must not overwrite: %+v", g)
	}
	// keys/aliases for this group must also be skipped
	var kc int64
	db.Model(&models.APIKey{}).Count(&kc)
	if kc != 0 {
		t.Errorf("keys must be skipped under same-name group skip, got %d", kc)
	}
}

func TestImport_ReplaceWipesStandardGroups(t *testing.T) {
	db := newTestDB(t)
	// pre-existing standard + its key + alias
	g := models.Group{Name: "old-grp", ChannelType: "openai", TestModel: "m"}
	db.Create(&g)
	db.Create(&models.APIKey{GroupID: g.ID, KeyValue: "enc:old", KeyHash: "ho"})
	db.Create(&models.ModelAlias{Alias: "a", GroupID: g.ID, RealModel: "m", Weight: 1, Priority: 100, Enabled: true})
	// pre-existing system aggregate (must survive)
	sys := models.Group{Name: "default-openai", ChannelType: "openai", TestModel: "m", IsSystem: true, GroupType: "aggregate"}
	db.Create(&sys)

	imp := NewImporter(db, fakeEncSvc{})
	if _, err := imp.Import(context.Background(), sampleBackup(), StrategyReplace); err != nil {
		t.Fatal(err)
	}
	var groups []models.Group
	db.Find(&groups)
	if len(groups) != 2 {
		t.Errorf("expected 1 sys + 1 imported standard, got %d (%+v)", len(groups), groups)
	}
	var oldCount int64
	db.Model(&models.Group{}).Where("name = ?", "old-grp").Count(&oldCount)
	if oldCount != 0 {
		t.Errorf("old standard group must be wiped under replace")
	}
}

func TestImport_SkipsSystemGroupsAndEncryptionKey(t *testing.T) {
	db := newTestDB(t)
	bk := sampleBackup()
	bk.Data.Groups = append(bk.Data.Groups, GroupDTO{Name: "default-openai", IsSystem: true, ChannelType: "openai", TestModel: "m"})
	bk.Data.SystemSettings = append(bk.Data.SystemSettings, SystemSettingDTO{SettingKey: "encryption_key", SettingValue: "hacked"})

	imp := NewImporter(db, fakeEncSvc{})
	rep, err := imp.Import(context.Background(), bk, StrategyMerge)
	if err != nil {
		t.Fatal(err)
	}
	var sysCount int64
	db.Model(&models.Group{}).Where("is_system = ?", true).Count(&sysCount)
	if sysCount != 0 {
		t.Errorf("is_system group must not be inserted from backup")
	}
	var s models.SystemSetting
	if err := db.Where("setting_key = ?", "encryption_key").First(&s).Error; err == nil {
		t.Errorf("encryption_key must not be imported")
	}
	_ = rep
}
```

- [ ] **Step 2: 跑测试**

Run: `go test ./internal/backup/ -run TestImport -v`
Expected: FAIL (`NewImporter` undefined).

- [ ] **Step 3: 实现**

```go
// internal/backup/importer.go
package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"autogateway/internal/encryption"
	"autogateway/internal/models"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Counts 是 import 报告的分项计数。
type Counts struct {
	SystemSettings int `json:"system_settings"`
	Groups         int `json:"groups"`
	APIKeys        int `json:"api_keys"`
	ModelAliases   int `json:"model_aliases"`
	GroupSubGroups int `json:"group_sub_groups"`
}

// Report 是 Import 返回的完整结果。
type Report struct {
	Applied   Counts   `json:"applied"`
	Skipped   Counts   `json:"skipped"`
	Warnings  []string `json:"warnings"`
	ElapsedMs int64    `json:"elapsed_ms"`
}

// Importer 把 BackupV1 写入数据库。
type Importer struct {
	db  *gorm.DB
	enc encryption.Service
}

func NewImporter(db *gorm.DB, enc encryption.Service) *Importer {
	return &Importer{db: db, enc: enc}
}

// Import 单事务写入，按 strategy 处理冲突。
func (i *Importer) Import(ctx context.Context, bk *BackupV1, strat Strategy) (*Report, error) {
	if bk == nil {
		return nil, errors.New("nil backup payload")
	}
	if bk.SchemaVersion != CurrentSchemaVersion {
		return nil, fmt.Errorf("unsupported schema_version %d (supported: %d)", bk.SchemaVersion, CurrentSchemaVersion)
	}

	rep := &Report{}
	start := time.Now()

	err := i.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// replace 模式先清空 standard groups (cascade keys/aliases/sub-rels)
		if strat == StrategyReplace {
			if err := wipeStandardGroups(tx); err != nil {
				return err
			}
		}

		// 1. SystemSettings (跳过 encryption_key)
		for _, s := range bk.Data.SystemSettings {
			if s.SettingKey == "encryption_key" {
				rep.Warnings = append(rep.Warnings, "system_settings.encryption_key skipped (protected)")
				rep.Skipped.SystemSettings++
				continue
			}
			applied, err := upsertSystemSetting(tx, s, strat)
			if err != nil {
				return err
			}
			if applied {
				rep.Applied.SystemSettings++
			} else {
				rep.Skipped.SystemSettings++
			}
		}

		// 2. Groups (跳过 is_system)
		groupIDByName := make(map[string]uint, len(bk.Data.Groups))
		// 把当前 DB 里所有 group 的 name→id 索引一遍（含系统聚合，用于 sub_groups 查找）
		var existing []models.Group
		if err := tx.Find(&existing).Error; err != nil {
			return err
		}
		for _, g := range existing {
			groupIDByName[g.Name] = g.ID
		}
		skippedGroupNames := make(map[string]struct{}) // skip 模式下被跳过的，下面 keys/aliases 也要跳

		for _, dto := range bk.Data.Groups {
			if dto.IsSystem {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("group %q skipped (is_system=true)", dto.Name))
				rep.Skipped.Groups++
				continue
			}
			id, applied, err := upsertGroup(tx, dto, strat)
			if err != nil {
				return err
			}
			if applied {
				rep.Applied.Groups++
				groupIDByName[dto.Name] = id
			} else {
				rep.Skipped.Groups++
				skippedGroupNames[dto.Name] = struct{}{}
			}
		}

		// 3. APIKeys
		for _, k := range bk.Data.APIKeys {
			if _, skipped := skippedGroupNames[k.GroupName]; skipped {
				rep.Skipped.APIKeys++
				continue
			}
			gid, ok := groupIDByName[k.GroupName]
			if !ok {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("api_key references unknown group %q", k.GroupName))
				rep.Skipped.APIKeys++
				continue
			}
			applied, err := upsertAPIKey(tx, k, gid, i.enc, strat)
			if err != nil {
				return err
			}
			if applied {
				rep.Applied.APIKeys++
			} else {
				rep.Skipped.APIKeys++
			}
		}

		// 4. ModelAliases
		for _, a := range bk.Data.ModelAliases {
			if _, skipped := skippedGroupNames[a.GroupName]; skipped {
				rep.Skipped.ModelAliases++
				continue
			}
			gid, ok := groupIDByName[a.GroupName]
			if !ok {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("alias %q references unknown group %q", a.Alias, a.GroupName))
				rep.Skipped.ModelAliases++
				continue
			}
			applied, err := upsertAlias(tx, a, gid, strat)
			if err != nil {
				return err
			}
			if applied {
				rep.Applied.ModelAliases++
			} else {
				rep.Skipped.ModelAliases++
			}
		}

		// 5. GroupSubGroups
		for _, s := range bk.Data.GroupSubGroups {
			parentID, okP := groupIDByName[s.ParentName]
			subID, okS := groupIDByName[s.SubGroupName]
			if !okP || !okS {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("group_sub_group %q→%q: name unresolved", s.ParentName, s.SubGroupName))
				rep.Skipped.GroupSubGroups++
				continue
			}
			applied, err := upsertSubGroup(tx, parentID, subID, s.Weight, strat)
			if err != nil {
				return err
			}
			if applied {
				rep.Applied.GroupSubGroups++
			} else {
				rep.Skipped.GroupSubGroups++
			}
		}

		return nil
	})

	rep.ElapsedMs = time.Since(start).Milliseconds()
	if err != nil {
		return nil, err
	}
	return rep, nil
}

func wipeStandardGroups(tx *gorm.DB) error {
	// 找所有 is_system=false 的 id
	var ids []uint
	if err := tx.Model(&models.Group{}).Where("is_system = ?", false).Pluck("id", &ids).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	if err := tx.Where("group_id IN ?", ids).Delete(&models.APIKey{}).Error; err != nil {
		return err
	}
	if err := tx.Where("group_id IN ?", ids).Delete(&models.ModelAlias{}).Error; err != nil {
		return err
	}
	if err := tx.Where("group_id IN ? OR sub_group_id IN ?", ids, ids).Delete(&models.GroupSubGroup{}).Error; err != nil {
		return err
	}
	if err := tx.Where("id IN ?", ids).Delete(&models.Group{}).Error; err != nil {
		return err
	}
	return nil
}

func upsertSystemSetting(tx *gorm.DB, s SystemSettingDTO, strat Strategy) (bool, error) {
	var existing models.SystemSetting
	err := tx.Where("setting_key = ?", s.SettingKey).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true, tx.Create(&models.SystemSetting{
			SettingKey: s.SettingKey, SettingValue: s.SettingValue, Description: s.Description,
		}).Error
	}
	if err != nil {
		return false, err
	}
	if strat == StrategySkip {
		return false, nil
	}
	existing.SettingValue = s.SettingValue
	existing.Description = s.Description
	return true, tx.Save(&existing).Error
}

func upsertGroup(tx *gorm.DB, dto GroupDTO, strat Strategy) (uint, bool, error) {
	var existing models.Group
	err := tx.Where("name = ?", dto.Name).First(&existing).Error
	newRow := dtoToGroupModel(dto)

	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := tx.Create(&newRow).Error; err != nil {
			return 0, false, err
		}
		return newRow.ID, true, nil
	}
	if err != nil {
		return 0, false, err
	}
	if strat == StrategySkip {
		return existing.ID, false, nil
	}
	newRow.ID = existing.ID
	if err := tx.Model(&existing).Updates(newRow).Error; err != nil {
		return 0, false, err
	}
	return existing.ID, true, nil
}

func dtoToGroupModel(dto GroupDTO) models.Group {
	g := models.Group{
		Name:                dto.Name,
		DisplayName:         dto.DisplayName,
		ProxyKeys:           dto.ProxyKeys,
		Description:         dto.Description,
		GroupType:           dto.GroupType,
		IsSystem:            false, // 强制
		SystemRole:          dto.SystemRole,
		ValidationEndpoint:  dto.ValidationEndpoint,
		ChannelType:         dto.ChannelType,
		Sort:                dto.Sort,
		TestModel:           dto.TestModel,
		ParamOverrides:      datatypes.JSONMap(dto.ParamOverrides),
		Config:              datatypes.JSONMap(dto.Config),
		ModelRedirectRules:  datatypes.JSONMap(dto.ModelRedirectRules),
		ModelRedirectStrict: dto.ModelRedirectStrict,
		ModelRoutingMode:    dto.ModelRoutingMode,
	}
	if dto.Upstreams != nil {
		if raw, err := json.Marshal(dto.Upstreams); err == nil {
			g.Upstreams = datatypes.JSON(raw)
		}
	}
	if g.Upstreams == nil {
		g.Upstreams = datatypes.JSON([]byte("[]"))
	}
	if dto.HeaderRules != nil {
		if raw, err := json.Marshal(dto.HeaderRules); err == nil {
			g.HeaderRules = datatypes.JSON(raw)
		}
	}
	if dto.ExposedModels != nil {
		if raw, err := json.Marshal(dto.ExposedModels); err == nil {
			g.ExposedModels = datatypes.JSON(raw)
		}
	}
	if dto.BlockedModels != nil {
		if raw, err := json.Marshal(dto.BlockedModels); err == nil {
			g.BlockedModels = datatypes.JSON(raw)
		}
	}
	return g
}

func hashKey(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

func upsertAPIKey(tx *gorm.DB, k APIKeyDTO, gid uint, enc encryption.Service, strat Strategy) (bool, error) {
	hash := hashKey(k.KeyValue)
	var existing models.APIKey
	err := tx.Where("group_id = ? AND key_hash = ?", gid, hash).First(&existing).Error

	enced, encErr := enc.Encrypt(k.KeyValue)
	if encErr != nil {
		return false, fmt.Errorf("encrypt key: %w", encErr)
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true, tx.Create(&models.APIKey{
			GroupID: gid, KeyValue: enced, KeyHash: hash,
			Status: k.Status, Notes: k.Notes,
		}).Error
	}
	if err != nil {
		return false, err
	}
	if strat == StrategySkip {
		return false, nil
	}
	existing.Status = k.Status
	existing.Notes = k.Notes
	return true, tx.Save(&existing).Error
}

func upsertAlias(tx *gorm.DB, a ModelAliasDTO, gid uint, strat Strategy) (bool, error) {
	var existing models.ModelAlias
	err := tx.Where("group_id = ? AND alias = ? AND real_model = ?", gid, a.Alias, a.RealModel).First(&existing).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true, tx.Create(&models.ModelAlias{
			Alias: a.Alias, GroupID: gid, RealModel: a.RealModel,
			Weight: a.Weight, Priority: a.Priority,
			Enabled: a.Enabled, IsReserved: a.IsReserved,
		}).Error
	}
	if err != nil {
		return false, err
	}
	if strat == StrategySkip {
		return false, nil
	}
	existing.Weight = a.Weight
	existing.Priority = a.Priority
	existing.Enabled = a.Enabled
	return true, tx.Save(&existing).Error
}

func upsertSubGroup(tx *gorm.DB, parentID, subID uint, weight int, strat Strategy) (bool, error) {
	var existing models.GroupSubGroup
	err := tx.Where("group_id = ? AND sub_group_id = ?", parentID, subID).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true, tx.Create(&models.GroupSubGroup{
			GroupID: parentID, SubGroupID: subID, Weight: weight,
		}).Error
	}
	if err != nil {
		return false, err
	}
	if strat == StrategySkip {
		return false, nil
	}
	existing.Weight = weight
	return true, tx.Save(&existing).Error
}
```

- [ ] **Step 4: 跑测试**

Run: `go test ./internal/backup/ -v`
Expected: all PASS.

- [ ] **Step 5: 提交**

```bash
git add internal/backup/importer.go internal/backup/importer_test.go
git commit -m "$(cat <<'EOF'
✨ feat(backup): Importer 单事务三策略 (merge/skip/replace)

按业务键 upsert：(group.name) / (group_id,key_hash) /
(group_id,alias,real_model) / (group_id,sub_group_id)。硬规则：
跳过 is_system 与 encryption_key。replace 模式先清空 standard。
导入时用目标实例 ENCRYPTION_KEY 重新加密 + 重算 key_hash。

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Service 装配 `internal/backup/service.go`

**Files:**
- Create: `internal/backup/service.go`
- Test: `internal/backup/service_test.go`

`Service` 把 codec + exporter + importer 拼起来，并管理 preview token。

- [ ] **Step 1: 写测试 — 完整 E2E round-trip**

```go
// internal/backup/service_test.go
package backup

import (
	"context"
	"testing"

	"autogateway/internal/models"

	"gorm.io/datatypes"
)

func TestService_E2E_ExportThenImportToFreshDB(t *testing.T) {
	srcDB := newTestDB(t)
	g := models.Group{Name: "openai-main", ChannelType: "openai", TestModel: "gpt-4o-mini", Upstreams: datatypes.JSON("[]")}
	srcDB.Create(&g)
	srcDB.Create(&models.APIKey{GroupID: g.ID, KeyValue: "enc:sk-secret", KeyHash: "h1", Status: "active"})
	srcDB.Create(&models.ModelAlias{Alias: "fast", GroupID: g.ID, RealModel: "gpt-4o-mini", Weight: 1, Priority: 100, Enabled: true})

	srcSvc := NewService(srcDB, fakeEncSvc{}, "test")
	blob, _, err := srcSvc.Export(context.Background(), "pw")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	dstDB := newTestDB(t)
	dstSvc := NewService(dstDB, fakeEncSvc{}, "test")
	tok, _, err := dstSvc.Preview(context.Background(), blob, "pw")
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	rep, err := dstSvc.Import(context.Background(), blob, "pw", StrategyMerge, tok)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if rep.Applied.Groups != 1 || rep.Applied.APIKeys != 1 || rep.Applied.ModelAliases != 1 {
		t.Errorf("applied: %+v", rep.Applied)
	}
	var keys []models.APIKey
	dstDB.Find(&keys)
	if len(keys) != 1 || keys[0].KeyValue != "enc:sk-secret" {
		t.Errorf("re-encrypted key mismatch: %+v", keys)
	}
}

func TestService_Preview_WrongPassword(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db, fakeEncSvc{}, "t")
	blob, _, err := svc.Export(context.Background(), "right")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Preview(context.Background(), blob, "wrong"); err == nil {
		t.Fatal("expected wrong-password error")
	}
}

func TestService_Import_StaleToken(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db, fakeEncSvc{}, "t")
	blob, _, _ := svc.Export(context.Background(), "p")
	if _, err := svc.Import(context.Background(), blob, "p", StrategyMerge, "bogus-token"); err == nil {
		t.Fatal("expected stale-token error")
	}
}
```

- [ ] **Step 2: 跑测试**

Run: `go test ./internal/backup/ -run TestService -v`
Expected: FAIL.

- [ ] **Step 3: 实现**

```go
// internal/backup/service.go
package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"autogateway/internal/encryption"

	"gorm.io/gorm"
)

// PreviewReport 是 Preview 返回的预检报告。
type PreviewReport struct {
	SchemaVersion       int               `json:"schema_version"`
	ExportedAt          time.Time         `json:"exported_at"`
	ExportedBy          string            `json:"exported_by"`
	Counts              Counts            `json:"counts"`
	Conflicts           ConflictReport    `json:"conflicts"`
	WillDeleteIfReplace Counts            `json:"will_delete_if_replace"`
	Warnings            []string          `json:"warnings"`
	ConfirmToken        string            `json:"confirm_token"`
}

// ConflictReport 列出与本地已存在记录冲突的业务键。
type ConflictReport struct {
	Groups          []string `json:"groups"`
	APIKeysByHash   int      `json:"api_keys_by_hash"`
	Aliases         int      `json:"aliases"`
	SystemSettings  []string `json:"system_settings"`
	GroupSubGroups  int      `json:"group_sub_groups"`
}

// Service 是 backup 包的对外门面。
type Service struct {
	db      *gorm.DB
	enc     encryption.Service
	version string

	tokensMu sync.Mutex
	tokens   map[string]tokenEntry
}

type tokenEntry struct {
	blobHash string
	expires  time.Time
}

const previewTokenTTL = 10 * time.Minute

func NewService(db *gorm.DB, enc encryption.Service, version string) *Service {
	return &Service{
		db:      db,
		enc:     enc,
		version: version,
		tokens:  make(map[string]tokenEntry),
	}
}

// Export 返回加密后的 .acb 字节流。
func (s *Service) Export(ctx context.Context, password string) ([]byte, []string, error) {
	if password == "" {
		return nil, nil, errors.New("password required")
	}
	exp := NewExporter(s.db, s.enc, s.version)
	bk, warns, err := exp.Export(ctx)
	if err != nil {
		return nil, warns, err
	}
	payload, err := json.Marshal(bk)
	if err != nil {
		return nil, warns, fmt.Errorf("marshal payload: %w", err)
	}
	blob, err := EncodeContainer(payload, password)
	if err != nil {
		return nil, warns, err
	}
	return blob, warns, nil
}

// Preview 解密 + 解析 payload 但不写库，返回预检报告与 confirm_token。
func (s *Service) Preview(ctx context.Context, blob []byte, password string) (string, *PreviewReport, error) {
	bk, err := s.decode(blob, password)
	if err != nil {
		return "", nil, err
	}

	rep := &PreviewReport{
		SchemaVersion: bk.SchemaVersion,
		ExportedAt:    bk.ExportedAt,
		ExportedBy:    bk.ExportedBy,
		Counts: Counts{
			SystemSettings: len(bk.Data.SystemSettings),
			Groups:         len(bk.Data.Groups),
			APIKeys:        len(bk.Data.APIKeys),
			ModelAliases:   len(bk.Data.ModelAliases),
			GroupSubGroups: len(bk.Data.GroupSubGroups),
		},
	}
	if err := s.fillConflictsAndDeletes(ctx, bk, rep); err != nil {
		return "", nil, err
	}

	tok, err := s.mintToken(blob)
	if err != nil {
		return "", nil, err
	}
	rep.ConfirmToken = tok
	return tok, rep, nil
}

// Import 校验 token + 重解密 + 调 Importer。
func (s *Service) Import(ctx context.Context, blob []byte, password string, strat Strategy, token string) (*Report, error) {
	if !s.consumeToken(token, blob) {
		return nil, errors.New("stale or invalid confirm_token")
	}
	bk, err := s.decode(blob, password)
	if err != nil {
		return nil, err
	}
	imp := NewImporter(s.db, s.enc)
	return imp.Import(ctx, bk, strat)
}

func (s *Service) decode(blob []byte, password string) (*BackupV1, error) {
	payload, err := DecodeContainer(blob, password)
	if err != nil {
		return nil, err
	}
	var bk BackupV1
	if err := json.Unmarshal(payload, &bk); err != nil {
		return nil, fmt.Errorf("malformed payload: %w", err)
	}
	if bk.SchemaVersion != CurrentSchemaVersion {
		return nil, fmt.Errorf("unsupported schema_version %d (supported: %d)", bk.SchemaVersion, CurrentSchemaVersion)
	}
	return &bk, nil
}

func (s *Service) fillConflictsAndDeletes(ctx context.Context, bk *BackupV1, rep *PreviewReport) error {
	db := s.db.WithContext(ctx)

	// will_delete_if_replace = 当前 DB 中 is_system=false 的实体数
	var standardIDs []uint
	if err := db.Table("groups").Where("is_system = ?", false).Pluck("id", &standardIDs).Error; err != nil {
		return err
	}
	rep.WillDeleteIfReplace.Groups = len(standardIDs)
	if len(standardIDs) > 0 {
		var n int64
		db.Table("api_keys").Where("group_id IN ?", standardIDs).Count(&n)
		rep.WillDeleteIfReplace.APIKeys = int(n)
		db.Table("model_aliases").Where("group_id IN ?", standardIDs).Count(&n)
		rep.WillDeleteIfReplace.ModelAliases = int(n)
		db.Table("group_sub_groups").Where("group_id IN ? OR sub_group_id IN ?", standardIDs, standardIDs).Count(&n)
		rep.WillDeleteIfReplace.GroupSubGroups = int(n)
	}

	// conflicts.groups
	groupNames := make([]string, 0, len(bk.Data.Groups))
	for _, g := range bk.Data.Groups {
		if !g.IsSystem {
			groupNames = append(groupNames, g.Name)
		}
	}
	if len(groupNames) > 0 {
		var hits []string
		db.Table("groups").Where("name IN ?", groupNames).Pluck("name", &hits)
		rep.Conflicts.Groups = hits
	}

	// conflicts.system_settings
	keys := make([]string, 0, len(bk.Data.SystemSettings))
	for _, s := range bk.Data.SystemSettings {
		if s.SettingKey != "encryption_key" {
			keys = append(keys, s.SettingKey)
		}
	}
	if len(keys) > 0 {
		var hits []string
		db.Table("system_settings").Where("setting_key IN ?", keys).Pluck("setting_key", &hits)
		rep.Conflicts.SystemSettings = hits
	}

	// conflicts.api_keys_by_hash
	if len(bk.Data.APIKeys) > 0 {
		hashes := make([]string, 0, len(bk.Data.APIKeys))
		for _, k := range bk.Data.APIKeys {
			hashes = append(hashes, hashKey(k.KeyValue))
		}
		var n int64
		db.Table("api_keys").Where("key_hash IN ?", hashes).Count(&n)
		rep.Conflicts.APIKeysByHash = int(n)
	}

	// conflicts.aliases (粗略：同 alias+real_model 即视为可能冲突，不精确到 group_id 因 name 还没解析)
	if len(bk.Data.ModelAliases) > 0 {
		aliasNames := make([]string, 0, len(bk.Data.ModelAliases))
		for _, a := range bk.Data.ModelAliases {
			aliasNames = append(aliasNames, a.Alias)
		}
		var n int64
		db.Table("model_aliases").Where("alias IN ?", aliasNames).Count(&n)
		rep.Conflicts.Aliases = int(n)
	}

	return nil
}

func (s *Service) mintToken(blob []byte) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(buf)

	s.tokensMu.Lock()
	defer s.tokensMu.Unlock()
	s.gcTokensLocked()
	s.tokens[tok] = tokenEntry{
		blobHash: hashKey(string(blob)),
		expires:  time.Now().Add(previewTokenTTL),
	}
	return tok, nil
}

func (s *Service) consumeToken(tok string, blob []byte) bool {
	s.tokensMu.Lock()
	defer s.tokensMu.Unlock()
	s.gcTokensLocked()
	entry, ok := s.tokens[tok]
	if !ok {
		return false
	}
	delete(s.tokens, tok)
	if entry.blobHash != hashKey(string(blob)) {
		return false
	}
	if time.Now().After(entry.expires) {
		return false
	}
	return true
}

func (s *Service) gcTokensLocked() {
	now := time.Now()
	for k, v := range s.tokens {
		if now.After(v.expires) {
			delete(s.tokens, k)
		}
	}
}
```

- [ ] **Step 4: 跑测试**

Run: `go test ./internal/backup/ -v`
Expected: all PASS.

- [ ] **Step 5: 提交**

```bash
git add internal/backup/service.go internal/backup/service_test.go
git commit -m "$(cat <<'EOF'
✨ feat(backup): Service 装配 + Preview 接口 + 一次性 confirm_token

Service 串联 codec/exporter/importer，提供 Export / Preview / Import
三个方法。Preview 返回 conflicts/will_delete_if_replace 报告与 10
分钟一次性 token，Import 校验后才允许写库。

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: Handler `internal/handler/backup_handler.go`

**Files:**
- Create: `internal/handler/backup_handler.go`
- Test: `internal/handler/backup_handler_test.go`

- [ ] **Step 1: 写测试**

```go
// internal/handler/backup_handler_test.go
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"autogateway/internal/backup"
	"autogateway/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func newTestServerWithBackup(t *testing.T) (*gin.Engine, *gorm.DB, *BackupHandler) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	db.AutoMigrate(&models.SystemSetting{}, &models.Group{}, &models.GroupSubGroup{}, &models.APIKey{}, &models.ModelAlias{})
	enc := stubEnc{}
	svc := backup.NewService(db, enc, "test")
	h := NewBackupHandler(svc)
	r := gin.New()
	r.POST("/api/admin/backup/export", h.Export)
	r.POST("/api/admin/backup/preview", h.Preview)
	r.POST("/api/admin/backup/import", h.Import)
	return r, db, h
}

type stubEnc struct{}

func (stubEnc) Encrypt(s string) (string, error) { return "enc:" + s, nil }
func (stubEnc) Decrypt(s string) (string, error) {
	if !strings.HasPrefix(s, "enc:") {
		return "", io.EOF
	}
	return s[4:], nil
}

func TestBackupHandler_ExportThenImport(t *testing.T) {
	r, srcDB, _ := newTestServerWithBackup(t)
	g := models.Group{Name: "x", ChannelType: "openai", TestModel: "m", Upstreams: datatypes.JSON("[]")}
	srcDB.Create(&g)

	// 1. export
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/admin/backup/export", strings.NewReader(`{"password":"p"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("export status %d body=%s", w.Code, w.Body.String())
	}
	blob := w.Body.Bytes()
	if !bytes.HasPrefix(blob, []byte("ACB1")) {
		t.Fatal("not an .acb")
	}

	// 2. preview
	pw, tok := uploadBackup(t, r, "/api/admin/backup/preview", blob, "p", "")
	if pw.Code != http.StatusOK {
		t.Fatalf("preview status %d body=%s", pw.Code, pw.Body.String())
	}
	if tok == "" {
		t.Fatal("missing confirm_token")
	}

	// 3. import (merge)
	iw, _ := uploadBackup(t, r, "/api/admin/backup/import", blob, "p", tok+"|merge")
	if iw.Code != http.StatusOK {
		t.Fatalf("import status %d body=%s", iw.Code, iw.Body.String())
	}
}

func TestBackupHandler_BadPassword(t *testing.T) {
	r, _, _ := newTestServerWithBackup(t)

	// export with one password
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/admin/backup/export", strings.NewReader(`{"password":"right"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	blob := w.Body.Bytes()

	pw, _ := uploadBackup(t, r, "/api/admin/backup/preview", blob, "wrong", "")
	if pw.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", pw.Code)
	}
}

// uploadBackup posts a multipart body with file + password [+ strategy & confirm_token].
// extra is "<token>|<strategy>" when posting to /import; pass "" otherwise.
// Returns the recorder and the confirm_token parsed from a JSON response (for preview).
func uploadBackup(t *testing.T, r *gin.Engine, path string, blob []byte, password, extra string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "backup.acb")
	fw.Write(blob)
	mw.WriteField("password", password)
	if extra != "" {
		parts := strings.SplitN(extra, "|", 2)
		mw.WriteField("confirm_token", parts[0])
		if len(parts) > 1 {
			mw.WriteField("strategy", parts[1])
		}
	}
	mw.Close()

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	r.ServeHTTP(w, req)

	if w.Code == http.StatusOK && strings.Contains(w.Header().Get("Content-Type"), "application/json") {
		var p struct{ ConfirmToken string `json:"confirm_token"` }
		if err := json.Unmarshal(w.Body.Bytes(), &p); err == nil {
			return w, p.ConfirmToken
		}
	}
	return w, ""
}

var _ = context.Background
```

- [ ] **Step 2: 跑测试**

Run: `go test ./internal/handler/ -run TestBackup -v`
Expected: FAIL (`BackupHandler` / `NewBackupHandler` undefined).

- [ ] **Step 3: 实现**

```go
// internal/handler/backup_handler.go
package handler

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"autogateway/internal/backup"
	"autogateway/internal/config"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// BackupHandler 暴露 /api/admin/backup/* 接口。
type BackupHandler struct {
	svc             *backup.Service
	settingsManager *config.SystemSettingsManager
}

func NewBackupHandler(svc *backup.Service) *BackupHandler {
	return &BackupHandler{svc: svc}
}

// WithSettingsManager 让 handler 能读 app_url 拼文件名（构造时未必有，可选）。
func (h *BackupHandler) WithSettingsManager(m *config.SystemSettingsManager) *BackupHandler {
	h.settingsManager = m
	return h
}

type exportRequest struct {
	Password string `json:"password" binding:"required"`
}

// Export POST /api/admin/backup/export
func (h *BackupHandler) Export(c *gin.Context) {
	var req exportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "MISSING_PASSWORD"})
		return
	}
	blob, warns, err := h.svc.Export(c.Request.Context(), req.Password)
	if err != nil {
		logrus.WithError(err).Error("backup.export failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "EXPORT_FAILED", "detail": err.Error()})
		return
	}
	for _, w := range warns {
		logrus.Warnf("backup.export warning: %s", w)
	}
	host := h.hostSlug()
	fname := fmt.Sprintf("api-center-backup-%s-%s.acb", host, time.Now().UTC().Format("20060102-150405"))
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", `attachment; filename="`+fname+`"`)
	logrus.Infof("event=backup.export bytes=%d", len(blob))
	io.Copy(c.Writer, bytes.NewReader(blob))
}

// Preview POST /api/admin/backup/preview (multipart: file, password)
func (h *BackupHandler) Preview(c *gin.Context) {
	blob, password, err := readMultipart(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "BAD_REQUEST", "detail": err.Error()})
		return
	}
	_, rep, err := h.svc.Preview(c.Request.Context(), blob, password)
	if err != nil {
		c.JSON(classifyDecodeError(err), gin.H{"error": classifyErrorCode(err), "detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rep)
}

// Import POST /api/admin/backup/import (multipart: file, password, strategy, confirm_token)
func (h *BackupHandler) Import(c *gin.Context) {
	blob, password, err := readMultipart(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "BAD_REQUEST", "detail": err.Error()})
		return
	}
	stratRaw := c.PostForm("strategy")
	tok := c.PostForm("confirm_token")
	strat, ok := backup.ParseStrategy(stratRaw)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "BAD_STRATEGY", "detail": stratRaw})
		return
	}
	rep, err := h.svc.Import(c.Request.Context(), blob, password, strat, tok)
	if err != nil {
		if strings.Contains(err.Error(), "stale") {
			c.JSON(http.StatusConflict, gin.H{"error": "STALE_PREVIEW", "detail": err.Error()})
			return
		}
		c.JSON(classifyDecodeError(err), gin.H{"error": classifyErrorCode(err), "detail": err.Error()})
		return
	}
	logrus.Infof("event=backup.import applied=%+v skipped=%+v", rep.Applied, rep.Skipped)
	c.JSON(http.StatusOK, rep)
}

func readMultipart(c *gin.Context) ([]byte, string, error) {
	if err := c.Request.ParseMultipartForm(32 << 20); err != nil {
		return nil, "", err
	}
	file, _, err := c.Request.FormFile("file")
	if err != nil {
		return nil, "", errors.New("file required")
	}
	defer file.Close()
	blob, err := io.ReadAll(file)
	if err != nil {
		return nil, "", err
	}
	pw := c.PostForm("password")
	if pw == "" {
		return nil, "", errors.New("password required")
	}
	return blob, pw, nil
}

func classifyDecodeError(err error) int {
	if err == nil {
		return http.StatusInternalServerError
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "decrypt"), strings.Contains(msg, "bad magic"):
		return http.StatusBadRequest
	case strings.Contains(msg, "unsupported"):
		return http.StatusBadRequest
	case strings.Contains(msg, "malformed"):
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

func classifyErrorCode(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "decrypt"):
		return "INVALID_PASSWORD_OR_CORRUPTED"
	case strings.Contains(msg, "bad magic"):
		return "UNSUPPORTED_BACKUP_FORMAT"
	case strings.Contains(msg, "unsupported"):
		return "UNSUPPORTED_SCHEMA_VERSION"
	case strings.Contains(msg, "malformed"):
		return "MALFORMED_PAYLOAD"
	}
	return "IMPORT_FAILED"
}

var hostSafe = regexp.MustCompile(`[^A-Za-z0-9.-]+`)

func (h *BackupHandler) hostSlug() string {
	if h.settingsManager == nil {
		return "unknown"
	}
	// best-effort: app_url is a SystemSetting key
	v, _ := h.settingsManager.GetSettingValue("app_url")
	if v == "" {
		return "unknown"
	}
	s := strings.TrimPrefix(strings.TrimPrefix(v, "https://"), "http://")
	s = hostSafe.ReplaceAllString(s, "-")
	if s == "" {
		return "unknown"
	}
	return s
}
```

> **Note on `settingsManager.GetSettingValue`**: 如果项目里此方法名不同，请用 grep 找到等价读 setting 的方法替换。整体流程不依赖此细节，host 解析失败回退 `"unknown"` 即可。先用 `grep -n "func.*SystemSettingsManager.*Get" internal/config/system_settings.go` 确认。

- [ ] **Step 4: 跑测试**

Run: `go test ./internal/handler/ -run TestBackup -v`
Expected: all PASS.

- [ ] **Step 5: 提交**

```bash
git add internal/handler/backup_handler.go internal/handler/backup_handler_test.go
git commit -m "$(cat <<'EOF'
✨ feat(backup): HTTP handler (export/preview/import)

POST /api/admin/backup/export 返回 .acb 二进制流；
POST /api/admin/backup/preview 返回 conflicts 报告 + confirm_token；
POST /api/admin/backup/import 校验 token 后写库。
错误分类为 INVALID_PASSWORD_OR_CORRUPTED / UNSUPPORTED_* /
MALFORMED_PAYLOAD / STALE_PREVIEW / IMPORT_FAILED。

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: 路由 + DI 接线

**Files:**
- Modify: `internal/container/container.go`
- Modify: `internal/router/router.go`

- [ ] **Step 1: DI 容器注册 Service 和 Handler**

打开 `internal/container/container.go`，在 `handler.NewServer` Provide 附近（约第 108 行）加：

```go
// 在 import 块加：
//   "autogateway/internal/backup"
//
// 在 BuildContainer 函数里加（贴在 handler.NewServer 的 Provide 之后即可）：
if err := container.Provide(func(db *gorm.DB, enc encryption.Service, ver types.ConfigManager) *backup.Service {
    return backup.NewService(db, enc, ver.GetVersion())
}); err != nil {
    return nil, err
}
if err := container.Provide(func(svc *backup.Service, sm *config.SystemSettingsManager) *handler.BackupHandler {
    return handler.NewBackupHandler(svc).WithSettingsManager(sm)
}); err != nil {
    return nil, err
}
```

> **检查点**：`types.ConfigManager` 是否有 `GetVersion()` 方法。用 `grep -n "GetVersion\|Version()" internal/types/*.go internal/config/*.go` 确认。若无，改成传入字符串常量 `"api-center"` 即可（version 字符串只用作 payload 标注，不影响功能）。

- [ ] **Step 2: 路由注册**

打开 `internal/router/router.go`，按现有 `registerProtectedAPIRoutes` 的签名模式，增加 `backupHandler *handler.BackupHandler` 参数；在 `registerAPIRoutes` 调用处把新参数传下去；在 `protectedAPI` 分组下方加：

```go
// 在 registerProtectedAPIRoutes 内的合适位置（例如末尾）追加：
backup := api.Group("/admin/backup")
{
    backup.POST("/export", backupHandler.Export)
    backup.POST("/preview", backupHandler.Preview)
    backup.POST("/import", backupHandler.Import)
}
```

并在 `NewRouter` 的 signature 同步加 `backupHandler *handler.BackupHandler` 参数，从 `BuildContainer` 的 `container.Invoke(NewRouter)` 处通过 dig 自动注入（dig 已经会按类型查找 `*handler.BackupHandler`，不需要手动改 Invoke 的调用）。

> **检查点**：找一下 `Invoke(.*NewRouter` 或 `dig.*Invoke`，确认 router 是怎么从容器拿出来的。用 `grep -rn "NewRouter\|Invoke" internal/app/ internal/container/` 看。如果是 `container.Invoke(func(... *handler.AliasHandler) *gin.Engine { return router.NewRouter(...) })` 这种 lambda 形式，需要在 lambda 参数表加上 `*handler.BackupHandler` 并把它传给 `NewRouter`。

- [ ] **Step 3: 编译 + 全量测试**

Run: `go build ./... && go test ./internal/backup/... ./internal/handler/... -v`
Expected: build 通过，测试全 PASS。

- [ ] **Step 4: 启动 smoke**

```bash
# 用现有 Makefile 启动，或直接：
go run ./main.go &
SERVER_PID=$!
sleep 3
# 拿到 auth_key（看用户 .env 或现有逻辑），然后 curl export：
curl -sS -X POST http://localhost:8080/api/admin/backup/export \
  -H "Authorization: Bearer $AUTH_KEY" \
  -H "Content-Type: application/json" \
  -d '{"password":"smoke"}' \
  -o /tmp/smoke.acb
file /tmp/smoke.acb
head -c 4 /tmp/smoke.acb # 应该是 ACB1
kill $SERVER_PID
```

Expected: `/tmp/smoke.acb` 存在，前 4 字节是 `ACB1`。

- [ ] **Step 5: 提交**

```bash
git add internal/container/container.go internal/router/router.go
git commit -m "$(cat <<'EOF'
✨ feat(backup): DI 装配 + /api/admin/backup/* 路由

container 注册 backup.Service 与 BackupHandler；router 暴露
export/preview/import 三接口于 admin auth 分组下。

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: 前端 API 客户端

**Files:**
- Create: `web/src/api/backup.ts`

- [ ] **Step 1: 创建 API client**

```ts
// web/src/api/backup.ts
import http from "./http"; // 复用项目已有 axios 实例

export interface PreviewReport {
  schema_version: number;
  exported_at: string;
  exported_by: string;
  counts: Counts;
  conflicts: {
    groups: string[];
    api_keys_by_hash: number;
    aliases: number;
    system_settings: string[];
    group_sub_groups: number;
  };
  will_delete_if_replace: Counts;
  warnings: string[];
  confirm_token: string;
}

export interface Counts {
  system_settings: number;
  groups: number;
  api_keys: number;
  model_aliases: number;
  group_sub_groups: number;
}

export interface ImportReport {
  applied: Counts;
  skipped: Counts;
  warnings: string[];
  elapsed_ms: number;
}

export type Strategy = "merge" | "skip" | "replace";

export const backupApi = {
  async exportBackup(password: string): Promise<Blob> {
    const res = await http.post(
      "/api/admin/backup/export",
      { password },
      { responseType: "blob" },
    );
    return res.data as Blob;
  },

  async previewBackup(file: File, password: string): Promise<PreviewReport> {
    const fd = new FormData();
    fd.append("file", file);
    fd.append("password", password);
    const res = await http.post("/api/admin/backup/preview", fd, {
      headers: { "Content-Type": "multipart/form-data" },
    });
    return res.data as PreviewReport;
  },

  async importBackup(
    file: File,
    password: string,
    strategy: Strategy,
    confirmToken: string,
  ): Promise<ImportReport> {
    const fd = new FormData();
    fd.append("file", file);
    fd.append("password", password);
    fd.append("strategy", strategy);
    fd.append("confirm_token", confirmToken);
    const res = await http.post("/api/admin/backup/import", fd, {
      headers: { "Content-Type": "multipart/form-data" },
    });
    return res.data as ImportReport;
  },
};
```

> **检查点**：项目里 axios 实例的实际导出名/路径。用 `grep -rn "import.*from.*['\"]\\./http\\|import.*from.*['\"]@/api/http" web/src/api/ | head -3` 看一下，若实际是 `http` / `axiosInstance` / `request`，统一调整 import。看一个现有 api 文件就知道了：`web/src/api/keys.ts`。

- [ ] **Step 2: 编译检查**

Run: `cd web && npm run type-check 2>&1 | head -20` （或者项目里实际的检查命令，参考 `package.json` 的 scripts）
Expected: no errors related to `backup.ts`.

- [ ] **Step 3: 提交**

```bash
git add web/src/api/backup.ts
git commit -m "$(cat <<'EOF'
✨ feat(backup/web): API client for /api/admin/backup/*

exportBackup / previewBackup / importBackup with strict TS types.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: 前端 BackupCard 组件

**Files:**
- Create: `web/src/components/v3/V3BackupCard.vue`

- [ ] **Step 1: 写组件**

```vue
<!-- web/src/components/v3/V3BackupCard.vue -->
<script setup lang="ts">
import { ref } from "vue";
import { useMessage, NCard, NSpace, NButton, NInput, NUpload, NRadioGroup, NRadio, NAlert, NDescriptions, NDescriptionsItem } from "naive-ui";
import type { UploadFileInfo } from "naive-ui";
import { backupApi, type PreviewReport, type Strategy } from "@/api/backup";

const msg = useMessage();

// --- Export ---
const exportPwd = ref("");
const exporting = ref(false);
async function doExport() {
  if (!exportPwd.value || exportPwd.value.length < 8) {
    msg.warning("Password should be at least 8 characters.");
    return;
  }
  exporting.value = true;
  try {
    const blob = await backupApi.exportBackup(exportPwd.value);
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = `api-center-backup-${new Date().toISOString().replace(/[:.]/g, "-")}.acb`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    msg.success("Backup downloaded. Keep the password safe.");
  } catch (e: any) {
    msg.error(`Export failed: ${e?.message ?? e}`);
  } finally {
    exporting.value = false;
  }
}

function generateRandomPassword() {
  const buf = new Uint8Array(24);
  crypto.getRandomValues(buf);
  exportPwd.value = btoa(String.fromCharCode(...buf)).replace(/[+/=]/g, "").slice(0, 32);
}

// --- Import ---
const importFile = ref<File | null>(null);
const importPwd = ref("");
const preview = ref<PreviewReport | null>(null);
const previewing = ref(false);
const strategy = ref<Strategy>("merge");
const deleteConfirm = ref(""); // user must type DELETE for replace
const importing = ref(false);

function onFileChange(opts: { fileList: UploadFileInfo[] }) {
  importFile.value = (opts.fileList[0]?.file as File) ?? null;
  preview.value = null;
}

async function doPreview() {
  if (!importFile.value || !importPwd.value) {
    msg.warning("Pick a .acb file and enter the password.");
    return;
  }
  previewing.value = true;
  try {
    preview.value = await backupApi.previewBackup(importFile.value, importPwd.value);
  } catch (e: any) {
    msg.error(`Preview failed: ${e?.response?.data?.error ?? e?.message ?? e}`);
  } finally {
    previewing.value = false;
  }
}

async function doImport() {
  if (!preview.value || !importFile.value) return;
  if (strategy.value === "replace" && deleteConfirm.value !== "DELETE") {
    msg.warning(`Type DELETE (uppercase) to confirm replace.`);
    return;
  }
  importing.value = true;
  try {
    const rep = await backupApi.importBackup(
      importFile.value, importPwd.value, strategy.value, preview.value.confirm_token,
    );
    msg.success(`Imported. applied groups=${rep.applied.groups}, keys=${rep.applied.api_keys}, aliases=${rep.applied.model_aliases}.`);
    // refresh preview state
    preview.value = null;
    deleteConfirm.value = "";
  } catch (e: any) {
    msg.error(`Import failed: ${e?.response?.data?.error ?? e?.message ?? e}`);
  } finally {
    importing.value = false;
  }
}
</script>

<template>
  <NSpace vertical :size="16">
    <NCard title="Export Configuration">
      <NSpace vertical :size="12">
        <NAlert type="warning">
          The backup file is encrypted with the password you set below.
          <strong>Lost password = lost data.</strong> Keep it together with the file.
        </NAlert>
        <NSpace>
          <NInput
            v-model:value="exportPwd"
            type="password"
            placeholder="Backup password (≥ 8 chars)"
            style="width: 320px"
            show-password-on="click"
          />
          <NButton @click="generateRandomPassword">Random</NButton>
          <NButton type="primary" :loading="exporting" @click="doExport">Download Backup</NButton>
        </NSpace>
      </NSpace>
    </NCard>

    <NCard title="Restore from Backup">
      <NSpace vertical :size="12">
        <NAlert type="info">
          We strongly recommend exporting a current backup before restoring.
        </NAlert>
        <NUpload :max="1" :default-upload="false" @change="onFileChange" accept=".acb">
          <NButton>Choose .acb file</NButton>
        </NUpload>
        <NInput
          v-model:value="importPwd"
          type="password"
          placeholder="Backup password"
          style="width: 320px"
          show-password-on="click"
        />
        <NButton :loading="previewing" :disabled="!importFile || !importPwd" @click="doPreview">
          Preview
        </NButton>

        <template v-if="preview">
          <NDescriptions :column="2" bordered size="small" label-placement="left">
            <NDescriptionsItem label="Schema">{{ preview.schema_version }}</NDescriptionsItem>
            <NDescriptionsItem label="Exported at">{{ preview.exported_at }}</NDescriptionsItem>
            <NDescriptionsItem label="Groups">{{ preview.counts.groups }} ({{ preview.conflicts.groups.length }} conflict)</NDescriptionsItem>
            <NDescriptionsItem label="API Keys">{{ preview.counts.api_keys }} ({{ preview.conflicts.api_keys_by_hash }} conflict)</NDescriptionsItem>
            <NDescriptionsItem label="Aliases">{{ preview.counts.model_aliases }} ({{ preview.conflicts.aliases }} conflict)</NDescriptionsItem>
            <NDescriptionsItem label="Settings">{{ preview.counts.system_settings }} ({{ preview.conflicts.system_settings.length }} conflict)</NDescriptionsItem>
          </NDescriptions>

          <div>Conflict strategy:</div>
          <NRadioGroup v-model:value="strategy">
            <NRadio value="merge">Merge (upsert; existing fields overwritten)</NRadio>
            <NRadio value="skip">Skip (keep all local; only add new)</NRadio>
            <NRadio value="replace">
              <strong style="color: var(--n-color-danger, #d03050)">Replace</strong>
              (delete all non-system groups + their keys/aliases, then import)
            </NRadio>
          </NRadioGroup>

          <NAlert v-if="strategy === 'replace'" type="error">
            Replace will delete
            <strong>{{ preview.will_delete_if_replace.groups }}</strong> groups,
            <strong>{{ preview.will_delete_if_replace.api_keys }}</strong> keys,
            <strong>{{ preview.will_delete_if_replace.model_aliases }}</strong> aliases.
            Type <code>DELETE</code> below to confirm.
            <NInput v-model:value="deleteConfirm" placeholder="Type DELETE to confirm" style="margin-top: 8px" />
          </NAlert>

          <NButton type="primary" :loading="importing" @click="doImport">Apply Import</NButton>
        </template>
      </NSpace>
    </NCard>
  </NSpace>
</template>
```

> **检查点**：Naive UI 组件实际项目里是否需要全局注册，看其他 v3 组件的 import 风格（直接 import or auto-import）。如果项目用 `unplugin-vue-components` 自动导入，去掉显式 `import` 即可；保险起见显式 import。

- [ ] **Step 2: 类型检查**

Run: `cd web && npm run type-check 2>&1 | grep -E "V3BackupCard|backup.ts" | head -10`
Expected: no errors.

- [ ] **Step 3: 提交**

```bash
git add web/src/components/v3/V3BackupCard.vue
git commit -m "$(cat <<'EOF'
✨ feat(backup/web): V3BackupCard 导出/恢复双卡片组件

包含随机密码生成、预览报告、merge/skip/replace 三策略 radio、
replace 模式 DELETE 二次确认。

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 11: 挂载到 Settings.vue

**Files:**
- Modify: `web/src/views/Settings.vue`

- [ ] **Step 1: 在 Settings.vue 顶部加 BackupCard**

打开 `web/src/views/Settings.vue`，在 `<template>` 块顶部（`<div class="v3-viewhead">` 之后、`<h1>` 之前，或紧贴 `<n-form>` 上方）插入：

```vue
<V3BackupCard style="margin-bottom: 24px" />
```

并在 `<script setup lang="ts">` 块顶部增加：

```ts
import V3BackupCard from "@/components/v3/V3BackupCard.vue";
```

- [ ] **Step 2: 启动前端 + 后端，浏览器手工 smoke**

```bash
# 后端
go run ./main.go &
# 前端
cd web && npm run dev &
```

打开浏览器 `http://localhost:5173`（或前端实际端口），登录后进入 Settings 页：
1. 输入密码 → 点 "Download Backup" → 下载得到 `.acb` 文件。
2. 选刚才那个文件 + 同密码 → "Preview"，应看到 counts 报告。
3. 选 `merge` → "Apply Import"，应看到 success message。
4. 试 `replace` → 看到 DELETE 输入框。
5. 故意输错密码 preview → 应看到 INVALID_PASSWORD_OR_CORRUPTED。

- [ ] **Step 3: 提交**

```bash
git add web/src/views/Settings.vue
git commit -m "$(cat <<'EOF'
✨ feat(backup/web): Settings 页顶部挂载 V3BackupCard

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 12: 导入后缓存失效 + 收尾

**Files:**
- Modify: `internal/backup/service.go`（增加可选 invalidator 钩子）
- Modify: `internal/container/container.go`（接线）

按 spec §6.2 "导入后处理"，导入完成需调用 `groupManager.Invalidate()`。

- [ ] **Step 1: 修改 service.go 增加 hook**

在 `Service` 结构体加：

```go
type InvalidateFunc func() error

type Service struct {
    db          *gorm.DB
    enc         encryption.Service
    version     string
    invalidates []InvalidateFunc // append-only, called after successful Import

    tokensMu sync.Mutex
    tokens   map[string]tokenEntry
}

// WithInvalidator 注册导入后要触发的 cache invalidate 回调。
func (s *Service) WithInvalidator(fn InvalidateFunc) *Service {
    s.invalidates = append(s.invalidates, fn)
    return s
}
```

在 `Import` 函数 `rep, err := imp.Import(...)` 成功之后调用：

```go
rep, err := imp.Import(ctx, bk, strat)
if err != nil {
    return nil, err
}
for _, fn := range s.invalidates {
    if e := fn(); e != nil {
        rep.Warnings = append(rep.Warnings, fmt.Sprintf("post-import invalidate: %v", e))
    }
}
return rep, nil
```

- [ ] **Step 2: 修改 container.go 接线**

把 backup.Service 的 Provide 改为：

```go
if err := container.Provide(func(
    db *gorm.DB,
    enc encryption.Service,
    cfg types.ConfigManager,
    gm *services.GroupManager,
    sm *config.SystemSettingsManager,
    aggSvc *services.AggregateGroupService,
) *backup.Service {
    return backup.NewService(db, enc, cfg.GetVersion()).
        WithInvalidator(func() error {
            // 1. backfill system aggregates for newly imported standard groups
            aggSvc.BackfillSystemAggregates(context.Background())
            return nil
        }).
        WithInvalidator(func() error { return gm.Invalidate() }).
        WithInvalidator(func() error {
            // SettingsManager invalidates via syncer; 若有 public method 直接调，
            // 否则可以触发任一 setting save 的代码路径。先以 group invalidate 为
            // 主，settings 端的 hot-reload 由用户重启或下次 settings 写入触发。
            return nil
        })
}); err != nil {
    return nil, err
}
```

> **检查点**：`SystemSettingsManager` 是否有公开的 `Invalidate()` 方法。看 `internal/config/system_settings.go:230` —— `sm.syncer.Invalidate()`，但 `syncer` 是私有。如果没有公开方法，本 plan 暂不调用 settings invalidate；只调 group invalidate。**spec §6.2 注：** settings 端在下次 UI 改 setting 时会自动重载。可在后续工作里给 SystemSettingsManager 加个 public `Invalidate()`，但不在本 plan 范围。

- [ ] **Step 3: 跑全套测试**

Run: `go test ./internal/backup/... ./internal/handler/... -v && go build ./...`
Expected: all PASS, build OK.

- [ ] **Step 4: 手工 E2E（跨实例）**

```bash
# 起实例 A，seed 一些 group/key，export
# 起实例 B（清空 DB 或不同 ENCRYPTION_KEY），导入
# 在 B 上调用 /v1/chat/completions（用 A 的某个 key）验证 key 能用
```

- [ ] **Step 5: 提交**

```bash
git add internal/backup/service.go internal/container/container.go
git commit -m "$(cat <<'EOF'
✨ feat(backup): 导入完成后触发 GroupManager.Invalidate + Backfill

通过可选 Invalidator hook 接线，避免 backup 包硬依赖 services。
导入 standard group 后自动挂回系统聚合分组，缓存失效让下次请求
重新加载。

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Self-Review

**1. Spec 覆盖：**
- §3 架构：✅ Task 1-7 创建 `internal/backup/` 包 + handler；Task 8 接路由
- §4.1 容器格式：✅ Task 2 实现 EncodeContainer/DecodeContainer
- §4.2 Payload schema：✅ Task 3 定义 BackupV1
- §4.3 字段映射规则（剔除 id/timestamps、外键用 name、key_value 明文、跳过 is_system 与 encryption_key）：✅ Task 4-5
- §5 导出流程：✅ Task 4 exporter + Task 7 handler
- §6 导入流程（preview / import / token / 三策略 / 硬规则）：✅ Task 5-7
- §6.2 导入后处理（Invalidate + Backfill）：✅ Task 12
- §7 错误处理（INVALID_PASSWORD_OR_CORRUPTED / STALE_PREVIEW 等）：✅ Task 7 classifyErrorCode
- §8 测试策略（codec/crypto/schema/importer/E2E/handler）：✅ 分散在各 Task 的 Step 1
- §9 安全（不入日志、AAD 防降级）：✅ Task 1 AAD、Task 7 password 不写日志
- §10 兼容性（schema_version 校验）：✅ Task 5、Task 6 都检查

**2. Placeholder 扫描：** 无 TBD/TODO；几处 "检查点" 是显式指引（如 axios import 路径、`GetVersion`/`Invalidate` 方法名），都给了 grep 命令兜底。

**3. 类型一致性：**
- `Strategy` / `StrategyMerge|Skip|Replace` 在 schema.go 定义，importer/service/handler 全部一致使用 ✅
- `Counts` 在 importer.go 定义，service.go 与 handler 复用 ✅
- `BackupV1.SchemaVersion` 为 `int`，CurrentSchemaVersion 也是 int ✅
- 函数签名 `(blob []byte, password string)` 在 Encode/Decode/Preview/Import 一致 ✅

---

## Execution Handoff

Plan saved to `docs/superpowers/plans/2026-05-22-config-backup-restore.md`. Two execution options:

1. **Subagent-Driven (recommended)** — 我每个 task 派一个 fresh subagent，每个完成后两段式 review，速度快、上下文干净。
2. **Inline Execution** — 在当前会话里按 task 顺序逐个执行，遇到 checkpoint 暂停让你看一眼。

Which?
