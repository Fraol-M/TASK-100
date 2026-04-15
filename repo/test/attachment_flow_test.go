package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"testing"

	"github.com/gin-gonic/gin"

	"propertyops/backend/internal/common"
)

// jpegMinimal is a syntactically minimal JPEG: magic bytes + JFIF APP0 marker header.
// It passes both the magic-byte check (0xFF 0xD8 0xFF) and the declared-MIME check.
var jpegMinimal = []byte{
	0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00, 0x01,
	0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00,
}

// postAttachment builds a multipart/form-data request with a minimal JPEG and
// sends it to POST /work-orders/:id/attachments. Returns the recorder.
func postAttachment(t *testing.T, router *gin.Engine, woID uint64, token string) *httptest.ResponseRecorder {
	t.Helper()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	fh := make(textproto.MIMEHeader)
	fh.Set("Content-Disposition", `form-data; name="file"; filename="photo.jpg"`)
	fh.Set("Content-Type", "image/jpeg")
	fw, err := mw.CreatePart(fh)
	if err != nil {
		t.Fatalf("postAttachment: CreatePart: %v", err)
	}
	if _, err := fw.Write(jpegMinimal); err != nil {
		t.Fatalf("postAttachment: Write: %v", err)
	}
	mw.Close()

	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/work-orders/%d/attachments", woID), &buf)
	if err != nil {
		t.Fatalf("postAttachment: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// createWOForAttach creates a work order via JSON POST and returns its ID.
func createWOForAttach(t *testing.T, router *gin.Engine, token string) uint64 {
	t.Helper()

	body := map[string]interface{}{
		"property_id": 1,
		"description": "The kitchen tap is leaking and needs urgent repair by plumber.",
		"priority":    common.PriorityNormal,
	}
	w := makeRequest(t, router, http.MethodPost, "/api/v1/work-orders", token, body)
	assertStatus(t, w, http.StatusCreated)

	var resp struct {
		Data struct{ ID uint64 `json:"id"` } `json:"data"`
	}
	parseResponse(t, w, &resp)
	if resp.Data.ID == 0 {
		t.Fatal("createWOForAttach: got work order ID=0")
	}
	return resp.Data.ID
}

// TestAttachment_UploadListDownloadDelete covers the full attachment lifecycle
// for a tenant acting on their own work order.
func TestAttachment_UploadListDownloadDelete(t *testing.T) {
	db := setupTestDB(t)
	seedRoles(t, db)
	cfg := testConfig()
	router := newTestRouter(db, cfg)
	// Clean up any attachment files written under the test working directory.
	t.Cleanup(func() { os.RemoveAll("attachments") })

	_, tenantPw := createTestUser(t, db, "att_tenant", common.RoleTenant)
	tenantToken := loginUser(t, router, "att_tenant", tenantPw)

	woID := createWOForAttach(t, router, tenantToken)

	// --- Upload ---
	w := postAttachment(t, router, woID, tenantToken)
	assertStatus(t, w, http.StatusCreated)

	var upResp struct {
		Data struct {
			ID       uint64 `json:"id"`
			MimeType string `json:"mime_type"`
		} `json:"data"`
	}
	parseResponse(t, w, &upResp)
	if upResp.Data.ID == 0 {
		t.Fatal("expected non-zero attachment ID")
	}
	if upResp.Data.MimeType != "image/jpeg" {
		t.Errorf("mime_type: expected image/jpeg, got %q", upResp.Data.MimeType)
	}
	attachID := upResp.Data.ID

	// --- List ---
	w = makeRequest(t, router, http.MethodGet,
		fmt.Sprintf("/api/v1/work-orders/%d/attachments", woID), tenantToken, nil)
	assertStatus(t, w, http.StatusOK)

	var listResp struct {
		Data []struct{ ID uint64 `json:"id"` } `json:"data"`
	}
	parseResponse(t, w, &listResp)
	if len(listResp.Data) != 1 {
		t.Errorf("expected 1 attachment, got %d", len(listResp.Data))
	}

	// --- Download ---
	w = makeRequest(t, router, http.MethodGet,
		fmt.Sprintf("/api/v1/work-orders/attachments/%d", attachID), tenantToken, nil)
	assertStatus(t, w, http.StatusOK)
	if ct := w.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("download Content-Type: expected image/jpeg, got %q", ct)
	}
	if w.Body.Len() == 0 {
		t.Error("download: expected non-empty response body")
	}

	// --- Delete ---
	w = makeRequest(t, router, http.MethodDelete,
		fmt.Sprintf("/api/v1/work-orders/attachments/%d", attachID), tenantToken, nil)
	assertStatus(t, w, http.StatusNoContent)

	// Confirm deletion: list should now be empty.
	w = makeRequest(t, router, http.MethodGet,
		fmt.Sprintf("/api/v1/work-orders/%d/attachments", woID), tenantToken, nil)
	assertStatus(t, w, http.StatusOK)
	var listAfterDel struct {
		Data []json.RawMessage `json:"data"`
	}
	parseResponse(t, w, &listAfterDel)
	if len(listAfterDel.Data) != 0 {
		t.Errorf("expected 0 attachments after delete, got %d", len(listAfterDel.Data))
	}
}

// TestAttachment_CrossUserForbidden verifies that a different tenant cannot
// upload to, list, download, or delete attachments on another tenant's WO.
func TestAttachment_CrossUserForbidden(t *testing.T) {
	db := setupTestDB(t)
	seedRoles(t, db)
	cfg := testConfig()
	router := newTestRouter(db, cfg)
	t.Cleanup(func() { os.RemoveAll("attachments") })

	_, ownerPw := createTestUser(t, db, "att_owner", common.RoleTenant)
	_, otherPw := createTestUser(t, db, "att_other", common.RoleTenant)
	ownerToken := loginUser(t, router, "att_owner", ownerPw)
	otherToken := loginUser(t, router, "att_other", otherPw)

	woID := createWOForAttach(t, router, ownerToken)

	// Owner uploads successfully.
	w := postAttachment(t, router, woID, ownerToken)
	assertStatus(t, w, http.StatusCreated)
	var upResp struct {
		Data struct{ ID uint64 `json:"id"` } `json:"data"`
	}
	parseResponse(t, w, &upResp)
	attachID := upResp.Data.ID

	// Other tenant must be refused on every operation.
	w = postAttachment(t, router, woID, otherToken)
	assertStatus(t, w, http.StatusForbidden)

	w = makeRequest(t, router, http.MethodGet,
		fmt.Sprintf("/api/v1/work-orders/%d/attachments", woID), otherToken, nil)
	assertStatus(t, w, http.StatusForbidden)

	w = makeRequest(t, router, http.MethodGet,
		fmt.Sprintf("/api/v1/work-orders/attachments/%d", attachID), otherToken, nil)
	assertStatus(t, w, http.StatusForbidden)

	w = makeRequest(t, router, http.MethodDelete,
		fmt.Sprintf("/api/v1/work-orders/attachments/%d", attachID), otherToken, nil)
	assertStatus(t, w, http.StatusForbidden)
}

// TestAttachment_UnauthenticatedForbidden verifies that all attachment endpoints
// require a valid session token and return 401 when none is provided.
func TestAttachment_UnauthenticatedForbidden(t *testing.T) {
	db := setupTestDB(t)
	cfg := testConfig()
	router := newTestRouter(db, cfg)

	endpoints := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/work-orders/1/attachments"},
		{http.MethodGet, "/api/v1/work-orders/1/attachments"},
		{http.MethodGet, "/api/v1/work-orders/attachments/1"},
		{http.MethodDelete, "/api/v1/work-orders/attachments/1"},
	}
	for _, ep := range endpoints {
		w := makeRequest(t, router, ep.method, ep.path, "", nil)
		assertStatus(t, w, http.StatusUnauthorized)
	}
}

// TestWorkOrder_CreateWithInlineAttachment tests the multipart/form-data work-order
// create path: JSON metadata in the "data" field + a JPEG in "attachments[]".
// This exercises the inline-attachment integration that Issue 8 covers.
func TestWorkOrder_CreateWithInlineAttachment(t *testing.T) {
	db := setupTestDB(t)
	seedRoles(t, db)
	cfg := testConfig()
	router := newTestRouter(db, cfg)
	t.Cleanup(func() { os.RemoveAll("attachments") })

	_, tenantPw := createTestUser(t, db, "woa_tenant", common.RoleTenant)
	tenantToken := loginUser(t, router, "woa_tenant", tenantPw)

	// Build multipart body: JSON metadata + one JPEG attachment.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	dataField, err := mw.CreateFormField("data")
	if err != nil {
		t.Fatalf("CreateFormField: %v", err)
	}
	meta := `{"property_id":1,"description":"The kitchen tap is leaking and needs urgent repair.","priority":"Normal"}`
	if _, err := dataField.Write([]byte(meta)); err != nil {
		t.Fatalf("write data field: %v", err)
	}

	fh := make(textproto.MIMEHeader)
	fh.Set("Content-Disposition", `form-data; name="attachments[]"; filename="photo.jpg"`)
	fh.Set("Content-Type", "image/jpeg")
	fw, err := mw.CreatePart(fh)
	if err != nil {
		t.Fatalf("CreatePart: %v", err)
	}
	if _, err := fw.Write(jpegMinimal); err != nil {
		t.Fatalf("write file part: %v", err)
	}
	mw.Close()

	req, err := http.NewRequest(http.MethodPost, "/api/v1/work-orders", &buf)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+tenantToken)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assertStatus(t, w, http.StatusCreated)

	var woResp struct {
		Data struct{ ID uint64 `json:"id"` } `json:"data"`
	}
	parseResponse(t, w, &woResp)
	if woResp.Data.ID == 0 {
		t.Fatal("expected non-zero work order ID")
	}

	// The inline attachment must have been created atomically with the work order.
	w = makeRequest(t, router, http.MethodGet,
		fmt.Sprintf("/api/v1/work-orders/%d/attachments", woResp.Data.ID), tenantToken, nil)
	assertStatus(t, w, http.StatusOK)
	var listResp struct {
		Data []struct{ ID uint64 `json:"id"` } `json:"data"`
	}
	parseResponse(t, w, &listResp)
	if len(listResp.Data) != 1 {
		t.Errorf("expected 1 inline attachment, got %d", len(listResp.Data))
	}
}

