package migrationintegrationtest

import (
	"reflect"

	"github.com/XManCodeing/XMall/internal/bootstrap/database/migrations"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"gorm.io/gorm"
	"testing"
)

// TestRefundExecutionV44Upgrade preserves legacy audits without fabricating execution bindings.
// t owns an isolated SQLite database; the production migration is replayed twice.
func TestRefundExecutionV44Upgrade(t *testing.T) {
	db := openMigrationDB(t)
	statements := []string{
		"CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, completed_at VARCHAR(40) NOT NULL)",
		"INSERT INTO schema_migrations VALUES (43, 'synthetic-v43')",
		"CREATE TABLE telegram_membership_refund_dispositions (id INTEGER PRIMARY KEY, refund_id INTEGER NOT NULL, order_item_id INTEGER NOT NULL, actor_admin_id INTEGER NOT NULL, account_id INTEGER NOT NULL, beneficiary_telegram_id BIGINT NOT NULL, snapshot_hash TEXT NOT NULL, source_ids TEXT NOT NULL, expected_version BIGINT NOT NULL, created_at DATETIME NOT NULL)",
		"INSERT INTO telegram_membership_refund_dispositions VALUES (1, 2, 13, 3, 4, 6277335228, 'original', '[12]', 1, '2026-11-07 22:50:11')",
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := migrations.AutoMigrate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, column := range []string{"execution_version", "executions"} {
		if !db.Migrator().HasColumn(&d.RefundDisposition{}, column) {
			t.Fatalf("v44 incremental upgrade is missing column %s", column)
		}
	}
	if !db.Migrator().HasIndex(&d.RefundDisposition{}, "uidx_membership_refund_disposition") {
		t.Fatal("v44 incremental upgrade is missing refund/item uniqueness")
	}
	var completedAt string
	if err := db.Raw("SELECT completed_at FROM schema_migrations WHERE version = 43").Scan(&completedAt).Error; err != nil || completedAt != "synthetic-v43" {
		t.Fatalf("upgrade rewrote historical ledger: %q %v", completedAt, err)
	}
	var count int64
	if err := db.Table("schema_migrations").Where("version = ?", 44).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("v44 must be recorded exactly once: %d %v", count, err)
	}
	var audit d.RefundDisposition
	if err := db.First(&audit, 1).Error; err != nil {
		t.Fatal(err)
	}
	if audit.ExecutionVersion != 0 || len(audit.Executions) != 0 || audit.SnapshotHash != "original" || len(audit.SourceIDs) != 1 || audit.SourceIDs[0] != 12 || audit.OrderItemID != 13 {
		t.Fatalf("legacy audit changed: %+v", audit)
	}
	assertRefundMigrationLedger(t, db, []int64{43, 44, 45, 46, 47, 48})
}

// TestMembershipRefundRetryV47Upgrade preserves pending/queued commands and money evidence.
// t owns a frozen v46 SQLite schema; production upgrade and replay must not issue refunds.
func TestMembershipRefundRetryV47Upgrade(t *testing.T) {
	db := openMigrationDB(t)
	seedRefundRetryV46(t, db)
	const historyQuery = `SELECT id, request_key, fingerprint, actor_admin_id, method, amount, remark,
		existing_refund_id, order_id, refund_id, item_ids, funds_state, rights_state, created_at, updated_at
		FROM membership_refund_requests ORDER BY id`
	var before, after []map[string]any
	if err := db.Raw(historyQuery).Scan(&before).Error; err != nil {
		t.Fatal(err)
	}
	var firstCompleted string
	for attempt := range 2 {
		if err := migrations.AutoMigrate(); err != nil {
			t.Fatalf("v46 -> v47 attempt %d: %v", attempt, err)
		}
		assertRefundMigrationLedger(t, db, []int64{45, 46, 47, 48})
		after = nil
		if err := db.Raw(historyQuery).Scan(&after).Error; err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("migration changed original refund commands: before=%v after=%v err=%v", before, after, err)
		}
		var rows []orderdomain.MembershipRefundRequest
		if err := db.Order("id").Find(&rows).Error; err != nil || len(rows) != 2 {
			t.Fatalf("migration replayed or removed refund requests: %+v %v", rows, err)
		}
		for _, row := range rows {
			if row.RetryAttempts != 0 || row.RetryVersion != 0 || row.RetryAfter != nil || row.RetryLeaseUntil != nil {
				t.Fatalf("migration fabricated retry ownership: %+v", row)
			}
		}
		var valid bool
		if err := db.Raw(`SELECT (SELECT completed_at FROM schema_migrations WHERE version=45)='original-v45' AND
			(SELECT completed_at FROM schema_migrations WHERE version=46)='original-v46' AND
			(SELECT COUNT(*) FROM order_refund_records)=1 AND
			EXISTS(SELECT 1 FROM order_refund_records WHERE id=81 AND order_id=12 AND amount='19.80')`).Scan(&valid).Error; err != nil || !valid {
			t.Fatalf("upgrade changed historical migrations or refunded money again: %v", err)
		}
		var completed string
		if err := db.Raw("SELECT completed_at FROM schema_migrations WHERE version=47").Scan(&completed).Error; err != nil || completed == "" {
			t.Fatalf("missing v47 migration timestamp: %q %v", completed, err)
		}
		if attempt == 0 {
			firstCompleted = completed
		} else if completed != firstCompleted {
			t.Fatal("replay changed v47 migration timestamp")
		}
	}
	columns := sqliteMigrationColumns(t, db, "membership_refund_requests")
	for name, expected := range map[string]string{
		"retry_attempts": "INTEGER null=1 default=0 pk=0", "retry_version": "INTEGER null=1 default=0 pk=0",
		"retry_after": "DATETIME null=0 default=<absent> pk=0", "retry_lease_until": "DATETIME null=0 default=<absent> pk=0",
	} {
		if columns[name] != expected {
			t.Errorf("retry column %s = %q, want %q", name, columns[name], expected)
		}
	}
	for _, index := range []string{"idx_membership_refund_requests_request_key", "idx_membership_refund_requests_retry_after"} {
		if !db.Migrator().HasIndex(&orderdomain.MembershipRefundRequest{}, index) {
			t.Errorf("missing refund retry index %s", index)
		}
	}
}

