package analytics

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"propertyops/backend/internal/workorders"
)

// setupAnalyticsTestDB opens a private in-memory SQLite DB and migrates the
// tables needed by analytics repository queries.
func setupAnalyticsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?mode=memory&cache=private"), &gorm.Config{
		Logger: logger.Discard,
	})
	if err != nil {
		t.Skipf("SQLite unavailable (CGO disabled or no C compiler): %v", err)
	}
	if err := db.AutoMigrate(
		&workorders.WorkOrder{},
		&SavedReport{},
		&GeneratedReport{},
	); err != nil {
		t.Fatalf("AutoMigrate failed: %v", err)
	}
	return db
}

// insertWO is a test helper that inserts a minimal work order row.
func insertWO(t *testing.T, db *gorm.DB, propertyID uint64, status, issueType, skillTag string, unitID *uint64, rating *uint8) uint64 {
	t.Helper()
	wo := workorders.WorkOrder{
		UUID:       uuid.New().String(),
		PropertyID: propertyID,
		UnitID:     unitID,
		TenantID:   1,
		Description: "test work order",
		Priority:   "Normal",
		Status:     status,
		IssueType:  issueType,
		SkillTag:   skillTag,
		Rating:     rating,
	}
	if err := db.Create(&wo).Error; err != nil {
		t.Fatalf("insertWO: %v", err)
	}
	return wo.ID
}

// --- Popularity ---

// TestPopularityMetrics_DB verifies that PopularityMetrics returns correct
// issue-type counts from the database.
func TestPopularityMetrics_DB(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewRepository(db)

	uid1 := uint64(10)
	insertWO(t, db, 1, "New", "Plumbing", "", &uid1, nil)
	insertWO(t, db, 1, "New", "Plumbing", "", &uid1, nil)
	insertWO(t, db, 1, "New", "Electrical", "", nil, nil)

	results, err := repo.PopularityMetrics(AnalyticsFilters{})
	if err != nil {
		t.Fatalf("PopularityMetrics: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one popularity result")
	}
	// Plumbing should be most popular (count=2).
	if results[0].IssueType != "Plumbing" {
		t.Errorf("expected top issue_type=Plumbing, got %q", results[0].IssueType)
	}
	if results[0].Count != 2 {
		t.Errorf("Plumbing count: want 2, got %d", results[0].Count)
	}
}

// TestPopularityMetrics_PropertyFilter verifies PM scope: filtering by
// property_id excludes work orders from other properties.
func TestPopularityMetrics_PropertyFilter(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewRepository(db)

	insertWO(t, db, 1, "New", "Plumbing", "", nil, nil)
	insertWO(t, db, 1, "New", "Plumbing", "", nil, nil)
	insertWO(t, db, 2, "New", "Electrical", "", nil, nil) // different property

	pid := uint64(1)
	results, err := repo.PopularityMetrics(AnalyticsFilters{PropertyID: &pid})
	if err != nil {
		t.Fatalf("PopularityMetrics with filter: %v", err)
	}

	// Only property 1 work orders should appear — no Electrical.
	for _, r := range results {
		if r.IssueType == "Electrical" {
			t.Errorf("property filter did not exclude Electrical from property 2")
		}
	}
	if len(results) != 1 || results[0].IssueType != "Plumbing" {
		t.Errorf("expected exactly Plumbing for property 1, got %v", results)
	}
}

// TestPopularityMetrics_EmptyReturnsNonNilSlice ensures the repository returns
// an empty JSON-array-compatible slice (not nil) when there are no rows.
func TestPopularityMetrics_EmptyReturnsNonNilSlice(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewRepository(db)

	results, err := repo.PopularityMetrics(AnalyticsFilters{})
	if err != nil {
		t.Fatalf("PopularityMetrics: %v", err)
	}
	if results == nil {
		t.Fatal("expected non-nil empty slice, got nil")
	}
	if len(results) != 0 {
		t.Fatalf("expected no popularity rows, got %d", len(results))
	}
}

// --- Funnel ---

// TestConversionFunnel_DB verifies that ConversionFunnel counts each status bucket correctly.
func TestConversionFunnel_DB(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewRepository(db)

	insertWO(t, db, 1, "New", "", "", nil, nil)
	insertWO(t, db, 1, "New", "", "", nil, nil)
	insertWO(t, db, 1, "Assigned", "", "", nil, nil)
	insertWO(t, db, 1, "Completed", "", "", nil, nil)
	insertWO(t, db, 1, "Completed", "", "", nil, nil)

	funnel, err := repo.ConversionFunnel(AnalyticsFilters{})
	if err != nil {
		t.Fatalf("ConversionFunnel: %v", err)
	}
	if funnel.New != 2 {
		t.Errorf("New: want 2, got %d", funnel.New)
	}
	if funnel.Assigned != 1 {
		t.Errorf("Assigned: want 1, got %d", funnel.Assigned)
	}
	if funnel.Completed != 2 {
		t.Errorf("Completed: want 2, got %d", funnel.Completed)
	}
	if funnel.Total != 5 {
		t.Errorf("Total: want 5, got %d", funnel.Total)
	}
}

