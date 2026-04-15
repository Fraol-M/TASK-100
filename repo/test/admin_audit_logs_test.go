package integration_test

import (
	"fmt"
	"net/http"
	"testing"

	"propertyops/backend/internal/audit"
	"propertyops/backend/internal/common"
)

// seedAuditLog inserts a single audit log row directly so the list/get handlers
// have something to return.
func seedAuditLog(t *testing.T, env *plainEnv, action string, actorID uint64) uint64 {
	t.Helper()
	resID := uint64(1)
	a := audit.AuditLog{
		UUID:         newUUID(),
		Action:       action,
		ResourceType: "TestResource",
		ResourceID:   &resID,
		Description:  "seeded log entry for integration test",
		ActorID:      &actorID,
		IPAddress:    "127.0.0.1",
		RequestID:    "test-req-1",
	}
	if err := env.db.Create(&a).Error; err != nil {
		t.Fatalf("seedAuditLog: %v", err)
	}
	return a.ID
}

// TestAdmin_AuditLogs_List verifies a SystemAdmin can list audit logs and
// pagination meta is returned.
func TestAdmin_AuditLogs_List(t *testing.T) {
	env := newAdminEnv(t)
	adminID, adminToken := createSystemAdminUser(t, env.db, env.router, "al_admin")
	seedAuditLog(t, env, common.AuditActionUpdate, adminID)

	w := makeRequest(t, env.router, http.MethodGet, "/api/v1/admin/audit-logs", adminToken, nil)
	assertStatus(t, w, http.StatusOK)

	var resp struct {
		Data []map[string]interface{} `json:"data"`
		Meta map[string]interface{}   `json:"meta"`
	}
	parseResponse(t, w, &resp)
	if len(resp.Data) == 0 {
		t.Errorf("expected at least 1 audit log entry, got 0; body: %s", w.Body.String())
	}
	if resp.Meta == nil {
		t.Errorf("expected pagination meta, got nil; body: %s", w.Body.String())
	}
}

// TestAdmin_AuditLogs_Get verifies a SystemAdmin can fetch a single audit log
// entry by ID.
func TestAdmin_AuditLogs_Get(t *testing.T) {
	env := newAdminEnv(t)
	adminID, adminToken := createSystemAdminUser(t, env.db, env.router, "ag_admin")
	logID := seedAuditLog(t, env, common.AuditActionDelete, adminID)

	w := makeRequest(t, env.router, http.MethodGet,
		fmt.Sprintf("/api/v1/admin/audit-logs/%d", logID), adminToken, nil)
	assertStatus(t, w, http.StatusOK)

	var resp struct {
		Data struct {
			ID     uint64 `json:"id"`
			Action string `json:"action"`
		} `json:"data"`
	}
	parseResponse(t, w, &resp)
	if resp.Data.ID != logID {
		t.Errorf("expected id=%d, got %d", logID, resp.Data.ID)
	}
	if resp.Data.Action != common.AuditActionDelete {
		t.Errorf("expected action=%q, got %q", common.AuditActionDelete, resp.Data.Action)
	}
}

// TestAdmin_AuditLogs_GetMissing verifies a 404 for a non-existent ID.
func TestAdmin_AuditLogs_GetMissing(t *testing.T) {
	env := newAdminEnv(t)
	_, adminToken := createSystemAdminUser(t, env.db, env.router, "agm_admin")

	w := makeRequest(t, env.router, http.MethodGet,
		"/api/v1/admin/audit-logs/99999", adminToken, nil)
	assertStatus(t, w, http.StatusNotFound)
}

// TestAdmin_LogsList verifies a SystemAdmin can call GET /admin/logs.
// With no log-file backend seeded, the list is allowed to be empty — we just
// assert the route/handler are wired correctly and respond with a success body.
func TestAdmin_LogsList(t *testing.T) {
	env := newAdminEnv(t)
	_, adminToken := createSystemAdminUser(t, env.db, env.router, "ll_admin")

	w := makeRequest(t, env.router, http.MethodGet, "/api/v1/admin/logs", adminToken, nil)
	assertStatus(t, w, http.StatusOK)

	var resp struct {
		Data []map[string]interface{} `json:"data"`
		Meta map[string]interface{}   `json:"meta"`
	}
	parseResponse(t, w, &resp)
	// data may be nil/empty — that's OK; pagination meta should still be present.
	if resp.Meta == nil {
		t.Errorf("expected pagination meta, got nil; body: %s", w.Body.String())
	}
}

// TestAdmin_AuditLogs_NonAdminForbidden verifies all admin log routes are
// gated to SystemAdmin only.
func TestAdmin_AuditLogs_NonAdminForbidden(t *testing.T) {
	env := newAdminEnv(t)
	_, pmToken := createUserAndLogin(t, env.db, env.router, "aln_pm", common.RolePropertyManager)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/admin/audit-logs"},
		{http.MethodGet, "/api/v1/admin/audit-logs/1"},
		{http.MethodGet, "/api/v1/admin/logs"},
	} {
		w := makeRequest(t, env.router, tc.method, tc.path, pmToken, nil)
		assertStatus(t, w, http.StatusForbidden)
	}
}
