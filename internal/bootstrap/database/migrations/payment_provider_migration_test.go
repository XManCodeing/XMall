package migrations

import (
	"bufio"
	"errors"
	"fmt"
	groupsdomain "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	groupsstore "github.com/XManCodeing/XMall/internal/modules/telegram/groups/infrastructure/gormstore"
	membershiprouterecovery "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore/routerecovery"
	supportdomain "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	paymentdomain "github.com/XManCodeing/XMall/internal/modules/payment/domain"
	settingsstore "github.com/XManCodeing/XMall/internal/modules/settings/infrastructure/gormstore"
	"github.com/XManCodeing/XMall/internal/platform/database/gormdb"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"github.com/XManCodeing/XMall/internal/shared/money"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// TestRecoveryTargetsV22Upgrade checks registered upgrade rollback, replay and legacy proof preservation.
// t owns a disposable SQLite database; no runtime or platform operation is invoked.
func TestRecoveryTargetsV22Upgrade(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "recovery-v22.sqlite"))
	legacy := "telegram_group_recovery_fences"
	if err := db.Exec(`CREATE TABLE telegram_group_recovery_fences (
		task_id INTEGER PRIMARY KEY, target_id INTEGER NOT NULL,
		old_bot_id INTEGER, old_chat_id INTEGER, new_bot_id INTEGER, new_chat_id INTEGER,
		initial_version INTEGER, initial_route_version INTEGER, frozen_version INTEGER,
		replaced_version INTEGER, released_version INTEGER, released_observation_seq INTEGER,
		released_generation INTEGER, released_bot_telegram_id INTEGER)`).Error; err != nil {
		t.Fatal(err)
	}
	original := groupsdomain.RecoveryFence{TaskID: 7, TargetID: 3, OldBotID: 1, NewBotID: 2, OldChatID: -101, NewChatID: -102, InitialVersion: 4, InitialRouteVersion: 5, FrozenVersion: 5, ReplacedVersion: 6, ReleasedVersion: 7, ReleasedGeneration: 9, ReleasedObservationSeq: 11, ReleasedBotTelegramID: 123}
	if err := db.Table(legacy).Create(&original).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, completed_at VARCHAR(40) NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO schema_migrations VALUES (21, 'synthetic')").Error; err != nil {
		t.Fatal(err)
	}
	injected := errors.New("synthetic recovery migration interruption")
	steps := append([]migrationStep(nil), schemaSteps...)
	steps[20].run = func(tx *gorm.DB) error {
		if err := groupsstore.MigrateRecoveryTargets(tx); err != nil {
			return err
		}
		return injected
	}
	if err := runSchemaMigrations(db, schemaVersion, migrateSchema, steps...); !errors.Is(err, injected) {
		t.Fatalf("interruption: %v", err)
	}
	var version int64
	if err := db.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&version).Error; err != nil || version != 21 {
		t.Fatalf("failed upgrade advanced ledger: %d %v", version, err)
	}
	for range 2 {
		if err := runSchemaMigrations(db, schemaVersion, migrateSchema, schemaSteps...); err != nil {
			t.Fatal(err)
		}
	}
	for _, model := range membershiprouterecovery.Models() {
		if !db.Migrator().HasTable(model) {
			t.Fatalf("registered v22 upgrade omitted membership recovery model %T", model)
		}
		var count int64
		if err := db.Model(model).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("upgrade fabricated membership recovery evidence: %T %d %v", model, count, err)
		}
	}
	var actual groupsdomain.RecoveryFence
	if err := db.Where("task_id = ? AND target_id = ?", 7, 3).Take(&actual).Error; err != nil || !reflect.DeepEqual(original, actual) {
		t.Fatalf("proof changed: %+v %v", actual, err)
	}
	second := original
	second.TargetID = 4
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("same parent second target rejected: %v", err)
	}
	if err := groupsstore.MigrateRecoveryTargets(db); err != nil {
		t.Fatal(err)
	}
	var preserved groupsdomain.RecoveryFence
	if err := db.Table(legacy).Take(&preserved).Error; err != nil || !reflect.DeepEqual(original, preserved) {
		t.Fatalf("legacy receipt changed: %+v %v", preserved, err)
	}
}

// TestSupportRestorationV22PreservesBufferedEvidence verifies additive defaults and replay.
// t owns synthetic legacy rows; migration must neither retire nor redeliver messages.
func TestSupportRestorationV22PreservesBufferedEvidence(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "support-restoration-v22.sqlite"))
	if err := db.AutoMigrate(&supportdomain.BufferedMessage{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&supportdomain.BufferedMessage{}, "legacy_task_id"); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO telegram_support_messages (id, bot_id, update_id, session_id, sequence, state, payload_cipher) VALUES (1, 8, 17, 9, 1, 'unknown', 'synthetic-retained')").Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrateTelegramRecoveryContinuation(db); err != nil {
			t.Fatal(err)
		}
	}
	var row supportdomain.BufferedMessage
	if err := db.First(&row, 1).Error; err != nil {
		t.Fatal(err)
	}
	if row.LegacyTaskID != 0 || row.State != "unknown" || row.PayloadCipher != "synthetic-retained" {
		t.Fatalf("migration changed historical message: %+v", row)
	}
	for _, model := range []any{&supportdomain.RestorationCheckpoint{}, &supportdomain.RestorationEntry{}} {
		if !db.Migrator().HasTable(model) {
			t.Fatalf("missing restoration model %T", model)
		}
		var count int64
		if err := db.Model(model).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("migration fabricated restoration: %T %d %v", model, count, err)
		}
	}
}

