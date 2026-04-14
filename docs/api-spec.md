# PropertyOps — API Specification

Go + Gin REST backend for multifamily property operations. MySQL 8.0 persistence, AES-256-GCM field encryption at rest, bcrypt password hashing, DB-backed opaque sessions, and role-based access control. Offline-first, Docker Compose single-node deployment.

---

## Table of Contents

- [Roles & Permissions](#roles--permissions)
- [Enumerations](#enumerations)
- [Response Envelope](#response-envelope)
- [API Endpoints](#api-endpoints)
  - [AuthController](#1-authcontroller)
  - [UsersController](#2-userscontroller)
  - [TenantsController](#3-tenantscontroller)
  - [PropertiesController](#4-propertiescontroller)
  - [WorkOrdersController](#5-workorderscontroller)
  - [AttachmentsController](#6-attachmentscontroller)
  - [NotificationsController](#7-notificationscontroller)
  - [GovernanceController](#8-governancecontroller)
  - [PaymentsController](#9-paymentscontroller)
  - [AnalyticsController](#10-analyticscontroller)
  - [AdminController](#11-admincontroller)
  - [HealthController](#12-healthcontroller)
- [Domain Policies](#domain-policies)
  - [Work Order State Machine](#work-order-state-machine)
  - [SLA Assignment](#sla-assignment)
  - [Round-Robin Dispatch](#round-robin-dispatch)
  - [Payment Lifecycle](#payment-lifecycle)
  - [Dual-Approval Gate](#dual-approval-gate)
  - [Daily Reconciliation](#daily-reconciliation)
  - [Governance & Enforcement](#governance--enforcement)
- [Encryption Model](#encryption-model)
- [Scheduled Tasks](#scheduled-tasks)
- [Validation Rules](#validation-rules)
- [Configuration Reference](#configuration-reference)

---

## Roles & Permissions

| Role | Description |
|---|---|
| `Tenant` | Resident who files and tracks their own work orders. |
| `Technician` | Field staff assigned to and executing work orders. |
| `PropertyManager` | Manages properties they are actively assigned to; scoped access only. |
| `ComplianceReviewer` | Reviews governance reports, applies enforcement actions. Global read access. |
| `SystemAdmin` | Full system administrator. Inherits all permissions. |

### Permission Matrix

| Permission | Tenant | Technician | PropertyManager | ComplianceReviewer | SystemAdmin |
|---|---|---|---|---|---|
| work_orders:create | Y | — | — | — | Y |
| work_orders:read | Own | Assigned | Managed property | — | Y |
| work_orders:transition | Own→Rating | Tech transitions | PM transitions | — | Y |
| work_orders:dispatch | — | — | Managed property | — | Y |
| work_orders:reassign | — | — | Managed property | — | Y |
| work_orders:cost_items | — | Assigned | Managed property | — | Y |
| attachments:read | Own WO | Assigned WO | Managed property | Y | Y |
| attachments:write | Own WO | Assigned WO | Managed property | — | Y |
| attachments:delete | Own upload | Own upload | Managed property | — | Y |
| payments:read | — | — | Managed property | — | Y |
| payments:write | — | — | Managed property | — | Y |
| payments:reconcile | — | — | — | — | Y |
| governance:report | Y | Y | Y | Y | Y |
| governance:review | — | — | — | Y | Y |
| governance:enforce | — | — | — | Y | Y |
| governance:admin | — | — | — | — | Y |
| notifications:read | Own | Own | Own | Own | Y |
| notifications:send | — | — | Y | — | Y |
| analytics:read | — | — | Managed property | — | Y |
| users:manage | — | — | — | — | Y |
| admin:* | — | — | — | — | Y |

PropertyManager scope is always limited to properties where the PM has an active `PropertyManager` assignment in `property_staff_assignments`. A PM with zero active assignments sees zero results on all scoped endpoints — the empty managed-property list never falls through to return all records.

Role hierarchy: `SystemAdmin` unconditionally satisfies all role checks.

---

## Enumerations

### Work Order Statuses (Lifecycle Order)
`New` | `Assigned` | `InProgress` | `AwaitingApproval` | `Completed` | `Archived`

### Work Order Priorities
`Low` | `Normal` | `High` | `Emergency`

### Cost Item Types
`Labor` | `Material`

### Cost Responsibility
`Tenant` | `Vendor` | `Property`

### Payment Kinds
`Intent` | `SettlementPosting` | `MakeupPosting` | `Reversal`

### Payment Statuses
`Pending` | `Paid` | `Expired` | `Reversed` | `Settled`

### Report Target Types
`Tenant` | `WorkOrder` | `Thread`

### Report Categories
`Harassment` | `Damage` | `Noise` | `Maintenance` | `Fraud` | `Other`

### Report Statuses
`Open` | `InReview` | `Resolved` | `Dismissed`

### Enforcement Action Types
`Warning` | `RateLimit` | `Suspension`

### Unit Statuses
`Vacant` | `Occupied` | `Maintenance`

### Notification Statuses
`Pending` | `Sent` | `Failed` | `Cancelled`

### Audit Action Types
`Create` | `Update` | `Delete` | `StatusChange` | `Login` | `Logout` | `Export` | `Approval` | `Enforcement` | `KeyRotation` | `Backup` | `Restore`

---

## Response Envelope

Every JSON response is wrapped in:

```json
{
  "success": true,
  "data": { "..." },
  "error": null
}
```

Error responses:

```json
{
  "success": false,
  "data": null,
  "error": {
    "code": "VALIDATION_ERROR",
    "message": "Validation failed",
    "errors": [
      { "field": "amount", "message": "must be greater than zero" }
    ]
  }
}
```

Paginated list responses include a `meta` block:

```json
{
  "success": true,
  "data": [ "..." ],
  "meta": {
    "page": 1,
    "per_page": 20,
    "total": 87,
    "total_pages": 5
  }
}
```

### Error Code → HTTP Status Mapping

| Code | HTTP Status | Condition |
|---|---|---|
| `VALIDATION_ERROR` | 422 | Field-level validation failure |
| `NOT_FOUND` | 404 | Resource does not exist |
| `UNAUTHORIZED` | 401 | Missing or invalid session token |
| `FORBIDDEN` | 403 | Authenticated but lacks permission |
| `CONFLICT` | 409 | State conflict (duplicate approval, etc.) |
| `RATE_LIMITED` | 429 | Submission rate limit exceeded |
| `BAD_REQUEST` | 400 | Malformed request body or params |
| `PAYLOAD_TOO_LARGE` | 413 | File exceeds 5 MB limit |
| `INTERNAL_ERROR` | 500 | Unexpected server error |

---

## API Endpoints

All endpoints are prefixed with `/api/v1`. Unless noted, requests and responses use `application/json`. Protected endpoints require a `Bearer <token>` header.

---

### 1. AuthController

Authentication and session management.

#### POST /api/v1/auth/login

| Field | Value |
|---|---|
| Auth | None (public) |
| Body | `{ "username": string, "password": string }` |
| Returns | 200 — `{ "token": string, "expires_at": datetime, "user": UserInfo }` |
| Errors | 401 — invalid credentials (generic message for wrong password, unknown user, deactivated account) |

UserInfo fields: `id`, `uuid`, `username`, `email`, `roles[]`.

Session is a 32-byte cryptographically random opaque token. It expires after 30 minutes of inactivity (sliding window) or 7 days from creation, whichever comes first.

#### POST /api/v1/auth/logout

| Field | Value |
|---|---|
| Auth | Authenticated (session deleted on server) |
| Returns | 200 — `{}` |
| Behavior | Idempotent. Deletes the session row; further requests with the same token return 401. |

#### GET /api/v1/auth/me

| Field | Value |
|---|---|
| Auth | Authenticated |
| Returns | 200 — UserInfo |

---

### 2. UsersController

User account management and role administration.

#### POST /api/v1/users

| Field | Value |
|---|---|
| Auth | SystemAdmin |
| Body | CreateUserRequest (see below) |
| Returns | 201 — UserResponse |

CreateUserRequest:

| Field | Type | Required | Validation |
|---|---|---|---|
| username | string | Y | Unique |
| email | string | Y | Valid email format |
| password | string | Y | Min 8 characters, bcrypt cost ≥ 12 |
| first_name | string | N | |
| last_name | string | N | |
| phone | string | N | |
| role_names | []string | N | Must be valid role names |

#### GET /api/v1/users

| Field | Value |
|---|---|
| Auth | SystemAdmin |
| Params | `page`, `per_page`, `role` (filter), `search` (filter), `active` (bool filter) |
| Returns | 200 — Page\<UserResponse\> |

#### GET /api/v1/users/:id

| Field | Value |
|---|---|
| Auth | Authenticated (self or SystemAdmin) |
| Returns | 200 — UserResponse |
| Errors | 403 if not self and not SystemAdmin |

#### PUT /api/v1/users/:id

| Field | Value |
|---|---|
| Auth | Authenticated (self only or SystemAdmin) |
| Body | UpdateUserRequest: `first_name`, `last_name`, `email`, `phone` (all optional) |
| Returns | 200 — UserResponse |

#### PATCH /api/v1/users/:id/active

| Field | Value |
|---|---|
| Auth | SystemAdmin |
| Body | `{ "is_active": bool }` |
| Returns | 200 — UserResponse |

#### POST /api/v1/users/:id/roles

| Field | Value |
|---|---|
| Auth | SystemAdmin |
| Body | `{ "role_name": string }` |
| Returns | 200 — UserResponse |

#### DELETE /api/v1/users/:id/roles/:role

| Field | Value |
|---|---|
| Auth | SystemAdmin |
| Returns | 200 — UserResponse |

UserResponse includes: `id`, `uuid`, `username`, `email`, `first_name`, `last_name`, `phone` (masked), `is_active`, `roles[]`, `created_at`, `updated_at`.

---

### 3. TenantsController

Tenant profile management linked to User + Unit.

#### POST /api/v1/tenants

| Field | Value |
|---|---|
| Auth | PropertyManager or SystemAdmin |
| Body | CreateTenantProfileRequest (see below) |
| Returns | 201 — TenantProfileResponse |

CreateTenantProfileRequest:

| Field | Type | Required |
|---|---|---|
| user_id | uint64 | Y |
| unit_id | uint64 | N |
| emergency_contact | string | N |
| lease_start | string | N |
| lease_end | string | N |
| move_in_date | string | N |
| notes | string | N |

#### GET /api/v1/tenants/:id

| Field | Value |
|---|---|
| Auth | Authenticated (own profile, PM of property, or SystemAdmin) |
| Returns | 200 — TenantProfileResponse |

#### GET /api/v1/tenants/by-user/:user_id

| Field | Value |
|---|---|
| Auth | Authenticated (object-level: self or PM/Admin) |
| Returns | 200 — TenantProfileResponse |

#### PUT /api/v1/tenants/:id

| Field | Value |
|---|---|
| Auth | Object-level (owner, PM, or SystemAdmin) |
| Body | UpdateTenantProfileRequest: all fields optional |
| Returns | 200 — TenantProfileResponse |

#### GET /api/v1/tenants/by-property/:property_id

| Field | Value |
|---|---|
| Auth | PropertyManager (managed property only) or SystemAdmin |
| Returns | 200 — \[\]TenantProfileResponse |

TenantProfileResponse includes: `id`, `uuid`, `user_id`, `unit_id`, `emergency_contact` (masked by default), `lease_start`, `lease_end`, `move_in_date`, `notes`, `created_at`, `updated_at`.

---

### 4. PropertiesController

Property, unit, and staff management.

#### Property CRUD

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | /api/v1/properties | PM\|Admin | Create property |
| GET | /api/v1/properties | PM\|Admin | List properties |
| GET | /api/v1/properties/:id | PM\|Admin | Get property |
| PUT | /api/v1/properties/:id | PM\|Admin | Update property |

CreatePropertyRequest:

| Field | Type | Required |
|---|---|---|
| name | string | Y |
| address_line1 | string | Y |
| address_line2 | string | N |
| city | string | Y |
| state | string | Y |
| zip_code | string | Y |
| manager_id | uint64 | N |
| timezone | string | N |

#### Unit CRUD

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | /api/v1/properties/:id/units | PM\|Admin | Create unit |
| GET | /api/v1/properties/:id/units | PM\|Admin | List units |
| GET | /api/v1/properties/:id/units/:unit_id | PM\|Admin | Get unit |
| PUT | /api/v1/properties/:id/units/:unit_id | PM\|Admin | Update unit |

CreateUnitRequest: `unit_number` (required), `floor`, `bedrooms`, `bathrooms`, `square_feet`, `status` (all optional).

#### Staff Assignments

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | /api/v1/properties/:id/staff | PM\|Admin | Assign staff member |
| GET | /api/v1/properties/:id/staff | PM\|Admin | List staff assignments |
| DELETE | /api/v1/properties/:id/staff/:user_id/:role | PM\|Admin | Remove staff assignment |

AssignStaffRequest: `user_id` (required), `role` (required — e.g., `PropertyManager` or `Technician`).

#### Technician Skill Tags

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | /api/v1/properties/technicians/:user_id/skills | PM\|Admin | Add skill tag |
| GET | /api/v1/properties/technicians/:user_id/skills | PM\|Admin | List skill tags |
| DELETE | /api/v1/properties/technicians/:user_id/skills/:tag | PM\|Admin | Remove skill tag |

AddSkillTagRequest: `tag` (required). Skill tags drive the round-robin dispatch algorithm.

---

### 5. WorkOrdersController

Full work order lifecycle including creation, state transitions, dispatch, cost items, and ratings.

#### POST /api/v1/work-orders

| Field | Value |
|---|---|
| Auth | Tenant only |
| Rate Limit | DB-backed; configurable max per hour |
| Content-Type | `application/json` or `multipart/form-data` |
| Returns | 201 — WorkOrderResponse |

**JSON body:**

| Field | Type | Required | Validation |
|---|---|---|---|
| property_id | uint64 | Y | |
| unit_id | uint64 | N | |
| description | string | Y | 20–2000 characters |
| priority | string | Y | Low\|Normal\|High\|Emergency |
| issue_type | string | N | |
| skill_tag | string | N | Drives auto-dispatch if provided |
| tags | JSON object | N | |
| preferred_access_date | string | N | |
| preferred_access_start_time | string | N | |
| preferred_access_end_time | string | N | |

**Multipart body:** Metadata JSON in the `data` field; up to 6 files in `attachments[]`. If any file fails validation, the work order and all already-written attachment records are rolled back atomically — the client receives a 422 with no partial resource created.

SLA `sla_due_at` is assigned immediately on creation based on priority (see [SLA Assignment](#sla-assignment)). Auto-dispatch fires if `skill_tag` is provided (see [Round-Robin Dispatch](#round-robin-dispatch)).

#### GET /api/v1/work-orders

| Field | Value |
|---|---|
| Auth | Authenticated (role-scoped) |
| Params | `property_id`, `status`, `priority`, `date_from`, `date_to`, `page`, `per_page` |
| Returns | 200 — Page\<WorkOrderResponse\> |

Scoping rules:
- Tenant: only their own work orders.
- Technician: only work orders assigned to them.
- PropertyManager: only work orders for properties they manage. An empty managed-property list returns zero results.
- SystemAdmin: unrestricted.

#### GET /api/v1/work-orders/:id

| Field | Value |
|---|---|
| Auth | Authenticated (object-level) |
| Returns | 200 — WorkOrderResponse |
| Errors | 403 if actor does not own/manage/have assignment to the work order |

#### POST /api/v1/work-orders/:id/dispatch

| Field | Value |
|---|---|
| Auth | PropertyManager (managed property only) or SystemAdmin |
| Body | `{ "technician_id": uint64, "reason": string (optional) }` |
| Returns | 200 — WorkOrderResponse |
| Behavior | Manually assigns a Technician to a `New` work order. Used when no `skill_tag` was provided at creation, preventing auto-dispatch. Sets status to `Assigned`. |

#### POST /api/v1/work-orders/:id/transition

| Field | Value |
|---|---|
| Auth | Role-specific (see transition table) |
| Body | `{ "to_status": string, "notes": string (optional) }` |
| Returns | 200 — WorkOrderResponse |

Transition authorization (in addition to property/assignment scope):

| Transition | Permitted Roles |
|---|---|
| New → Assigned | PropertyManager, SystemAdmin |
| Assigned → InProgress | Assigned Technician, SystemAdmin |
| InProgress → AwaitingApproval | Assigned Technician, SystemAdmin |
| AwaitingApproval → Completed | PropertyManager (managed property), SystemAdmin |
| AwaitingApproval → InProgress | PropertyManager (managed property), SystemAdmin |
| Completed → Archived | PropertyManager (managed property), SystemAdmin |

Every transition writes an append-only `WorkOrderEvent` record.

#### POST /api/v1/work-orders/:id/reassign

| Field | Value |
|---|---|
| Auth | PropertyManager (managed property only) or SystemAdmin |
| Body | `{ "technician_id": uint64, "reason": string }` |
| Validation | `reason`: 10–500 characters |
| Returns | 200 — WorkOrderResponse |

#### POST /api/v1/work-orders/:id/cost-items

| Field | Value |
|---|---|
| Auth | PropertyManager (managed), Technician (assigned), or SystemAdmin |
| Body | AddCostItemRequest (see below) |
| Returns | 201 — CostItemResponse |

AddCostItemRequest:

| Field | Type | Required | Validation |
|---|---|---|---|
| cost_type | string | Y | Labor\|Material |
| description | string | Y | |
| amount | float64 | Y | DECIMAL(12,2), > 0 |
| responsibility | string | Y | Tenant\|Vendor\|Property |
| notes | string | N | |

#### GET /api/v1/work-orders/:id/cost-items

| Field | Value |
|---|---|
| Auth | Authenticated (object-level as per WO access) |
| Returns | 200 — \[\]CostItemResponse |

#### POST /api/v1/work-orders/:id/rate

| Field | Value |
|---|---|
| Auth | Tenant only (must be WO owner, WO must be Completed) |
| Body | `{ "rating": int, "feedback": string (optional) }` |
| Validation | `rating`: 1–5; `feedback`: ≤ 1000 characters |
| Returns | 200 — WorkOrderResponse |

#### GET /api/v1/work-orders/:id/events

| Field | Value |
|---|---|
| Auth | Authenticated (object-level as per WO access) |
| Returns | 200 — \[\]WorkOrderEventResponse |

WorkOrderEventResponse includes: `id`, `work_order_id`, `actor_id`, `event_type`, `from_status`, `to_status`, `description`, `metadata`, `created_at`.

---

### 6. AttachmentsController

Mounted under `/api/v1/work-orders`.

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | /api/v1/work-orders/:id/attachments | Object-level | Upload JPEG/PNG to work order |
| GET | /api/v1/work-orders/:id/attachments | Object-level | List work order attachments |
| GET | /api/v1/work-orders/attachments/:id | Object-level | Download attachment (serves file) |
| DELETE | /api/v1/work-orders/attachments/:id | Object-level | Delete attachment |

**Upload validation:**
- MIME types: `image/jpeg`, `image/png` only.
- File size: ≤ 5 MB.
- Max 6 attachments per work order.
- Magic-byte validation: declared `Content-Type` must match detected file signature.
- SHA-256 hash computed and stored for integrity verification.

**Access control** (uniform across upload, list, download, delete):
- SystemAdmin: unconditional.
- ComplianceReviewer: unconditional read access (for evidence review).
- PropertyManager: must manage the work order's property.
- Technician: must be the assigned technician.
- Tenant: must be the work order creator.

AttachmentResponse includes: `id`, `uuid`, `entity_type`, `entity_id`, `filename`, `mime_type`, `file_size`, `sha256_hash`, `uploaded_by`, `created_at`.

---

### 7. NotificationsController

In-app notification center and message threads.

#### Notifications

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | /api/v1/notifications | Authenticated | List own notifications (paginated) |
| GET | /api/v1/notifications/unread-count | Authenticated | Get unread notification count |
| GET | /api/v1/notifications/:id | Authenticated (own only) | Get single notification |
| PATCH | /api/v1/notifications/:id/read | Authenticated (own only) | Mark notification as read |
| POST | /api/v1/notifications/send | PM or SystemAdmin | Send notification to a user |

GET /api/v1/notifications query params: `status`, `category`, `read` (`true`/`false`), `page`, `per_page`.

POST /api/v1/notifications/send body:

| Field | Type | Required |
|---|---|---|
| recipient_id | uint64 | Y |
| subject | string | Y |
| body | string | Y |
| category | string | N |
| scheduled_for | datetime | N |

NotificationResponse includes: `id`, `uuid`, `recipient_id`, `template_id`, `subject`, `body`, `category`, `status`, `is_read`, `scheduled_for`, `sent_at`, `created_at`.

#### Message Threads

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | /api/v1/notifications/threads | Authenticated | Create thread |
| GET | /api/v1/notifications/threads | Authenticated | List own threads (paginated) |
| GET | /api/v1/notifications/threads/:id | Participant | Get thread |
| POST | /api/v1/notifications/threads/:id/participants | Participant | Add participant |
| GET | /api/v1/notifications/threads/:id/participants | Participant | List participants |
| POST | /api/v1/notifications/threads/:id/messages | Participant | Post message |
| GET | /api/v1/notifications/threads/:id/messages | Participant | List messages (paginated) |

POST /api/v1/notifications/threads body:

| Field | Type | Required | Notes |
|---|---|---|---|
| work_order_id | uint64 | N | If provided, WO access is enforced at service layer |
| subject | string | Y | |

When `work_order_id` is provided, WO access rules mirror the attachment service: PM must manage the property; Technician must be assigned; Tenant must be the creator. SystemAdmin and ComplianceReviewer have unconditional access.

---

### 8. GovernanceController

Reports, enforcement actions, keyword moderation, and risk rules.

#### Reports

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | /api/v1/governance/reports | All authenticated (rate-limited) | File a report |
| GET | /api/v1/governance/reports | ComplianceReviewer\|Admin | List all reports |
| GET | /api/v1/governance/reports/:id | ComplianceReviewer\|Admin | Get report |
| PATCH | /api/v1/governance/reports/:id/review | ComplianceReviewer\|Admin | Update report status |
| POST | /api/v1/governance/reports/:id/evidence | Reporter\|Reviewer\|Admin | Upload evidence attachment |
| GET | /api/v1/governance/reports/:id/evidence | ComplianceReviewer\|Admin | List evidence |

POST /api/v1/governance/reports body:

| Field | Type | Required | Validation |
|---|---|---|---|
| target_type | string | Y | Tenant\|WorkOrder\|Thread |
| target_id | uint64 | Y | |
| category | string | Y | Harassment\|Damage\|Noise\|Maintenance\|Fraud\|Other |
| description | string | Y | 20–2000 characters |

PATCH /api/v1/governance/reports/:id/review body: `{ "status": Open|InReview|Resolved|Dismissed, "resolution_notes": string (optional) }`.

#### Enforcement Actions

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | /api/v1/governance/enforcements | ComplianceReviewer\|Admin | Apply enforcement |
| GET | /api/v1/governance/enforcements | ComplianceReviewer\|Admin | List enforcements |
| POST | /api/v1/governance/enforcements/:id/revoke | ComplianceReviewer\|Admin | Revoke enforcement |

POST /api/v1/governance/enforcements body:

| Field | Type | Required | Notes |
|---|---|---|---|
| user_id | uint64 | Y | |
| action_type | string | Y | Warning\|RateLimit\|Suspension |
| reason | string | Y | |
| ends_at | string | N | For Suspension: `1day`, `7day`, or `indefinite` |
| rate_limit_max | int | N | For RateLimit actions |
| rate_limit_window_minutes | int | N | For RateLimit actions |
| report_id | uint64 | N | Link to originating report |

POST /api/v1/governance/enforcements/:id/revoke body: `{ "reason": string }`.

Active suspensions block all authenticated requests (checked in middleware). Suspended users receive 403 with a suspension message.

#### Keywords (SystemAdmin Only)

| Method | Path | Description |
|---|---|---|
| POST | /api/v1/governance/keywords | Create keyword |
| GET | /api/v1/governance/keywords | List keywords |
| DELETE | /api/v1/governance/keywords/:id | Delete keyword |

POST body: `{ "keyword": string, "category": string (optional), "severity": string (optional) }`.

#### Risk Rules (SystemAdmin Only)

| Method | Path | Description |
|---|---|---|
| POST | /api/v1/governance/risk-rules | Create risk rule |
| GET | /api/v1/governance/risk-rules | List risk rules |
| GET | /api/v1/governance/risk-rules/:id | Get risk rule |
| PUT | /api/v1/governance/risk-rules/:id | Update risk rule |
| DELETE | /api/v1/governance/risk-rules/:id | Delete risk rule |

POST body: `{ "name": string, "description": string, "condition_type": string, "condition_params": JSON, "action_type": string, "action_params": JSON (optional) }`.

---

### 9. PaymentsController

Offline payment intents, settlements, reversals, dual-approval, and reconciliation.

#### Intents and Postings

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | /api/v1/payments/intents | PM\|Admin (scoped) | Create payment intent |
| GET | /api/v1/payments | PM\|Admin (property-scoped) | List payments |
| GET | /api/v1/payments/:id | PM\|Admin (property-scoped) | Get payment |
| POST | /api/v1/payments/:id/mark-paid | PM\|Admin (scoped) | Mark intent as paid |
| POST | /api/v1/payments/:id/approve | PM\|Admin (scoped) | Approve payment |
| POST | /api/v1/payments/:id/reverse | PM\|Admin (scoped) | Create reversal |
| POST | /api/v1/payments/:id/makeup | PM\|Admin (scoped) | Create makeup posting |
| POST | /api/v1/payments/settlements | PM\|Admin (scoped) | Create settlement posting |

POST /api/v1/payments/intents body:

| Field | Type | Required | Validation |
|---|---|---|---|
| property_id | uint64 | Y | PM must manage this property |
| amount | float64 | Y | DECIMAL(12,2), > 0 |
| work_order_id | uint64 | N | |
| tenant_id | uint64 | N | |
| unit_id | uint64 | N | |
| description | string | N | |

Intent expires in 30 minutes by default (configurable). Background job marks expired intents as `Expired`.

POST /api/v1/payments/:id/approve body: `{ "notes": string (optional) }`.

Dual-approval rules (for `amount > threshold`, default $500):
- Two distinct approvers required.
- Same approver cannot approve twice — returns 409.
- On second approval, payment status transitions to `Settled`.
- Reversal and makeup operations on amounts above threshold require prior dual approval.

#### Reconciliation

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | /api/v1/payments/reconciliation/run | SystemAdmin | Run daily reconciliation |
| GET | /api/v1/payments/reconciliation | SystemAdmin | List reconciliation runs |
| GET | /api/v1/payments/reconciliation/:id | SystemAdmin | Get reconciliation run |

POST /api/v1/payments/reconciliation/run body: `{ "run_date": "YYYY-MM-DD" }`.

Reconciliation queries settlements, makeups, and reversals for the date. Expected total counts intents with status `Paid` or `Settled`. Discrepancies trigger flagging. Generates a local CSV statement file.

PaymentResponse includes: `id`, `uuid`, `work_order_id`, `tenant_id`, `unit_id`, `property_id`, `kind`, `amount`, `currency`, `status`, `description`, `expires_at`, `paid_at`, `paid_by`, `reversed_at`, `reversal_reason`, `related_payment_id`, `created_by`, `created_at`, `updated_at`.

---

### 10. AnalyticsController

Aggregated metrics, saved reports, and data export. Access requires PropertyManager (managed property, PM must supply `property_id`) or SystemAdmin.

#### Metrics

| Method | Path | Description |
|---|---|---|
| GET | /api/v1/analytics/popularity | Issue type frequency ranking |
| GET | /api/v1/analytics/funnel | Work order conversion funnel |
| GET | /api/v1/analytics/retention | Repeat request rate by unit (30d/90d) |
| GET | /api/v1/analytics/tags | Tag usage counts |
| GET | /api/v1/analytics/quality | Rating averages, negative rate, report rate |

Common query params: `property_id`, `from` (YYYY-MM-DD), `to` (YYYY-MM-DD), `period`.

Popularity metric: `{ "issue_type": string, "count": int64 }`.

Funnel metric: `{ "new": int64, "assigned": int64, "in_progress": int64, "completed": int64, "total": int64 }`.

Retention metric: `{ "unique_units_30d": int64, "repeat_units_30d": int64, "repeat_rate_30d": float64, "unique_units_90d": int64, "repeat_units_90d": int64, "repeat_rate_90d": float64 }`.

Quality metric: `{ "total_rated": int64, "average_rating": float64, "negative_count": int64, "negative_rate": float64, "reported_count": int64 }`.

#### Reports

| Method | Path | Description |
|---|---|---|
| POST | /api/v1/analytics/reports/saved | Create saved report definition |
| GET | /api/v1/analytics/reports/saved | List saved reports |
| GET | /api/v1/analytics/reports/saved/:id | Get saved report |
| DELETE | /api/v1/analytics/reports/saved/:id | Delete saved report |
| POST | /api/v1/analytics/reports/generate/:id | Trigger report generation |
| GET | /api/v1/analytics/reports/generated/:id | Get generated report metadata |

#### Export

| Method | Path | Description |
|---|---|---|
| POST | /api/v1/analytics/export | Ad-hoc data export (CSV) |

POST body: `{ "type": "work_orders"|"payments"|"audit_logs", "format": "csv" (optional), "purpose": string (required) }`.

Exporting `audit_logs` requires SystemAdmin. For other types, PropertyManager must provide a `property_id` they manage, or they receive 403.

---

### 11. AdminController

System administration endpoints. All require SystemAdmin unless noted.

#### Audit Logs

| Method | Path | Description |
|---|---|---|
| GET | /api/v1/admin/audit-logs | List audit log entries (paginated) |
| GET | /api/v1/admin/audit-logs/:id | Get audit log entry |

Query params: `resource_type`, `resource_id`, `actor_id`, `action`, `from`, `to`, `page`, `per_page`.

#### Structured Logs

| Method | Path | Description |
|---|---|---|
| GET | /api/v1/admin/logs | Query structured application logs |

#### Backups

| Method | Path | Description |
|---|---|---|
| POST | /api/v1/admin/backups | Trigger immediate backup |
| GET | /api/v1/admin/backups | List backup files |
| POST | /api/v1/admin/backups/validate | Validate backup file integrity |
| DELETE | /api/v1/admin/backups/retention | Apply retention policy (deletes expired backups) |

Backups use `mysqldump` + AES-256-GCM encryption (when `BACKUP_ENCRYPTION_ENABLED=true`). Files stored in `BACKUP_ROOT`. Scheduled daily via cron (`BACKUP_SCHEDULE_CRON`, default `0 2 * * *`).

Retention: financial records — 7 years; message records — 2 years.

#### Encryption Key Management

| Method | Path | Description |
|---|---|---|
| GET | /api/v1/admin/keys | List encryption key versions and rotation status |
| POST | /api/v1/admin/keys/rotate | Rotate to a new encryption key version |

Key rotation due date is computed from `ENCRYPTION_ROTATION_DAYS` (default 180). Response includes `active_key_id` and `rotation_due` timestamp.

#### System Settings

| Method | Path | Description |
|---|---|---|
| GET | /api/v1/admin/settings | List system settings |
| PUT | /api/v1/admin/settings/:key | Update a setting value |

#### PII Export

| Method | Path | Description |
|---|---|---|
| POST | /api/v1/admin/pii-export | Export PII data |

Body: `{ "type": string, "format": "csv", "purpose": string }`. `purpose` is required and is recorded in the audit log. The export is fully audit-logged under action `Export`.

#### Data Retention

| Method | Path | Description |
|---|---|---|
| DELETE | /api/v1/admin/data-retention | Run retention enforcement (permanent deletion of expired records) |

#### Anomaly Ingestion

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | /api/v1/admin/anomaly | None (local network only) | Ingest an anomaly event |

No bearer token required. Caller IP is validated against `ANOMALY_ALLOWED_CIDRS` (default: RFC-1918 + loopback). Requests from non-allowed CIDRs receive 403. Ingested anomalies are recorded and audit-logged.

---

### 12. HealthController

Public endpoints — no authentication required.

#### GET /health/live

Returns 200 if the process is running.

```json
{ "status": "ok" }
```

#### GET /health/ready

Returns 200 only when all subsystems are operational.

```json
{
  "status": "ok",
  "checks": {
    "db":         { "status": "ok" },
    "storage":    { "status": "ok" },
    "keys":       { "status": "ok" },
    "backup_dir": { "status": "ok" }
  }
}
```

Returns 503 if any check fails.

---

## Domain Policies

### Work Order State Machine

```
New ──→ Assigned ──→ InProgress ──→ AwaitingApproval ──→ Completed ──→ Archived
```

Every allowed transition and its authorized roles:

| From | To | Who |
|---|---|---|
| New | Assigned | PropertyManager (managed property), SystemAdmin |
| Assigned | InProgress | Assigned Technician, SystemAdmin |
| InProgress | AwaitingApproval | Assigned Technician, SystemAdmin |
| AwaitingApproval | Completed | PropertyManager (managed property), SystemAdmin |
| AwaitingApproval | InProgress | PropertyManager (managed property), SystemAdmin |
| Completed | Archived | PropertyManager (managed property), SystemAdmin |

All other transitions are rejected with 422. Every transition creates an immutable `work_order_events` row.

### SLA Assignment

SLA `sla_due_at` is set at creation time based on `priority`:

| Priority | SLA |
|---|---|
| Emergency | 4 hours |
| High | 24 hours |
| Normal | 72 hours |
| Low | 5 business days |

A background scheduler polls for work orders where `sla_due_at < now` and `sla_breached_at IS NULL`, sets `sla_breached_at`, creates a `sla_breached` event, and sends in-app notifications to the tenant and assigned technician.

### Round-Robin Dispatch

When a work order is created with a `skill_tag`, the system auto-assigns a technician using deterministic round-robin per `(property_id, skill_tag)`:

1. Load all technicians assigned to the property with a matching skill tag.
2. Read the current cursor position from `dispatch_cursors`.
3. Pick the technician at `cursor % len(technicians)`.
4. Increment and persist the cursor (upsert).
5. Set `assigned_to` and transition status to `Assigned`.

If no technicians are available or no `skill_tag` is provided, the work order remains in `New` status until a PropertyManager dispatches manually via `POST /:id/dispatch`.

### Payment Lifecycle

```
[Intent → Pending]
     ↓ mark-paid
[Intent → Paid]
     ↓ approve (×1 for amount ≤ threshold, ×2 for amount > threshold)
[Intent → Settled]
     ↓ (reversal requires dual approval if amount > threshold)
[Intent → Reversed]

[Intent → Paid] ──→ [SettlementPosting → Settled]
                ──→ [MakeupPosting → Paid]
[Intent → Pending] ──→ [Intent → Expired] (background job, 30 min)
```

### Dual-Approval Gate

Applies when `payment.amount > PAYMENT_DUAL_APPROVAL_THRESHOLD` (default $500.00):

- First approval creates `PaymentApproval` with `approval_order = 1`.
- Second approval (different user) creates `approval_order = 2` and transitions payment to `Settled`.
- Same approver twice → 409 Conflict.
- Reversal or makeup before two approvals → 422.
- For amounts ≤ threshold, one approval immediately settles.

### Daily Reconciliation

`POST /api/v1/payments/reconciliation/run` for a given date:
1. Queries `SettlementPosting`, `MakeupPosting`, `Reversal` payments.
2. Computes: `totalActual = settlements + makeups − reversals`.
3. Queries `Intent` payments with status `Paid` or `Settled` (both included to account for the approval transition).
4. Computes `totalExpected` from step 3.
5. If `|expected − actual| > $0.01`, creates a discrepancy entry.
6. Flags `Reversal` records without a `related_payment_id`.
7. Generates a local CSV statement at `STORAGE_ROOT/reconciliation/YYYY-MM-DD.csv`.
8. Saves `ReconciliationRun` with totals, discrepancy count, and file path.

### Governance & Enforcement

Report flow:
1. Any authenticated user files a report (rate-limited).
2. ComplianceReviewer sets status to `InReview`, then `Resolved` or `Dismissed`.
3. ComplianceReviewer may apply an enforcement action linked to the report.

Enforcement effects:
- `Warning`: logged only; no functional restriction.
- `RateLimit`: enforced by the DB-backed rate limiter against the user's account.
- `Suspension`: middleware blocks all authenticated requests for the user; returns 403.

Suspension ends at `ends_at` (if set). ComplianceReviewer or SystemAdmin may revoke any active enforcement early.

---

## Encryption Model

### At-Rest Field Encryption

| Property | Value |
|---|---|
| Algorithm | AES-256-GCM |
| IV Length | 96-bit (12 bytes), random per field value |
| Auth Tag | 128-bit |
| Key Storage | Per-key files in `ENCRYPTION_KEY_DIR` |
| Active Key | Controlled by `ENCRYPTION_ACTIVE_KEY_ID` |
| Storage Format | Base64-encoded (IV prepended to ciphertext) |
| Rotation Period | 180 days (`ENCRYPTION_ROTATION_DAYS`) |

Encrypted fields: sensitive PII (emergency contacts, phone numbers). The active key ID is versioned; rotation creates a new key version without immediately re-encrypting existing data.

### Backup Encryption

When `BACKUP_ENCRYPTION_ENABLED=true`, mysqldump output is encrypted with AES-256-GCM using the active key before writing to `BACKUP_ROOT`. The `.enc` extension marks encrypted files. Restoration requires the corresponding key version.

---

## Scheduled Tasks

| Task | Default Schedule | Description |
|---|---|---|
| SLA breach check | Continuous (5-min poll) | Sets `sla_breached_at` on overdue work orders; sends notifications |
| Payment intent expiry | Continuous (background) | Marks `Pending` intents past `expires_at` as `Expired` |
| Notification delivery | 60-second poll | Retries `Pending` and `Failed` notifications; max 3 retries |
| Daily backup | `0 2 * * *` (2 AM) | Encrypted `mysqldump` of the database |
| Report generation | 300-second poll | Executes scheduled saved reports |

---

## Validation Rules

| Rule | Value |
|---|---|
| Work order description | 20–2000 characters |
| Reassignment reason | 10–500 characters |
| Rating | 1–5 (integer) |
| Feedback | ≤ 1000 characters |
| Password minimum | 8 characters |
| Bcrypt cost | ≥ 12 |
| Session idle timeout | 30 minutes (configurable) |
| Session max lifetime | 7 days (configurable) |
| Attachment MIME types | `image/jpeg`, `image/png` only |
| Attachment max size | 5 MB |
| Attachments per WO | ≤ 6 |
| Money amounts | DECIMAL(12,2), must be > 0 |
| Payment dual-approval threshold | $500.00 (configurable) |
| Payment intent expiry | 30 minutes (configurable) |
| Pagination default | page=1, per_page=20 |
| Pagination maximum | per_page=100 |
| Governance report description | 20–2000 characters |
| Report categories | Harassment\|Damage\|Noise\|Maintenance\|Fraud\|Other (enumerated) |
| Encryption key rotation | 180 days (configurable) |
| Financial retention | 7 years |
| Message retention | 2 years |

---

## Configuration Reference

All configuration is loaded from environment variables at startup. The process exits if required values are absent.

| Property | Env Variable | Default | Required |
|---|---|---|---|
| Server port | `SERVER_PORT` | 8080 | — |
| Gin mode | `GIN_MODE` | `release` | — |
| DB host | `DB_HOST` | `localhost` | — |
| DB port | `DB_PORT` | 3306 | — |
| DB user | `DB_USER` | `propertyops` | — |
| DB password | `DB_PASSWORD` | — | **Yes** |
| DB name | `DB_NAME` | `propertyops` | — |
| DB max open connections | `DB_MAX_OPEN_CONNS` | 25 | — |
| DB max idle connections | `DB_MAX_IDLE_CONNS` | 10 | — |
| DB connection lifetime | `DB_CONN_MAX_LIFETIME_MINUTES` | 30 | — |
| Bcrypt cost | `BCRYPT_COST` | 12 (min: 12) | — |
| Session idle timeout | `SESSION_IDLE_TIMEOUT_MINUTES` | 30 | — |
| Session max lifetime | `SESSION_MAX_LIFETIME_HOURS` | 168 (7 days) | — |
| Encryption key dir | `ENCRYPTION_KEY_DIR` | `/run/propertyops/keys` | **Yes** |
| Active key ID | `ENCRYPTION_ACTIVE_KEY_ID` | 1 (min: 1) | — |
| Key rotation days | `ENCRYPTION_ROTATION_DAYS` | 180 | — |
| Storage root | `STORAGE_ROOT` | `/var/lib/propertyops/storage` | **Yes** |
| Backup root | `BACKUP_ROOT` | `/var/lib/propertyops/backups` | — |
| Log root | `LOG_ROOT` | `/var/log/propertyops` | — |
| Backup cron schedule | `BACKUP_SCHEDULE_CRON` | `0 2 * * *` | — |
| Backup retention days | `BACKUP_RETENTION_DAYS` | 30 | — |
| Backup encryption enabled | `BACKUP_ENCRYPTION_ENABLED` | `true` | — |
| Financial retention years | `FINANCIAL_RETENTION_YEARS` | 7 | — |
| Message retention years | `MESSAGE_RETENTION_YEARS` | 2 | — |
| Rate limit (max/hour) | `RATE_LIMIT_MAX_SUBMISSIONS_PER_HOUR` | 10 | — |
| Notification poll interval | `NOTIFICATION_POLL_INTERVAL_SECONDS` | 60 | — |
| Notification max retries | `NOTIFICATION_MAX_RETRIES` | 3 | — |
| Notification retry delay | `NOTIFICATION_RETRY_DELAY_SECONDS` | 300 | — |
| Payment intent expiry | `PAYMENT_INTENT_EXPIRY_MINUTES` | 30 | — |
| Dual-approval threshold | `PAYMENT_DUAL_APPROVAL_THRESHOLD` | 500.00 | — |
| Report schedule poll | `REPORT_SCHEDULE_POLL_INTERVAL_SECONDS` | 300 | — |
| Anomaly allowed CIDRs | `ANOMALY_ALLOWED_CIDRS` | RFC-1918 + loopback | — |
| Default timezone | `DEFAULT_TIMEZONE` | `America/New_York` | — |
| Admin bootstrap password | `ADMIN_BOOTSTRAP_PASSWORD` | — | Seed only |
