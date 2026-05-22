// internal/backup/service.go
package backup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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
	SchemaVersion       int            `json:"schema_version"`
	ExportedAt          time.Time      `json:"exported_at"`
	ExportedBy          string         `json:"exported_by"`
	Counts              Counts         `json:"counts"`
	Conflicts           ConflictReport `json:"conflicts"`
	WillDeleteIfReplace Counts         `json:"will_delete_if_replace"`
	Warnings            []string       `json:"warnings"`
	ConfirmToken        string         `json:"confirm_token"`
}

// ConflictReport 列出与本地已存在记录冲突的业务键。
type ConflictReport struct {
	Groups         []string `json:"groups"`
	APIKeysByHash  int      `json:"api_keys_by_hash"`
	Aliases        int      `json:"aliases"`
	SystemSettings []string `json:"system_settings"`
	GroupSubGroups int      `json:"group_sub_groups"`
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

// blobFingerprint 计算 blob 的 SHA-256 摘要，用于 confirm_token ↔ blob 绑定。
// 这是对上传文件的完整性校验，不是密钥派生，所以裸 SHA-256 足够。
func blobFingerprint(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
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

	// conflicts.api_keys_by_hash —— 使用 encryption.Service.Hash 与项目其他地方一致
	if len(bk.Data.APIKeys) > 0 {
		hashes := make([]string, 0, len(bk.Data.APIKeys))
		for _, k := range bk.Data.APIKeys {
			hashes = append(hashes, s.enc.Hash(k.KeyValue))
		}
		var n int64
		db.Table("api_keys").Where("key_hash IN ?", hashes).Count(&n)
		rep.Conflicts.APIKeysByHash = int(n)
	}

	// conflicts.aliases (粗略：同 alias 即视为可能冲突，不精确到 group_id 因 name 还没解析)
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
		blobHash: blobFingerprint(blob),
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
	if entry.blobHash != blobFingerprint(blob) {
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
