package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"propertyops/backend/internal/app"
	authpkg "propertyops/backend/internal/auth"
	"propertyops/backend/internal/common"
	"propertyops/backend/internal/config"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// testConfig returns a minimal Config suitable for integration tests.
// It does not require any real DB, encryption keys, or storage paths.
func testConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{Port: 0, GinMode: "test"},
		DB:     config.DBConfig{},
		Auth: config.AuthConfig{
			BcryptCost:         4, // minimum cost for fast tests
			SessionIdleTimeout: 30 * time.Minute,
			SessionMaxLifetime: 168 * time.Hour,
		},
		Encryption: config.EncryptionConfig{
			KeyDir:      ".",
			ActiveKeyID: 1,
		},
		Storage: config.StorageConfig{
			Root:       ".",
			BackupRoot: ".",
			LogRoot:    ".",
		},
		Backup: config.BackupConfig{
			ScheduleCron:      "0 2 * * *",
			RetentionDays:     30,
			EncryptionEnabled: false,
		},
		RateLimit: config.RateLimitConfig{
			MaxSubmissionsPerHour: 1000, // generous limit so tests don't hit it
		},
		Payment: config.PaymentConfig{
			IntentExpiryMinutes:   30,
			DualApprovalThreshold: 500.00,
		},
		Anomaly: config.AnomalyConfig{
			AllowedCIDRs: []string{"127.0.0.0/8", "0.0.0.0/0"},
		},
	}
}

// newTestRouter builds a full Gin engine backed by the provided SQLite DB.
// All routes and middleware are registered exactly as in production via app.RegisterRoutes.
func newTestRouter(db *gorm.DB, cfg *config.Config) *gin.Engine {
	engine := gin.New()
	app.RegisterRoutes(engine, db, cfg)
	return engine
}

// makeRequest performs an HTTP request against the provided router and returns the recorder.
// token is the raw bearer token; pass "" to omit the Authorization header.
// body may be nil for requests without a body.
func makeRequest(t *testing.T, router *gin.Engine, method, path, token string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()

	var reqBody *bytes.Buffer
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("makeRequest: marshal body: %v", err)
		}
		reqBody = bytes.NewBuffer(data)
	} else {
		reqBody = bytes.NewBuffer(nil)
	}

	req, err := http.NewRequest(method, path, reqBody)
	if err != nil {
		t.Fatalf("makeRequest: new request: %v", err)
	}
	// Populate RemoteAddr so Gin's c.ClientIP() returns a valid IP. Required by
	// CIDR-gated middleware (e.g. AnomalyAllowlist) which rejects when IP is empty.
	req.RemoteAddr = testRemoteAddr

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// testRemoteAddr is the RemoteAddr set on every test request. 127.0.0.1 is
// inside the test AnomalyAllowlist CIDRs (127.0.0.0/8), so CIDR-gated endpoints
// see a valid loopback client.
const testRemoteAddr = "127.0.0.1:12345"

// makeRawRequest performs an HTTP request with a pre-encoded JSON string body.
func makeRawRequest(t *testing.T, router *gin.Engine, method, path, token, rawBody string) *httptest.ResponseRecorder {
	t.Helper()

	var bodyReader *strings.Reader
	if rawBody != "" {
		bodyReader = strings.NewReader(rawBody)
	} else {
		bodyReader = strings.NewReader("")
	}

	req, err := http.NewRequest(method, path, bodyReader)
	if err != nil {
		t.Fatalf("makeRawRequest: %v", err)
	}
	req.RemoteAddr = testRemoteAddr
	if rawBody != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// loginUser logs in a user via the API and returns the raw bearer token.
// Fails the test if the login does not return 200 or does not include a token.
func loginUser(t *testing.T, router *gin.Engine, username, password string) string {
	t.Helper()

	w := makeRequest(t, router, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"username": username,
		"password": password,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("loginUser: expected 200, got %d; body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("loginUser: parse response: %v", err)
	}
	if resp.Data.Token == "" {
		t.Fatalf("loginUser: got empty token; body: %s", w.Body.String())
	}
	return resp.Data.Token
}

// parseResponse unmarshals the JSON response body into target.
// It fails the test if unmarshalling fails.
func parseResponse(t *testing.T, w *httptest.ResponseRecorder, target interface{}) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), target); err != nil {
		t.Fatalf("parseResponse: %v; body: %s", err, w.Body.String())
	}
}

// assertStatus fails the test if the recorder's status code does not match expected.
func assertStatus(t *testing.T, w *httptest.ResponseRecorder, expected int) {
	t.Helper()
	if w.Code != expected {
		t.Errorf("expected HTTP %d, got %d; body: %s", expected, w.Code, w.Body.String())
	}
}

// plainEnv bundles DB + cfg + router for tests that don't need anything fancier
// than the standard test setup. Returned by newPlainEnv.
type plainEnv struct {
	db     *gorm.DB
	cfg    *config.Config
	router *gin.Engine
}

// newPlainEnv builds a fresh test DB, seeds the standard roles, builds a router,
// and returns the bundle. Use this in tests that need the router + db + cfg
// without any extra setup.
func newPlainEnv(t *testing.T) *plainEnv {
	t.Helper()
	db := setupTestDB(t)
	seedRoles(t, db)
	cfg := testConfig()
	router := newTestRouter(db, cfg)
	return &plainEnv{db: db, cfg: cfg, router: router}
}

// createUserAndLogin is a thin convenience wrapper: create a user with the given
// role, then log in and return the bearer token alongside the auth.User struct.
func createUserAndLogin(t *testing.T, db *gorm.DB, router *gin.Engine, username, role string) (*authpkg.User, string) {
	t.Helper()
	user, pw := createTestUser(t, db, username, role)
	token := loginUser(t, router, username, pw)
	return user, token
}

// createSystemAdminUser creates a SystemAdmin user, logs in, and returns
// the user ID + bearer token. Most admin-suite tests need exactly this combo.
func createSystemAdminUser(t *testing.T, db *gorm.DB, router *gin.Engine, username string) (uint64, string) {
	t.Helper()
	user, token := createUserAndLogin(t, db, router, username, common.RoleSystemAdmin)
	return user.ID, token
}

// multipartFile describes one file part for postMultipart.
type multipartFile struct {
	FieldName string // form field name (e.g. "file" or "attachments[]")
	Filename  string // original filename (e.g. "photo.jpg")
	MimeType  string // Content-Type header for the part (e.g. "image/jpeg")
	Content   []byte // raw bytes
}

// postMultipart issues a multipart/form-data request with the given text fields
// and file parts. method is typically POST or PUT. token may be empty to omit auth.
func postMultipart(t *testing.T, router *gin.Engine, method, path, token string,
	textFields map[string]string, files []multipartFile) *httptest.ResponseRecorder {
	t.Helper()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	for k, v := range textFields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatalf("postMultipart: WriteField(%q): %v", k, err)
		}
	}

	for _, f := range files {
		hdr := make(textproto.MIMEHeader)
		hdr.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, f.FieldName, f.Filename))
		if f.MimeType != "" {
			hdr.Set("Content-Type", f.MimeType)
		}
		part, err := mw.CreatePart(hdr)
		if err != nil {
			t.Fatalf("postMultipart: CreatePart: %v", err)
		}
		if _, err := part.Write(f.Content); err != nil {
			t.Fatalf("postMultipart: Write: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("postMultipart: Close: %v", err)
	}

	req, err := http.NewRequest(method, path, &buf)
	if err != nil {
		t.Fatalf("postMultipart: NewRequest: %v", err)
	}
	req.RemoteAddr = testRemoteAddr
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}
