//go:build integration

package integrationtest

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/authz"
	"github.com/XManCodeing/XMall/internal/bootstrap/database/migrations"
	adminapp "github.com/XManCodeing/XMall/internal/modules/identity/admin/application"
	admindomain "github.com/XManCodeing/XMall/internal/modules/identity/admin/domain"
	adminstore "github.com/XManCodeing/XMall/internal/modules/identity/admin/infrastructure/gormstore"
	userdomain "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"
	resellerdomain "github.com/XManCodeing/XMall/internal/modules/reseller/domain"
	groupdomain "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	membershipdomain "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	membershipstore "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	recoverycontract "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	recoverydomain "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	recoverystore "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/infrastructure/gormstore"
	"github.com/XManCodeing/XMall/internal/persistence/gormutil"
	"github.com/XManCodeing/XMall/internal/platform/database/gormdb"
	"github.com/glebarez/sqlite"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// TEST_MYSQL_DSN is a server login for an isolated test instance, not a live
// application DSN. Only a newly generated database is migrated and dropped.
// No existing database named in the DSN is accessed.
func setupMySQLIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN is unset; real MySQL integration not executed")
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid test MySQL DSN")
	}
	cfg.DBName = ""
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	server, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{})
	if err != nil {
		t.Fatal("cannot connect to isolated MySQL test server")
	}
	serverSQL, err := server.DB()
	if err != nil {
		t.Fatalf("get MySQL maintenance connection: %v", err)
	}
	t.Cleanup(func() {
		if err := serverSQL.Close(); err != nil {
			t.Errorf("close MySQL maintenance connection: %v", err)
		}
	})
	var version string
	if err := server.Raw("SELECT VERSION()").Scan(&version).Error; err != nil {
		t.Fatal(err)
	}
	t.Logf("MySQL integration server: %s", version)
	if !strings.HasPrefix(version, "8.") || strings.Contains(strings.ToLower(version), "mariadb") {
		t.Fatal("this compatibility suite requires MySQL 8.x")
	}
	name := "xmall_mysql_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := server.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := server.Exec("DROP DATABASE `" + name + "`").Error; err != nil {
			t.Errorf("drop isolated database: %v", err)
		}
	})
	cfg.DBName = name
	previous := gormdb.DB
	if err := gormdb.InitDB("mysql", cfg.FormatDSN(), gormdb.DBPoolConfig{MaxOpenConns: 8, MaxIdleConns: 2}, "release"); err != nil {
		t.Fatal(err)
	}
	db := gormdb.DB
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get isolated MySQL connection: %v", err)
	}
	t.Cleanup(func() {
		gormdb.DB = previous
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close isolated MySQL connection: %v", err)
		}
	})
	return db
}

// TestMySQLProactiveEmptyScan verifies idle polling on the real changed-rows MySQL connection.
// t owns an isolated database; history cadence advances without changing the live checkpoint.
func TestMySQLProactiveEmptyScan(t *testing.T) {
	db := setupMySQLIntegrationDB(t)
	models := []any{&membershipdomain.LedgerSource{}, &membershipdomain.LedgerOutbox{}, &groupdomain.Target{}}
	if err := db.AutoMigrate(append(models, membershipstore.ProactiveModels()...)...); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	now := time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)
	checkpoint := membershipdomain.ProactiveCursor{BotID: 9, FastAfter: 99, HistoryAfter: 12, HistoryAt: now.Add(-time.Minute)}
	if err := db.Create(&checkpoint).Error; err != nil {
		t.Fatal(err)
	}
	// Explicitly reproduce the driver behavior that made the old empty scan fail.
	unchanged := db.Model(&membershipdomain.ProactiveCursor{}).Where("bot_id = ?", 9).Update("fast_after", 99)
	if unchanged.Error != nil || unchanged.RowsAffected != 0 {
		t.Fatalf("requires changed-row semantics: affected=%d err=%v", unchanged.RowsAffected, unchanged.Error)
	}
	store := membershipstore.New(db)
	for _, elapsed := range []time.Duration{0, 0, time.Second, time.Minute, time.Minute + time.Second} {
		if count, err := store.ScanProactive(ctx, 9, 10, now.Add(elapsed)); count != 0 || err != nil {
			t.Fatalf("empty live scan: count=%d err=%v", count, err)
		}
	}
	var actual membershipdomain.ProactiveCursor
	if err := db.First(&actual, "bot_id = ?", 9).Error; err != nil {
		t.Fatal(err)
	}
	if actual.FastAfter != 99 || actual.HistoryAfter != 0 || !actual.HistoryAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("history cadence/wrap or live cursor changed: %+v", actual)
	}
	// A new bot exercises zero-time checkpoint initialization under strict mode too.
	for range 2 {
		if count, err := store.ScanProactive(ctx, 10, 10, now); count != 0 || err != nil {
			t.Fatalf("fresh bot idle scan: count=%d err=%v", count, err)
		}
	}
}

