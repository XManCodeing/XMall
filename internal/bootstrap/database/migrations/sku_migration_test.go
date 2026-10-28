package migrations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
	"github.com/XManCodeing/XMall/internal/crypto"
	cardsecretdomain "github.com/XManCodeing/XMall/internal/modules/cardsecret/domain"
	cartdomain "github.com/XManCodeing/XMall/internal/modules/cart/domain"
	channel "github.com/XManCodeing/XMall/internal/modules/channelclient/domain"
	externalidentitydomain "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/domain"
	userauthdomain "github.com/XManCodeing/XMall/internal/modules/identity/userauth/domain"
	paymentdomain "github.com/XManCodeing/XMall/internal/modules/payment/domain"
	broadcastdomain "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/domain"
	deliverycontract "github.com/XManCodeing/XMall/internal/modules/telegram/delivery/contract"
	runtimedomain "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	runtimestore "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"

	productdomain "github.com/XManCodeing/XMall/internal/modules/catalog/product/domain"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	procurementdomain "github.com/XManCodeing/XMall/internal/modules/procurement/domain"
	resellerdomain "github.com/XManCodeing/XMall/internal/modules/reseller/domain"
	settingsstore "github.com/XManCodeing/XMall/internal/modules/settings/infrastructure/gormstore"
	"github.com/XManCodeing/XMall/internal/platform/database/gormdb"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"github.com/XManCodeing/XMall/internal/shared/money"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Fingerprint only schema inputs, not locking, ledger/runner mechanics or progress
// logging. The old whole-package v1 fixture is retained for audit; the scoped v1
// fixture was captured before the progress-only baseline call substitution below.
// Future model/baseline/data changes require a new target AND its incremental step.
const legacyWholePackageV1Fingerprint = "14a135bc9b1cc99bc28c03a1f4a5a8f27e928ce371bdd75e9c11df8e24ab578a"

// XMall module rename changes the source fingerprint (including imports), not
// database schema. Preserve the pre-rename v1 fingerprint for audit; no migration
// step or schemaVersion change is required for this package-path-only change.
const preXMallScopedV1Fingerprint = "85555e2b375b75da42e9e25d8a68dcc8cae4f41e34217ea211472b3cac20d899"

// The GitHub owner rename also changes imports only. Retain the previous XMall
// baseline for audit; database schema and schemaVersion remain unchanged.
const preOwnerRenameScopedV1Fingerprint = "f5c4c4d544768db1ce8eb2768d082671c6476aca3ddf8635830ae2f751d3b281"

var schemaFingerprints = map[int64]string{
	1: "6aa3cd137e927bbeee42774a013fec690c0930f262434d2c686dc6464f7a6c26",
	2: "3b601bf8773d9f82915578189f724ded072ff20a4803aaae6f7898af12f11600",
	// Final unreleased v3 includes broadcast retry and procurement recipient store logic.
	// Both are covered by the existing v3 model migration; no deployed version is rewritten.
	3: "da6480dffd796c57a13bc40d6b0f3f743b21a9a3d99216e0b37978ae4e42a5db",
	4: "29e6bbdf957d8c52063fa619d7aff50ee3eaf6ac9b492632ac818dad16f5ad10",
	5: "63934f8dc6a6b0ed97b9ca46269fcd4a08ea4eeaf8a13a2226c5bd73163a509f",
}

func TestV4OwnershipUpgradePreservesUnknownAndBackfillsSnapshot(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	pool.SetMaxOpenConns(1)
	defer pool.Close()
	key := crypto.DeriveKey("synthetic-migration-key")
	cipher, _ := crypto.Encrypt(key, "123:old")
	if err := runSchemaMigrations(db, 4, func(tx *gorm.DB) error {
		if err := tx.AutoMigrate(&channel.Client{}); err != nil {
			return err
		}
		if err := migrateTelegramRuntime(tx); err != nil {
			return err
		}
		for index, token := range []string{cipher, "", "invalid-cipher"} {
			if err := tx.Create(&channel.Client{Name: "upgrade", ChannelType: "telegram_bot", ChannelKey: fmt.Sprint(index), BotToken: token, ChannelSecret: "synthetic"}).Error; err != nil {
				return err
			}
		}
		return nil
	}, schemaSteps[:3]...); err != nil {
		t.Fatal(err)
	}
	if err := runSchemaMigrations(db, schemaVersion, func(*gorm.DB) error { return errors.New("unexpected baseline") }, schemaSteps...); err != nil {
		t.Fatal(err)
	}
	// A concurrent/post-upgrade credential change cannot erase the snapshot.
	if err := db.Table("channel_clients").Where("id = 1").Update("bot_token", "").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := runtimestore.OwnershipLock(tx); err != nil {
			return err
		}
		return runtimestore.OwnershipBackfill(tx, func(s string) (string, error) { return crypto.Decrypt(key, s) })
	}); err != nil {
		t.Fatal(err)
	}
	var owned int64
	db.Model(&runtimedomain.BotOwnership{}).Where("channel_id = 1 AND telegram_id = 123").Count(&owned)
	if owned != 1 {
		t.Fatal("lost upgrade identity")
	}
	var unknown int64
	db.Model(&runtimedomain.BotOwnership{}).Where("state = ?", "unresolved").Count(&unknown)
	if unknown != 2 {
		t.Fatal("unknown historical ownership silently resolved")
	}
	if err := migrateTelegramOwnership(db); err != nil {
		t.Fatal(err)
	}
	db.Model(&runtimedomain.BotOwnership{}).Where("state = ?", "unresolved").Count(&unknown)
	if unknown != 2 {
		t.Fatal("migration retry rewrote history")
	}
}

func TestV3UpgradeRequiresExplicitCutoverProof(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	pool.SetMaxOpenConns(1)
	defer pool.Close()
	if err := runSchemaMigrations(db, 3, func(tx *gorm.DB) error {
		if err := migrateTelegramRuntime(tx); err != nil {
			return err
		}
		if err := tx.Create(&runtimedomain.Instance{TelegramID: 123, TokenCipher: "synthetic", Mode: "internal", Enabled: true}).Error; err != nil {
			return err
		}
		return tx.Migrator().DropColumn(&runtimedomain.Instance{}, "cutover_completed")
	}, schemaSteps[:2]...); err != nil {
		t.Fatal(err)
	}
	if err := runSchemaMigrations(db, schemaVersion, func(*gorm.DB) error { t.Fatal("unexpected baseline"); return nil }, schemaSteps...); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasColumn(&runtimedomain.Instance{}, "cutover_completed") {
		t.Fatal("missing ownership proof column")
	}
	var row runtimedomain.Instance
	if err := db.First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.CutoverCompleted || row.TelegramID != 123 {
		t.Fatal("upgrade fabricated cutover proof")
	}
}

