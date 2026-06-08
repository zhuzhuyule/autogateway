package router_engine

// 集成测试：Middleware sticky 命中跳过 Pick 行为契约
//
// 方案：真实 sqlite 内存 DB + store.NewMemoryStore() + 测试内 mock resolver。
// 不依赖 services.GroupManager 以避免引入过重的构造链。
//
// 覆盖三条契约：
//   1. 首轮（单 user 消息，无 assistant）：不写 sticky 锁。
//   2. 多轮命中：手动写锁后，再跑 Middleware 断言 context 中返回锁定 candidate。
//   3. 失效回退：锁定 candidate 指向无 active key 的 group，断言回退到正常选择或干净失败（不 panic）。

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"autogateway/internal/models"
	"autogateway/internal/store"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ----- mock resolver -----

type mockResolver struct {
	nameByID map[uint]string
	byName   map[string]*models.Group
}

func (m *mockResolver) GetGroupNameByID(id uint) (string, bool) {
	n, ok := m.nameByID[id]
	return n, ok
}

func (m *mockResolver) GetGroupByName(name string) (*models.Group, error) {
	g, ok := m.byName[name]
	if !ok {
		return nil, nil
	}
	return g, nil
}

// ----- sqlite helper -----

func newIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Group{},
		&models.ModelAlias{},
		&models.APIKey{},
		&models.SystemSetting{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

// seedGroupWithKey seeds a group + one active key + one model alias.
// Returns the created group ID.
func seedGroupWithKey(t *testing.T, db *gorm.DB, groupName, alias, realModel string) uint {
	t.Helper()
	g := models.Group{
		Name:             groupName,
		ChannelType:      "openai",
		TestModel:        realModel,
		ModelRoutingMode: "passthrough",
		Upstreams:        datatypes.JSON([]byte(`[]`)),
	}
	if err := db.Create(&g).Error; err != nil {
		t.Fatalf("seed group: %v", err)
	}
	key := models.APIKey{GroupID: g.ID, KeyValue: "sk-test", Status: models.KeyStatusActive}
	if err := db.Create(&key).Error; err != nil {
		t.Fatalf("seed key: %v", err)
	}
	ma := models.ModelAlias{
		Alias:    alias,
		GroupID:  g.ID,
		RealModel: realModel,
		Weight:   1,
		Priority: 100,
		Enabled:  true,
	}
	if err := db.Create(&ma).Error; err != nil {
		t.Fatalf("seed alias: %v", err)
	}
	return g.ID
}

// chatBody constructs a minimal chat/completions JSON body.
// If withAssistant=true it includes an assistant message (→ isMultiTurn).
func chatBody(model string, withAssistant bool) []byte {
	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	msgs := []msg{{Role: "user", Content: "hello"}}
	if withAssistant {
		msgs = append(msgs,
			msg{Role: "assistant", Content: "hi"},
			msg{Role: "user", Content: "again"},
		)
	}
	b, _ := json.Marshal(map[string]any{
		"model":    model,
		"messages": msgs,
	})
	return b
}

// runMiddleware fires the middleware with a given body and returns the gin
// context so the caller can inspect c.Get("router_engine.candidate").
func runMiddleware(t *testing.T, sel *Selector, res AliasResolver, body []byte) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "group_name", Value: "test-group"}}
	req := httptest.NewRequest(http.MethodPost, "/proxy/test-group/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req

	handler := Middleware(sel, res)
	handler(c)
	return c
}

// ----- 契约 1：首轮请求不写 sticky 锁 -----

func TestMiddlewareIntegration_FirstTurnNoSticky(t *testing.T) {
	db := newIntegrationDB(t)
	st := store.NewMemoryStore()
	groupID := seedGroupWithKey(t, db, "openai-prod", "gpt-4o", "gpt-4o")

	res := &mockResolver{
		nameByID: map[uint]string{groupID: "openai-prod"},
		byName:   map[string]*models.Group{"openai-prod": {ID: groupID, Name: "openai-prod"}},
	}
	sel := NewSelector(db, st, nil)

	body := chatBody("gpt-4o", false) // 首轮，无 assistant
	runMiddleware(t, sel, res, body)

	// 首轮不应写 sticky — store 应为空
	sessionKey, isMultiTurn := parseSession(body, http.Header{})
	if isMultiTurn {
		t.Fatal("single-user body should not be multi-turn")
	}
	sk := stickyStoreKey(sessionKey, "gpt-4o")
	got := sel.GetSticky(sk)
	if got != nil {
		t.Fatalf("首轮请求不应写 sticky，但 GetSticky = %+v", got)
	}
}

// ----- 契约 2：多轮命中时 Middleware 读取锁定 candidate 放入 context -----

