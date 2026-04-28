# Three Quick Wins Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship three orthogonal usability improvements: (A) auto-detect channel type / version prefix when a user pastes an upstream base URL, (B) suggest aliases from recent 404/405 request logs, (C) allow `param_overrides` to differ per model within a group.

**Architecture:**
- A is a new probe service in `internal/services/upstream_probe.go` exposed via `GET /api/upstream/probe`. Frontend debounces input in `KeyCreateDialog.vue` and `AggregateGroupModal.vue` to call it. No DB writes.
- B is a read-only query in `internal/services/alias_suggestion_service.go` exposed via `GET /api/aliases/suggestions`. Frontend renders a dismissable banner above the alias list in `Aliases.vue`. No new DB tables.
- C is a backward-compatible upgrade of `applyParamOverrides` in `internal/proxy/request_helpers.go`. JSON shape `{"*": {…}, "model-id": {…}}` is interpreted as per-model; the existing flat shape `{"key": "value"}` keeps applying to all models. Detection is value-type-based (every value being a JSON object → nested mode).

**Tech Stack:** Go 1.24, Gin, GORM, dig DI, Vue 3, Naive UI, vitest (frontend not used yet — backend-only tests with `go test`).

---

## File Structure

**Created:**
- `internal/services/upstream_probe.go` — probe four candidate model-list endpoints, classify channel_type + version prefix, return latency.
- `internal/services/upstream_probe_test.go` — unit tests with `httptest.Server` faking the four upstreams.
- `internal/services/alias_suggestion_service.go` — query `request_logs` for unmatched models, cross-check against `model_aliases` and `groups.available_models`.
- `internal/services/alias_suggestion_service_test.go` — unit tests over an in-memory SQLite DB.
- `internal/handler/upstream_probe_handler.go` — gin handler wrapping the service.
- `internal/handler/alias_suggestion_handler.go` — gin handler wrapping the suggestion service.
- `internal/proxy/request_helpers_test.go` — tests for the upgraded `applyParamOverrides`.
- `web/src/api/upstream.ts` — frontend client for `GET /api/upstream/probe`.
- `web/src/components/keys/UrlProbeBadge.vue` — small inline ✓/⚠ badge component reused by both modals.

**Modified:**
- `internal/container/container.go` — register two new services + two new handlers.
- `internal/router/router.go` — add `GET /api/upstream/probe` and `GET /api/aliases/suggestions`.
- `internal/proxy/request_helpers.go` — replace `applyParamOverrides` body with the model-aware version.
- `web/src/components/keys/AggregateGroupModal.vue` — wire `UrlProbeBadge` (note: this modal has no upstreams, so probing not applicable here — see Task A note below; this entry stays but may be a no-op).
- `web/src/views/Keys.vue` or whichever component owns the standard-group create form (`KeyCreateDialog.vue` is for keys, not groups; the group create form is opened from `V3GroupSidebar.vue` via `<create-group-modal>`). We'll wire the badge into the standard group create modal instead.
- `web/src/views/Aliases.vue` — add suggestions banner.
- `web/src/api/aliases.ts` — add `suggestions()` call.

**Note on the group create modal:** `V3GroupSidebar.vue` references `<create-group-modal>` as the standard-group create UI. Locate it before Task A3 (search: `rg "name=\"create-group-modal\"|<create-group-modal" web/src`). The component name in code may differ; confirm the actual file at execution time.

---

## Task A: Upstream URL Probe

### Task A1: Probe service core (TDD)

**Files:**
- Create: `internal/services/upstream_probe.go`
- Create: `internal/services/upstream_probe_test.go`

- [ ] **Step 1: Write the failing test for OpenAI-style probe**

Create `internal/services/upstream_probe_test.go`:

```go
package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProbeOpenAIStyle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(http.StatusUnauthorized) // 401 = endpoint exists, just no auth
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got, err := ProbeUpstream(ctx, srv.URL)
	if err != nil {
		t.Fatalf("ProbeUpstream returned error: %v", err)
	}
	if got.ChannelType != "openai" {
		t.Errorf("expected channel openai, got %q", got.ChannelType)
	}
	if got.VersionPrefix != "/v1" {
		t.Errorf("expected version prefix /v1, got %q", got.VersionPrefix)
	}
}

func TestProbeAnthropicStyle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" && r.Header.Get("anthropic-version") != "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	got, err := ProbeUpstream(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("ProbeUpstream returned error: %v", err)
	}
	if got.ChannelType != "anthropic" {
		t.Errorf("expected channel anthropic, got %q", got.ChannelType)
	}
}

func TestProbeGeminiStyle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1beta/models" {
			w.WriteHeader(http.StatusForbidden) // gemini returns 403 when key missing
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	got, err := ProbeUpstream(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("ProbeUpstream returned error: %v", err)
	}
	if got.ChannelType != "gemini" {
		t.Errorf("expected gemini, got %q", got.ChannelType)
	}
	if got.VersionPrefix != "/v1beta" {
		t.Errorf("expected /v1beta, got %q", got.VersionPrefix)
	}
}

func TestProbeUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := ProbeUpstream(context.Background(), srv.URL)
	if err == nil {
		t.Fatalf("expected error for unknown upstream, got nil")
	}
}

func TestProbeRejectsNonHTTP(t *testing.T) {
	_, err := ProbeUpstream(context.Background(), "file:///etc/passwd")
	if err == nil {
		t.Fatalf("expected scheme rejection, got nil")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/services/ -run 'TestProbe' -v`
