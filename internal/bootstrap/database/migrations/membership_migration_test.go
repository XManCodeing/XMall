package migrations

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	fulfillment "github.com/XManCodeing/XMall/internal/modules/fulfillment/domain"
	groupsdomain "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	membershipdomain "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	recoverydomain "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	runtimedomain "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	supportdomain "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"gorm.io/gorm"
)

// assertHistoricalV9Sentinels checks db's synthetic identity/balance and contiguous current ledger; t reports failures.
func assertHistoricalV9Sentinels(t *testing.T, db *gorm.DB) {
	t.Helper()
	var valid bool
	if err := db.Raw(`SELECT (SELECT COUNT(*) FROM schema_migrations WHERE version BETWEEN 9 AND ?)=? AND (SELECT MAX(version) FROM schema_migrations)=? AND (SELECT completed_at FROM schema_migrations WHERE version=9)='synthetic-v9-baseline' AND EXISTS(SELECT 1 FROM users u JOIN wallet_accounts w ON w.user_id=u.id JOIN user_oauth_identities i ON i.user_id=u.id WHERE u.id=71 AND u.email='synthetic-v9@example.invalid' AND w.balance=123.45 AND i.provider_user_id='9007199254740993')`, schemaVersion, schemaVersion-8, schemaVersion).Scan(&valid).Error; err != nil || !valid {
		t.Fatalf("historical upgrade lost business sentinels or ledger: %v", err)
	}
}

// TestRuntimeRecoveryV21Upgrade verifies interrupted upgrade/replay on t's isolated DB without inventing old worker exits.
func TestRuntimeRecoveryV21Upgrade(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "runtime-v21.sqlite"))
		if fresh {
			if err := runSchemaMigrations(db, schemaVersion, migrateSchema, schemaSteps...); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := runSchemaMigrations(db, 20, migrateSchema, schemaSteps[:19]...); err != nil {
				t.Fatal(err)
			}
			if err := db.Migrator().DropTable(&runtimedomain.GenerationExit{}); err != nil {
				t.Fatal(err)
			}
			for _, column := range []string{"recovery_task_id", "recovery_version", "recovery_generation"} {
				if err := db.Migrator().DropColumn(&runtimedomain.Instance{}, column); err != nil {
					t.Fatal(err)
				}
			}
			injected := errors.New("synthetic runtime recovery interruption")
			steps := append([]migrationStep(nil), schemaSteps...)
			steps[19].run = func(tx *gorm.DB) error {
				if err := migrateTelegramRuntimeRecovery(tx); err != nil {
					return err
				}
				return injected
			}
			if err := runSchemaMigrations(db, schemaVersion, migrateSchema, steps...); !errors.Is(err, injected) {
				t.Fatalf("interruption not reported: %v", err)
			}
			var version int64
			if err := db.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&version).Error; err != nil || version != 20 {
				t.Fatalf("failed upgrade advanced version: %d %v", version, err)
			}
		}
		for range 2 {
			if err := runSchemaMigrations(db, schemaVersion, migrateSchema, schemaSteps...); err != nil {
				t.Fatal(err)
			}
		}
		bot := runtimedomain.Instance{TelegramID: 987, Generation: 4, Version: 3, Enabled: false, State: "stopped"}
		if err := db.Create(&bot).Error; err != nil {
			t.Fatal(err)
		}
		if err := migrateTelegramRuntimeRecovery(db); err != nil {
			t.Fatal(err)
		}
		var count int64
		if err := db.Model(&runtimedomain.GenerationExit{}).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("historical drain proof fabricated: %d %v", count, err)
		}
		joined := time.Now().UTC()
		proof := runtimedomain.GenerationExit{BotID: bot.ID, Generation: 4, Version: 3, JoinedAt: &joined}
		if err := db.Create(&proof).Error; err != nil {
			t.Fatal(err)
		}
		if err := migrateTelegramRuntimeRecovery(db); err != nil {
			t.Fatal(err)
		}
		var saved runtimedomain.GenerationExit
		if err := db.First(&saved).Error; err != nil || saved.JoinedAt == nil || !saved.JoinedAt.Equal(joined) || saved.Version != 3 {
			t.Fatalf("real proof changed on replay: %+v %v", saved, err)
		}
	}
}

// TestRecoveryV20MappingReplay checks additive baseline and upgrade replay without
// changing original identities, term dates or unresolved support evidence.
func TestRecoveryV20MappingReplay(t *testing.T) {
	for _, baseline := range []bool{false, true} {
		db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "recovery-v20.sqlite"))
		var err error
		if baseline {
			err = migrateSchema(db)
		} else {
			err = migrateTelegramRecoveryMappings(db)
		}
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		mapping := membershipdomain.IdentityMigration{AccountID: 7, TargetID: 4, OldTelegramID: 9007199254740993,
			NewTelegramID: 9007199254740995, ActorAdminID: 3, BindingVersion: 5, BotID: 2, ChatID: -100,
			RouteVersion: 9, GroupVersion: 11, EntitlementVersion: 13, Version: 2, State: "awaiting_admission", CreatedAt: now}
		checkpoint := supportdomain.DisasterCheckpoint{Key: "synthetic-v20", SourceBotID: 2, DestinationBotID: 3,
			SourceGeneration: 4, ActorID: 3, Evidence: "synthetic", Revision: 7, Phase: "topics", Cursor: 5,
			TopicBound: 10, RetainedMessages: 4, UncertainTopics: 2, CreatedAt: now, UpdatedAt: now}
		if err := db.Create(&mapping).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&checkpoint).Error; err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := migrateTelegramRecoveryMappings(db); err != nil {
				t.Fatal(err)
			}
		}
		var saved membershipdomain.IdentityMigration
		if err := db.First(&saved, mapping.ID).Error; err != nil || saved.OldTelegramID != mapping.OldTelegramID || saved.NewTelegramID != mapping.NewTelegramID || saved.State != mapping.State || saved.Version != 2 || saved.AdmittedAt != nil || saved.CompletedAt != nil {
			t.Fatalf("mapping changed: %+v %v", saved, err)
		}
		var progress supportdomain.DisasterCheckpoint
		if err := db.First(&progress, checkpoint.ID).Error; err != nil || progress.Revision != 7 || progress.Cursor != 5 || progress.RetainedMessages != 4 || progress.UncertainTopics != 2 {
			t.Fatalf("checkpoint changed: %+v %v", progress, err)
		}
		mapping.ID, checkpoint.ID = 0, 0
		if err := db.Create(&mapping).Error; err == nil {
			t.Fatal("duplicate beneficiary mapping accepted")
		}
		if err := db.Create(&checkpoint).Error; err == nil {
			t.Fatal("duplicate disaster scope accepted")
		}
	}
}

