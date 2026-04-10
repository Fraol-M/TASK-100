package integration_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	authpkg "propertyops/backend/internal/auth"
	"propertyops/backend/internal/common"
	"propertyops/backend/internal/config"
)

// matrixEnv holds a single DB + router shared by all sub-tests in the matrix.
type matrixEnv struct {
	db     *gorm.DB
	cfg    *config.Config
	router *gin.Engine
	tokens map[string]string // role → bearer token
	userIDs map[string]uint64
}

// setupMatrixEnv creates one DB + router and seeds one user per role.
func setupMatrixEnv(t *testing.T) *matrixEnv {
	t.Helper()
	db := setupTestDB(t)
	seedRoles(t, db)
	cfg := testConfig()
	router := newTestRouter(db, cfg)

	env := &matrixEnv{
		db:     db,
		cfg:    cfg,
		router: router,
		tokens: make(map[string]string),
		userIDs: make(map[string]uint64),
	}

	roles := []string{
		common.RoleTenant,
		common.RoleTechnician,
		common.RolePropertyManager,
		common.RoleComplianceReviewer,
		common.RoleSystemAdmin,
	}

	for _, role := range roles {
		username := "matrix_" + role
		u, pw := createTestUser(t, db, username, role)
		env.tokens[role] = loginUser(t, router, username, pw)
		env.userIDs[role] = u.ID
	}

	// Seed minimal property graph required by matrix setup calls:
	// - Tenant work-order create enforces tenant profile -> unit -> property matching.
	// - PM payment intent create enforces active PropertyManager assignment.
	if err := db.Exec(`INSERT INTO properties (id, uuid, name, address_line1, city, state, zip_code, timezone, is_active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NOW(), NOW())`,
		1, newUUID(), "Matrix Property", "1 Matrix Way", "Austin", "TX", "78701", "America/New_York", true).Error; err != nil {
		t.Fatalf("setupMatrixEnv: insert property: %v", err)
	}

	if err := db.Exec(`INSERT INTO units (id, uuid, property_id, unit_number, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, NOW(), NOW())`,
		1, newUUID(), 1, "101", "Occupied").Error; err != nil {
		t.Fatalf("setupMatrixEnv: insert unit: %v", err)
	}

	if err := db.Exec(`INSERT INTO tenant_profiles (uuid, user_id, unit_id, created_at, updated_at)
		VALUES (?, ?, ?, NOW(), NOW())`,
		newUUID(), env.userIDs[common.RoleTenant], 1).Error; err != nil {
		t.Fatalf("setupMatrixEnv: insert tenant profile: %v", err)
	}

	if err := db.Exec(`INSERT INTO property_staff_assignments (property_id, user_id, role, is_active)
		VALUES (?, ?, ?, ?)`,
		1, env.userIDs[common.RolePropertyManager], common.RolePropertyManager, true).Error; err != nil {
		t.Fatalf("setupMatrixEnv: insert PM assignment: %v", err)
	}

	return env
}

// matrixCase describes a single authorization matrix test row.
type matrixCase struct {
	method        string
	path          string
	body          interface{}
	allowedRoles  []string // roles that should get 2xx
	expectedWrong int      // expected status for wrong-role requests (403 or 401)
}