func TestTelegramUpgradeFromV1AndV2(t *testing.T) {
	for _, scenario := range []struct {
		version int64
		retry   bool
	}{{1, false}, {1, true}, {2, false}, {2, true}} {
		t.Run(fmt.Sprintf("from_v%d_retry_%t", scenario.version, scenario.retry), func(t *testing.T) {
			db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "telegram.sqlite"))
			if err := runSchemaMigrations(db, scenario.version, func(tx *gorm.DB) error {
				if err := tx.AutoMigrate(&paymentdomain.Payment{}, &broadcastdomain.Broadcast{}); err != nil {
					return err
				}
				for _, payment := range []paymentdomain.Payment{
					{OrderID: 42, ChannelID: 1, ProviderType: "epay", ChannelType: "alipay", InteractionMode: "redirect", Amount: money.FromDecimal(decimal.NewFromInt(12)), Currency: "CNY", Status: "pending", PayURL: "https://example.test/pay"},
					{OrderID: 42, ChannelID: 1, ProviderType: "epay", ChannelType: "alipay", InteractionMode: "redirect", Amount: money.FromDecimal(decimal.NewFromInt(12)), Currency: "CNY", Status: "failed"},
				} {
					if err := tx.Create(&payment).Error; err != nil {
						return err
					}
				}
				if err := tx.Create(&broadcastdomain.Broadcast{Title: "legacy", RecipientType: "all", MessageHTML: "legacy message", Status: "completed", SuccessCount: 2}).Error; err != nil {
					return err
				}
				// Remove every new payment/broadcast field, not just source IDs.
				if err := tx.Migrator().DropIndex(&paymentdomain.Payment{}, "idx_payment_order_operation"); err != nil {
					return err
				}
				for _, column := range []string{"operation_key", "operation_hash", "creation_state", "source_bot_id", "source_telegram_id", "source_private_chat_id"} {
					if err := tx.Migrator().DropColumn(&paymentdomain.Payment{}, column); err != nil {
						return err
					}
				}
				for _, column := range []string{"bot_id", "delivery_version", "payload_cipher", "unknown_count"} {
					if err := tx.Migrator().DropColumn(&broadcastdomain.Broadcast{}, column); err != nil {
						return err
					}
				}
				if scenario.version == 2 {
					if err := migrateTelegramRuntime(tx); err != nil {
						return err
					}
					for _, column := range []string{"telegram_user_id", "user_id", "broadcast_id"} {
						if err := tx.Migrator().DropColumn(&runtimedomain.Delivery{}, column); err != nil {
							return err
						}
					}
				}
				return tx.Exec("CREATE TABLE retained_legacy (id INTEGER PRIMARY KEY, value TEXT)").Error
			}, schemaSteps[:scenario.version-1]...); err != nil {
				t.Fatal(err)
			}
			if err := db.Exec("INSERT INTO retained_legacy (id,value) VALUES (1,'preserved')").Error; err != nil {
				t.Fatal(err)
			}
			baseline := func(*gorm.DB) error { t.Fatal("versioned database ran baseline"); return nil }
			if scenario.retry {
				steps := append([]migrationStep(nil), schemaSteps...)
				injected := errors.New("injected v3 failure after DDL")
				steps[1].run = func(tx *gorm.DB) error {
					if err := migrateTelegramBusinessDelivery(tx); err != nil {
						return err
					}
					return injected
				}
				if err := runSchemaMigrations(db, schemaVersion, baseline, steps...); !errors.Is(err, injected) {
					t.Fatalf("failure = %v", err)
				}
				var completed int64
				if err := db.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&completed).Error; err != nil {
					t.Fatal(err)
				}
				if completed != scenario.version {
					t.Fatalf("failed SQLite attempt committed version %d", completed)
				}
			}
			if err := runSchemaMigrations(db, schemaVersion, baseline, schemaSteps...); err != nil {
				t.Fatal(err)
			}
			for _, model := range []interface{}{
				&runtimedomain.Instance{}, &runtimedomain.Contact{}, &runtimedomain.Inbox{}, &runtimedomain.Session{}, &runtimedomain.Delivery{},
				&userauthdomain.TelegramHistoryGrant{}, &deliverycontract.Event{}, &deliverycontract.OrderSource{}, &deliverycontract.OrderOperation{},
				&paymentdomain.Payment{}, &broadcastdomain.Broadcast{},
			} {
				stmt := &gorm.Statement{DB: db}
				if err := stmt.Parse(model); err != nil {
					t.Fatal(err)
				}
				for _, field := range stmt.Schema.Fields {
					if field.DBName != "" && !field.IgnoreMigration && !db.Migrator().HasColumn(model, field.DBName) {
						t.Fatalf("missing %T column %s", model, field.DBName)
					}
				}
				for _, index := range stmt.Schema.ParseIndexes() {
					if !db.Migrator().HasIndex(model, index.Name) {
						t.Fatalf("missing %T index %s", model, index.Name)
					}
				}
				if !db.Migrator().HasTable(model) {
					t.Fatalf("missing Telegram table %T", model)
				}
			}
			for _, column := range []string{"source_bot_id", "source_telegram_id", "source_private_chat_id"} {
				if !db.Migrator().HasColumn(&paymentdomain.Payment{}, column) {
					t.Fatalf("missing payment column %s", column)
				}
			}
			if !db.Migrator().HasColumn(&broadcastdomain.Broadcast{}, "bot_id") {
				t.Fatal("missing broadcast bot_id")
			}
			var payments []paymentdomain.Payment
			if err := db.Order("id").Find(&payments).Error; err != nil {
				t.Fatal(err)
			}
			if len(payments) != 2 || payments[0].PayURL != "https://example.test/pay" || payments[0].OperationKey != nil || payments[1].OperationKey != nil || !payments[0].Amount.Decimal.Equal(decimal.NewFromInt(12)) {
				t.Fatalf("legacy payments changed: %+v", payments)
			}
			var broadcast broadcastdomain.Broadcast
			if err := db.First(&broadcast).Error; err != nil {
				t.Fatal(err)
			}
			if broadcast.MessageHTML != "legacy message" || broadcast.SuccessCount != 2 || broadcast.DeliveryVersion != 0 || broadcast.UnknownCount != 0 || broadcast.PayloadCipher != "" {
				t.Fatalf("legacy broadcast changed: %+v", broadcast)
			}
			// The nullable key must preserve legacy duplicate order IDs while enforcing
			// uniqueness for newly keyed payment operations after an upgrade.
			key := "migration-test"
			if err := db.Model(&payments[0]).Update("operation_key", key).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&payments[1]).Update("operation_key", key).Error; err == nil {
				t.Fatal("payment operation unique index is ineffective")
			}
			var value string
			if err := db.Raw("SELECT value FROM retained_legacy WHERE id = 1").Scan(&value).Error; err != nil || value != "preserved" {
				t.Fatalf("legacy row changed: %q %v", value, err)
			}
			var versions []int64
			if err := db.Raw("SELECT version FROM schema_migrations ORDER BY version").Scan(&versions).Error; err != nil {
				t.Fatal(err)
			}
			if len(versions) != int(schemaVersion-scenario.version+1) || versions[0] != scenario.version || versions[len(versions)-1] != schemaVersion {
				t.Fatal(versions)
			}
			if err := runSchemaMigrations(db, schemaVersion, baseline, schemaSteps...); err != nil {
				t.Fatal(err)
			}
			// Replay v3 directly as well: MySQL may retain DDL after a failed step.
			if err := migrateTelegramBusinessDelivery(db); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSchemaVersionCoversModelsAndMigrations(t *testing.T) {
	root := filepath.Clean("../../../..")
	hash := sha256.New()
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		include := strings.HasPrefix(name, "internal/bootstrap/database/migrations/") ||
			strings.Contains(name, "/domain/") || strings.Contains(name, "/infrastructure/gormstore/") ||
			strings.HasPrefix(name, "internal/shared/money/") || strings.HasPrefix(name, "internal/shared/jsonmap/")
		if !include {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		// Exclude only the explicit version constant to avoid circular hashes.
		for _, decl := range file.Decls {
			if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.CONST {
				for _, spec := range gen.Specs {
					value := spec.(*ast.ValueSpec)
					if len(value.Names) == 1 && value.Names[0].Name == "schemaVersion" {
						value.Values = []ast.Expr{&ast.BasicLit{Kind: token.INT, Value: "0"}}
					}
				}
			}
		}
		if strings.HasSuffix(name, "/migrations/registry.go") {
			file.Decls = schemaRegistryDeclarations(file.Decls)
		}
		var canonical bytes.Buffer
		if err := format.Node(&canonical, fset, file); err != nil {
			return err
		}
		fmt.Fprintf(hash, "%s\x00%s\x00", name, canonical.Bytes())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	actual := fmt.Sprintf("%x", hash.Sum(nil))
	if expected, ok := schemaFingerprints[schemaVersion]; !ok || expected != actual {
		t.Fatalf("schema inputs changed: increment schemaVersion and add its fingerprint; version=%d fingerprint=%s", schemaVersion, actual)
	}
}

// Keep unknown/new declarations by default: schema helpers must not silently
// escape the gate. Only explicitly identified runner infrastructure is excluded.
func schemaRegistryDeclarations(decls []ast.Decl) []ast.Decl {
	runnerOnly := map[string]bool{
		"AutoMigrate": true, "validateMigrationSteps": true, "runSchemaMigrations": true,
		"validateMigrationForeignKeys": true, "withMigrationLock": true,
		"Commit": true, "Rollback": true, "migrateModelsWithProgress": true,
		"sqliteMigrationTx": true, "migrationStep": true,
		"schemaVersion": true, "migrationLockName": true, "migrationAdvisoryKey": true,
	}
	var result []ast.Decl
	for _, decl := range decls {
		switch node := decl.(type) {
		case *ast.FuncDecl:
			if runnerOnly[node.Name.Name] {
				continue
			}
			// The wrapper only adds per-table progress. Canonicalize its call to
			// the original AutoMigrate call; retain the complete model list.
			ast.Inspect(node, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if fn, ok := call.Fun.(*ast.Ident); ok && fn.Name == "migrateModelsWithProgress" && len(call.Args) > 0 {
					call.Fun = &ast.SelectorExpr{X: call.Args[0], Sel: ast.NewIdent("AutoMigrate")}
					call.Args = call.Args[1:]
				}
				return true
			})
		case *ast.GenDecl:
			if node.Tok == token.IMPORT {
				continue
			}
			var specs []ast.Spec
			for _, spec := range node.Specs {
				skip := false
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					skip = runnerOnly[spec.Name.Name]
				case *ast.ValueSpec:
					skip = len(spec.Names) == 1 && runnerOnly[spec.Names[0].Name]
				}
				if !skip {
					specs = append(specs, spec)
				}
			}
			node.Specs = specs
			if len(specs) == 0 {
				continue
			}
		}
		result = append(result, decl)
	}
	return result
}