// TestServerMigrationSessionDisconnectRecovery closes the physical lock-owning session.
// t uses only newly created databases; this covers session loss before baseline, not a
// network partition or interruption midway through historical DDL/data migration.
func TestServerMigrationSessionDisconnectRecovery(t *testing.T) {
	for _, dialect := range []string{"mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var db *gorm.DB
			if dialect == "mysql" {
				db = setupMySQLIntegrationDB(t)
			} else {
				db = openIsolatedPostgresDB(t)
			}
			previous := gormdb.DB
			t.Cleanup(func() { gormdb.DB = previous })
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			gormdb.DB = db.WithContext(ctx)
			injected := errors.New("test disconnected migration lock owner")
			triggered := false
			const callback = "integration:disconnect_migration_owner"
			// Ledger creation is the first GORM Exec after the production runner
			// successfully acquires its session lock on a pinned *sql.Conn.
			if err := db.Callback().Raw().After("gorm:raw").Register(callback, func(tx *gorm.DB) {
				if triggered || tx.Error != nil || !strings.Contains(tx.Statement.SQL.String(), "CREATE TABLE IF NOT EXISTS schema_migrations") {
					return
				}
				triggered = true
				conn, ok := tx.Statement.ConnPool.(*sql.Conn)
				if !ok {
					tx.AddError(errors.New("migration did not pin a physical SQL connection"))
					return
				}
				// ErrBadConn discards/closes this physical session, unlike Conn.Close
				// which only returns it to the pool and can retain a session lock.
				if err := conn.Raw(func(any) error { return driver.ErrBadConn }); !errors.Is(err, driver.ErrBadConn) {
					tx.AddError(fmt.Errorf("discard migration connection: %w", err))
					return
				}
				tx.AddError(injected)
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := db.Callback().Raw().Remove(callback); err != nil {
					t.Errorf("remove disconnect callback: %v", err)
				}
			})
			if err := migrations.AutoMigrate(); !triggered || !errors.Is(err, injected) {
				t.Fatalf("migration did not fail on physical disconnect: triggered=%t err=%v", triggered, err)
			}
			var count int64
			if err := db.WithContext(ctx).Table("schema_migrations").Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("disconnected baseline recorded completion: count=%d err=%v", count, err)
			}
			// The one-shot callback is inert now. Success requires a new physical
			// session to reacquire the production lock released by disconnection.
			for range 2 {
				if err := migrations.AutoMigrate(); err != nil {
					t.Fatalf("reacquire migration lock after disconnect: %v", err)
				}
			}
			if err := db.WithContext(ctx).Table("schema_migrations").Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("recovered baseline ledger: count=%d err=%v", count, err)
			}
		})
	}
}

// TestTelegramRecoveryThreeDialects runs identical real-store acceptance on each dialect.
// t owns every database; absent explicit server DSNs skip only their own subtests.
func TestTelegramRecoveryThreeDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := setupRecoveryAcceptanceDB(t, dialect)
			// Exercise the physical CHECK on real servers, not just GORM SQL generation.
			if err := db.Create(&recoverydomain.ScopeSlot{Key: "", TaskID: 999}).Error; err == nil {
				t.Fatal("database accepted empty recovery scope")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			store := recoverystore.New(db)
			owner := createRecoveryPreflight(t, ctx, store, "owner", 1, 2)
			owner, err := store.Confirm(ctx, recoverycontract.Confirmation{PreflightRef: owner.PreflightRef, ExpectedVersion: owner.Version, ActorID: 7})
			if err != nil {
				t.Fatalf("confirm scope owner: %v", err)
			}
			assertRecoveryScopeRollback(t, ctx, db, store, owner)
			assertRecoveryTaskCAS(t, ctx, store, owner)
			assertRecoveryStepCAS(t, ctx, store, owner)
		})
	}
}

