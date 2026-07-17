package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"autogateway/internal/channel"
	"autogateway/internal/config"
	"autogateway/internal/encryption"
	"autogateway/internal/keypool"
	"autogateway/internal/models"
	"autogateway/internal/services"
	"autogateway/internal/store"

	"gorm.io/gorm"
)

// buildProxyWithLog is buildProxy + a real RequestLogService so logRequest
// actually persists rows (buildProxy passes nil, making logRequest a no-op).
func buildProxyWithLog(t *testing.T, db *gorm.DB, st store.Store) (*ProxyServer, *services.RequestLogService) {
	t.Helper()
	registerCharTestChannel()

	encSvc, err := encryption.NewService("")
	if err != nil {
		t.Fatalf("encryption: %v", err)
	}
	settingsManager := config.NewSystemSettingsManager()
	subGroupManager := services.NewSubGroupManager(st)
	groupManager := services.NewGroupManager(db, st, settingsManager, subGroupManager)
	if err := groupManager.Initialize(); err != nil {
		t.Fatalf("group manager init: %v", err)
	}
	keyProvider := keypool.NewProvider(db, st, settingsManager, encSvc, nil)
	factory := channel.NewFactory(settingsManager, nil)
	rls := services.NewRequestLogService(db, st, settingsManager)

	ps, err := NewProxyServer(
		keyProvider, groupManager, subGroupManager, settingsManager,
		factory, rls, nil, nil, nil, encSvc,
	)
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	return ps, rls
}

// TestE2E_NonStreamCostCaptureChain drives a real request through the proxy
// (mock upstream → real channel round-trip → response handling → logRequest)
// and asserts the full ①成本可观测性 chain end-to-end:
//   1. upstream response forwarded to client unchanged
//   2. X-AC-* usage/cost headers set on the response
//   3. RequestLog row persisted with token counts + priced cost
//   4. the row is aggregatable the way the dashboard reads it
func TestE2E_NonStreamCostCaptureChain(t *testing.T) {
	db := charTestDB(t)
	if err := db.AutoMigrate(&models.RequestLog{}, &models.GroupHourlyStat{}); err != nil {
		t.Fatalf("migrate log tables: %v", err)
	}
	st := store.NewMemoryStore()

	// Mock upstream: 200 OK OpenAI chat completion carrying usage.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}}`))
	}))
	defer srv.Close()

	std := &models.Group{
		Name: "std", GroupType: "standard", ChannelType: charChannelType,
		TestModel: "m", Upstreams: []byte(`[{"url":"http://s","weight":1}]`),
	}
	if err := db.Create(std).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	setRoute("std", srv.URL)
	seedKeyIntoStore(t, st, std.ID, 301, "keyE")

	ps, rls := buildProxyWithLog(t, db, st)
	stdG, err := ps.groupManager.GetGroupByName("std")
	if err != nil {
		t.Fatalf("get group: %v", err)
	}
	handler, err := ps.channelFactory.GetChannel(stdG)
	if err != nil {
		t.Fatalf("get channel: %v", err)
	}

	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	c, rec := newGinCtx(body)
	ps.executeRequestWithRetry(c, handler, stdG, stdG, []byte(body), false, time.Now(), 0, map[string]bool{}, "gpt-4o")

	// 1) upstream body forwarded unchanged.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"content":"hi"`) {
		t.Fatalf("response body not forwarded: %s", rec.Body.String())
	}

	// 2) X-AC-* headers on the response.
	if got := rec.Header().Get("X-AC-Prompt-Tokens"); got != "100" {
		t.Fatalf("X-AC-Prompt-Tokens = %q, want 100", got)
	}
	if got := rec.Header().Get("X-AC-Completion-Tokens"); got != "50" {
		t.Fatalf("X-AC-Completion-Tokens = %q, want 50", got)
	}
	if got := rec.Header().Get("X-AC-Total-Tokens"); got != "150" {
		t.Fatalf("X-AC-Total-Tokens = %q, want 150", got)
	}
	// gpt-4o: 100/1e6*2.5 + 50/1e6*10 = 0.00075 → header present & non-zero.
	if got := rec.Header().Get("X-AC-Cost-USD"); got == "" || got == "0.000000" {
		t.Fatalf("X-AC-Cost-USD = %q, want non-zero priced cost", got)
	}

	// 3) RequestLog persisted. Default write-interval=1 → Record caches to the
	// store; Stop() flushes the cache to the DB (we never called Start, so wg is
	// empty and Stop returns after the flush).
	rls.Stop(context.Background())
	var rl models.RequestLog
	if err := db.Where("model = ?", "gpt-4o").First(&rl).Error; err != nil {
		t.Fatalf("request log not persisted: %v", err)
	}
	if rl.PromptTokens != 100 || rl.CompletionTokens != 50 || rl.TotalTokens != 150 {
		t.Fatalf("logged tokens = %d/%d/%d, want 100/50/150", rl.PromptTokens, rl.CompletionTokens, rl.TotalTokens)
	}
	if rl.CostUSD <= 0 {
		t.Fatalf("logged cost = %v, want > 0 (gpt-4o priced)", rl.CostUSD)
	}
	if !rl.IsSuccess || rl.RequestType != models.RequestTypeFinal {
		t.Fatalf("log meta wrong: success=%v type=%q", rl.IsSuccess, rl.RequestType)
	}

	// 4) aggregatable the way the dashboard reads (SUM over final rows).
	var agg struct {
		Total int64
		Cost  float64
	}
	if err := db.Model(&models.RequestLog{}).
		Select("COALESCE(SUM(total_tokens),0) as total, COALESCE(SUM(cost_usd),0) as cost").
		Where("request_type = ?", models.RequestTypeFinal).
		Scan(&agg).Error; err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if agg.Total != 150 || agg.Cost <= 0 {
		t.Fatalf("dashboard aggregate = total %d cost %v, want 150 / >0", agg.Total, agg.Cost)
	}
}
