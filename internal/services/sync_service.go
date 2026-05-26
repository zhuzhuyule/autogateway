package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"autogateway/internal/encryption"
	"autogateway/internal/models"
	"autogateway/internal/types"

	"gorm.io/gorm"
)

// SyncPayload 定义了多端同步的明文数据体，直接使用数据库实体以完整保留主键 ID 与所有时间戳（含 DeletedAt 软删除墓碑）
type SyncPayload struct {
	SourcePeerID string                 `json:"source_peer_id"`
	Timestamp    time.Time              `json:"timestamp"`
	Settings     []models.SystemSetting `json:"settings,omitempty"`
	Groups       []models.Group         `json:"groups,omitempty"`
	SubGroups    []models.GroupSubGroup `json:"sub_groups,omitempty"`
	APIKeys      []models.APIKey        `json:"api_keys,omitempty"`
	ModelAliases []models.ModelAlias    `json:"model_aliases,omitempty"`
}

// WSMessage 是 WebSocket 上传递的统一消息封装. 通过 type 字段区分:
//   - "hello"   : 客户端连上后第一帧, 携带 version + schema_hash
//   - "welcome" : 服务端鉴权 + 兼容性通过, 同步可以开始
//   - "warning" : 服务端允许同步但发现 minor 版本不一致, 仅警告
//   - "reject"  : 服务端拒绝同步 (major / schema 不兼容), 含 reason 字段
//   - "sync"    : 携带 ciphertext, 走正常 payload 同步路径
type WSMessage struct {
	Type        string `json:"type"`
	Version     string `json:"version,omitempty"`
	SchemaHash  string `json:"schema_hash,omitempty"`
	PeerVersion string `json:"peer_version,omitempty"`
	PeerSchema  string `json:"peer_schema,omitempty"`
	MyVersion   string `json:"my_version,omitempty"`
	MySchema    string `json:"my_schema,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Ciphertext  string `json:"ciphertext,omitempty"`
}

// ExtractMajor 仅返回 semver 的 major 部分.
// "v2.4.10" -> 2; "v3.0.0" -> 3; 解析失败 / 空串返回 -1.
func ExtractMajor(v string) int {
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		return -1
	}
	parts := strings.SplitN(v, ".", 2)
	head := parts[0]
	if head == "" {
		return -1
	}
	n := 0
	for _, ch := range head {
		if ch < '0' || ch > '9' {
			return -1
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

// syncMergeKey 是用于在 context 中标记"当前事务是同步合并触发"的 key,
// 防止 GORM hook 在合并事务中再次触发 push 形成回环。
type syncMergeKey struct{}

// IsSyncMerge 供 GORM hook 判断当前事务是否由同步合并触发。
func IsSyncMerge(ctx context.Context) bool {
	v, _ := ctx.Value(syncMergeKey{}).(bool)
	return v
}

// SyncService 负责多端数据加密封包、解密解包以及记录级智能合并业务
type SyncService struct {
	db            *gorm.DB
	configManager types.ConfigManager
}

// NewSyncService 构造函数，支持 dig 自动依赖注入
func NewSyncService(db *gorm.DB, configManager types.ConfigManager) *SyncService {
	return &SyncService{
		db:            db,
		configManager: configManager,
	}
}

// ExportPayload 查询数据库中自 `since` 时间戳以来发生变更（包含被软删除的墓碑记录）的所有核心配置.
//
// 启用同步即同步全部内容 (含 api_keys), 不再有细粒度开关. 用户对"是否同步密钥"
// 的选择已简化为"是否启用同步": 启用 ⇒ 全部, 不启用 ⇒ 什么都不同步.
func (s *SyncService) ExportPayload(ctx context.Context, since *time.Time) (*SyncPayload, error) {
	payload := &SyncPayload{
		SourcePeerID: s.configManager.GetAuthConfig().Key, // 复用本端的 Master 密钥哈希或 Key 作为本端节点 ID
		Timestamp:    time.Now(),
	}

	// 1. 系统设置 (SystemSettings)
	{
		var items []models.SystemSetting
		query := s.db.WithContext(ctx).Unscoped()
		if since != nil {
			query = query.Where("updated_at > ?", *since)
		}
		if err := query.Find(&items).Error; err != nil {
			return nil, fmt.Errorf("failed to export system settings: %w", err)
		}
		payload.Settings = items
	}

	// 2. 路由分组 (Groups)
	{
		var items []models.Group
		query := s.db.WithContext(ctx).Unscoped()
		if since != nil {
			query = query.Where("updated_at > ?", *since)
		}
		if err := query.Find(&items).Error; err != nil {
			return nil, fmt.Errorf("failed to export groups: %w", err)
		}
		payload.Groups = items
	}

	// 3. 聚合关联 (GroupSubGroups)
	{
		var items []models.GroupSubGroup
		query := s.db.WithContext(ctx).Unscoped()
		if since != nil {
			query = query.Where("updated_at > ?", *since)
		}
		if err := query.Find(&items).Error; err != nil {
			return nil, fmt.Errorf("failed to export group subgroups: %w", err)
		}
		payload.SubGroups = items
	}

	// 4. 路由别名 (ModelAliases)
	{
		var items []models.ModelAlias
		query := s.db.WithContext(ctx).Unscoped()
		if since != nil {
			query = query.Where("updated_at > ?", *since)
		}
		if err := query.Find(&items).Error; err != nil {
			return nil, fmt.Errorf("failed to export model aliases: %w", err)
		}
		payload.ModelAliases = items
	}

	// 5. API 密钥 (APIKeys) - 简化后总是导出, 由 SyncEnabled 统管开关
	{
		var items []models.APIKey
		query := s.db.WithContext(ctx).Unscoped()
		if since != nil {
			query = query.Where("updated_at > ?", *since)
		}
		if err := query.Find(&items).Error; err != nil {
			return nil, fmt.Errorf("failed to export api keys: %w", err)
		}
		payload.APIKeys = items
	}

	return payload, nil
}

// ProcessPayload 在单个事务中执行记录级最新写入生效（LWW per Record）智能合并。
// 在 context 上挂 syncMergeKey 标记,GORM hook 见到后短路,避免合并触发回环 push。
func (s *SyncService) ProcessPayload(ctx context.Context, payload *SyncPayload) error {
	if payload == nil {
		return nil
	}

	ctx = context.WithValue(ctx, syncMergeKey{}, true)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. 合并系统设置 (SystemSettings)
		for _, incoming := range payload.Settings {
			var existing models.SystemSetting
			err := tx.Unscoped().Where("id = ?", incoming.ID).First(&existing).Error
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					if err := tx.Create(&incoming).Error; err != nil {
						return fmt.Errorf("failed to create system setting %d: %w", incoming.ID, err)
					}
				} else {
					return err
				}
			} else {
				if incoming.UpdatedAt.After(existing.UpdatedAt) {
					if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Save(&incoming).Error; err != nil {
						return fmt.Errorf("failed to update system setting %d: %w", incoming.ID, err)
					}
				}
			}
		}

		// 2. 合并分组 (Groups)
		for _, incoming := range payload.Groups {
			var existing models.Group
			err := tx.Unscoped().Where("id = ?", incoming.ID).First(&existing).Error
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					if err := tx.Create(&incoming).Error; err != nil {
						return fmt.Errorf("failed to create group %d: %w", incoming.ID, err)
					}
				} else {
					return err
				}
			} else {
				if incoming.UpdatedAt.After(existing.UpdatedAt) {
					if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Save(&incoming).Error; err != nil {
						return fmt.Errorf("failed to update group %d: %w", incoming.ID, err)
					}
				}
			}
		}

		// 3. 合并子分组关联 (GroupSubGroups)
		for _, incoming := range payload.SubGroups {
			var existing models.GroupSubGroup
			err := tx.Unscoped().Where("id = ?", incoming.ID).First(&existing).Error
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					if err := tx.Create(&incoming).Error; err != nil {
						return fmt.Errorf("failed to create subgroup association %d: %w", incoming.ID, err)
					}
				} else {
					return err
				}
			} else {
				if incoming.UpdatedAt.After(existing.UpdatedAt) {
					if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Save(&incoming).Error; err != nil {
						return fmt.Errorf("failed to update subgroup association %d: %w", incoming.ID, err)
					}
				}
			}
		}

		// 4. 合并路由别名 (ModelAliases)
		for _, incoming := range payload.ModelAliases {
			var existing models.ModelAlias
			err := tx.Unscoped().Where("id = ?", incoming.ID).First(&existing).Error
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					if err := tx.Create(&incoming).Error; err != nil {
						return fmt.Errorf("failed to create model alias %d: %w", incoming.ID, err)
					}
				} else {
					return err
				}
			} else {
				if incoming.UpdatedAt.After(existing.UpdatedAt) {
					if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Save(&incoming).Error; err != nil {
						return fmt.Errorf("failed to update model alias %d: %w", incoming.ID, err)
					}
				}
			}
		}

		// 5. 合并 API 密钥 (APIKeys)
		for _, incoming := range payload.APIKeys {
			var existing models.APIKey
			err := tx.Unscoped().Where("id = ?", incoming.ID).First(&existing).Error
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					if err := tx.Create(&incoming).Error; err != nil {
						return fmt.Errorf("failed to create api key %d: %w", incoming.ID, err)
					}
				} else {
					return err
				}
			} else {
				if incoming.UpdatedAt.After(existing.UpdatedAt) {
					if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Save(&incoming).Error; err != nil {
						return fmt.Errorf("failed to update api key %d: %w", incoming.ID, err)
					}
				}
			}
		}

		return nil
	})
}

// EncryptPayload 使用指定的同步密钥对配置数据明文包进行 AES-256-GCM 强对称加密，生成十六进制密文
func (s *SyncService) EncryptPayload(payload *SyncPayload, syncKey string) (string, error) {
	plainBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal sync payload: %w", err)
	}

	encSvc, err := encryption.NewService(syncKey)
	if err != nil {
		return "", fmt.Errorf("failed to initialize encryption service: %w", err)
	}

	ciphertext, err := encSvc.Encrypt(string(plainBytes))
	if err != nil {
		return "", fmt.Errorf("failed to encrypt sync payload: %w", err)
	}

	return ciphertext, nil
}

// DecryptPayload 使用指定的同步密钥对十六进制配置数据密文进行 AES-256-GCM 解密，并反序列化为明文包
func (s *SyncService) DecryptPayload(ciphertext string, syncKey string) (*SyncPayload, error) {
	encSvc, err := encryption.NewService(syncKey)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize decryption service: %w", err)
	}

	plaintext, err := encSvc.Decrypt(ciphertext)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt sync payload: %w", err)
	}

	var payload SyncPayload
	if err := json.Unmarshal([]byte(plaintext), &payload); err != nil {
		return nil, fmt.Errorf("failed to unmarshal decrypted payload: %w", err)
	}

	return &payload, nil
}