// TestRecoveryV20InterruptedUpgrade verifies partially-created schema is safely
// retried and the version ledger never advances after a failed migration.
func TestRecoveryV20InterruptedUpgrade(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "recovery-v20-interrupted.sqlite"))
	if err := runSchemaMigrations(db, 19, migrateSchema, schemaSteps[:18]...); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(&membershipdomain.IdentityMigration{}, &supportdomain.DisasterCheckpoint{}); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("synthetic mapping interruption")
	steps := append([]migrationStep(nil), schemaSteps...)
	steps[18].run = func(tx *gorm.DB) error {
		if err := migrateTelegramRecoveryMappings(tx); err != nil {
			return err
		}
		return injected
	}
	if err := runSchemaMigrations(db, 20, migrateSchema, steps[:19]...); !errors.Is(err, injected) {
		t.Fatal("missing interruption", err)
	}
	var version int64
	if err := db.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&version).Error; err != nil || version != 19 {
		t.Fatal("version advanced after failure", version, err)
	}
	for range 2 {
		if err := runSchemaMigrations(db, 20, migrateSchema, schemaSteps[:19]...); err != nil {
			t.Fatal(err)
		}
	}
	if !db.Migrator().HasTable(&membershipdomain.IdentityMigration{}) || !db.Migrator().HasTable(&supportdomain.DisasterCheckpoint{}) {
		t.Fatal("mapping tables missing after replay")
	}
}

// TestRecoveryV19ReplayPreservesFences checks additive upgrade, interrupted DDL replay,
// task checkpoints and unique scope ownership without changing historical routes.
func TestRecoveryV19ReplayPreservesFences(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "recovery-v19.sqlite"))
	if err := migrateTelegramGroups(db); err != nil {
		t.Fatal(err)
	}
	target := groupsdomain.Target{ID: 1, Name: "synthetic", Purpose: "membership", Version: 3, RouteVersion: 2, BotInstanceID: 7, TelegramChatID: -100, OccupancySlot: "chat:-100"}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&groupsdomain.Target{}, "RecoveryTaskID"); err != nil {
		t.Fatal(err)
	}
	// Simulate partially applied MySQL DDL before the migration ledger was advanced.
	if err := db.AutoMigrate(&recoverydomain.Task{}); err != nil {
		t.Fatal(err)
	}
	if err := migrateTelegramRecovery(db); err != nil {
		t.Fatal(err)
	}
	var upgraded groupsdomain.Target
	if err := db.First(&upgraded, 1).Error; err != nil || upgraded.RecoveryTaskID != 0 || upgraded.TelegramChatID != -100 || upgraded.RouteVersion != 2 {
		t.Fatalf("upgrade changed route: %+v %v", upgraded, err)
	}
	now := time.Now().UTC()
	task := recoverydomain.Task{ID: 11, Kind: recoverydomain.GroupReplace, State: recoverydomain.NeedsReview, RequestKey: "migration-test", RequestHash: "synthetic", PreflightRef: "synthetic-ref", SnapshotJSON: "{}", ActorID: 1, Reason: "synthetic", Version: 7, ExpiresAt: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now}
	step := recoverydomain.Step{TaskID: 11, BusinessKey: "switch:1", Operation: "switch", State: recoverydomain.StepUnknown, Version: 4, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&step).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&recoverydomain.ScopeSlot{Key: "target:1", TaskID: 11}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&groupsdomain.Target{}).Where("id = 1").Update("recovery_task_id", 11).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrateTelegramRecovery(db); err != nil {
			t.Fatal(err)
		}
	}
	var saved recoverydomain.Step
	if err := db.First(&saved, step.ID).Error; err != nil || saved.State != recoverydomain.StepUnknown || saved.Version != 4 {
		t.Fatalf("lost checkpoint: %+v %v", saved, err)
	}
	if err := db.First(&upgraded, 1).Error; err != nil || upgraded.RecoveryTaskID != 11 {
		t.Fatal("lost fence", err)
	}
	if err := db.Create(&recoverydomain.ScopeSlot{Key: "target:1", TaskID: 12}).Error; err == nil {
		t.Fatal("duplicate scope accepted")
	}
	if err := db.Create(&recoverydomain.ScopeSlot{Key: "", TaskID: 12}).Error; err == nil {
		t.Fatal("empty scope accepted after migration replay")
	}
}

