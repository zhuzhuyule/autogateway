package handler

import (
	"strconv"

	app_errors "autogateway/internal/errors"
	"autogateway/internal/response"
	"autogateway/internal/services"

	"github.com/gin-gonic/gin"
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
