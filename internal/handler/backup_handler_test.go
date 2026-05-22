package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"autogateway/internal/backup"
	"autogateway/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func newTestServerWithBackup(t *testing.T) (*gin.Engine, *gorm.DB, *BackupHandler) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	db.AutoMigrate(&models.SystemSetting{}, &models.Group{}, &models.GroupSubGroup{}, &models.APIKey{}, &models.ModelAlias{})
	enc := stubEnc{}
	svc := backup.NewService(db, enc, "test")
	h := NewBackupHandler(svc)
	r := gin.New()
	r.POST("/api/admin/backup/export", h.Export)
	r.POST("/api/admin/backup/preview", h.Preview)
	r.POST("/api/admin/backup/import", h.Import)
	return r, db, h
}

type stubEnc struct{}

func (stubEnc) Encrypt(s string) (string, error) { return "enc:" + s, nil }
func (stubEnc) Decrypt(s string) (string, error) {
	if !strings.HasPrefix(s, "enc:") {
		return "", io.EOF
	}
	return s[4:], nil
}
func (stubEnc) Hash(s string) string { return "hash:" + s }

func TestBackupHandler_ExportThenImport(t *testing.T) {
	r, srcDB, _ := newTestServerWithBackup(t)
	g := models.Group{Name: "x", ChannelType: "openai", TestModel: "m", Upstreams: datatypes.JSON("[]")}
	srcDB.Create(&g)

	// 1. export
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/admin/backup/export", strings.NewReader(`{"password":"p"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("export status %d body=%s", w.Code, w.Body.String())
	}
	blob := w.Body.Bytes()
	if !bytes.HasPrefix(blob, []byte("ACB1")) {
		t.Fatal("not an .acb")
	}

	// 2. preview
	pw, tok := uploadBackup(t, r, "/api/admin/backup/preview", blob, "p", "")
	if pw.Code != http.StatusOK {
		t.Fatalf("preview status %d body=%s", pw.Code, pw.Body.String())
	}
	if tok == "" {
		t.Fatal("missing confirm_token")
	}

	// 3. import (merge)
	iw, _ := uploadBackup(t, r, "/api/admin/backup/import", blob, "p", tok+"|merge")
	if iw.Code != http.StatusOK {
		t.Fatalf("import status %d body=%s", iw.Code, iw.Body.String())
	}
}

func TestBackupHandler_BadPassword(t *testing.T) {
	r, _, _ := newTestServerWithBackup(t)

	// export with one password
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/admin/backup/export", strings.NewReader(`{"password":"right"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	blob := w.Body.Bytes()

	pw, _ := uploadBackup(t, r, "/api/admin/backup/preview", blob, "wrong", "")
	if pw.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", pw.Code)
	}
}

func uploadBackup(t *testing.T, r *gin.Engine, path string, blob []byte, password, extra string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "backup.acb")
	fw.Write(blob)
	mw.WriteField("password", password)
	if extra != "" {
		parts := strings.SplitN(extra, "|", 2)
		mw.WriteField("confirm_token", parts[0])
		if len(parts) > 1 {
			mw.WriteField("strategy", parts[1])
		}
	}
	mw.Close()

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	r.ServeHTTP(w, req)

	if w.Code == http.StatusOK && strings.Contains(w.Header().Get("Content-Type"), "application/json") {
		var p struct {
			ConfirmToken string `json:"confirm_token"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &p); err == nil {
			return w, p.ConfirmToken
		}
	}
	return w, ""
}

func TestBackupHandler_StalePreview(t *testing.T) {
	r, srcDB, _ := newTestServerWithBackup(t)
	srcDB.Create(&models.Group{Name: "x", ChannelType: "openai", TestModel: "m", Upstreams: datatypes.JSON("[]")})

	// export
	ew := httptest.NewRecorder()
	er := httptest.NewRequest("POST", "/api/admin/backup/export", strings.NewReader(`{"password":"p"}`))
	er.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(ew, er)
	blob := ew.Body.Bytes()

	// preview
	pw, tok := uploadBackup(t, r, "/api/admin/backup/preview", blob, "p", "")
	if pw.Code != http.StatusOK || tok == "" {
		t.Fatal("preview setup failed")
	}

	// Tamper the blob (flip byte 30 to corrupt ciphertext) — fingerprint mismatch
	if len(blob) < 50 {
		t.Fatal("blob too small to tamper")
	}
	tampered := append([]byte(nil), blob...)
	tampered[30] ^= 0xFF

	iw, _ := uploadBackup(t, r, "/api/admin/backup/import", tampered, "p", tok+"|merge")
	if iw.Code != http.StatusConflict {
		t.Fatalf("expected 409 STALE_PREVIEW (blob fingerprint mismatch), got %d body=%s", iw.Code, iw.Body.String())
	}
}

func TestBackupHandler_BadStrategy(t *testing.T) {
	r, _, _ := newTestServerWithBackup(t)
	// import with an invalid strategy string — bypass preview
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "x.acb")
	fw.Write([]byte("ACB1"))
	mw.WriteField("password", "p")
	mw.WriteField("strategy", "nope")
	mw.WriteField("confirm_token", "anything")
	mw.Close()

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/admin/backup/import", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 BAD_STRATEGY, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "BAD_STRATEGY") {
		t.Errorf("expected BAD_STRATEGY in body, got %s", w.Body.String())
	}
}

func TestHostSlug(t *testing.T) {
	h := &BackupHandler{}
	if got := h.hostSlug(); got != "unknown" {
		t.Errorf("nil settingsManager: got %q want unknown", got)
	}
	// Without a real settingsManager we can't test the URL paths directly without
	// wiring a config.SystemSettingsManager. The internal regex behavior is
	// covered by hostSafe applied to the parsed hostname in production paths.
}