// TestConversionFunnel_PropertyFilter verifies funnel scope is restricted by property.
func TestConversionFunnel_PropertyFilter(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewRepository(db)

	insertWO(t, db, 1, "New", "", "", nil, nil)
	insertWO(t, db, 2, "Completed", "", "", nil, nil) // different property

	pid := uint64(1)
	funnel, err := repo.ConversionFunnel(AnalyticsFilters{PropertyID: &pid})
	if err != nil {
		t.Fatalf("ConversionFunnel with filter: %v", err)
	}
	if funnel.Total != 1 {
		t.Errorf("Total: want 1 (property 1 only), got %d", funnel.Total)
	}
	if funnel.New != 1 {
		t.Errorf("New: want 1, got %d", funnel.New)
	}
	if funnel.Completed != 0 {
		t.Errorf("Completed: want 0 (property 2 excluded), got %d", funnel.Completed)
	}
}

// --- Quality ---

// TestQualityMetrics_DB verifies rating aggregations from real DB rows.
func TestQualityMetrics_DB(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewRepository(db)

	r5 := uint8(5)
	r1 := uint8(1)
	r2 := uint8(2)
	insertWO(t, db, 1, "Completed", "", "", nil, &r5)
	insertWO(t, db, 1, "Completed", "", "", nil, &r1)
	insertWO(t, db, 1, "Completed", "", "", nil, &r2)
	insertWO(t, db, 1, "New", "", "", nil, nil) // unrated — should not count

	q, err := repo.QualityMetrics(AnalyticsFilters{})
	if err != nil {
		t.Fatalf("QualityMetrics: %v", err)
	}
	if q.TotalRated != 3 {
		t.Errorf("TotalRated: want 3, got %d", q.TotalRated)
	}
	if q.NegativeCount != 2 {
		t.Errorf("NegativeCount: want 2 (ratings 1,2), got %d", q.NegativeCount)
	}
	wantRate := 2.0 / 3.0
	if diff := q.NegativeRate - wantRate; diff > 0.001 || diff < -0.001 {
		t.Errorf("NegativeRate: want %.4f, got %.4f", wantRate, q.NegativeRate)
	}
	wantAvg := (5.0 + 1.0 + 2.0) / 3.0
	if diff := q.AverageRating - wantAvg; diff > 0.001 || diff < -0.001 {
		t.Errorf("AverageRating: want %.4f, got %.4f", wantAvg, q.AverageRating)
	}
}

// --- Retention ---

