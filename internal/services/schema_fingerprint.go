package services

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"autogateway/internal/models"
)

// schemaHashCache 缓存启动时算好的 hash, 避免每次握手重算.
var (
	schemaHashCache string
	schemaHashOnce  sync.Once
)

// ComputeSchemaHash 计算所有 SyncPayload 涉及的 GORM model 的字段 fingerprint.
// 任何字段增删改名都会改变 hash, 两端不一致即可探测到 schema 漂移.
//
// 算法: 遍历每个 model 的反射类型, 收集 (model_name, field_name, field_type),
// 排序拼接后 SHA256, 取前 12 字节十六进制.
//
// 用于 P9.1 schema fingerprint 闸门: WS 握手时双方交换 schema_hash, 不一致即
// 拒绝同步, 防止 LWW 把对端缺失字段抹空.
func ComputeSchemaHash() string {
	schemaHashOnce.Do(func() {
		schemaHashCache = computeSchemaHashOnce()
	})
	return schemaHashCache
}

func computeSchemaHashOnce() string {
	targets := []any{
		models.SystemSetting{},
		models.Group{},
		models.GroupSubGroup{},
		models.ModelAlias{},
		models.APIKey{},
	}

	var entries []string
	for _, m := range targets {
		t := reflect.TypeOf(m)
		modelName := t.Name()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			// 跳过非 GORM 持久化字段 (gorm:"-" 或 unexported)
			if !f.IsExported() {
				continue
			}
			tag := f.Tag.Get("gorm")
			if strings.Contains(tag, "-") && !strings.Contains(tag, "column") {
				// gorm:"-" 表示不持久化, 跳过. 但 "primary" 等不算.
				if tag == "-" || strings.HasPrefix(tag, "-;") || strings.HasSuffix(tag, ";-") {
					continue
				}
			}
			entries = append(entries, fmt.Sprintf("%s.%s:%s", modelName, f.Name, f.Type.String()))
		}
	}
	sort.Strings(entries)
	h := sha256.Sum256([]byte(strings.Join(entries, "|")))
	return hex.EncodeToString(h[:6]) // 12 字符
}