// TestSupportDownstreamV23Upgrade preserves aggregate-only history across interruption and replay.
// t owns a disposable single-connection database; no delivery or real data is touched.
func TestSupportDownstreamV23Upgrade(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "support-downstream-v23.sqlite"))
	for _, statement := range []string{
		`CREATE TABLE telegram_support_restoration_entries (id INTEGER PRIMARY KEY, task_id INTEGER NOT NULL, target_id INTEGER NOT NULL, source_session_id INTEGER NOT NULL, destination_session_id INTEGER, operation_id INTEGER, state TEXT, legacy INTEGER, created_at DATETIME, updated_at DATETIME)`,
		`INSERT INTO telegram_support_restoration_entries (id, task_id, target_id, source_session_id, state, legacy) VALUES (1, 7, 3, 9, 'succeeded', 4)`,
		`CREATE TABLE telegram_support_restorations (task_id INTEGER, target_id INTEGER, revision INTEGER, cursor INTEGER, upper_bound INTEGER, scanned NUMERIC, PRIMARY KEY(task_id, target_id))`,
		`INSERT INTO telegram_support_restorations (task_id, target_id, revision, cursor, upper_bound, scanned) VALUES (7, 3, 6, 9, 9, 1)`,
		`CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, completed_at VARCHAR(40) NOT NULL)`,
		`INSERT INTO schema_migrations VALUES (22, 'synthetic')`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	injected := errors.New("synthetic downstream migration interruption")
	steps := append([]migrationStep(nil), schemaSteps...)
	steps[len(steps)-1].run = func(tx *gorm.DB) error {
		if err := migrateTelegramSupportDownstreamLiabilities(tx); err != nil {
			return err
		}
		return injected
	}
	if err := runSchemaMigrations(db, schemaVersion, migrateSchema, steps...); !errors.Is(err, injected) {
		t.Fatalf("interruption: %v", err)
	}
	var version int64
	if err := db.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&version).Error; err != nil || version != 22 {
		t.Fatalf("failed upgrade advanced ledger: %d %v", version, err)
	}
	for range 2 {
		if err := runSchemaMigrations(db, schemaVersion, migrateSchema, schemaSteps...); err != nil {
			t.Fatal(err)
		}
		if err := migrateTelegramSupportDownstreamLiabilities(db); err != nil {
			t.Fatal(err)
		}
	}
	var entry supportdomain.RestorationEntry
	if err := db.First(&entry, 1).Error; err != nil {
		t.Fatal(err)
	}
	if entry.TaskID != 7 || entry.TargetID != 3 || entry.SourceSessionID != 9 || entry.State != "succeeded" || entry.Legacy != 4 || entry.DownstreamTrackingVersion != 0 || entry.DownstreamUpperBound != 0 || entry.DownstreamCursor != 0 || entry.DownstreamScanned {
		t.Fatalf("historical aggregate changed: %+v", entry)
	}
	var checkpoint supportdomain.RestorationCheckpoint
	if err := db.Where("task_id = ? AND target_id = ?", 7, 3).Take(&checkpoint).Error; err != nil {
		t.Fatal(err)
	}
	if checkpoint.Revision != 6 || checkpoint.Cursor != 9 || checkpoint.UpperBound != 9 || !checkpoint.Scanned || checkpoint.DownstreamReceiptCursor != 0 {
		t.Fatalf("historical checkpoint changed: %+v", checkpoint)
	}
	var count int64
	if err := db.Model(&supportdomain.RestorationDownstreamLiability{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("fabricated downstream ownership: %d %v", count, err)
	}
	assertDownstreamLiabilityIndexes(t, db)
}

// assertDownstreamLiabilityIndexes checks source uniqueness and bounded-page indexing.
// t reports failures; db is an already migrated disposable database. No value is returned.
func assertDownstreamLiabilityIndexes(t *testing.T, db *gorm.DB) {
	t.Helper()
	model := &supportdomain.RestorationDownstreamLiability{}
	for _, name := range []string{"idx_support_downstream_liability_source", "idx_support_downstream_liability_page"} {
		if !db.Migrator().HasIndex(model, name) {
			t.Fatalf("missing liability index %s", name)
		}
	}
	original := supportdomain.RestorationDownstreamLiability{TaskID: 7, TargetID: 3, EntryID: 1, DownstreamID: 11, SourceVersion: 2, State: "pending", Version: 1}
	if err := db.Create(&original).Error; err != nil {
		t.Fatal(err)
	}
	duplicate := original
	duplicate.ID, duplicate.SourceVersion = 0, 3
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("source version change allowed double counting")
	}
	duplicate.TaskID = 8
	if err := db.Create(&duplicate).Error; err != nil {
		t.Fatalf("independent task rejected: %v", err)
	}
}

