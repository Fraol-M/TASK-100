package integration_test

import (
	"net/http"
	"testing"

	"propertyops/backend/internal/common"
)

// newAdminEnv builds a plainEnv but with cfg.Encryption.KeyDir routed to
// t.TempDir() so the key-rotation handler can safely write new key files.
func newAdminEnv(t *testing.T) *plainEnv {
	t.Helper()
	db := setupTestDB(t)
	seedRoles(t, db)
	cfg := testConfig()
	cfg.Encryption.KeyDir = t.TempDir()
	cfg.Storage.Root = t.TempDir()
	cfg.Storage.BackupRoot = t.TempDir()
	router := newTestRouter(db, cfg)
	return &plainEnv{db: db, cfg: cfg, router: router}
}

// TestAdmin_ListSettings verifies a SystemAdmin can fetch the (initially empty)
// system_settings table and the response body parses as a JSON array.
func TestAdmin_ListSettings(t *testing.T) {
	env := newAdminEnv(t)
	_, adminToken := createSystemAdminUser(t, env.db, env.router, "as_admin")

	w := makeRequest(t, env.router, http.MethodGet, "/api/v1/admin/settings", adminToken, nil)
	assertStatus(t, w, http.StatusOK)

	var resp struct {
		Data []map[string]interface{} `json:"data"`
	}
	parseResponse(t, w, &resp)
	if resp.Data == nil {
		t.Errorf("expected data to be a (possibly empty) array, got nil; body: %s", w.Body.String())
	}
}

// TestAdmin_UpdateSetting verifies a SystemAdmin can update an existing system
// setting after seeding one row directly into the table.
func TestAdmin_UpdateSetting(t *testing.T) {
	env := newAdminEnv(t)
	_, adminToken := createSystemAdminUser(t, env.db, env.router, "us_admin")

	// Seed a setting row so the handler has something to update.
	if err := env.db.Exec(
		`INSERT INTO system_settings (setting_key, setting_value, created_at, updated_at)
		 VALUES (?, ?, datetime('now'), datetime('now'))`,
		"feature.enable_x", "false",
	).Error; err != nil {
		t.Fatalf("seed system_setting: %v", err)
	}

	body := map[string]interface{}{"value": "true"}
	w := makeRequest(t, env.router, http.MethodPut,
		"/api/v1/admin/settings/feature.enable_x", adminToken, body)
	assertStatus(t, w, http.StatusOK)

	var resp struct {
		Data struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"data"`
	}
	parseResponse(t, w, &resp)
	if resp.Data.Value != "true" {
		t.Errorf("expected value=true, got %q (body: %s)", resp.Data.Value, w.Body.String())
	}
}

// TestAdmin_UpdateSetting_UnknownKeyNotFound verifies the handler 404s when the
// key does not exist (instead of silently creating one).
func TestAdmin_UpdateSetting_UnknownKeyNotFound(t *testing.T) {
	env := newAdminEnv(t)
	_, adminToken := createSystemAdminUser(t, env.db, env.router, "usu_admin")

	w := makeRequest(t, env.router, http.MethodPut,
		"/api/v1/admin/settings/no.such.key", adminToken, map[string]string{"value": "x"})
	assertStatus(t, w, http.StatusNotFound)
}

// TestAdmin_ListKeys verifies a SystemAdmin can fetch the encryption-key
// version metadata. With an empty key directory, loaded_versions is [].
func TestAdmin_ListKeys(t *testing.T) {
	env := newAdminEnv(t)
	_, adminToken := createSystemAdminUser(t, env.db, env.router, "lk_admin")

	w := makeRequest(t, env.router, http.MethodGet, "/api/v1/admin/keys", adminToken, nil)
	assertStatus(t, w, http.StatusOK)

	var resp struct {
		Data struct {
			ActiveKeyID    int   `json:"active_key_id"`
			RotationDue    bool  `json:"rotation_due"`
			LoadedVersions []int `json:"loaded_versions"`
		} `json:"data"`
	}
	parseResponse(t, w, &resp)
	// active_key_id defaults to 1 from testConfig.
	if resp.Data.ActiveKeyID < 1 {
		t.Errorf("expected active_key_id >= 1, got %d", resp.Data.ActiveKeyID)
	}
}

// TestAdmin_RotateKey verifies a SystemAdmin can rotate the encryption key.
// The handler writes a new key file under cfg.Encryption.KeyDir.
func TestAdmin_RotateKey(t *testing.T) {
	env := newAdminEnv(t)
	_, adminToken := createSystemAdminUser(t, env.db, env.router, "rk_admin")

	w := makeRequest(t, env.router, http.MethodPost, "/api/v1/admin/keys/rotate", adminToken, nil)
	assertStatus(t, w, http.StatusCreated)

	var resp struct {
		Data struct {
			NewKeyVersion int    `json:"new_key_version"`
			Message       string `json:"message"`
		} `json:"data"`
	}
	parseResponse(t, w, &resp)
	if resp.Data.NewKeyVersion <= 0 {
		t.Errorf("expected new_key_version > 0, got %d", resp.Data.NewKeyVersion)
	}
}

// TestAdmin_NonAdminForbidden asserts the four endpoints reject non-admin tokens.
func TestAdmin_NonAdminForbidden(t *testing.T) {
	env := newAdminEnv(t)
	_, pmToken := createUserAndLogin(t, env.db, env.router, "an_pm", common.RolePropertyManager)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/admin/settings"},
		{http.MethodPut, "/api/v1/admin/settings/x"},
		{http.MethodGet, "/api/v1/admin/keys"},
		{http.MethodPost, "/api/v1/admin/keys/rotate"},
	} {
		w := makeRequest(t, env.router, tc.method, tc.path, pmToken, map[string]string{"value": "x"})
		assertStatus(t, w, http.StatusForbidden)
	}
}
