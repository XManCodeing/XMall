package migrationintegrationtest

import (
	"crypto/sha256"
	"fmt"
	order "github.com/XManCodeing/XMall/internal/modules/order/domain"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	supportdomain "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/bootstrap/database/migrations"
	paymentdomain "github.com/XManCodeing/XMall/internal/modules/payment/domain"
	"github.com/XManCodeing/XMall/internal/platform/database/gormdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestEncryptedMembershipShareV41Replay checks upgrading an existing v40 ledger without rewriting data.
// t owns the database and evidence; production migration is run twice to verify idempotence.
func TestEncryptedMembershipShareV41Replay(t *testing.T) {
	db := openMigrationDB(t)
	for _, statement := range []string{
		"CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, completed_at VARCHAR(40) NOT NULL)",
		"INSERT INTO schema_migrations VALUES (40, 'synthetic-v40')",
		"CREATE TABLE encrypted_share_evidence (id INTEGER PRIMARY KEY, payload TEXT NOT NULL)",
		"INSERT INTO encrypted_share_evidence VALUES (1, 'preserved')",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := migrations.AutoMigrate(); err != nil {
			t.Fatal(err)
		}
	}
	var payload string
	if err := db.Raw("SELECT payload FROM encrypted_share_evidence WHERE id = 1").Scan(&payload).Error; err != nil || payload != "preserved" {
		t.Fatalf("encrypted-share upgrade changed evidence: %v", err)
	}
	var versions []int64
	if err := db.Raw("SELECT version FROM schema_migrations ORDER BY version").Scan(&versions).Error; err != nil || !reflect.DeepEqual(versions, []int64{40, 41, 42, 43, 44, 45, 46, 47, 48}) {
		t.Fatalf("encrypted-share ledger = %v: %v", versions, err)
	}
}

// TestMembershipShareV40Replay verifies the read-only contract upgrade preserves schema and evidence.
// t owns an isolated database; rerunning the production migration must be idempotent.
func TestMembershipShareV40Replay(t *testing.T) {
	db := openMigrationDB(t)
	if err := migrations.AutoMigrate(); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"DELETE FROM schema_migrations",
		"INSERT INTO schema_migrations(version, completed_at) VALUES (39, 'synthetic-v39')",
		"CREATE TABLE share_upgrade_evidence (id INTEGER PRIMARY KEY, payload TEXT NOT NULL)",
		"INSERT INTO share_upgrade_evidence(id, payload) VALUES (1, 'preserved')",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	var schemaBefore, schemaAfter string
	const schemaQuery = "SELECT group_concat(sql, ';') FROM (SELECT sql FROM sqlite_master ORDER BY name)"
	if err := db.Raw(schemaQuery).Scan(&schemaBefore).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrations.AutoMigrate(); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Raw(schemaQuery).Scan(&schemaAfter).Error; err != nil || schemaBefore != schemaAfter {
		t.Fatalf("read-only upgrade changed schema: %v", err)
	}
	var payload string
	if err := db.Raw("SELECT payload FROM share_upgrade_evidence WHERE id = 1").Scan(&payload).Error; err != nil || payload != "preserved" {
		t.Fatalf("read-only upgrade changed data: %v", err)
	}
	var versions []int64
	if err := db.Raw("SELECT version FROM schema_migrations ORDER BY version").Scan(&versions).Error; err != nil || !reflect.DeepEqual(versions, []int64{39, 40, 41, 42, 43, 44, 45, 46, 47, 48}) {
		t.Fatalf("upgrade ledger = %v: %v", versions, err)
	}
}

// TestSupportDailyGateV38Replay verifies additive upgrades from the prior schema.
// t owns an isolated database; existing encrypted history must survive both runs.
func TestSupportDailyGateV38Replay(t *testing.T) {
	db := openMigrationDB(t)
	for _, statement := range []string{
		"CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, completed_at VARCHAR(40) NOT NULL)",
		"INSERT INTO schema_migrations VALUES (37, 'synthetic-v37')",
		"CREATE TABLE telegram_support_sessions (id INTEGER PRIMARY KEY, bot_id INTEGER NOT NULL, user_id BIGINT NOT NULL, chat_id BIGINT NOT NULL, state TEXT NOT NULL, version BIGINT NOT NULL, sequence BIGINT)",
		"INSERT INTO telegram_support_sessions (id, bot_id, user_id, chat_id, state, version, sequence) VALUES (7, 1, 42, 42, 'open', 9, 3)",
		"CREATE TABLE telegram_support_messages (id INTEGER PRIMARY KEY, bot_id INTEGER NOT NULL, update_id BIGINT NOT NULL, session_id INTEGER NOT NULL, sequence BIGINT NOT NULL, payload_cipher TEXT, state TEXT NOT NULL)",
		"INSERT INTO telegram_support_messages (id, bot_id, update_id, session_id, sequence, payload_cipher, state) VALUES (11, 1, 22, 7, 3, 'synthetic-encrypted-history', 'delivered')",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := migrations.AutoMigrate(); err != nil {
			t.Fatal(err)
		}
	}
	var session supportdomain.Session
	if err := db.First(&session, 7).Error; err != nil || session.UserID != 42 || session.State != "open" || session.Version != 9 || session.Sequence != 3 {
		t.Fatal("daily verification upgrade changed existing session", err)
	}
	var message supportdomain.BufferedMessage
	if err := db.First(&message, 11).Error; err != nil || message.PayloadCipher != "synthetic-encrypted-history" || message.State != "delivered" || message.SessionID != 7 {
		t.Fatal("daily verification upgrade changed retained consultation", err)
	}
	for _, column := range []string{"DisplayName", "Username"} {
		if !db.Migrator().HasColumn(&supportdomain.Session{}, column) {
			t.Fatalf("missing customer identity column %s", column)
		}
	}
	for _, column := range []string{"Question", "Choices", "AnswerIndex"} {
		if !db.Migrator().HasColumn(&supportdomain.Challenge{}, column) {
			t.Fatalf("missing arithmetic verification column %s", column)
		}
	}
	for model, columns := range map[any][]string{
		&supportdomain.OperationControl{}:   {"DailyKey", "ConsumedAt"},
		&supportdomain.OperationReference{}: {"ControlRef", "BlockDays"},
	} {
		for _, column := range columns {
			if !db.Migrator().HasColumn(model, column) {
				t.Fatalf("missing inquiry control column %s", column)
			}
		}
	}
	var versions []int64
	if err := db.Raw("SELECT version FROM schema_migrations ORDER BY version").Scan(&versions).Error; err != nil || !reflect.DeepEqual(versions, []int64{37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48}) {
		t.Fatalf("upgrade ledger = %v: %v", versions, err)
	}
}

// TestOrderScopedReadsV35Replay proves the behavior-only upgrade preserves existing lifecycle evidence.
// t owns an isolated synthetic database; no live database or platform action is used.
func TestOrderScopedReadsV35Replay(t *testing.T) {
	db := openMigrationDB(t)
	if err := migrations.AutoMigrate(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	operation := d.AccessOperation{BusinessKey: "synthetic-order-scope", Kind: "lifecycle", State: "needs_review", BanOutcome: "unknown", UnbanOutcome: "failed", TelegramID: 42, TargetID: 7, BotID: 9, ChatID: -1007, Version: 8, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&operation).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DELETE FROM schema_migrations WHERE version >= 35").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO schema_migrations (version, completed_at) VALUES (34, 'synthetic-v34') ON CONFLICT(version) DO NOTHING").Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrations.AutoMigrate(); err != nil {
			t.Fatal(err)
		}
	}
	var restored d.AccessOperation
	if err := db.First(&restored, operation.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored, operation) {
		t.Fatal("read-contract upgrade rewrote historical operation")
	}
	var versions []int64
	if err := db.Raw("SELECT version FROM schema_migrations ORDER BY version").Scan(&versions).Error; err != nil || !reflect.DeepEqual(versions, []int64{34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48}) {
		t.Fatalf("upgrade ledger = %v: %v", versions, err)
	}
}

