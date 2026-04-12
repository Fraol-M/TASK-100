package workorders

import (
	"encoding/json"
	"mime/multipart"
	"strconv"

	"propertyops/backend/internal/common"

	"github.com/gin-gonic/gin"
)

// AttachmentUploader abstracts the file attachment service for inline upload during WO creation.
// To avoid an import cycle, this interface is satisfied by an adapter in app/routes.go.
type AttachmentUploader interface {
	UploadFile(workOrderID uint64, file *multipart.FileHeader, uploaderID uint64, uploaderRoles []string, ip, requestID string) *common.AppError
	// DeleteWorkOrderAttachments removes all previously-uploaded attachments for the given
	// work order. Called during rollback when a later inline file fails validation.
	DeleteWorkOrderAttachments(workOrderID uint64)
}

// Handler holds HTTP handlers for work order endpoints.
type Handler struct {
	service    *Service
	fileUpload AttachmentUploader // optional; enables multipart create with inline attachments
}

// NewHandler creates a new work order Handler.
func NewHandler(service *Service, fileUpload AttachmentUploader) *Handler {
	return &Handler{service: service, fileUpload: fileUpload}
}

// Create handles POST /work-orders.
//
// Accepts two content types:
//   - application/json  — standard JSON body, no inline attachments.
//   - multipart/form-data — metadata JSON in the "data" field, optional files in "attachments[]".
//
// When submitted as multipart, up to 6 files are uploaded atomically with the work order.
// Each file must be a valid JPEG or PNG ≤ 5 MB (enforced by the attachment service).
func (h *Handler) Create(c *gin.Context) {
	userID := c.GetUint64(string(common.CtxKeyUserID))
	roles := c.GetStringSlice(string(common.CtxKeyRoles))
	ip := c.ClientIP()
	requestID, _ := c.Get(string(common.CtxKeyRequestID))
	reqID, _ := requestID.(string)

	// Only tenants can create work orders.
	if !hasRole(roles, common.RoleTenant) {
		common.RespondError(c, common.NewForbiddenError("only tenants can create work orders"))
		return
	}

	var req CreateWorkOrderRequest
	var files []*multipart.FileHeader

	ct := c.ContentType()
	if ct == "multipart/form-data" || (len(ct) > 19 && ct[:19] == "multipart/form-data") {
		// Multipart: metadata lives in the "data" JSON field; files in "attachments[]".
		raw := c.PostForm("data")
		if raw == "" {
			common.RespondError(c, common.NewBadRequestError("multipart: missing 'data' field with JSON metadata"))
			return
		}
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			common.RespondError(c, common.NewBadRequestError("multipart: invalid JSON in 'data' field"))
			return
		}
		form, err := c.MultipartForm()
		if err == nil && form != nil {
			files = form.File["attachments[]"]
		}
	} else {
		if err := c.ShouldBindJSON(&req); err != nil {
			common.RespondError(c, common.NewBadRequestError("invalid request body"))
			return
		}
	}

	// Preflight: reject before creating the work order row so we never need to roll back
	// solely because the client sent too many files.
	if len(files) > 6 {
		common.RespondError(c, common.NewValidationError("Validation failed",
			common.FieldError{Field: "attachments", Message: "maximum 6 attachments allowed"}))
		return
	}

	wo, appErr := h.service.Create(req, userID, ip, reqID)
	if appErr != nil {
		common.RespondError(c, appErr)
		return
	}

	// Process any inline attachment files.
	// Failure is treated as a hard error: the work order is rolled back so the client
	// never receives a partially-created resource with missing attachments.
	if len(files) > 0 && h.fileUpload != nil {
		for _, f := range files {
			if appErr := h.fileUpload.UploadFile(wo.ID, f, userID, roles, ip, reqID); appErr != nil {
				// Roll back atomically: first delete any already-written attachment files/records,
				// then delete the work order row. This ensures no orphaned attachments remain.
				h.fileUpload.DeleteWorkOrderAttachments(wo.ID)
				_ = h.service.Delete(wo.ID)
				common.RespondError(c, common.NewValidationError("attachment upload failed: "+appErr.Message+"; work order was not created"))
				return
			}
		}
	}

	common.Created(c, ToWorkOrderResponse(wo))
}