// TestVersionRunnerKilledProcessRecoversLockAndLedger kills a lock-owning child.
// t owns the SQLite file; committed v1 survives while uncommitted v2/v3 roll back.
func TestVersionRunnerKilledProcessRecoversLockAndLedger(t *testing.T) {
	if len(os.Args) >= 3 && os.Args[len(os.Args)-2] == "xmall-kill-child" {
		runKilledMigrationChild(t, os.Args[len(os.Args)-1])
		return
	}
	path := filepath.Join(t.TempDir(), "killed.sqlite")
	db := openVersionRunnerDB(t, path)
	if err := runSchemaMigrations(db, 1, func(tx *gorm.DB) error {
		if err := tx.Exec("CREATE TABLE kill_probe (id INTEGER PRIMARY KEY, value INTEGER NOT NULL)").Error; err != nil {
			return err
		}
		return tx.Exec("INSERT INTO kill_probe VALUES (1, 10)").Error
	}); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestVersionRunnerKilledProcessRecoversLockAndLedger$", "-test.timeout=30s", "--", "xmall-kill-child", path)
	command.Env = append(os.Environ(), "TEST_MYSQL_DSN=", "TEST_POSTGRES_DSN=", "XMALL_MIGRATION_TEST_DB=")
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = command.Stdout
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				t.Errorf("cleanup migration child: %v", err)
			}
			if err := command.Wait(); err != nil {
				t.Logf("reaped interrupted migration child: %v", err)
			}
		}
	})
	ready := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			if strings.TrimSpace(scanner.Text()) == "MIGRATION_LOCK_HELD" {
				ready <- nil
				return
			}
		}
		ready <- fmt.Errorf("child exited before holding migration lock: %v", scanner.Err())
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for child migration lock")
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatalf("kill lock-owning child: %v", err)
	}
	err = command.Wait()
	waited = true
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ProcessState.Success() {
		t.Fatalf("child did not terminate abnormally: %v", err)
	}
	reopened := openVersionRunnerDB(t, path)
	assertKilledMigrationState(t, reopened, 1, 10)
	steps := killedMigrationSteps(nil)
	for range 2 {
		if err := runSchemaMigrations(reopened, 3, func(*gorm.DB) error {
			return errors.New("recovery reran committed baseline")
		}, steps...); err != nil {
			t.Fatalf("recover/replay after kill: %v", err)
		}
		assertKilledMigrationState(t, reopened, 3, 12)
	}
}

// runKilledMigrationChild runs an interrupted upgrade on path, supplied by its parent.
// t reports setup failures; readiness is emitted only with v2's ledger still uncommitted.
func runKilledMigrationChild(t *testing.T, path string) {
	t.Helper()
	db := openVersionRunnerDB(t, path)
	steps := killedMigrationSteps(func(tx *gorm.DB) error {
		var version int64
		if err := tx.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&version).Error; err != nil {
			return err
		}
		if version != 2 {
			return fmt.Errorf("child expected uncommitted ledger v2, got %d", version)
		}
		if _, err := fmt.Fprintln(os.Stdout, "MIGRATION_LOCK_HELD"); err != nil {
			return err
		}
		time.Sleep(time.Hour) // Parent must kill this process, never unwind its defers.
		return errors.New("child unexpectedly resumed")
	})
	if err := runSchemaMigrations(db, 3, func(*gorm.DB) error {
		return errors.New("child reran baseline")
	}, steps...); err != nil {
		t.Fatal(err)
	}
}

// killedMigrationSteps returns synthetic upgrades, with pause invoked inside v3.
// A nil pause completes normally; these probes test runner recovery, not v9 history.
func killedMigrationSteps(pause func(*gorm.DB) error) []migrationStep {
	increment := func(tx *gorm.DB) error {
		return tx.Exec("UPDATE kill_probe SET value = value + 1 WHERE id = 1").Error
	}
	return []migrationStep{
		{2, "kill recovery first increment", increment},
		{3, "kill recovery second increment", func(tx *gorm.DB) error {
			if err := increment(tx); err != nil {
				return err
			}
			if pause != nil {
				return pause(tx)
			}
			return nil
		}},
	}
}

// assertKilledMigrationState checks db's durable ledger and business probe.
// t receives failures; version and value are the exact expected committed values.
func assertKilledMigrationState(t *testing.T, db *gorm.DB, version, value int64) {
	t.Helper()
	var versions []int64
	if err := db.Raw("SELECT version FROM schema_migrations ORDER BY version").Scan(&versions).Error; err != nil {
		t.Fatal(err)
	}
	if int64(len(versions)) != version {
		t.Fatalf("unexpected ledger rows: %v, want 1..%d", versions, version)
	}
	for index, actual := range versions {
		if actual != int64(index+1) {
			t.Fatalf("noncontiguous recovered ledger: %v", versions)
		}
	}
	var actual int64
	if err := db.Raw("SELECT value FROM kill_probe WHERE id = 1").Scan(&actual).Error; err != nil || actual != value {
		t.Fatalf("recovered value=%d want=%d err=%v", actual, value, err)
	}
}

// TestAuditSafetyV46OnlyRecordsVersion freezes the registered v46 step, not the latest schema.
// t owns a synthetic v45 DB; the runner must preserve all DDL, evidence and ledger timestamps.
func TestAuditSafetyV46OnlyRecordsVersion(t *testing.T) {
	testBehaviorOnlyMigration(t, 45, 46)
}

// TestStoreSafetyV45PreservesSchemaAndHistory freezes v45 before later additive migrations.
// t owns a synthetic v44 DB; the production runner must only append the v45 marker.
func TestStoreSafetyV45PreservesSchemaAndHistory(t *testing.T) {
	testBehaviorOnlyMigration(t, 44, 45)
}

