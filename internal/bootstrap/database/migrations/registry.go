package migrations

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"github.com/homles698698-lab/XMall/internal/constants"
	affiliatedomain "github.com/homles698698-lab/XMall/internal/modules/affiliate/domain"
	apicredentialdomain "github.com/homles698698-lab/XMall/internal/modules/apicredential/domain"
	auditlogdomain "github.com/homles698698-lab/XMall/internal/modules/auditlog/domain"
	cardsecretdomain "github.com/homles698698-lab/XMall/internal/modules/cardsecret/domain"
	cartdomain "github.com/homles698698-lab/XMall/internal/modules/cart/domain"
	categorydomain "github.com/homles698698-lab/XMall/internal/modules/catalog/category/domain"
	mappingdomain "github.com/homles698698-lab/XMall/internal/modules/catalog/mapping/domain"
	productdomain "github.com/homles698698-lab/XMall/internal/modules/catalog/product/domain"
	channelclientdomain "github.com/homles698698-lab/XMall/internal/modules/channelclient/domain"
	contentdomain "github.com/homles698698-lab/XMall/internal/modules/content/domain"
	coupondomain "github.com/homles698698-lab/XMall/internal/modules/coupon/domain"
	downstreamcallbackdomain "github.com/homles698698-lab/XMall/internal/modules/downstreamcallback/domain"
	fulfillmentdomain "github.com/homles698698-lab/XMall/internal/modules/fulfillment/domain"
	giftcarddomain "github.com/homles698698-lab/XMall/internal/modules/giftcard/domain"
	admindomain "github.com/homles698698-lab/XMall/internal/modules/identity/admin/domain"
	emailverificationdomain "github.com/homles698698-lab/XMall/internal/modules/identity/emailverification/domain"
	externalidentitydomain "github.com/homles698698-lab/XMall/internal/modules/identity/externalidentity/domain"
	invitecodedomain "github.com/homles698698-lab/XMall/internal/modules/identity/invitecode/domain"
	userdomain "github.com/homles698698-lab/XMall/internal/modules/identity/user/domain"
	memberleveldomain "github.com/homles698698-lab/XMall/internal/modules/memberlevel/domain"
	notificationdomain "github.com/homles698698-lab/XMall/internal/modules/notification/domain"
	orderdomain "github.com/homles698698-lab/XMall/internal/modules/order/domain"
	orderriskcontract "github.com/homles698698-lab/XMall/internal/modules/orderrisk/contract"
	orderriskdomain "github.com/homles698698-lab/XMall/internal/modules/orderrisk/domain"
	paymentdomain "github.com/homles698698-lab/XMall/internal/modules/payment/domain"
	procurementdomain "github.com/homles698698-lab/XMall/internal/modules/procurement/domain"
	promotiondomain "github.com/homles698698-lab/XMall/internal/modules/promotion/domain"
	reconciliationdomain "github.com/homles698698-lab/XMall/internal/modules/reconciliation/domain"
	resellerstore "github.com/homles698698-lab/XMall/internal/modules/reseller/infrastructure/gormstore"
	settingsstore "github.com/homles698698-lab/XMall/internal/modules/settings/infrastructure/gormstore"
	siteconnectiondomain "github.com/homles698698-lab/XMall/internal/modules/siteconnection/domain"
	broadcastdomain "github.com/homles698698-lab/XMall/internal/modules/telegram/broadcast/domain"
	walletdomain "github.com/homles698698-lab/XMall/internal/modules/wallet/domain"
	"github.com/homles698698-lab/XMall/internal/platform/database/gormdb"
	"log"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/migrator"
)

// schemaVersion tracks schema/data changes, not runner implementation changes.
// For vN: update the current-model baseline AND append the targeted vN step.
const schemaVersion int64 = 1
const migrationLockName = "dujiao_next_schema_migration"
const migrationAdvisoryKey int64 = 0x64756a69616f

