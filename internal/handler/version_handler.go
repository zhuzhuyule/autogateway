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
// 用于 P9.1 节点间互探 — peer 列表 UI 调用 /api/version 拉取对端版本与 schema_hash,
// 进行兼容性比对.
type VersionResponse struct {
	Version    string    `json:"version"`
	SchemaHash string    `json:"schema_hash"`
	StartedAt  time.Time `json:"started_at"`
}

// VersionHandler 提供 /api/version 公开端点.
type VersionHandler struct {
	startedAt time.Time
}

// NewVersionHandler 通过 dig 注入. startedAt 在 app 启动时定格.
func NewVersionHandler() *VersionHandler {
	return &VersionHandler{
		startedAt: time.Now(),
	}
}

// Get 返回本节点版本信息. 公开端点 (不需 auth), 避免握手前先卡鉴权.
func (h *VersionHandler) Get(c *gin.Context) {
	c.JSON(http.StatusOK, VersionResponse{
		Version:    version.Version,
		SchemaHash: services.ComputeSchemaHash(),
		StartedAt:  h.startedAt,
	})
}
