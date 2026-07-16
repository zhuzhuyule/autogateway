package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"autogateway/internal/i18n"
	"autogateway/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestMain 初始化 i18n bundle —— response.Success 会调 i18n.Message, 缺 bundle 会 panic。
func TestMain(m *testing.M) {
	if err := i18n.Init(); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func newUsageTestServer(t *testing.T) *Server {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&models.RequestLog{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return &Server{DB: db}
}

func seedLog(t *testing.T, db *gorm.DB, id, model string, ts time.Time, reqType string, prompt, completion, total int, cost float64, success bool) {
	t.Helper()
	err := db.Create(&models.RequestLog{
		ID:               id,
		Timestamp:        ts,
		GroupID:          1,
		GroupName:        "g1",
		Model:            model,
		IsSuccess:        success,
		StatusCode:       200,
		Duration:         100,
		RequestType:      reqType,
		PromptTokens:     prompt,
		CompletionTokens: completion,
		TotalTokens:      total,
		CostUSD:          cost,
	}).Error
	if err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// decodeData 解出 response.Success 包裹的 data 字段到 v。
func decodeData(t *testing.T, body []byte, v any) {
	t.Helper()
	var wrapper struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		t.Fatalf("unmarshal wrapper: %v (body=%s)", err, body)
	}
	if wrapper.Code != 0 {
		t.Fatalf("response code = %d, body=%s", wrapper.Code, body)
	}
	if err := json.Unmarshal(wrapper.Data, v); err != nil {
		t.Fatalf("unmarshal data: %v (data=%s)", err, wrapper.Data)
	}
}

func callGET(s *Server, path string, h gin.HandlerFunc) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, path, nil)
	h(c)
	return rec
}

func TestUsageSummary_SumsWithinWindow(t *testing.T) {
	s := newUsageTestServer(t)
	now := time.Now()

	// 窗口内 final 请求 (计入)。
	seedLog(t, s.DB, "a", "gpt-4o", now.Add(-1*time.Hour), models.RequestTypeFinal, 100, 50, 150, 0.001, true)
	seedLog(t, s.DB, "b", "gpt-4o", now.Add(-2*time.Hour), models.RequestTypeFinal, 200, 80, 280, 0.002, true)
	// 免费源: cost=0 但 token 计入。
	seedLog(t, s.DB, "c", "free-qwen", now.Add(-3*time.Hour), models.RequestTypeFinal, 300, 100, 400, 0, true)
	// retry 请求 (request_type != final, 排除, 避免重复计数)。
	seedLog(t, s.DB, "d", "gpt-4o", now.Add(-1*time.Hour), models.RequestTypeRetry, 999, 999, 999, 9.9, false)
	// 窗口外 (25h 前, 24h 窗口应排除)。
	seedLog(t, s.DB, "e", "gpt-4o", now.Add(-25*time.Hour), models.RequestTypeFinal, 500, 500, 1000, 5.0, true)

	rec := callGET(s, "/dashboard/usage-summary?window=24h", s.UsageSummary)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out UsageSummaryResponse
	decodeData(t, rec.Body.Bytes(), &out)

	// a+b+c: prompt=600, completion=230, total=830, cost=0.003, metered=3。
	if out.PromptTokens != 600 || out.CompletionTokens != 230 || out.TotalTokens != 830 {
		t.Fatalf("tokens = %+v, want prompt=600 completion=230 total=830", out)
	}
	if out.CostUSD < 0.00299 || out.CostUSD > 0.00301 {
		t.Fatalf("cost = %v, want ~0.003", out.CostUSD)
	}
	if out.MeteredRequests != 3 {
		t.Fatalf("metered = %d, want 3", out.MeteredRequests)
	}
}

func TestTopModels_IncludesTokensAndCost(t *testing.T) {
	s := newUsageTestServer(t)
	now := time.Now()
	seedLog(t, s.DB, "a", "gpt-4o", now.Add(-1*time.Hour), models.RequestTypeFinal, 100, 50, 150, 0.001, true)
	seedLog(t, s.DB, "b", "gpt-4o", now.Add(-2*time.Hour), models.RequestTypeFinal, 200, 80, 280, 0.002, true)
	seedLog(t, s.DB, "c", "free-qwen", now.Add(-3*time.Hour), models.RequestTypeFinal, 300, 100, 400, 0, true)

	rec := callGET(s, "/dashboard/top-models?window=24h", s.TopModels)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out []TopModelStat
	decodeData(t, rec.Body.Bytes(), &out)

	byModel := map[string]TopModelStat{}
	for _, r := range out {
		byModel[r.Model] = r
	}
	gpt := byModel["gpt-4o"]
	if gpt.Calls != 2 || gpt.Tokens != 430 {
		t.Fatalf("gpt-4o = %+v, want calls=2 tokens=430", gpt)
	}
	if gpt.CostUSD < 0.00299 || gpt.CostUSD > 0.00301 {
		t.Fatalf("gpt-4o cost = %v, want ~0.003", gpt.CostUSD)
	}
	free := byModel["free-qwen"]
	if free.Tokens != 400 || free.CostUSD != 0 {
		t.Fatalf("free-qwen = %+v, want tokens=400 cost=0", free)
	}
}