// GetByID handles GET /work-orders/:id
func (h *Handler) GetByID(c *gin.Context) {
	woID, err := parseIDParam(c, "id")
	if err != nil {
		common.RespondError(c, common.NewBadRequestError("invalid work order ID"))
		return
	}

	wo, appErr := h.service.GetByID(woID)
	if appErr != nil {
		common.RespondError(c, appErr)
		return
	}

	// Object-level authorization.
	if appErr := h.authorizeView(c, wo); appErr != nil {
		common.RespondError(c, appErr)
		return
	}

	common.Success(c, ToWorkOrderResponse(wo))
}

// List handles GET /work-orders
func (h *Handler) List(c *gin.Context) {
	userID := c.GetUint64(string(common.CtxKeyUserID))
	roles := c.GetStringSlice(string(common.CtxKeyRoles))

	var req WorkOrderListRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		common.RespondError(c, common.NewBadRequestError("invalid query parameters"))
		return
	}

	if req.Page == 0 {
		req.Page = 1
	}
	if req.PerPage == 0 {
		req.PerPage = 20
	}

	// Scope based on role.
	if hasRole(roles, common.RoleTenant) && !hasRole(roles, common.RolePropertyManager) && !hasRole(roles, common.RoleSystemAdmin) {
		// Push the tenant filter to the repository so total and results are both scoped.
		tenantID := userID
		req.TenantID = &tenantID
		req.PropertyID = nil
		req.AssignedTo = nil
	}

	if hasRole(roles, common.RoleTechnician) && !hasRole(roles, common.RolePropertyManager) && !hasRole(roles, common.RoleSystemAdmin) {
		// Technicians see only their assigned work orders.
		assignedTo := userID
		req.AssignedTo = &assignedTo
	}

	// PropertyManagers see only work orders for properties they manage.
	// The managed-property check is unconditional — a PM cannot bypass it by supplying
	// an explicit property_id for a property they do not manage.
	if hasRole(roles, common.RolePropertyManager) && !hasRole(roles, common.RoleSystemAdmin) {
		managedIDs, err := h.service.GetManagedPropertyIDs(userID)
		if err != nil {
			common.RespondError(c, common.NewInternalError("failed to resolve managed properties"))
			return
		}
		if req.PropertyID != nil {
			// Verify the requested property is one the PM manages.
			isManagedByPM := false
			for _, id := range managedIDs {
				if id == *req.PropertyID {
					isManagedByPM = true
					break
				}
			}
			if !isManagedByPM {
				common.RespondError(c, common.NewForbiddenError("you do not manage this property"))
				return
			}
			// Single-property filter is valid — keep req.PropertyID, clear the broader set.
		} else {
			req.PropertyIDs = managedIDs
			req.ScopedToPropertyIDs = true // empty list must return zero results, not all records
		}
	}

	orders, total, appErr := h.service.List(req)
	if appErr != nil {
		common.RespondError(c, appErr)
		return
	}

	responses := make([]WorkOrderResponse, len(orders))
	for i := range orders {
		responses[i] = ToWorkOrderResponse(&orders[i])
	}

	meta := common.BuildMeta(req.Page, req.PerPage, total)
	common.SuccessWithMeta(c, responses, meta)
}

// Dispatch handles POST /work-orders/:id/dispatch
// Allows PropertyManagers and SystemAdmins to explicitly assign a New work order
// to a technician when auto-dispatch did not fire (e.g. no skill_tag was provided).
func (h *Handler) Dispatch(c *gin.Context) {
	woID, err := parseIDParam(c, "id")
	if err != nil {
		common.RespondError(c, common.NewBadRequestError("invalid work order ID"))
		return
	}

	var req DispatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.RespondError(c, common.NewBadRequestError("invalid request body"))
		return
	}

	userID := c.GetUint64(string(common.CtxKeyUserID))
	roles := c.GetStringSlice(string(common.CtxKeyRoles))
	ip := c.ClientIP()
	requestID, _ := c.Get(string(common.CtxKeyRequestID))
	reqID, _ := requestID.(string)

	if !hasRole(roles, common.RolePropertyManager) && !hasRole(roles, common.RoleSystemAdmin) {
		common.RespondError(c, common.NewForbiddenError("only property managers or system admins can dispatch work orders"))
		return
	}

	// PM must manage the property of this work order.
	if hasRole(roles, common.RolePropertyManager) && !hasRole(roles, common.RoleSystemAdmin) {
		wo, appErr := h.service.GetByID(woID)
		if appErr != nil {
			common.RespondError(c, appErr)
			return
		}
		managed, err := h.service.IsManagedBy(wo.PropertyID, userID)
		if err != nil || !managed {
			common.RespondError(c, common.NewForbiddenError("you do not manage the property for this work order"))
			return
		}
	}

	wo, appErr := h.service.Dispatch(woID, req, userID, ip, reqID)
	if appErr != nil {
		common.RespondError(c, appErr)
		return
	}

	common.Success(c, ToWorkOrderResponse(wo))
}

