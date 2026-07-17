package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"autogateway/internal/channel"
	"autogateway/internal/config"
	"autogateway/internal/encryption"
	"autogateway/internal/keypool"
	"autogateway/internal/models"
	"autogateway/internal/services"
	"autogateway/internal/store"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// =============================================================================
// Characterization tests for executeRequestWithRetry's failure / failover path.
//
// These lock the *observable behavior* of the failure block (and, after the
// refactor, of handleAttemptFailure) so the extraction can be proven
// byte-for-byte behavior-preserving:
//   - aggregate group: sub-group A returns a failover status (500) -> request
//     switches to sub-group B and succeeds, with B actually receiving the call;
//   - standard group: upstream keeps failing, retries are exhausted -> the
//     upstream error status is returned to the client.
//
// The proxy is wired end-to-end with real collaborators (KeyProvider,
// SubGroupManager, GroupManager, channel.Factory) backed by an in-memory sqlite
// DB + in-memory store. The only test double is the channel proxy, registered
// under a dedicated channel type so the Factory hands it out during failover
// recursion; it forwards to httptest upstreams keyed by group name.
// =============================================================================

// charTestRoutes maps group name -> upstream base URL for the test channel.
// Populated per-test before the proxy runs. Guarded for the (sequential) tests.
var (
	charTestRoutes   = map[string]string{}
	charTestRoutesMu sync.Mutex
	charChannelOnce  sync.Once
)

const charChannelType = "chartest"

// registerCharTestChannel registers the test channel constructor exactly once.
// The Factory looks up constructors by group.ChannelType; "chartest" routes to
// the httptest server bound to the group's name via charTestRoutes.
func registerCharTestChannel() {
	charChannelOnce.Do(func() {
		channel.Register(charChannelType, func(f *channel.Factory, g *models.Group) (channel.ChannelProxy, error) {
			return &charTestChannel{groupName: g.Name}, nil
		})
	})
}

func setRoute(groupName, baseURL string) {
	charTestRoutesMu.Lock()
	defer charTestRoutesMu.Unlock()
	charTestRoutes[groupName] = baseURL
}

func getRoute(groupName string) string {
	charTestRoutesMu.Lock()
	defer charTestRoutesMu.Unlock()
	return charTestRoutes[groupName]
}

// charTestChannel is a minimal channel.ChannelProxy that forwards each attempt
// to the httptest upstream registered for its group. It is intentionally dumb:
// real HTTP round-trips through net/http exercise the exact same client.Do path
// the production code takes.
type charTestChannel struct {
	groupName string
}

func (ch *charTestChannel) BuildUpstreamURL(originalURL *url.URL, groupName string) (string, error) {
	base := getRoute(ch.groupName)
	if base == "" {
		return "", fmt.Errorf("no upstream route registered for group %q", ch.groupName)
	}
	return base + "/v1/chat/completions", nil
}

func (ch *charTestChannel) IsConfigStale(group *models.Group) bool { return false }
func (ch *charTestChannel) GetHTTPClient() *http.Client            { return &http.Client{Timeout: 5 * time.Second} }
func (ch *charTestChannel) GetStreamClient() *http.Client          { return &http.Client{Timeout: 5 * time.Second} }
func (ch *charTestChannel) ModifyRequest(req *http.Request, apiKey *models.APIKey, group *models.Group) {
}
func (ch *charTestChannel) IsStreamRequest(c *gin.Context, bodyBytes []byte) bool { return false }
func (ch *charTestChannel) ExtractModel(c *gin.Context, bodyBytes []byte) string {
	// Parse the model from the JSON body like the real channels do, so pricing
	// resolves in the cost-capture E2E. Failover tests pass "test-model" and get
	// the same value back — no behavior change for them.
	var b struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(bodyBytes, &b) == nil && b.Model != "" {
		return b.Model
	}
	return "test-model"
}
func (ch *charTestChannel) ValidateKey(ctx context.Context, apiKey *models.APIKey, group *models.Group) channel.ValidateResult {
	return channel.ValidateResult{}
}
func (ch *charTestChannel) ApplyModelRedirect(req *http.Request, bodyBytes []byte, group *models.Group) ([]byte, error) {
	return bodyBytes, nil
}
func (ch *charTestChannel) TransformModelList(req *http.Request, bodyBytes []byte, group *models.Group) (map[string]any, error) {
	return nil, nil
}