// TestWorkOrder_CreateTooManyInlineAttachments verifies the preflight count check:
// submitting more than 6 inline files must be rejected before the WO row is created.
func TestWorkOrder_CreateTooManyInlineAttachments(t *testing.T) {
	db := setupTestDB(t)
	seedRoles(t, db)
	cfg := testConfig()
	router := newTestRouter(db, cfg)

	_, tenantPw := createTestUser(t, db, "woa_tenant2", common.RoleTenant)
	tenantToken := loginUser(t, router, "woa_tenant2", tenantPw)

	// Build multipart body with 7 JPEG files (exceeds the 6-file limit).
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	dataField, _ := mw.CreateFormField("data")
	dataField.Write([]byte(`{"property_id":1,"description":"The kitchen tap is leaking and needs urgent repair.","priority":"Normal"}`)) //nolint:errcheck

	for i := 0; i < 7; i++ {
		fh := make(textproto.MIMEHeader)
		fh.Set("Content-Disposition", fmt.Sprintf(`form-data; name="attachments[]"; filename="photo%d.jpg"`, i))
		fh.Set("Content-Type", "image/jpeg")
		part, _ := mw.CreatePart(fh)
		part.Write(jpegMinimal) //nolint:errcheck
	}
	mw.Close()

	req, err := http.NewRequest(http.MethodPost, "/api/v1/work-orders", &buf)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+tenantToken)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	// 422 Unprocessable Entity from NewValidationError.
	assertStatus(t, w, http.StatusUnprocessableEntity)
}