// TestRecoveryV19InterruptedUpgrade verifies the version ledger advances only after all models succeed.
func TestRecoveryV19InterruptedUpgrade(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "recovery-interrupted.sqlite"))
	if err := runSchemaMigrations(db, 18, migrateSchema, schemaSteps[:17]...); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(&recoverydomain.Task{}, &recoverydomain.ScopeSlot{}, &recoverydomain.Step{}); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("synthetic recovery interruption")
	steps := append([]migrationStep(nil), schemaSteps[:18]...)
	steps[17].run = func(tx *gorm.DB) error {
		if err := migrateTelegramRecovery(tx); err != nil {
			return err
		}
		return injected
	}
	if err := runSchemaMigrations(db, 19, migrateSchema, steps...); !errors.Is(err, injected) {
		t.Fatalf("interruption: %v", err)
	}
	var version int64
	if err := db.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&version).Error; err != nil || version != 18 {
		t.Fatalf("ledger advanced: %d %v", version, err)
	}
	if db.Migrator().HasTable(&recoverydomain.Task{}) {
		t.Fatal("DDL escaped rollback")
	}
	for range 2 {
		if err := runSchemaMigrations(db, 19, func(*gorm.DB) error { return errors.New("baseline replayed") }, schemaSteps[:18]...); err != nil {
			t.Fatal(err)
		}
	}
	if !db.Migrator().HasTable(&recoverydomain.ScopeSlot{}) || !db.Migrator().HasTable(&recoverydomain.Step{}) {
		t.Fatal("recovery tables missing")
	}
}

// TestMembershipV18ReminderReplay verifies baseline, upgrade and replay preserve receipt and unique cycle identity.
func TestMembershipV18ReminderReplay(t *testing.T) {
	for _, baseline := range []bool{false, true} {
		db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "reminder.sqlite"))
		var err error
		if baseline {
			err = migrateSchema(db)
		} else {
			err = migrateTelegramMembershipLifecycle(db)
		}
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		row := membershipdomain.Reminder{BusinessKey: "membership-reminder:2:3:4:5:7", BotID: 1, TargetID: 2, AccountID: 7, TelegramID: 3, ChatID: -100, BindingVersion: 1, EntitlementVersion: 5, RouteVersion: 1, ExpiresAt: now.Add(time.Hour), Days: 7, State: "unknown", NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}
		cursor := membershipdomain.ReminderCursor{BotID: 1, TargetID: 2, TelegramID: 3, Version: 9}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&cursor).Error; err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := migrateTelegramMembershipLifecycle(db); err != nil {
				t.Fatal(err)
			}
		}
		var saved membershipdomain.Reminder
		if err := db.First(&saved, row.ID).Error; err != nil || saved.State != "unknown" || saved.BusinessKey != row.BusinessKey {
			t.Fatal(saved, err)
		}
		if err := db.First(&cursor, "bot_id = 1").Error; err != nil || cursor.Version != 9 {
			t.Fatal(cursor, err)
		}
		row.ID = 0
		if err := db.Create(&row).Error; err == nil {
			t.Fatal("duplicate reminder cycle allowed")
		}
	}
}

// TestMembershipV18RefundReplay verifies event version and exact audit source IDs survive upgrade replay.
func TestMembershipV18RefundReplay(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "refund-v18.sqlite"))
	if err := migrateTelegramMembershipLifecycle(db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	event := membershipdomain.RefundNotice{RefundID: 5, OrderID: 6, Version: 3, CreatedAt: now}
	audit := membershipdomain.RefundDisposition{RefundID: 5, OrderItemID: 7, ActorAdminID: 8, AccountID: 9, BeneficiaryTelegramID: 9007199254740993, SnapshotHash: "synthetic", SourceIDs: []uint{10, 11}, ExpectedVersion: 2, CreatedAt: now}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&audit).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrateTelegramMembershipLifecycle(db); err != nil {
			t.Fatal(err)
		}
	}
	var saved membershipdomain.RefundDisposition
	if err := db.First(&saved, audit.ID).Error; err != nil || len(saved.SourceIDs) != 2 || saved.SourceIDs[1] != 11 || saved.ExpectedVersion != 2 {
		t.Fatal("audit changed", err)
	}
	var notice membershipdomain.RefundNotice
	if err := db.First(&notice, "refund_id = 5").Error; err != nil || notice.Version != 3 {
		t.Fatal("event version changed", err)
	}
	audit.ID = 0
	if err := db.Create(&audit).Error; err == nil {
		t.Fatal("duplicate disposition accepted")
	}
}