Expected: FAIL — `undefined: ProbeUpstream`.

- [ ] **Step 3: Implement `ProbeUpstream`**

Create `internal/services/upstream_probe.go`:

```go
package services

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ProbeResult is what /api/upstream/probe returns.
type ProbeResult struct {
	ChannelType   string `json:"channel_type"`   // openai | anthropic | gemini
	VersionPrefix string `json:"version_prefix"` // /v1 or /v1beta
	NormalizedURL string `json:"normalized_url"` // user-facing canonical base URL
	StatusCode    int    `json:"status_code"`    // upstream status (often 401/403, that's OK)
	LatencyMs     int64  `json:"latency_ms"`
}

var probeClient = &http.Client{Timeout: 3 * time.Second}

// ProbeUpstream fans out three HEAD-equivalent GETs to the OpenAI, Anthropic
// and Gemini model-list endpoints and returns the first that responds with a
// non-network error. 401 / 403 count as "endpoint exists" — we just have no
// auth credentials. We deliberately do NOT require a successful 2xx because
// that would make probing unauthenticated upstreams impossible.
func ProbeUpstream(ctx context.Context, rawURL string) (*ProbeResult, error) {
	u, err := url.Parse(strings.TrimRight(rawURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("only http/https probing is allowed, got %q", u.Scheme)
	}
	base := u.String()

	type attempt struct {
		channel string
		path    string
		header  map[string]string
	}
	attempts := []attempt{
		{"anthropic", "/v1/models", map[string]string{"anthropic-version": "2023-06-01"}},
		{"gemini", "/v1beta/models", nil},
		{"openai", "/v1/models", nil},
	}

	type outcome struct {
		res *ProbeResult
		err error
	}
	results := make(chan outcome, len(attempts))
	var wg sync.WaitGroup
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	for _, a := range attempts {
		a := a
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- runOne(probeCtx, base, a.channel, a.path, a.header)
		}()
	}
	go func() { wg.Wait(); close(results) }()

	var firstHit *ProbeResult
	for r := range results {
		if r.err != nil || r.res == nil {
			continue
		}
		// Anthropic-specific header check: only accept anthropic if its
		// distinguishing header was honored (i.e. server didn't 404).
		if firstHit == nil || preferChannel(r.res.ChannelType, firstHit.ChannelType) {
			firstHit = r.res
		}
	}
	if firstHit == nil {
		return nil, fmt.Errorf("no known upstream protocol responded at %s", base)
	}
	return firstHit, nil
}

// preferChannel picks anthropic > gemini > openai when more than one endpoint
// responds (anthropic and openai share /v1/models, so a real Anthropic server
// will hit both attempts). Anthropic header is the disambiguator.
func preferChannel(candidate, current string) bool {
	rank := map[string]int{"anthropic": 3, "gemini": 2, "openai": 1}
	return rank[candidate] > rank[current]
}

func runOne(ctx context.Context, base, channel, path string, headers map[string]string) outcome {
	endpoint := base + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return outcome{nil, err}
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	start := time.Now()
	resp, err := probeClient.Do(req)
	if err != nil {
		return outcome{nil, err}
	}
	defer resp.Body.Close()
	// Treat anything that's not "I don't know this URL" as a hit.
	if resp.StatusCode == http.StatusNotFound {
		return outcome{nil, fmt.Errorf("404")}
	}
	prefix := "/v1"
	if strings.HasPrefix(path, "/v1beta") {
		prefix = "/v1beta"
	}
	return outcome{&ProbeResult{
		ChannelType:   channel,
		VersionPrefix: prefix,
		NormalizedURL: base,
		StatusCode:    resp.StatusCode,
		LatencyMs:     time.Since(start).Milliseconds(),
	}, nil}
}

type outcome struct {
	res *ProbeResult
	err error
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/services/ -run 'TestProbe' -v`
Expected: PASS for all four (`OpenAIStyle`, `AnthropicStyle`, `GeminiStyle`, `Unknown`, `RejectsNonHTTP`).

- [ ] **Step 5: Commit**

