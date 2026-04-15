package integration_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"propertyops/backend/internal/common"
	"propertyops/backend/internal/payments"
)

// seedPayment inserts a PAID payment-intent row. CreateSettlement enforces
// `status == "Paid"` on the linked payment, and CreateMakeup tolerates any
// status below the dual-approval threshold, so both endpoints can target this
// seed safely. We issue the INSERT followed by an explicit UPDATE on `status`
// and `paid_at` to guarantee those columns land as intended, independent of
// any GORM default-tag interaction with the `status VARCHAR(20) NOT NULL
// DEFAULT 'Pending'` column.
func seedPayment(t *testing.T, env *plainEnv, propertyID uint64, amount float64) *payments.Payment {
	t.Helper()

	p := payments.Payment{
		UUID:       newUUID(),
		PropertyID: propertyID,
		Kind:       common.PaymentKindIntent,
		Amount:     amount,
		Currency:   "USD",
		Status:     common.PaymentStatusPaid,
	}
	if err := env.db.Create(&p).Error; err != nil {
		t.Fatalf("seedPayment: create: %v", err)
	}

	// Force status explicitly — some GORM/driver combinations end up applying
	// the column DEFAULT ('Pending') on INSERT. Updating after Create is
	// unambiguous SQL and guarantees the row observed by later GetPayment calls.
	now := time.Now().UTC()
	if err := env.db.Exec(
		"UPDATE payments SET status = ?, paid_at = ? WHERE id = ?",
		common.PaymentStatusPaid, now, p.ID,
	).Error; err != nil {
		t.Fatalf("seedPayment: force status: %v", err)
	}

	// Reload so the caller sees the current DB state (Status = "Paid", PaidAt set).
	var reloaded payments.Payment
	if err := env.db.First(&reloaded, p.ID).Error; err != nil {
		t.Fatalf("seedPayment: reload: %v", err)
	}
	if reloaded.Status != common.PaymentStatusPaid {
		t.Fatalf("seedPayment: expected status=%q after force-update, got %q",
			common.PaymentStatusPaid, reloaded.Status)
	}
	return &reloaded
}

// TestPayment_CreateMakeup_ByPM verifies that a PropertyManager managing the
// property can create a makeup posting against an existing payment.
func TestPayment_CreateMakeup_ByPM(t *testing.T) {
	env := newPlainEnv(t)

	pmUser, pmToken := createUserAndLogin(t, env.db, env.router, "pmk_pm", common.RolePropertyManager)
	const propertyID uint64 = 1
	seedPaymentProperty(t, env.db, propertyID)
	assignPMToProperty(t, env.db, propertyID, pmUser.ID)

	existing := seedPayment(t, env, propertyID, 100.00)

	body := map[string]interface{}{
		"amount":      25.50,
		"description": "Adjustment for short payment last month",
	}
	w := makeRequest(t, env.router, http.MethodPost,
		fmt.Sprintf("/api/v1/payments/%d/makeup", existing.ID), pmToken, body)
	assertStatus(t, w, http.StatusCreated)

	var resp struct {
		Data struct {
			ID         uint64  `json:"id"`
			Kind       string  `json:"kind"`
			Amount     float64 `json:"amount"`
			PropertyID uint64  `json:"property_id"`
		} `json:"data"`
	}
	parseResponse(t, w, &resp)
	if resp.Data.ID == 0 {
		t.Error("expected non-zero makeup payment ID")
	}
	if resp.Data.Kind != common.PaymentKindMakeupPosting {
		t.Errorf("expected kind=%q, got %q", common.PaymentKindMakeupPosting, resp.Data.Kind)
	}
	if resp.Data.Amount != 25.50 {
		t.Errorf("expected amount=25.50, got %v", resp.Data.Amount)
	}
	if resp.Data.PropertyID != propertyID {
		t.Errorf("expected property_id=%d, got %d", propertyID, resp.Data.PropertyID)
	}
}

// TestPayment_CreateMakeup_ByTenantForbidden verifies a Tenant cannot create
// a makeup posting (handler-level role check).
func TestPayment_CreateMakeup_ByTenantForbidden(t *testing.T) {
	env := newPlainEnv(t)

	_, tenantToken := createUserAndLogin(t, env.db, env.router, "pmk_tenant", common.RoleTenant)
	const propertyID uint64 = 1
	seedPaymentProperty(t, env.db, propertyID)
	existing := seedPayment(t, env, propertyID, 100.00)

	body := map[string]interface{}{
		"amount": 5.00,
	}
	w := makeRequest(t, env.router, http.MethodPost,
		fmt.Sprintf("/api/v1/payments/%d/makeup", existing.ID), tenantToken, body)
	assertStatus(t, w, http.StatusForbidden)
}

// TestPayment_CreateSettlement_ByPM verifies a PropertyManager can create a
// settlement linked to an existing payment on a property they manage.
func TestPayment_CreateSettlement_ByPM(t *testing.T) {
	env := newPlainEnv(t)

	pmUser, pmToken := createUserAndLogin(t, env.db, env.router, "pms_pm", common.RolePropertyManager)
	const propertyID uint64 = 1
	seedPaymentProperty(t, env.db, propertyID)
	assignPMToProperty(t, env.db, propertyID, pmUser.ID)

	linked := seedPayment(t, env, propertyID, 200.00)

	body := map[string]interface{}{
		"payment_id":  linked.ID,
		"amount":      200.00,
		"description": "Bank wire reconciled with intent",
	}
	w := makeRequest(t, env.router, http.MethodPost, "/api/v1/payments/settlements", pmToken, body)
	assertStatus(t, w, http.StatusCreated)

	var resp struct {
		Data struct {
			ID         uint64  `json:"id"`
			Kind       string  `json:"kind"`
			Amount     float64 `json:"amount"`
			PropertyID uint64  `json:"property_id"`
		} `json:"data"`
	}
	parseResponse(t, w, &resp)
	if resp.Data.Kind != common.PaymentKindSettlementPosting {
		t.Errorf("expected kind=%q, got %q", common.PaymentKindSettlementPosting, resp.Data.Kind)
	}
	if resp.Data.Amount != 200.00 {
		t.Errorf("expected amount=200.00, got %v", resp.Data.Amount)
	}
}

// TestPayment_CreateSettlement_ByTenantForbidden verifies a Tenant cannot create
// a settlement.
func TestPayment_CreateSettlement_ByTenantForbidden(t *testing.T) {
	env := newPlainEnv(t)

	_, tenantToken := createUserAndLogin(t, env.db, env.router, "pms_tenant", common.RoleTenant)
	const propertyID uint64 = 1
	seedPaymentProperty(t, env.db, propertyID)
	linked := seedPayment(t, env, propertyID, 50.00)

	body := map[string]interface{}{
		"payment_id": linked.ID,
		"amount":     50.00,
	}
	w := makeRequest(t, env.router, http.MethodPost, "/api/v1/payments/settlements", tenantToken, body)
	assertStatus(t, w, http.StatusForbidden)
}