// setupRecoveryAcceptanceDB returns a freshly migrated disposable database for dialect.
// t closes local SQLite and restores the global migration handle; server setup owns cleanup.
func setupRecoveryAcceptanceDB(t *testing.T, dialect string) *gorm.DB {
	t.Helper()
	var db *gorm.DB
	switch dialect {
	case "mysql":
		db = setupMySQLIntegrationDB(t)
	case "postgres":
		db = openIsolatedPostgresDB(t)
	case "sqlite":
		var err error
		db, err = gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "recovery.sqlite")+"?_pragma=foreign_keys(1)"), &gorm.Config{})
		if err != nil {
			t.Fatalf("open disposable SQLite: %v", err)
		}
		pool, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		pool.SetMaxOpenConns(1) // Exercise the production single-connection transaction invariant.
		t.Cleanup(func() {
			if err := pool.Close(); err != nil {
				t.Errorf("close recovery SQLite: %v", err)
			}
		})
	default:
		t.Fatalf("unsupported acceptance dialect %q", dialect)
	}
	previous := gormdb.DB
	gormdb.DB = db
	t.Cleanup(func() { gormdb.DB = previous })
	// Use the production schema, including its physical constraints, not a custom probe table.
	if err := migrations.AutoMigrate(); err != nil {
		t.Fatalf("migrate recovery acceptance database: %v", err)
	}
	return db
}

// createRecoveryPreflight creates a group-replacement task for key/target/bot and optional accounts.
// ctx bounds database operations; t reports errors and the returned task is unconfirmed.
func createRecoveryPreflight(t *testing.T, ctx context.Context, store *recoverystore.Store, key string, target, bot uint, accounts ...string) *recoverydomain.Task {
	t.Helper()
	oldChat := -int64(1000 + target*2)
	newChat := oldChat - 1
	input := recoverycontract.PreflightInput{
		Kind: recoverydomain.GroupReplace, RequestKey: key, ActorID: 7, Reason: "isolated recovery acceptance", TTL: time.Minute,
		Snapshot: recoverydomain.Snapshot{
			Scope: recoverydomain.Scope{AccountIDs: accounts, TargetIDs: []string{fmt.Sprint(target)}, BotIDs: []string{fmt.Sprint(bot)}, ChatIDs: []string{fmt.Sprint(oldChat), fmt.Sprint(newChat)}},
			Old:   recoverydomain.Endpoint{TargetID: target, BotID: bot, ChatID: oldChat},
			New:   recoverydomain.Endpoint{TargetID: target, BotID: bot, ChatID: newChat},
		},
	}
	task, err := store.CreatePreflight(ctx, input)
	if err != nil {
		t.Fatalf("create recovery preflight %s: %v", key, err)
	}
	return task
}

// assertRecoveryScopeRollback verifies shared-bot cross-target exclusion and atomic rollback.
// db is read only here; every mutation uses store. owner must already hold its scope.
func assertRecoveryScopeRollback(t *testing.T, ctx context.Context, db *gorm.DB, store *recoverystore.Store, owner *recoverydomain.Task) {
	t.Helper()
	// account:99 sorts before bot:2, ensuring the losing transaction writes a
	// previously free slot before hitting the conflicting bot and rolling back.
	competitor := createRecoveryPreflight(t, ctx, store, "cross-target", 9, 2, "99")
	_, err := store.Confirm(ctx, recoverycontract.Confirmation{PreflightRef: competitor.PreflightRef, ExpectedVersion: competitor.Version, ActorID: 7})
	if !errors.Is(err, recoverycontract.ErrScopeBusy) {
		t.Fatalf("cross-target shared bot accepted: %v", err)
	}
	var slots []recoverydomain.ScopeSlot
	if err := db.WithContext(ctx).Find(&slots).Error; err != nil {
		t.Fatalf("read scope ownership: %v", err)
	}
	if len(slots) != 4 {
		t.Fatalf("scope confirmation leaked partial slots: %+v", slots)
	}
	for _, slot := range slots {
		if slot.TaskID != owner.ID {
			t.Fatalf("scope belongs to losing task: %+v", slot)
		}
	}
	detail, err := store.Get(ctx, competitor.ID)
	if err != nil || detail.Task.Version != competitor.Version || detail.Task.State != recoverydomain.AwaitingConfirmation {
		t.Fatalf("scope conflict changed losing task: %+v err=%v", detail, err)
	}
	// Rejection must not strand account 99, target 9 or their chat resources.
	independent := createRecoveryPreflight(t, ctx, store, "independent", 9, 10, "99")
	if _, err := store.Confirm(ctx, recoverycontract.Confirmation{PreflightRef: independent.PreflightRef, ExpectedVersion: independent.Version, ActorID: 7}); err != nil {
		t.Fatalf("scope rollback stranded competitor resources: %v", err)
	}
}

