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
	rep, err := h.svc.Preview(c.Request.Context(), blob, password)
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
	v := h.settingsManager.GetAppUrl()
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