// TestProactiveV29UpgradePreservesUnknownDelivery validates the incremental step and replay without platform work.
// t owns an isolated SQLite database; the test returns after checking persisted source and receipt evidence.
func TestProactiveV29UpgradePreservesUnknownDelivery(t *testing.T) {
	db := openMigrationDB(t)
	if err := migrations.AutoMigrate(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	row := d.ProactiveDelivery{SourceID: 17, OutboxID: 23, AccountID: 7, TelegramID: 42, TargetID: 1, BotID: 9, State: "unknown", BusinessKey: "historical-delivery-evidence", AvailableAt: now, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.Exec("DELETE FROM schema_migrations WHERE version >= 29").Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec("INSERT INTO schema_migrations (version, completed_at) VALUES (28, 'synthetic-v28') ON CONFLICT(version) DO NOTHING").Error; err != nil {
			t.Fatal(err)
		}
		if err := migrations.AutoMigrate(); err != nil {
			t.Fatal(err)
		}
	}
	var restored d.ProactiveDelivery
	if err := db.First(&restored, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if restored.State != "unknown" || restored.SourceID != row.SourceID || restored.BusinessKey != row.BusinessKey {
		t.Fatal("proactive migration rewrote delivery responsibility")
	}
}

// TestProactiveCursorV30Upgrade verifies checkpoint creation on an existing v29 database.
// Replaying the incremental migration must retain both live and historical progress.
func TestProactiveCursorV30Upgrade(t *testing.T) {
	db := openMigrationDB(t)
	if err := migrations.AutoMigrate(); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(&d.ProactiveCursor{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DELETE FROM schema_migrations WHERE version >= 30").Error; err != nil {
		t.Fatal(err)
	}
	if err := migrations.AutoMigrate(); err != nil {
		t.Fatal(err)
	}
	checkpoint := d.ProactiveCursor{BotID: 9, FastAfter: 99, HistoryAfter: 7, HistoryAt: time.Now().UTC().Truncate(time.Second)}
	if err := db.Create(&checkpoint).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DELETE FROM schema_migrations WHERE version >= 30").Error; err != nil {
		t.Fatal(err)
	}
	if err := migrations.AutoMigrate(); err != nil {
		t.Fatal(err)
	}
	var restored d.ProactiveCursor
	if err := db.First(&restored, "bot_id = ?", 9).Error; err != nil {
		t.Fatal(err)
	}
	if restored.FastAfter != 99 || restored.HistoryAfter != 7 || !restored.HistoryAt.Equal(checkpoint.HistoryAt) {
		t.Fatalf("checkpoint lost: %+v", restored)
	}
}

// TestInviteLifecycleV27PreservesHistory 检查旧邀请默认有限、不延长历史截止，迁移可重复执行。
func TestInviteLifecycleV27PreservesHistory(t *testing.T) {
	db := openMigrationDB(t)
	if err := db.AutoMigrate(&d.AccessInvite{}, &d.AccessOperation{}); err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
	row := d.AccessInvite{AccountID: 7, TelegramID: 42, TargetID: 1, BotID: 9, ChatID: -1001, RouteVersion: 2, State: "unknown", ExpiresAt: expiry, ExpiryTrusted: true, CreatedAt: expiry.Add(-time.Hour), UpdatedAt: expiry.Add(-time.Hour)}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&d.AccessInvite{}, "Term"); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&d.AccessOperation{}, "CleanupCycleID"); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, completed_at VARCHAR(40) NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO schema_migrations VALUES (26, 'synthetic-v26')").Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.Exec("DELETE FROM schema_migrations WHERE version > 26").Error; err != nil {
			t.Fatal(err)
		}
		if err := migrations.AutoMigrate(); err != nil {
			t.Fatal(err)
		}
	}
	var restored d.AccessInvite
	if err := db.First(&restored, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if restored.Term != d.InviteTermLimited || !restored.ExpiresAt.Equal(expiry) || restored.State != "unknown" {
		t.Fatalf("historical rewrite %+v", restored)
	}
	if !db.Migrator().HasTable(&d.InviteCleanupCycle{}) || !db.Migrator().HasTable(&d.InviteCleanupItem{}) {
		t.Fatal("cleanup schema missing")
	}
}

