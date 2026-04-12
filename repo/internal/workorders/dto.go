package workorders

import (
	"encoding/json"
	"time"
)

// CreateWorkOrderRequest is the payload for creating a new work order.
//
// Submission formats:
//   - application/json — all fields encoded as a standard JSON body; no inline attachments.
//   - multipart/form-data — JSON fields encoded in a "data" form field; optional files in
//     "attachments[]" fields (up to 6 JPEG/PNG files, each ≤ 5 MB).
type CreateWorkOrderRequest struct {
	PropertyID               uint64          `json:"property_id" binding:"required"`
	UnitID                   *uint64         `json:"unit_id,omitempty"`
	Description              string          `json:"description" binding:"required"`
	Priority                 string          `json:"priority" binding:"required"`
	IssueType                string          `json:"issue_type,omitempty"`
	SkillTag                 string          `json:"skill_tag,omitempty"`
	Tags                     json.RawMessage `json:"tags,omitempty"`
	PreferredAccessDate      string          `json:"preferred_access_date,omitempty"`
	PreferredAccessStartTime string          `json:"preferred_access_start_time,omitempty"`
	PreferredAccessEndTime   string          `json:"preferred_access_end_time,omitempty"`
}

// UpdateWorkOrderRequest is the payload for updating a work order.
type UpdateWorkOrderRequest struct {
	Description            *string         `json:"description,omitempty"`
	Priority               *string         `json:"priority,omitempty"`
	IssueType              *string         `json:"issue_type,omitempty"`
	SkillTag               *string         `json:"skill_tag,omitempty"`
	Tags                   json.RawMessage `json:"tags,omitempty"`
	PreferredAccessDate    *string         `json:"preferred_access_date,omitempty"`
	PreferredAccessStartTime *string       `json:"preferred_access_start_time,omitempty"`
	PreferredAccessEndTime *string         `json:"preferred_access_end_time,omitempty"`
}

// TransitionRequest is the payload for transitioning a work order status.
type TransitionRequest struct {
	ToStatus string `json:"to_status" binding:"required"`
	Notes    string `json:"notes,omitempty"`
}

// DispatchRequest is the payload for explicitly dispatching a New work order to a technician.
// Used by PropertyManagers and SystemAdmins to assign a technician when auto-dispatch did not fire.
type DispatchRequest struct {
	TechnicianID uint64 `json:"technician_id" binding:"required"`
	Reason       string `json:"reason,omitempty"`
}

// ReassignRequest is the payload for reassigning a work order to a different technician.
type ReassignRequest struct {
	TechnicianID uint64 `json:"technician_id" binding:"required"`
	Reason       string `json:"reason" binding:"required"`
}

// AddCostItemRequest is the payload for adding a cost item to a work order.
type AddCostItemRequest struct {
	CostType       string  `json:"cost_type" binding:"required"`
	Description    string  `json:"description" binding:"required"`
	Amount         float64 `json:"amount" binding:"required"`
	Responsibility string  `json:"responsibility" binding:"required"`
	Notes          string  `json:"notes,omitempty"`
}

// RateWorkOrderRequest is the payload for rating a completed work order.
type RateWorkOrderRequest struct {
	Rating   int    `json:"rating" binding:"required"`
	Feedback string `json:"feedback,omitempty"`
}

// WorkOrderResponse is the API response DTO for a single work order.
type WorkOrderResponse struct {
	ID                       uint64          `json:"id"`
	UUID                     string          `json:"uuid"`
	PropertyID               uint64          `json:"property_id"`
	UnitID                   *uint64         `json:"unit_id,omitempty"`
	TenantID                 uint64          `json:"tenant_id"`
	AssignedTo               *uint64         `json:"assigned_to,omitempty"`
	Title                    string          `json:"title"`
	Description              string          `json:"description"`
	Priority                 string          `json:"priority"`
	Status                   string          `json:"status"`
	IssueType                string          `json:"issue_type,omitempty"`
	SkillTag                 string          `json:"skill_tag,omitempty"`
	Tags                     json.RawMessage `json:"tags,omitempty"`
	PreferredAccessDate      *time.Time      `json:"preferred_access_date,omitempty"`
	PreferredAccessStartTime string          `json:"preferred_access_start_time,omitempty"`
	PreferredAccessEndTime   string          `json:"preferred_access_end_time,omitempty"`
	SLADueAt                 *time.Time      `json:"sla_due_at,omitempty"`
	SLABreachedAt            *time.Time      `json:"sla_breached_at,omitempty"`
	Rating                   *uint8          `json:"rating,omitempty"`
	Feedback                 *string         `json:"feedback,omitempty"`
	CompletedAt              *time.Time      `json:"completed_at,omitempty"`
	ArchivedAt               *time.Time      `json:"archived_at,omitempty"`
	CreatedAt                time.Time       `json:"created_at"`
	UpdatedAt                time.Time       `json:"updated_at"`
}

