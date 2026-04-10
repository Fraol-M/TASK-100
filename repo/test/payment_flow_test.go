package integration_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"propertyops/backend/internal/common"
	"propertyops/backend/internal/payments"
	"gorm.io/gorm"
)

func seedPaymentProperty(t *testing.T, db *gorm.DB, propertyID uint64) {
	t.Helper()
	if err := db.Exec(`INSERT INTO properties (id, uuid, name, address_line1, city, state, zip_code, timezone, is_active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NOW(), NOW())`,
		propertyID, newUUID(), "Payments Test Property", "1 Billing St", "Austin", "TX", "78701", "America/New_York", true).Error; err != nil {
		t.Fatalf("seedPaymentProperty: %v", err)
	}
}

func assignPMToProperty(t *testing.T, db *gorm.DB, propertyID, userID uint64) {
	t.Helper()
	if err := db.Exec(`INSERT INTO property_staff_assignments (property_id, user_id, role, is_active)
		VALUES (?, ?, ?, ?)`, propertyID, userID, common.RolePropertyManager, true).Error; err != nil {
		t.Fatalf("assignPMToProperty: %v", err)
	}
}


// TestPaymentIntent_CreatesWithExpiry verifies that a payment intent is created with
// an expires_at field set in the future.
func TestPaymentIntent_CreatesWithExpiry(t *testing.T) {
	db := setupTestDB(t)
	seedRoles(t, db)
	cfg := testConfig()
	router := newTestRouter(db, cfg)

	managerUser, managerPw := createTestUser(t, db, "pi_manager", common.RolePropertyManager)
	seedPaymentProperty(t, db, 1)
	assignPMToProperty(t, db, 1, managerUser.ID)
	managerToken := loginUser(t, router, "pi_manager", managerPw)

	intentBody := map[string]interface{}{
		"property_id": 1,
		"amount":      250.00,
		"description": "Monthly rent payment for unit 101",
	}
	w := makeRequest(t, router, http.MethodPost, "/api/v1/payments/intents", managerToken, intentBody)
	assertStatus(t, w, http.StatusCreated)

	var resp struct {
		Data struct {
			ID        uint64  `json:"id"`
			Status    string  `json:"status"`
			Kind      string  `json:"kind"`
			Amount    float64 `json:"amount"`
			ExpiresAt *string `json:"expires_at"`
		} `json:"data"`
	}
	parseResponse(t, w, &resp)

	if resp.Data.ID == 0 {
		t.Errorf("expected non-zero payment ID")
	}
	if resp.Data.Status != common.PaymentStatusPending {
		t.Errorf("expected status=Pending, got %q", resp.Data.Status)
	}
	if resp.Data.Kind != common.PaymentKindIntent {
		t.Errorf("expected kind=Intent, got %q", resp.Data.Kind)
	}
	if resp.Data.Amount != 250.00 {
		t.Errorf("expected amount=250, got %v", resp.Data.Amount)
	}
	if resp.Data.ExpiresAt == nil || *resp.Data.ExpiresAt == "" {
		t.Errorf("expected expires_at to be set")
	}
}

// TestPaymentIntent_MarkPaid_ByManager verifies that a manager can mark an intent as paid.
func TestPaymentIntent_MarkPaid_ByManager(t *testing.T) {
	db := setupTestDB(t)
	seedRoles(t, db)
	cfg := testConfig()
	router := newTestRouter(db, cfg)

	managerUser, managerPw := createTestUser(t, db, "mp_manager", common.RolePropertyManager)
	seedPaymentProperty(t, db, 1)
	assignPMToProperty(t, db, 1, managerUser.ID)
	managerToken := loginUser(t, router, "mp_manager", managerPw)

	// Create intent.
	w := makeRequest(t, router, http.MethodPost, "/api/v1/payments/intents", managerToken, map[string]interface{}{
		"property_id": 1,
		"amount":      300.00,
	})
	assertStatus(t, w, http.StatusCreated)
	var createResp struct {
		Data struct{ ID uint64 `json:"id"` } `json:"data"`
	}
	parseResponse(t, w, &createResp)
	paymentID := createResp.Data.ID

	// Mark paid.
	w = makeRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/v1/payments/%d/mark-paid", paymentID), managerToken, map[string]string{"notes": "Received via bank transfer"})
	assertStatus(t, w, http.StatusOK)

	var resp struct {
		Data struct {
			Status string  `json:"status"`
			PaidAt *string `json:"paid_at"`
		} `json:"data"`
	}
	parseResponse(t, w, &resp)
	if resp.Data.Status != common.PaymentStatusPaid {
		t.Errorf("expected status=Paid, got %q", resp.Data.Status)
	}
	if resp.Data.PaidAt == nil {
		t.Errorf("expected paid_at to be set")
	}
}