// TestProactiveIdleV48UpgradePreservesHistory checks the behavior-only upgrade and replay.
// t owns a frozen v47 SQLite checkpoint; schema, cursor values and prior ledger remain unchanged.
func TestProactiveIdleV48UpgradePreservesHistory(t *testing.T) {
	db := openMigrationDB(t)
	for _, statement := range []string{
		"CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, completed_at VARCHAR(40) NOT NULL)",
		"INSERT INTO schema_migrations VALUES (47, 'original-v47')",
		"CREATE TABLE telegram_membership_proactive_cursors (bot_id INTEGER PRIMARY KEY, fast_after INTEGER NOT NULL, history_after INTEGER NOT NULL, history_at DATETIME NOT NULL)",
		"INSERT INTO telegram_membership_proactive_cursors VALUES (9, 99, 12, '2026-11-10 06:00:00')",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	const schemaQuery = "SELECT type, name, sql FROM sqlite_master ORDER BY type, name"
	var before, after []map[string]any
	if err := db.Raw(schemaQuery).Scan(&before).Error; err != nil {
		t.Fatal(err)
	}
	var completedAt string
	for attempt := range 2 {
		if err := migrations.AutoMigrate(); err != nil {
			t.Fatal(err)
		}
		assertRefundMigrationLedger(t, db, []int64{47, 48})
		after = nil
		if err := db.Raw(schemaQuery).Scan(&after).Error; err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("behavior upgrade changed schema: err=%v", err)
		}
		var valid bool
		if err := db.Raw(`SELECT
			(SELECT completed_at FROM schema_migrations WHERE version=47)='original-v47' AND
			(SELECT COUNT(*) FROM telegram_membership_proactive_cursors)=1 AND
			EXISTS(SELECT 1 FROM telegram_membership_proactive_cursors WHERE bot_id=9 AND
			fast_after=99 AND history_after=12 AND history_at='2026-11-10 06:00:00')`).Scan(&valid).Error; err != nil || !valid {
			t.Fatalf("behavior upgrade rewrote cursor/history: valid=%t err=%v", valid, err)
		}
		var actual string
		if err := db.Raw("SELECT completed_at FROM schema_migrations WHERE version=48").Scan(&actual).Error; err != nil || actual == "" {
			t.Fatalf("missing v48 completion: err=%v", err)
		}
		if attempt == 0 {
			completedAt = actual
		} else if actual != completedAt {
			t.Fatal("replay changed v48 completion time")
		}
	}
}

// seedRefundRetryV46 creates frozen pre-retry DDL and synthetic pending/queued evidence.
// t reports SQL errors in db; no current model is used to construct the historical schema.
func seedRefundRetryV46(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, statement := range []string{
		"CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, completed_at VARCHAR(40) NOT NULL)",
		"INSERT INTO schema_migrations VALUES (45, 'original-v45'), (46, 'original-v46')",
		`CREATE TABLE membership_refund_requests (id INTEGER PRIMARY KEY AUTOINCREMENT, request_key TEXT NOT NULL,
			fingerprint TEXT NOT NULL, actor_admin_id INTEGER, method TEXT NOT NULL, amount TEXT, remark TEXT,
			existing_refund_id INTEGER, order_id INTEGER NOT NULL, refund_id INTEGER NOT NULL, item_ids TEXT NOT NULL,
			funds_state TEXT NOT NULL, rights_state TEXT NOT NULL, created_at DATETIME, updated_at DATETIME)`,
		"CREATE UNIQUE INDEX idx_membership_refund_requests_request_key ON membership_refund_requests(request_key)",
		`INSERT INTO membership_refund_requests VALUES
			(1, 'pending-original', 'pending-fingerprint', 3, 'wallet', '7.25', 'original pending', 0, 11, 0, '[31, 32]', 'pending', 'pending', '2026-10-01 01:02:03', '2026-10-02 01:02:03'),
			(2, 'queued-original', 'queued-fingerprint', 4, 'wallet', '19.80', 'original queued', 81, 12, 81, '[42,41]', 'succeeded', 'queued', '2026-10-03 01:02:03', '2026-10-04 01:02:03')`,
		"CREATE TABLE order_refund_records (id INTEGER PRIMARY KEY, order_id INTEGER NOT NULL, amount TEXT NOT NULL)",
		"INSERT INTO order_refund_records VALUES (81, 12, '19.80')",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("seed frozen v46 refund fixture: %v", err)
		}
	}
}

// assertRefundMigrationLedger verifies db contains exactly expected versions; t reports mismatches.
func assertRefundMigrationLedger(t *testing.T, db *gorm.DB, expected []int64) {
	t.Helper()
	var versions []int64
	if err := db.Raw("SELECT version FROM schema_migrations ORDER BY version").Scan(&versions).Error; err != nil || !reflect.DeepEqual(versions, expected) {
		t.Fatalf("refund migration ledger = %v, want %v: %v", versions, expected, err)
	}
}