// WorkOrderEventResponse is the API response DTO for a work order event.
type WorkOrderEventResponse struct {
	ID          uint64          `json:"id"`
	WorkOrderID uint64          `json:"work_order_id"`
	ActorID     *uint64         `json:"actor_id,omitempty"`
	EventType   string          `json:"event_type"`
	FromStatus  string          `json:"from_status,omitempty"`
	ToStatus    string          `json:"to_status,omitempty"`
	Description string          `json:"description,omitempty"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
}

// CostItemResponse is the API response DTO for a cost item.
type CostItemResponse struct {
	ID             uint64    `json:"id"`
	UUID           string    `json:"uuid"`
	WorkOrderID    uint64    `json:"work_order_id"`
	CostType       string    `json:"cost_type"`
	Description    string    `json:"description"`
	Amount         float64   `json:"amount"`
	Responsibility string    `json:"responsibility"`
	Notes          *string   `json:"notes,omitempty"`
	CreatedBy      uint64    `json:"created_by"`
	CreatedAt      time.Time `json:"created_at"`
}

// WorkOrderListRequest holds filter and pagination parameters for listing work orders.
type WorkOrderListRequest struct {
	PropertyID          *uint64  `form:"property_id"`
	PropertyIDs         []uint64 // set internally for PM scope; not a query param
	ScopedToPropertyIDs bool     // set internally when PM scope is active (empty list = no results)
	TenantID            *uint64  // set internally for tenant scope; not a query param
	Status              string   `form:"status"`
	AssignedTo          *uint64  `form:"assigned_to"`
	Priority            string   `form:"priority"`
	DateFrom            string   `form:"date_from"`
	DateTo              string   `form:"date_to"`
	Page                int      `form:"page"`
	PerPage             int      `form:"per_page"`
}

// ToWorkOrderResponse converts a WorkOrder model to its API response DTO.
func ToWorkOrderResponse(wo *WorkOrder) WorkOrderResponse {
	return WorkOrderResponse{
		ID:                       wo.ID,
		UUID:                     wo.UUID,
		PropertyID:               wo.PropertyID,
		UnitID:                   wo.UnitID,
		TenantID:                 wo.TenantID,
		AssignedTo:               wo.AssignedTo,
		Title:                    wo.Title,
		Description:              wo.Description,
		Priority:                 wo.Priority,
		Status:                   wo.Status,
		IssueType:                wo.IssueType,
		SkillTag:                 wo.SkillTag,
		Tags:                     json.RawMessage(wo.Tags),
		PreferredAccessDate:      wo.PreferredAccessDate,
		PreferredAccessStartTime: wo.PreferredAccessStartTime,
		PreferredAccessEndTime:   wo.PreferredAccessEndTime,
		SLADueAt:                 wo.SLADueAt,
		SLABreachedAt:            wo.SLABreachedAt,
		Rating:                   wo.Rating,
		Feedback:                 wo.Feedback,
		CompletedAt:              wo.CompletedAt,
		ArchivedAt:               wo.ArchivedAt,
		CreatedAt:                wo.CreatedAt,
		UpdatedAt:                wo.UpdatedAt,
	}
}

// ToWorkOrderEventResponse converts a WorkOrderEvent model to its API response DTO.
func ToWorkOrderEventResponse(e *WorkOrderEvent) WorkOrderEventResponse {
	return WorkOrderEventResponse{
		ID:          e.ID,
		WorkOrderID: e.WorkOrderID,
		ActorID:     e.ActorID,
		EventType:   e.EventType,
		FromStatus:  e.FromStatus,
		ToStatus:    e.ToStatus,
		Description: e.Description,
		Metadata:    json.RawMessage(e.Metadata),
		CreatedAt:   e.CreatedAt,
	}
}

// ToCostItemResponse converts a CostItem model to its API response DTO.
func ToCostItemResponse(ci *CostItem) CostItemResponse {
	return CostItemResponse{
		ID:             ci.ID,
		UUID:           ci.UUID,
		WorkOrderID:    ci.WorkOrderID,
		CostType:       ci.CostType,
		Description:    ci.Description,
		Amount:         ci.Amount,
		Responsibility: ci.Responsibility,
		Notes:          ci.Notes,
		CreatedBy:      ci.CreatedBy,
		CreatedAt:      ci.CreatedAt,
	}
}
