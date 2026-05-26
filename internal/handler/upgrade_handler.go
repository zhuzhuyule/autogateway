package handler

import (
	"net/http"
	"time"

	"autogateway/internal/services"

	"github.com/gin-gonic/gin"
)

// UpgradeHandler 暴露 /api/upgrade/* 端点, 包装 UpgradeService 业务逻辑.
type UpgradeHandler struct {
	upgradeSvc *services.UpgradeService
}

// NewUpgradeHandler dig 注入构造.
func NewUpgradeHandler(upgradeSvc *services.UpgradeService) *UpgradeHandler {
	return &UpgradeHandler{upgradeSvc: upgradeSvc}
}

// upgradeRequestBody 是 POST /api/upgrade/request 的请求体.
type upgradeRequestBody struct {
	TargetVersion string `json:"target_version" binding:"required"`
	// RequestedBy 可选: "self" 或 远端 peer_id. 默认 "self".
	RequestedBy string `json:"requested_by"`
}

// Request 触发一次本端升级请求.
//
// 真正的升级执行不由本进程完成 — 进程只写一个信号文件, 由宿主机 watcher
// (systemd / 伴随容器) 看到文件后执行 docker compose pull && up -d.
//
// 安全限制由 UpgradeService.RequestUpgrade 实现:
//   - target_version 必须是合法 semver
//   - 禁止降级
//   - 已有 pending 升级时返回 409
func (h *UpgradeHandler) Request(c *gin.Context) {
	var body upgradeRequestBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	if body.RequestedBy == "" {
		body.RequestedBy = "self"
	}

	err := h.upgradeSvc.RequestUpgrade(services.UpgradeRequest{
		TargetVersion: body.TargetVersion,
		RequestedBy:   body.RequestedBy,
		RequestedAt:   time.Now(),
	})
	if err != nil {
		// 已有 pending 用 409, 其余 400
		code := http.StatusBadRequest
		if err.Error() == "upgrade already pending" {
			code = http.StatusConflict
		}
		c.JSON(code, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"message": "upgrade request accepted, awaiting host watcher"})
}

// Status 返回当前的升级请求状态. UI 用来判断 "等待 watcher 接管" 或
// "watcher 未部署 (信号文件超时未消费)".
func (h *UpgradeHandler) Status(c *gin.Context) {
	c.JSON(http.StatusOK, h.upgradeSvc.Status())
}