// TestPaymentDualApproval_Above500 verifies that a payment above $500 requires two
// distinct approvers to reach Settled status.
func TestPaymentDualApproval_Above500(t *testing.T) {
	db := setupTestDB(t)
	seedRoles(t, db)
	cfg := testConfig()
	router := newTestRouter(db, cfg)

	manager1User, manager1Pw := createTestUser(t, db, "da_manager1", common.RolePropertyManager)
	manager2User, manager2Pw := createTestUser(t, db, "da_manager2", common.RolePropertyManager)
	seedPaymentProperty(t, db, 1)
	assignPMToProperty(t, db, 1, manager1User.ID)
	assignPMToProperty(t, db, 1, manager2User.ID)

	m1Token := loginUser(t, router, "da_manager1", manager1Pw)
	m2Token := loginUser(t, router, "da_manager2", manager2Pw)

	// Create intent for $750 (above $500 threshold).
	w := makeRequest(t, router, http.MethodPost, "/api/v1/payments/intents", m1Token, map[string]interface{}{
		"property_id": 1,
		"amount":      750.00,
	})
	assertStatus(t, w, http.StatusCreated)
	var createResp struct {
		Data struct{ ID uint64 `json:"id"` } `json:"data"`
	}
	parseResponse(t, w, &createResp)
	paymentID := createResp.Data.ID

	// Mark paid.
	makeRequest(t, router, http.MethodPost, fmt.Sprintf("/api/v1/payments/%d/mark-paid", paymentID), m1Token,
		map[string]string{})

	// First approval (manager1).
	w = makeRequest(t, router, http.MethodPost, fmt.Sprintf("/api/v1/payments/%d/approve", paymentID), m1Token,
		map[string]string{"notes": "First approval"})
	assertStatus(t, w, http.StatusCreated)

	// Verify payment is still Paid (not yet Settled after first approval for >$500).
	w = makeRequest(t, router, http.MethodGet, fmt.Sprintf("/api/v1/payments/%d", paymentID), m1Token, nil)
	assertStatus(t, w, http.StatusOK)
	var getResp struct {
		Data struct{ Status string `json:"status"` } `json:"data"`
	}
	parseResponse(t, w, &getResp)
	if getResp.Data.Status == common.PaymentStatusSettled {
		t.Errorf("payment should not be Settled after only one approval for >$500 amount")
	}

	// Second approval (manager2).
	w = makeRequest(t, router, http.MethodPost, fmt.Sprintf("/api/v1/payments/%d/approve", paymentID), m2Token,
		map[string]string{"notes": "Second approval"})
	assertStatus(t, w, http.StatusCreated)

	// Now the payment should be Settled.
	w = makeRequest(t, router, http.MethodGet, fmt.Sprintf("/api/v1/payments/%d", paymentID), m1Token, nil)
	assertStatus(t, w, http.StatusOK)
	parseResponse(t, w, &getResp)
	if getResp.Data.Status != common.PaymentStatusSettled {
		t.Errorf("expected status=Settled after dual approval, got %q", getResp.Data.Status)
	}
}

// TestPaymentDualApproval_SameApproverBlocked verifies that the same user cannot
// approve a >$500 payment twice (returns 409).
func TestPaymentDualApproval_SameApproverBlocked(t *testing.T) {
	db := setupTestDB(t)
	seedRoles(t, db)
	cfg := testConfig()
	router := newTestRouter(db, cfg)

	managerUser, managerPw := createTestUser(t, db, "sa_manager", common.RolePropertyManager)
	seedPaymentProperty(t, db, 1)
	assignPMToProperty(t, db, 1, managerUser.ID)
	managerToken := loginUser(t, router, "sa_manager", managerPw)

	// Create intent for $1000.
	w := makeRequest(t, router, http.MethodPost, "/api/v1/payments/intents", managerToken, map[string]interface{}{
		"property_id": 1,
		"amount":      1000.00,
	})
	assertStatus(t, w, http.StatusCreated)
	var createResp struct {
		Data struct{ ID uint64 `json:"id"` } `json:"data"`
	}
	parseResponse(t, w, &createResp)
	paymentID := createResp.Data.ID

	makeRequest(t, router, http.MethodPost, fmt.Sprintf("/api/v1/payments/%d/mark-paid", paymentID), managerToken,
		map[string]string{})

	// First approval.
	w = makeRequest(t, router, http.MethodPost, fmt.Sprintf("/api/v1/payments/%d/approve", paymentID), managerToken,
		map[string]string{})
	assertStatus(t, w, http.StatusCreated)

	// Second approval — same user → 409.
	w = makeRequest(t, router, http.MethodPost, fmt.Sprintf("/api/v1/payments/%d/approve", paymentID), managerToken,
		map[string]string{})
	if w.Code != http.StatusConflict && w.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 409 or 422 for duplicate approver, got %d; body: %s", w.Code, w.Body.String())
	}
}