// assertRecoveryTaskCAS submits two identical versioned transitions for owner.
// Exactly one may advance; t checks the stored version and rejects the stale replay.
func assertRecoveryTaskCAS(t *testing.T, ctx context.Context, store *recoverystore.Store, owner *recoverydomain.Task) {
	t.Helper()
	assertSingleRecoveryWinner(t, func() error {
		_, err := store.Transition(ctx, recoverycontract.Transition{TaskID: owner.ID, ExpectedVersion: owner.Version, State: recoverydomain.Switching})
		return err
	})
	detail, err := store.Get(ctx, owner.ID)
	if err != nil || detail.Task.State != recoverydomain.Switching || detail.Task.Version != owner.Version+1 {
		t.Fatalf("task CAS advanced incorrectly: %+v err=%v", detail, err)
	}
}

// assertSingleRecoveryWinner races two calls to operation and checks the business error.
// t fails on database/locking errors rather than treating them as valid CAS rejection.
func assertSingleRecoveryWinner(t *testing.T, operation func() error) {
	t.Helper()
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			results <- operation()
		}()
	}
	close(start)
	winners, stale := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			winners++
		} else if errors.Is(err, recoverycontract.ErrStale) {
			stale++
		} else {
			t.Errorf("unexpected business CAS error: %v", err)
		}
	}
	if winners != 1 || stale != 1 {
		t.Fatalf("business CAS winners=%d stale=%d, want 1/1", winners, stale)
	}
}

// assertRecoveryStepCAS checks immutable intent, single claim, lease fencing and completion.
// t reports failures; owner has an active task and ctx bounds concurrent store operations.
func assertRecoveryStepCAS(t *testing.T, ctx context.Context, store *recoverystore.Store, owner *recoverydomain.Task) {
	t.Helper()
	input := recoverycontract.StepInput{TaskID: owner.ID, BusinessKey: "acceptance.route", Operation: "replace_route"}
	step, err := store.EnsureStep(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.EnsureStep(ctx, input)
	if err != nil || replay.ID != step.ID {
		t.Fatalf("step replay duplicated intent: %+v err=%v", replay, err)
	}
	input.Operation = "different_operation"
	if _, err := store.EnsureStep(ctx, input); !errors.Is(err, recoverycontract.ErrConflict) {
		t.Fatalf("changed step intent accepted: %v", err)
	}
	assertSingleRecoveryWinner(t, func() error {
		_, err := store.ClaimStep(ctx, recoverycontract.StepClaim{StepID: step.ID, ExpectedVersion: step.Version, Lease: time.Minute})
		return err
	})
	page, err := store.ListSteps(ctx, owner.ID, 0, 10)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("step uniqueness lost: count=%d err=%v", len(page.Items), err)
	}
	claimed := page.Items[0]
	if claimed.State != recoverydomain.StepRunning || claimed.Version != step.Version+1 {
		t.Fatalf("invalid claim state/version: state=%s version=%d", claimed.State, claimed.Version)
	}
	assertForeignRecoveryLeaseRejected(t, ctx, store, &claimed)
	completion := recoverycontract.StepCompletion{StepID: claimed.ID, ExpectedVersion: claimed.Version, LeaseToken: claimed.LeaseToken, Outcome: recoverydomain.StepSucceeded, ResultCode: "verified"}
	stale := completion
	stale.ExpectedVersion--
	if _, err := store.CompleteStep(ctx, stale); !errors.Is(err, recoverycontract.ErrStale) {
		t.Fatalf("stale step completion accepted: %v", err)
	}
	assertSingleRecoveryWinner(t, func() error {
		_, err := store.CompleteStep(ctx, completion)
		return err
	})
	page, err = store.ListSteps(ctx, owner.ID, 0, 10)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("read completed step: count=%d err=%v", len(page.Items), err)
	}
	completed := page.Items[0]
	if completed.State != recoverydomain.StepSucceeded || completed.Version != claimed.Version+1 || completed.LeaseToken != "" || completed.LeaseUntil != nil {
		t.Fatalf("completion lost CAS/lease cleanup: state=%s version=%d", completed.State, completed.Version)
	}
}