// migrationStep upgrades version-1 to version. Steps must be ordered, contiguous,
// and retry-safe: MySQL DDL cannot be rolled back if a step or ledger write fails.
type migrationStep struct {
	version int64
	name    string
	run     func(*gorm.DB) error
}

// The baseline is the CURRENT model/data snapshot, not a frozen v1 snapshot.
// An unversioned DB runs it once and records schemaVersion, skipping historical
// steps which could conflict with current models. A versioned DB ONLY runs steps.
var schemaSteps = []migrationStep{}

func AutoMigrate() error {
	return runSchemaMigrations(gormdb.DB, schemaVersion, migrateSchema, schemaSteps...)
}

func validateMigrationSteps(target int64, baseline func(*gorm.DB) error, steps []migrationStep) error {
	if target < 1 || baseline == nil {
		return errors.New("invalid schema migration target or baseline")
	}
	if int64(len(steps)) != target-1 {
		return fmt.Errorf("schema migration steps must cover every version 2..%d", target)
	}
	for i, step := range steps {
		if step.version != int64(i)+2 || step.name == "" || step.run == nil {
			return fmt.Errorf("invalid schema migration step %d: expected version %d with name and callback", i, i+2)
		}
	}
	return nil
}

// runSchemaMigrations preserves deployed version-1 ledgers. Completion is written
// after EACH step on the lock-owning connection. SQLite's outer transaction rolls
// back the entire attempt; MySQL/Postgres retain completed steps on later failure.
func runSchemaMigrations(db *gorm.DB, target int64, baseline func(*gorm.DB) error, steps ...migrationStep) error {
	if db == nil {
		return errors.New("database is not initialized")
	}
	if err := validateMigrationSteps(target, baseline, steps); err != nil {
		return err
	}
	started := time.Now()
	log.Printf("schema migration: waiting for database lock, target=%d", target)
	db = db.Session(&gorm.Session{Logger: db.Logger.LogMode(logger.Warn)})
	return withMigrationLock(db, func(locked *gorm.DB) error {
		log.Printf("schema migration: lock acquired after %s", time.Since(started))
		if err := locked.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
			version BIGINT NOT NULL PRIMARY KEY, completed_at VARCHAR(40) NOT NULL
		)`).Error; err != nil {
			return fmt.Errorf("create schema migration ledger: %w", err)
		}
		var versions []int64
		if err := locked.Raw("SELECT version FROM schema_migrations ORDER BY version").Scan(&versions).Error; err != nil {
			return fmt.Errorf("read schema migration versions: %w", err)
		}
		var current int64
		for i, version := range versions {
			// The first row may be >1: a fresh baseline records only its target.
			if version < 1 || version > target || (i > 0 && version != current+1) {
				return fmt.Errorf("unknown or noncontiguous schema migration version %d (target %d)", version, target)
			}
			current = version
		}
		if current == target {
			log.Printf("schema migration: version=%d already complete, skipped (%s)", target, time.Since(started))
			return nil
		}
		apply := func(version int64, name string, run func(*gorm.DB) error) error {
			stepStarted := time.Now()
			log.Printf("schema migration: version=%d step=%s started", version, name)
			if err := run(locked); err != nil {
				return fmt.Errorf("schema migration version %d (%s): %w", version, name, err)
			}
			if err := validateMigrationForeignKeys(locked); err != nil {
				return err
			}
			if err := locked.Exec("INSERT INTO schema_migrations (version, completed_at) VALUES (?, ?)", version, time.Now().UTC().Format(time.RFC3339Nano)).Error; err != nil {
				return fmt.Errorf("record schema migration version %d: %w", version, err)
			}
			log.Printf("schema migration: version=%d step=%s completed (%s)", version, name, time.Since(stepStarted))
			return nil
		}
		if current == 0 {
			return apply(target, "current snapshot baseline", baseline)
		}
		for _, step := range steps {
			if step.version > current {
				if err := apply(step.version, step.name, step.run); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func validateMigrationForeignKeys(db *gorm.DB) error {
	if db.Dialector.Name() != "sqlite" {
		return nil
	}
	// Rebuilds need foreign_keys OFF before BEGIN; validate before recording each
	// completion. Never scan data on the already-complete startup fast path.
	rows, err := db.Raw("PRAGMA foreign_key_check").Rows()
	if err != nil {
		return err
	}
	invalid := rows.Next()
	checkErr := rows.Err()
	closeErr := rows.Close()
	if invalid {
		return errors.New("schema migration foreign key validation failed")
	}
	return errors.Join(checkErr, closeErr)
}

// sqliteMigrationTx makes GORM's existing internal Transaction calls use
// savepoints inside BEGIN IMMEDIATE, never BEGIN or a different pooled connection.
type sqliteMigrationTx struct{ gorm.ConnPool }

func (tx sqliteMigrationTx) Commit() error {
	_, err := tx.ExecContext(context.Background(), "COMMIT")
	return err
}
func (tx sqliteMigrationTx) Rollback() error {
	_, err := tx.ExecContext(context.Background(), "ROLLBACK")
	return err
}

func withMigrationLock(db *gorm.DB, run func(*gorm.DB) error) (err error) {
	pool, err := db.DB()
	if err != nil {
		return err
	}
	conn, err := pool.Conn(db.Statement.Context)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Every statement, including nested transactions, uses the lock-owning
	// physical connection. Losing it cannot transparently resume on another one.
	locked := db.Session(&gorm.Session{NewDB: true, Context: db.Statement.Context})
	locked.Statement.ConnPool = conn
	lockCtx, cancel := context.WithTimeout(db.Statement.Context, 5*time.Minute)
	defer cancel()
	switch db.Dialector.Name() {
	case "mysql", "postgres":
		for {
			var acquired bool
			if db.Dialector.Name() == "mysql" {
				var result sql.NullInt64
				err = conn.QueryRowContext(lockCtx, "SELECT GET_LOCK(?, 0)", migrationLockName).Scan(&result)
				if err == nil && !result.Valid {
					err = errors.New("GET_LOCK returned NULL")
				}
				acquired = result.Valid && result.Int64 == 1
			} else {
				err = conn.QueryRowContext(lockCtx, "SELECT pg_try_advisory_lock($1)", migrationAdvisoryKey).Scan(&acquired)
			}
			if err != nil {
				// A lost response can hide a successfully acquired session lock.
				// Discard the physical connection rather than pooling an owner.
				_ = conn.Raw(func(interface{}) error { return driver.ErrBadConn })
				return fmt.Errorf("acquire schema migration lock: %w", err)
			}
			if acquired {
				break
			}
			select {
			case <-lockCtx.Done():
				return fmt.Errorf("wait for schema migration lock: %w", lockCtx.Err())
			case <-time.After(100 * time.Millisecond):
			}
		}
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var released bool
			var releaseErr error
			if db.Dialector.Name() == "mysql" {
				releaseErr = conn.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", migrationLockName).Scan(&released)
			} else {
				releaseErr = conn.QueryRowContext(ctx, "SELECT pg_advisory_unlock($1)", migrationAdvisoryKey).Scan(&released)
			}
			if releaseErr == nil && !released {
				releaseErr = errors.New("schema migration lock was not owned")
			}
			if releaseErr != nil {
				// Never return a session that might still own a lock to the pool.
				_ = conn.Raw(func(interface{}) error { return driver.ErrBadConn })
				err = errors.Join(err, fmt.Errorf("release schema migration lock: %w", releaseErr))
			}
		}()
		return run(locked)
	case "sqlite":
		var foreignKeys, busyTimeout int
		if err = conn.QueryRowContext(lockCtx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
			return err
		}
		if err = conn.QueryRowContext(lockCtx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
			return err
		}
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, fkErr := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA foreign_keys = %d", foreignKeys))
			_, busyErr := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", busyTimeout))
			if restoreErr := errors.Join(fkErr, busyErr); restoreErr != nil {
				_ = conn.Raw(func(interface{}) error { return driver.ErrBadConn })
				err = errors.Join(err, restoreErr)
			}
		}()
		if _, err = conn.ExecContext(lockCtx, "PRAGMA foreign_keys = OFF"); err != nil {
			return err
		}
		if _, err = conn.ExecContext(lockCtx, "PRAGMA busy_timeout = 300000"); err != nil {
			return err
		}
		if _, err = conn.ExecContext(lockCtx, "BEGIN IMMEDIATE"); err != nil {
			// An interrupted BEGIN can leave its outcome unknown.
			_ = conn.Raw(func(interface{}) error { return driver.ErrBadConn })
			return fmt.Errorf("acquire sqlite migration write lock: %w", err)
		}
		tx := sqliteMigrationTx{ConnPool: conn}
		committed := false
		defer func() {
			if !committed {
				if rollbackErr := tx.Rollback(); rollbackErr != nil {
					_ = conn.Raw(func(interface{}) error { return driver.ErrBadConn })
					err = errors.Join(err, rollbackErr)
				}
			}
		}()
		locked = locked.Session(&gorm.Session{SkipDefaultTransaction: true})
		locked.Statement.ConnPool = tx
		if err = run(locked); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	default:
		return fmt.Errorf("unsupported schema migration dialect %q", db.Dialector.Name())
	}
}

// Order the entire registry before splitting calls: independently migrating the
// original list would lose GORM's cross-model dependency ordering. Models here
// have no creation-time associations; AutoMigrate still handles future ones.
func migrateModelsWithProgress(db *gorm.DB, models ...interface{}) error {
	ordered := (migrator.Migrator{Config: migrator.Config{DB: db}}).ReorderModels(models, true)
	for i, model := range ordered {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(model); err != nil {
			return err
		}
		started := time.Now()
		log.Printf("schema migration: table=%s progress=%d/%d started", stmt.Schema.Table, i+1, len(ordered))
		if err := db.AutoMigrate(model); err != nil {
			return fmt.Errorf("auto migrate table %s: %w", stmt.Schema.Table, err)
		}
		log.Printf("schema migration: table=%s progress=%d/%d completed (%s)", stmt.Schema.Table, i+1, len(ordered), time.Since(started))
	}
	return nil
}

func migrateSchema(db *gorm.DB) error {
	var err error
	db, err = gormdb.WithMigrationDatabase(db)
	if err != nil {
		return err
	}
	log.Printf("schema migration: stage=preflight started")
	if err := validateMappingExternalIDLengths(db); err != nil {
		return err
	}
	if err := validateProcurementLocalUniqueness(db); err != nil {
		return err
	}
	log.Printf("schema migration: stage=auto migrate models started")
	if err := migrateModelsWithProgress(db,
		&admindomain.Admin{},
		&userdomain.User{},
		&externalidentitydomain.Identity{},
		&invitecodedomain.InviteCode{},
		&affiliatedomain.Profile{},
		&affiliatedomain.Click{},
		&affiliatedomain.Commission{},
		&affiliatedomain.WithdrawRequest{},
		&walletdomain.Account{},
		&walletdomain.Transaction{},
		&walletdomain.RechargeOrder{},
		&auditlogdomain.UserLoginLog{},
		&auditlogdomain.AuthzAuditLog{},
		&notificationdomain.NotificationLog{},
		&auditlogdomain.AdminLoginLog{},
		&emailverificationdomain.Code{},
		&orderdomain.Order{},
		&orderdomain.OrderItem{},
		&orderdomain.OrderRefundRecord{},
		&orderriskdomain.LockKey{},
		&cartdomain.Item{},
		&paymentdomain.PaymentChannel{},
		&paymentdomain.Payment{},
		&cardsecretdomain.Secret{},
		&cardsecretdomain.Batch{},
		&giftcarddomain.GiftCard{},
		&giftcarddomain.GiftCardBatch{},
		&fulfillmentdomain.Fulfillment{},
		&coupondomain.Coupon{},
		&coupondomain.CouponUsage{},
		&promotiondomain.Promotion{},
		&categorydomain.Category{},
		&productdomain.Product{},
		&productdomain.ProductSKU{},
		&contentdomain.Post{},
		&contentdomain.PostProduct{},
		&contentdomain.PostCategory{},
		&contentdomain.Banner{},
		&settingsstore.SettingRecord{},
		&apicredentialdomain.ApiCredential{},
		&siteconnectiondomain.Connection{},
		&mappingdomain.Mapping{},
		&mappingdomain.SKUMapping{},
		&procurementdomain.Order{},
		&downstreamcallbackdomain.OrderRef{},
		&reconciliationdomain.Job{},
		&reconciliationdomain.Item{},
		&channelclientdomain.Client{},
		&broadcastdomain.Broadcast{},
		&memberleveldomain.MemberLevel{},
		&memberleveldomain.MemberLevelPrice{},
		&contentdomain.Media{},
	); err != nil {
		return err
	}

	stages := []struct {
		name string
		run  func(...*gorm.DB) error
	}{
		{"oauth identity unique index", ensureUserOAuthIdentityUserProviderUniqueIndex},
		{"pending order risk IPs", func(dbs ...*gorm.DB) error { return backfillPendingOrderRiskIPs(dbs[0]) }},
		{"reseller schema", func(dbs ...*gorm.DB) error { return resellerstore.Migrate(dbs[0]) }},
		{"cart SKU unique index", migrateCartSKUUniqueIndex},
		{"product SKU backfill", ensureProductSKUMigration},
		{"manual remaining stock", ensureManualStockRemainingMigration},
		{"category parent", ensureCategoryParentMigration},
		{"payment provider rename", ensurePaymentProviderBepusdtRenameMigration},
		{"payment channel config", ensurePaymentChannelBepusdtConfigMigration},
		{"payment fee policy", ensurePaymentFeePolicyMigration},
		{"order refund fee", ensureOrderRefundPaymentFeeMigration},
		{"order item original price", ensureOrderItemOriginalPriceMigration},
		{"cart foreign keys", ensureCartForeignKeyConstraints},
		{"procurement foreign key", ensureProcurementOrderForeignKeyConstraint},
	}
	for _, stage := range stages {
		started := time.Now()
		log.Printf("schema migration: stage=%s started", stage.name)
		if err := stage.run(db); err != nil {
			return fmt.Errorf("%s: %w", stage.name, err)
		}
		log.Printf("schema migration: stage=%s completed (%s)", stage.name, time.Since(started))
	}
	if db.Migrator().HasColumn(&productdomain.Product{}, "price_currency") {
		if err := db.Migrator().DropColumn(&productdomain.Product{}, "price_currency"); err != nil {
			return err
		}
	}
	return nil
}

// backfillPendingOrderRiskIPs 只迁移仍可能占用库存的在途订单，避免启动时扫描全部历史订单。
func backfillPendingOrderRiskIPs(db *gorm.DB) error {
	type orderIPRow struct {
		ID       uint
		ClientIP string
	}
	var rows []orderIPRow
	if err := db.Model(&orderdomain.Order{}).
		Select("id", "client_ip").
		Where("deleted_at IS NULL AND status = ? AND (risk_ip IS NULL OR risk_ip = '') AND client_ip <> ''", constants.OrderStatusPendingPayment).
		Find(&rows).Error; err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		for _, row := range rows {
			riskIP := orderriskcontract.NormalizeRiskIP(row.ClientIP)
			if riskIP == "" {
				continue
			}
			if err := tx.Model(&orderdomain.Order{}).
				Where("id = ? AND (risk_ip IS NULL OR risk_ip = '')", row.ID).
				UpdateColumn("risk_ip", riskIP).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
