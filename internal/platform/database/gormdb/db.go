package gormdb

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/glebarez/sqlite" // 纯 Go SQLite 驱动（基于 modernc.org/sqlite）
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/shopspring/decimal"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	"gorm.io/gorm/migrator"
	"gorm.io/gorm/schema"
)

// Embed the concrete dialector to preserve its optional GORM interfaces too.
// AutoMigrate calls execTx.Migrator().MigrateColumn, so wrapping the dialector
// (rather than one returned migrator) also applies this fix to cloned sessions.
type mysqlDialector struct {
	*mysql.Dialector
	// Set only on a private migration session, never on the business pool.
	migrationDatabase string
}

func (d mysqlDialector) Migrator(db *gorm.DB) gorm.Migrator {
	return mysqlMigrator{Migrator: d.Dialector.Migrator(db).(mysql.Migrator), database: d.migrationDatabase}
}

// WithMigrationDatabase resolves the selected schema once on the lock-owning
// connection. GORM's generic table/column/index/constraint checks otherwise issue
// SELECT DATABASE() plus a SCHEMATA lookup on every call. Do not use this session
// for USE statements; the caller must keep its selected database fixed.
func WithMigrationDatabase(db *gorm.DB) (*gorm.DB, error) {
	if db == nil {
		return nil, errors.New("migration database is not initialized")
	}
	dialect, ok := db.Dialector.(mysqlDialector)
	if !ok {
		return db, nil
	}
	var name sql.NullString
	if err := db.Raw("SELECT DATABASE()").Scan(&name).Error; err != nil {
		return nil, fmt.Errorf("resolve migration database: %w", err)
	}
	if !name.Valid || name.String == "" {
		return nil, errors.New("migration connection has no selected database")
	}
	dialect.migrationDatabase = name.String
	local := db.Session(&gorm.Session{Context: db.Statement.Context})
	local.Config.Dialector = dialect
	return local, nil
}

// Embed the concrete driver migrator, not gorm.Migrator: GORM's CreateTable
// and CreateIndex assert BuildIndexOptionsInterface on cloned sessions. The
// concrete method set also preserves other driver/generic migration extensions.
type mysqlMigrator struct {
	mysql.Migrator
	database string
}

var (
	_ gorm.Migrator                       = mysqlMigrator{}
	_ migrator.BuildIndexOptionsInterface = mysqlMigrator{}
	_ gorm.ErrorTranslator                = mysqlDialector{}
	_ gorm.SavePointerDialectorInterface  = mysqlDialector{}
)

func (m mysqlMigrator) CurrentDatabase() string {
	if m.database != "" {
		return m.database
	}
	return m.Migrator.CurrentDatabase()
}

func (m mysqlMigrator) MigrateColumn(value interface{}, field *schema.Field, columnType gorm.ColumnType) error {
	return m.Migrator.MigrateColumn(value, normalizeMySQLColumnDefault(field, columnType), columnType)
}

func normalizeMySQLColumnDefault(field *schema.Field, columnType gorm.ColumnType) *schema.Field {
	if !field.IgnoreMigration && field.HasDefaultValue {
		switch strings.ToLower(columnType.DatabaseTypeName()) {
		case "decimal", "numeric":
			if dbDefault, ok := columnType.DefaultValue(); ok {
				modelNumber, modelErr := decimal.NewFromString(field.DefaultValue)
				dbNumber, dbErr := decimal.NewFromString(dbDefault)
				if modelErr == nil && dbErr == nil && modelNumber.Equal(dbNumber) {
					// Never mutate the shared schema cache. MySQL pads defaults to
					// the column's scale, while GORM compares the literal strings.
					copied := *field
					copied.DefaultValue = dbDefault
					// GORM 1.31.1's generic default branch assigns alterColumn,
					// erasing earlier precision/scale/nullability differences.
					// Its string branch only sets it on a difference. DataType and
					// DefaultValueInterface stay intact for SQL generation.
					copied.GORMDataType = schema.String
					field = &copied
				}
			}
		}
	}
	return field
}

var DB *gorm.DB

// DBPoolConfig 数据库连接池配置
type DBPoolConfig struct {
	MaxOpenConns           int
	MaxIdleConns           int
	ConnMaxLifetimeSeconds int
	ConnMaxIdleTimeSeconds int
}