// Transition handles POST /work-orders/:id/transition
func (h *Handler) Transition(c *gin.Context) {
	woID, err := parseIDParam(c, "id")
	if err != nil {
		common.RespondError(c, common.NewBadRequestError("invalid work order ID"))
		return
	}

	var req TransitionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.RespondError(c, common.NewBadRequestError("invalid request body"))
		return
	}

	userID := c.GetUint64(string(common.CtxKeyUserID))
	roles := c.GetStringSlice(string(common.CtxKeyRoles))
	ip := c.ClientIP()
	requestID, _ := c.Get(string(common.CtxKeyRequestID))
	reqID, _ := requestID.(string)

	// Check authorization for the transition.
	wo, appErr := h.service.GetByID(woID)
	if appErr != nil {
		common.RespondError(c, appErr)
		return
	}

	if appErr := h.authorizeTransition(c, wo, req.ToStatus, userID, roles); appErr != nil {
		common.RespondError(c, appErr)
		return
	}

	wo, appErr = h.service.Transition(woID, req, userID, ip, reqID)
	if appErr != nil {
		common.RespondError(c, appErr)
		return
	}

	common.Success(c, ToWorkOrderResponse(wo))
}

// Reassign handles POST /work-orders/:id/reassign
func (h *Handler) Reassign(c *gin.Context) {
	woID, err := parseIDParam(c, "id")
	if err != nil {
		common.RespondError(c, common.NewBadRequestError("invalid work order ID"))
		return
	}

	var req ReassignRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.RespondError(c, common.NewBadRequestError("invalid request body"))
		return
	}

	userID := c.GetUint64(string(common.CtxKeyUserID))
	roles := c.GetStringSlice(string(common.CtxKeyRoles))
	ip := c.ClientIP()
	requestID, _ := c.Get(string(common.CtxKeyRequestID))
	reqID, _ := requestID.(string)

	// Only PropertyManagers or SystemAdmins can reassign.
	if !hasRole(roles, common.RolePropertyManager) && !hasRole(roles, common.RoleSystemAdmin) {
		common.RespondError(c, common.NewForbiddenError("only property managers can reassign work orders"))
		return
	}

	// PM must manage the work order's property.
	if hasRole(roles, common.RolePropertyManager) && !hasRole(roles, common.RoleSystemAdmin) {
		existing, appErr := h.service.GetByID(woID)
		if appErr != nil {
			common.RespondError(c, appErr)
			return
		}
		managed, err := h.service.IsManagedBy(existing.PropertyID, userID)
		if err != nil || !managed {
			common.RespondError(c, common.NewForbiddenError("you do not manage the property for this work order"))
			return
		}
	}

	wo, appErr := h.service.Reassign(woID, req, userID, ip, reqID)
	if appErr != nil {
		common.RespondError(c, appErr)
		return
	}

	common.Success(c, ToWorkOrderResponse(wo))
}

