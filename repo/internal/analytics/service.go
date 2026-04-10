package analytics

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"propertyops/backend/internal/common"
	"propertyops/backend/internal/config"

	"github.com/google/uuid"
)

// Service provides analytics business logic and report management.
type Service struct {
	repo        *Repository
	storageRoot string
}

// NewService creates a new analytics Service.
// The reports directory is created lazily on first use.
func NewService(repo *Repository, cfg config.StorageConfig) *Service {
	return &Service{
		repo:        repo,
		storageRoot: cfg.Root,
	}
}

// reportsDir returns the absolute path to the reports directory, creating it if needed.
func (s *Service) reportsDir() (string, error) {
	dir := filepath.Join(s.storageRoot, "reports")
	if err := os.MkdirAll(dir, 0750); err != nil {
		return "", fmt.Errorf("failed to create reports directory: %w", err)
	}
	return dir, nil
}

// --- Analytics ---

// GetPopularity returns work-order popularity by issue type.
func (s *Service) GetPopularity(filters AnalyticsFilters) ([]PopularityMetric, *common.AppError) {
	result, err := s.repo.PopularityMetrics(filters)
	if err != nil {
		return nil, common.NewInternalError("failed to fetch popularity metrics")
	}
	return result, nil
}

// GetFunnel returns conversion funnel metrics (work-order lifecycle counts).
func (s *Service) GetFunnel(filters AnalyticsFilters) (*FunnelMetric, *common.AppError) {
	result, err := s.repo.ConversionFunnel(filters)
	if err != nil {
		return nil, common.NewInternalError("failed to fetch funnel metrics")
	}
	return result, nil
}

// GetRetention returns retention metrics for 30- and 90-day windows.
func (s *Service) GetRetention(filters AnalyticsFilters) (*RetentionMetric, *common.AppError) {
	result, err := s.repo.RetentionMetrics(filters)
	if err != nil {
		return nil, common.NewInternalError("failed to fetch retention metrics")
	}
	return result, nil
}

// GetTagAnalysis returns tag usage counts.
func (s *Service) GetTagAnalysis(filters AnalyticsFilters) ([]TagMetric, *common.AppError) {
	result, err := s.repo.TagAnalysis(filters)
	if err != nil {
		return nil, common.NewInternalError("failed to fetch tag analysis")
	}
	return result, nil
}

// GetQualityMetrics returns rating and report quality indicators.
func (s *Service) GetQualityMetrics(filters AnalyticsFilters) (*QualityMetric, *common.AppError) {
	result, err := s.repo.QualityMetrics(filters)
	if err != nil {
		return nil, common.NewInternalError("failed to fetch quality metrics")
	}
	return result, nil
}

// --- Saved reports ---

// validScheduleValues enumerates the schedule strings accepted by the report
// scheduler. The only supported value is "daily"; any other non-empty string
// is rejected to prevent silent no-op scheduled reports.
var validScheduleValues = map[string]bool{
	"":      true, // empty = no schedule (on-demand only)
	"daily": true,
}

// CreateSavedReport persists a new SavedReport.
func (s *Service) CreateSavedReport(req SavedReportRequest, ownerID uint64) (*SavedReport, *common.AppError) {
	if !validScheduleValues[req.Schedule] {
		return nil, common.NewValidationError("schedule must be 'daily' or empty (on-demand only)")
	}

	format := req.OutputFormat
	if format == "" {
		format = "CSV"
	}

	// saved_reports.filters is NOT NULL in MySQL, so persist an empty JSON object
	// when callers omit filters.
	filtersJSON := []byte("{}")
	if req.Filters != nil {
		b, err := json.Marshal(req.Filters)
		if err != nil {
			return nil, common.NewInternalError("failed to encode filters")
		}
		if string(b) != "null" {
			filtersJSON = b
		}
	}

	report := &SavedReport{
		UUID:         uuid.New().String(),
		Name:         req.Name,
		ReportType:   req.ReportType,
		Filters:      filtersJSON,
		OutputFormat: format,
		Schedule:     req.Schedule,
		OwnerID:      ownerID,
		IsActive:     true,
	}

	if err := s.repo.CreateSavedReport(report); err != nil {
		return nil, common.NewInternalError("failed to create saved report")
	}
	return report, nil
}

// GetSavedReport retrieves a SavedReport, enforcing requester ownership.
func (s *Service) GetSavedReport(id, requesterID uint64) (*SavedReport, *common.AppError) {
	report, err := s.repo.FindSavedReport(id)
	if err != nil {
		return nil, common.NewNotFoundError("SavedReport")
	}
	if report.OwnerID != requesterID {
		return nil, common.NewForbiddenError("you do not own this report")
	}
	return report, nil
}