// TestAuthorizationMatrix is the comprehensive role-based access test.
// It iterates every entry in the matrix and verifies:
//   - Unauthenticated → 401
//   - Wrong role     → 403 (or expectedWrong if specified differently)
//   - Correct role   → not 401/403
func TestAuthorizationMatrix(t *testing.T) {
	env := setupMatrixEnv(t)

	// Pre-create some resources so GET/:id and similar endpoints have valid IDs.
	woID := matrixSetupWorkOrder(t, env)
	paymentID := matrixSetupPayment(t, env)
	reportID := matrixSetupReport(t, env)
	enfID := matrixSetupEnforcement(t, env)

	cases := []matrixCase{
		// --- Admin-only endpoints ---
		{
			method:        http.MethodGet,
			path:          "/api/v1/admin/audit-logs",
			body:          nil,
			allowedRoles:  []string{common.RoleSystemAdmin},
			expectedWrong: http.StatusForbidden,
		},
		{
			method:        http.MethodPost,
			path:          "/api/v1/users",
			body:          map[string]interface{}{"username": "newuser_matrix", "email": "newuser_matrix@test.com", "password": "Password123!"},
			allowedRoles:  []string{common.RoleSystemAdmin},
			expectedWrong: http.StatusForbidden,
		},
		{
			method:        http.MethodGet,
			path:          "/api/v1/users",
			body:          nil,
			allowedRoles:  []string{common.RoleSystemAdmin},
			expectedWrong: http.StatusForbidden,
		},

		// --- Work order endpoints (only Tenant can create work orders) ---
		{
			method: http.MethodPost,
			path:   "/api/v1/work-orders",
			body: map[string]interface{}{
				"property_id": 1,
				"description": "Matrix test work order description that is sufficiently long.",
				"priority":    common.PriorityNormal,
			},
			allowedRoles:  []string{common.RoleTenant},
			expectedWrong: http.StatusForbidden,
		},
		{
			// Only SystemAdmin can view any work order; Tenant sees own, PM sees managed property.
			// For the matrix test the WO is created by the Tenant role; SystemAdmin always has access.
			method:        http.MethodGet,
			path:          fmt.Sprintf("/api/v1/work-orders/%d", woID),
			body:          nil,
			allowedRoles:  []string{common.RoleTenant, common.RolePropertyManager, common.RoleSystemAdmin},
			expectedWrong: http.StatusForbidden,
		},

		// --- Payment endpoints ---
		{
			method: http.MethodPost,
			path:   "/api/v1/payments/intents",
			body: map[string]interface{}{
				"property_id": 1,
				"amount":      100.0,
			},
			allowedRoles:  []string{common.RolePropertyManager, common.RoleSystemAdmin},
			expectedWrong: http.StatusForbidden,
		},
		{
			method:        http.MethodGet,
			path:          "/api/v1/payments",
			body:          nil,
			allowedRoles:  []string{common.RolePropertyManager, common.RoleSystemAdmin},
			expectedWrong: http.StatusForbidden,
		},
		{
			method:        http.MethodGet,
			path:          fmt.Sprintf("/api/v1/payments/%d", paymentID),
			body:          nil,
			allowedRoles:  []string{common.RolePropertyManager, common.RoleSystemAdmin},
			expectedWrong: http.StatusForbidden,
		},

		// --- Governance report endpoints ---
		{
			method: http.MethodPost,
			path:   "/api/v1/governance/reports",
			body: map[string]interface{}{
				"target_type": common.ReportTargetTenant,
				"target_id":   1,
				"category":    "Harassment",
				"description": "Matrix test report description that is sufficiently detailed for validation.",
			},
			// Any authenticated user can file a report.
			allowedRoles:  []string{common.RoleTenant, common.RoleTechnician, common.RolePropertyManager, common.RoleComplianceReviewer, common.RoleSystemAdmin},
			expectedWrong: http.StatusForbidden,
		},
		{
			// Reading individual reports is restricted to ComplianceReviewer and SystemAdmin.
			method:        http.MethodGet,
			path:          fmt.Sprintf("/api/v1/governance/reports/%d", reportID),
			body:          nil,
			allowedRoles:  []string{common.RoleComplianceReviewer, common.RoleSystemAdmin},
			expectedWrong: http.StatusForbidden,
		},

		// --- Governance enforcement endpoints (ComplianceReviewer / SystemAdmin) ---
		{
			method:        http.MethodGet,
			path:          "/api/v1/governance/enforcements",
			body:          nil,
			allowedRoles:  []string{common.RoleComplianceReviewer, common.RoleSystemAdmin},
			expectedWrong: http.StatusForbidden,
		},

		// --- Enforcement revoke ---
		{
			method: http.MethodPost,
			path:   fmt.Sprintf("/api/v1/governance/enforcements/%d/revoke", enfID),
			body: map[string]string{
				"reason": "Matrix test revocation reason that is long enough for validation requirements.",
			},
			allowedRoles:  []string{common.RoleComplianceReviewer, common.RoleSystemAdmin},
			expectedWrong: http.StatusForbidden,
		},
	}

	allRoles := []string{
		common.RoleTenant,
		common.RoleTechnician,
		common.RolePropertyManager,
		common.RoleComplianceReviewer,
		common.RoleSystemAdmin,
	}

	for _, tc := range cases {
		tc := tc // capture range variable
		t.Run(fmt.Sprintf("%s %s", tc.method, tc.path), func(t *testing.T) {
			// Unauthenticated → always 401.
			w := makeRequest(t, env.router, tc.method, tc.path, "", tc.body)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("unauthenticated: expected 401, got %d; body: %s", w.Code, w.Body.String())
			}

			// Build set of allowed roles for O(1) lookup.
			allowed := make(map[string]bool, len(tc.allowedRoles))
			for _, r := range tc.allowedRoles {
				allowed[r] = true
			}

			for _, role := range allRoles {
				role := role
				token := env.tokens[role]
				w := makeRequest(t, env.router, tc.method, tc.path, token, tc.body)

				if allowed[role] {
					// Correct role: must receive a 2xx response.
					if w.Code < 200 || w.Code >= 300 {
						// Revoke is stateful in this matrix loop: the first allowed role
						// revokes successfully, and subsequent allowed roles may receive 409.
						if strings.HasPrefix(tc.path, "/api/v1/governance/enforcements/") &&
							strings.HasSuffix(tc.path, "/revoke") &&
							w.Code == http.StatusConflict {
							continue
						}
						t.Errorf("role=%s: expected 2xx (allowed), got %d; body: %s", role, w.Code, w.Body.String())
					}
				} else {
					// Wrong role: should be 403 (or expectedWrong).
					expectedCode := tc.expectedWrong
					if expectedCode == 0 {
						expectedCode = http.StatusForbidden
					}
					if w.Code != expectedCode && w.Code != http.StatusForbidden {
						t.Errorf("role=%s: expected %d (denied), got %d; body: %s", role, expectedCode, w.Code, w.Body.String())
					}
				}
			}
		})
	}
}