// TestRetentionMetrics_DB verifies that repeat units are correctly identified
// within the 30-day window.
func TestRetentionMetrics_DB(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewRepository(db)

	uid1 := uint64(1)
	uid2 := uint64(2)

	// unit 1 has 2 work orders → repeat
	insertWO(t, db, 1, "New", "", "", &uid1, nil)
	insertWO(t, db, 1, "New", "", "", &uid1, nil)
	// unit 2 has 1 work order → unique only
	insertWO(t, db, 1, "New", "", "", &uid2, nil)

	ret, err := repo.RetentionMetrics(AnalyticsFilters{From: time.Now().UTC().Add(-48 * time.Hour), To: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatalf("RetentionMetrics: %v", err)
	}
	if ret.UniqueUnits30d != 2 {
		t.Errorf("UniqueUnits30d: want 2, got %d", ret.UniqueUnits30d)
	}
	if ret.RepeatUnits30d != 1 {
		t.Errorf("RepeatUnits30d: want 1 (unit 1), got %d", ret.RepeatUnits30d)
	}
	wantRate := 1.0 / 2.0
	if diff := ret.RepeatRate30d - wantRate; diff > 0.001 || diff < -0.001 {
		t.Errorf("RepeatRate30d: want %.4f, got %.4f", wantRate, ret.RepeatRate30d)
	}
}

// --- Tag analysis ---

// TestTagAnalysis_SkillTag_DB verifies tag analysis via the skill_tag column on SQLite.
// (JSON_TABLE is MySQL-only; the fallback returns skill_tag counts.)
func TestTagAnalysis_SkillTag_DB(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewRepository(db)

	insertWO(t, db, 1, "New", "", "plumbing", nil, nil)
	insertWO(t, db, 1, "New", "", "plumbing", nil, nil)
	insertWO(t, db, 1, "New", "", "electrical", nil, nil)

	tags, err := repo.TagAnalysis(AnalyticsFilters{})
	if err != nil {
		t.Fatalf("TagAnalysis: %v", err)
	}
	if len(tags) == 0 {
		t.Fatal("expected at least one tag result")
	}
	// plumbing should be the top tag (count=2).
	if tags[0].Tag != "plumbing" {
		t.Errorf("expected top tag=plumbing, got %q", tags[0].Tag)
	}
	if tags[0].Count < 2 {
		t.Errorf("plumbing count: want >=2, got %d", tags[0].Count)
	}
}

// TestTagAnalysis_EmptyReturnsNonNilSlice ensures empty tag results are
// returned as a non-nil slice so API encoding stays [] instead of null.
func TestTagAnalysis_EmptyReturnsNonNilSlice(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewRepository(db)

	tags, err := repo.TagAnalysis(AnalyticsFilters{})
	if err != nil {
		t.Fatalf("TagAnalysis: %v", err)
	}
	if tags == nil {
		t.Fatal("expected non-nil empty slice, got nil")
	}
	if len(tags) != 0 {
		t.Fatalf("expected no tag rows, got %d", len(tags))
	}
}

// --- SavedReport CRUD ---

// TestSavedReport_ServiceCRUD_DB exercises the full SavedReport create/get/list/delete
// lifecycle through the service layer with a real database.
func TestSavedReport_ServiceCRUD_DB(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewRepository(db)
	svc := &Service{repo: repo, storageRoot: t.TempDir()}

	ownerID := uint64(42)

	// Create
	req := SavedReportRequest{
		Name:         "Weekly Plumbing Report",
		ReportType:   "work_orders",
		OutputFormat: "CSV",
		Schedule:     "daily",
	}
	created, appErr := svc.CreateSavedReport(req, ownerID)
	if appErr != nil {
		t.Fatalf("CreateSavedReport: %v", appErr)
	}
	if created.ID == 0 {
		t.Fatal("expected non-zero ID after create")
	}
	if created.OwnerID != ownerID {
		t.Errorf("OwnerID: want %d, got %d", ownerID, created.OwnerID)
	}
	if len(created.Filters) == 0 {
		t.Fatal("expected non-empty default filters JSON")
	}
	if string(created.Filters) != "{}" {
		t.Errorf("default filters JSON: want {}, got %s", string(created.Filters))
	}

	// Get — owner can retrieve
	fetched, appErr := svc.GetSavedReport(created.ID, ownerID)
	if appErr != nil {
		t.Fatalf("GetSavedReport: %v", appErr)
	}
	if fetched.Name != req.Name {
		t.Errorf("Name: want %q, got %q", req.Name, fetched.Name)
	}

	// Get — non-owner gets forbidden
	_, appErr = svc.GetSavedReport(created.ID, ownerID+1)
	if appErr == nil {
		t.Error("expected forbidden error for non-owner, got nil")
	}

	// List — owner sees their report
	reports, total, appErr := svc.ListSavedReports(ownerID, 1, 20)
	if appErr != nil {
		t.Fatalf("ListSavedReports: %v", appErr)
	}
	if total != 1 {
		t.Errorf("total: want 1, got %d", total)
	}
	if len(reports) != 1 {
		t.Errorf("reports len: want 1, got %d", len(reports))
	}

	// Delete — non-owner cannot delete
	appErr = svc.DeleteSavedReport(created.ID, ownerID+1)
	if appErr == nil {
		t.Error("expected forbidden error for non-owner delete, got nil")
	}

	// Delete — owner can delete
	appErr = svc.DeleteSavedReport(created.ID, ownerID)
	if appErr != nil {
		t.Fatalf("DeleteSavedReport: %v", appErr)
	}

	// After delete, get returns not-found
	_, appErr = svc.GetSavedReport(created.ID, ownerID)
	if appErr == nil {
		t.Error("expected not-found after delete, got nil")
	}
}

// TestSavedReport_InvalidSchedule_DB verifies that an unsupported schedule value
// is rejected before hitting the database.
func TestSavedReport_InvalidSchedule_DB(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewRepository(db)
	svc := &Service{repo: repo, storageRoot: t.TempDir()}

	_, appErr := svc.CreateSavedReport(SavedReportRequest{
		Name:       "Bad Schedule",
		ReportType: "work_orders",
		Schedule:   "weekly", // not a valid value
	}, 1)
	if appErr == nil {
		t.Error("expected validation error for unsupported schedule, got nil")
	}
}

// TestListSavedReports_EmptyReturnsNonNilSlice verifies list endpoints can
// serialize empty data as [] when the owner has no saved reports.
func TestListSavedReports_EmptyReturnsNonNilSlice(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewRepository(db)

	reports, total, err := repo.ListSavedReports(777, 1, 20)
	if err != nil {
		t.Fatalf("ListSavedReports: %v", err)
	}
	if reports == nil {
		t.Fatal("expected non-nil empty slice, got nil")
	}
	if total != 0 {
		t.Fatalf("expected total=0, got %d", total)
	}
	if len(reports) != 0 {
		t.Fatalf("expected no reports, got %d", len(reports))
	}
}

// TestListGeneratedReports_EmptyReturnsNonNilSlice verifies generated report
// listing returns [] (not null) when there are no generated reports.
func TestListGeneratedReports_EmptyReturnsNonNilSlice(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewRepository(db)

	reports, total, err := repo.ListGeneratedReports(nil, 1, 20)
	if err != nil {
		t.Fatalf("ListGeneratedReports: %v", err)
	}
	if reports == nil {
		t.Fatal("expected non-nil empty slice, got nil")
	}
	if total != 0 {
		t.Fatalf("expected total=0, got %d", total)
	}
	if len(reports) != 0 {
		t.Fatalf("expected no reports, got %d", len(reports))
	}
}