```bash
git add internal/services/upstream_probe.go internal/services/upstream_probe_test.go
git commit -m "$(cat <<'EOF'
✨ feat(probe): detect upstream channel_type from base URL

Probes /v1/models (openai/anthropic) and /v1beta/models (gemini)
in parallel; treats 401/403 as endpoint-exists. anthropic > gemini >
openai disambiguation when /v1/models responds for both.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task A2: Probe handler + route + DI registration

**Files:**
- Create: `internal/handler/upstream_probe_handler.go`
- Modify: `internal/container/container.go`
- Modify: `internal/router/router.go`

- [ ] **Step 1: Write the handler**

Create `internal/handler/upstream_probe_handler.go`:

```go
package handler

import (
	"strings"

	app_errors "autogateway/internal/errors"
	"autogateway/internal/response"
	"autogateway/internal/services"

	"github.com/gin-gonic/gin"
)

// UpstreamProbeHandler exposes GET /api/upstream/probe.
type UpstreamProbeHandler struct{}

func NewUpstreamProbeHandler() *UpstreamProbeHandler { return &UpstreamProbeHandler{} }

// Probe accepts ?url=<base_url> and returns ProbeResult or 400/502.
func (h *UpstreamProbeHandler) Probe(c *gin.Context) {
	raw := strings.TrimSpace(c.Query("url"))
	if raw == "" {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_payload")
		return
	}
	res, err := services.ProbeUpstream(c.Request.Context(), raw)
	if err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrBadGateway, err.Error()))
		return
	}
	response.Success(c, res)
}
```

- [ ] **Step 2: Verify `app_errors.ErrBadGateway` exists**

Run: `grep -n "ErrBadGateway" internal/errors/errors.go`
If absent, add it next to other 5xx errors. Do NOT proceed without confirming. If absent, add this line in `internal/errors/errors.go` near the existing 5xx errors:

```go
ErrBadGateway = &APIError{HTTPStatus: 502, Code: "bad_gateway", Message: "Bad gateway"}
```

- [ ] **Step 3: Register handler in DI container**

Edit `internal/container/container.go` after the `NewCommonHandler` block:

```go
if err := container.Provide(handler.NewUpstreamProbeHandler); err != nil {
    return nil, err
}
```

- [ ] **Step 4: Wire route in router**

Edit `internal/router/router.go` `NewRouter` signature: add `upstreamProbeHandler *handler.UpstreamProbeHandler` parameter. Edit `registerAPIRoutes` to thread it through. In `registerProtectedAPIRoutes`, add before the `groups := api.Group("/groups")` block:

```go
api.GET("/upstream/probe", upstreamProbeHandler.Probe)
```

Also update the `BuildContainer().Invoke(...)` call site (search: `rg "router.NewRouter" -n`) to pull the new dependency through dig — dig does this automatically as long as `Provide` was called.

- [ ] **Step 5: Build to confirm wiring**

Run: `go build ./...`
Expected: clean build.

- [ ] **Step 6: Smoke-test the route**

Run: `go run ./main.go &`. Wait 2s. `curl -H "Authorization: Bearer $AUTH_KEY" 'http://localhost:3001/api/upstream/probe?url=https://api.openai.com'`. Expected: JSON with `channel_type:"openai"`, `version_prefix:"/v1"`. Stop server.

- [ ] **Step 7: Commit**

```bash
git add internal/handler/upstream_probe_handler.go internal/container/container.go internal/router/router.go internal/errors/errors.go
git commit -m "$(cat <<'EOF'
✨ feat(probe): expose GET /api/upstream/probe

Wire UpstreamProbeHandler into dig container and protected
API group. Smoke-tested against api.openai.com.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task A3: Frontend probe badge

**Files:**
- Create: `web/src/api/upstream.ts`
- Create: `web/src/components/keys/UrlProbeBadge.vue`
- Modify: standard-group create modal (locate via `rg "create-group-modal" web/src`; likely under `web/src/components/keys/`)

- [ ] **Step 1: Add API client**

Create `web/src/api/upstream.ts`:

```ts
import { api } from "./request"; // confirm helper name in web/src/api/request.ts

export interface ProbeResult {
  channel_type: "openai" | "anthropic" | "gemini";
  version_prefix: string;
  normalized_url: string;
  status_code: number;
  latency_ms: number;
}

export const upstreamApi = {
  async probe(url: string): Promise<ProbeResult> {
    const r = await api.get<ProbeResult>("/api/upstream/probe", { params: { url } });
    return r.data;
  },
};
```

If `api` import path is wrong, run `rg "from \"@/api/" web/src/api/aliases.ts` and copy the exact import shape used there.

- [ ] **Step 2: Create badge component**

Create `web/src/components/keys/UrlProbeBadge.vue`:

