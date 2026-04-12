# Previous Inspection Issues - Fix Verification (Static)

Date: 2026-04-10
Method: static source review only (no runtime execution)
Last updated: 2026-04-12 (issues 8, 10, 11 resolved)

## Summary
- Verified issues from prior inspection: 11
- Fixed: 11
- Partially fixed / still risky: 0
- Not fixed: 0

## Verification Matrix

| # | Previous issue | Current status | Evidence |
|---|---|---|---|
| 1 | Tenant can create work orders for arbitrary properties/units | Fixed | `repo/internal/workorders/service.go:114`, `repo/internal/workorders/service.go:119`, `repo/internal/workorders/service.go:124` |
| 2 | Backup may silently degrade to metadata-only export | Fixed | `repo/internal/backups/service.go:305`, `repo/internal/backups/service.go:306`, `repo/internal/backups/service.go:308` |
| 3 | Notification retry semantics under-implemented | Fixed | `repo/internal/app/scheduler.go:98`, `repo/internal/app/scheduler.go:118`, `repo/internal/app/scheduler.go:128`, `repo/internal/app/scheduler.go:130` |
| 4 | Analytics tag search-term logic only used `skill_tag` | Fixed | `repo/internal/analytics/repository.go:139`, `repo/internal/analytics/repository.go:140`, `repo/internal/analytics/repository.go:154`, `repo/internal/analytics/repository.go:173` |
| 5 | Manual verification guide had stale backup/health expectations | Fixed | `repo/docs/manual-verification.md:31`, `repo/docs/manual-verification.md:111`, `repo/internal/backups/routes.go:20`, `repo/internal/health/handler.go:39` |
| 6 | Governance integration test category mismatch (`Safety`) | Fixed | No `Safety` usage found in `repo/test/governance_flow_test.go`; valid categories used at `repo/test/governance_flow_test.go:45`, `repo/test/governance_flow_test.go:82`, `repo/test/governance_flow_test.go:104`, `repo/test/governance_flow_test.go:409` |
| 7 | Local-network allowlist depended on `ClientIP` without trusted proxy hardening | Fixed | `repo/internal/app/app.go:50`, `repo/internal/http/middleware.go:310` |
| 8 | Images not integrated into initial work-order submission flow | Fixed | Preflight count check added at `repo/internal/workorders/handler.go:82` (rejects >6 files with 422 before WO row is created); multipart format documented in `repo/internal/workorders/dto.go:9`; end-to-end covered by `repo/test/attachment_flow_test.go:TestWorkOrder_CreateWithInlineAttachment` and `TestWorkOrder_CreateTooManyInlineAttachments` |
| 9 | Manual docs health response mismatch | Fixed | `repo/docs/manual-verification.md:31`, `repo/internal/health/handler.go:39` |
| 10 | No integration coverage for anomaly endpoint | Fixed | `repo/test/anomaly_test.go`: `TestAnomaly_ValidIngestion` (201), `TestAnomaly_MissingDescription` (400), `TestAnomaly_NoAuthRequired` (201 without bearer token), `TestAnomaly_WithMetadata` (201 with optional metadata) |
| 11 | No integration coverage for attachment endpoints | Fixed | `repo/test/attachment_flow_test.go`: `TestAttachment_UploadListDownloadDelete` (full CRUD lifecycle), `TestAttachment_CrossUserForbidden` (403 for non-owner tenant), `TestAttachment_UnauthenticatedForbidden` (401 on all four endpoints) |

## Notes
- The trusted-proxy concern is materially improved because `SetTrustedProxies` is now explicitly configured in app startup (`repo/internal/app/app.go:50`).
- Notification retry now enforces max retries and increments retry counters on delivery update failure in scheduler path.
- Work-order create now includes tenant-property and unit-property validation guardrails.

## Additional observation (new)
- Resolved: the governance test now uses a valid category (`Damage`) at `repo/test/governance_flow_test.go:375`, and `Vandalism` remains only in description text.

## Conclusion
All 11 previously reported defects are now fixed. No remaining material gaps.