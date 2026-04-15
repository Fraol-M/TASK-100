package integration_test

import (
	"fmt"
	"net/http"
	"testing"

	"propertyops/backend/internal/common"
)

// createWOAsTenant is a test-local helper that creates a New work order via the
// JSON POST endpoint and returns its ID. Used by dispatch + transition tests
// that need a starting state without exercising the create flow themselves.
func createWOAsTenant(t *testing.T, env *plainEnv, tenantToken string, propertyID uint64) uint64 {
	t.Helper()

	body := map[string]interface{}{
		"property_id": propertyID,
		"description": "The dishwasher is leaking and needs urgent attention from a plumber.",
		"priority":    common.PriorityNormal,
	}
	w := makeRequest(t, env.router, http.MethodPost, "/api/v1/work-orders", tenantToken, body)
	assertStatus(t, w, http.StatusCreated)

	var resp struct {
		Data struct {
			ID uint64 `json:"id"`
		} `json:"data"`
	}
	parseResponse(t, w, &resp)
	if resp.Data.ID == 0 {
		t.Fatal("createWOAsTenant: got work order ID = 0")
	}
	return resp.Data.ID
}

// TestWorkOrder_DispatchByPM verifies a PropertyManager can dispatch a New work
// order to a technician on a property they manage. The response should reflect
// the new assigned_to and Assigned status.
func TestWorkOrder_DispatchByPM(t *testing.T) {
	env := newPlainEnv(t)

	_, tenantToken := createUserAndLogin(t, env.db, env.router, "wod_tenant", common.RoleTenant)
	pmUser, pmToken := createUserAndLogin(t, env.db, env.router, "wod_pm", common.RolePropertyManager)
	techUser, _ := createUserAndLogin(t, env.db, env.router, "wod_tech", common.RoleTechnician)

	const propertyID uint64 = 1
	assignManagerToPropertyDB(t, env, pmUser.ID, propertyID)

	woID := createWOAsTenant(t, env, tenantToken, propertyID)

	body := map[string]interface{}{
		"technician_id": techUser.ID,
		"reason":        "Initial dispatch to on-call plumber",
	}
	w := makeRequest(t, env.router, http.MethodPost,
		fmt.Sprintf("/api/v1/work-orders/%d/dispatch", woID), pmToken, body)
	assertStatus(t, w, http.StatusOK)

	var resp struct {
		Data struct {
			ID         uint64  `json:"id"`
			AssignedTo *uint64 `json:"assigned_to"`
			Status     string  `json:"status"`
		} `json:"data"`
	}
	parseResponse(t, w, &resp)
	if resp.Data.AssignedTo == nil || *resp.Data.AssignedTo != techUser.ID {
		t.Errorf("assigned_to: expected %d, got %v", techUser.ID, resp.Data.AssignedTo)
	}
	if resp.Data.Status != common.WOStatusAssigned {
		t.Errorf("status: expected %q, got %q", common.WOStatusAssigned, resp.Data.Status)
	}
}

// TestWorkOrder_DispatchByPMUnmanagedForbidden verifies a PropertyManager cannot
// dispatch a work order on a property they do NOT manage.
func TestWorkOrder_DispatchByPMUnmanagedForbidden(t *testing.T) {
	env := newPlainEnv(t)

	_, tenantToken := createUserAndLogin(t, env.db, env.router, "wod_tenant2", common.RoleTenant)
	_, pmToken := createUserAndLogin(t, env.db, env.router, "wod_pm2", common.RolePropertyManager)
	techUser, _ := createUserAndLogin(t, env.db, env.router, "wod_tech2", common.RoleTechnician)
	// Intentionally do not assign the PM to property 1.

	woID := createWOAsTenant(t, env, tenantToken, 1)

	body := map[string]interface{}{
		"technician_id": techUser.ID,
		"reason":        "attempted unauthorized dispatch",
	}
	w := makeRequest(t, env.router, http.MethodPost,
		fmt.Sprintf("/api/v1/work-orders/%d/dispatch", woID), pmToken, body)
	assertStatus(t, w, http.StatusForbidden)
}

// TestWorkOrder_DispatchByTenantForbidden verifies the dispatch handler rejects
// non-PM/non-Admin actors with 403.
func TestWorkOrder_DispatchByTenantForbidden(t *testing.T) {
	env := newPlainEnv(t)

	_, tenantToken := createUserAndLogin(t, env.db, env.router, "wod_tenant3", common.RoleTenant)
	techUser, _ := createUserAndLogin(t, env.db, env.router, "wod_tech3", common.RoleTechnician)

	woID := createWOAsTenant(t, env, tenantToken, 1)

	body := map[string]interface{}{
		"technician_id": techUser.ID,
		"reason":        "tenant attempting dispatch",
	}
	w := makeRequest(t, env.router, http.MethodPost,
		fmt.Sprintf("/api/v1/work-orders/%d/dispatch", woID), tenantToken, body)
	assertStatus(t, w, http.StatusForbidden)
}