// assertForeignRecoveryLeaseRejected tries a valid lease from another target's task.
// t verifies rejection leaves both the recipient and the lease owner's step unchanged.
func assertForeignRecoveryLeaseRejected(t *testing.T, ctx context.Context, store *recoverystore.Store, recipient *recoverydomain.Step) {
	t.Helper()
	other := createRecoveryPreflight(t, ctx, store, "foreign-lease", 20, 21)
	other, err := store.Confirm(ctx, recoverycontract.Confirmation{PreflightRef: other.PreflightRef, ExpectedVersion: other.Version, ActorID: 7})
	if err != nil {
		t.Fatal(err)
	}
	step, err := store.EnsureStep(ctx, recoverycontract.StepInput{TaskID: other.ID, BusinessKey: "acceptance.route", Operation: "replace_route"})
	if err != nil {
		t.Fatal(err)
	}
	step, err = store.ClaimStep(ctx, recoverycontract.StepClaim{StepID: step.ID, ExpectedVersion: step.Version, Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CompleteStep(ctx, recoverycontract.StepCompletion{StepID: recipient.ID, ExpectedVersion: recipient.Version, LeaseToken: step.LeaseToken, Outcome: recoverydomain.StepSucceeded, ResultCode: "verified"})
	if !errors.Is(err, recoverycontract.ErrStale) {
		t.Fatalf("another target's valid lease accepted: %v", err)
	}
	for _, expected := range []*recoverydomain.Step{recipient, step} {
		page, err := store.ListSteps(ctx, expected.TaskID, 0, 10)
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("read fenced step: count=%d err=%v", len(page.Items), err)
		}
		actual := page.Items[0]
		if actual.ID != expected.ID || actual.State != expected.State || actual.Version != expected.Version || actual.LeaseToken != expected.LeaseToken {
			t.Fatalf("foreign completion mutated step %d", expected.ID)
		}
	}
}

func TestMySQLFullInitializationAndRestart(t *testing.T) {
	db := setupMySQLIntegrationDB(t)
	for attempt := 0; attempt < 2; attempt++ {
		if err := migrations.AutoMigrate(); err != nil {
			t.Fatalf("full migration run %d: %v", attempt+1, err)
		}
	}
	var jsonText string
	if err := db.Raw("SELECT "+gormutil.JSONTextExprByDialect("mysql", "?", "en-US"), `{"en-US":"Mixed Case title"}`, `{"en-US":"Mixed Case title"}`).Scan(&jsonText).Error; err != nil {
		t.Fatal(err)
	}
	if jsonText != "Mixed Case title" {
		t.Fatalf("JSON text is quoted or corrupted: %q", jsonText)
	}
	var arrayLength int
	if err := db.Raw("SELECT "+gormutil.JSONArrayLengthExprByDialect("mysql", "?"), `[1,2]`).Scan(&arrayLength).Error; err != nil {
		t.Fatal(err)
	}
	if arrayLength != 2 {
		t.Fatalf("JSON length %d", arrayLength)
	}

	store := adminstore.New(db)
	if err := adminapp.InitDefaultAdmin(store, "mysql-admin", ""); err != nil {
		t.Fatal(err)
	}
	var adminCount int64
	if err := db.Model(&admindomain.Admin{}).Count(&adminCount).Error; err != nil {
		t.Fatal(err)
	}
	if adminCount != 0 {
		t.Fatal("administrator created without an explicit bootstrap password")
	}
	if err := adminapp.InitDefaultAdmin(store, "mysql-admin", "Initial-Test-Password-123!"); err != nil {
		t.Fatal(err)
	}
	var first admindomain.Admin
	if err := db.Where("username = ?", "mysql-admin").First(&first).Error; err != nil {
		t.Fatal(err)
	}
	if !first.IsSuper || bcrypt.CompareHashAndPassword([]byte(first.PasswordHash), []byte("Initial-Test-Password-123!")) != nil {
		t.Fatal("first administrator was not initialized correctly")
	}
	if err := db.Model(&first).Update("is_super", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := adminapp.InitDefaultAdmin(store, "mysql-admin", "Must-Not-Replace-Password-456!"); err != nil {
		t.Fatal(err)
	}
	var current admindomain.Admin
	if err := db.First(&current, first.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.IsSuper || current.PasswordHash != first.PasswordHash {
		t.Fatal("restart changed existing administrator")
	}
	service, err := authz.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := service.BootstrapBuiltinRoles(); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := service.Enforcer().AddPolicy("mysql-test-reader", "/admin/mysql-test", "GET"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := authz.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := reloaded.Enforce("mysql-test-reader", "/admin/mysql-test", "GET")
	if err != nil || !allowed {
		t.Fatalf("persisted permission not restored: allowed=%v err=%v", allowed, err)
	}
	allowed, err = reloaded.Enforce("mysql-test-reader", "/admin/mysql-test", "DELETE")
	if err != nil || allowed {
		t.Fatalf("unexpected write permission: allowed=%v err=%v", allowed, err)
	}

	// Active-only uniqueness must reject duplicates but allow recreation after deletion.
	user := userdomain.User{Email: "mysql-reseller@example.invalid", PasswordHash: "test-hash"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	profile := resellerdomain.Profile{UserID: user.ID}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatal(err)
	}
	domain := resellerdomain.Domain{ResellerID: profile.ID, Domain: "mysql-test.example.invalid", Type: "custom"}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	duplicate := resellerdomain.Domain{ResellerID: profile.ID, Domain: domain.Domain, Type: "custom"}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("active domain uniqueness was lost")
	}
	if err := db.Model(&domain).Update("deleted_at", time.Now().UTC()).Error; err != nil {
		t.Fatal(err)
	}
	duplicate.ID = 0
	if err := db.Create(&duplicate).Error; err != nil {
		t.Fatalf("cannot reuse soft-deleted domain: %v", err)
	}
	if err := db.Model(&duplicate).Update("deleted_at", time.Now().UTC()).Error; err != nil {
		t.Fatal(err)
	}
	third := resellerdomain.Domain{ResellerID: profile.ID, Domain: domain.Domain, Type: "custom"}
	if err := db.Create(&third).Error; err != nil {
		t.Fatal(err)
	}
	if err := migrations.AutoMigrate(); err != nil {
		t.Fatalf("restart with deleted history: %v", err)
	}

	// Validate the dialect's guarded update semantics under real contention.
	// Repository-specific payment/fulfillment tests are separate from this probe.
	if err := db.Exec("CREATE TABLE mysql_transaction_probe (id BIGINT PRIMARY KEY, stock INT NOT NULL, balance DECIMAL(20,2) NOT NULL, event_key VARCHAR(64) UNIQUE) ENGINE=InnoDB").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO mysql_transaction_probe (id, stock, balance, event_key) VALUES (1, 1, 10.00, 'PayCaseA')").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO mysql_transaction_probe (id, stock, balance, event_key) VALUES (2, 0, 0, 'paycasea')").Error; err != nil {
		t.Fatalf("opaque keys lost case sensitivity: %v", err)
	}
	if err := db.Exec("INSERT INTO mysql_transaction_probe (id, stock, balance, event_key) VALUES (3, 0, 0, 'PayCaseA')").Error; err == nil {
		t.Fatal("duplicate event key was accepted")
	}
	var wg sync.WaitGroup
	results := make(chan int64, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := db.Exec("UPDATE mysql_transaction_probe SET stock = stock - 1, balance = balance - 2.50 WHERE id = 1 AND stock > 0 AND balance >= 2.50")
			if r.Error != nil {
				t.Errorf("guarded update: %v", r.Error)
			}
			results <- r.RowsAffected
		}()
	}
	wg.Wait()
	close(results)
	var winners int64
	for n := range results {
		winners += n
	}
	if winners != 1 {
		t.Fatalf("expected one stock claim, got %d", winners)
	}
	rollback := errors.New("intentional rollback")
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE mysql_transaction_probe SET balance = 0 WHERE id = 1").Error; err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("rollback returned %v", err)
	}
	var balance string
	if err := db.Raw("SELECT balance FROM mysql_transaction_probe WHERE id = 1").Scan(&balance).Error; err != nil {
		t.Fatal(err)
	}
	if balance != "7.50" {
		t.Fatalf("unexpected decimal balance %s", balance)
	}
}
