package integration_test

import (
	"net/http"
	"testing"

	"propertyops/backend/internal/common"
)

// assignManagerToPropertyDB is a local helper that inserts a PropertyStaffAssignment
// row directly so the PropertyManager scope check (IsPMForProperty) returns true.
// It mirrors the helper used in workorder_lifecycle_test.go but is package-local
// so this file does not depend on the woEnv struct.
func assignManagerToPropertyDB(t *testing.T, env *plainEnv, managerID, propertyID uint64) {
	t.Helper()
	if err := env.db.Exec(
		"INSERT INTO property_staff_assignments (property_id, user_id, role, is_active, created_at) VALUES (?, ?, 'PropertyManager', true, datetime('now'))",
		propertyID, managerID,
	).Error; err != nil {
		t.Fatalf("assignManagerToPropertyDB: %v", err)
	}
}

// TestTenants_ListByProperty_AsAdmin verifies a SystemAdmin can list tenants
// for any property without a property-scope check, returning 200 + a paginated body.
func TestTenants_ListByProperty_AsAdmin(t *testing.T) {
	env := newPlainEnv(t)

	_, adminToken := createSystemAdminUser(t, env.db, env.router, "tlbp_admin")

	w := makeRequest(t, env.router, http.MethodGet, "/api/v1/tenants/by-property/1", adminToken, nil)
	assertStatus(t, w, http.StatusOK)

	var resp struct {
		Data []map[string]interface{} `json:"data"`
		Meta map[string]interface{}   `json:"meta"`
	}
	parseResponse(t, w, &resp)
	if resp.Meta == nil {
		t.Errorf("expected pagination meta in response, got %s", w.Body.String())
	}
}

// TestTenants_ListByProperty_AsPMManagedProperty verifies that a PropertyManager
// who manages the property gets 200.
func TestTenants_ListByProperty_AsPMManagedProperty(t *testing.T) {
	env := newPlainEnv(t)

	pmUser, pmToken := createUserAndLogin(t, env.db, env.router, "tlbp_pm", common.RolePropertyManager)
	assignManagerToPropertyDB(t, env, pmUser.ID, 1)

	w := makeRequest(t, env.router, http.MethodGet, "/api/v1/tenants/by-property/1", pmToken, nil)
	assertStatus(t, w, http.StatusOK)
}

// TestTenants_ListByProperty_AsPMUnmanagedProperty verifies that a PropertyManager
// who does NOT manage the property gets 403 from the handler-level scope check.
func TestTenants_ListByProperty_AsPMUnmanagedProperty(t *testing.T) {
	env := newPlainEnv(t)

	_, pmToken := createUserAndLogin(t, env.db, env.router, "tlbp_pm2", common.RolePropertyManager)
	// Do not assign the PM to property 99.

	w := makeRequest(t, env.router, http.MethodGet, "/api/v1/tenants/by-property/99", pmToken, nil)
	assertStatus(t, w, http.StatusForbidden)
}

// TestTenants_ListByProperty_AsTenantForbidden verifies the route-level role guard
// rejects Tenant callers with 403 before reaching the handler.
func TestTenants_ListByProperty_AsTenantForbidden(t *testing.T) {
	env := newPlainEnv(t)

	_, tenantToken := createUserAndLogin(t, env.db, env.router, "tlbp_tenant", common.RoleTenant)

	w := makeRequest(t, env.router, http.MethodGet, "/api/v1/tenants/by-property/1", tenantToken, nil)
	assertStatus(t, w, http.StatusForbidden)
}

// TestTenants_ListByProperty_Unauthenticated verifies 401 when no token is supplied.
func TestTenants_ListByProperty_Unauthenticated(t *testing.T) {
	env := newPlainEnv(t)
	w := makeRequest(t, env.router, http.MethodGet, "/api/v1/tenants/by-property/1", "", nil)
	assertStatus(t, w, http.StatusUnauthorized)
}