func TestSchemaFingerprintExcludesRunnerButIncludesSchemaInputs(t *testing.T) {
	const source = `package migrations
const schemaVersion = 1
var schemaSteps = []migrationStep{}
func runSchemaMigrations() { lock() }
func migrateModelsWithProgress() { progress() }
func migrateSchema(db DB) { db.AutoMigrate(&Model{}); backfill() }
func backfill() { updateRows() }
`
	canonical := func(source string) string {
		t.Helper()
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "registry.go", source, 0)
		if err != nil {
			t.Fatal(err)
		}
		file.Decls = schemaRegistryDeclarations(file.Decls)
		var out bytes.Buffer
		if err := format.Node(&out, fset, file); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	before := canonical(source)
	for _, change := range [][2]string{{"lock()", "newLock()"}, {"progress()", "moreProgress()"}, {"schemaVersion = 1", "schemaVersion = 2"}, {"db.AutoMigrate(&Model{})", "migrateModelsWithProgress(db, &Model{})"}} {
		if canonical(strings.Replace(source, change[0], change[1], 1)) != before {
			t.Fatalf("runner-only change affected fingerprint: %v", change)
		}
	}
	for _, change := range [][2]string{{"&Model{}", "&NewModel{}"}, {"updateRows()", "differentRows()"}, {"[]migrationStep{}", "[]migrationStep{{version: 2}}"}} {
		if canonical(strings.Replace(source, change[0], change[1], 1)) == before {
			t.Fatalf("schema input escaped fingerprint: %v", change)
		}
	}
}

type migrationSQLRecorder struct {
	logger.Interface
	mu         sync.Mutex
	statements []string
}

func (r *migrationSQLRecorder) LogMode(logger.LogLevel) logger.Interface { return r }
func (r *migrationSQLRecorder) Trace(_ context.Context, _ time.Time, sql func() (string, int64), _ error) {
	statement, _ := sql()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statements = append(r.statements, statement)
}

func openVersionRunnerDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path+"?_pragma=foreign_keys(1)"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1) // A hidden fallback to the global pool would deadlock.
	t.Cleanup(func() { _ = pool.Close() })
	return db
}

func TestVersionRunnerFirstRepeatFailureAndUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "versions.sqlite")
	db := openVersionRunnerDB(t, path)
	if err := db.Exec("CREATE TABLE legacy_data (id INTEGER PRIMARY KEY, value INTEGER NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO legacy_data VALUES (1, 10)").Error; err != nil {
		t.Fatal(err)
	}
	failure := errors.New("injected migration failure")
	calls := 0
	migrate := func(tx *gorm.DB) error {
		calls++
		return tx.Transaction(func(inner *gorm.DB) error {
			if err := inner.Exec("UPDATE legacy_data SET value = value + 1").Error; err != nil {
				return err
			}
			if calls == 1 {
				return failure
			}
			return nil
		})
	}
	if err := runSchemaMigrations(db, 1, migrate); !errors.Is(err, failure) {
		t.Fatalf("failure lost: %v", err)
	}
	var value int
	if err := db.Raw("SELECT value FROM legacy_data").Scan(&value).Error; err != nil || value != 10 {
		t.Fatalf("rollback value=%d err=%v", value, err)
	}
	if err := runSchemaMigrations(db, 1, migrate); err != nil {
		t.Fatal(err)
	}
	// Reopen independently: completion must survive the original connection.
	reopened := openVersionRunnerDB(t, path)
	recorder := &migrationSQLRecorder{Interface: logger.Default}
	if err := runSchemaMigrations(reopened.Session(&gorm.Session{Logger: recorder}), 1, migrate); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("completed version reran: %d calls", calls)
	}
	if len(recorder.statements) != 2 {
		t.Fatalf("fast path must issue only ledger DDL/read via GORM: %v", recorder.statements)
	}
	for _, statement := range recorder.statements {
		if strings.Contains(statement, "sqlite_master") || strings.Contains(statement, "legacy_data") {
			t.Fatalf("fast path scanned schema/data: %s", statement)
		}
	}
	if err := runSchemaMigrations(reopened, 2, func(*gorm.DB) error { t.Fatal("upgrade reran baseline"); return nil }, migrationStep{2, "increment legacy value", migrate}); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("upgrade skipped: %d", calls)
	}
	if err := reopened.Raw("SELECT value FROM legacy_data").Scan(&value).Error; err != nil || value != 12 {
		t.Fatalf("upgrade value=%d err=%v", value, err)
	}
	if err := runSchemaMigrations(reopened, 1, migrate); err == nil {
		t.Fatal("downgrade accepted")
	}
	var fk int
	if err := db.Raw("PRAGMA foreign_keys").Scan(&fk).Error; err != nil || fk != 1 {
		t.Fatalf("foreign keys not restored: %d %v", fk, err)
	}
}

func TestVersionRunnerConcurrentConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.sqlite")
	first, second := openVersionRunnerDB(t, path), openVersionRunnerDB(t, path)
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	migrate := func(tx *gorm.DB) error {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return tx.Transaction(func(inner *gorm.DB) error { return inner.Exec("CREATE TABLE applied (id INTEGER)").Error })
	}
	results := make(chan error, 2)
	go func() { results <- runSchemaMigrations(first, 1, migrate) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first runner did not acquire lock")
	}
	go func() { results <- runSchemaMigrations(second, 1, migrate) }()
	select {
	case err := <-results:
		t.Fatalf("second runner bypassed held lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("parallel baseline calls=%d", calls.Load())
	}
}

func TestVersionRunnerCrossProcess(t *testing.T) {
	if path := os.Getenv("XMALL_MIGRATION_TEST_DB"); path != "" {
		db := openVersionRunnerDB(t, path)
		if err := runSchemaMigrations(db, 1, func(tx *gorm.DB) error {
			time.Sleep(100 * time.Millisecond)
			return tx.Exec("INSERT INTO executions (id) VALUES (1)").Error
		}); err != nil {
			t.Fatal(err)
		}
		return
	}
	path := filepath.Join(t.TempDir(), "processes.sqlite")
	db := openVersionRunnerDB(t, path)
	if err := db.Exec("CREATE TABLE executions (id INTEGER PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	commands := make([]*exec.Cmd, 3)
	outputs := make([]bytes.Buffer, len(commands))
	for i := range commands {
		commands[i] = exec.Command(os.Args[0], "-test.run=^TestVersionRunnerCrossProcess$", "-test.timeout=30s")
		commands[i].Env = append(os.Environ(), "XMALL_MIGRATION_TEST_DB="+path, "TEST_MYSQL_DSN=", "TEST_POSTGRES_DSN=")
		commands[i].Stdout, commands[i].Stderr = &outputs[i], &outputs[i]
		if err := commands[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for i, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("process %d: %v\n%s", i, err, outputs[i].String())
		}
	}
	var count int
	if err := db.Raw("SELECT COUNT(*) FROM executions").Scan(&count).Error; err != nil || count != 1 {
		t.Fatalf("executions=%d err=%v", count, err)
	}
}

func TestVersionRunnerPanicReleasesLockAndRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panic.sqlite")
	db := openVersionRunnerDB(t, path)
	func() {
		defer func() {
			if recover() != "injected panic" {
				t.Error("migration panic was swallowed")
			}
		}()
		_ = runSchemaMigrations(db, 1, func(tx *gorm.DB) error {
			if err := tx.Exec("CREATE TABLE incomplete (id INTEGER)").Error; err != nil {
				t.Fatal(err)
			}
			panic("injected panic")
		})
	}()
	if db.Migrator().HasTable("incomplete") {
		t.Fatal("panic committed partial migration")
	}
	retry := openVersionRunnerDB(t, path)
	if err := runSchemaMigrations(retry, 1, func(tx *gorm.DB) error { return tx.Exec("CREATE TABLE complete (id INTEGER)").Error }); err != nil {
		t.Fatal(err)
	}
}