```vue
<script setup lang="ts">
import { ref, watch } from "vue";
import { upstreamApi, type ProbeResult } from "@/api/upstream";

const props = defineProps<{ url: string }>();
const emit = defineEmits<{ (e: "detected", res: ProbeResult): void }>();

const state = ref<"idle" | "probing" | "ok" | "fail">("idle");
const detail = ref<ProbeResult | null>(null);
let timer: number | undefined;

watch(
  () => props.url,
  next => {
    if (timer) {
      window.clearTimeout(timer);
    }
    if (!next || !/^https?:\/\//.test(next)) {
      state.value = "idle";
      detail.value = null;
      return;
    }
    state.value = "probing";
    timer = window.setTimeout(async () => {
      try {
        const res = await upstreamApi.probe(next);
        state.value = "ok";
        detail.value = res;
        emit("detected", res);
      } catch {
        state.value = "fail";
        detail.value = null;
      }
    }, 500);
  },
  { immediate: true }
);
</script>

<template>
  <span class="probe-badge" :data-state="state">
    <template v-if="state === 'idle'">&nbsp;</template>
    <template v-else-if="state === 'probing'">… probing</template>
    <template v-else-if="state === 'ok' && detail">
      ✓ {{ detail.channel_type }} ({{ detail.version_prefix }}) · {{ detail.latency_ms }}ms
    </template>
    <template v-else>⚠ unknown</template>
  </span>
</template>

<style scoped>
.probe-badge {
  font: 500 11px/1.6 var(--v3-mono);
  padding: 2px 6px;
  border-radius: 4px;
}
.probe-badge[data-state="ok"] {
  background: var(--v3-ok-soft);
  color: var(--v3-ok);
}
.probe-badge[data-state="fail"] {
  background: oklch(0.96 0.05 80);
  color: oklch(0.5 0.15 80);
}
.probe-badge[data-state="probing"] {
  color: var(--v3-ink-3);
}
</style>
```

- [ ] **Step 3: Wire badge into the standard-group create modal**

Locate the modal: run `rg -n "<create-group-modal|name=\"create-group-modal\"" web/src`. Open the file referenced by the `<script>` import. Find the `n-input` for `upstreams[0].url` (or equivalent) and add next to it:

```vue
<UrlProbeBadge
  :url="formData.upstreams[0]?.url || ''"
  @detected="onUrlDetected"
/>
```

In the `<script setup>`, import the badge and wire `onUrlDetected`:

```ts
import UrlProbeBadge from "@/components/keys/UrlProbeBadge.vue";
import type { ProbeResult } from "@/api/upstream";

function onUrlDetected(res: ProbeResult) {
  // Only auto-fill when user hasn't explicitly chosen a channel yet.
  if (!formData.channel_type || formData.channel_type === "openai") {
    formData.channel_type = res.channel_type;
  }
}
```

- [ ] **Step 4: Manual verify in browser**

Run `make run` (or `cd web && npm run dev` + `go run ./main.go` in two terminals). Open `http://localhost:3001`. Open the create-group dialog. Paste `https://api.anthropic.com`. Expected: badge shows `✓ anthropic (/v1) · …ms` within ~1s and `channel_type` flips to Anthropic. Paste `https://api.groq.com/openai`. Expected: `✓ openai (/v1)`. Paste a 404 URL. Expected: `⚠ unknown`.

- [ ] **Step 5: Commit**

```bash
git add web/src/api/upstream.ts web/src/components/keys/UrlProbeBadge.vue web/src/components/keys/<modal-file>.vue
git commit -m "$(cat <<'EOF'
✨ feat(probe): pasted base URL auto-detects channel_type

Inline ✓/⚠ badge calls /api/upstream/probe on debounce; fills
channel_type when user hasn't customized it.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task B: Log-Driven Alias Suggestions

### Task B1: Suggestion service (TDD)

**Files:**
- Create: `internal/services/alias_suggestion_service.go`
- Create: `internal/services/alias_suggestion_service_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/services/alias_suggestion_service_test.go`:

```go
package services

import (
	"context"
	"testing"
	"time"

	"autogateway/internal/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.RequestLog{}, &models.ModelAlias{}, &models.Group{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

func TestSuggestionsFromUnmatchedLogs(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()

	// Two failed requests for "gpt-4.1" and one for "claude-omega"
	logs := []models.RequestLog{
		{ID: "1", Timestamp: now.Add(-1 * time.Hour), Model: "gpt-4.1", StatusCode: 405, IsSuccess: false},
		{ID: "2", Timestamp: now.Add(-30 * time.Minute), Model: "gpt-4.1", StatusCode: 404, IsSuccess: false},
		{ID: "3", Timestamp: now.Add(-2 * time.Hour), Model: "claude-omega", StatusCode: 405, IsSuccess: false},
		{ID: "4", Timestamp: now.Add(-3 * time.Hour), Model: "gpt-4o", StatusCode: 200, IsSuccess: true},   // success → ignored
		{ID: "5", Timestamp: now.Add(-72 * time.Hour), Model: "old-model", StatusCode: 405, IsSuccess: false}, // outside window
	}
	if err := db.Create(&logs).Error; err != nil {
		t.Fatalf("seed logs: %v", err)
	}

	svc := NewAliasSuggestionService(db)
	got, err := svc.Suggest(context.Background(), 24*time.Hour)
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 suggestions, got %d: %+v", len(got), got)
	}
	if got[0].Model != "gpt-4.1" || got[0].Count != 2 {
		t.Errorf("expected gpt-4.1 count=2 first, got %+v", got[0])
	}
	if got[1].Model != "claude-omega" || got[1].Count != 1 {
		t.Errorf("expected claude-omega count=1 second, got %+v", got[1])
	}
}