func TestMiddlewareIntegration_MultiTurnHitReturnsStickyCandidate(t *testing.T) {
	db := newIntegrationDB(t)
	st := store.NewMemoryStore()
	groupID := seedGroupWithKey(t, db, "openai-prod", "gpt-4o", "gpt-4o")

	res := &mockResolver{
		nameByID: map[uint]string{groupID: "openai-prod"},
		byName:   map[string]*models.Group{"openai-prod": {ID: groupID, Name: "openai-prod"}},
	}
	sel := NewSelector(db, st, nil)

	// 预先手动写入 sticky（模拟上一轮已锁定）
	body := chatBody("gpt-4o", true) // 多轮
	sessionKey, isMultiTurn := parseSession(body, http.Header{})
	if !isMultiTurn {
		t.Fatal("body with assistant should be multi-turn")
	}
	sk := stickyStoreKey(sessionKey, "gpt-4o")
	lockedCand := &Candidate{GroupID: groupID, RealModel: "gpt-4o", Alias: "gpt-4o", Weight: 1}
	sel.SetSticky(sk, lockedCand)

	// 跑 Middleware
	c := runMiddleware(t, sel, res, body)

	// 断言 context 中的 candidate 与锁定一致
	v, exists := c.Get("router_engine.candidate")
	if !exists {
		t.Fatal("context 中应有 router_engine.candidate")
	}
	picked, ok := v.(*Candidate)
	if !ok || picked == nil {
		t.Fatalf("router_engine.candidate 类型断言失败：%T %v", v, v)
	}
	if picked.GroupID != groupID || picked.RealModel != "gpt-4o" {
		t.Fatalf("命中粘性：期望 GroupID=%d RealModel=gpt-4o，得到 %+v", groupID, picked)
	}
}

// ----- 契约 3：锁定 candidate 指向失活 group，回退不 panic -----

func TestMiddlewareIntegration_StickyDeadCandidateFallback(t *testing.T) {
	db := newIntegrationDB(t)
	st := store.NewMemoryStore()

	// group1：有 active key + alias（正常备选）
	groupID1 := seedGroupWithKey(t, db, "openai-prod", "gpt-4o", "gpt-4o")
	// group2：无 active key（死亡 group）
	deadGroup := models.Group{Name: "dead-group", ChannelType: "openai", TestModel: "gpt-4o", ModelRoutingMode: "passthrough", Upstreams: datatypes.JSON([]byte(`[]`))}
	if err := db.Create(&deadGroup).Error; err != nil {
		t.Fatalf("seed dead group: %v", err)
	}
	// dead group 的 alias（enabled，但无 active key）
	deadAlias := models.ModelAlias{
		Alias:    "gpt-4o",
		GroupID:  deadGroup.ID,
		RealModel: "gpt-4o",
		Weight:   1,
		Priority: 200,
		Enabled:  true,
	}
	if err := db.Create(&deadAlias).Error; err != nil {
		t.Fatalf("seed dead alias: %v", err)
	}

	res := &mockResolver{
		nameByID: map[uint]string{
			groupID1:     "openai-prod",
			deadGroup.ID: "dead-group",
		},
		byName: map[string]*models.Group{
			"openai-prod": {ID: groupID1, Name: "openai-prod"},
			"dead-group":  &deadGroup,
		},
	}
	sel := NewSelector(db, st, nil)

	// 锁定指向 dead group 的 candidate
	body := chatBody("gpt-4o", true)
	sessionKey, _ := parseSession(body, http.Header{})
	sk := stickyStoreKey(sessionKey, "gpt-4o")
	deadCand := &Candidate{GroupID: deadGroup.ID, RealModel: "gpt-4o", Alias: "gpt-4o", Weight: 1}
	sel.SetSticky(sk, deadCand)

	// 跑 Middleware — 不应 panic；应回退到正常 Pick（选出 groupID1）或干净失败
	c := runMiddleware(t, sel, res, body)

	// 验证：要么回退成功选出存活 group，要么干净地不设置 candidate（不 panic 即通过）
	if v, exists := c.Get("router_engine.candidate"); exists {
		picked, ok := v.(*Candidate)
		if !ok || picked == nil {
			t.Fatalf("candidate 非预期类型：%T %v", v, v)
		}
		// 若回退成功，选出的应是存活的 groupID1
		if picked.GroupID == deadGroup.ID {
			t.Fatalf("失活 candidate 应被淘汰，但 Middleware 仍返回了 dead group: %+v", picked)
		}
		if picked.GroupID != groupID1 {
			t.Fatalf("期望回退到 groupID1=%d，得到 GroupID=%d", groupID1, picked.GroupID)
		}
	}
	// 若 context 无 candidate，说明 Pick 失败（可能是 alias pool 只剩无 key 候选），
	// 这也是合法的干净失败路径，只要不 panic 就通过。

	// 最后确认锁定的 dead candidate 确实不被 IsCandidateAlive 认为存活
	if sel.IsCandidateAlive(context.Background(), deadCand) {
		t.Fatal("dead group（无 active key）的 candidate 不应被认为存活")
	}
}
