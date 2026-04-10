package analytics

import (
	"path/filepath"
	"slices"
	"strconv"

	"propertyops/backend/internal/common"

	"github.com/gin-gonic/gin"
)

// Handler holds Gin handler methods for analytics endpoints.
type Handler struct {
	service     *Service
	propChecker PropertyChecker
}

// NewHandler creates a new analytics Handler.
func NewHandler(service *Service, propChecker PropertyChecker) *Handler {
	return &Handler{service: service, propChecker: propChecker}
}

// --- Metric handlers ---

// GetPopularity handles GET /analytics/popularity
func (h *Handler) GetPopularity(c *gin.Context) {
	filters, ok := h.enforcePMScope(c, bindFilters(c))
	if !ok {
		return
	}
	result, appErr := h.service.GetPopularity(filters)
	if appErr != nil {
		common.RespondError(c, appErr)
		return
	}
	common.Success(c, result)
}

// GetFunnel handles GET /analytics/funnel
func (h *Handler) GetFunnel(c *gin.Context) {
	filters, ok := h.enforcePMScope(c, bindFilters(c))
	if !ok {
		return
	}
	result, appErr := h.service.GetFunnel(filters)
	if appErr != nil {
		common.RespondError(c, appErr)
		return
	}
	common.Success(c, result)
}

// GetRetention handles GET /analytics/retention
func (h *Handler) GetRetention(c *gin.Context) {
	filters, ok := h.enforcePMScope(c, bindFilters(c))
	if !ok {
		return
	}
	result, appErr := h.service.GetRetention(filters)
	if appErr != nil {
		common.RespondError(c, appErr)
		return
	}
	common.Success(c, result)
}

// GetTags handles GET /analytics/tags
func (h *Handler) GetTags(c *gin.Context) {
	filters, ok := h.enforcePMScope(c, bindFilters(c))
	if !ok {
		return
	}
	result, appErr := h.service.GetTagAnalysis(filters)
	if appErr != nil {
		common.RespondError(c, appErr)
		return
	}
	common.Success(c, result)
}

// GetQuality handles GET /analytics/quality
func (h *Handler) GetQuality(c *gin.Context) {
	filters, ok := h.enforcePMScope(c, bindFilters(c))
	if !ok {
		return
	}
	result, appErr := h.service.GetQualityMetrics(filters)
	if appErr != nil {
		common.RespondError(c, appErr)
		return
	}
	common.Success(c, result)
}

// --- Saved report handlers ---

// CreateSavedReport handles POST /analytics/reports/saved
func (h *Handler) CreateSavedReport(c *gin.Context) {
	var req SavedReportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.RespondError(c, common.NewBadRequestError("invalid request body"))
		return
	}

	userID := c.GetUint64(string(common.CtxKeyUserID))
	report, appErr := h.service.CreateSavedReport(req, userID)
	if appErr != nil {
		common.RespondError(c, appErr)
		return
	}

	common.Created(c, ToSavedReportResponse(report))
}

// ListSavedReports handles GET /analytics/reports/saved
func (h *Handler) ListSavedReports(c *gin.Context) {
	page, perPage := common.PaginationFromQuery(c)
	userID := c.GetUint64(string(common.CtxKeyUserID))

	reports, total, appErr := h.service.ListSavedReports(userID, page, perPage)
	if appErr != nil {
		common.RespondError(c, appErr)
		return
	}

	responses := make([]SavedReportResponse, len(reports))
	for i := range reports {
		responses[i] = ToSavedReportResponse(&reports[i])
	}

	common.SuccessWithMeta(c, responses, common.BuildMeta(page, perPage, total))
}

// GetSavedReport handles GET /analytics/reports/saved/:id
func (h *Handler) GetSavedReport(c *gin.Context) {
	id, err := parseIDParam(c, "id")
	if err != nil {
		common.RespondError(c, common.NewBadRequestError("invalid report ID"))
		return
	}

	userID := c.GetUint64(string(common.CtxKeyUserID))
	report, appErr := h.service.GetSavedReport(id, userID)
	if appErr != nil {
		common.RespondError(c, appErr)
		return
	}

	common.Success(c, ToSavedReportResponse(report))
}

// DeleteSavedReport handles DELETE /analytics/reports/saved/:id
func (h *Handler) DeleteSavedReport(c *gin.Context) {
	id, err := parseIDParam(c, "id")
	if err != nil {
		common.RespondError(c, common.NewBadRequestError("invalid report ID"))
		return
	}

	userID := c.GetUint64(string(common.CtxKeyUserID))
	if appErr := h.service.DeleteSavedReport(id, userID); appErr != nil {
		common.RespondError(c, appErr)
		return
	}

	common.Success(c, gin.H{})
}

