package migrationintegrationtest

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	procurementdomain "github.com/XManCodeing/XMall/internal/modules/procurement/domain"
	resellerdomain "github.com/XManCodeing/XMall/internal/modules/reseller/domain"
	runtimedomain "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"github.com/XManCodeing/XMall/internal/platform/database/gormdb"
	"github.com/glebarez/sqlite"
	"testing"

	"github.com/XManCodeing/XMall/internal/bootstrap/database/migrations"
	"gorm.io/gorm"
)

// TestHistoricalUpgradeCoversCurrentBaseline compares the frozen v9 upgrade chain
// with a fresh baseline using disposable SQLite databases. t reports missing
// tables, column attributes and named indexes; legacy extra columns are allowed.
// Historical steps use current models, so this is not a frozen v44 source audit.
func TestHistoricalUpgradeCoversCurrentBaseline(t *testing.T) {
	baseline := openMigrationDB(t)
	if err := migrations.AutoMigrate(); err != nil {
		t.Fatal(err)
	}
	upgraded := openMigrationDB(t)
	fixture, err := os.ReadFile("../testdata/v9_sqlite.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := upgraded.Exec(string(fixture)).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrations.AutoMigrate(); err != nil {
			t.Fatal(err)
		}
	}
	var tables []string
	if err := baseline.Raw("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name").Scan(&tables).Error; err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		t.Run(table, func(t *testing.T) {
			if !upgraded.Migrator().HasTable(table) {
				t.Fatal("incremental upgrade is missing baseline table")
			}
			actualColumns := sqliteMigrationColumns(t, upgraded, table)
			for name, expected := range sqliteMigrationColumns(t, baseline, table) {
				if actualColumns[name] != expected {
					t.Errorf("incremental column %s = %q, baseline = %q", name, actualColumns[name], expected)
				}
			}
			actual := sqliteMigrationIndexes(t, upgraded, table)
			for name, expected := range sqliteMigrationIndexes(t, baseline, table) {
				if actual[name] != expected {
					t.Errorf("incremental index %s = %q, baseline = %q", name, actual[name], expected)
				}
			}
		})
	}
}

// sqliteMigrationColumns returns type/nullability/default/PK signatures for table
// in db; t reports query errors. Names come only from the local SQLite catalog.
func sqliteMigrationColumns(t *testing.T, db *gorm.DB, table string) map[string]string {
	t.Helper()
	var rows []struct {
		Name      string
		Type      string
		Notnull   int
		DfltValue *string
		Pk        int
	}
	if err := db.Raw("PRAGMA table_info(" + quoteMigrationIdentifier(table) + ")").Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	columns := make(map[string]string, len(rows))
	for _, row := range rows {
		defaultValue := "<absent>"
		if row.DfltValue != nil {
			defaultValue = *row.DfltValue
		}
		columns[row.Name] = fmt.Sprintf("%s null=%d default=%s pk=%d", strings.ToUpper(row.Type), row.Notnull, defaultValue, row.Pk)
	}
	return columns
}

// sqliteMigrationIndexes returns ordered column/uniqueness signatures for named
// indexes on table in db. SQLite internal autoindexes are excluded because their
// numbering is creation-order dependent. t reports inspection errors.
func sqliteMigrationIndexes(t *testing.T, db *gorm.DB, table string) map[string]string {
	t.Helper()
	var indexes []struct {
		Name    string
		Unique  int
		Partial int
	}
	if err := db.Raw("PRAGMA index_list(" + quoteMigrationIdentifier(table) + ")").Scan(&indexes).Error; err != nil {
		t.Fatal(err)
	}
	result := make(map[string]string)
	for _, index := range indexes {
		if strings.HasPrefix(index.Name, "sqlite_autoindex_") {
			continue
		}
		var fields []struct {
			Seqno int
			Name  string
		}
		if err := db.Raw("PRAGMA index_info(" + quoteMigrationIdentifier(index.Name) + ")").Scan(&fields).Error; err != nil {
			t.Fatal(err)
		}
		sort.Slice(fields, func(left, right int) bool { return fields[left].Seqno < fields[right].Seqno })
		columns := make([]string, 0, len(fields))
		for _, field := range fields {
			columns = append(columns, field.Name)
		}
		result[index.Name] = fmt.Sprintf("unique=%d partial=%d columns=%s", index.Unique, index.Partial, strings.Join(columns, ","))
	}
	return result
}