// charTestDB spins up an in-memory sqlite DB with the schema the proxy path
// touches (groups, sub-group links, keys).
func charTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	logrus.SetLevel(logrus.PanicLevel)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Group{},
		&models.GroupSubGroup{},
		&models.APIKey{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// seedKeyIntoStore makes SelectKey succeed for the given group: it pushes the
// key ID onto the group's active_keys list and writes the key hash with an
// active status. Encryption is the noop service, so key_string is stored raw.
func seedKeyIntoStore(t *testing.T, st store.Store, groupID, keyID uint, keyValue string) {
	t.Helper()
	if err := st.LPush(fmt.Sprintf("group:%d:active_keys", groupID), strconv.FormatUint(uint64(keyID), 10)); err != nil {
		t.Fatalf("seed active_keys: %v", err)
	}
	if err := st.HSet(fmt.Sprintf("key:%d", keyID), map[string]any{
		"id":            strconv.FormatUint(uint64(keyID), 10),
		"key_string":    keyValue, // noop encryption -> stored as-is
		"status":        models.KeyStatusActive,
		"failure_count": "0",
		"created_at":    strconv.FormatInt(time.Now().Unix(), 10),
	}); err != nil {
		t.Fatalf("seed key hash: %v", err)
	}
}

// buildProxy wires a ProxyServer with real collaborators backed by db+store.
func buildProxy(t *testing.T, db *gorm.DB, st store.Store) *ProxyServer {
	t.Helper()
	registerCharTestChannel()

	encSvc, err := encryption.NewService("") // noop service: Encrypt/Decrypt are identity
	if err != nil {
		t.Fatalf("encryption: %v", err)
	}

	settingsManager := config.NewSystemSettingsManager() // uninitialized -> default settings

	subGroupManager := services.NewSubGroupManager(st)
	groupManager := services.NewGroupManager(db, st, settingsManager, subGroupManager)
	if err := groupManager.Initialize(); err != nil {
		t.Fatalf("group manager init: %v", err)
	}

	keyProvider := keypool.NewProvider(db, st, settingsManager, encSvc, nil)

	// channelFactory needs a real Factory so failover recursion can resolve the
	// next sub-group's channel. clientManager is nil because the chartest channel
	// supplies its own clients and never calls newBaseChannel.
	factory := channel.NewFactory(settingsManager, nil)

	ps, err := NewProxyServer(
		keyProvider,
		groupManager,
		subGroupManager,
		settingsManager,
		factory,
		nil, // requestLogService nil -> logRequest is a no-op
		nil, // aliasService unused on this path
		nil, // selector nil -> markRoutingCandidate is a no-op
		nil, // modelResolver unused (no P4 candidate seeded)
		encSvc,
	)
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	return ps
}

// newGinCtx builds a gin context with a recorder and a POST chat/completions
// request carrying the given body.
func newGinCtx(body string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/proxy/agg/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	return c, rec
}

// -----------------------------------------------------------------------------
// Test 1: aggregate group, sub-group A -> 500 (failover) -> sub-group B succeeds.
// -----------------------------------------------------------------------------
func TestFailover_AggregateSwitchesToHealthySubGroup(t *testing.T) {
	db := charTestDB(t)
	st := store.NewMemoryStore()

	// Upstream A always fails with 500 (a failover status under the default spec).
	var aHits int32
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&aHits, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
	}))
	defer srvA.Close()

	// Upstream B succeeds.
	var bHits int32
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&bHits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srvB.Close()

	// Seed groups: aggregate "agg" with sub-groups subA(weight 100) and subB(weight 1).
	// max_retries=0 so subA exhausts immediately and failover kicks in.
	aggCfg := mustJSONMap(t, `{"max_retries":0}`)
	subA := &models.Group{Name: "subA", GroupType: "standard", ChannelType: charChannelType, TestModel: "m", Upstreams: []byte(`[{"url":"http://a","weight":1}]`), Config: aggCfg}
	subB := &models.Group{Name: "subB", GroupType: "standard", ChannelType: charChannelType, TestModel: "m", Upstreams: []byte(`[{"url":"http://b","weight":1}]`), Config: aggCfg}
	agg := &models.Group{Name: "agg", GroupType: "aggregate", ChannelType: charChannelType, TestModel: "m", Upstreams: []byte(`[{"url":"http://agg","weight":1}]`), Config: aggCfg}
	for _, g := range []*models.Group{subA, subB, agg} {
		if err := db.Create(g).Error; err != nil {
			t.Fatalf("create group %s: %v", g.Name, err)
		}
	}
	// Link sub-groups to aggregate.
	for _, l := range []models.GroupSubGroup{
		{GroupID: agg.ID, SubGroupID: subA.ID, Weight: 100},
		{GroupID: agg.ID, SubGroupID: subB.ID, Weight: 1},
	} {
		if err := db.Create(&l).Error; err != nil {
			t.Fatalf("create link: %v", err)
		}
	}

	setRoute("subA", srvA.URL)
	setRoute("subB", srvB.URL)

	// Seed keys so SelectKey succeeds for both sub-groups.
	seedKeyIntoStore(t, st, subA.ID, 101, "keyA")
	seedKeyIntoStore(t, st, subB.ID, 102, "keyB")

	ps := buildProxy(t, db, st)

	// Re-read the post-initialize group objects from the manager so we use the
	// real EffectiveConfig / FailoverStatusCodeMatcher the production loader built.
	aggG, err := ps.groupManager.GetGroupByName("agg")
	if err != nil {
		t.Fatalf("get agg: %v", err)
	}
	subAG, err := ps.groupManager.GetGroupByName("subA")
	if err != nil {
		t.Fatalf("get subA: %v", err)
	}
	handler, err := ps.channelFactory.GetChannel(subAG)
	if err != nil {
		t.Fatalf("get channel: %v", err)
	}

	c, rec := newGinCtx(`{"model":"test-model"}`)
	attempted := map[string]bool{subAG.Name: true}
	// requestedModel="" exercises the plain SWRR failover path (no per-model
	// availableModels cache seeded); the failover decision logic is identical.
	ps.executeRequestWithRetry(c, handler, aggG, subAG, []byte(`{"model":"test-model"}`), false, time.Now(), 0, attempted, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 after failover to subB, got %d body=%s", rec.Code, rec.Body.String())
	}
	if atomic.LoadInt32(&bHits) == 0 {
		t.Fatalf("expected subB upstream to be called during failover, got 0 hits")
	}
	if atomic.LoadInt32(&aHits) == 0 {
		t.Fatalf("expected subA upstream to be called before failover, got 0 hits")
	}
	// Let the async UpdateStatus goroutines settle before the DB closes.
	time.Sleep(50 * time.Millisecond)
}

