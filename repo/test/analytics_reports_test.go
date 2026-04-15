package integration_test

import (
	"fmt"
	"net/http"
	"testing"

	"propertyops/backend/internal/common"
)

// createSavedReport calls POST /analytics/reports/saved as the given PM token
// and returns the new saved-report ID. Centralized so individual tests don't
// repeat the boilerplate.
func createSavedReport(t *testing.T, env *plainEnv, token, name, reportType string) uint64 {
	t.Helper()
	w := makeRequest(t, env.router, http.MethodPost, "/api/v1/analytics/reports/saved", token,
		map[string]interface{}{
			"name":          name,
			"report_type":   reportType,
			"output_format": "CSV",
		})
	assertStatus(t, w, http.StatusCreated)

	var resp struct {
		Data struct{ ID uint64 `json:"id"` } `json:"data"`
	}
	parseResponse(t, w, &resp)
	if resp.Data.ID == 0 {
		t.Fatal("createSavedReport: got ID = 0")
	}
	return resp.Data.ID
}

// TestAnalytics_GenerateReport verifies a PM can generate (materialize) a
// saved report and gets back metadata referencing the on-disk file.
func TestAnalytics_GenerateReport(t *testing.T) {
	env := newPlainEnv(t)
	// Storage root must be a real directory the service can write CSVs to.
	env.cfg.Storage.Root = t.TempDir()

	_, pmToken := createUserAndLogin(t, env.db, env.router, "agr_pm", common.RolePropertyManager)

	savedID := createSavedReport(t, env, pmToken, "PM Monthly", "work_orders")

	w := makeRequest(t, env.router, http.MethodPost,
		fmt.Sprintf("/api/v1/analytics/reports/generate/%d", savedID), pmToken, nil)
	assertStatus(t, w, http.StatusCreated)

	var resp struct {
		Data struct {
			ID            uint64 `json:"id"`
			SavedReportID uint64 `json:"saved_report_id"`
			Status        string `json:"status"`
		} `json:"data"`
	}
	parseResponse(t, w, &resp)
	if resp.Data.ID == 0 {
		t.Error("expected non-zero generated_report ID")
	}
	if resp.Data.SavedReportID != savedID {
		t.Errorf("saved_report_id: expected %d, got %d", savedID, resp.Data.SavedReportID)
	}
}

// TestAnalytics_GetGeneratedReport verifies the owner can retrieve the
// generated-report metadata after generation.
func TestAnalytics_GetGeneratedReport(t *testing.T) {
	env := newPlainEnv(t)
	env.cfg.Storage.Root = t.TempDir()

	_, pmToken := createUserAndLogin(t, env.db, env.router, "agg_pm", common.RolePropertyManager)

	savedID := createSavedReport(t, env, pmToken, "PM Quarterly", "work_orders")

	// Generate first.
	w := makeRequest(t, env.router, http.MethodPost,
		fmt.Sprintf("/api/v1/analytics/reports/generate/%d", savedID), pmToken, nil)
	assertStatus(t, w, http.StatusCreated)
	var genResp struct {
		Data struct{ ID uint64 `json:"id"` } `json:"data"`
	}
	parseResponse(t, w, &genResp)
	genID := genResp.Data.ID

	// Now retrieve.
	w = makeRequest(t, env.router, http.MethodGet,
		fmt.Sprintf("/api/v1/analytics/reports/generated/%d", genID), pmToken, nil)
	assertStatus(t, w, http.StatusOK)

	var getResp struct {
		Data struct {
			ID            uint64 `json:"id"`
			SavedReportID uint64 `json:"saved_report_id"`
		} `json:"data"`
	}
	parseResponse(t, w, &getResp)
	if getResp.Data.ID != genID {
		t.Errorf("expected id=%d, got %d", genID, getResp.Data.ID)
	}
}