// TestMembershipRefundV28UpgradesExistingV27 verifies the real runner adds recovery storage
// after an already-completed invitation migration, and preserves commands on restart.
func TestMembershipRefundV28UpgradesExistingV27(t *testing.T) {
	db := openMigrationDB(t)
	if err := db.Exec("CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, completed_at VARCHAR(40) NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO schema_migrations VALUES (27, 'synthetic-v27')").Error; err != nil {
		t.Fatal(err)
	}
	if err := migrations.AutoMigrate(); err != nil {
		t.Fatal(err)
	}
	if db.Migrator().HasTable("orders") {
		t.Fatal("versioned database ran baseline")
	}
	command := order.MembershipRefundRequest{RequestKey: "synthetic-preserved-command", ActorAdminID: 9, OrderID: 7, RefundID: 3, ItemIDs: []uint{8}, FundsState: "completed", RightsState: "pending"}
	if err := db.Create(&command).Error; err != nil {
		t.Fatal(err)
	}
	if err := migrations.AutoMigrate(); err != nil {
		t.Fatal(err)
	}
	var restored order.MembershipRefundRequest
	if err := db.First(&restored, command.ID).Error; err != nil {
		t.Fatal(err)
	}
	if restored.RefundID != 3 || restored.RightsState != "pending" || len(restored.ItemIDs) != 1 || restored.ItemIDs[0] != 8 {
		t.Fatalf("recovery command changed: %+v", restored)
	}
}

