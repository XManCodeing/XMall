package gormdb

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	mysqldriver "github.com/go-sql-driver/mysql"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/migrator"
	"gorm.io/gorm/schema"
)

type migrationSQLLog struct {
	logger.Interface
	statements []string
}

func (l *migrationSQLLog) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	query, _ := fc()
	l.statements = append(l.statements, query)
}

// The real MySQL dialector/migrator builds every DDL statement. SQLite is only
// used to answer SELECT DATABASE() for the cached migration-session variant;
// DryRun prevents MySQL DDL execution and no network connection is opened.
func mysqlIndexTestDB(t *testing.T) (*gorm.DB, *migrationSQLLog) {
	t.Helper()
	backing, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := backing.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	logs := &migrationSQLLog{Interface: logger.Discard}
	db, err := gorm.Open(mysqlDialector{Dialector: &mysql.Dialector{Config: &mysql.Config{
		Conn: &migrationMetadataPool{ConnPool: pool}, SkipInitializeWithVersion: true, DefaultStringSize: 255,
	}}}, &gorm.Config{DisableAutomaticPing: true, Logger: logs})
	if err != nil {
		t.Fatal(err)
	}
	return db, logs
}

func TestMySQLMigratorPreservesExtensionMethods(t *testing.T) {
	// Cover all exported driver methods, not just BuildIndexOptionsInterface,
	// so upgrades cannot silently narrow the wrapper's method set.
	for _, pair := range [][2]reflect.Type{
		{reflect.TypeOf(mysql.Migrator{}), reflect.TypeOf(mysqlMigrator{})},
		{reflect.TypeOf(&mysql.Dialector{}), reflect.TypeOf(mysqlDialector{})},
	} {
		for i := 0; i < pair[0].NumMethod(); i++ {
			original := pair[0].Method(i)
			wrapped, ok := pair[1].MethodByName(original.Name)
			if !ok {
				t.Errorf("%s loses %s", pair[1], original.Name)
				continue
			}
			// Ignore receiver types, but check full argument/return signatures.
			a, b := original.Type, wrapped.Type
			if a.NumIn() != b.NumIn() || a.NumOut() != b.NumOut() || a.IsVariadic() != b.IsVariadic() {
				t.Errorf("%s changes %s signature", pair[1], original.Name)
				continue
			}
			for j := 1; j < a.NumIn(); j++ {
				if a.In(j) != b.In(j) {
					t.Errorf("%s changes %s argument %d", pair[1], original.Name, j)
				}
			}
			for j := 0; j < a.NumOut(); j++ {
				if a.Out(j) != b.Out(j) {
					t.Errorf("%s changes %s return %d", pair[1], original.Name, j)
				}
			}
		}
	}
}

// Keep platform regression fixtures local: no business-domain dependencies.
type mysqlIndexOptionsModel struct {
	ID     uint   `gorm:"primaryKey"`
	Tenant uint   `gorm:"uniqueIndex:idx_tenant_name,priority:1"`
	Name   string `gorm:"size:128;uniqueIndex:idx_tenant_name,priority:2,length:32,sort:desc"`
	Status string `gorm:"size:24;index:idx_status"`
}

func TestMySQLMigratorRealIndexSQL(t *testing.T) {
	for _, cached := range []bool{false, true} {
		name := "business session"
		if cached {
			name = "cached migration session"
		}
		t.Run(name, func(t *testing.T) {
			db, logs := mysqlIndexTestDB(t)
			if cached {
				var err error
				db, err = WithMigrationDatabase(db)
				if err != nil {
					t.Fatal(err)
				}
			}
			db = db.Session(&gorm.Session{DryRun: true, NewDB: true})
			logs.statements = nil
			m := db.Session(&gorm.Session{}).Migrator()
			if err := m.CreateTable(&mysqlIndexOptionsModel{}); err != nil {
				t.Fatal(err)
			}
			if err := m.CreateIndex(&mysqlIndexOptionsModel{}, "idx_tenant_name"); err != nil {
				t.Fatal(err)
			}
			if err := m.CreateIndex(&mysqlIndexOptionsModel{}, "idx_status"); err != nil {
				t.Fatal(err)
			}
			if len(logs.statements) != 3 {
				t.Fatalf("unexpected SQL: %v", logs.statements)
			}
			if !strings.HasPrefix(logs.statements[0], "CREATE TABLE `mysql_index_options_models`") ||
				!strings.Contains(logs.statements[0], "UNIQUE INDEX `idx_tenant_name` (`tenant`,`name`(32) desc)") ||
				!strings.Contains(logs.statements[0], "INDEX `idx_status` (`status`)") {
				t.Fatalf("inline index options lost: %s", logs.statements[0])
			}
			for i, want := range []string{
				"CREATE UNIQUE INDEX `idx_tenant_name` ON `mysql_index_options_models`(`tenant`,`name`(32) desc)",
				"CREATE INDEX `idx_status` ON `mysql_index_options_models`(`status`)",
			} {
				if logs.statements[i+1] != want {
					t.Fatalf("index SQL = %q, want %q", logs.statements[i+1], want)
				}
			}
		})
	}
}

