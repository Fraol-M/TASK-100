package integration_test

import (
	"net/http"
	"testing"

	"propertyops/backend/internal/common"
)

// TestAdmin_PIIExport_RequiresPurpose verifies a missing/short purpose is rejected.
func TestAdmin_PIIExport_RequiresPurpose(t *testing.T) {
	env := newAdminEnv(t)
	_, adminToken := createSystemAdminUser(t, env.db, env.router, "ppr_admin")

	// Missing purpose entirely → 400 from binding (purpose is `required`).
	w := makeRequest(t, env.router, http.MethodPost, "/api/v1/admin/pii-export", adminToken,
		map[string]interface{}{"type": "users"})
	assertStatus(t, w, http.StatusBadRequest)

	// Short purpose → 422 from explicit length validation.
	w = makeRequest(t, env.router, http.MethodPost, "/api/v1/admin/pii-export", adminToken,
		map[string]interface{}{"type": "users", "purpose": "short"})
	assertStatus(t, w, http.StatusUnprocessableEntity)
}

// TestAdmin_PIIExport_Success verifies a valid PII export request succeeds and
// returns a file path + filename in the response body.
func TestAdmin_PIIExport_Success(t *testing.T) {
	env := newAdminEnv(t)
	_, adminToken := createSystemAdminUser(t, env.db, env.router, "pps_admin")

	body := map[string]interface{}{
		"type":    "users",
		"purpose": "Quarterly compliance review for finance audit",
	}
	w := makeRequest(t, env.router, http.MethodPost, "/api/v1/admin/pii-export", adminToken, body)
	assertStatus(t, w, http.StatusCreated)

	var resp struct {
		Data struct {
			FilePath string `json:"file_path"`
			Filename string `json:"filename"`
			Type     string `json:"type"`
		} `json:"data"`
	}
	parseResponse(t, w, &resp)
	if resp.Data.FilePath == "" {
		t.Error("expected non-empty file_path in response")
	}
	if resp.Data.Type != "users" {
		t.Errorf("expected type=users, got %q", resp.Data.Type)
	}
}

// TestAdmin_PIIExport_UnsupportedType verifies a 400 for an unknown export type.
func TestAdmin_PIIExport_UnsupportedType(t *testing.T) {
	env := newAdminEnv(t)
	_, adminToken := createSystemAdminUser(t, env.db, env.router, "ppt_admin")

	body := map[string]interface{}{
		"type":    "secrets",
		"purpose": "some legitimate purpose explanation",
	}
	w := makeRequest(t, env.router, http.MethodPost, "/api/v1/admin/pii-export", adminToken, body)
	assertStatus(t, w, http.StatusBadRequest)
}

// TestAdmin_PIIExport_NonAdminForbidden verifies the route is gated to SystemAdmin.
func TestAdmin_PIIExport_NonAdminForbidden(t *testing.T) {
	env := newAdminEnv(t)
	_, pmToken := createUserAndLogin(t, env.db, env.router, "ppn_pm", common.RolePropertyManager)

	body := map[string]interface{}{
		"type":    "users",
		"purpose": "anything sufficiently long enough",
	}
	w := makeRequest(t, env.router, http.MethodPost, "/api/v1/admin/pii-export", pmToken, body)
	assertStatus(t, w, http.StatusForbidden)
}

// TestAdmin_DataRetention_Enforce verifies the destructive retention-enforcement
// endpoint runs and returns the deleted-row counts.
func TestAdmin_DataRetention_Enforce(t *testing.T) {
	env := newAdminEnv(t)
	_, adminToken := createSystemAdminUser(t, env.db, env.router, "dr_admin")

	w := makeRequest(t, env.router, http.MethodDelete, "/api/v1/admin/data-retention", adminToken, nil)
	assertStatus(t, w, http.StatusOK)

	var resp struct {
		Data struct {
			MessagesDeleted int64  `json:"messages_deleted"`
			PaymentsDeleted int64  `json:"payments_deleted"`
			MessageCutoff   string `json:"message_cutoff"`
			FinancialCutoff string `json:"financial_cutoff"`
		} `json:"data"`
	}
	parseResponse(t, w, &resp)
	if resp.Data.MessageCutoff == "" {
		t.Errorf("expected message_cutoff to be set, body: %s", w.Body.String())
	}
	if resp.Data.FinancialCutoff == "" {
		t.Errorf("expected financial_cutoff to be set, body: %s", w.Body.String())
	}
}

// TestAdmin_DataRetention_NonAdminForbidden verifies the route is admin-only.
func TestAdmin_DataRetention_NonAdminForbidden(t *testing.T) {
	env := newAdminEnv(t)
	_, pmToken := createUserAndLogin(t, env.db, env.router, "drn_pm", common.RolePropertyManager)

	w := makeRequest(t, env.router, http.MethodDelete, "/api/v1/admin/data-retention", pmToken, nil)
	assertStatus(t, w, http.StatusForbidden)
}
