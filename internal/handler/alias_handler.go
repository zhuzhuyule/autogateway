package handler

import (
	"strconv"

	app_errors "autogateway/internal/errors"
	"autogateway/internal/response"
	"autogateway/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// AliasHandler exposes CRUD over model_aliases.
type AliasHandler struct {
	svc *services.AliasService
}

func NewAliasHandler(svc *services.AliasService) *AliasHandler {
	return &AliasHandler{svc: svc}
}

func (h *AliasHandler) List(c *gin.Context) {
	rows, err := h.svc.ListAll(c.Request.Context())
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.cannot_list_aliases")
		return
	}
	response.Success(c, rows)
}

func (h *AliasHandler) GetByAlias(c *gin.Context) {
	name := c.Param("alias")
	rows, err := h.svc.ListByAlias(c.Request.Context(), name)
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.cannot_list_aliases")
		return
	}
	response.Success(c, rows)
}

func (h *AliasHandler) Create(c *gin.Context) {
	var req services.AliasCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_payload")
		return
	}
	row, err := h.svc.Create(c.Request.Context(), req)
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_payload")
		return
	}
	response.Success(c, row)
}

func (h *AliasHandler) Update(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_id")
		return
	}
	var req services.AliasUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_payload")
		return
	}
	row, err := h.svc.Update(c.Request.Context(), uint(id), req)
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_payload")
		return
	}
	response.Success(c, row)
}

func (h *AliasHandler) Delete(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_id")
		return
	}
	if err := h.svc.Delete(c.Request.Context(), uint(id)); err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_payload")
		return
	}
	response.Success(c, gin.H{"deleted": true})
}

// RenameAlias 整体改一个别名的名字。
//
// 走 PUT /api/aliases/rename(from/to 放在 body 里)而不是 /api/aliases/{alias}/rename:
// 后者会和已有的 PUT /:id 在路由树同一层出现两个不同名的参数段, gin 直接 panic。
func (h *AliasHandler) RenameAlias(c *gin.Context) {
	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_payload")
		return
	}
	n, err := h.svc.RenameAlias(c.Request.Context(), req.From, req.To)
	if err != nil {
		// 保留别名 / 目标名已存在 这些都要能在日志里查到, 否则前端只看到"参数不合法"。
		logrus.WithError(err).WithFields(logrus.Fields{"from": req.From, "to": req.To}).
			Warn("rename alias failed")
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_payload")
		return
	}
	response.Success(c, gin.H{"renamed": n})
}

// ReplaceCandidates 整体替换某个别名的候选集合 —— 前端编辑抽屉的"保存"。
//
// 走 PUT /api/aliases/candidates(别名放在 body 里)而不是
// /api/aliases/{alias}/candidates: 后者会在路由树的第一层同时出现 :alias 和
// 已有的 :id 两个不同名的参数段, gin 直接 panic。
func (h *AliasHandler) ReplaceCandidates(c *gin.Context) {
	var req struct {
		Alias      string                         `json:"alias"`
		Candidates []services.AliasCandidateInput `json:"candidates"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_payload")
		return
	}
	rows, err := h.svc.ReplaceCandidates(c.Request.Context(), req.Alias, req.Candidates)
	if err != nil {
		// 唯一索引冲突(改成了重复的 group+model)等都要能查, 否则前端只会看到
		// 一句"参数不合法"。
		logrus.WithError(err).WithField("alias", req.Alias).
			Warn("replace alias candidates failed")
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_payload")
		return
	}
	response.Success(c, rows)
}

// Expose 把一条别名候选的 real_model 补进所在分组的 exposed_models。
//
// 候选三元组放在 body 里而不是路径上: 同 RenameAlias —— /aliases 这一层已经有
// :id 参数段, 再加一个不同名的段会让 gin 直接 panic。
func (h *AliasHandler) Expose(c *gin.Context) {
	var req struct {
		Alias     string `json:"alias"`
		GroupID   uint   `json:"group_id"`
		RealModel string `json:"real_model"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_payload")
		return
	}
	status, err := h.svc.ExposeCandidate(c.Request.Context(), req.Alias, req.GroupID, req.RealModel)
	if err != nil {
		// 分组 / 候选不存在、模型在黑名单里 —— 这些都要能在日志里查到原因,
		// 否则前端只剩一句"参数不合法"。
		logrus.WithError(err).WithFields(logrus.Fields{
			"alias": req.Alias, "group_id": req.GroupID, "model": req.RealModel,
		}).Warn("expose alias candidate failed")
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_payload")
		return
	}
	response.Success(c, gin.H{"status": status})
}