// TestPaymentDualApproval_Below500_SingleApprover verifies that a payment at or
// below $500 only requires a single approval to reach Settled.
func TestPaymentDualApproval_Below500_SingleApprover(t *testing.T) {
	db := setupTestDB(t)
	seedRoles(t, db)
	cfg := testConfig()
	router := newTestRouter(db, cfg)

	managerUser, managerPw := createTestUser(t, db, "bl_manager", common.RolePropertyManager)
	seedPaymentProperty(t, db, 1)
	assignPMToProperty(t, db, 1, managerUser.ID)
	managerToken := loginUser(t, router, "bl_manager", managerPw)

	// Create intent for $250 (below $500 threshold).
	w := makeRequest(t, router, http.MethodPost, "/api/v1/payments/intents", managerToken, map[string]interface{}{
		"property_id": 1,
		"amount":      250.00,
	})
	assertStatus(t, w, http.StatusCreated)
	var createResp struct {
		Data struct{ ID uint64 `json:"id"` } `json:"data"`
	}
	parseResponse(t, w, &createResp)
	paymentID := createResp.Data.ID

	makeRequest(t, router, http.MethodPost, fmt.Sprintf("/api/v1/payments/%d/mark-paid", paymentID), managerToken,
		map[string]string{})

	// Single approval → should reach Settled.
	w = makeRequest(t, router, http.MethodPost, fmt.Sprintf("/api/v1/payments/%d/approve", paymentID), managerToken,
		map[string]string{})
	assertStatus(t, w, http.StatusCreated)

	// Verify Settled.
	w = makeRequest(t, router, http.MethodGet, fmt.Sprintf("/api/v1/payments/%d", paymentID), managerToken, nil)
	assertStatus(t, w, http.StatusOK)
	var getResp struct {
		Data struct{ Status string `json:"status"` } `json:"data"`
	}
	parseResponse(t, w, &getResp)
	if getResp.Data.Status != common.PaymentStatusSettled {
		t.Errorf("expected status=Settled after single approval for <=500, got %q", getResp.Data.Status)
	}
}

// TestPaymentReversal_RequiresReason verifies that a reversal without a reason body
// (or with an empty/too-short reason) is rejected.
func TestPaymentReversal_RequiresReason(t *testing.T) {
	db := setupTestDB(t)
	seedRoles(t, db)
	cfg := testConfig()
	router := newTestRouter(db, cfg)

	managerUser, managerPw := createTestUser(t, db, "rev_manager", common.RolePropertyManager)
	seedPaymentProperty(t, db, 1)
	assignPMToProperty(t, db, 1, managerUser.ID)
	managerToken := loginUser(t, router, "rev_manager", managerPw)

	// Create and mark paid.
	w := makeRequest(t, router, http.MethodPost, "/api/v1/payments/intents", managerToken, map[string]interface{}{
		"property_id": 1,
		"amount":      100.00,
	})
	assertStatus(t, w, http.StatusCreated)
	var createResp struct {
		Data struct{ ID uint64 `json:"id"` } `json:"data"`
	}
	parseResponse(t, w, &createResp)
	paymentID := createResp.Data.ID

	makeRequest(t, router, http.MethodPost, fmt.Sprintf("/api/v1/payments/%d/mark-paid", paymentID), managerToken,
		map[string]string{})

	// Attempt reversal with no reason (empty string) → must fail.
	w = makeRequest(t, router, http.MethodPost, fmt.Sprintf("/api/v1/payments/%d/reverse", paymentID), managerToken,
		map[string]string{"reason": ""})
	if w.Code == http.StatusOK || w.Code == http.StatusCreated {
		t.Errorf("expected non-2xx for reversal without reason, got %d", w.Code)
	}

	// Attempt reversal with a too-short reason (< 10 chars) → must fail.
	w = makeRequest(t, router, http.MethodPost, fmt.Sprintf("/api/v1/payments/%d/reverse", paymentID), managerToken,
		map[string]string{"reason": "short"})
	if w.Code == http.StatusOK || w.Code == http.StatusCreated {
		t.Errorf("expected non-2xx for reversal with short reason, got %d", w.Code)
	}

	// Reversal with valid reason → must succeed.
	w = makeRequest(t, router, http.MethodPost, fmt.Sprintf("/api/v1/payments/%d/reverse", paymentID), managerToken,
		map[string]string{"reason": "Duplicate payment posted in error by accounting team"})
	assertStatus(t, w, http.StatusCreated)
}