// TestTopicV15UnversionedBaseline verifies the public baseline replaces the legacy slot index.
// t owns an unversioned database; different replacement slots must both remain insertable.
func TestTopicV15UnversionedBaseline(t *testing.T) {
	db := openMigrationDB(t)
	model := &supportdomain.TopicOperation{}
	if err := db.AutoMigrate(model); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropIndex(model, "idx_support_topic_slot"); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX idx_support_topic_slot ON telegram_support_topic_operations (bot_id, session_id, target_id, route_version)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := migrations.AutoMigrate(); err != nil {
		t.Fatal(err)
	}
	for _, slot := range []uint64{0, 1} {
		row := supportdomain.TopicOperation{BotID: 2, SessionID: 3, TargetID: 4, RouteVersion: 5, SlotVersion: slot}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("baseline retained old slot constraint: %v", err)
		}
	}
}

// TestManualConfirmationBaseline 验证 t 的全新临时库经生产 AutoMigrate 建立空审计及唯一索引。
func TestManualConfirmationBaseline(t *testing.T) {
	db := openMigrationDB(t)
	for range 2 {
		if err := migrations.AutoMigrate(); err != nil {
			t.Fatal(err)
		}
	}
	assertEmptyTables(t, db)
	assertUniqueness(t, db)
	var versions []int64
	if err := db.Raw("SELECT version FROM schema_migrations ORDER BY version").Scan(&versions).Error; err != nil || !reflect.DeepEqual(versions, []int64{48}) {
		t.Fatalf("baseline ledger = %v: %v", versions, err)
	}
}

// TestManualConfirmationV23Upgrade 验证冻结历史支付表的 v23 增量和重放保留原证据，不伪造人工审计。
func TestManualConfirmationV23Upgrade(t *testing.T) {
	db := historicalV23DB(t)
	before := readPayment(t, db)
	for range 2 {
		if err := migrations.AutoMigrate(); err != nil {
			t.Fatal(err)
		}
	}
	if after := readPayment(t, db); !reflect.DeepEqual(before, after) {
		t.Fatal("v24 upgrade changed original payment evidence")
	}
	if db.Migrator().HasTable("orders") {
		t.Fatal("versioned upgrade ran unrelated current baseline")
	}
	assertEmptyTables(t, db)
	assertUniqueness(t, db)
	var versions []int64
	if err := db.Raw("SELECT version FROM schema_migrations ORDER BY version").Scan(&versions).Error; err != nil || !reflect.DeepEqual(versions, []int64{23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48}) {
		t.Fatalf("upgrade ledger = %v: %v", versions, err)
	}
}