// AddCostItem handles POST /work-orders/:id/cost-items
func (h *Handler) AddCostItem(c *gin.Context) {
	woID, err := parseIDParam(c, "id")
	if err != nil {
		common.RespondError(c, common.NewBadRequestError("invalid work order ID"))
		return
	}

	var req AddCostItemRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.RespondError(c, common.NewBadRequestError("invalid request body"))
		return
	}

	userID := c.GetUint64(string(common.CtxKeyUserID))
	roles := c.GetStringSlice(string(common.CtxKeyRoles))
	ip := c.ClientIP()
	requestID, _ := c.Get(string(common.CtxKeyRequestID))
	reqID, _ := requestID.(string)

	// Only PropertyManagers, Technicians, or SystemAdmins can add cost items.
	if !hasRole(roles, common.RolePropertyManager) && !hasRole(roles, common.RoleTechnician) && !hasRole(roles, common.RoleSystemAdmin) {
		common.RespondError(c, common.NewForbiddenError("insufficient permissions to add cost items"))
		return
	}

	// Load the work order once; used for both PM and Technician scope checks.
	wo, appErr := h.service.GetByID(woID)
	if appErr != nil {
		common.RespondError(c, appErr)
		return
	}

	// PropertyManagers may only add cost items to work orders on properties they manage.
	if hasRole(roles, common.RolePropertyManager) && !hasRole(roles, common.RoleSystemAdmin) {
		managed, err := h.service.IsManagedBy(wo.PropertyID, userID)
		if err != nil {
			common.RespondError(c, common.NewInternalError("failed to verify property access"))
			return
		}
		if !managed {
			common.RespondError(c, common.NewForbiddenError("you do not manage the property for this work order"))
			return
		}
	}

	// Technicians can only add cost items to work orders assigned to them.
	if hasRole(roles, common.RoleTechnician) && !hasRole(roles, common.RolePropertyManager) && !hasRole(roles, common.RoleSystemAdmin) {
		if wo.AssignedTo == nil || *wo.AssignedTo != userID {
			common.RespondError(c, common.NewForbiddenError("you can only add cost items to work orders assigned to you"))
			return
		}
	}

	item, appErr := h.service.AddCostItem(woID, req, userID, ip, reqID)
	if appErr != nil {
		common.RespondError(c, appErr)
		return
	}

	common.Created(c, ToCostItemResponse(item))
}

// Rate handles POST /work-orders/:id/rate
func (h *Handler) Rate(c *gin.Context) {
	woID, err := parseIDParam(c, "id")
	if err != nil {
		common.RespondError(c, common.NewBadRequestError("invalid work order ID"))
		return
	}

	var req RateWorkOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.RespondError(c, common.NewBadRequestError("invalid request body"))
		return
	}

	userID := c.GetUint64(string(common.CtxKeyUserID))
	roles := c.GetStringSlice(string(common.CtxKeyRoles))
	ip := c.ClientIP()
	requestID, _ := c.Get(string(common.CtxKeyRequestID))
	reqID, _ := requestID.(string)

	// Only tenants can rate work orders.
	if !hasRole(roles, common.RoleTenant) {
		common.RespondError(c, common.NewForbiddenError("only tenants can rate work orders"))
		return
	}

	wo, appErr := h.service.Rate(woID, req, userID, ip, reqID)
	if appErr != nil {
		common.RespondError(c, appErr)
		return
	}

	common.Success(c, ToWorkOrderResponse(wo))
}

// ListEvents handles GET /work-orders/:id/events
func (h *Handler) ListEvents(c *gin.Context) {
	woID, err := parseIDParam(c, "id")
	if err != nil {
		common.RespondError(c, common.NewBadRequestError("invalid work order ID"))
		return
	}

	// Authorize view on the parent work order.
	wo, appErr := h.service.GetByID(woID)
	if appErr != nil {
		common.RespondError(c, appErr)
		return
	}
	if appErr := h.authorizeView(c, wo); appErr != nil {
		common.RespondError(c, appErr)
		return
	}

	events, appErr := h.service.ListEvents(woID)
	if appErr != nil {
		common.RespondError(c, appErr)
		return
	}

	responses := make([]WorkOrderEventResponse, len(events))
	for i := range events {
		responses[i] = ToWorkOrderEventResponse(&events[i])
	}

	common.Success(c, responses)
}