// Frozen runtime v9 source: 75379d7cfa87e5711471b07d3ae311584d477df4,
// internal/modules/telegram/runtime/domain/{models,media_cache}.go.
// Field types, GORM tags and table names are copied, not derived from current models.
// Only these SIX runtime tables are frozen: this is NOT a full-business v9 schema.
type runtimeV9Instance struct {
	ID                                     uint   `gorm:"primarykey"`
	TelegramID                             int64  `gorm:"uniqueIndex;not null"`
	Username                               string `gorm:"size:128"`
	TokenCipher                            string `gorm:"type:text;not null"`
	Enabled                                bool
	Mode                                   string `gorm:"size:24;not null;default:'internal'"`
	Version                                uint64 `gorm:"not null;default:1"`
	AppliedVersion                         uint64
	Generation                             uint64 `gorm:"not null;default:0"`
	Offset                                 int64
	State                                  string `gorm:"size:32;not null;default:'stopped'"`
	LastError                              string `gorm:"size:300"`
	ProfileName                            string `gorm:"size:64"`
	ProfileDescription                     string `gorm:"type:text"`
	ProfileCoverURL                        string `gorm:"size:512"`
	ProfileVersion                         uint64 `gorm:"not null;default:1"`
	ProfileSyncError                       string `gorm:"size:300"`
	LastSeenAt, LastReceivedAt, LastSentAt *time.Time
	CreatedAt, UpdatedAt                   time.Time
}

// TableName returns the historical table; the receiver contains no naming input.
func (runtimeV9Instance) TableName() string { return "telegram_bot_instances" }

type runtimeV9Contact struct {
	ID                   uint   `gorm:"primarykey"`
	BotID                uint   `gorm:"uniqueIndex:idx_bot_contact;not null"`
	TelegramUserID       int64  `gorm:"uniqueIndex:idx_bot_contact;not null"`
	ChatID               int64  `gorm:"not null"`
	UserID               uint   `gorm:"index"`
	Reachable            bool   `gorm:"not null"`
	Locale               string `gorm:"size:16"`
	CreatedAt, UpdatedAt time.Time
}

// TableName returns the historical table; the receiver contains no naming input.
func (runtimeV9Contact) TableName() string { return "telegram_bot_contacts" }

type runtimeV9Inbox struct {
	ID                   uint   `gorm:"primarykey"`
	BotID                uint   `gorm:"uniqueIndex:idx_bot_update;not null;index"`
	UpdateID             int64  `gorm:"uniqueIndex:idx_bot_update;not null"`
	PayloadCipher        string `gorm:"type:text;not null"`
	Status               string `gorm:"size:24;index;not null"`
	Attempts             int
	AvailableAt          time.Time `gorm:"index"`
	ClaimedUntil         *time.Time
	Claim                string `gorm:"size:64"`
	LastError            string `gorm:"size:300"`
	CreatedAt, UpdatedAt time.Time
}

// TableName returns the historical table; the receiver contains no naming input.
func (runtimeV9Inbox) TableName() string { return "telegram_bot_inbox" }

type runtimeV9Session struct {
	ID                              uint   `gorm:"primarykey"`
	BotID                           uint   `gorm:"uniqueIndex:idx_bot_session;not null"`
	TelegramUserID                  int64  `gorm:"uniqueIndex:idx_bot_session;not null"`
	ChatID                          int64  `gorm:"uniqueIndex:idx_bot_session;not null"`
	Version                         uint64 `gorm:"not null"`
	State                           string `gorm:"size:64"`
	DataCipher                      string `gorm:"type:text"`
	PanelEpoch                      string `gorm:"size:64"`
	PanelRevision                   uint64 `gorm:"not null;default:0"`
	PanelMessageID                  int64  `gorm:"not null;default:0"`
	PanelMessageEpoch               string `gorm:"size:64"`
	PanelKind                       string `gorm:"size:24"`
	PanelState                      string `gorm:"size:24"`
	PanelDeliveredRevision          uint64 `gorm:"not null;default:0"`
	ExpiresAt, CreatedAt, UpdatedAt time.Time
}

// TableName returns the historical table; the receiver contains no naming input.
func (runtimeV9Session) TableName() string { return "telegram_bot_sessions" }

type runtimeV9Delivery struct {
	ID                   uint   `gorm:"primarykey"`
	BotID                uint   `gorm:"uniqueIndex:idx_bot_delivery_key;not null;index"`
	Key                  string `gorm:"size:191;uniqueIndex:idx_bot_delivery_key;not null"`
	ChatID               int64  `gorm:"index"`
	TelegramUserID       int64
	UserID               uint
	BroadcastID          uint   `gorm:"index"`
	PayloadCipher        string `gorm:"type:text"`
	Priority             int    `gorm:"index"`
	Status               string `gorm:"size:24;index"`
	Attempts             int
	AvailableAt          time.Time `gorm:"index"`
	ClaimedUntil         *time.Time
	Claim                string `gorm:"size:64"`
	Purpose              string `gorm:"size:24;index"`
	Operation            string `gorm:"size:24"`
	PanelEpoch           string `gorm:"size:64;index"`
	PanelRevision        uint64 `gorm:"not null;default:0"`
	TargetMessageID      int64  `gorm:"not null;default:0"`
	PreviousMessageID    int64  `gorm:"not null;default:0"`
	MessageKind          string `gorm:"size:24"`
	MessageID            int64
	LastError            string `gorm:"size:300"`
	CreatedAt, UpdatedAt time.Time
}

// TableName returns the historical table; the receiver contains no naming input.
func (runtimeV9Delivery) TableName() string { return "telegram_bot_deliveries" }

type runtimeV9MediaCache struct {
	ID          uint   `gorm:"primarykey"`
	BotID       uint   `gorm:"uniqueIndex:idx_bot_media;not null"`
	PathHash    string `gorm:"size:64;uniqueIndex:idx_bot_media;not null"`
	ContentHash string `gorm:"size:64;uniqueIndex:idx_bot_media;not null"`
	FileID      string `gorm:"type:text;not null"`
	UpdatedAt   time.Time
}