func TestVersionRunnerUpgradeFailureKeepsPreviousVersion(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "retry-upgrade.sqlite"))
	if err := runSchemaMigrations(db, 1, func(*gorm.DB) error { return nil }); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("upgrade failed")
	if err := runSchemaMigrations(db, 2, migrateSchema, migrationStep{2, "failed upgrade", func(*gorm.DB) error { return failure }}); !errors.Is(err, failure) {
		t.Fatalf("got %v", err)
	}
	var current int64
	if err := db.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&current).Error; err != nil || current != 1 {
		t.Fatalf("failed upgrade recorded: %d %v", current, err)
	}
	if err := runSchemaMigrations(db, 2, migrateSchema, migrationStep{2, "retry upgrade", func(*gorm.DB) error { return nil }}); err != nil {
		t.Fatal(err)
	}
}

func TestVersionRunnerFreshSnapshotSkipsHistoricalSteps(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "fresh-current.sqlite"))
	baselineCalls := 0
	baseline := func(tx *gorm.DB) error {
		baselineCalls++
		return tx.Exec("CREATE TABLE current_model (id INTEGER PRIMARY KEY, new_column TEXT)").Error
	}
	steps := []migrationStep{
		{2, "old add column", func(tx *gorm.DB) error { return tx.Exec("ALTER TABLE current_model ADD COLUMN new_column TEXT").Error }},
		{3, "old data conversion", func(*gorm.DB) error { t.Fatal("fresh baseline ran historical step"); return nil }},
	}
	for i := 0; i < 2; i++ {
		if err := runSchemaMigrations(db, 3, baseline, steps...); err != nil {
			t.Fatal(err)
		}
	}
	var versions []int64
	if err := db.Raw("SELECT version FROM schema_migrations").Scan(&versions).Error; err != nil {
		t.Fatal(err)
	}
	if baselineCalls != 1 || len(versions) != 1 || versions[0] != 3 {
		t.Fatalf("baseline=%d ledger=%v", baselineCalls, versions)
	}
	steps = append(steps, migrationStep{4, "next upgrade", func(tx *gorm.DB) error { return tx.Exec("ALTER TABLE current_model ADD COLUMN next_column TEXT").Error }})
	if err := runSchemaMigrations(db, 4, baseline, steps...); err != nil {
		t.Fatal(err)
	}
	if baselineCalls != 1 {
		t.Fatal("snapshot baseline repeated on upgrade")
	}
}

func TestVersionRunnerRejectsInvalidPlansBeforeDatabaseAccess(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "invalid-plan.sqlite"))
	noop := func(*gorm.DB) error { return nil }
	for _, tc := range []struct {
		name   string
		target int64
		steps  []migrationStep
	}{
		{"missing", 3, []migrationStep{{2, "two", noop}}},
		{"duplicate", 3, []migrationStep{{2, "two", noop}, {2, "two again", noop}}},
		{"unordered", 3, []migrationStep{{3, "three", noop}, {2, "two", noop}}},
		{"gap", 3, []migrationStep{{2, "two", noop}, {4, "four", noop}}},
		{"nil callback", 2, []migrationStep{{2, "two", nil}}},
		{"empty name", 2, []migrationStep{{2, "", noop}}},
		{"unexpected step", 1, []migrationStep{{2, "two", noop}}},
		{"invalid target", 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := runSchemaMigrations(db, tc.target, noop, tc.steps...); err == nil {
				t.Fatal("invalid plan accepted")
			}
		})
	}
	if db.Migrator().HasTable("schema_migrations") {
		t.Fatal("invalid plan accessed database")
	}
}

func TestVersionRunnerRejectsUnknownAndGappedLedger(t *testing.T) {
	for _, versions := range [][]int64{{0}, {4}, {1, 3}} {
		db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "unknown.sqlite"))
		if err := db.Exec("CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, completed_at VARCHAR(40) NOT NULL)").Error; err != nil {
			t.Fatal(err)
		}
		for _, version := range versions {
			if err := db.Exec("INSERT INTO schema_migrations VALUES (?, '')", version).Error; err != nil {
				t.Fatal(err)
			}
		}
		unexpected := func(*gorm.DB) error { t.Fatal("invalid ledger ran migration"); return nil }
		if err := runSchemaMigrations(db, 3, unexpected, migrationStep{2, "two", unexpected}, migrationStep{3, "three", unexpected}); err == nil {
			t.Fatalf("accepted ledger %v", versions)
		}
	}
}

func TestVersionRunnerSQLiteMultiStepFailureRollsBackAttempt(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "multi-step.sqlite"))
	if err := runSchemaMigrations(db, 1, func(tx *gorm.DB) error { return tx.Exec("CREATE TABLE changes (version INTEGER PRIMARY KEY)").Error }); err != nil {
		t.Fatal(err)
	}
	baseline := func(*gorm.DB) error { t.Fatal("existing DB reran baseline"); return nil }
	calls := map[int64]int{}
	failure := errors.New("step three failed")
	step := func(version int64) migrationStep {
		return migrationStep{version, fmt.Sprintf("step %d", version), func(tx *gorm.DB) error {
			calls[version]++
			if err := tx.Exec("INSERT INTO changes VALUES (?)", version).Error; err != nil {
				return err
			}
			if version == 3 && calls[version] == 1 {
				return failure
			}
			return nil
		}}
	}
	steps := []migrationStep{step(2), step(3), step(4)}
	if err := runSchemaMigrations(db, 4, baseline, steps...); !errors.Is(err, failure) {
		t.Fatalf("got %v", err)
	}
	var current, count int64
	if err := db.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&current).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw("SELECT COUNT(*) FROM changes").Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if current != 1 || count != 0 || calls[4] != 0 {
		t.Fatalf("partial SQLite commit: version=%d count=%d calls=%v", current, count, calls)
	}
	if err := runSchemaMigrations(db, 4, baseline, steps...); err != nil {
		t.Fatal(err)
	}
	if calls[2] != 2 || calls[3] != 2 || calls[4] != 1 {
		t.Fatalf("retry calls=%v", calls)
	}
	// All successfully committed steps stay skipped on the next upgrade.
	steps = append(steps, step(5))
	if err := runSchemaMigrations(db, 5, baseline, steps...); err != nil {
		t.Fatal(err)
	}
	if calls[2] != 2 || calls[3] != 2 || calls[4] != 1 || calls[5] != 1 {
		t.Fatalf("completed steps repeated: %v", calls)
	}
}

// A tiny database/sql driver exercises MySQL's real runner/lock branch without
// opening sockets. It models autocommitted ledger writes, not MySQL DDL semantics.
type migrationLedgerConnector struct {
	versions []int64
	locked   bool
}
type migrationLedgerDriver struct{}
type migrationLedgerConn struct{ state *migrationLedgerConnector }
type migrationLedgerRows struct {
	values []driver.Value
	index  int
}