// TestManualConfirmationFailureReplay 在 t 的隔离库注入 ledger 写入失败，覆盖 DDL 后回滚及重新升级。
func TestManualConfirmationFailureReplay(t *testing.T) {
	db := historicalV23DB(t)
	before := readPayment(t, db)
	if err := db.Exec(`CREATE TRIGGER fail_manual_v24 BEFORE INSERT ON schema_migrations WHEN NEW.version=24 BEGIN SELECT RAISE(ABORT,'synthetic v24 ledger failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if err := migrations.AutoMigrate(); err == nil || !strings.Contains(err.Error(), "synthetic v24 ledger failure") {
		t.Fatalf("expected injected ledger failure, got %v", err)
	}
	var version int64
	if err := db.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&version).Error; err != nil || version != 23 {
		t.Fatalf("failed migration advanced ledger to %d: %v", version, err)
	}
	for _, model := range auditModels() {
		if db.Migrator().HasTable(model) {
			t.Fatalf("SQLite did not roll back %T", model)
		}
	}
	if err := db.Exec("DROP TRIGGER fail_manual_v24").Error; err != nil {
		t.Fatal(err)
	}
	if err := migrations.AutoMigrate(); err != nil {
		t.Fatal(err)
	}
	if after := readPayment(t, db); !reflect.DeepEqual(before, after) {
		t.Fatal("migration failure/replay changed original payment evidence")
	}
	assertEmptyTables(t, db)
}

// TestManualConfirmationPartialDDLReplay 模拟 DDL 已完成但版本未记录，生产重放不得覆盖审计。
func TestManualConfirmationPartialDDLReplay(t *testing.T) {
	db := historicalV23DB(t)
	if err := db.AutoMigrate(auditModels()...); err != nil {
		t.Fatal(err)
	}
	assertUniqueness(t, db)
	var before []paymentdomain.ManualConfirmation
	if err := db.Find(&before).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrations.AutoMigrate(); err != nil {
			t.Fatal(err)
		}
	}
	var after []paymentdomain.ManualConfirmation
	if err := db.Find(&after).Error; err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("replay changed persisted audit: %v", err)
	}
}

// TestOrderCancellationEvidenceMigration uses the production incremental runner and a frozen pre-v25 orders table.
// Replays must preserve unknown historical cancellation causes rather than backfilling timeout provenance.
func TestOrderCancellationEvidenceMigration(t *testing.T) {
	db := openMigrationDB(t)
	for _, statement := range []string{
		"CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, completed_at VARCHAR(40) NOT NULL)",
		"INSERT INTO schema_migrations VALUES (24, 'synthetic-v24-baseline')",
		"CREATE TABLE orders (id integer primary key, status text, canceled_at datetime)",
		"INSERT INTO orders (id, status, canceled_at) VALUES (1, 'canceled', '2026-01-01 00:00:00')",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := migrations.AutoMigrate(); err != nil {
			t.Fatal(err)
		}
	}
	var stored struct {
		Status             string
		CancellationReason string
		CanceledAt         *time.Time
	}
	if err := db.Table("orders").Where("id = 1").First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.CancellationReason != "" || stored.Status != "canceled" || stored.CanceledAt == nil {
		t.Fatalf("historical evidence changed: %+v", stored)
	}
}

// openMigrationDB 返回 t 独占的单连接临时 SQLite，临时绑定生产迁移入口并在结束恢复。
func openMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "manual.sqlite")+"?_pragma=foreign_keys(1)"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	previous := gormdb.DB
	gormdb.DB = db
	t.Cleanup(func() {
		gormdb.DB = previous
		if err := pool.Close(); err != nil {
			t.Errorf("close temporary migration DB: %v", err)
		}
	})
	return db
}

// historicalV23DB 复用冻结 fixture 的支付表 SQL（v9 至 v23 未变），返回仅含相关历史表及真实 v23 ledger 的隔离库。
// 不使用当前 Payment 模型反向生成历史表，以发现对原证据和历史列的误改。
func historicalV23DB(t *testing.T) *gorm.DB {
	t.Helper()
	db := openMigrationDB(t)
	fixture, err := os.ReadFile("../testdata/v9_sqlite.sql")
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(fixture)) != "ef76ef9f9ecbf3ccb391181d04f45052ef7ccadebef7bc8e46890552b736a11c" {
		t.Fatalf("historical fixture changed or unreadable: %v", err)
	}
	for _, statement := range strings.Split(string(fixture), "\n") {
		if strings.HasPrefix(statement, "CREATE TABLE `payments`") {
			if err := db.Exec(statement).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, statement := range []string{
		"CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, completed_at VARCHAR(40) NOT NULL)",
		"INSERT INTO schema_migrations VALUES (23, 'synthetic-v23-baseline')",
		`INSERT INTO payments (id,order_id,channel_id,provider_type,channel_type,interaction_mode,amount,currency,status,provider_ref,gateway_order_no,provider_payload,pay_url,qr_code,paid_at,callback_at,exception_code) VALUES (91,81,71,'epay','alipay','redirect',42.50,'CNY','success','original-provider-ref','original-gateway-order','{"raw":"original-callback"}','https://example.invalid/pay','original-qr','2026-01-02 03:04:05','2026-01-02 03:04:06','original-exception')`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// readPayment 读取 db 冻结支付样本；t 报告错误，返回完整领域对象用于证据比较。
func readPayment(t *testing.T, db *gorm.DB) paymentdomain.Payment {
	t.Helper()
	var payment paymentdomain.Payment
	if err := db.First(&payment, 91).Error; err != nil {
		t.Fatal(err)
	}
	return payment
}

// auditModels 返回 v24 仅新增的三个模型，不包含旧支付表。
func auditModels() []any {
	return []any{&paymentdomain.ManualConfirmation{}, &paymentdomain.PaymentEvidenceClaim{}, &paymentdomain.PaymentReceiptAnomaly{}}
}

// assertEmptyTables 验证 db 已建模且没有迁移生成的业务记录；失败由 t 报告。
func assertEmptyTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, model := range auditModels() {
		var count int64
		if err := db.Model(model).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("migration manufactured %T records: count=%d, error=%v", model, count, err)
		}
	}
}

// assertUniqueness 用合成记录验证 db 的审计/请求/渠道证据/异常唯一约束；失败由 t 报告。
func assertUniqueness(t *testing.T, db *gorm.DB) {
	t.Helper()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	audit := paymentdomain.ManualConfirmation{PaymentID: 91, OrderID: 81, AdminID: 61,
		RequestID: "synthetic-manual-request", RequestHash: "synthetic-hash", Amount: "42.50", Currency: "CNY",
		TransactionID: "synthetic-reference", EvidenceReference: "synthetic-bank-record", Reason: "synthetic-reviewed-evidence",
		PreviousPaymentStatus: "pending", PreviousOrderStatus: "pending_payment", PaymentStatus: "success", OrderStatus: "paid", PaidAt: now, CreatedAt: now}
	if err := db.Create(&audit).Error; err != nil {
		t.Fatal(err)
	}
	for _, duplicate := range []paymentdomain.ManualConfirmation{{PaymentID: audit.PaymentID, RequestID: "different-request"}, {PaymentID: audit.PaymentID + 1, RequestID: audit.RequestID}} {
		if err := db.Create(&duplicate).Error; err == nil {
			t.Fatal("manual confirmation payment/request uniqueness missing")
		}
	}
	claim := paymentdomain.PaymentEvidenceClaim{ChannelID: 71, ReferenceHash: "synthetic-reference-hash", PaymentID: 91, CreatedAt: now}
	if err := db.Create(&claim).Error; err != nil {
		t.Fatal(err)
	}
	claim.ID, claim.PaymentID = 0, 92
	if err := db.Create(&claim).Error; err == nil {
		t.Fatal("channel evidence uniqueness missing")
	}
	claim.ID, claim.ChannelID = 0, 72
	if err := db.Create(&claim).Error; err != nil {
		t.Fatalf("evidence scope incorrectly crosses channels: %v", err)
	}
	anomaly := paymentdomain.PaymentReceiptAnomaly{PaymentID: 91, ReferenceHash: "synthetic-extra-receipt", TransactionID: "synthetic-extra", Amount: "42.50", Currency: "CNY", ObservedAt: now}
	if err := db.Create(&anomaly).Error; err != nil {
		t.Fatal(err)
	}
	anomaly.ID = 0
	if err := db.Create(&anomaly).Error; err == nil {
		t.Fatal("receipt anomaly uniqueness missing")
	}
}