// TestAnalytics_GetGeneratedReport_OtherUserForbidden verifies a different
// (non-admin) user cannot read another user's generated report.
func TestAnalytics_GetGeneratedReport_OtherUserForbidden(t *testing.T) {
	env := newPlainEnv(t)
	env.cfg.Storage.Root = t.TempDir()

	_, pm1Token := createUserAndLogin(t, env.db, env.router, "aggo_pm1", common.RolePropertyManager)
	_, pm2Token := createUserAndLogin(t, env.db, env.router, "aggo_pm2", common.RolePropertyManager)

	savedID := createSavedReport(t, env, pm1Token, "PM1 Report", "work_orders")
	w := makeRequest(t, env.router, http.MethodPost,
		fmt.Sprintf("/api/v1/analytics/reports/generate/%d", savedID), pm1Token, nil)
	assertStatus(t, w, http.StatusCreated)
	var genResp struct {
		Data struct{ ID uint64 `json:"id"` } `json:"data"`
	}
	parseResponse(t, w, &genResp)

	// PM2 attempts to fetch PM1's generated report.
	w = makeRequest(t, env.router, http.MethodGet,
		fmt.Sprintf("/api/v1/analytics/reports/generated/%d", genResp.Data.ID), pm2Token, nil)
	assertStatus(t, w, http.StatusForbidden)
}

// TestAnalytics_Export verifies POST /analytics/export produces a generated
// report record (response is 201 + GeneratedReportResponse JSON, not a raw file).
func TestAnalytics_Export(t *testing.T) {
	env := newPlainEnv(t)
	env.cfg.Storage.Root = t.TempDir()

	_, pmToken := createUserAndLogin(t, env.db, env.router, "aex_pm", common.RolePropertyManager)

	body := map[string]interface{}{
		"type":   "work_orders",
		"format": "CSV",
	}
	w := makeRequest(t, env.router, http.MethodPost, "/api/v1/analytics/export", pmToken, body)
	assertStatus(t, w, http.StatusCreated)

	var resp struct {
		Data struct {
			ID          uint64 `json:"id"`
			Status      string `json:"status"`
			StoragePath string `json:"storage_path"`
		} `json:"data"`
	}
	parseResponse(t, w, &resp)
	if resp.Data.ID == 0 {
		t.Error("expected non-zero export report ID")
	}
}

// TestAnalytics_Export_AuditLogsRequiresAdmin verifies the audit_logs export
// type is gated to SystemAdmin only.
func TestAnalytics_Export_AuditLogsRequiresAdmin(t *testing.T) {
	env := newPlainEnv(t)
	env.cfg.Storage.Root = t.TempDir()

	_, pmToken := createUserAndLogin(t, env.db, env.router, "aexa_pm", common.RolePropertyManager)

	body := map[string]interface{}{
		"type":    "audit_logs",
		"purpose": "Compliance Q4 review for finance audit team",
	}
	w := makeRequest(t, env.router, http.MethodPost, "/api/v1/analytics/export", pmToken, body)
	assertStatus(t, w, http.StatusForbidden)
}

// TestAnalytics_Export_AsTenantForbidden verifies the route-level role guard
// blocks tenants from the export endpoint.
func TestAnalytics_Export_AsTenantForbidden(t *testing.T) {
	env := newPlainEnv(t)
	_, tenantToken := createUserAndLogin(t, env.db, env.router, "aex_tenant", common.RoleTenant)

	body := map[string]interface{}{"type": "work_orders"}
	w := makeRequest(t, env.router, http.MethodPost, "/api/v1/analytics/export", tenantToken, body)
	assertStatus(t, w, http.StatusForbidden)
}

// TestAnalytics_GenerateReport_AsTenantForbidden verifies the route-level role
// guard blocks tenants from the generate endpoint.
func TestAnalytics_GenerateReport_AsTenantForbidden(t *testing.T) {
	env := newPlainEnv(t)
	_, tenantToken := createUserAndLogin(t, env.db, env.router, "agrt_tenant", common.RoleTenant)

	w := makeRequest(t, env.router, http.MethodPost, "/api/v1/analytics/reports/generate/1", tenantToken, nil)
	assertStatus(t, w, http.StatusForbidden)
}