type migrationMetadataPool struct {
	gorm.ConnPool
	queries       []string
	failDatabase  bool
	emptyDatabase bool
}

func (p *migrationMetadataPool) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	p.queries = append(p.queries, query)
	if query == "SELECT DATABASE()" {
		if p.failDatabase {
			return nil, errors.New("injected metadata failure")
		}
		if p.emptyDatabase {
			return p.ConnPool.QueryContext(ctx, "SELECT NULL")
		}
		return p.ConnPool.QueryContext(ctx, "SELECT 'shop_exact'")
	}
	if strings.Contains(strings.ToUpper(query), "INFORMATION_SCHEMA.SCHEMATA") {
		return p.ConnPool.QueryContext(ctx, "SELECT 'shop_exact'")
	}
	return nil, fmt.Errorf("unexpected query: %s", query)
}

func (p *migrationMetadataPool) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	p.queries = append(p.queries, query)
	if query == "SELECT DATABASE()" {
		return p.ConnPool.QueryRowContext(ctx, "SELECT 'shop_exact'")
	}
	return p.ConnPool.QueryRowContext(ctx, "SELECT 1")
}

func TestMigrationDatabaseResolvedOnceAndSessionIsolated(t *testing.T) {
	if _, err := WithMigrationDatabase(nil); err == nil {
		t.Fatal("nil migration database accepted")
	}
	backing, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := backing.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	recorder := &migrationMetadataPool{ConnPool: pool}
	dialect := mysqlDialector{Dialector: &mysql.Dialector{Config: &mysql.Config{
		Conn: recorder, SkipInitializeWithVersion: true,
	}}}
	db, err := gorm.Open(dialect, &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	local, err := WithMigrationDatabase(db)
	if err != nil {
		t.Fatal(err)
	}
	if db.Dialector.(mysqlDialector).migrationDatabase != "" || local.Statement.ConnPool != db.Statement.ConnPool {
		t.Fatal("cache modified business dialector or lost the locked connection")
	}
	for range 10 {
		m := local.Session(&gorm.Session{}).Migrator()
		if m.CurrentDatabase() != "shop_exact" {
			t.Fatal("wrong selected database")
		}
		if !m.HasTable("products") || !m.HasColumn("products", "id") || !m.HasIndex("products", "idx_products_slug") || !m.HasConstraint("products", "fk_products_category") {
			t.Fatal("metadata delegation failed")
		}
	}
	if len(recorder.queries) != 41 {
		t.Fatalf("want 1 schema resolution + 40 metadata checks, got %d: %v", len(recorder.queries), recorder.queries)
	}
	for _, query := range recorder.queries[1:] {
		if strings.Contains(strings.ToUpper(query), "SCHEMATA") || query == "SELECT DATABASE()" {
			t.Fatalf("repeated schema resolution: %s", query)
		}
	}
	// Compare against the actual original GORM delegation, not a timing estimate.
	recorder.queries = nil
	for range 10 {
		m := db.Migrator()
		if !m.HasTable("products") || !m.HasColumn("products", "id") || !m.HasIndex("products", "idx_products_slug") || !m.HasConstraint("products", "fk_products_category") {
			t.Fatal("original metadata delegation failed")
		}
	}
	if len(recorder.queries) != 120 {
		t.Fatalf("expected original 120 queries, got %d: %v", len(recorder.queries), recorder.queries)
	}
	recorder.failDatabase = true
	if _, err := WithMigrationDatabase(db); err == nil {
		t.Fatal("schema resolution failure was swallowed")
	}
	if db.Dialector.(mysqlDialector).migrationDatabase != "" {
		t.Fatal("failed lookup poisoned business session")
	}
	recorder.failDatabase = false
	recorder.emptyDatabase = true
	if _, err := WithMigrationDatabase(db); err == nil {
		t.Fatal("missing selected database accepted")
	}
	unchanged, err := WithMigrationDatabase(backing)
	if err != nil || unchanged != backing {
		t.Fatal("non-MySQL connection changed")
	}
}

func TestMySQLMigrateColumnDecimalDefault(t *testing.T) {
	for _, tc := range []struct {
		name, dataType, modelDefault, dbDefault string
		hasDefault, validDefault, normalize     bool
	}{
		{"zero", "decimal", "0", "0.00", true, true, true},
		{"numeric alias", "NUMERIC", "-1.2", "-1.2000", true, true, true},
		{"already equal", "decimal", "0.00", "0.00", true, true, true},
		{"large exact value", "decimal", "123456789012345678901234567890.12", "123456789012345678901234567890.1200", true, true, true},
		{"sub float precision change", "decimal", "12345678901234567890.12", "12345678901234567890.13", true, true, false},
		{"changed", "decimal", "1", "0.00", true, true, false},
		{"missing model default", "decimal", "0", "0.00", false, true, false},
		{"missing database default", "decimal", "0", "0.00", true, false, false},
		{"null", "decimal", "NULL", "0.00", true, true, false},
		{"expression", "decimal", "(1 + 1)", "2.00", true, true, false},
		{"database expression", "decimal", "2", "(1 + 1)", true, true, false},
		{"non decimal", "varchar", "0", "0.00", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			field := &schema.Field{
				DBName: "amount", DataType: "decimal(38,2)", GORMDataType: schema.Float,
				DefaultValue: tc.modelDefault, DefaultValueInterface: 0,
				HasDefaultValue: tc.hasDefault, NotNull: true, Precision: 38, Scale: 2,
				TagSettings: map[string]string{"DEFAULT": tc.modelDefault},
			}
			original := *field
			column := migrator.ColumnType{
				DataTypeValue:     sql.NullString{String: tc.dataType, Valid: true},
				DefaultValueValue: sql.NullString{String: tc.dbDefault, Valid: tc.validDefault},
			}
			normalized := normalizeMySQLColumnDefault(field, column)
			if !reflect.DeepEqual(*field, original) {
				t.Fatal("shared schema field was mutated")
			}
			expected := original
			if tc.normalize {
				expected.DefaultValue = tc.dbDefault
				expected.GORMDataType = schema.String
				if normalized == field {
					t.Fatal("expected a private field copy")
				}
			} else if normalized != field {
				t.Fatal("unexpected field replacement")
			}
			if !reflect.DeepEqual(*normalized, expected) {
				t.Fatalf("unexpected delegated field: %+v", normalized)
			}
		})
	}
}

