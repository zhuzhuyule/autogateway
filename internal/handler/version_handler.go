package handler

import (
	"net/http"
	"time"

	"autogateway/internal/services"
	"autogateway/internal/version"

	"github.com/gin-gonic/gin"
)

// VersionResponse 公开版本端点的返回结构.
//
// 用于 P9.1 / P9.x 节点间互探:
//   - version + schema_hash 决定能否同步 (兼容性闸门)
//   - public_key + fingerprint 用于非对称密钥握手 (取代全局 SyncSecret)
//
// public_key 是 X25519 32 字节公钥 base64 标准编码 (44 字符),
// fingerprint 是 SHA256(public_key) 前 8 字节 base64 RawStd 编码 (~11 字符),
// 用户在 UI 上肉眼核对两端身份.
type VersionResponse struct {
	Version     string    `json:"version"`
	SchemaHash  string    `json:"schema_hash"`
	PublicKey   string    `json:"public_key"`
	Fingerprint string    `json:"fingerprint"`
	StartedAt   time.Time `json:"started_at"`
}

// VersionHandler 提供 /api/version 公开端点.
type VersionHandler struct {
	startedAt time.Time
	keypair   *services.NodeKeypairService
}

// NewVersionHandler 通过 dig 注入. startedAt 在 app 启动时定格.
func NewVersionHandler(keypair *services.NodeKeypairService) *VersionHandler {
	return &VersionHandler{
		startedAt: time.Now(),
		keypair:   keypair,
	}
}

// Get 返回本节点版本与身份信息. 公开端点 (不需 auth),
// 避免握手前先卡鉴权 + 让对端可以查我们的公钥指纹.
func (h *VersionHandler) Get(c *gin.Context) {
	c.JSON(http.StatusOK, VersionResponse{
		Version:     version.Version,
		SchemaHash:  services.ComputeSchemaHash(),
		PublicKey:   h.keypair.PublicKeyBase64(),
		Fingerprint: h.keypair.Fingerprint(),
		StartedAt:   h.startedAt,
	})
}