func (c *migrationLedgerConnector) Connect(context.Context) (driver.Conn, error) {
	return &migrationLedgerConn{state: c}, nil
}
func (*migrationLedgerConnector) Driver() driver.Driver { return migrationLedgerDriver{} }
func (migrationLedgerDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use connector")
}
func (*migrationLedgerConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (*migrationLedgerConn) Close() error { return nil }
func (*migrationLedgerConn) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected transaction")
}
func (c *migrationLedgerConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if !c.state.locked {
		return nil, errors.New("ledger write without owning lock")
	}
	switch {
	case strings.HasPrefix(query, "CREATE TABLE IF NOT EXISTS schema_migrations"):
	case strings.HasPrefix(query, "INSERT INTO schema_migrations"):
		c.state.versions = append(c.state.versions, args[0].Value.(int64))
	default:
		return nil, fmt.Errorf("unexpected exec: %s", query)
	}
	return driver.RowsAffected(1), nil
}
func (c *migrationLedgerConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	rows := &migrationLedgerRows{}
	switch query {
	case "SELECT GET_LOCK(?, 0)":
		if c.state.locked {
			return nil, errors.New("lock already owned")
		}
		c.state.locked = true
		rows.values = []driver.Value{int64(1)}
	case "SELECT RELEASE_LOCK(?)":
		if !c.state.locked {
			return nil, errors.New("lock not owned")
		}
		c.state.locked = false
		rows.values = []driver.Value{int64(1)}
	case "SELECT version FROM schema_migrations ORDER BY version":
		if !c.state.locked {
			return nil, errors.New("ledger read without lock")
		}
		for _, version := range c.state.versions {
			rows.values = append(rows.values, version)
		}
	default:
		return nil, fmt.Errorf("unexpected query: %s", query)
	}
	return rows, nil
}
func (*migrationLedgerRows) Columns() []string { return []string{"version"} }
func (*migrationLedgerRows) Close() error      { return nil }
func (r *migrationLedgerRows) Next(dest []driver.Value) error {
	if r.index == len(r.values) {
		return io.EOF
	}
	dest[0] = r.values[r.index]
	r.index++
	return nil
}

func TestVersionRunnerMySQLRetryPreservesCompletedSteps(t *testing.T) {
	state := &migrationLedgerConnector{versions: []int64{1}}
	pool := sql.OpenDB(state)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: pool, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	baseline := func(*gorm.DB) error { t.Fatal("versioned MySQL reran baseline"); return nil }
	failure := errors.New("injected step failure")
	calls := map[int64]int{}
	step := func(version int64) migrationStep {
		return migrationStep{version, fmt.Sprintf("step %d", version), func(tx *gorm.DB) error {
			if _, ok := tx.Statement.ConnPool.(*sql.Conn); !ok || !state.locked {
				t.Fatal("step lost pinned lock connection")
			}
			calls[version]++
			if version == 3 && calls[version] == 1 {
				return failure
			}
			return nil
		}}
	}
	steps := []migrationStep{step(2), step(3), step(4)}
	if err := runSchemaMigrations(db, 4, baseline, steps...); !errors.Is(err, failure) {
		t.Fatalf("got %v", err)
	}
	if fmt.Sprint(state.versions) != "[1 2]" || state.locked || calls[4] != 0 {
		t.Fatalf("state=%+v calls=%v", state, calls)
	}
	if err := runSchemaMigrations(db, 4, baseline, steps...); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(state.versions) != "[1 2 3 4]" || state.locked || calls[2] != 1 || calls[3] != 2 || calls[4] != 1 {
		t.Fatalf("retry state=%+v calls=%v", state, calls)
	}
}

