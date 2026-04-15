package integration_test

import (
	"fmt"
	"net/http"
	"testing"

	"propertyops/backend/internal/common"
)

// TestGovernance_KeywordsCRUD exercises create → list → delete → list-empty.
func TestGovernance_KeywordsCRUD(t *testing.T) {
	env := newPlainEnv(t)
	_, adminToken := createSystemAdminUser(t, env.db, env.router, "gkc_admin")

	// Create
	w := makeRequest(t, env.router, http.MethodPost, "/api/v1/governance/keywords", adminToken,
		map[string]interface{}{
			"keyword":  "scam",
			"category": "Fraud",
			"severity": "High",
		})
	assertStatus(t, w, http.StatusCreated)
	var createResp struct {
		Data struct {
			ID      uint64 `json:"id"`
			Keyword string `json:"keyword"`
		} `json:"data"`
	}
	parseResponse(t, w, &createResp)
	if createResp.Data.ID == 0 {
		t.Fatal("expected non-zero keyword ID")
	}
	if createResp.Data.Keyword != "scam" {
		t.Errorf("expected keyword=scam, got %q", createResp.Data.Keyword)
	}
	keywordID := createResp.Data.ID

	// List — should contain the new keyword.
	w = makeRequest(t, env.router, http.MethodGet, "/api/v1/governance/keywords", adminToken, nil)
	assertStatus(t, w, http.StatusOK)
	var listResp struct {
		Data []struct {
			ID      uint64 `json:"id"`
			Keyword string `json:"keyword"`
		} `json:"data"`
	}
	parseResponse(t, w, &listResp)
	if len(listResp.Data) != 1 {
		t.Errorf("expected 1 keyword after create, got %d", len(listResp.Data))
	}

	// Delete
	w = makeRequest(t, env.router, http.MethodDelete,
		fmt.Sprintf("/api/v1/governance/keywords/%d", keywordID), adminToken, nil)
	assertStatus(t, w, http.StatusNoContent)

	// List should now be empty.
	w = makeRequest(t, env.router, http.MethodGet, "/api/v1/governance/keywords", adminToken, nil)
	assertStatus(t, w, http.StatusOK)
	parseResponse(t, w, &listResp)
	if len(listResp.Data) != 0 {
		t.Errorf("expected 0 keywords after delete, got %d", len(listResp.Data))
	}
}

// TestGovernance_Keywords_NonAdminForbidden verifies all keyword endpoints are
// gated to SystemAdmin only.
func TestGovernance_Keywords_NonAdminForbidden(t *testing.T) {
	env := newPlainEnv(t)
	_, reviewerToken := createUserAndLogin(t, env.db, env.router, "gkn_rev", common.RoleComplianceReviewer)

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/governance/keywords"},
		{http.MethodGet, "/api/v1/governance/keywords"},
		{http.MethodDelete, "/api/v1/governance/keywords/1"},
	} {
		w := makeRequest(t, env.router, tc.method, tc.path, reviewerToken, map[string]string{"keyword": "x"})
		assertStatus(t, w, http.StatusForbidden)
	}
}

// TestGovernance_RiskRulesCRUD exercises create → list → get → update → delete.
func TestGovernance_RiskRulesCRUD(t *testing.T) {
	env := newPlainEnv(t)
	_, adminToken := createSystemAdminUser(t, env.db, env.router, "grc_admin")

	// Create
	body := map[string]interface{}{
		"name":             "Excessive complaints",
		"description":      "Flag tenants exceeding 5 complaints in 30 days",
		"condition_type":   "complaint_count",
		"condition_params": map[string]interface{}{"threshold": 5, "window_days": 30},
		"action_type":      "Warning",
		"action_params":    map[string]interface{}{"reason": "Threshold exceeded"},
	}
	w := makeRequest(t, env.router, http.MethodPost, "/api/v1/governance/risk-rules", adminToken, body)
	assertStatus(t, w, http.StatusCreated)

	var createResp struct {
		Data struct {
			ID   uint64 `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	parseResponse(t, w, &createResp)
	if createResp.Data.ID == 0 {
		t.Fatal("expected non-zero risk_rule ID")
	}
	ruleID := createResp.Data.ID

	// List
	w = makeRequest(t, env.router, http.MethodGet, "/api/v1/governance/risk-rules", adminToken, nil)
	assertStatus(t, w, http.StatusOK)

	// Get
	w = makeRequest(t, env.router, http.MethodGet,
		fmt.Sprintf("/api/v1/governance/risk-rules/%d", ruleID), adminToken, nil)
	assertStatus(t, w, http.StatusOK)
	var getResp struct {
		Data struct {
			ID   uint64 `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	parseResponse(t, w, &getResp)
	if getResp.Data.ID != ruleID {
		t.Errorf("expected ID=%d, got %d", ruleID, getResp.Data.ID)
	}

	// Update
	body["name"] = "Excessive complaints (updated)"
	w = makeRequest(t, env.router, http.MethodPut,
		fmt.Sprintf("/api/v1/governance/risk-rules/%d", ruleID), adminToken, body)
	assertStatus(t, w, http.StatusOK)
	parseResponse(t, w, &getResp)
	if getResp.Data.Name != "Excessive complaints (updated)" {
		t.Errorf("expected updated name, got %q", getResp.Data.Name)
	}

	// Delete
	w = makeRequest(t, env.router, http.MethodDelete,
		fmt.Sprintf("/api/v1/governance/risk-rules/%d", ruleID), adminToken, nil)
	assertStatus(t, w, http.StatusNoContent)

	// Get should now be 404.
	w = makeRequest(t, env.router, http.MethodGet,
		fmt.Sprintf("/api/v1/governance/risk-rules/%d", ruleID), adminToken, nil)
	assertStatus(t, w, http.StatusNotFound)
}

// TestGovernance_RiskRules_NonAdminForbidden verifies all risk-rule endpoints
// are gated to SystemAdmin only.
func TestGovernance_RiskRules_NonAdminForbidden(t *testing.T) {
	env := newPlainEnv(t)
	_, reviewerToken := createUserAndLogin(t, env.db, env.router, "grn_rev", common.RoleComplianceReviewer)

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/governance/risk-rules"},
		{http.MethodGet, "/api/v1/governance/risk-rules"},
		{http.MethodGet, "/api/v1/governance/risk-rules/1"},
		{http.MethodPut, "/api/v1/governance/risk-rules/1"},
		{http.MethodDelete, "/api/v1/governance/risk-rules/1"},
	} {
		w := makeRequest(t, env.router, tc.method, tc.path, reviewerToken, map[string]string{"name": "x"})
		assertStatus(t, w, http.StatusForbidden)
	}
}