// This fake replaces metadata/DDL only, leaving real MySQL and GORM
// AutoMigrate/MigrateColumn delegation in place. No database is contacted.
type offlineMySQLDialector struct {
	mysqlDialector
	column   gorm.ColumnType
	alters   *int
	alterErr error
}

func (d offlineMySQLDialector) Migrator(db *gorm.DB) gorm.Migrator {
	return offlineMySQLMigrator{
		mysqlMigrator: d.mysqlDialector.Migrator(db).(mysqlMigrator), column: d.column, alters: d.alters, alterErr: d.alterErr,
	}
}

type offlineMySQLMigrator struct {
	mysqlMigrator
	column   gorm.ColumnType
	alters   *int
	alterErr error
}

func (m offlineMySQLMigrator) HasTable(interface{}) bool { return true }
func (m offlineMySQLMigrator) ColumnTypes(interface{}) ([]gorm.ColumnType, error) {
	return []gorm.ColumnType{m.column}, nil
}
func (m offlineMySQLMigrator) AlterColumn(interface{}, string) error {
	*m.alters++
	return m.alterErr
}

func TestMySQLAutoMigratePreservesRealChanges(t *testing.T) {
	for _, tc := range []struct {
		name       string
		model      interface{}
		wantAlters int
	}{
		{"repeat unchanged", &struct {
			Amount float64 `gorm:"type:decimal(12,2);default:0;not null"`
		}{}, 0},
		{"different default", &struct {
			Amount float64 `gorm:"type:decimal(12,2);default:1;not null"`
		}{}, 2},
		{"different precision", &struct {
			Amount float64 `gorm:"type:decimal(14,2);default:0;not null"`
		}{}, 2},
		{"different scale", &struct {
			Amount float64 `gorm:"type:decimal(12,4);default:0;not null"`
		}{}, 2},
		{"nullable", &struct {
			Amount float64 `gorm:"type:decimal(12,2);default:0"`
		}{}, 2},
		{"drop default", &struct {
			Amount float64 `gorm:"type:decimal(12,2);not null"`
		}{}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			alters := 0
			column := migrator.ColumnType{
				NameValue:         sql.NullString{String: "amount", Valid: true},
				DataTypeValue:     sql.NullString{String: "decimal", Valid: true},
				DefaultValueValue: sql.NullString{String: "0.00", Valid: true},
				DecimalSizeValue:  sql.NullInt64{Int64: 12, Valid: true},
				ScaleValue:        sql.NullInt64{Int64: 2, Valid: true},
				LengthValue:       sql.NullInt64{Valid: true},
				NullableValue:     sql.NullBool{Valid: true},
			}
			d := offlineMySQLDialector{
				mysqlDialector: mysqlDialector{Dialector: &mysql.Dialector{Config: &mysql.Config{
					DSN: "unused:unused@tcp(127.0.0.1:1)/unused", SkipInitializeWithVersion: true,
				}}},
				column: column, alters: &alters,
			}
			db, err := gorm.Open(d, &gorm.Config{DisableAutomaticPing: true})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = sqlDB.Close() })
			for range 2 {
				if err := db.Table("decimal_migration_test").AutoMigrate(tc.model); err != nil {
					t.Fatal(err)
				}
			}
			if alters != tc.wantAlters {
				t.Fatalf("AlterColumn calls = %d, want %d", alters, tc.wantAlters)
			}
			if tc.wantAlters > 0 {
				// Keep error propagation covered through the real MySQL/GORM
				// MigrateColumn chain, not an interface-only migrator stub.
				wantErr := errors.New("underlying alteration failed")
				d.alterErr = wantErr
				db.Config.Dialector = d
				if err := db.Table("decimal_migration_test").AutoMigrate(tc.model); !errors.Is(err, wantErr) {
					t.Fatalf("migration error was lost: %v", err)
				}
			}
		})
	}
}

