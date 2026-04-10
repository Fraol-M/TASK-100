package integration_test

import (
	"os"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/mysql"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"propertyops/backend/internal/attachments"
	"propertyops/backend/internal/analytics"
	authpkg "propertyops/backend/internal/auth"
	"propertyops/backend/internal/audit"
	"propertyops/backend/internal/governance"
	"propertyops/backend/internal/notifications"
	"propertyops/backend/internal/payments"
	"propertyops/backend/internal/properties"
	"propertyops/backend/internal/tenants"
	userpkg "propertyops/backend/internal/users"
	"propertyops/backend/internal/workorders"
)

// setupTestDB opens a test database and auto-migrates all models.
//
// MySQL mode (full fidelity):
//
//	Set TEST_MYSQL_DSN to a MySQL 8.0 DSN, e.g.:
//	  TEST_MYSQL_DSN="user:pass@tcp(127.0.0.1:3306)/propertyops_test?parseTime=true" \
//	    go test ./test/... -count=1
//
//	Each test gets a clean slate via TRUNCATE before and after the test run.
//	MySQL mode validates FK constraints, JSON column semantics, DECIMAL
//	precision and index uniqueness exactly as production does.
//
// SQLite mode (fast, default):
//
//	Each test gets its own private in-memory SQLite database so they never
//	share state. SQLite does not reproduce MySQL-specific behaviors such as
//	JSON_TABLE, strict FK enforcement, and DECIMAL rounding.
//
//	If sqlite3 is unavailable (CGO_ENABLED=0 / no C compiler), the test is
//	skipped with a clear message rather than failing.
func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	if dsn := os.Getenv("TEST_MYSQL_DSN"); dsn != "" {
		return setupMySQLTestDB(t, dsn)
	}

	// Each test gets its own private in-memory database so they never share state.
	db, err := gorm.Open(sqlite.Open("file::memory:?mode=memory&cache=private"), &gorm.Config{
		Logger:                                   logger.Discard,
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		// gorm.io/driver/sqlite returns an error containing "CGO_ENABLED=0" or
		// "cgo to work" when the binary was built without CGO support.
		t.Skipf("SQLite unavailable (CGO disabled or no C compiler): %v", err)
	}

	if err := db.AutoMigrate(allModels()...); err != nil {
		t.Fatalf("AutoMigrate failed: %v", err)
	}

	// SQLite does not enforce FK constraints by default; enable WAL for better
	// concurrent-read performance in tests.
	db.Exec("PRAGMA journal_mode=WAL;")

	return db
}

// setupMySQLTestDB connects to a real MySQL 8.0 instance, migrates the schema,
// truncates all tables for a clean slate, and registers cleanup to truncate again
// after the test. This provides full MySQL-fidelity: FK enforcement, JSON_TABLE,
// DECIMAL precision, and production-identical index behaviour.
func setupMySQLTestDB(t *testing.T, dsn string) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("MySQL connection failed (TEST_MYSQL_DSN=%q): %v", dsn, err)
	}

	if err := db.AutoMigrate(allModels()...); err != nil {
		t.Fatalf("MySQL AutoMigrate failed: %v", err)
	}

	truncateMySQLTables(t, db)
	t.Cleanup(func() { truncateMySQLTables(t, db) })

	return db
}

// truncateMySQLTables clears all test tables in dependency order so FK checks
// do not block the truncation. Disabling FK checks is safe here because we
// immediately re-enable them and AutoMigrate enforces the correct constraints.
func truncateMySQLTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	db.Exec("SET FOREIGN_KEY_CHECKS = 0")
	// Ordered children-before-parents so truncation never races with FK checks
	// on the re-enable step.
	tables := []string{
		"audit_logs",
		"generated_reports", "saved_reports",
		"attachments",
		"notification_receipts", "thread_messages", "thread_participants", "message_threads",
		"notifications", "notification_templates",
		"payment_approvals", "reconciliation_runs", "payments",
		"enforcement_actions", "keywords_blacklist", "risk_rules", "reports",
		"work_order_events", "cost_items", "work_orders",
		"dispatch_cursors", "technician_skill_tags", "property_staff_assignments",
		"tenant_profiles",
		"units", "properties",
		"user_roles", "sessions",
		"users", "roles",
	}
	for _, tbl := range tables {
		if err := db.Exec("TRUNCATE TABLE `" + tbl + "`").Error; err != nil {
			t.Logf("WARN truncateMySQLTables: could not truncate %q: %v", tbl, err)
		}
	}
	db.Exec("SET FOREIGN_KEY_CHECKS = 1")
}