// TestUnauthenticated_AllProtectedEndpoints verifies that every protected endpoint
// returns 401 when called without a bearer token.
func TestUnauthenticated_AllProtectedEndpoints(t *testing.T) {
	db := setupTestDB(t)
	seedRoles(t, db)
	cfg := testConfig()
	router := newTestRouter(db, cfg)

	endpoints := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/auth/me"},
		{http.MethodPost, "/api/v1/auth/logout"},
		{http.MethodGet, "/api/v1/work-orders"},
		{http.MethodPost, "/api/v1/work-orders"},
		{http.MethodGet, "/api/v1/payments"},
		{http.MethodPost, "/api/v1/payments/intents"},
		{http.MethodGet, "/api/v1/governance/reports"},
		{http.MethodPost, "/api/v1/governance/reports"},
		{http.MethodGet, "/api/v1/governance/enforcements"},
		{http.MethodGet, "/api/v1/users"},
		{http.MethodGet, "/api/v1/admin/audit-logs"},
	}

	for _, ep := range endpoints {
		ep := ep
		t.Run(fmt.Sprintf("%s %s", ep.method, ep.path), func(t *testing.T) {
			w := makeRequest(t, router, ep.method, ep.path, "", nil)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("expected 401, got %d; body: %s", w.Code, w.Body.String())
			}
		})
	}
}

