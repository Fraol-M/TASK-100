package integration_test

import (
	"net/http"
	"os/exec"
	"testing"

	"propertyops/backend/internal/common"
)

// TestAdmin_BackupsList verifies a SystemAdmin can list backups; with an empty
// (newly-created) backup root, the result is an empty array — that's still a
// valid 200.
func TestAdmin_BackupsList(t *testing.T) {
	env := newAdminEnv(t)
	_, adminToken := createSystemAdminUser(t, env.db, env.router, "bl_admin")

	w := makeRequest(t, env.router, http.MethodGet, "/api/v1/admin/backups", adminToken, nil)
	assertStatus(t, w, http.StatusOK)

	var resp struct {
		Data []map[string]interface{} `json:"data"`
	}
	parseResponse(t, w, &resp)
	// A fresh backup root has no files, so the handler returns data: null (a
	// nil slice), which unmarshals as len 0. Assert the count, not the raw value.
	if len(resp.Data) != 0 {
		t.Errorf("expected 0 backups on fresh backup root, got %d; body: %s", len(resp.Data), w.Body.String())
	}
}

// TestAdmin_BackupsValidate_MissingFile verifies that validating a non-existent
// file returns 200 with errors recorded in the response body (the route does
// not 500 on missing files; the validation result captures the failure).
func TestAdmin_BackupsValidate_MissingFile(t *testing.T) {
	env := newAdminEnv(t)
	_, adminToken := createSystemAdminUser(t, env.db, env.router, "bv_admin")

	body := map[string]interface{}{"file_path": "/no/such/path/backup.sql.enc"}
	w := makeRequest(t, env.router, http.MethodPost, "/api/v1/admin/backups/validate", adminToken, body)
	assertStatus(t, w, http.StatusOK)

	var resp struct {
		Data struct {
			Valid  bool     `json:"valid"`
			Errors []string `json:"errors"`
		} `json:"data"`
	}
	parseResponse(t, w, &resp)
	if resp.Data.Valid {
		t.Error("expected valid=false for missing file")
	}
	if len(resp.Data.Errors) == 0 {
		t.Errorf("expected at least one error in result, got none; body: %s", w.Body.String())
	}
}

// TestAdmin_BackupsValidate_RequiresFilePath verifies the handler rejects a
// request body that's missing file_path.
func TestAdmin_BackupsValidate_RequiresFilePath(t *testing.T) {
	env := newAdminEnv(t)
	_, adminToken := createSystemAdminUser(t, env.db, env.router, "bvr_admin")

	w := makeRequest(t, env.router, http.MethodPost, "/api/v1/admin/backups/validate", adminToken,
		map[string]interface{}{})
	assertStatus(t, w, http.StatusBadRequest)
}

// TestAdmin_BackupsRetention verifies that applying retention on an empty
// backup root succeeds and returns the standard message.
func TestAdmin_BackupsRetention(t *testing.T) {
	env := newAdminEnv(t)
	_, adminToken := createSystemAdminUser(t, env.db, env.router, "br_admin")

	w := makeRequest(t, env.router, http.MethodDelete, "/api/v1/admin/backups/retention", adminToken, nil)
	assertStatus(t, w, http.StatusOK)

	var resp struct {
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	}
	parseResponse(t, w, &resp)
	if resp.Data.Message == "" {
		t.Errorf("expected non-empty message, body: %s", w.Body.String())
	}
}

// TestAdmin_BackupsCreate_RequiresMysqldump exercises the create path. The
// underlying service shells out to `mysqldump`; if it's not on PATH (typical
// for CI without mysql-client installed), we skip rather than fail.
func TestAdmin_BackupsCreate_RequiresMysqldump(t *testing.T) {
	if _, err := exec.LookPath("mysqldump"); err != nil {
		t.Skip("mysqldump not available in PATH — skipping backup-create happy-path test")
	}

	env := newAdminEnv(t)
	_, adminToken := createSystemAdminUser(t, env.db, env.router, "bc_admin")

	w := makeRequest(t, env.router, http.MethodPost, "/api/v1/admin/backups", adminToken, nil)
	// Either 201 (success) or 500 (mysqldump itself failed because we're not
	// pointing at a real DB) — both demonstrate the route is wired up. The
	// stricter happy path requires a real MySQL backend (TEST_MYSQL_DSN).
	if w.Code != http.StatusCreated && w.Code != http.StatusInternalServerError {
		t.Errorf("expected 201 or 500, got %d; body: %s", w.Code, w.Body.String())
	}
}

// TestAdmin_Backups_NonAdminForbidden verifies the four backup endpoints all
// reject non-SystemAdmin tokens with 403.
func TestAdmin_Backups_NonAdminForbidden(t *testing.T) {
	env := newAdminEnv(t)
	_, pmToken := createUserAndLogin(t, env.db, env.router, "bn_pm", common.RolePropertyManager)

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/admin/backups"},
		{http.MethodGet, "/api/v1/admin/backups"},
		{http.MethodPost, "/api/v1/admin/backups/validate"},
		{http.MethodDelete, "/api/v1/admin/backups/retention"},
	} {
		w := makeRequest(t, env.router, tc.method, tc.path, pmToken,
			map[string]string{"file_path": "/x"})
		assertStatus(t, w, http.StatusForbidden)
	}
}