// allModels returns the ordered list of GORM models used by AutoMigrate.
func allModels() []interface{} {
	return []interface{}{
		// Auth
		&authpkg.Session{},
		&authpkg.User{},
		&authpkg.Role{},
		// Users
		&userpkg.User{},
		&userpkg.Role{},
		&userpkg.UserRole{},
		// Properties (required for PM scope checks against property_staff_assignments)
		&properties.Property{},
		&properties.Unit{},
		&properties.PropertyStaffAssignment{},
		&properties.TechnicianSkillTag{},
		&properties.DispatchCursor{},
		// Tenants
		&tenants.TenantProfile{},
		// Work Orders
		&workorders.WorkOrder{},
		&workorders.WorkOrderEvent{},
		&workorders.CostItem{},
		// Attachments
		&attachments.Attachment{},
		// Analytics
		&analytics.SavedReport{},
		&analytics.GeneratedReport{},
		// Payments
		&payments.Payment{},
		&payments.PaymentApproval{},
		&payments.ReconciliationRun{},
		// Governance
		&governance.Report{},
		&governance.EnforcementAction{},
		&governance.KeywordBlacklist{},
		&governance.RiskRule{},
		// Notifications
		&notifications.Notification{},
		&notifications.NotificationTemplate{},
		&notifications.NotificationReceipt{},
		&notifications.MessageThread{},
		&notifications.ThreadParticipant{},
		&notifications.ThreadMessage{},
		// Audit
		&audit.AuditLog{},
	}
}

// seedRoles inserts the five standard application roles into the DB.
func seedRoles(t *testing.T, db *gorm.DB) {
	t.Helper()

	roleNames := []string{
		"Tenant",
		"Technician",
		"PropertyManager",
		"ComplianceReviewer",
		"SystemAdmin",
	}
	for _, name := range roleNames {
		role := userpkg.Role{Name: name, Description: name + " role"}
		if err := db.FirstOrCreate(&role, userpkg.Role{Name: name}).Error; err != nil {
			t.Fatalf("seedRoles: failed to create role %q: %v", name, err)
		}
	}
}

// hashPassword bcrypt-hashes a plaintext password for test seeding.
// Uses cost 4 (minimum) so tests run fast.
func hashPassword(t *testing.T, password string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	return string(hash)
}

// createTestUser inserts a User with the given username and role into both the
// auth.users table and the users.users table (they share the same physical
// table in SQLite). Returns the created user and the plaintext password used.
func createTestUser(t *testing.T, db *gorm.DB, username, roleName string) (*authpkg.User, string) {
	t.Helper()

	password := "Password123!"
	hash := hashPassword(t, password)

	// Ensure the role exists.
	var role userpkg.Role
	if err := db.Where("name = ?", roleName).First(&role).Error; err != nil {
		t.Fatalf("createTestUser: role %q not found (did you call seedRoles?): %v", roleName, err)
	}

	// Insert into the shared users table via userpkg.User (same table).
	user := userpkg.User{
		UUID:         newUUID(),
		Username:     username,
		Email:        username + "@test.local",
		PasswordHash: hash,
		FirstName:    username,
		LastName:     "Test",
		IsActive:     true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("createTestUser: failed to create user %q: %v", username, err)
	}

	// Assign role via join table.
	ur := userpkg.UserRole{UserID: user.ID, RoleID: role.ID}
	if err := db.Create(&ur).Error; err != nil {
		t.Fatalf("createTestUser: failed to assign role: %v", err)
	}

	// Load as auth.User (same table, auth package's struct).
	var authUser authpkg.User
	if err := db.Preload("Roles").First(&authUser, user.ID).Error; err != nil {
		t.Fatalf("createTestUser: reload failed: %v", err)
	}

	return &authUser, password
}

// newUUID returns a simple time-seeded pseudo-unique string suitable for tests.
func newUUID() string {
	return time.Now().Format("20060102150405.000000000")
}