// TestRoleEscalation_TenantCannotCreateUsers verifies that a Tenant cannot create users.
func TestRoleEscalation_TenantCannotCreateUsers(t *testing.T) {
	db := setupTestDB(t)
	seedRoles(t, db)
	cfg := testConfig()
	router := newTestRouter(db, cfg)

	_, pw := createTestUser(t, db, "esc_tenant", common.RoleTenant)
	token := loginUser(t, router, "esc_tenant", pw)

	w := makeRequest(t, router, http.MethodPost, "/api/v1/users", token, map[string]interface{}{
		"username": "hacked_user",
		"email":    "hacked@test.com",
		"password": "Password123!",
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("tenant should not be able to create users, got %d; body: %s", w.Code, w.Body.String())
	}
}

// TestRoleEscalation_TechnicianCannotApprovePayments verifies that a Technician cannot
// access payment management endpoints.
func TestRoleEscalation_TechnicianCannotApprovePayments(t *testing.T) {
	db := setupTestDB(t)
	seedRoles(t, db)
	cfg := testConfig()
	router := newTestRouter(db, cfg)

	_, pw := createTestUser(t, db, "esc_tech", common.RoleTechnician)
	token := loginUser(t, router, "esc_tech", pw)

	// Technicians should not be able to create payment intents.
	w := makeRequest(t, router, http.MethodPost, "/api/v1/payments/intents", token, map[string]interface{}{
		"property_id": 1,
		"amount":      500.0,
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("technician should not be able to create payment intents, got %d; body: %s", w.Code, w.Body.String())
	}
}

// TestRoleEscalation_TenantCannotApplyEnforcement verifies that a Tenant cannot
// apply enforcement actions.
func TestRoleEscalation_TenantCannotApplyEnforcement(t *testing.T) {
	db := setupTestDB(t)
	seedRoles(t, db)
	cfg := testConfig()
	router := newTestRouter(db, cfg)

	targetUser, _ := createTestUser(t, db, "enf_target", common.RoleTenant)
	_, pw := createTestUser(t, db, "enf_attacker", common.RoleTenant)
	token := loginUser(t, router, "enf_attacker", pw)

	w := makeRequest(t, router, http.MethodPost, "/api/v1/governance/enforcements", token, map[string]interface{}{
		"user_id":     targetUser.ID,
		"action_type": common.EnforcementSuspension,
		"reason":      "Unauthorized enforcement attempt from tenant account",
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("tenant should not be able to apply enforcement, got %d; body: %s", w.Code, w.Body.String())
	}
}

// TestRoleEscalation_ComplianceReviewerCannotAccessAuditLogs verifies that a
// ComplianceReviewer cannot access the admin audit log endpoint.
func TestRoleEscalation_ComplianceReviewerCannotAccessAuditLogs(t *testing.T) {
	db := setupTestDB(t)
	seedRoles(t, db)
	cfg := testConfig()
	router := newTestRouter(db, cfg)

	_, pw := createTestUser(t, db, "cr_reviewer", common.RoleComplianceReviewer)
	token := loginUser(t, router, "cr_reviewer", pw)

	w := makeRequest(t, router, http.MethodGet, "/api/v1/admin/audit-logs", token, nil)
	if w.Code != http.StatusForbidden {
		t.Errorf("compliance reviewer should not access audit logs, got %d; body: %s", w.Code, w.Body.String())
	}
}

// --- helpers for matrix setup ---

// matrixSetupWorkOrder creates a work order for use by matrix endpoint tests and returns its ID.
func matrixSetupWorkOrder(t *testing.T, env *matrixEnv) uint64 {
	t.Helper()
	tenantToken := env.tokens[common.RoleTenant]
	w := makeRequest(t, env.router, http.MethodPost, "/api/v1/work-orders", tenantToken, map[string]interface{}{
		"property_id": 1,
		"description": "Matrix setup work order for authorization testing across roles.",
		"priority":    common.PriorityNormal,
	})
	if w.Code != http.StatusCreated {
		t.Logf("matrixSetupWorkOrder: got %d; body: %s", w.Code, w.Body.String())
		return 1 // fallback; GET /work-orders/1 will just 404
	}
	var resp struct {
		Data struct{ ID uint64 `json:"id"` } `json:"data"`
	}
	parseResponse(t, w, &resp)
	return resp.Data.ID
}

// matrixSetupPayment creates a payment intent and returns its ID.
func matrixSetupPayment(t *testing.T, env *matrixEnv) uint64 {
	t.Helper()
	managerToken := env.tokens[common.RolePropertyManager]
	w := makeRequest(t, env.router, http.MethodPost, "/api/v1/payments/intents", managerToken, map[string]interface{}{
		"property_id": 1,
		"amount":      50.00,
	})
	if w.Code != http.StatusCreated {
		t.Logf("matrixSetupPayment: got %d; body: %s", w.Code, w.Body.String())
		return 1
	}
	var resp struct {
		Data struct{ ID uint64 `json:"id"` } `json:"data"`
	}
	parseResponse(t, w, &resp)
	return resp.Data.ID
}

// matrixSetupReport creates a report and returns its ID.
func matrixSetupReport(t *testing.T, env *matrixEnv) uint64 {
	t.Helper()
	tenantToken := env.tokens[common.RoleTenant]
	w := makeRequest(t, env.router, http.MethodPost, "/api/v1/governance/reports", tenantToken, map[string]interface{}{
		"target_type": common.ReportTargetTenant,
		"target_id":   1,
		"category":    "Harassment",
		"description": "Matrix setup report for authorization testing across multiple user roles.",
	})
	if w.Code != http.StatusCreated {
		t.Logf("matrixSetupReport: got %d; body: %s", w.Code, w.Body.String())
		return 1
	}
	var resp struct {
		Data struct{ ID uint64 `json:"id"` } `json:"data"`
	}
	parseResponse(t, w, &resp)
	return resp.Data.ID
}

// matrixSetupEnforcement creates an enforcement action for a throwaway user and returns its ID.
func matrixSetupEnforcement(t *testing.T, env *matrixEnv) uint64 {
	t.Helper()

	// Create a throwaway user to target.
	targetUser, _ := createTestUser(t, env.db, "matrix_enf_victim", common.RoleTenant)

	adminToken := env.tokens[common.RoleSystemAdmin]
	w := makeRequest(t, env.router, http.MethodPost, "/api/v1/governance/enforcements", adminToken, map[string]interface{}{
		"user_id":     targetUser.ID,
		"action_type": common.EnforcementWarning,
		"reason":      "Matrix setup enforcement for authorization testing of revoke endpoint access.",
	})
	if w.Code != http.StatusCreated {
		t.Logf("matrixSetupEnforcement: got %d; body: %s", w.Code, w.Body.String())
		return 1
	}
	var resp struct {
		Data struct{ ID uint64 `json:"id"` } `json:"data"`
	}
	parseResponse(t, w, &resp)

	// Warning does NOT suspend the user so the matrix token for that user remains usable.
	// But we need to revoke it — load the auth.User to get their real ID for the session check.
	_ = authpkg.HashToken // just ensure import is used
	return resp.Data.ID
}

// ---------------------------------------------------------------------------
// Negative PM property-scope tests
// ---------------------------------------------------------------------------

// TestPMScope_Payment_UnassignedPMSeesEmptyList verifies that a PropertyManager with no
// property assignments receives an empty list from GET /payments, rather than leaking
// all payments.  Regression guard for the ScopedToPropertyIDs sentinel.
func TestPMScope_Payment_UnassignedPMSeesEmptyList(t *testing.T) {
	db := setupTestDB(t)
	seedRoles(t, db)
	cfg := testConfig()
	router := newTestRouter(db, cfg)

	// PM1 is assigned to property 1.
	pm1User, pm1Pw := createTestUser(t, db, "scope_pm1", common.RolePropertyManager)
	db.Exec("INSERT INTO property_staff_assignments (property_id, user_id, role, is_active) VALUES (?, ?, ?, ?)",
		1, pm1User.ID, "PropertyManager", true)
	pm1Token := loginUser(t, router, "scope_pm1", pm1Pw)

	// PM2 has the PropertyManager role but no property assignments at all.
	_, pm2Pw := createTestUser(t, db, "scope_pm2", common.RolePropertyManager)
	pm2Token := loginUser(t, router, "scope_pm2", pm2Pw)

	// PM1 creates a payment intent for property 1.
	w := makeRequest(t, router, http.MethodPost, "/api/v1/payments/intents", pm1Token, map[string]interface{}{
		"property_id": 1,
		"amount":      100.00,
	})
	assertStatus(t, w, http.StatusCreated)

	var createResp struct {
		Data struct{ ID uint64 `json:"id"` } `json:"data"`
	}
	parseResponse(t, w, &createResp)
	paymentID := createResp.Data.ID
	if paymentID == 0 {
		t.Fatalf("expected a non-zero payment ID from PM1")
	}

	// PM2 lists payments — must get an empty list, not PM1's payment.
	w = makeRequest(t, router, http.MethodGet, "/api/v1/payments", pm2Token, nil)
	assertStatus(t, w, http.StatusOK)

	var listResp struct {
		Data []struct{ ID uint64 `json:"id"` } `json:"data"`
	}
	parseResponse(t, w, &listResp)
	if len(listResp.Data) != 0 {
		t.Errorf("PM2 (no property assignments) should see 0 payments, got %d", len(listResp.Data))
	}

	// PM2 fetching the specific payment by ID must be denied.
	w = makeRequest(t, router, http.MethodGet, fmt.Sprintf("/api/v1/payments/%d", paymentID), pm2Token, nil)
	if w.Code != http.StatusForbidden {
		t.Errorf("PM2 should not access payment for unmanaged property, got %d; body: %s", w.Code, w.Body.String())
	}
}

// TestPMScope_Attachment_DeleteBlockedForUnassignedPM verifies that a PropertyManager who
// does NOT manage a work order's property cannot delete attachments belonging to that work order.
func TestPMScope_Attachment_DeleteBlockedForUnassignedPM(t *testing.T) {
	db := setupTestDB(t)
	seedRoles(t, db)
	cfg := testConfig()
	router := newTestRouter(db, cfg)

	// PM1 manages property 1; PM2 manages nothing.
	pm1User, pm1Pw := createTestUser(t, db, "att_pm1", common.RolePropertyManager)
	db.Exec("INSERT INTO property_staff_assignments (property_id, user_id, role, is_active) VALUES (?, ?, ?, ?)",
		1, pm1User.ID, "PropertyManager", true)
	pm1Token := loginUser(t, router, "att_pm1", pm1Pw)

	_, pm2Pw := createTestUser(t, db, "att_pm2", common.RolePropertyManager)
	pm2Token := loginUser(t, router, "att_pm2", pm2Pw)

	// Tenant creates a work order for property 1.
	tenantUser, tenantPw := createTestUser(t, db, "att_tenant", common.RoleTenant)
	tenantToken := loginUser(t, router, "att_tenant", tenantPw)

	w := makeRequest(t, router, http.MethodPost, "/api/v1/work-orders", tenantToken, map[string]interface{}{
		"property_id": 1,
		"description": "Negative scope test: work order for attachment delete auth check.",
		"priority":    common.PriorityNormal,
	})
	assertStatus(t, w, http.StatusCreated)

	var woResp struct {
		Data struct{ ID uint64 `json:"id"` } `json:"data"`
	}
	parseResponse(t, w, &woResp)
	woID := woResp.Data.ID
	_ = tenantUser // created for context, token is used above

	// Insert an attachment record directly into the DB (avoids multipart upload complexity).
	db.Exec(`INSERT INTO attachments (uuid, entity_type, entity_id, filename, mime_type, file_size, sha256_hash, storage_path, uploaded_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"test-uuid-scope-attach", "WorkOrder", woID, "test.jpg", "image/jpeg",
		1024, "abc123hash", "attachments/test-uuid-scope-attach.jpg", pm1User.ID)

	var attachID uint64
	db.Raw("SELECT id FROM attachments WHERE uuid = ?", "test-uuid-scope-attach").Scan(&attachID)
	if attachID == 0 {
		t.Fatalf("failed to insert test attachment")
	}

	// PM2 (no property assignment) must be denied.
	w = makeRequest(t, router, http.MethodDelete, fmt.Sprintf("/api/v1/work-orders/attachments/%d", attachID), pm2Token, nil)
	if w.Code != http.StatusForbidden {
		t.Errorf("PM2 should not delete attachment for unmanaged property, got %d; body: %s", w.Code, w.Body.String())
	}

	// PM1 (manages property 1) must be allowed.
	w = makeRequest(t, router, http.MethodDelete, fmt.Sprintf("/api/v1/work-orders/attachments/%d", attachID), pm1Token, nil)
	if w.Code != http.StatusOK && w.Code != http.StatusNoContent {
		t.Errorf("PM1 should be able to delete attachment for managed property, got %d; body: %s", w.Code, w.Body.String())
	}
}