func TestMySQLStringStorageSupportsIndexesAndLargePayloads(t *testing.T) {
	dialect := mysql.New(mysql.Config{DefaultStringSize: 255, SkipInitializeWithVersion: true})
	if got := dialect.DataTypeOf(&schema.Field{DataType: schema.String, TagSettings: map[string]string{"UNIQUEINDEX": ""}}); got != "varchar(255)" {
		t.Fatalf("unbounded indexed string: %s", got)
	}
	large := &schema.Field{DataType: schema.String, Size: 16777215}
	if got := dialect.DataTypeOf(large); got != "mediumtext" {
		t.Fatalf("MySQL payload type: %s", got)
	}
	if got := postgres.New(postgres.Config{}).DataTypeOf(large); got != "text" {
		t.Fatalf("PostgreSQL payload type changed: %s", got)
	}
}

func TestNormalizeMySQLDSN(t *testing.T) {
	dsn, err := normalizeMySQLDSN("app:secret@tcp(127.0.0.1:3306)/shop?parseTime=false&loc=Local&charset=latin1&clientFoundRows=true&tls=preferred")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ParseTime || cfg.Loc != time.UTC || cfg.ClientFoundRows {
		t.Fatal("unsafe timestamp or affected-row defaults")
	}
	if !strings.Contains(dsn, "charset=utf8mb4") || strings.Contains(dsn, "latin1") || cfg.Params["time_zone"] != "'+00:00'" {
		t.Fatalf("unexpected session params: %v", cfg.Params)
	}
	if strings.Count(dsn, "charset=") != 1 || cfg.Collation != "utf8mb4_bin" {
		t.Fatal("charset override must be unique and case-sensitive")
	}
	if cfg.TLSConfig != "preferred" || cfg.DBName != "shop" || cfg.User != "app" || cfg.Passwd != "secret" {
		t.Fatal("connection configuration was not preserved")
	}
}

func TestNormalizeMySQLDSNRequiresDatabaseAndRedactsErrors(t *testing.T) {
	for _, dsn := range []string{"app:private-password@tcp(localhost:3306)/", "private-password"} {
		_, err := normalizeMySQLDSN(dsn)
		if err == nil {
			t.Fatal("expected invalid DSN rejection")
		}
		if strings.Contains(err.Error(), "private-password") {
			t.Fatal("credentials leaked")
		}
	}
}

type gormLogRecorder struct {
	bytes.Buffer
}

func (recorder *gormLogRecorder) Printf(format string, args ...interface{}) {
	_, _ = fmt.Fprintf(&recorder.Buffer, format, args...)
}

func TestReleaseGORMLoggerDoesNotExposeQueryParameters(t *testing.T) {
	recorder := &gormLogRecorder{}
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: newGORMLogger(" release ", recorder),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	secret := "must-not-appear-in-production-database-logs"
	if err := db.Exec("INSERT INTO missing_table (secret) VALUES (?)", secret).Error; err == nil {
		t.Fatal("expected missing-table error")
	}

	output := recorder.String()
	if output == "" {
		t.Fatal("expected database error to be logged")
	}
	if strings.Contains(output, secret) {
		t.Fatalf("release database log exposed a query parameter: %s", output)
	}
	if !strings.Contains(output, "missing_table") {
		t.Fatalf("release database log should retain useful query context: %s", output)
	}
}