// TestMembershipV18UpgradeReplay preserves v17 unknown evidence while adding conservative lifecycle columns.
func TestMembershipV18UpgradeReplay(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "lifecycle-upgrade.sqlite"))
	if err := migrateTelegramMembershipAccess(db); err != nil {
		t.Fatal(err)
	}
	verifyAccessMigrationReplay(t, db)
	for _, column := range []string{"LifecyclePhase", "BanOutcome", "UnbanOutcome", "BanConfirmed", "BanBlocked", "BanConfirmedAt"} {
		if err := db.Migrator().DropColumn(&membershipdomain.AccessOperation{}, column); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := migrateTelegramMembershipLifecycle(db); err != nil {
			t.Fatal(err)
		}
	}
	var operation membershipdomain.AccessOperation
	if err := db.First(&operation, 1).Error; err != nil {
		t.Fatal(err)
	}
	if operation.State != "unknown" || operation.BanConfirmed || operation.BanBlocked || operation.BanConfirmedAt != nil || operation.LifecyclePhase != "" || operation.BanOutcome != "" {
		t.Fatalf("upgrade fabricated lifecycle evidence: %+v", operation)
	}
	cursor := membershipdomain.LifecycleCursor{BotID: 2, TargetID: 9, ChatID: -10099, TelegramID: 9007199254740993, Version: 8, UpdatedAt: time.Now().UTC()}
	if err := db.Create(&cursor).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&operation).Updates(map[string]any{"ban_confirmed": true, "ban_blocked": true, "lifecycle_phase": "unknown_unban"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := migrateTelegramMembershipLifecycle(db); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&operation, 1).Error; err != nil || !operation.BanConfirmed || !operation.BanBlocked || operation.LifecyclePhase != "unknown_unban" {
		t.Fatal("replay lost provenance", err)
	}
	var saved membershipdomain.LifecycleCursor
	if err := db.First(&saved, "bot_id = ?", 2).Error; err != nil || saved.TelegramID != cursor.TelegramID || saved.Version != 8 {
		t.Fatal("replay reset cursor", err)
	}
}

// TestMembershipV18InterruptedUpgrade checks transactional rollback and retry after partial schema creation.
func TestMembershipV18InterruptedUpgrade(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "lifecycle-interrupted.sqlite"))
	if err := runSchemaMigrations(db, 17, migrateSchema, schemaSteps[:16]...); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(&membershipdomain.LifecycleCursor{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(&membershipdomain.Reminder{}, &membershipdomain.ReminderCursor{}); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("synthetic lifecycle interruption")
	steps := append([]migrationStep(nil), schemaSteps[:17]...)
	steps[len(steps)-1].run = func(tx *gorm.DB) error {
		if err := migrateTelegramMembershipLifecycle(tx); err != nil {
			return err
		}
		return injected
	}
	if err := runSchemaMigrations(db, 18, migrateSchema, steps...); !errors.Is(err, injected) {
		t.Fatalf("expected interruption: %v", err)
	}
	if db.Migrator().HasTable(&membershipdomain.Reminder{}) || db.Migrator().HasTable(&membershipdomain.ReminderCursor{}) {
		t.Fatal("interrupted migration retained reminder schema")
	}
	var version int64
	if err := db.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&version).Error; err != nil || version != 17 {
		t.Fatalf("failed migration advanced ledger: %d %v", version, err)
	}
	for range 2 {
		if err := runSchemaMigrations(db, 18, func(*gorm.DB) error { return errors.New("baseline replayed") }, schemaSteps[:17]...); err != nil {
			t.Fatal(err)
		}
	}
	if !db.Migrator().HasTable(&membershipdomain.Reminder{}) || !db.Migrator().HasTable(&membershipdomain.ReminderCursor{}) {
		t.Fatal("reminder schema missing after interrupted retry")
	}
	if !db.Migrator().HasTable(&membershipdomain.LifecycleCursor{}) {
		t.Fatal("cursor table missing after retry")
	}
}