// -----------------------------------------------------------------------------
// Test 2: standard group, upstream keeps failing, retries exhausted -> upstream
// error status is returned to the client.
// -----------------------------------------------------------------------------
func TestFailover_StandardExhaustsRetriesReturnsUpstreamStatus(t *testing.T) {
	db := charTestDB(t)
	st := store.NewMemoryStore()

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"always fails"}}`))
	}))
	defer srv.Close()

	// max_retries=2 -> attempts at retryCount 0,1,2 then final.
	cfg := mustJSONMap(t, `{"max_retries":2}`)
	std := &models.Group{Name: "std", GroupType: "standard", ChannelType: charChannelType, TestModel: "m", Upstreams: []byte(`[{"url":"http://s","weight":1}]`), Config: cfg}
	if err := db.Create(std).Error; err != nil {
		t.Fatalf("create std: %v", err)
	}

	setRoute("std", srv.URL)
	seedKeyIntoStore(t, st, std.ID, 201, "keyS")

	ps := buildProxy(t, db, st)

	stdG, err := ps.groupManager.GetGroupByName("std")
	if err != nil {
		t.Fatalf("get std: %v", err)
	}
	handler, err := ps.channelFactory.GetChannel(stdG)
	if err != nil {
		t.Fatalf("get channel: %v", err)
	}

	c, rec := newGinCtx(`{"model":"test-model"}`)
	// standard group: originalGroup == group, attempted empty.
	ps.executeRequestWithRetry(c, handler, stdG, stdG, []byte(`{"model":"test-model"}`), false, time.Now(), 0, map[string]bool{}, "test-model")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected upstream 500 returned to client, got %d body=%s", rec.Code, rec.Body.String())
	}
	// 3 retries (0,1,2) all hit the upstream.
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("expected 3 upstream attempts (max_retries=2), got %d", got)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body not JSON: %v (%s)", err, rec.Body.String())
	}
	time.Sleep(50 * time.Millisecond)
}
