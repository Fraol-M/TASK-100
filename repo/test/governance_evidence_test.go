package integration_test

import (
	"fmt"
	"net/http"
	"os"
	"testing"

	"propertyops/backend/internal/common"
)

// fileGovernanceReport creates a governance report as the given token and
// returns the new report ID. Used by evidence tests as the parent resource.
func fileGovernanceReport(t *testing.T, env *plainEnv, token string) uint64 {
	t.Helper()

	w := makeRequest(t, env.router, http.MethodPost, "/api/v1/governance/reports", token,
		map[string]interface{}{
			"target_type": common.ReportTargetTenant,
			"target_id":   42,
			"category":    "Harassment",
			"description": "This tenant has been making repeated complaints with insufficient cause across multiple weeks.",
		})
	assertStatus(t, w, http.StatusCreated)
	var resp struct {
		Data struct{ ID uint64 `json:"id"` } `json:"data"`
	}
	parseResponse(t, w, &resp)
	if resp.Data.ID == 0 {
		t.Fatal("fileGovernanceReport: got ID = 0")
	}
	return resp.Data.ID
}

// TestGovernance_UploadEvidence_AsReporter verifies the original reporter can
// upload a JPEG evidence attachment to their own report.
func TestGovernance_UploadEvidence_AsReporter(t *testing.T) {
	env := newPlainEnv(t)
	t.Cleanup(func() { os.RemoveAll("evidence") })

	_, reporterToken := createUserAndLogin(t, env.db, env.router, "gue_reporter", common.RoleTenant)
	reportID := fileGovernanceReport(t, env, reporterToken)

	w := postMultipart(t, env.router, http.MethodPost,
		fmt.Sprintf("/api/v1/governance/reports/%d/evidence", reportID), reporterToken,
		nil,
		[]multipartFile{{
			FieldName: "file",
			Filename:  "evidence.jpg",
			MimeType:  "image/jpeg",
			Content:   jpegMinimal,
		}})
	assertStatus(t, w, http.StatusCreated)
}

// TestGovernance_UploadEvidence_AsReviewer verifies a ComplianceReviewer can
// upload evidence to any report (cross-reporter access).
func TestGovernance_UploadEvidence_AsReviewer(t *testing.T) {
	env := newPlainEnv(t)
	t.Cleanup(func() { os.RemoveAll("evidence") })

	_, reporterToken := createUserAndLogin(t, env.db, env.router, "guer_reporter", common.RoleTenant)
	_, reviewerToken := createUserAndLogin(t, env.db, env.router, "guer_rev", common.RoleComplianceReviewer)
	reportID := fileGovernanceReport(t, env, reporterToken)

	w := postMultipart(t, env.router, http.MethodPost,
		fmt.Sprintf("/api/v1/governance/reports/%d/evidence", reportID), reviewerToken,
		nil,
		[]multipartFile{{
			FieldName: "file",
			Filename:  "review.jpg",
			MimeType:  "image/jpeg",
			Content:   jpegMinimal,
		}})
	assertStatus(t, w, http.StatusCreated)
}

// TestGovernance_UploadEvidence_OtherTenantForbidden verifies a different
// tenant cannot upload evidence to someone else's report.
func TestGovernance_UploadEvidence_OtherTenantForbidden(t *testing.T) {
	env := newPlainEnv(t)
	t.Cleanup(func() { os.RemoveAll("evidence") })

	_, reporterToken := createUserAndLogin(t, env.db, env.router, "guef_rep", common.RoleTenant)
	_, otherToken := createUserAndLogin(t, env.db, env.router, "guef_other", common.RoleTenant)
	reportID := fileGovernanceReport(t, env, reporterToken)

	w := postMultipart(t, env.router, http.MethodPost,
		fmt.Sprintf("/api/v1/governance/reports/%d/evidence", reportID), otherToken,
		nil,
		[]multipartFile{{
			FieldName: "file",
			Filename:  "x.jpg",
			MimeType:  "image/jpeg",
			Content:   jpegMinimal,
		}})
	assertStatus(t, w, http.StatusForbidden)
}

// TestGovernance_ListEvidence_AsReporter verifies the reporter sees their own
// uploaded evidence.
func TestGovernance_ListEvidence_AsReporter(t *testing.T) {
	env := newPlainEnv(t)
	t.Cleanup(func() { os.RemoveAll("evidence") })

	_, reporterToken := createUserAndLogin(t, env.db, env.router, "gle_reporter", common.RoleTenant)
	reportID := fileGovernanceReport(t, env, reporterToken)

	// Upload one piece of evidence first.
	w := postMultipart(t, env.router, http.MethodPost,
		fmt.Sprintf("/api/v1/governance/reports/%d/evidence", reportID), reporterToken,
		nil,
		[]multipartFile{{
			FieldName: "file",
			Filename:  "ev1.jpg",
			MimeType:  "image/jpeg",
			Content:   jpegMinimal,
		}})
	assertStatus(t, w, http.StatusCreated)

	// List should return at least 1 entry.
	w = makeRequest(t, env.router, http.MethodGet,
		fmt.Sprintf("/api/v1/governance/reports/%d/evidence", reportID), reporterToken, nil)
	assertStatus(t, w, http.StatusOK)

	var resp struct {
		Data []map[string]interface{} `json:"data"`
	}
	parseResponse(t, w, &resp)
	if len(resp.Data) == 0 {
		t.Errorf("expected at least 1 evidence entry, got %d", len(resp.Data))
	}
}

// TestGovernance_ListEvidence_OtherTenantForbidden verifies a different tenant
// cannot list evidence on someone else's report.
func TestGovernance_ListEvidence_OtherTenantForbidden(t *testing.T) {
	env := newPlainEnv(t)
	_, reporterToken := createUserAndLogin(t, env.db, env.router, "glef_rep", common.RoleTenant)
	_, otherToken := createUserAndLogin(t, env.db, env.router, "glef_other", common.RoleTenant)
	reportID := fileGovernanceReport(t, env, reporterToken)

	w := makeRequest(t, env.router, http.MethodGet,
		fmt.Sprintf("/api/v1/governance/reports/%d/evidence", reportID), otherToken, nil)
	assertStatus(t, w, http.StatusForbidden)
}