func TestSuggestionsExcludeAlreadyAliased(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()
	if err := db.Create(&[]models.RequestLog{
		{ID: "1", Timestamp: now, Model: "gpt-4.1", StatusCode: 405, IsSuccess: false},
		{ID: "2", Timestamp: now, Model: "claude-omega", StatusCode: 405, IsSuccess: false},
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := db.Create(&models.ModelAlias{
		Alias: "gpt-4.1", GroupID: 1, RealModel: "gpt-4o", Weight: 1, Priority: 100, Enabled: true,
	}).Error; err != nil {
		t.Fatalf("seed alias: %v", err)
	}

	svc := NewAliasSuggestionService(db)
	got, err := svc.Suggest(context.Background(), 24*time.Hour)
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if len(got) != 1 || got[0].Model != "claude-omega" {
		t.Fatalf("expected only claude-omega, got %+v", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/services/ -run 'TestSuggestions' -v`
Expected: FAIL — `undefined: NewAliasSuggestionService`.

- [ ] **Step 3: Implement the service**

Create `internal/services/alias_suggestion_service.go`:

```go
package services

import (
	"context"
	"time"

	"autogateway/internal/models"

	"gorm.io/gorm"
)

// AliasSuggestion is one row in the response: a client-supplied model that
// repeatedly failed with 404/405 over the lookback window and currently has
// no matching alias.
type AliasSuggestion struct {
	Model    string    `json:"model"`
	Count    int64     `json:"count"`
	LastSeen time.Time `json:"last_seen"`
}

// AliasSuggestionService scans request_logs for models the gateway didn't
// know and weren't already aliased.
type AliasSuggestionService struct {
	db *gorm.DB
}

func NewAliasSuggestionService(db *gorm.DB) *AliasSuggestionService {
	return &AliasSuggestionService{db: db}
}

// Suggest returns up to 20 unmatched models ordered by failure count desc.
func (s *AliasSuggestionService) Suggest(ctx context.Context, lookback time.Duration) ([]AliasSuggestion, error) {
	since := time.Now().Add(-lookback)
	type row struct {
		Model    string
		Count    int64
		LastSeen time.Time
	}
	var rows []row
	if err := s.db.WithContext(ctx).
		Model(&models.RequestLog{}).
		Select("model, COUNT(*) as count, MAX(timestamp) as last_seen").
		Where("status_code IN (404, 405) AND model <> '' AND timestamp >= ?", since).
		Group("model").
		Order("count DESC").
		Limit(20).
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	if len(rows) == 0 {
		return nil, nil
	}

	// Filter out models already covered by an enabled alias.
	candidates := make([]string, 0, len(rows))
	for _, r := range rows {
		candidates = append(candidates, r.Model)
	}
	var aliased []string
	if err := s.db.WithContext(ctx).
		Model(&models.ModelAlias{}).
		Where("alias IN ? AND enabled = ?", candidates, true).
		Distinct("alias").
		Pluck("alias", &aliased).Error; err != nil {
		return nil, err
	}
	skip := make(map[string]bool, len(aliased))
	for _, a := range aliased {
		skip[a] = true
	}

	out := make([]AliasSuggestion, 0, len(rows))
	for _, r := range rows {
		if skip[r.Model] {
			continue
		}
		out = append(out, AliasSuggestion{Model: r.Model, Count: r.Count, LastSeen: r.LastSeen})
	}
	return out, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/services/ -run 'TestSuggestions' -v`
Expected: PASS for both.

- [ ] **Step 5: Commit**

```bash
git add internal/services/alias_suggestion_service.go internal/services/alias_suggestion_service_test.go
git commit -m "$(cat <<'EOF'
✨ feat(aliases): suggest aliases from 404/405 request logs

Group request_logs by model where status in (404,405) over the
last 24h, exclude already-aliased models, return top 20.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task B2: Suggestion handler + route + DI

**Files:**
- Create: `internal/handler/alias_suggestion_handler.go`
- Modify: `internal/container/container.go`
- Modify: `internal/router/router.go`

- [ ] **Step 1: Write the handler**

Create `internal/handler/alias_suggestion_handler.go`:

```go
package handler

import (
	"time"

	app_errors "autogateway/internal/errors"
	"autogateway/internal/response"
	"autogateway/internal/services"

	"github.com/gin-gonic/gin"
)

// AliasSuggestionHandler exposes GET /api/aliases/suggestions.
type AliasSuggestionHandler struct {
	svc *services.AliasSuggestionService
}

func NewAliasSuggestionHandler(svc *services.AliasSuggestionService) *AliasSuggestionHandler {
	return &AliasSuggestionHandler{svc: svc}
}

func (h *AliasSuggestionHandler) Suggest(c *gin.Context) {
	rows, err := h.svc.Suggest(c.Request.Context(), 24*time.Hour)
	if err != nil {
		response.Error(c, app_errors.ParseDBError(err))
		return
	}
	response.Success(c, rows)
}
```

- [ ] **Step 2: Register in DI**

Edit `internal/container/container.go` next to the existing alias entries:

```go
if err := container.Provide(services.NewAliasSuggestionService); err != nil {
    return nil, err
}
if err := container.Provide(handler.NewAliasSuggestionHandler); err != nil {
    return nil, err
}
```

- [ ] **Step 3: Wire route**

In `internal/router/router.go` `registerProtectedAPIRoutes`, find the `aliases := api.Group("/aliases")` block and add **inside** the braces:

```go
aliases.GET("/suggestions", aliasSuggestionHandler.Suggest)
```

Add `aliasSuggestionHandler *handler.AliasSuggestionHandler` to `registerProtectedAPIRoutes`, `registerAPIRoutes`, and `NewRouter` parameter lists. The dig container will inject it.

- [ ] **Step 4: Build**

Run: `go build ./...`
Expected: clean build.

- [ ] **Step 5: Commit**

```bash
git add internal/handler/alias_suggestion_handler.go internal/container/container.go internal/router/router.go
git commit -m "$(cat <<'EOF'
✨ feat(aliases): expose GET /api/aliases/suggestions

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task B3: Frontend banner

**Files:**
- Modify: `web/src/api/aliases.ts`
- Modify: `web/src/views/Aliases.vue`

- [ ] **Step 1: Extend API client**

Edit `web/src/api/aliases.ts`. Add to `aliasesApi`:

```ts
async suggestions(): Promise<AliasSuggestion[]> {
  const r = await api.get<AliasSuggestion[]>("/api/aliases/suggestions");
  return r.data;
},
```

And export the type:

```ts
export interface AliasSuggestion {
  model: string;
  count: number;
  last_seen: string;
}
```

- [ ] **Step 2: Render banner in Aliases.vue**

Edit `web/src/views/Aliases.vue`. In `<script setup>`, add:

```ts
import type { AliasSuggestion } from "@/api/aliases";
const suggestions = ref<AliasSuggestion[]>([]);
async function loadSuggestions() {
  try {
    suggestions.value = await aliasesApi.suggestions();
  } catch {
    suggestions.value = [];
  }
}
// in onMounted, after loadAll():
onMounted(async () => {
  await loadAll();
  await loadSuggestions();
});
```

In `<template>`, before the alias list, add:

```vue
<div v-if="suggestions.length" class="v3-card v3-suggest-banner">
  <div class="v3-suggest-banner__title">
    {{ t("v5.suggestionsTitle") || "Detected unknown models in recent requests" }}
  </div>
  <div class="v3-suggest-banner__list">
    <button
      v-for="s in suggestions"
      :key="s.model"
      class="v3-suggest-chip"
      @click="onClickSuggestion(s)"
      :title="`Last seen ${s.last_seen}`"
    >
      {{ s.model }}
      <span class="v3-suggest-count">×{{ s.count }}</span>
    </button>
  </div>
</div>
```

```ts
function onClickSuggestion(s: AliasSuggestion) {
  // Open the existing picker, prefilling the alias name as the unmatched model.
  pickerTargetAlias.value = s.model;
  pickerSearch.value = "";
  pickerOpen.value = true;
}
```

Add minimal CSS at the end of the existing `<style>` block:

```css
.v3-suggest-banner {
  padding: 12px 14px;
  margin-bottom: 12px;
}
.v3-suggest-banner__title {
  font: 500 12px/1.4 var(--v3-sans);
  color: var(--v3-ink-2);
  margin-bottom: 8px;
}
.v3-suggest-banner__list {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.v3-suggest-chip {
  padding: 4px 8px;
  border: 1px solid var(--v3-rule);
  border-radius: 4px;
  background: var(--v3-bg);
  cursor: pointer;
  font: 500 11px/1 var(--v3-mono);
}
.v3-suggest-chip:hover {
  border-color: var(--v3-accent);
}
.v3-suggest-count {
  margin-left: 4px;
  color: var(--v3-ink-3);
}
```

- [ ] **Step 3: Manual verify**

Start dev server. Force a few failing requests with unknown model names: `curl -H "Authorization: Bearer $AUTH_KEY" -X POST http://localhost:3001/openai/v1/chat/completions -d '{"model":"ghost-model","messages":[{"role":"user","content":"hi"}]}'`. Wait for the request log flush interval (or set `RequestLogWriteIntervalMinutes=1` in settings). Reload `/aliases`. Expected: `ghost-model ×N` chip appears at top.

- [ ] **Step 4: Commit**

```bash
git add web/src/api/aliases.ts web/src/views/Aliases.vue
git commit -m "$(cat <<'EOF'
✨ feat(aliases): suggest aliases from recent unknown models

Top banner shows models hit by 404/405 in last 24h; click opens
picker prefilled with the model name as alias.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task C: Per-Model Param Overrides

### Task C1: Upgrade `applyParamOverrides` (TDD)

**Files:**
- Create: `internal/proxy/request_helpers_test.go`
- Modify: `internal/proxy/request_helpers.go`

- [ ] **Step 1: Write failing tests covering both shapes**

Create `internal/proxy/request_helpers_test.go`:

```go
package proxy

import (
	"encoding/json"
	"reflect"
	"testing"

	"autogateway/internal/models"

	"gorm.io/datatypes"
)

func mustJSONMap(t *testing.T, raw string) datatypes.JSONMap {
	t.Helper()
	m := datatypes.JSONMap{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	return m
}

func TestApplyOverridesFlatLegacyShape(t *testing.T) {
	ps := &ProxyServer{}
	body := []byte(`{"model":"gpt-4o","temperature":0.9}`)
	g := &models.Group{ParamOverrides: mustJSONMap(t, `{"temperature":0.3,"top_p":0.5}`)}

	out, err := ps.applyParamOverrides(body, g)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["temperature"] != 0.3 {
		t.Errorf("flat override should set temperature=0.3, got %v", got["temperature"])
	}
	if got["top_p"] != 0.5 {
		t.Errorf("flat override should set top_p=0.5, got %v", got["top_p"])
	}
}

func TestApplyOverridesNestedShapeStarPlusModel(t *testing.T) {
	ps := &ProxyServer{}
	body := []byte(`{"model":"gpt-5","messages":[]}`)
	g := &models.Group{
		ParamOverrides: mustJSONMap(t, `{
			"*": {"temperature": 0.2},
			"gpt-5": {"reasoning_effort": "high"}
		}`),
	}

	out, err := ps.applyParamOverrides(body, g)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if got["temperature"] != 0.2 {
		t.Errorf("expected * fallback temperature=0.2, got %v", got["temperature"])
	}
	if got["reasoning_effort"] != "high" {
		t.Errorf("expected gpt-5 override reasoning_effort=high, got %v", got["reasoning_effort"])
	}
}

func TestApplyOverridesNestedModelOverridesStar(t *testing.T) {
	ps := &ProxyServer{}
	body := []byte(`{"model":"gpt-5"}`)
	g := &models.Group{
		ParamOverrides: mustJSONMap(t, `{
			"*": {"temperature": 0.2},
			"gpt-5": {"temperature": 0.9}
		}`),
	}
	out, _ := ps.applyParamOverrides(body, g)
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if got["temperature"] != 0.9 {
		t.Errorf("model-specific must beat *, got %v", got["temperature"])
	}
}

func TestApplyOverridesNestedNoMatchUsesStarOnly(t *testing.T) {
	ps := &ProxyServer{}
	body := []byte(`{"model":"gpt-4o-mini"}`)
	g := &models.Group{
		ParamOverrides: mustJSONMap(t, `{
			"*": {"temperature": 0.2},
			"gpt-5": {"reasoning_effort": "high"}
		}`),
	}
	out, _ := ps.applyParamOverrides(body, g)
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if got["temperature"] != 0.2 {
		t.Errorf("expected * fallback, got %v", got["temperature"])
	}
	if _, ok := got["reasoning_effort"]; ok {
		t.Errorf("non-matching model should not get gpt-5 overrides, got %v", got)
	}
}

func TestApplyOverridesEmptyBody(t *testing.T) {
	ps := &ProxyServer{}
	g := &models.Group{ParamOverrides: mustJSONMap(t, `{"temperature":0.3}`)}
	out, err := ps.applyParamOverrides([]byte(""), g)
	if err != nil || !reflect.DeepEqual(out, []byte("")) {
		t.Errorf("empty body should pass through unchanged: %v %v", out, err)
	}
}
```

- [ ] **Step 2: Run tests to verify failures**

Run: `go test ./internal/proxy/ -run 'TestApplyOverrides' -v`
Expected: legacy/empty pass; nested tests FAIL because the current implementation flat-applies all entries (it would write `*` and `gpt-5` map values directly into the request).

- [ ] **Step 3: Replace `applyParamOverrides` body**

Edit `internal/proxy/request_helpers.go`. Replace the function body:

```go
func (ps *ProxyServer) applyParamOverrides(bodyBytes []byte, group *models.Group) ([]byte, error) {
	if len(group.ParamOverrides) == 0 || len(bodyBytes) == 0 {
		return bodyBytes, nil
	}

	var requestData map[string]any
	if err := json.Unmarshal(bodyBytes, &requestData); err != nil {
		logrus.Warnf("failed to unmarshal request body for param override, passing through: %v", err)
		return bodyBytes, nil
	}

	if isNestedOverrides(group.ParamOverrides) {
		modelName, _ := requestData["model"].(string)
		applyNested(requestData, group.ParamOverrides, modelName)
	} else {
		for key, value := range group.ParamOverrides {
			requestData[key] = value
		}
	}

	return json.Marshal(requestData)
}

// isNestedOverrides reports whether every value in the override map is itself
// a JSON object — that's the marker for the {"*": {...}, "model-id": {...}}
// shape. Any non-object value collapses us back to the legacy flat shape.
func isNestedOverrides(o map[string]any) bool {
	if len(o) == 0 {
		return false
	}
	for _, v := range o {
		if _, ok := v.(map[string]any); !ok {
			return false
		}
	}
	return true
}

func applyNested(requestData, overrides map[string]any, modelName string) {
	if star, ok := overrides["*"].(map[string]any); ok {
		for k, v := range star {
			requestData[k] = v
		}
	}
	if modelName == "" {
		return
	}
	if specific, ok := overrides[modelName].(map[string]any); ok {
		for k, v := range specific {
			requestData[k] = v
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/proxy/ -run 'TestApplyOverrides' -v`
Expected: all five PASS.

- [ ] **Step 5: Run full test suite to confirm nothing else broke**

Run: `go test ./...`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/proxy/request_helpers.go internal/proxy/request_helpers_test.go
git commit -m "$(cat <<'EOF'
✨ feat(overrides): per-model param_overrides via {*, model-id} shape

Detect nested override shape (every value is a JSON object) and
apply * fallback then model-specific. Legacy flat shape continues
to apply to all models — fully backward compatible.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task C2: Frontend hint in group config UI

**Files:**
- Modify: the param_overrides editor (locate via `rg "param_overrides|paramOverrides" web/src`)

- [ ] **Step 1: Locate the editor**

Run: `rg -n "param_overrides|paramOverrides" web/src`. Expected: a JSON textarea or key/value list inside the standard-group create/edit modal. Open that file.

- [ ] **Step 2: Add an inline help string**

Below the param_overrides editor input, add:

```vue
<div class="v3-help-hint">
  {{ t("keys.paramOverridesHelp") || "Flat shape applies to all models. Use { \"*\": {…}, \"model-id\": {…} } for per-model overrides." }}
</div>
```

```css
.v3-help-hint {
  font: 500 11px/1.4 var(--v3-sans);
  color: var(--v3-ink-3);
  margin-top: 4px;
}
```

- [ ] **Step 3: Manual verify**

Reload `/keys`. Open edit on any group. Confirm the hint shows beneath the param_overrides input. Save a group with `{"*": {"temperature": 0.2}, "gpt-5": {"reasoning_effort": "high"}}`. Run a chat completion against `gpt-5`; check the upstream request body in logs (`logs.is_stream=false`, `request_body` field) shows `reasoning_effort: high` AND `temperature: 0.2`. Run against `gpt-4o-mini`; expect only `temperature: 0.2`.

- [ ] **Step 4: Commit**

```bash
git add web/src/<file-touched>.vue
git commit -m "$(cat <<'EOF'
📝 docs(overrides): hint nested {*, model-id} shape in editor

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Self-Review Checklist

**Spec coverage:**
- A.1 paste-then-detect → Tasks A1/A2/A3 ✓
- B.1 alias suggestions from logs → Tasks B1/B2/B3 ✓
- C.1 per-model param overrides → Tasks C1/C2 ✓

**Placeholder scan:**
- Task A3 leaves the standard-group create modal filename unresolved — the `rg` lookup is shown explicitly with the search string. Acceptable because the file truly varies (V3 chrome) and we hand the executor the exact command.
- Task C2 same — explicit `rg` lookup. The hint string is concrete.

**Type consistency:**
- `ProbeResult` defined in A1, imported the same in A3.
- `AliasSuggestion` defined in B1, exported in B3 frontend mirror with same fields (`model`, `count`, `last_seen`).
- `applyParamOverrides` signature unchanged — only behavior changes.

**Risks called out:**
- Task A's probe is unauthenticated outbound HTTP. Limited to http/https. Auth-protected route, so only logged-in operators can trigger.
- Task B's query scans `request_logs` grouped by model — already indexed (`Model varchar(255);index`).
- Task C's detection (`every value is map`) breaks if a future flat override has a JSON-object value (e.g. a dict-typed param). None of the current `GroupConfig` knobs have that shape; if it appears later, switch detection to a versioned key like `__per_model__: true`.