// TestTopicV15ReplacesSlotIndex 验证旧四列槽位升级、重跑和索引创建前中断后的恢复。
func TestTopicV15ReplacesSlotIndex(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "topic15.sqlite"))
	for _, statement := range []string{
		`CREATE TABLE telegram_support_topic_operations (id INTEGER PRIMARY KEY, bot_id INTEGER NOT NULL, session_id INTEGER NOT NULL, target_id INTEGER NOT NULL, route_version INTEGER NOT NULL, state TEXT, version INTEGER)`,
		`CREATE UNIQUE INDEX idx_support_topic_slot ON telegram_support_topic_operations (bot_id, session_id, target_id, route_version)`,
		`INSERT INTO telegram_support_topic_operations (id, bot_id, session_id, target_id, route_version, state, version) VALUES (1,2,3,4,5,'unknown',7)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := migrateTelegramTopicRecovery(db); err != nil {
			t.Fatal(err)
		}
	}
	var original supportdomain.TopicOperation
	if err := db.First(&original, 1).Error; err != nil {
		t.Fatal(err)
	}
	if original.State != "unknown" || original.Version != 7 || original.SlotVersion != 0 || original.RetryAfterSeconds != 0 || original.RetryNotBefore != nil {
		t.Fatal("upgrade changed original operation or invented retry evidence")
	}
	retryAt := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	replacement := supportdomain.TopicOperation{BotID: 2, SessionID: 3, TargetID: 4, RouteVersion: 5, SlotVersion: 1,
		State: "failed", Version: 1, RetryAfterSeconds: 60, RetryNotBefore: &retryAt}
	if err := db.Create(&replacement).Error; err != nil {
		t.Fatalf("old index still blocks replacement: %v", err)
	}
	duplicate := replacement
	duplicate.ID = 0
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("duplicate replacement slot accepted")
	}
	// 模拟非事务 DDL 删除旧索引后中断；重跑必须补回约束且不清空退避。
	if err := db.Migrator().DropIndex(&supportdomain.TopicOperation{}, "idx_support_topic_slot"); err != nil {
		t.Fatal(err)
	}
	if err := migrateTelegramTopicRecovery(db); err != nil {
		t.Fatal(err)
	}
	var saved supportdomain.TopicOperation
	if err := db.First(&saved, replacement.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.RetryNotBefore == nil || !saved.RetryNotBefore.Equal(retryAt) || saved.RetryAfterSeconds != 60 {
		t.Fatal("replay lost persistent backoff")
	}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("recovery failed to restore unique slot")
	}
}

// TestMembershipV14InterruptedUpgrade 验证最后一步失败不提交版本，重试只执行未完成迁移。
func TestMembershipV14InterruptedUpgrade(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "membership14-interrupted.sqlite"))
	if err := runSchemaMigrations(db, 13, migrateSchema, schemaSteps[:12]...); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(&membershipdomain.ProductTarget{}, &membershipdomain.SKUTerm{}, &membershipdomain.OrderItemSnapshot{}, &membershipdomain.ProductConfig{}); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("synthetic interrupted membership migration")
	steps := append([]migrationStep(nil), schemaSteps[:13]...)
	steps[len(steps)-1].run = func(tx *gorm.DB) error {
		if err := migrateTelegramMembershipCheckout(tx); err != nil {
			return err
		}
		return injected
	}
	if err := runSchemaMigrations(db, 14, migrateSchema, steps...); !errors.Is(err, injected) {
		t.Fatalf("expected interrupted migration, got %v", err)
	}
	var version int64
	if err := db.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&version).Error; err != nil {
		t.Fatal(err)
	}
	if version != 13 {
		t.Fatalf("failed step advanced ledger: %d", version)
	}
	for range 2 {
		if err := runSchemaMigrations(db, schemaVersion, func(*gorm.DB) error { return errors.New("baseline replayed") }, schemaSteps...); err != nil {
			t.Fatal(err)
		}
	}
	if !db.Migrator().HasTable(&membershipdomain.OrderItemSnapshot{}) {
		t.Fatal("retry did not create snapshot table")
	}
}

// TestMembershipV14ReplayPreservesSnapshots 验证新库和已有版本迁移可重跑，且不回填虚构权益。
func TestMembershipV14ReplayPreservesSnapshots(t *testing.T) {
	for _, fresh := range []bool{true, false} {
		name := "upgrade"
		if fresh {
			name = "fresh"
		}
		t.Run(name, func(t *testing.T) {
			db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "membership14.sqlite"))
			if !fresh {
				if err := runSchemaMigrations(db, 13, migrateSchema, schemaSteps[:12]...); err != nil {
					t.Fatal(err)
				}
				// 旧版本基线不应包含本批新增表；模拟正式 v13 库。
				if err := db.Migrator().DropTable(&membershipdomain.ProductTarget{}, &membershipdomain.SKUTerm{}, &membershipdomain.OrderItemSnapshot{}, &membershipdomain.ProductConfig{}); err != nil {
					t.Fatal(err)
				}
			}
			if err := runSchemaMigrations(db, schemaVersion, migrateSchema, schemaSteps...); err != nil {
				t.Fatal(err)
			}
			var count int64
			if err := db.Model(&membershipdomain.OrderItemSnapshot{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("migration invented order snapshots: count=%d err=%v", count, err)
			}
			config := membershipdomain.ProductConfig{ProductID: 7, Version: 3, Quantity: 1}
			if err := db.Create(&config).Error; err != nil {
				t.Fatal(err)
			}
			snapshot := membershipdomain.OrderItemSnapshot{
				OrderItemID: 91, ProductID: 7, SKUID: 8, AccountID: 9,
				BeneficiaryTelegramID: 9007199254740993, IdentityVersion: 12, ConfigVersion: 3,
				Kind: "months", Months: 5, DurationSeconds: 5 * 30 * 24 * 3600, Quantity: 1,
				Targets:   []membershipdomain.SnapshotTarget{{TargetID: 4, RouteVersion: 2, BotInstanceID: 5, TelegramChatID: -1001234567890}},
				CreatedAt: time.Now().UTC().Truncate(time.Second),
			}
			if err := db.Create(&snapshot).Error; err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := migrateTelegramMembershipCheckout(db); err != nil {
					t.Fatal(err)
				}
			}
			var saved membershipdomain.OrderItemSnapshot
			if err := db.First(&saved, "order_item_id = ?", snapshot.OrderItemID).Error; err != nil {
				t.Fatal(err)
			}
			if saved.BeneficiaryTelegramID != snapshot.BeneficiaryTelegramID || saved.ConfigVersion != 3 || saved.Months != 5 || len(saved.Targets) != 1 || saved.Targets[0] != snapshot.Targets[0] {
				t.Fatal("migration changed immutable identity, term or target snapshot")
			}
			var savedConfig membershipdomain.ProductConfig
			if err := db.First(&savedConfig, "product_id = ?", config.ProductID).Error; err != nil {
				t.Fatal(err)
			}
			if savedConfig.Enabled || savedConfig.Version != 3 {
				t.Fatal("migration enabled sales or reset CAS version")
			}
			if err := db.Create(&snapshot).Error; err == nil {
				t.Fatal("duplicate order item snapshot accepted")
			}
		})
	}
}

// accessMigrationModels lists only B6a persistence; no migration fabricates invitations or members.
func accessMigrationModels() []interface{} {
	return []interface{}{&membershipdomain.AccessInvite{}, &membershipdomain.AccessSlot{}, &membershipdomain.AccessJoin{}, &membershipdomain.AccessOperation{}, &membershipdomain.AccessMember{}, &membershipdomain.AccessResult{}}
}

// TestMembershipV17BaselineAndUpgrade checks fresh schema, v16 upgrade, replay, and secret/unknown-state preservation.
func TestMembershipV17BaselineAndUpgrade(t *testing.T) {
	for _, fresh := range []bool{true, false} {
		name := "upgrade"
		if fresh {
			name = "baseline"
		}
		t.Run(name, func(t *testing.T) {
			db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "access.sqlite"))
			if !fresh {
				if err := runSchemaMigrations(db, 16, migrateSchema, schemaSteps[:15]...); err != nil {
					t.Fatal(err)
				}
				if err := db.Migrator().DropTable(accessMigrationModels()...); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if err := runSchemaMigrations(db, schemaVersion, migrateSchema, schemaSteps...); err != nil {
					t.Fatal(err)
				}
			}
			for _, model := range accessMigrationModels() {
				var count int64
				if err := db.Model(model).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("migration fabricated %T: %d %v", model, count, err)
				}
			}
			verifyAccessMigrationReplay(t, db)
		})
	}
}

// verifyAccessMigrationReplay ensures unknown outcomes, encrypted links and observed identity survive idempotent DDL.
func verifyAccessMigrationReplay(t *testing.T, db *gorm.DB) {
	t.Helper()
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	invite := membershipdomain.AccessInvite{ID: 1, AccountID: 8, TelegramID: 9007199254740993, TargetID: 9, BotID: 2, ChatID: -10099, RouteVersion: 3, Generation: 4, BindingVersion: 12, EntitlementVersion: 5, SourceID: 6, State: "unknown", LinkCipher: "synthetic-encrypted-link", ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now}
	operation := membershipdomain.AccessOperation{BusinessKey: "invite:1:create", Kind: "create", InviteID: 1, AccountID: 8, TelegramID: invite.TelegramID, TargetID: 9, BotID: 2, ChatID: invite.ChatID, RouteVersion: 3, Generation: 4, BindingVersion: 12, EntitlementVersion: 5, State: "unknown", Version: 7, NextAttemptAt: now, LeaseUntil: now, CreatedAt: now, UpdatedAt: now}
	rows := []interface{}{&invite, &operation, &membershipdomain.AccessResult{OperationID: 1, LeaseVersion: 7, Generation: 4, Outcome: "unknown", LinkCipher: "synthetic-encrypted-result", ReceivedAt: now}, &membershipdomain.AccessSlot{TelegramID: invite.TelegramID, TargetID: 9, InviteID: 1, Version: 7}, &membershipdomain.AccessJoin{BotID: 2, UpdateID: 101, ChatID: invite.ChatID, TelegramID: invite.TelegramID, InviteID: 1, Decision: "approve", State: "unknown", ReceivedAt: now, UpdatedAt: now}, &membershipdomain.AccessMember{TargetID: 9, ChatID: invite.ChatID, TelegramID: invite.TelegramID, BotID: 2, RouteVersion: 3, SourceID: 6, Status: "restricted", IsMember: true, ObservedAt: now, Sequence: 102, ObservationSource: "chat_member", ManagedAt: now}}
	for _, row := range rows {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := migrateTelegramMembershipAccess(db); err != nil {
			t.Fatal(err)
		}
	}
	var saved membershipdomain.AccessInvite
	if err := db.First(&saved, 1).Error; err != nil {
		t.Fatal(err)
	}
	if saved.BindingVersion != invite.BindingVersion || saved.State != "unknown" || saved.LinkCipher != invite.LinkCipher || saved.TelegramID != invite.TelegramID || !saved.ExpiresAt.Equal(invite.ExpiresAt) {
		t.Fatal("migration changed invitation identity, secret, expiry or unknown evidence")
	}
	for _, model := range accessMigrationModels() {
		var count int64
		if err := db.Model(model).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("replay changed %T: %d %v", model, count, err)
		}
	}
	var savedOperation membershipdomain.AccessOperation
	if err := db.First(&savedOperation, operation.ID).Error; err != nil || savedOperation.BindingVersion != 12 || savedOperation.EntitlementVersion != 5 || savedOperation.State != "unknown" || savedOperation.Version != 7 {
		t.Fatalf("migration changed operation authorization/evidence: %+v %v", savedOperation, err)
	}
	operation.ID = 0
	if err := db.Create(&operation).Error; err == nil {
		t.Fatal("duplicate operation business key accepted")
	}
}

// TestMembershipV17AdminRecoveryColumns preserves unknown facts while adding conservative recovery metadata.
func TestMembershipV17AdminRecoveryColumns(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "access-admin.sqlite"))
	if err := migrateTelegramMembershipAccess(db); err != nil {
		t.Fatal(err)
	}
	verifyAccessMigrationReplay(t, db)
	columns := []struct {
		model any
		names []string
	}{
		{&membershipdomain.AccessInvite{}, []string{"ExpiryTrusted"}},
		{&membershipdomain.AccessOperation{}, []string{"LastAttemptAt"}},
		{&membershipdomain.AccessJoin{}, []string{"ActorAdminID", "ReviewReason", "ReviewedAt"}},
		{&membershipdomain.AccessResult{}, []string{"ActorAdminID", "Action", "Reason", "ErrorClass"}},
	}
	for _, item := range columns {
		for _, name := range item.names {
			if err := db.Migrator().DropColumn(item.model, name); err != nil {
				t.Fatal(err)
			}
		}
	}
	for range 2 {
		if err := migrateTelegramMembershipAccess(db); err != nil {
			t.Fatal(err)
		}
	}
	var invite membershipdomain.AccessInvite
	if err := db.First(&invite, 1).Error; err != nil {
		t.Fatal(err)
	}
	if invite.ExpiryTrusted {
		t.Fatal("legacy expiry was falsely trusted")
	}
	var receipt membershipdomain.AccessResult
	if err := db.First(&receipt, "operation_id = ?", 1).Error; err != nil || receipt.ErrorClass != "" || receipt.Outcome != "unknown" {
		t.Fatal("upgrade fabricated error evidence", err)
	}
	if err := db.Model(&receipt).Update("error_class", "invalid_response").Error; err != nil {
		t.Fatal(err)
	}
	if err := migrateTelegramMembershipAccess(db); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&receipt, "operation_id = ?", 1).Error; err != nil || receipt.ErrorClass != "invalid_response" {
		t.Fatal("replay lost negative evidence", err)
	}
	for _, item := range columns {
		for _, name := range item.names {
			if !db.Migrator().HasColumn(item.model, name) {
				t.Fatalf("missing recovery column %s", name)
			}
		}
	}
}

// TestMembershipV17BindingColumnsUpgrade 验证未发布 v17 旧表增加绑定代次时不伪造历史授权。
// 使用真实旧列结构升级两次，旧任务保留零值，新记录绑定和权益版本原样保留。
func TestMembershipV17BindingColumnsUpgrade(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "access-binding.sqlite"))
	if err := migrateTelegramMembershipAccess(db); err != nil {
		t.Fatal(err)
	}
	verifyAccessMigrationReplay(t, db)
	for _, model := range []any{&membershipdomain.AccessInvite{}, &membershipdomain.AccessOperation{}} {
		if err := db.Migrator().DropColumn(model, "BindingVersion"); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := migrateTelegramMembershipAccess(db); err != nil {
			t.Fatal(err)
		}
	}
	var invite membershipdomain.AccessInvite
	var operation membershipdomain.AccessOperation
	if err := db.First(&invite, 1).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&operation, 1).Error; err != nil {
		t.Fatal(err)
	}
	if invite.BindingVersion != 0 || operation.BindingVersion != 0 || invite.State != "unknown" || operation.State != "unknown" || invite.EntitlementVersion != 5 || operation.EntitlementVersion != 5 || invite.LinkCipher != "synthetic-encrypted-link" {
		t.Fatalf("upgrade invented authorization or lost evidence: %+v %+v", invite, operation)
	}
	for _, model := range []any{&invite, &operation} {
		if err := db.Model(model).Update("binding_version", 37).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateTelegramMembershipAccess(db); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&invite, 1).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&operation, 1).Error; err != nil {
		t.Fatal(err)
	}
	if invite.BindingVersion != 37 || operation.BindingVersion != 37 {
		t.Fatal("replay reset persisted binding generation")
	}
}

// TestMembershipV17ReconcileColumnsUpgrade verifies additive read-task fields without inventing historical conclusions.
// Uses an old operation shape and replays v17 twice; original unknown status and binding remain unchanged.
func TestMembershipV17ReconcileColumnsUpgrade(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "access-reconcile.sqlite"))
	if err := migrateTelegramMembershipAccess(db); err != nil {
		t.Fatal(err)
	}
	verifyAccessMigrationReplay(t, db)
	model := &membershipdomain.AccessOperation{}
	if err := db.Migrator().DropIndex(model, "idx_telegram_membership_access_operations_parent_operation_id"); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"ParentOperationID", "ReviewReason", "MemberStatus", "MemberIsMember", "ObservedAt"} {
		if err := db.Migrator().DropColumn(model, column); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := migrateTelegramMembershipAccess(db); err != nil {
			t.Fatal(err)
		}
	}
	var row membershipdomain.AccessOperation
	if err := db.First(&row, 1).Error; err != nil {
		t.Fatal(err)
	}
	if row.State != "unknown" || row.ParentOperationID != 0 || row.MemberStatus != "" || row.ObservedAt != nil || row.MemberIsMember || row.EntitlementVersion != 5 {
		t.Fatalf("upgrade invented an observation: %+v", row)
	}
	if err := db.Model(&row).Updates(map[string]any{"review_reason": "member_without_entitlement", "member_status": "restricted", "member_is_member": true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := migrateTelegramMembershipAccess(db); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&row, 1).Error; err != nil || row.ReviewReason != "member_without_entitlement" || !row.MemberIsMember || row.MemberStatus != "restricted" {
		t.Fatalf("migration reset reconciliation: %+v %v", row, err)
	}
}

// TestMembershipV17InterruptedUpgrade verifies an interrupted DDL step never advances the migration ledger.
func TestMembershipV17InterruptedUpgrade(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "access-interrupted.sqlite"))
	if err := runSchemaMigrations(db, 16, migrateSchema, schemaSteps[:15]...); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(accessMigrationModels()...); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("synthetic access migration interruption")
	steps := append([]migrationStep(nil), schemaSteps[:16]...)
	steps[len(steps)-1].run = func(tx *gorm.DB) error {
		if err := migrateTelegramMembershipAccess(tx); err != nil {
			return err
		}
		return injected
	}
	if err := runSchemaMigrations(db, 17, migrateSchema, steps...); !errors.Is(err, injected) {
		t.Fatalf("expected interruption: %v", err)
	}
	var version int64
	if err := db.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&version).Error; err != nil {
		t.Fatal(err)
	}
	if version != 16 {
		t.Fatalf("failure advanced ledger to %d", version)
	}
	if err := runSchemaMigrations(db, schemaVersion, func(*gorm.DB) error { return errors.New("baseline replayed") }, schemaSteps...); err != nil {
		t.Fatal(err)
	}
	for _, model := range accessMigrationModels() {
		if !db.Migrator().HasTable(model) {
			t.Fatalf("missing %T", model)
		}
	}
}

// ledgerMigrationModels 返回 B5 持久模型，供迁移测试核对新增表与空基线。
func ledgerMigrationModels() []interface{} {
	return []interface{}{&membershipdomain.LedgerSource{}, &membershipdomain.LedgerRevocation{}, &membershipdomain.LedgerProjection{}, &membershipdomain.LedgerOutbox{}, &membershipdomain.PaidEvent{}, &fulfillment.ItemReceipt{}}
}

// TestMembershipV16BaselineAndUpgrade 验证新库与 v15 增量创建一致，且不虚构权益。
func TestMembershipV16BaselineAndUpgrade(t *testing.T) {
	for _, fresh := range []bool{true, false} {
		name := "upgrade"
		if fresh {
			name = "baseline"
		}
		t.Run(name, func(t *testing.T) {
			db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "ledger.sqlite"))
			if !fresh {
				if err := runSchemaMigrations(db, 15, migrateSchema, schemaSteps[:14]...); err != nil {
					t.Fatal(err)
				}
				if err := db.Migrator().DropTable(ledgerMigrationModels()...); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if err := runSchemaMigrations(db, schemaVersion, migrateSchema, schemaSteps...); err != nil {
					t.Fatal(err)
				}
			}
			for _, model := range ledgerMigrationModels() {
				var count int64
				if err := db.Model(model).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("model=%T count=%d err=%v", model, count, err)
				}
			}
			verifyLedgerMigrationReplay(t, db)
		})
	}
}

// verifyLedgerMigrationReplay 检验来源唯一约束、墓碑、固定付款时间及永久投影重跑不变。
func verifyLedgerMigrationReplay(t *testing.T, db *gorm.DB) {
	t.Helper()
	paid := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	source := membershipdomain.LedgerSource{OrderItemID: 31, TargetID: 9, Operation: "grant", BeneficiaryTelegramID: 9007199254740993, AccountID: 8, OrderID: 10, PaymentOrderID: 10, PaymentID: 20, PaidAt: paid, Kind: "permanent", SnapshotHash: "synthetic-hash", CreatedAt: paid}
	projection := membershipdomain.LedgerProjection{BeneficiaryTelegramID: source.BeneficiaryTelegramID, TargetID: 9, Version: 7, Active: true, Kind: "permanent", CalculatedAt: paid}
	rows := []interface{}{
		&source, &projection,
		&membershipdomain.LedgerRevocation{OrderItemID: 32, TargetID: 9, BeneficiaryTelegramID: source.BeneficiaryTelegramID, RevokedAt: paid},
		&membershipdomain.LedgerOutbox{OrderItemID: 31, TargetID: 9, Operation: "grant", BeneficiaryTelegramID: source.BeneficiaryTelegramID, ProjectionVersion: 7, State: "pending", AvailableAt: paid, CreatedAt: paid, UpdatedAt: paid},
		&membershipdomain.PaidEvent{OrderID: 10, PaymentID: 20, PaidAt: paid, AvailableAt: paid, Attempts: 3},
		&fulfillment.ItemReceipt{OrderItemID: 31, OrderID: 10, Kind: "membership", Status: "entitlement_recorded", PaidAt: paid, RecordedAt: paid},
	}
	for _, row := range rows {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := migrateTelegramMembershipLedger(db); err != nil {
			t.Fatal(err)
		}
	}
	var saved membershipdomain.LedgerSource
	if err := db.First(&saved, source.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.SnapshotHash != source.SnapshotHash || !saved.PaidAt.Equal(paid) || saved.BeneficiaryTelegramID != source.BeneficiaryTelegramID {
		t.Fatal("source changed on replay")
	}
	var effective membershipdomain.LedgerProjection
	if err := db.First(&effective).Error; err != nil {
		t.Fatal(err)
	}
	if effective.Version != 7 || !effective.Active || effective.ExpiresAt != nil || effective.Kind != "permanent" {
		t.Fatal("projection changed on replay")
	}
	for _, model := range ledgerMigrationModels() {
		var count int64
		if err := db.Model(model).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("replay changed %T: count=%d err=%v", model, count, err)
		}
	}
	source.ID = 0
	source.BeneficiaryTelegramID++
	if err := db.Create(&source).Error; err == nil {
		t.Fatal("duplicate grant accepted by changing beneficiary")
	}
}

// TestMembershipV16InterruptedUpgrade 检验 DDL 失败不会推进版本且重跑能完整恢复。
func TestMembershipV16InterruptedUpgrade(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "ledger-interrupted.sqlite"))
	if err := runSchemaMigrations(db, 15, migrateSchema, schemaSteps[:14]...); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(ledgerMigrationModels()...); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("synthetic ledger migration interruption")
	steps := append([]migrationStep(nil), schemaSteps[:15]...)
	steps[len(steps)-1].run = func(tx *gorm.DB) error {
		if err := migrateTelegramMembershipLedger(tx); err != nil {
			return err
		}
		return injected
	}
	if err := runSchemaMigrations(db, 16, migrateSchema, steps...); !errors.Is(err, injected) {
		t.Fatalf("expected interruption: %v", err)
	}
	var version int64
	if err := db.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&version).Error; err != nil {
		t.Fatal(err)
	}
	if version != 15 {
		t.Fatalf("failure advanced version to %d", version)
	}
	if err := runSchemaMigrations(db, schemaVersion, func(*gorm.DB) error { return errors.New("baseline replayed") }, schemaSteps...); err != nil {
		t.Fatal(err)
	}
	for _, model := range ledgerMigrationModels() {
		if !db.Migrator().HasTable(model) {
			t.Fatalf("missing %T after retry", model)
		}
	}
}
