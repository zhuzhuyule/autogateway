package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"autogateway/internal/models"
	"autogateway/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newHandlerTestRouter(t *testing.T) (*gin.Engine, *services.VideoTaskService) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&models.VideoTask{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	svc := services.NewVideoTaskService(db)
	h := NewVideoTaskHandler(svc)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/video-tasks")
	g.POST("", h.Create)
	g.GET("", h.List)
	g.GET("/:id", h.Get)
	g.POST("/:id/cancel", h.Cancel)
	g.POST("/:id/retry", h.Retry)
	g.DELETE("/:id", h.Delete)
	return r, svc
}

func TestVideoTaskHandler_CreateAndGet(t *testing.T) {
	r, _ := newHandlerTestRouter(t)

	body, _ := json.Marshal(map[string]any{
		"group": "agnes", "model": "agnes-video-v2.0", "prompt": "a cat",
		"params": map[string]any{"num_frames": 121},
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/video-tasks", bytes.NewReader(body))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}
	var created models.VideoTask
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if created.ID == "" || created.Status != models.VideoTaskPending {
		t.Fatalf("bad created: %+v", created)
	}

	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest(http.MethodGet, "/api/video-tasks/"+created.ID, nil)
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("get status=%d", w2.Code)
	}
}

func TestVideoTaskHandler_ListByIDs(t *testing.T) {
	r, svc := newHandlerTestRouter(t)
	a, _ := svc.Create("g", "m", "p", "")
	b, _ := svc.Create("g", "m", "p", "")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/video-tasks?ids="+a.ID+","+b.ID, nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list status=%d", w.Code)
	}
	var resp struct {
		Tasks []models.VideoTask `json:"tasks"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(resp.Tasks))
	}
}