// ListSavedReports returns a paginated list of saved reports owned by ownerID.
func (s *Service) ListSavedReports(ownerID uint64, page, perPage int) ([]SavedReport, int64, *common.AppError) {
	reports, total, err := s.repo.ListSavedReports(ownerID, page, perPage)
	if err != nil {
		return nil, 0, common.NewInternalError("failed to list saved reports")
	}
	return reports, total, nil
}

// DeleteSavedReport removes a saved report after verifying ownership.
func (s *Service) DeleteSavedReport(id, requesterID uint64) *common.AppError {
	report, err := s.repo.FindSavedReport(id)
	if err != nil {
		return common.NewNotFoundError("SavedReport")
	}
	if report.OwnerID != requesterID {
		return common.NewForbiddenError("you do not own this report")
	}
	if err := s.repo.DeleteSavedReport(id); err != nil {
		return common.NewInternalError("failed to delete saved report")
	}
	return nil
}

// GenerateReport runs a saved report and persists the file.
func (s *Service) GenerateReport(savedReportID uint64, generatedBy uint64) (*GeneratedReport, *common.AppError) {
	saved, err := s.repo.FindSavedReport(savedReportID)
	if err != nil {
		return nil, common.NewNotFoundError("SavedReport")
	}

	// Decode filters stored in the saved report.
	var filters AnalyticsFilters
	if len(saved.Filters) > 0 {
		_ = json.Unmarshal(saved.Filters, &filters) // best-effort
	}

	gen, appErr := s.ExportCSV(saved.ReportType, filters, generatedBy, "scheduled-report")
	if appErr != nil {
		return nil, appErr
	}
	gen.SavedReportID = &savedReportID

	// Update last generated timestamp.
	now := time.Now().UTC()
	saved.LastGeneratedAt = &now
	_ = s.repo.UpdateSavedReport(saved)

	// Persist the link between saved and generated report.
	if err := s.repo.UpdateSavedReport(saved); err != nil {
		// Non-fatal — we already have the generated report.
	}

	return gen, nil
}

// ExportCSV runs a query for the given reportType and writes the result to a CSV
// file under storageRoot/reports/<uuid>.csv. Returns the GeneratedReport record.
func (s *Service) ExportCSV(reportType string, filters AnalyticsFilters, generatedBy uint64, purpose string) (*GeneratedReport, *common.AppError) {
	dir, err := s.reportsDir()
	if err != nil {
		return nil, common.NewInternalError("storage not available")
	}

	id := uuid.New().String()
	filename := fmt.Sprintf("%s-%s.csv", reportType, id)
	fullPath := filepath.Join(dir, filename)

	f, err := os.Create(fullPath)
	if err != nil {
		return nil, common.NewInternalError("failed to create report file")
	}
	defer f.Close()

	var count int
	var writeErr error

	switch reportType {
	case "work_orders":
		count, writeErr = WriteWorkOrdersCSV(f, s.repo.db, filters)
	case "payments":
		count, writeErr = WritePaymentsCSV(f, s.repo.db, filters)
	case "audit_logs":
		count, writeErr = WriteAuditLogsCSV(f, s.repo.db, filters)
	default:
		_ = os.Remove(fullPath)
		return nil, common.NewBadRequestError(fmt.Sprintf("unsupported export type: %s", reportType))
	}

	if writeErr != nil {
		_ = os.Remove(fullPath)
		return nil, common.NewInternalError("failed to write CSV")
	}

	info, err := f.Stat()
	if err != nil {
		return nil, common.NewInternalError("failed to stat report file")
	}

	paramsJSON, _ := json.Marshal(map[string]interface{}{
		"filters": filters,
		"purpose": purpose,
	})

	gen := &GeneratedReport{
		UUID:        id,
		ReportType:  reportType,
		Format:      "CSV",
		StoragePath: fullPath,
		FileSize:    uint64(info.Size()),
		RecordCount: count,
		Parameters:  paramsJSON,
		GeneratedBy: generatedBy,
		GeneratedAt: time.Now().UTC(),
	}

	if err := s.repo.CreateGeneratedReport(gen); err != nil {
		return nil, common.NewInternalError("failed to persist generated report record")
	}
	return gen, nil
}

// GetGeneratedReport retrieves a generated report record by ID.
func (s *Service) GetGeneratedReport(id uint64) (*GeneratedReport, *common.AppError) {
	report, err := s.repo.FindGeneratedReport(id)
	if err != nil {
		return nil, common.NewNotFoundError("GeneratedReport")
	}
	return report, nil
}
