package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"autogateway/internal/services"

	"github.com/gin-gonic/gin"
)

type VideoTaskHandler struct {
	svc *services.VideoTaskService
}

func NewVideoTaskHandler(svc *services.VideoTaskService) *VideoTaskHandler {
	return &VideoTaskHandler{svc: svc}
}

type createVideoTaskRequest struct {
	Group  string         `json:"group"`
	Model  string         `json:"model"`
	Prompt string         `json:"prompt"`
	Params map[string]any `json:"params"`
}

func (h *VideoTaskHandler) Create(c *gin.Context) {
	var req createVideoTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Group == "" || req.Model == "" || strings.TrimSpace(req.Prompt) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "group, model, prompt are required"})
		return
	}
	params := ""
	if req.Params != nil {
		if b, err := json.Marshal(req.Params); err == nil {
			params = string(b)
		}
	}
	task, err := h.svc.Create(req.Group, req.Model, req.Prompt, params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, task)
}

func (h *VideoTaskHandler) Get(c *gin.Context) {
	task, err := h.svc.Get(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}
	c.JSON(http.StatusOK, task)
}

// List: ?ids=a,b,c 批量查;否则 ?status=&page=&page_size= 分页。
func (h *VideoTaskHandler) List(c *gin.Context) {
	if idsParam := c.Query("ids"); idsParam != "" {
		ids := strings.Split(idsParam, ",")
		tasks, err := h.svc.ListByIDs(ids)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"tasks": tasks})
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	tasks, total, err := h.svc.List(c.Query("status"), page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"tasks": tasks, "total": total})
}

func (h *VideoTaskHandler) Cancel(c *gin.Context) {
	if err := h.svc.Cancel(c.Param("id")); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *VideoTaskHandler) Retry(c *gin.Context) {
	task, err := h.svc.Retry(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, task)
}

func (h *VideoTaskHandler) Delete(c *gin.Context) {
	if err := h.svc.Delete(c.Param("id")); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