// InitDB 初始化数据库连接
func InitDB(driver, dsn string, pool DBPoolConfig, mode string) error {
	var err error
	normalized := strings.ToLower(strings.TrimSpace(driver))
	var dialector gorm.Dialector
	switch normalized {
	case "", "sqlite":
		// glebarez/sqlite 是基于 modernc.org/sqlite 的纯 Go 驱动
		// 追加 PRAGMA 参数避免并发写入时 busy-spin 导致 CPU 飙升
		dialector = sqlite.Open(appendSQLitePragmas(dsn))
	case "mysql":
		normalizedDSN, err := normalizeMySQLDSN(dsn)
		if err != nil {
			return err
		}
		dialector = mysqlDialector{Dialector: &mysql.Dialector{Config: &mysql.Config{
			DSN: normalizedDSN, DefaultStringSize: 255,
		}}}
	case "postgres", "postgresql":
		dialector = postgres.Open(dsn)
	default:
		return fmt.Errorf("unsupported database driver: %s", driver)
	}
	DB, err = gorm.Open(dialector, &gorm.Config{
		Logger:  newGORMLogger(mode, nil),
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return err
	}

	sqlDB, err := DB.DB()
	if err != nil {
		return err
	}
	applyDBPool(sqlDB, pool)
	if normalized == "mysql" {
		// Identity fields must remain case-sensitive; human-facing searches use
		// explicit query semantics rather than weakening every unique index.
		// Start a fresh reusable session: Set alone returns an initialized
		// statement and would otherwise share mutable query state across calls.
		DB = DB.Set("gorm:table_options", "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin").Session(&gorm.Session{})
	}

	// SQLite 额外执行 PRAGMA 确保 WAL 模式生效
	if normalized == "" || normalized == "sqlite" {
		DB.Exec("PRAGMA journal_mode=WAL")
		DB.Exec("PRAGMA busy_timeout=5000")
		DB.Exec("PRAGMA synchronous=NORMAL")
	}
	return nil
}

// normalizeMySQLDSN keeps credentials out of validation errors and preserves
// UTC timestamps and guarded-update affected-row semantics across connections.
func normalizeMySQLDSN(dsn string) (string, error) {
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil || cfg.DBName == "" {
		return "", errors.New("invalid MySQL DSN: a pre-created database name is required")
	}
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	if cfg.Params == nil {
		cfg.Params = make(map[string]string)
	}
	if err := cfg.Apply(mysqldriver.Charset("utf8mb4", "utf8mb4_bin")); err != nil {
		return "", errors.New("cannot configure MySQL character set")
	}
	cfg.Params["time_zone"] = "'+00:00'"
	cfg.ClientFoundRows = false
	return cfg.FormatDSN(), nil
}

func newGORMLogger(_ string, writer gormlogger.Writer) gormlogger.Interface {
	// Background workers poll continuously in every server mode. Keep routine
	// queries quiet; explicit db.Debug() remains available for SQL diagnostics.
	if writer == nil {
		writer = log.New(os.Stdout, "\r\n", log.LstdFlags)
	}
	return gormlogger.New(writer, gormlogger.Config{
		SlowThreshold:             2 * time.Second,
		LogLevel:                  gormlogger.Warn,
		IgnoreRecordNotFoundError: true,
		ParameterizedQueries:      true,
		Colorful:                  false,
	})
}

// appendSQLitePragmas 在 SQLite DSN 中追加关键 PRAGMA 参数
func appendSQLitePragmas(dsn string) string {
	// glebarez/sqlite 支持 ?_pragma=key=value 形式
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep +
		"_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)"
}

func applyDBPool(sqlDB *sql.DB, pool DBPoolConfig) {
	if sqlDB == nil {
		return
	}
	if pool.MaxOpenConns > 0 {
		sqlDB.SetMaxOpenConns(pool.MaxOpenConns)
	}
	if pool.MaxIdleConns >= 0 {
		sqlDB.SetMaxIdleConns(pool.MaxIdleConns)
	}
	if pool.ConnMaxLifetimeSeconds > 0 {
		sqlDB.SetConnMaxLifetime(time.Duration(pool.ConnMaxLifetimeSeconds) * time.Second)
	}
	if pool.ConnMaxIdleTimeSeconds > 0 {
		sqlDB.SetConnMaxIdleTime(time.Duration(pool.ConnMaxIdleTimeSeconds) * time.Second)
	}
}
