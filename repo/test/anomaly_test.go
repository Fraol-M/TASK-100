package integration_test

import (
	"net/http"
	"testing"
)

// TestAnomaly_ValidIngestion verifies that a well-formed anomaly payload
// is accepted and returns 201 with the ingested status body.
func TestAnomaly_ValidIngestion(t *testing.T) {
	db := setupTestDB(t)
	cfg := testConfig()
	router := newTestRouter(db, cfg)

	body := map[string]interface{}{
		"source":      "sensor-42",
		"severity":    "high",
		"description": "temperature exceeded threshold",
	}
	w := makeRequest(t, router, http.MethodPost, "/api/v1/admin/anomaly", "", body)
	assertStatus(t, w, http.StatusCreated)

	var resp struct {
		Data struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"data"`
	}
	parseResponse(t, w, &resp)
	if resp.Data.Status != "ingested" {
		t.Errorf("expected status=ingested, got %q", resp.Data.Status)
	}
	if resp.Data.Message != "anomaly recorded successfully" {
		t.Errorf("expected message=%q, got %q", "anomaly recorded successfully", resp.Data.Message)
	}
}

// TestAnomaly_MissingDescription verifies that an anomaly payload without a
// description is rejected with 400.
func TestAnomaly_MissingDescription(t *testing.T) {
	db := setupTestDB(t)
	cfg := testConfig()
	router := newTestRouter(db, cfg)

	body := map[string]interface{}{
		"source":   "sensor-42",
		"severity": "high",
		// description intentionally omitted
	}
	w := makeRequest(t, router, http.MethodPost, "/api/v1/admin/anomaly", "", body)
	assertStatus(t, w, http.StatusBadRequest)
}

// TestAnomaly_NoAuthRequired verifies that the anomaly endpoint does not
// require a session token — it is protected only by the CIDR allowlist.
// The test config permits all source IPs (0.0.0.0/0).
func TestAnomaly_NoAuthRequired(t *testing.T) {
	db := setupTestDB(t)
	cfg := testConfig()
	router := newTestRouter(db, cfg)

	// No bearer token — the endpoint bypasses session auth.
	body := map[string]interface{}{
		"source":      "internal-monitor",
		"severity":    "low",
		"description": "disk usage above 80 percent",
	}
	w := makeRequest(t, router, http.MethodPost, "/api/v1/admin/anomaly", "", body)
	assertStatus(t, w, http.StatusCreated)
}

// TestAnomaly_WithMetadata verifies that the optional metadata field is accepted.
func TestAnomaly_WithMetadata(t *testing.T) {
	db := setupTestDB(t)
	cfg := testConfig()
	router := newTestRouter(db, cfg)

	body := map[string]interface{}{
		"source":      "prometheus",
		"severity":    "critical",
		"description": "p99 latency exceeded SLA threshold",
		"metadata": map[string]interface{}{
			"service":  "api",
			"p99_ms":   312,
			"threshold": 300,
		},
	}
	w := makeRequest(t, router, http.MethodPost, "/api/v1/admin/anomaly", "", body)
	assertStatus(t, w, http.StatusCreated)
}