// TestAutoMigrateOwnsResellerSchemaAndCrossModuleConstraints checks the public schema contract.
// t owns the disposable database; replay must preserve reseller indexes and canonical foreign keys.
func TestAutoMigrateOwnsResellerSchemaAndCrossModuleConstraints(t *testing.T) {
	db := openMigrationDB(t)
	for range 2 {
		if err := migrations.AutoMigrate(); err != nil {
			t.Fatalf("auto migrate failed: %v", err)
		}
	}
	for _, table := range []string{
		"reseller_profiles", "reseller_domains", "reseller_site_configs", "reseller_product_settings",
		"reseller_order_snapshots", "reseller_ledger_entries", "reseller_withdraw_requests",
		"reseller_balance_accounts", "reseller_related_accounts",
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
	for _, constraint := range []string{"fk_cart_items_product", "fk_cart_items_sku"} {
		if !db.Migrator().HasConstraint("cart_items", constraint) {
			t.Errorf("central AutoMigrate did not create cart constraint %s", constraint)
		}
	}
	if !db.Migrator().HasConstraint(&procurementdomain.Order{}, "fk_procurement_orders_local_order") {
		t.Error("central AutoMigrate did not create procurement constraint")
	}
	if db.Migrator().HasConstraint(&procurementdomain.Order{}, "fk_procurement_orders_local_order_reference") {
		t.Error("central AutoMigrate retained superseded procurement constraint")
	}
}

// TestSQLiteProductionMigrationFreshAndRepeat uses the public production entry.
// t owns the file; reopening must preserve the completed ledger exactly.
func TestSQLiteProductionMigrationFreshAndRepeat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "production-fresh.sqlite")
	previous := gormdb.DB
	t.Cleanup(func() { gormdb.DB = previous })
	openDatabase := func() *gorm.DB {
		db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		pool, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() {
			if err := pool.Close(); err != nil {
				t.Error(err)
			}
		})
		return db
	}
	gormdb.DB = openDatabase()
	if err := migrations.AutoMigrate(); err != nil {
		t.Fatal(err)
	}
	var before []struct {
		Version     int64
		CompletedAt string
	}
	if err := gormdb.DB.Table("schema_migrations").Find(&before).Error; err != nil || len(before) != 1 || before[0].Version != 48 {
		t.Fatalf("fresh ledger=%v err=%v", before, err)
	}
	gormdb.DB = openDatabase()
	if err := migrations.AutoMigrate(); err != nil {
		t.Fatal(err)
	}
	var after []struct {
		Version     int64
		CompletedAt string
	}
	if err := gormdb.DB.Table("schema_migrations").Find(&after).Error; err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("repeat ledger=%v want=%v err=%v", after, before, err)
	}
}

// TestPayloadRetentionFreshBaselineDefaults verifies public baseline retention defaults.
// t owns synthetic rows; absent retention fields must not preserve payloads indefinitely.
func TestPayloadRetentionFreshBaselineDefaults(t *testing.T) {
	db := openMigrationDB(t)
	if err := migrations.AutoMigrate(); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO telegram_bot_inbox (bot_id,update_id,payload_cipher,status) VALUES (1,1,'cipher','done')").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO telegram_bot_deliveries (bot_id,`key`,payload_cipher,status) VALUES (1,'key','cipher','unknown')").Error; err != nil {
		t.Fatal(err)
	}
	var inbox runtimedomain.Inbox
	var delivery runtimedomain.Delivery
	if err := db.First(&inbox).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&delivery).Error; err != nil {
		t.Fatal(err)
	}
	if inbox.PayloadHeld || inbox.PayloadRetainUntil != nil || delivery.PayloadHeld || delivery.PayloadRetainUntil != nil || delivery.TargetType != "shop_private" {
		t.Fatal("unsafe defaults")
	}
}

// quoteMigrationIdentifier returns the escaped SQL identifier for catalog name.
func quoteMigrationIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