func TestVersionRunnerRejectsForeignKeyViolationsWithoutCompletion(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "foreign-keys.sqlite"))
	err := runSchemaMigrations(db, 1, func(tx *gorm.DB) error {
		for _, query := range []string{
			"CREATE TABLE parent (id INTEGER PRIMARY KEY)",
			"CREATE TABLE child (id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES parent(id))",
			"INSERT INTO child VALUES (1, 99)",
		} {
			if err := tx.Exec(query).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "foreign key validation") {
		t.Fatalf("invalid migration accepted: %v", err)
	}
	if db.Migrator().HasTable("schema_migrations") || db.Migrator().HasTable("child") {
		t.Fatal("invalid baseline committed")
	}
}

func TestVersionRunnerUsesLockedDBWithoutReplacingGlobal(t *testing.T) {
	db := openVersionRunnerDB(t, filepath.Join(t.TempDir(), "baseline.sqlite"))
	previous := gormdb.DB
	gormdb.DB = nil
	t.Cleanup(func() { gormdb.DB = previous })
	originalLogger := db.Logger
	originalPool := db.Statement.ConnPool
	if err := runSchemaMigrations(db, schemaVersion, migrateSchema, schemaSteps...); err != nil {
		t.Fatal(err)
	}
	if gormdb.DB != nil || db.Logger != originalLogger || db.Statement.ConnPool != originalPool {
		t.Fatal("runner changed business DB/logger/connection pool")
	}
	if err := runSchemaMigrations(db, schemaVersion, func(*gorm.DB) error { t.Fatal("full baseline repeated"); return nil }, schemaSteps...); err != nil {
		t.Fatal(err)
	}
}

func setupSKUMigrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := gormdb.DB
	t.Cleanup(func() {
		gormdb.DB = previousDB
	})
	dsn := fmt.Sprintf("file:sku_migration_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	gormdb.DB = db
	return db
}

func setupRegistryMigrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := gormdb.DB
	t.Cleanup(func() {
		gormdb.DB = previousDB
	})
	dsn := fmt.Sprintf("file:registry_migration_test_%d?mode=memory&cache=shared&_pragma=foreign_keys(1)", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	gormdb.DB = db
	return db
}

func modelIndexNames(t *testing.T, db *gorm.DB, model interface{}) []string {
	t.Helper()
	statement := &gorm.Statement{DB: db}
	if err := statement.Parse(model); err != nil {
		t.Fatalf("parse model indexes: %v", err)
	}
	indexes := statement.Schema.ParseIndexes()
	names := make([]string, 0, len(indexes))
	for _, index := range indexes {
		names = append(names, index.Name)
	}
	if len(names) == 0 {
		t.Fatalf("model %T does not declare any indexes", model)
	}
	return names
}

func assertModelIndexesExist(t *testing.T, db *gorm.DB, model interface{}) {
	t.Helper()
	for _, name := range modelIndexNames(t, db, model) {
		if !db.Migrator().HasIndex(model, name) {
			t.Errorf("model %T index %s does not exist", model, name)
		}
	}
}

func dropModelIndexes(t *testing.T, db *gorm.DB, model interface{}) {
	t.Helper()
	for _, name := range modelIndexNames(t, db, model) {
		if !db.Migrator().HasIndex(model, name) {
			continue
		}
		if err := db.Migrator().DropIndex(model, name); err != nil {
			t.Fatalf("drop model %T index %s: %v", model, name, err)
		}
	}
}

func TestEnsureProductSKUMigrationBackfillLegacyData(t *testing.T) {
	db := setupSKUMigrationTestDB(t)

	if err := db.AutoMigrate(
		&productdomain.Product{},
		&productdomain.ProductSKU{},
		&orderdomain.OrderItem{},
		&cartdomain.Item{},
		&cardsecretdomain.Secret{},
		&cardsecretdomain.Batch{},
		&settingsstore.SettingRecord{},
	); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}

	product := &productdomain.Product{
		CategoryID:        1,
		Slug:              "sku-migration-legacy",
		TitleJSON:         jsonmap.JSON{"zh-CN": "历史商品"},
		PriceAmount:       money.FromDecimal(decimal.NewFromInt(128)),
		PurchaseType:      "member",
		FulfillmentType:   "manual",
		ManualStockTotal:  20,
		ManualStockLocked: 3,
		ManualStockSold:   5,
		IsActive:          true,
	}
	if err := db.Create(product).Error; err != nil {
		t.Fatalf("create product failed: %v", err)
	}

	now := time.Now()
	orderItem := &orderdomain.OrderItem{
		OrderID:         1,
		ProductID:       product.ID,
		SKUID:           0,
		TitleJSON:       jsonmap.JSON{"zh-CN": "历史商品"},
		UnitPrice:       product.PriceAmount,
		Quantity:        1,
		TotalPrice:      product.PriceAmount,
		FulfillmentType: "manual",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(orderItem).Error; err != nil {
		t.Fatalf("create order item failed: %v", err)
	}

	cartItem := &cartdomain.Item{
		UserID:          1001,
		ProductID:       product.ID,
		SKUID:           0,
		Quantity:        2,
		FulfillmentType: "manual",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(cartItem).Error; err != nil {
		t.Fatalf("create cart item failed: %v", err)
	}

	batch := &cardsecretdomain.Batch{
		ProductID:  product.ID,
		SKUID:      0,
		BatchNo:    "SKU-MIGRATION-BATCH-001",
		Source:     "manual",
		TotalCount: 1,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := db.Create(batch).Error; err != nil {
		t.Fatalf("create card secret batch failed: %v", err)
	}

	secret := &cardsecretdomain.Secret{
		ProductID: product.ID,
		SKUID:     0,
		BatchID:   &batch.ID,
		Secret:    "CARD-001",
		Status:    cardsecretdomain.StatusAvailable,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(secret).Error; err != nil {
		t.Fatalf("create card secret failed: %v", err)
	}

	if err := ensureProductSKUMigration(); err != nil {
		t.Fatalf("ensure sku migration failed: %v", err)
	}

	var sku productdomain.ProductSKU
	if err := db.Where("product_id = ? AND sku_code = ?", product.ID, productdomain.DefaultSKUCode).First(&sku).Error; err != nil {
		t.Fatalf("query default sku failed: %v", err)
	}
	if !sku.PriceAmount.Decimal.Equal(product.PriceAmount.Decimal) {
		t.Fatalf("default sku price mismatch want %s got %s", product.PriceAmount.String(), sku.PriceAmount.String())
	}
	if sku.ManualStockTotal != product.ManualStockTotal || sku.ManualStockLocked != product.ManualStockLocked || sku.ManualStockSold != product.ManualStockSold {
		t.Fatalf("default sku stock snapshot mismatch")
	}

	var gotOrderItem orderdomain.OrderItem
	if err := db.First(&gotOrderItem, orderItem.ID).Error; err != nil {
		t.Fatalf("reload order item failed: %v", err)
	}
	if gotOrderItem.SKUID != sku.ID {
		t.Fatalf("order item sku_id want %d got %d", sku.ID, gotOrderItem.SKUID)
	}

	var gotCartItem cartdomain.Item
	if err := db.First(&gotCartItem, cartItem.ID).Error; err != nil {
		t.Fatalf("reload cart item failed: %v", err)
	}
	if gotCartItem.SKUID != sku.ID {
		t.Fatalf("cart item sku_id want %d got %d", sku.ID, gotCartItem.SKUID)
	}

	var gotBatch cardsecretdomain.Batch
	if err := db.First(&gotBatch, batch.ID).Error; err != nil {
		t.Fatalf("reload card secret batch failed: %v", err)
	}
	if gotBatch.SKUID != sku.ID {
		t.Fatalf("card secret batch sku_id want %d got %d", sku.ID, gotBatch.SKUID)
	}

	var gotSecret cardsecretdomain.Secret
	if err := db.First(&gotSecret, secret.ID).Error; err != nil {
		t.Fatalf("reload card secret failed: %v", err)
	}
	if gotSecret.SKUID != sku.ID {
		t.Fatalf("card secret sku_id want %d got %d", sku.ID, gotSecret.SKUID)
	}

	if err := ensureProductSKUMigration(); err != nil {
		t.Fatalf("ensure sku migration second run failed: %v", err)
	}

	var skuCount int64
	if err := db.Model(&productdomain.ProductSKU{}).Where("product_id = ?", product.ID).Count(&skuCount).Error; err != nil {
		t.Fatalf("count product sku failed: %v", err)
	}
	if skuCount != 1 {
		t.Fatalf("idempotent check failed: sku count want 1 got %d", skuCount)
	}
}

func TestMappingExternalIDLengthPreflightPreservesOversizedValues(t *testing.T) {
	for _, field := range []struct{ table, column string }{
		{"product_mappings", "upstream_product_external_id"},
		{"sku_mappings", "upstream_sku_external_id"},
	} {
		t.Run(field.table, func(t *testing.T) {
			db := setupSKUMigrationTestDB(t)
			if err := validateMappingExternalIDLengths(db); err != nil {
				t.Fatalf("empty database preflight: %v", err)
			}
			if err := db.Exec("CREATE TABLE " + field.table + " (id INTEGER PRIMARY KEY, " + field.column + " TEXT)").Error; err != nil {
				t.Fatal(err)
			}
			value := strings.Repeat("界", 256)
			if err := db.Table(field.table).Create(map[string]interface{}{"id": 1, field.column: value}).Error; err != nil {
				t.Fatal(err)
			}
			if err := validateMappingExternalIDLengths(db); err == nil || !strings.Contains(err.Error(), "no rows changed") {
				t.Fatalf("expected actionable oversized ID failure, got %v", err)
			}
			var preserved string
			if err := db.Table(field.table).Select(field.column).Scan(&preserved).Error; err != nil || preserved != value {
				t.Fatalf("opaque ID changed: %q, err=%v", preserved, err)
			}
			if err := db.Table(field.table).Where("id = 1").Update(field.column, strings.Repeat("界", 255)).Error; err != nil {
				t.Fatal(err)
			}
			if err := validateMappingExternalIDLengths(db); err != nil {
				t.Fatalf("255 Unicode characters must fit: %v", err)
			}
		})
	}
}

func TestMigrateCartSKUUniqueIndex(t *testing.T) {
	db := setupSKUMigrationTestDB(t)
	if err := db.AutoMigrate(&cartdomain.Item{}); err != nil {
		t.Fatalf("auto migrate cart item failed: %v", err)
	}

	// 构造历史唯一索引，验证迁移函数会移除该索引并保留新索引。
	if err := db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_cart_user_product ON cart_items(user_id, product_id)").Error; err != nil {
		t.Fatalf("create legacy index failed: %v", err)
	}

	if err := migrateCartSKUUniqueIndex(); err != nil {
		t.Fatalf("migrate cart unique index failed: %v", err)
	}

	if db.Migrator().HasIndex(&cartdomain.Item{}, "idx_cart_user_product") {
		t.Fatalf("legacy unique index idx_cart_user_product should be dropped")
	}
	if !db.Migrator().HasIndex(&cartdomain.Item{}, "idx_cart_user_product_sku") {
		t.Fatalf("new unique index idx_cart_user_product_sku should exist")
	}
}

func TestEnsureUserOAuthIdentityUserProviderUniqueIndexFailsClosedOnLegacyDuplicates(t *testing.T) {
	db := setupSKUMigrationTestDB(t)
	if err := db.AutoMigrate(&externalidentitydomain.Identity{}); err != nil {
		t.Fatalf("auto migrate identity: %v", err)
	}
	now := time.Now()
	first := &externalidentitydomain.Identity{
		UserID: 42, Provider: "telegram", ProviderUserID: "legacy-effective",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}
	second := &externalidentitydomain.Identity{
		UserID: 42, Provider: "telegram", ProviderUserID: "legacy-hidden",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(first).Error; err != nil {
		t.Fatalf("create first legacy identity: %v", err)
	}
	if err := db.Create(second).Error; err != nil {
		t.Fatalf("create duplicate legacy identity: %v", err)
	}

	err := ensureUserOAuthIdentityUserProviderUniqueIndex()
	if err == nil {
		t.Fatalf("expected explicit duplicate preflight failure")
	}
	for _, expected := range []string{
		userOAuthIdentityUserProviderUniqueIndex,
		"1 duplicate user/provider group",
		"user_id=42",
		`provider="telegram"`,
		"count=2",
		"SELECT id,user_id,provider,provider_user_id",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("migration error %q does not include %q", err, expected)
		}
	}
	if db.Migrator().HasIndex(
		&userOAuthIdentityUserProviderIndexSchema{},
		userOAuthIdentityUserProviderUniqueIndex,
	) {
		t.Fatalf("index %s must not exist after dirty preflight failure", userOAuthIdentityUserProviderUniqueIndex)
	}

	var identities []externalidentitydomain.Identity
	if err := db.Where("user_id = ? AND provider = ?", 42, "telegram").
		Order("id ASC").
		Find(&identities).Error; err != nil {
		t.Fatalf("load deduplicated identities: %v", err)
	}
	if len(identities) != 2 || identities[0].ID != first.ID || identities[1].ID != second.ID {
		t.Fatalf("dirty migration changed credentials: %+v", identities)
	}

	// After an operator resolves the duplicate deliberately, the same migration
	// creates the invariant and remains idempotent.
	if err := db.Delete(second).Error; err != nil {
		t.Fatalf("resolve duplicate explicitly: %v", err)
	}
	if err := ensureUserOAuthIdentityUserProviderUniqueIndex(); err != nil {
		t.Fatalf("ensure unique index after resolution: %v", err)
	}
	if err := ensureUserOAuthIdentityUserProviderUniqueIndex(); err != nil {
		t.Fatalf("idempotent ensure unique index: %v", err)
	}
	if !db.Migrator().HasIndex(
		&userOAuthIdentityUserProviderIndexSchema{},
		userOAuthIdentityUserProviderUniqueIndex,
	) {
		t.Fatalf("missing index %s", userOAuthIdentityUserProviderUniqueIndex)
	}

	duplicate := &externalidentitydomain.Identity{
		UserID: 42, Provider: "telegram", ProviderUserID: "must-fail",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(duplicate).Error; err == nil {
		t.Fatalf("expected database unique constraint for same user/provider")
	}
	otherUser := &externalidentitydomain.Identity{
		UserID: 43, Provider: "telegram", ProviderUserID: "other-user",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(otherUser).Error; err != nil {
		t.Fatalf("same provider on another user should remain valid: %v", err)
	}
}

type racingNamedIndexMigrator struct {
	hasIndexResults []bool
	hasIndexCalls   int
	createCalls     int
	createErr       error
}

func (m *racingNamedIndexMigrator) HasIndex(interface{}, string) bool {
	index := m.hasIndexCalls
	m.hasIndexCalls++
	if index >= len(m.hasIndexResults) {
		return false
	}
	return m.hasIndexResults[index]
}

func (m *racingNamedIndexMigrator) CreateIndex(interface{}, string) error {
	m.createCalls++
	return m.createErr
}

func TestCreateIndexConvergingOnExistingHandlesOnlyConcurrentWinner(t *testing.T) {
	ddlErr := errors.New("index already exists")
	migrator := &racingNamedIndexMigrator{
		hasIndexResults: []bool{false, true},
		createErr:       ddlErr,
	}
	if err := createIndexConvergingOnExisting(migrator, &struct{}{}, "idx_target"); err != nil {
		t.Fatalf("concurrent winner should converge: %v", err)
	}
	if migrator.createCalls != 1 || migrator.hasIndexCalls != 2 {
		t.Fatalf("calls = create:%d has:%d, want 1/2", migrator.createCalls, migrator.hasIndexCalls)
	}
}

func TestCreateIndexConvergingOnExistingPreservesDDLFailure(t *testing.T) {
	ddlErr := errors.New("duplicate rows prevent unique index")
	migrator := &racingNamedIndexMigrator{
		hasIndexResults: []bool{false, false},
		createErr:       ddlErr,
	}
	err := createIndexConvergingOnExisting(migrator, &struct{}{}, "idx_target")
	if !errors.Is(err, ddlErr) {
		t.Fatalf("error = %v, want original DDL error", err)
	}
}

func TestAutoMigrateOwnsResellerSchemaAndCrossModuleConstraints(t *testing.T) {
	db := setupRegistryMigrationTestDB(t)
	if err := AutoMigrate(); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}
	if err := AutoMigrate(); err != nil {
		t.Fatalf("idempotent auto migrate failed: %v", err)
	}
	for _, table := range []string{
		"reseller_profiles",
		"reseller_domains",
		"reseller_site_configs",
		"reseller_product_settings",
		"reseller_order_snapshots",
		"reseller_ledger_entries",
		"reseller_withdraw_requests",
		"reseller_balance_accounts",
		"reseller_related_accounts",
	} {
		if !db.Migrator().HasTable(table) {
			t.Errorf("central AutoMigrate did not create reseller table %s", table)
		}
	}
	for _, index := range []struct {
		model interface{}
		name  string
	}{
		{model: &resellerdomain.Domain{}, name: "idx_reseller_domains_active_domain"},
		{model: &resellerdomain.SiteConfig{}, name: "idx_reseller_site_configs_active_reseller"},
		{model: &resellerdomain.ProductSetting{}, name: "idx_reseller_product_settings_active_scope"},
		{model: &resellerdomain.BalanceAccount{}, name: "idx_reseller_balance_accounts_active_currency"},
		{model: &resellerdomain.RelatedAccount{}, name: "idx_reseller_related_accounts_active_user"},
	} {
		if !db.Migrator().HasIndex(index.model, index.name) {
			t.Errorf("central AutoMigrate did not create reseller index %s", index.name)
		}
	}
	for _, constraint := range []string{
		cartProductForeignKeyConstraint,
		cartSKUForeignKeyConstraint,
	} {
		if !db.Migrator().HasConstraint(&cartItemConstraintSchema{}, constraint) {
			t.Errorf("central AutoMigrate did not create cart constraint %s", constraint)
		}
	}
	if !db.Migrator().HasConstraint(&procurementdomain.Order{}, procurementOrderForeignKeyConstraint) {
		t.Errorf("central AutoMigrate did not create procurement constraint %s", procurementOrderForeignKeyConstraint)
	}
	if db.Migrator().HasConstraint(&procurementdomain.Order{}, supersededProcurementOrderForeignKeyConstraint) {
		t.Errorf("central AutoMigrate retained superseded procurement constraint %s", supersededProcurementOrderForeignKeyConstraint)
	}
}

func TestBackfillPendingOrderRiskIPsHandlesLegacyNullsAndIPv6(t *testing.T) {
	db := setupSKUMigrationTestDB(t)
	if err := db.AutoMigrate(&orderdomain.Order{}); err != nil {
		t.Fatalf("auto migrate orders: %v", err)
	}

	pendingIPv4 := &orderdomain.Order{OrderNo: "risk-ipv4", Status: constants.OrderStatusPendingPayment, Currency: "USD", ClientIP: "1.2.3.4"}
	pendingIPv6 := &orderdomain.Order{OrderNo: "risk-ipv6", Status: constants.OrderStatusPendingPayment, Currency: "USD", ClientIP: "2001:db8:abcd:12::9"}
	paid := &orderdomain.Order{OrderNo: "risk-paid", Status: constants.OrderStatusPaid, Currency: "USD", ClientIP: "5.6.7.8"}
	for _, order := range []*orderdomain.Order{pendingIPv4, pendingIPv6, paid} {
		if err := db.Create(order).Error; err != nil {
			t.Fatalf("create order %s: %v", order.OrderNo, err)
		}
		if err := db.Exec("UPDATE orders SET risk_ip = NULL WHERE id = ?", order.ID).Error; err != nil {
			t.Fatalf("set legacy NULL risk_ip for %s: %v", order.OrderNo, err)
		}
	}

	if err := backfillPendingOrderRiskIPs(db); err != nil {
		t.Fatalf("backfill risk IPs: %v", err)
	}
	var gotIPv4, gotIPv6, gotPaid orderdomain.Order
	if err := db.First(&gotIPv4, pendingIPv4.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&gotIPv6, pendingIPv6.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&gotPaid, paid.ID).Error; err != nil {
		t.Fatal(err)
	}
	if gotIPv4.RiskIP != "1.2.3.4" || gotIPv6.RiskIP != "2001:db8:abcd:12::/64" {
		t.Fatalf("unexpected risk IP backfill: ipv4=%q ipv6=%q", gotIPv4.RiskIP, gotIPv6.RiskIP)
	}
	if gotPaid.RiskIP != "" {
		t.Fatalf("paid history must not be backfilled, got %q", gotPaid.RiskIP)
	}
}

func TestEnsureCartForeignKeyConstraintsPreservesRowsAndEnforcesReferences(t *testing.T) {
	db := setupRegistryMigrationTestDB(t)
	for _, statement := range []string{
		`CREATE TABLE products (id integer PRIMARY KEY)`,
		`CREATE TABLE product_skus (id integer PRIMARY KEY)`,
		`INSERT INTO products (id) VALUES (1)`,
		`INSERT INTO product_skus (id) VALUES (2)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("prepare cart schema: %v", err)
		}
	}
	if err := db.AutoMigrate(&cartdomain.Item{}); err != nil {
		t.Fatalf("prepare indexed cart schema: %v", err)
	}
	if err := db.Exec(`INSERT INTO cart_items (id, user_id, product_id, sku_id, quantity) VALUES (3, 4, 1, 2, 1)`).Error; err != nil {
		t.Fatalf("prepare cart row: %v", err)
	}
	assertModelIndexesExist(t, db, &cartdomain.Item{})

	if err := ensureCartForeignKeyConstraints(); err != nil {
		t.Fatalf("ensure cart constraints: %v", err)
	}
	assertModelIndexesExist(t, db, &cartdomain.Item{})
	var rowCount int64
	if err := db.Table("cart_items").Where("id = ?", 3).Count(&rowCount).Error; err != nil {
		t.Fatalf("count preserved cart rows: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("cart constraint migration lost existing row: count=%d", rowCount)
	}
	if err := db.Exec(`INSERT INTO cart_items (id, user_id, product_id, sku_id, quantity) VALUES (5, 6, 999, 2, 1)`).Error; err == nil {
		t.Fatal("cart product foreign key did not reject missing product")
	}
	if err := db.Exec(`INSERT INTO cart_items (id, user_id, product_id, sku_id, quantity) VALUES (7, 8, 1, 999, 1)`).Error; err == nil {
		t.Fatal("cart SKU foreign key did not reject missing SKU")
	}

	dropModelIndexes(t, db, &cartdomain.Item{})
	if err := ensureCartForeignKeyConstraints(); err != nil {
		t.Fatalf("repair cart indexes with existing constraints: %v", err)
	}
	assertModelIndexesExist(t, db, &cartdomain.Item{})
	for _, constraint := range []string{cartProductForeignKeyConstraint, cartSKUForeignKeyConstraint} {
		if !db.Migrator().HasConstraint(&cartItemConstraintSchema{}, constraint) {
			t.Errorf("cart index repair removed constraint %s", constraint)
		}
	}
	if err := db.Exec(`INSERT INTO cart_items (id, user_id, product_id, sku_id, quantity) VALUES (9, 4, 1, 2, 1)`).Error; err == nil {
		t.Fatal("repaired cart unique index did not reject duplicate user/product/SKU")
	}
}

func TestEnsureCartForeignKeyConstraintsRejectsOrphansBeforeSchemaChanges(t *testing.T) {
	db := setupRegistryMigrationTestDB(t)
	for _, statement := range []string{
		`CREATE TABLE products (id integer PRIMARY KEY)`,
		`CREATE TABLE product_skus (id integer PRIMARY KEY)`,
		`CREATE TABLE cart_items (id integer PRIMARY KEY, user_id integer NOT NULL, product_id integer NOT NULL, sku_id integer NOT NULL, quantity integer NOT NULL)`,
		`INSERT INTO product_skus (id) VALUES (2)`,
		`INSERT INTO cart_items (id, user_id, product_id, sku_id, quantity) VALUES (3, 4, 999, 2, 1)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("prepare orphaned cart schema: %v", err)
		}
	}

	err := ensureCartForeignKeyConstraints()
	if err == nil || !strings.Contains(err.Error(), cartProductForeignKeyConstraint) {
		t.Fatalf("expected descriptive product reference error, got %v", err)
	}
	for _, constraint := range []string{cartProductForeignKeyConstraint, cartSKUForeignKeyConstraint} {
		if db.Migrator().HasConstraint(&cartItemConstraintSchema{}, constraint) {
			t.Fatalf("orphan preflight left partial constraint %s", constraint)
		}
	}
}

type supersededProcurementOrderSchema struct {
	ID                  uint `gorm:"primarykey"`
	LocalOrderID        uint
	LocalOrderReference *supersededProcurementOrderReference `gorm:"foreignKey:LocalOrderID;references:ID;constraint:fk_procurement_orders_local_order_reference"`
}

func (supersededProcurementOrderSchema) TableName() string { return "procurement_orders" }

type supersededProcurementOrderReference struct {
	ID uint `gorm:"primarykey"`
}

func (supersededProcurementOrderReference) TableName() string { return "orders" }

type procurementOrderIndexSchema struct {
	ID                      uint       `gorm:"primarykey"`
	ConnectionID            uint       `gorm:"index"`
	LocalOrderID            uint       `gorm:"uniqueIndex:uidx_procurement_local_order"`
	NotificationPending     bool       `gorm:"index"`
	UpstreamOrderExternalID string     `gorm:"index"`
	LocalOrderNo            string     `gorm:"index"`
	UpstreamOrderNo         string     `gorm:"index"`
	Status                  string     `gorm:"index"`
	NextRetryAt             *time.Time `gorm:"index"`
	TraceID                 string     `gorm:"index"`
	CreatedAt               time.Time  `gorm:"index"`
	UpdatedAt               time.Time  `gorm:"index"`
	DeletedAt               *time.Time `gorm:"index"`
}

func (procurementOrderIndexSchema) TableName() string { return "procurement_orders" }

func TestProcurementUniqueMigrationRejectsDuplicatesWithoutDeletion(t *testing.T) {
	db := setupRegistryMigrationTestDB(t)
	if err := db.Exec("CREATE TABLE procurement_orders (id INTEGER PRIMARY KEY, local_order_id INTEGER)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO procurement_orders (id,local_order_id) VALUES (1,7),(2,7)").Error; err != nil {
		t.Fatal(err)
	}
	if err := validateProcurementLocalUniqueness(db); err == nil {
		t.Fatal("duplicates must block migration")
	}
	var count int64
	db.Table("procurement_orders").Count(&count)
	if count != 2 {
		t.Fatalf("migration deleted rows: %d", count)
	}
}

func TestAutoMigrateReplacesSupersededProcurementConstraintName(t *testing.T) {
	db := setupRegistryMigrationTestDB(t)
	if err := db.AutoMigrate(&supersededProcurementOrderSchema{}); err != nil {
		t.Fatalf("create superseded procurement schema failed: %v", err)
	}
	if err := db.AutoMigrate(&procurementOrderIndexSchema{}); err != nil {
		t.Fatalf("create indexed superseded procurement schema failed: %v", err)
	}
	assertModelIndexesExist(t, db, &procurementdomain.Order{})
	if !db.Migrator().HasConstraint(&supersededProcurementOrderSchema{}, supersededProcurementOrderForeignKeyConstraint) {
		t.Fatalf("test setup did not create superseded procurement constraint")
	}

	if err := AutoMigrate(); err != nil {
		t.Fatalf("auto migrate superseded schema failed: %v", err)
	}
	if db.Migrator().HasConstraint(&procurementdomain.Order{}, supersededProcurementOrderForeignKeyConstraint) {
		t.Errorf("superseded procurement constraint %s still exists", supersededProcurementOrderForeignKeyConstraint)
	}
	if !db.Migrator().HasConstraint(&procurementdomain.Order{}, procurementOrderForeignKeyConstraint) {
		t.Errorf("canonical procurement constraint %s does not exist", procurementOrderForeignKeyConstraint)
	}
	assertModelIndexesExist(t, db, &procurementdomain.Order{})

	dropModelIndexes(t, db, &procurementdomain.Order{})
	if err := ensureProcurementOrderForeignKeyConstraint(); err != nil {
		t.Fatalf("repair procurement indexes with canonical constraint: %v", err)
	}
	assertModelIndexesExist(t, db, &procurementdomain.Order{})
	if !db.Migrator().HasConstraint(&procurementdomain.Order{}, procurementOrderForeignKeyConstraint) {
		t.Errorf("procurement index repair removed canonical constraint %s", procurementOrderForeignKeyConstraint)
	}
}