// TableName returns the historical table; the receiver contains no naming input.
func (runtimeV9MediaCache) TableName() string { return "telegram_bot_media_cache" }

// TestFrozenRuntimeV9SQLiteUpgrade runs production steps against six historical tables.
// t owns a single-connection temporary SQLite DB; no services or full-business baseline run.
func TestFrozenRuntimeV9SQLiteUpgrade(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "frozen-runtime-v9.sqlite"))
	models := []any{&runtimeV9Instance{}, &runtimeV9Contact{}, &runtimeV9Inbox{}, &runtimeV9Session{}, &runtimeV9Delivery{}, &runtimeV9MediaCache{}}
	if err := db.AutoMigrate(models...); err != nil {
		t.Fatal(err)
	}
	for _, model := range models {
		assertRuntimeV9Shape(t, db, model)
		seedRuntimeV9Row(t, db, model)
	}
	for _, column := range []string{"payload_held", "payload_retain_until", "target_type", "receipt_claim", "message_ids", "recovery_task_id", "recovery_version", "recovery_generation"} {
		for _, model := range models {
			if db.Migrator().HasColumn(model, column) {
				t.Fatalf("v9 fixture contains later column %T.%s", model, column)
			}
		}
	}
	for _, query := range []string{"CREATE TABLE schema_migrations (version BIGINT NOT NULL PRIMARY KEY, completed_at VARCHAR(40) NOT NULL)", "INSERT INTO schema_migrations VALUES (9, 'frozen-runtime-v9')"} {
		if err := db.Exec(query).Error; err != nil {
			t.Fatal(err)
		}
	}
	type ledgerRow struct {
		Version     int64
		CompletedAt string
	}
	var first []ledgerRow
	for attempt := 0; attempt < 2; attempt++ {
		if err := runSchemaMigrations(db, schemaVersion, func(*gorm.DB) error { return errors.New("v9 must not run current baseline") }, schemaSteps...); err != nil {
			t.Fatalf("upgrade/repeat %d: %v", attempt, err)
		}
		var ledger []ledgerRow
		if err := db.Table("schema_migrations").Order("version").Find(&ledger).Error; err != nil {
			t.Fatal(err)
		}
		if len(ledger) != int(schemaVersion-8) || ledger[0].CompletedAt != "frozen-runtime-v9" {
			t.Fatalf("invalid upgraded ledger: %+v", ledger)
		}
		for index, row := range ledger {
			if row.Version != int64(index)+9 {
				t.Fatalf("noncontiguous ledger: %+v", ledger)
			}
		}
		if attempt == 0 {
			first = ledger
		} else if !reflect.DeepEqual(first, ledger) {
			t.Fatal("repeat rewrote ledger")
		}
		for _, model := range models {
			actual := reflect.New(reflect.TypeOf(model).Elem()).Interface()
			if err := db.First(actual, 1).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(model, actual) {
				t.Fatalf("historical fields changed for %T", model)
			}
			assertModelIndexesExist(t, db, model)
			duplicate := reflect.New(reflect.TypeOf(model).Elem())
			duplicate.Elem().Set(reflect.ValueOf(model).Elem())
			duplicate.Elem().FieldByName("ID").SetUint(0)
			if err := db.Create(duplicate.Interface()).Error; err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
				t.Fatalf("historical unique constraint %T: %v", model, err)
			}
		}
	}
}

// assertRuntimeV9Shape checks exact historical column names and physical index definitions.
// t receives failures; db and model describe a newly created frozen table; nothing is returned.
func assertRuntimeV9Shape(t *testing.T, db *gorm.DB, model any) {
	t.Helper()
	statement := &gorm.Statement{DB: db}
	if err := statement.Parse(model); err != nil {
		t.Fatal(err)
	}
	columns, err := db.Migrator().ColumnTypes(model)
	if err != nil {
		t.Fatal(err)
	}
	if len(columns) != len(statement.Schema.DBNames) {
		t.Fatalf("unexpected v9 columns for %T", model)
	}
	for _, column := range columns {
		if statement.Schema.FieldsByDBName[column.Name()] == nil {
			t.Fatalf("unexpected v9 column %s", column.Name())
		}
	}
	indexes, err := db.Migrator().GetIndexes(model)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range statement.Schema.ParseIndexes() {
		found := false
		for _, actual := range indexes {
			if actual.Name() != expected.Name {
				continue
			}
			found = true
			wanted := make([]string, 0, len(expected.Fields))
			for _, field := range expected.Fields {
				wanted = append(wanted, field.DBName)
			}
			unique, known := actual.Unique()
			if !known || unique != (expected.Class == "UNIQUE") || !reflect.DeepEqual(actual.Columns(), wanted) {
				t.Fatalf("wrong index %s", expected.Name)
			}
		}
		if !found {
			t.Fatalf("missing historical index %s", expected.Name)
		}
	}
}

// seedRuntimeV9Row fills every historical field with deterministic synthetic evidence.
// t reports unsupported types/errors; db persists model then reloads its canonical timestamps.
func seedRuntimeV9Row(t *testing.T, db *gorm.DB, model any) {
	t.Helper()
	value := reflect.ValueOf(model).Elem()
	now := time.Date(2026, 10, 30, 1, 2, 3, 0, time.UTC)
	for index := 0; index < value.NumField(); index++ {
		field := value.Field(index)
		switch field.Interface().(type) {
		case time.Time:
			field.Set(reflect.ValueOf(now))
		case *time.Time:
			field.Set(reflect.ValueOf(&now))
		case string:
			field.SetString("synthetic")
		case bool:
			field.SetBool(true)
		case uint, uint64:
			field.SetUint(7)
		case int, int64:
			field.SetInt(17)
		default:
			t.Fatalf("unsupported frozen field %s", value.Type().Field(index).Name)
		}
	}
	value.FieldByName("ID").SetUint(1)
	if err := db.Create(model).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(model, 1).Error; err != nil {
		t.Fatal(err)
	}
}

func setupPaymentProviderRenameTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:payment_provider_rename_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	gormdb.DB = db
	if err := db.AutoMigrate(&paymentdomain.PaymentChannel{}, &paymentdomain.Payment{}, &settingsstore.SettingRecord{}); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}
	return db
}

func TestEnsurePaymentFeePolicyMigrationClassifiesHistoryAndIsIdempotent(t *testing.T) {
	db := setupPaymentProviderRenameTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	payments := []paymentdomain.Payment{
		{
			OrderID: 1, ProviderType: constants.PaymentProviderOfficial, ChannelType: constants.PaymentChannelTypeAlipay,
			InteractionMode: constants.PaymentInteractionRedirect, Amount: money.FromDecimal(decimal.NewFromInt(103)),
			FeeAmount: money.FromDecimal(decimal.NewFromInt(3)), Currency: "CNY", Status: constants.PaymentStatusSuccess,
			CreatedAt: now, UpdatedAt: now,
		},
		{
			OrderID: 2, ProviderType: constants.PaymentProviderOfficial, ChannelType: constants.PaymentChannelTypeWechat,
			InteractionMode: constants.PaymentInteractionQR, Amount: money.FromDecimal(decimal.NewFromInt(100)),
			FeeAmount: money.FromDecimal(decimal.Zero), Currency: "CNY", Status: constants.PaymentStatusPending,
			CreatedAt: now, UpdatedAt: now,
		},
		{
			OrderID: 3, ProviderType: constants.PaymentProviderOfficial, ChannelType: constants.PaymentChannelTypeWechat,
			InteractionMode: constants.PaymentInteractionQR, Amount: money.FromDecimal(decimal.NewFromInt(100)),
			FeeAmount: money.FromDecimal(decimal.NewFromInt(3)), FeePolicy: constants.PaymentFeePolicyMerchantAbsorbed,
			Currency: "CNY", Status: constants.PaymentStatusPending, CreatedAt: now, UpdatedAt: now,
		},
	}
	if err := db.Create(&payments).Error; err != nil {
		t.Fatalf("seed historical payments failed: %v", err)
	}
	if err := ensurePaymentFeePolicyMigration(); err != nil {
		t.Fatalf("migrate payment fee policies failed: %v", err)
	}
	var migrated []paymentdomain.Payment
	if err := db.Order("id asc").Find(&migrated).Error; err != nil {
		t.Fatalf("load migrated payments failed: %v", err)
	}
	if migrated[0].FeePolicy != constants.PaymentFeePolicyLegacyCustomerSurcharge {
		t.Fatalf("fee-bearing history policy = %q", migrated[0].FeePolicy)
	}
	if migrated[1].FeePolicy != constants.PaymentFeePolicyNone {
		t.Fatalf("fee-free history policy = %q", migrated[1].FeePolicy)
	}
	if migrated[2].FeePolicy != constants.PaymentFeePolicyMerchantAbsorbed {
		t.Fatalf("explicit payment policy was overwritten = %q", migrated[2].FeePolicy)
	}

	migrated[0].FeePolicy = constants.PaymentFeePolicyMerchantAbsorbed
	if err := db.Save(&migrated[0]).Error; err != nil {
		t.Fatalf("update post-migration payment failed: %v", err)
	}
	if err := ensurePaymentFeePolicyMigration(); err != nil {
		t.Fatalf("second migration failed: %v", err)
	}
	var unchanged paymentdomain.Payment
	if err := db.First(&unchanged, migrated[0].ID).Error; err != nil {
		t.Fatalf("reload post-migration payment failed: %v", err)
	}
	if unchanged.FeePolicy != constants.PaymentFeePolicyMerchantAbsorbed {
		t.Fatalf("idempotency rewrote payment policy to %q", unchanged.FeePolicy)
	}
}