// TestPaymentExpiry verifies that stale pending intents can be identified.
// We test by directly setting expires_at in the past and checking the DB state.
func TestPaymentExpiry(t *testing.T) {
	db := setupTestDB(t)
	seedRoles(t, db)
	cfg := testConfig()
	router := newTestRouter(db, cfg)

	managerUser, managerPw := createTestUser(t, db, "exp_manager", common.RolePropertyManager)
	seedPaymentProperty(t, db, 1)
	assignPMToProperty(t, db, 1, managerUser.ID)
	managerToken := loginUser(t, router, "exp_manager", managerPw)

	// Create intent.
	w := makeRequest(t, router, http.MethodPost, "/api/v1/payments/intents", managerToken, map[string]interface{}{
		"property_id": 1,
		"amount":      75.00,
	})
	assertStatus(t, w, http.StatusCreated)
	var createResp struct {
		Data struct{ ID uint64 `json:"id"` } `json:"data"`
	}
	parseResponse(t, w, &createResp)
	paymentID := createResp.Data.ID

	// Manually expire the intent.
	past := time.Now().UTC().Add(-2 * time.Hour)
	if err := db.Model(&payments.Payment{}).Where("id = ?", paymentID).Update("expires_at", past).Error; err != nil {
		t.Fatalf("failed to expire payment: %v", err)
	}

	// Attempt to mark-paid on expired intent → must fail with 422.
	w = makeRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/v1/payments/%d/mark-paid", paymentID), managerToken, map[string]string{})
	if w.Code != http.StatusUnprocessableEntity && w.Code != http.StatusBadRequest {
		t.Errorf("expected 422/400 for expired intent mark-paid, got %d; body: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Reconciliation authorization tests
// ---------------------------------------------------------------------------

// TestReconciliation_AdminCanRun verifies that a SystemAdmin can trigger a
// reconciliation run and receive a 201 response.
func TestReconciliation_AdminCanRun(t *testing.T) {
	db := setupTestDB(t)
	seedRoles(t, db)
	cfg := testConfig()
	router := newTestRouter(db, cfg)

	_, adminPw := createTestUser(t, db, "recon_admin", common.RoleSystemAdmin)
	adminToken := loginUser(t, router, "recon_admin", adminPw)

	w := makeRequest(t, router, http.MethodPost, "/api/v1/payments/reconciliation/run", adminToken, map[string]string{
		"run_date": "2024-01-15",
	})
	assertStatus(t, w, http.StatusCreated)
}

// TestReconciliation_PMDenied verifies that a PropertyManager cannot trigger a
// reconciliation run and receives 403.
func TestReconciliation_PMDenied(t *testing.T) {
	db := setupTestDB(t)
	seedRoles(t, db)
	cfg := testConfig()
	router := newTestRouter(db, cfg)

	_, pmPw := createTestUser(t, db, "recon_pm", common.RolePropertyManager)
	pmToken := loginUser(t, router, "recon_pm", pmPw)

	w := makeRequest(t, router, http.MethodPost, "/api/v1/payments/reconciliation/run", pmToken, map[string]string{
		"run_date": "2024-01-15",
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("PropertyManager should be denied reconciliation run, got %d; body: %s", w.Code, w.Body.String())
	}
}

// TestReconciliation_Unauthenticated verifies that all three reconciliation
// endpoints return 401 without a bearer token.
func TestReconciliation_Unauthenticated(t *testing.T) {
	db := setupTestDB(t)
	seedRoles(t, db)
	cfg := testConfig()
	router := newTestRouter(db, cfg)

	endpoints := []struct {
		method string
		path   string
		body   interface{}
	}{
		{http.MethodPost, "/api/v1/payments/reconciliation/run", map[string]string{"run_date": "2024-01-15"}},
		{http.MethodGet, "/api/v1/payments/reconciliation", nil},
		{http.MethodGet, "/api/v1/payments/reconciliation/1", nil},
	}

	for _, ep := range endpoints {
		ep := ep
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			w := makeRequest(t, router, ep.method, ep.path, "", ep.body)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("unauthenticated: expected 401, got %d; body: %s", w.Code, w.Body.String())
			}
		})
	}
}

// TestReconciliation_AllNonAdminRolesDenied verifies that every non-SystemAdmin role
// receives 403 on the reconciliation run endpoint.
func TestReconciliation_AllNonAdminRolesDenied(t *testing.T) {
	db := setupTestDB(t)
	seedRoles(t, db)
	cfg := testConfig()
	router := newTestRouter(db, cfg)

	deniedRoles := []string{
		common.RoleTenant,
		common.RoleTechnician,
		common.RolePropertyManager,
		common.RoleComplianceReviewer,
	}

	for _, role := range deniedRoles {
		role := role
		username := "recon_denied_" + role
		_, pw := createTestUser(t, db, username, role)
		token := loginUser(t, router, username, pw)

		t.Run(role, func(t *testing.T) {
			w := makeRequest(t, router, http.MethodPost, "/api/v1/payments/reconciliation/run", token, map[string]string{
				"run_date": "2024-01-15",
			})
			if w.Code != http.StatusForbidden {
				t.Errorf("role=%s: expected 403, got %d; body: %s", role, w.Code, w.Body.String())
			}
		})
	}
}

// TestReconciliation_List_AdminCanList verifies that a SystemAdmin can list
// reconciliation runs (even if none exist yet).
func TestReconciliation_List_AdminCanList(t *testing.T) {
	db := setupTestDB(t)
	seedRoles(t, db)
	cfg := testConfig()
	router := newTestRouter(db, cfg)

	_, adminPw := createTestUser(t, db, "recon_list_admin", common.RoleSystemAdmin)
	adminToken := loginUser(t, router, "recon_list_admin", adminPw)

	w := makeRequest(t, router, http.MethodGet, "/api/v1/payments/reconciliation", adminToken, nil)
	assertStatus(t, w, http.StatusOK)
}

// TestReconciliation_List_PMDenied verifies that a PropertyManager cannot list
// reconciliation runs.
func TestReconciliation_List_PMDenied(t *testing.T) {
	db := setupTestDB(t)
	seedRoles(t, db)
	cfg := testConfig()
	router := newTestRouter(db, cfg)

	_, pmPw := createTestUser(t, db, "recon_list_pm", common.RolePropertyManager)
	pmToken := loginUser(t, router, "recon_list_pm", pmPw)

	w := makeRequest(t, router, http.MethodGet, "/api/v1/payments/reconciliation", pmToken, nil)
	if w.Code != http.StatusForbidden {
		t.Errorf("PropertyManager should be denied reconciliation list, got %d; body: %s", w.Code, w.Body.String())
	}
}

// TestPaymentList verifies that the payments list endpoint returns the created payment.
func TestPaymentList(t *testing.T) {
	db := setupTestDB(t)
	seedRoles(t, db)
	cfg := testConfig()
	router := newTestRouter(db, cfg)

	managerUser, managerPw := createTestUser(t, db, "pl_manager", common.RolePropertyManager)
	seedPaymentProperty(t, db, 1)
	assignPMToProperty(t, db, 1, managerUser.ID)
	managerToken := loginUser(t, router, "pl_manager", managerPw)

	// Create two intents.
	for i := 0; i < 2; i++ {
		makeRequest(t, router, http.MethodPost, "/api/v1/payments/intents", managerToken, map[string]interface{}{
			"property_id": 1,
			"amount":      float64(100 * (i + 1)),
		})
	}

	w := makeRequest(t, router, http.MethodGet, "/api/v1/payments", managerToken, nil)
	assertStatus(t, w, http.StatusOK)

	var resp struct {
		Data []struct {
			ID uint64 `json:"id"`
		} `json:"data"`
	}
	parseResponse(t, w, &resp)
	if len(resp.Data) < 2 {
		t.Errorf("expected at least 2 payments, got %d", len(resp.Data))
	}
}