// ListCostItems handles GET /work-orders/:id/cost-items
func (h *Handler) ListCostItems(c *gin.Context) {
	woID, err := parseIDParam(c, "id")
	if err != nil {
		common.RespondError(c, common.NewBadRequestError("invalid work order ID"))
		return
	}

	// Authorize view on the parent work order.
	wo, appErr := h.service.GetByID(woID)
	if appErr != nil {
		common.RespondError(c, appErr)
		return
	}
	if appErr := h.authorizeView(c, wo); appErr != nil {
		common.RespondError(c, appErr)
		return
	}

	items, appErr := h.service.ListCostItems(woID)
	if appErr != nil {
		common.RespondError(c, appErr)
		return
	}

	responses := make([]CostItemResponse, len(items))
	for i := range items {
		responses[i] = ToCostItemResponse(&items[i])
	}

	common.Success(c, responses)
}

// --- Authorization helpers ---

// authorizeView checks if the current user is allowed to view the work order.
func (h *Handler) authorizeView(c *gin.Context, wo *WorkOrder) *common.AppError {
	userID := c.GetUint64(string(common.CtxKeyUserID))
	roles := c.GetStringSlice(string(common.CtxKeyRoles))

	// SystemAdmin can view everything.
	if hasRole(roles, common.RoleSystemAdmin) {
		return nil
	}

	// PropertyManager can only view work orders on properties they manage.
	if hasRole(roles, common.RolePropertyManager) {
		managed, err := h.service.IsManagedBy(wo.PropertyID, userID)
		if err != nil || !managed {
			return common.NewForbiddenError("you do not manage the property for this work order")
		}
		return nil
	}

	// Tenant can only view own work orders.
	if hasRole(roles, common.RoleTenant) && wo.TenantID == userID {
		return nil
	}

	// Technician can view assigned work orders.
	if hasRole(roles, common.RoleTechnician) && wo.AssignedTo != nil && *wo.AssignedTo == userID {
		return nil
	}

	return common.NewForbiddenError("you do not have access to this work order")
}

// authorizeTransition checks if the current user is allowed to perform the transition.
func (h *Handler) authorizeTransition(c *gin.Context, wo *WorkOrder, toStatus string, userID uint64, roles []string) *common.AppError {
	_ = c // included for consistency; roles/userID already extracted

	// SystemAdmin can do anything.
	if hasRole(roles, common.RoleSystemAdmin) {
		return nil
	}

	// PropertyManager can perform manager-owned transitions only, and only for
	// work orders on properties they manage:
	//   New → Assigned            (dispatch/assignment)
	//   AwaitingApproval → Completed   (approval)
	//   AwaitingApproval → InProgress  (rejection/send-back)
	//   Completed → Archived
	// Technician-only transitions (Assigned→InProgress, InProgress→AwaitingApproval)
	// are explicitly excluded.
	if hasRole(roles, common.RolePropertyManager) {
		managed, err := h.service.IsManagedBy(wo.PropertyID, userID)
		if err != nil || !managed {
			return common.NewForbiddenError("you do not manage the property for this work order")
		}
		pmAllowed := map[string]map[string]bool{
			common.WOStatusNew:              {common.WOStatusAssigned: true},
			common.WOStatusAwaitingApproval: {common.WOStatusCompleted: true, common.WOStatusInProgress: true},
			common.WOStatusCompleted:        {common.WOStatusArchived: true},
		}
		if targets, ok := pmAllowed[wo.Status]; ok && targets[toStatus] {
			return nil
		}
		return common.NewForbiddenError("property managers cannot perform this transition")
	}

	// Technician can transition their own assigned work orders (Assigned->InProgress, InProgress->AwaitingApproval).
	if hasRole(roles, common.RoleTechnician) {
		if wo.AssignedTo == nil || *wo.AssignedTo != userID {
			return common.NewForbiddenError("you can only transition work orders assigned to you")
		}
		if toStatus == common.WOStatusInProgress || toStatus == common.WOStatusAwaitingApproval {
			return nil
		}
		return common.NewForbiddenError("technicians can only move to InProgress or AwaitingApproval")
	}

	return common.NewForbiddenError("insufficient permissions for this transition")
}

// --- Utility helpers ---

func parseIDParam(c *gin.Context, param string) (uint64, error) {
	return strconv.ParseUint(c.Param(param), 10, 64)
}

func hasRole(roles []string, target string) bool {
	for _, r := range roles {
		if r == target {
			return true
		}
	}
	return false
}