func TestEnsureOrderRefundPaymentFeeMigrationBackfillsManualRefundsOnce(t *testing.T) {
	dsn := fmt.Sprintf("file:order_refund_payment_fee_migration_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(
		&orderdomain.Order{},
		&orderdomain.OrderRefundRecord{},
		&paymentdomain.Payment{},
		&settingsstore.SettingRecord{},
	); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}
	gormdb.DB = db

	now := time.Now().UTC().Truncate(time.Second)
	order := &orderdomain.Order{
		OrderNo: "REFUND-FEE-MIGRATION-001", Status: constants.OrderStatusRefunded, Currency: "CNY",
		TotalAmount: money.FromDecimal(decimal.NewFromInt(100)), PaidAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}
	if err := db.Create(&paymentdomain.Payment{
		OrderID: order.ID, ProviderType: constants.PaymentProviderOfficial,
		ChannelType: constants.PaymentChannelTypeAlipay, InteractionMode: constants.PaymentInteractionRedirect,
		Amount: money.FromDecimal(decimal.NewFromInt(100)), FeeAmount: money.FromDecimal(decimal.RequireFromString("3.00")), FeePolicy: constants.PaymentFeePolicyMerchantAbsorbed,
		Currency: "CNY", Status: constants.PaymentStatusSuccess, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create payment failed: %v", err)
	}
	manual := &orderdomain.OrderRefundRecord{
		OrderID: order.ID, Type: constants.OrderRefundTypeManual,
		Amount: money.FromDecimal(decimal.NewFromInt(50)), Currency: "CNY", CreatedAt: now, UpdatedAt: now,
	}
	wallet := &orderdomain.OrderRefundRecord{
		OrderID: order.ID, Type: constants.OrderRefundTypeWallet,
		Amount: money.FromDecimal(decimal.NewFromInt(50)), Currency: "CNY", CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
	}
	if err := db.Create(manual).Error; err != nil {
		t.Fatalf("create manual refund failed: %v", err)
	}
	if err := db.Create(wallet).Error; err != nil {
		t.Fatalf("create wallet refund failed: %v", err)
	}

	if err := ensureOrderRefundPaymentFeeMigration(); err != nil {
		t.Fatalf("migrate refund payment fees failed: %v", err)
	}
	if err := db.First(manual, manual.ID).Error; err != nil {
		t.Fatalf("reload manual refund failed: %v", err)
	}
	if !manual.PaymentFeeRefunded || !manual.PaymentFeeRefundedAmount.Decimal.Equal(decimal.RequireFromString("1.50")) {
		t.Fatalf("manual refund was not backfilled: %+v", manual)
	}
	if err := db.First(wallet, wallet.ID).Error; err != nil {
		t.Fatalf("reload wallet refund failed: %v", err)
	}
	if wallet.PaymentFeeRefunded || !wallet.PaymentFeeRefundedAmount.Decimal.IsZero() {
		t.Fatalf("wallet refund should not be backfilled: %+v", wallet)
	}

	if err := db.Model(manual).Updates(map[string]interface{}{
		"payment_fee_refunded":        false,
		"payment_fee_refunded_amount": money.FromDecimal(decimal.Zero),
	}).Error; err != nil {
		t.Fatalf("correct migrated refund failed: %v", err)
	}
	if err := ensureOrderRefundPaymentFeeMigration(); err != nil {
		t.Fatalf("second migration failed: %v", err)
	}
	if err := db.First(manual, manual.ID).Error; err != nil {
		t.Fatalf("reload corrected refund failed: %v", err)
	}
	if manual.PaymentFeeRefunded || !manual.PaymentFeeRefundedAmount.Decimal.IsZero() {
		t.Fatalf("idempotent migration overwrote correction: %+v", manual)
	}
}