// GenerateReport handles POST /analytics/reports/generate/:id
func (h *Handler) GenerateReport(c *gin.Context) {
	id, err := parseIDParam(c, "id")
	if err != nil {
		common.RespondError(c, common.NewBadRequestError("invalid report ID"))
		return
	}

	userID := c.GetUint64(string(common.CtxKeyUserID))
	report, appErr := h.service.GenerateReport(id, userID)
	if appErr != nil {
		common.RespondError(c, appErr)
		return
	}

	common.Created(c, ToGeneratedReportResponse(report))
}

// Export handles POST /analytics/export
func (h *Handler) Export(c *gin.Context) {
	var req ExportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.RespondError(c, common.NewBadRequestError("invalid request body"))
		return
	}

	roles := c.GetStringSlice(string(common.CtxKeyRoles))
	isAdmin := slices.Contains(roles, common.RoleSystemAdmin)

	// audit_logs export is restricted to SystemAdmin only — contains cross-property PII.
	if req.Type == "audit_logs" && !isAdmin {
		common.RespondError(c, common.NewForbiddenError("audit_logs export requires SystemAdmin role"))
		return
	}

	// PII-producing exports require an explicit purpose.
	if req.Type == "audit_logs" && len(req.Purpose) < 10 {
		common.RespondError(c, common.NewValidationError("purpose is required (min 10 chars) for PII exports",
			common.FieldError{Field: "purpose", Message: "must be at least 10 characters for PII data export"}))
		return
	}

	if req.Format == "" {
		req.Format = "CSV"
	}

	filters, ok := h.enforcePMScope(c, bindFilters(c))
	if !ok {
		return
	}
	userID := c.GetUint64(string(common.CtxKeyUserID))

	report, appErr := h.service.ExportCSV(req.Type, filters, userID, req.Purpose)
	if appErr != nil {
		common.RespondError(c, appErr)
		return
	}

	common.Created(c, ToGeneratedReportResponse(report))
}

// GetGeneratedReport handles GET /analytics/reports/generated/:id
// If the file exists on disk, it is served inline; otherwise the metadata is returned.
func (h *Handler) GetGeneratedReport(c *gin.Context) {
	id, err := parseIDParam(c, "id")
	if err != nil {
		common.RespondError(c, common.NewBadRequestError("invalid report ID"))
		return
	}

	report, appErr := h.service.GetGeneratedReport(id)
	if appErr != nil {
		common.RespondError(c, appErr)
		return
	}

	// Only the generating user or a SystemAdmin may access the report.
	userID := c.GetUint64(string(common.CtxKeyUserID))
	roles := c.GetStringSlice(string(common.CtxKeyRoles))
	if report.GeneratedBy != userID && !slices.Contains(roles, common.RoleSystemAdmin) {
		common.RespondError(c, common.NewForbiddenError("you do not have access to this report"))
		return
	}

	// Serve the file if the caller requests a download.
	if c.Query("download") == "1" {
		filename := filepath.Base(report.StoragePath)
		c.Header("Content-Disposition", "attachment; filename=\""+filename+"\"")
		c.Header("Content-Type", "text/csv; charset=utf-8")
		c.File(report.StoragePath)
		return
	}

	common.Success(c, ToGeneratedReportResponse(report))
}

// --- helpers ---

// enforcePMScope checks whether the actor is a PM (non-admin) and, if so, verifies that
// a provided property_id is within their managed portfolio.
// If property_id is omitted, PMs are allowed to proceed with unscoped analytics queries.
// Returns the (possibly clamped) filters and true if the caller should proceed.
func (h *Handler) enforcePMScope(c *gin.Context, filters AnalyticsFilters) (AnalyticsFilters, bool) {
	roles := c.GetStringSlice(string(common.CtxKeyRoles))
	isAdmin := slices.Contains(roles, common.RoleSystemAdmin)
	isPM := slices.Contains(roles, common.RolePropertyManager)

	if !isPM || isAdmin {
		return filters, true // SystemAdmin or non-PM: no additional restriction
	}

	// PM with no property_id: allow unscoped query.
	if filters.PropertyID == nil {
		return filters, true
	}

	userID := c.GetUint64(string(common.CtxKeyUserID))
	managedIDs, err := h.propChecker.GetManagedPropertyIDs(userID)
	if err != nil {
		common.RespondError(c, common.NewInternalError("failed to resolve managed properties"))
		return filters, false
	}
	if !slices.Contains(managedIDs, *filters.PropertyID) {
		common.RespondError(c, common.NewForbiddenError("you do not manage the requested property"))
		return filters, false
	}
	return filters, true
}

func bindFilters(c *gin.Context) AnalyticsFilters {
	var f AnalyticsFilters
	_ = c.ShouldBindQuery(&f)
	if v := c.Query("property_id"); v != "" {
		if id, err := strconv.ParseUint(v, 10, 64); err == nil {
			f.PropertyID = &id
		}
	}
	return f
}

func parseIDParam(c *gin.Context, param string) (uint64, error) {
	return strconv.ParseUint(c.Param(param), 10, 64)
}