func TestEnsurePaymentProviderBepusdtRenameMigration_RenamesAndIsIdempotent(t *testing.T) {
	db := setupPaymentProviderRenameTestDB(t)

	// Seed: 两条 provider_type='epusdt'（旧 BEpusdt）+ 一条无关
	now := time.Now()
	if err := db.Create(&paymentdomain.PaymentChannel{
		Name: "old-bepusdt-1", ProviderType: "epusdt", ChannelType: "usdt-trc20",
		InteractionMode: "redirect", IsActive: true, ConfigJSON: jsonmap.JSON{},
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed channel 1 failed: %v", err)
	}
	if err := db.Create(&paymentdomain.PaymentChannel{
		Name: "old-bepusdt-2", ProviderType: "epusdt", ChannelType: "trx",
		InteractionMode: "redirect", IsActive: true, ConfigJSON: jsonmap.JSON{},
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed channel 2 failed: %v", err)
	}
	if err := db.Create(&paymentdomain.PaymentChannel{
		Name: "alipay", ProviderType: "official", ChannelType: "alipay",
		InteractionMode: "redirect", IsActive: true, ConfigJSON: jsonmap.JSON{},
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed channel 3 failed: %v", err)
	}

	// First run: should rename
	if err := ensurePaymentProviderBepusdtRenameMigration(); err != nil {
		t.Fatalf("first migration failed: %v", err)
	}

	var renamed []paymentdomain.PaymentChannel
	if err := db.Where("provider_type = ?", "bepusdt").Find(&renamed).Error; err != nil {
		t.Fatalf("query bepusdt failed: %v", err)
	}
	if len(renamed) != 2 {
		t.Fatalf("expected 2 bepusdt channels after migration, got %d", len(renamed))
	}

	var stillEpusdt int64
	if err := db.Model(&paymentdomain.PaymentChannel{}).Where("provider_type = ?", "epusdt").Count(&stillEpusdt).Error; err != nil {
		t.Fatalf("count epusdt failed: %v", err)
	}
	if stillEpusdt != 0 {
		t.Fatalf("expected 0 epusdt channels after migration, got %d", stillEpusdt)
	}

	// Marker should be written
	var marker settingsstore.SettingRecord
	if err := db.First(&marker, "key = ?", "migration/payment_provider_bepusdt_rename_v1").Error; err != nil {
		t.Fatalf("expected marker after migration: %v", err)
	}
	if !migrationDone(marker.ValueJSON) {
		t.Fatalf("marker should have done=true, got %v", marker.ValueJSON)
	}

	// Now seed a NEW real epusdt channel (post-migration scenario)
	if err := db.Create(&paymentdomain.PaymentChannel{
		Name: "real-epusdt", ProviderType: "epusdt", ChannelType: "usdt-trc20",
		InteractionMode: "redirect", IsActive: true, ConfigJSON: jsonmap.JSON{},
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed real epusdt failed: %v", err)
	}

	// Second run: marker hits, should be a no-op for the new real epusdt
	if err := ensurePaymentProviderBepusdtRenameMigration(); err != nil {
		t.Fatalf("second migration failed: %v", err)
	}

	var realEpusdtCount int64
	if err := db.Model(&paymentdomain.PaymentChannel{}).Where("name = ? AND provider_type = ?", "real-epusdt", "epusdt").Count(&realEpusdtCount).Error; err != nil {
		t.Fatalf("count real epusdt failed: %v", err)
	}
	if realEpusdtCount != 1 {
		t.Fatalf("idempotency broken: real epusdt should still be provider_type='epusdt', count=%d", realEpusdtCount)
	}

	// And bepusdt count should still be 2 (not 3, the new real epusdt didn't get migrated)
	var bepusdtCount int64
	if err := db.Model(&paymentdomain.PaymentChannel{}).Where("provider_type = ?", "bepusdt").Count(&bepusdtCount).Error; err != nil {
		t.Fatalf("count bepusdt failed: %v", err)
	}
	if bepusdtCount != 2 {
		t.Fatalf("idempotency broken: bepusdt count expected 2, got %d", bepusdtCount)
	}
}

func TestEnsurePaymentChannelBepusdtConfigMigration_NormalizesLegacyChannels(t *testing.T) {
	db := setupPaymentProviderRenameTestDB(t)
	tests := []struct {
		name        string
		channelType string
		tradeType   string
	}{
		{name: "legacy-usdt", channelType: "usdt", tradeType: "usdt.trc20"},
		{name: "legacy-usdt-trc20", channelType: "usdt-trc20", tradeType: "usdt.trc20"},
		{name: "legacy-usdc-trc20", channelType: "usdc-trc20", tradeType: "usdt.trc20"},
		{name: "legacy-trx", channelType: "trx", tradeType: "usdt.trc20"},
	}
	for _, tc := range tests {
		if err := db.Create(&paymentdomain.PaymentChannel{
			Name: tc.name, ProviderType: "bepusdt", ChannelType: tc.channelType,
			InteractionMode: "redirect", IsActive: true, ConfigJSON: jsonmap.JSON{"gateway_url": "https://bepusdt.example.com"},
		}).Error; err != nil {
			t.Fatalf("seed %s failed: %v", tc.name, err)
		}
	}
	if err := db.Create(&paymentdomain.PaymentChannel{
		Name: "explicit", ProviderType: "bepusdt", ChannelType: "usdt-trc20",
		InteractionMode: "redirect", IsActive: true, ConfigJSON: jsonmap.JSON{"trade_type": "usdt.arbitrum"},
	}).Error; err != nil {
		t.Fatalf("seed explicit failed: %v", err)
	}
	if err := db.Create(&paymentdomain.PaymentChannel{
		Name: "unknown", ProviderType: "bepusdt", ChannelType: "future-coin",
		InteractionMode: "redirect", IsActive: true, ConfigJSON: jsonmap.JSON{},
	}).Error; err != nil {
		t.Fatalf("seed unknown failed: %v", err)
	}

	if err := ensurePaymentChannelBepusdtConfigMigration(); err != nil {
		t.Fatalf("migration failed: %v", err)
	}
	for _, tc := range tests {
		var channel paymentdomain.PaymentChannel
		if err := db.First(&channel, "name = ?", tc.name).Error; err != nil {
			t.Fatalf("load %s failed: %v", tc.name, err)
		}
		if channel.ChannelType != "bepusdt" {
			t.Fatalf("%s channel_type = %q, want bepusdt", tc.name, channel.ChannelType)
		}
		if got := channel.ConfigJSON["trade_type"]; got != tc.tradeType {
			t.Fatalf("%s trade_type = %v, want %s", tc.name, got, tc.tradeType)
		}
		if got := channel.ConfigJSON["order_mode"]; got != "transaction" {
			t.Fatalf("%s order_mode = %v, want transaction", tc.name, got)
		}
	}

	var explicit paymentdomain.PaymentChannel
	if err := db.First(&explicit, "name = ?", "explicit").Error; err != nil {
		t.Fatalf("load explicit failed: %v", err)
	}
	if got := explicit.ConfigJSON["trade_type"]; got != "usdt.arbitrum" {
		t.Fatalf("explicit trade_type changed to %v", got)
	}
	if explicit.ChannelType != "bepusdt" {
		t.Fatalf("explicit channel_type = %q, want bepusdt", explicit.ChannelType)
	}
	if got := explicit.ConfigJSON["order_mode"]; got != "transaction" {
		t.Fatalf("explicit order_mode = %v, want transaction", got)
	}
	var unknown paymentdomain.PaymentChannel
	if err := db.First(&unknown, "name = ?", "unknown").Error; err != nil {
		t.Fatalf("load unknown failed: %v", err)
	}
	if unknown.ChannelType != "future-coin" || len(unknown.ConfigJSON) != 0 {
		t.Fatalf("unknown channel should stay unchanged: channel_type=%q config=%v", unknown.ChannelType, unknown.ConfigJSON)
	}

	if err := ensurePaymentChannelBepusdtConfigMigration(); err != nil {
		t.Fatalf("second migration should be idempotent: %v", err)
	}
	var marker settingsstore.SettingRecord
	if err := db.First(&marker, "key = ?", paymentChannelBepusdtConfigMigrationSettingKey).Error; err != nil {
		t.Fatalf("load marker failed: %v", err)
	}
	if !migrationDone(marker.ValueJSON) || marker.ValueJSON["migrated_count"] != float64(len(tests)+1) {
		t.Fatalf("unexpected marker: %v", marker.ValueJSON)
	}
}
