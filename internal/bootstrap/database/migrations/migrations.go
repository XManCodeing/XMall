package migrations

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
	cardsecretdomain "github.com/XManCodeing/XMall/internal/modules/cardsecret/domain"
	cartdomain "github.com/XManCodeing/XMall/internal/modules/cart/domain"
	externalidentitydomain "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/domain"

	categorydomain "github.com/XManCodeing/XMall/internal/modules/catalog/category/domain"
	productdomain "github.com/XManCodeing/XMall/internal/modules/catalog/product/domain"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	paymentdomain "github.com/XManCodeing/XMall/internal/modules/payment/domain"
	procurementdomain "github.com/XManCodeing/XMall/internal/modules/procurement/domain"
	settingsstore "github.com/XManCodeing/XMall/internal/modules/settings/infrastructure/gormstore"
	"github.com/XManCodeing/XMall/internal/platform/database/gormdb"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"github.com/XManCodeing/XMall/internal/shared/money"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// migrationDB keeps standalone legacy helpers callable by tests; the runner always
// supplies its locked connection explicitly and never swaps the global business DB.
func migrationDB(databases []*gorm.DB) *gorm.DB {
	if len(databases) != 0 {
		return databases[0]
	}
	return gormdb.DB
}

const (
	manualStockRemainingMigrationSettingKey         = "migration/manual_stock_remaining_v1"
	skuMigrationSettingKey                          = "migration/product_sku_v1"
	categoryParentMigrationSettingKey               = "migration/category_parent_v1"
	paymentProviderBepusdtRenameMigrationSettingKey = "migration/payment_provider_bepusdt_rename_v1"
	paymentChannelBepusdtConfigMigrationSettingKey  = "migration/payment_channel_bepusdt_config_v2"
	paymentFeePolicyMigrationSettingKey             = "migration/payment_fee_policy_v1"
	orderRefundPaymentFeeMigrationSettingKey        = "migration/order_refund_payment_fee_v1"
	orderItemOriginalPriceMigrationKey              = "migration/order_item_original_price_v1"
	manualStockUnlimitedValue                       = -1
	cartProductForeignKeyConstraint                 = "fk_cart_items_product"
	cartSKUForeignKeyConstraint                     = "fk_cart_items_sku"
	procurementOrderForeignKeyConstraint            = "fk_procurement_orders_local_order"
	supersededProcurementOrderForeignKeyConstraint  = "fk_procurement_orders_local_order_reference"
	userOAuthIdentityUserProviderUniqueIndex        = "idx_user_oauth_identity_user_provider"
)

type userOAuthIdentityUserProviderIndexSchema struct {
	UserID   uint   `gorm:"uniqueIndex:idx_user_oauth_identity_user_provider"`
	Provider string `gorm:"uniqueIndex:idx_user_oauth_identity_user_provider"`
}

type namedIndexMigrator interface {
	HasIndex(value interface{}, name string) bool
	CreateIndex(value interface{}, name string) error
}

func (userOAuthIdentityUserProviderIndexSchema) TableName() string {
	return "user_oauth_identities"
}

type cartItemConstraintSchema struct {
	cartdomain.Item
	Product *cartProductReference    `gorm:"foreignKey:ProductID;references:ID;constraint:fk_cart_items_product"`
	SKU     *cartProductSKUReference `gorm:"foreignKey:SKUID;references:ID;constraint:fk_cart_items_sku"`
}

func (cartItemConstraintSchema) TableName() string { return "cart_items" }

type cartProductReference struct {
	ID uint `gorm:"primarykey"`
}

func (cartProductReference) TableName() string { return "products" }

type cartProductSKUReference struct {
	ID uint `gorm:"primarykey"`
}

func (cartProductSKUReference) TableName() string { return "product_skus" }

func ensureCartForeignKeyConstraints(databases ...*gorm.DB) error {
	db := migrationDB(databases)
	if db == nil {
		return errors.New("database is not initialized")
	}
	migrator := db.Migrator()
	missing := make([]struct {
		name     string
		relation string
	}, 0, 2)
	for _, constraint := range []struct {
		name     string
		relation string
	}{
		{name: cartProductForeignKeyConstraint, relation: "Product"},
		{name: cartSKUForeignKeyConstraint, relation: "SKU"},
	} {
		if migrator.HasConstraint(&cartItemConstraintSchema{}, constraint.name) {
			continue
		}
		missing = append(missing, constraint)
	}
	if len(missing) > 0 {
		if err := validateCartForeignKeyReferences(db); err != nil {
			return err
		}
		for _, constraint := range missing {
			if err := migrator.CreateConstraint(&cartItemConstraintSchema{}, constraint.relation); err != nil {
				return err
			}
		}
	}

	// SQLite 通过重建表来增加外键，重建过程不会保留原表索引。
	// 即使外键已经存在也要执行，以修复曾被旧迁移版本移除的索引。
	return db.AutoMigrate(&cartdomain.Item{})
}

func validateCartForeignKeyReferences(databases ...*gorm.DB) error {
	db := migrationDB(databases)
	for _, reference := range []struct {
		constraint string
		table      string
		column     string
	}{
		{constraint: cartProductForeignKeyConstraint, table: "products", column: "product_id"},
		{constraint: cartSKUForeignKeyConstraint, table: "product_skus", column: "sku_id"},
	} {
		var orphanCount int64
		query := fmt.Sprintf(
			"SELECT COUNT(*) FROM cart_items AS cart LEFT JOIN %s AS target ON target.id = cart.%s WHERE target.id IS NULL",
			reference.table,
			reference.column,
		)
		if err := db.Raw(query).Scan(&orphanCount).Error; err != nil {
			return err
		}
		if orphanCount > 0 {
			return fmt.Errorf(
				"cannot create %s: %d cart_items rows reference missing %s",
				reference.constraint,
				orphanCount,
				reference.table,
			)
		}
	}
	return nil
}

// validateMappingExternalIDLengths fails before narrowing historical TEXT IDs.
// Never truncate opaque upstream identifiers during a schema upgrade.
func validateMappingExternalIDLengths(db *gorm.DB) error {
	length := "CHAR_LENGTH"
	if db.Dialector.Name() == "sqlite" {
		length = "LENGTH"
	}
	for _, field := range []struct{ table, column string }{
		{"product_mappings", "upstream_product_external_id"},
		{"sku_mappings", "upstream_sku_external_id"},
	} {
		if !db.Migrator().HasTable(field.table) || !db.Migrator().HasColumn(field.table, field.column) {
			continue
		}
		var count int64
		if err := db.Table(field.table).Where(fmt.Sprintf("%s(%s) > ?", length, db.Statement.Quote(field.column)), 255).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return fmt.Errorf("cannot migrate %s.%s to varchar(255): %d oversized identifiers; manual reconciliation required, no rows changed", field.table, field.column, count)
		}
	}
	return nil
}

func validateProcurementLocalUniqueness(db *gorm.DB) error {
	if !db.Migrator().HasTable(&procurementdomain.Order{}) {
		return nil
	}
	var duplicates []struct {
		LocalOrderID uint
		Count        int64
	}
	if err := db.Table("procurement_orders").Select("local_order_id, COUNT(*) AS count").Group("local_order_id").Having("COUNT(*) > 1").Scan(&duplicates).Error; err != nil {
		return err
	}
	if len(duplicates) > 0 {
		return fmt.Errorf("cannot create procurement local-order unique index: %d duplicate local orders; manual reconciliation required, no rows deleted", len(duplicates))
	}
	return nil
}

func ensureProcurementOrderForeignKeyConstraint(databases ...*gorm.DB) error {
	db := migrationDB(databases)
	if db == nil {
		return errors.New("database is not initialized")
	}
	migrator := db.Migrator()
	if migrator.HasConstraint(&procurementdomain.Order{}, supersededProcurementOrderForeignKeyConstraint) {
		if err := migrator.DropConstraint(&procurementdomain.Order{}, supersededProcurementOrderForeignKeyConstraint); err != nil {
			return err
		}
	}
	if !migrator.HasConstraint(&procurementdomain.Order{}, procurementOrderForeignKeyConstraint) {
		if err := migrator.CreateConstraint(&procurementdomain.Order{}, "LocalOrderReference"); err != nil {
			return err
		}
	}

	// SQLite 的 DropConstraint/CreateConstraint 都可能重建表并丢失索引。
	// 无条件同步模型索引，同时让已经运行过旧迁移的数据库可以自愈。
	return db.AutoMigrate(&procurementdomain.Order{})
}

// ensureManualStockRemainingMigration 将历史“总量库存”迁移为“剩余库存”语义，仅执行一次。
func ensureManualStockRemainingMigration(databases ...*gorm.DB) error {
	db := migrationDB(databases)
	if db == nil {
		return errors.New("database is not initialized")
	}

	var marker settingsstore.SettingRecord
	if err := db.First(&marker, db.Statement.Quote("key")+" = ?", manualStockRemainingMigrationSettingKey).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	} else if migrationDone(marker.ValueJSON) {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&productdomain.Product{}).
			Where("deleted_at IS NULL AND manual_stock_total >= ?", manualStockUnlimitedValue+1).
			Update("manual_stock_total",
				gorm.Expr("CASE WHEN (manual_stock_total - manual_stock_locked - manual_stock_sold) < 0 THEN 0 ELSE (manual_stock_total - manual_stock_locked - manual_stock_sold) END")).
			Error; err != nil {
			return err
		}

		if err := tx.Model(&productdomain.ProductSKU{}).
			Where("deleted_at IS NULL AND manual_stock_total >= ?", manualStockUnlimitedValue+1).
			Update("manual_stock_total",
				gorm.Expr("CASE WHEN (manual_stock_total - manual_stock_locked - manual_stock_sold) < 0 THEN 0 ELSE (manual_stock_total - manual_stock_locked - manual_stock_sold) END")).
			Error; err != nil {
			return err
		}

		marker := settingsstore.SettingRecord{
			Key: manualStockRemainingMigrationSettingKey,
			ValueJSON: jsonmap.JSON{
				"done":        true,
				"migrated_at": time.Now().UTC().Format(time.RFC3339),
			},
		}
		return tx.Save(&marker).Error
	})
}

func migrationDone(value jsonmap.JSON) bool {
	if len(value) == 0 {
		return false
	}
	done, ok := value["done"]
	if !ok {
		return false
	}
	flag, ok := done.(bool)
	return ok && flag
}

// ensurePaymentFeePolicyMigration 为升级前的支付记录补充不可变手续费策略快照。
// 旧版本只在 fee_amount 中记录手续费，且实际向用户加收，因此非零记录明确标为 legacy。
func ensurePaymentFeePolicyMigration(databases ...*gorm.DB) error {
	db := migrationDB(databases)
	if db == nil {
		return errors.New("database is not initialized")
	}

	var marker settingsstore.SettingRecord
	if err := db.First(&marker, db.Statement.Quote("key")+" = ?", paymentFeePolicyMigrationSettingKey).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	} else if migrationDone(marker.ValueJSON) {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&paymentdomain.Payment{}).
			Where("fee_amount > 0 AND (fee_policy IS NULL OR fee_policy = '' OR fee_policy = ?)", constants.PaymentFeePolicyNone).
			Update("fee_policy", constants.PaymentFeePolicyLegacyCustomerSurcharge).Error; err != nil {
			return err
		}
		if err := tx.Model(&paymentdomain.Payment{}).
			Where("fee_amount = 0 AND (fee_policy IS NULL OR fee_policy = '')").
			Update("fee_policy", constants.PaymentFeePolicyNone).Error; err != nil {
			return err
		}

		marker := settingsstore.SettingRecord{
			Key: paymentFeePolicyMigrationSettingKey,
			ValueJSON: jsonmap.JSON{
				"done":        true,
				"migrated_at": time.Now().UTC().Format(time.RFC3339),
			},
		}
		return tx.Save(&marker).Error
	})
}

// ensureOrderRefundPaymentFeeMigration backfills manual refunds created before
// the per-refund accounting flag existed. Only merchant-absorbed fee snapshots
// are considered; legacy customer surcharges remain outside merchant costs.
func ensureOrderRefundPaymentFeeMigration(databases ...*gorm.DB) error {
	db := migrationDB(databases)
	if db == nil {
		return errors.New("database is not initialized")
	}

	var marker settingsstore.SettingRecord
	if err := db.First(&marker, db.Statement.Quote("key")+" = ?", orderRefundPaymentFeeMigrationSettingKey).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	} else if migrationDone(marker.ValueJSON) {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		var payments []paymentdomain.Payment
		if err := tx.Where(
			"deleted_at IS NULL AND status = ? AND provider_type <> ? AND fee_policy = ? AND fee_amount > 0 AND (exception_code IS NULL OR exception_code = '')",
			constants.PaymentStatusSuccess,
			constants.PaymentProviderWallet,
			constants.PaymentFeePolicyMerchantAbsorbed,
		).Find(&payments).Error; err != nil {
			return err
		}

		type paymentFeeSnapshot struct {
			Amount decimal.Decimal
			Fee    decimal.Decimal
		}
		paymentFees := make(map[uint]paymentFeeSnapshot)
		for _, payment := range payments {
			amount := payment.Amount.Decimal.Round(2)
			fee := payment.FeeAmount.Decimal.Round(2)
			if amount.LessThanOrEqual(decimal.Zero) || fee.LessThanOrEqual(decimal.Zero) {
				continue
			}
			snapshot := paymentFees[payment.OrderID]
			snapshot.Amount = snapshot.Amount.Add(amount)
			snapshot.Fee = snapshot.Fee.Add(fee)
			paymentFees[payment.OrderID] = snapshot
		}

		var records []orderdomain.OrderRefundRecord
		if err := tx.Where("deleted_at IS NULL AND type = ?", constants.OrderRefundTypeManual).
			Order("created_at ASC, id ASC").
			Find(&records).Error; err != nil {
			return err
		}
		orderIDs := make([]uint, 0, len(records))
		seenOrderIDs := make(map[uint]struct{}, len(records))
		for _, record := range records {
			if _, exists := seenOrderIDs[record.OrderID]; exists {
				continue
			}
			seenOrderIDs[record.OrderID] = struct{}{}
			orderIDs = append(orderIDs, record.OrderID)
		}
		type refundOrder struct {
			ID       uint
			ParentID *uint
		}
		var orders []refundOrder
		if len(orderIDs) > 0 {
			if err := tx.Model(&orderdomain.Order{}).
				Select("id", "parent_id").
				Where("deleted_at IS NULL AND id IN ?", orderIDs).
				Find(&orders).Error; err != nil {
				return err
			}
		}
		rootByOrderID := make(map[uint]uint, len(orders))
		for _, order := range orders {
			rootID := order.ID
			if order.ParentID != nil && *order.ParentID > 0 {
				rootID = *order.ParentID
			}
			rootByOrderID[order.ID] = rootID
		}

		type refundFeeState struct {
			Principal decimal.Decimal
			Fee       decimal.Decimal
		}
		states := make(map[uint]refundFeeState)
		migratedCount := 0
		for _, record := range records {
			rootID := rootByOrderID[record.OrderID]
			snapshot, exists := paymentFees[rootID]
			if !exists {
				continue
			}
			state := states[rootID]
			feeRefunded := orderdomain.CalculatePaymentFeeRefundAmount(
				snapshot.Amount,
				snapshot.Fee,
				state.Principal,
				state.Fee,
				record.Amount.Decimal,
			)
			if err := tx.Model(&orderdomain.OrderRefundRecord{}).
				Where("id = ?", record.ID).
				Updates(map[string]interface{}{
					"payment_fee_refunded":        true,
					"payment_fee_refunded_amount": money.FromDecimal(feeRefunded),
				}).Error; err != nil {
				return err
			}
			state.Principal = state.Principal.Add(record.Amount.Decimal)
			if state.Principal.GreaterThan(snapshot.Amount) {
				state.Principal = snapshot.Amount
			}
			state.Fee = state.Fee.Add(feeRefunded)
			states[rootID] = state
			migratedCount++
		}

		migrationMarker := settingsstore.SettingRecord{
			Key: orderRefundPaymentFeeMigrationSettingKey,
			ValueJSON: jsonmap.JSON{
				"done":           true,
				"migrated_at":    time.Now().UTC().Format(time.RFC3339),
				"migrated_count": migratedCount,
			},
		}
		return tx.Save(&migrationMarker).Error
	})
}

// ensureOrderItemOriginalPriceMigration 为历史订单项回填原价快照。
// 历史数据没有真实原价，只能以当时已记录的 unit_price/total_price 作为兼容回填。
func ensureOrderItemOriginalPriceMigration(databases ...*gorm.DB) error {
	db := migrationDB(databases)
	if db == nil {
		return errors.New("database is not initialized")
	}

	var marker settingsstore.SettingRecord
	if err := db.First(&marker, db.Statement.Quote("key")+" = ?", orderItemOriginalPriceMigrationKey).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	} else if migrationDone(marker.ValueJSON) {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&orderdomain.OrderItem{}).
			Where("original_unit_price = 0").
			Update("original_unit_price", gorm.Expr("unit_price")).
			Error; err != nil {
			return err
		}
		if err := tx.Model(&orderdomain.OrderItem{}).
			Where("original_total_price = 0").
			Update("original_total_price", gorm.Expr("total_price")).
			Error; err != nil {
			return err
		}

		marker := settingsstore.SettingRecord{
			Key: orderItemOriginalPriceMigrationKey,
			ValueJSON: jsonmap.JSON{
				"done":        true,
				"migrated_at": time.Now().UTC().Format(time.RFC3339),
			},
		}
		return tx.Save(&marker).Error
	})
}

// migrateCartSKUUniqueIndex 迁移购物车唯一索引为 user_id + product_id + sku_id 维度。
func migrateCartSKUUniqueIndex(databases ...*gorm.DB) error {
	db := migrationDB(databases)
	migrator := db.Migrator()

	// 历史唯一索引会阻止同一商品不同 SKU 共存，迁移时必须移除。
	if migrator.HasIndex(&cartdomain.Item{}, "idx_cart_user_product") {
		if err := migrator.DropIndex(&cartdomain.Item{}, "idx_cart_user_product"); err != nil {
			return err
		}
	}

	if !migrator.HasIndex(&cartdomain.Item{}, "idx_cart_user_product_sku") {
		if err := migrator.CreateIndex(&cartdomain.Item{}, "idx_cart_user_product_sku"); err != nil {
			return err
		}
	}
	return nil
}

// ensureUserOAuthIdentityUserProviderUniqueIndex makes one provider binding per
// user a database invariant. Historical duplicates are real login credentials,
// including rows that legacy GetByUserProvider(...).First(...) hid from the UI,
// so startup fails with an explicit preflight diagnostic rather than revoking
// any credential automatically.
func ensureUserOAuthIdentityUserProviderUniqueIndex(databases ...*gorm.DB) error {
	db := migrationDB(databases)
	if db == nil {
		return errors.New("database is not initialized")
	}
	migrator := db.Migrator()
	if migrator.HasIndex(&userOAuthIdentityUserProviderIndexSchema{}, userOAuthIdentityUserProviderUniqueIndex) {
		return nil
	}

	type duplicateGroup struct {
		UserID   uint
		Provider string
		Count    int64
	}
	var groups []duplicateGroup
	if err := db.Model(&externalidentitydomain.Identity{}).
		Select("user_id, provider, COUNT(*) AS count").
		Group("user_id, provider").
		Having("COUNT(*) > 1").
		Order("user_id ASC, provider ASC").
		Scan(&groups).Error; err != nil {
		return err
	}
	if len(groups) > 0 {
		sample := groups[0]
		return fmt.Errorf(
			"cannot create %s: found %d duplicate user/provider group(s); "+
				"first group user_id=%d provider=%q count=%d; "+
				"inspect with SELECT id,user_id,provider,provider_user_id,username,created_at "+
				"FROM user_oauth_identities WHERE (user_id,provider) IN "+
				"(SELECT user_id,provider FROM user_oauth_identities GROUP BY user_id,provider HAVING COUNT(*)>1) "+
				"ORDER BY user_id,provider,id",
			userOAuthIdentityUserProviderUniqueIndex,
			len(groups),
			sample.UserID,
			sample.Provider,
			sample.Count,
		)
	}
	return createIndexConvergingOnExisting(
		migrator,
		&userOAuthIdentityUserProviderIndexSchema{},
		userOAuthIdentityUserProviderUniqueIndex,
	)
}

// createIndexConvergingOnExisting tolerates only the rolling-deploy race where
// another instance creates the exact target index after our HasIndex check.
// The original CreateIndex error remains authoritative if the index still does
// not exist, so duplicate-data and other DDL failures are never swallowed.
func createIndexConvergingOnExisting(
	migrator namedIndexMigrator,
	value interface{},
	name string,
) error {
	if migrator.HasIndex(value, name) {
		return nil
	}
	err := migrator.CreateIndex(value, name)
	if err == nil {
		return nil
	}
	if migrator.HasIndex(value, name) {
		return nil
	}
	return err
}

// ensureProductSKUMigration 执行 SKU 迁移：补默认 SKU、回填 sku_id、完整性校验。
// 迁移完成后写入幂等标记，后续启动跳过。
func ensureProductSKUMigration(databases ...*gorm.DB) error {
	db := migrationDB(databases)
	if db == nil {
		return errors.New("database is not initialized")
	}

	// 检查迁移标记，已完成则跳过
	var marker settingsstore.SettingRecord
	if err := db.First(&marker, db.Statement.Quote("key")+" = ?", skuMigrationSettingKey).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	} else if migrationDone(marker.ValueJSON) {
		return nil
	}

	if err := ensureDefaultProductSKUs(db); err != nil {
		return err
	}

	skuMap, err := buildProductSKUMap(db)
	if err != nil {
		return err
	}

	if err := backfillLegacySKUID(db, skuMap); err != nil {
		return err
	}

	if err := validateSKUMigrationIntegrity(db); err != nil {
		return err
	}

	// 迁移完成，写入标记
	doneMarker := settingsstore.SettingRecord{
		Key: skuMigrationSettingKey,
		ValueJSON: jsonmap.JSON{
			"done":        true,
			"migrated_at": time.Now().UTC().Format(time.RFC3339),
		},
	}
	return db.Save(&doneMarker).Error
}

// ensureDefaultProductSKUs 为每个历史商品补一条 DEFAULT SKU。
func ensureDefaultProductSKUs(db *gorm.DB) error {
	var products []productdomain.Product
	if err := db.Unscoped().
		Select("id, price_amount, manual_stock_total, manual_stock_locked, manual_stock_sold, is_active").
		Find(&products).Error; err != nil {
		return err
	}
	if len(products) == 0 {
		return nil
	}

	type skuProductRow struct {
		ProductID uint
	}
	var existing []skuProductRow
	if err := db.Unscoped().Model(&productdomain.ProductSKU{}).
		Select("DISTINCT product_id").
		Scan(&existing).Error; err != nil {
		return err
	}
	existingMap := make(map[uint]struct{}, len(existing))
	for _, row := range existing {
		existingMap[row.ProductID] = struct{}{}
	}

	createRows := make([]productdomain.ProductSKU, 0)
	for _, product := range products {
		if _, ok := existingMap[product.ID]; ok {
			continue
		}
		createRows = append(createRows, productdomain.ProductSKU{
			ProductID:         product.ID,
			SKUCode:           productdomain.DefaultSKUCode,
			SpecValuesJSON:    jsonmap.JSON{},
			PriceAmount:       product.PriceAmount,
			ManualStockTotal:  product.ManualStockTotal,
			ManualStockLocked: product.ManualStockLocked,
			ManualStockSold:   product.ManualStockSold,
			IsActive:          product.IsActive,
		})
	}

	if len(createRows) == 0 {
		return nil
	}

	return db.Create(&createRows).Error
}

// buildProductSKUMap 构建 product_id -> sku_id 映射，优先选择 DEFAULT SKU。
func buildProductSKUMap(db *gorm.DB) (map[uint]uint, error) {
	type skuRow struct {
		ID        uint
		ProductID uint
		SKUCode   string
	}
	var rows []skuRow
	if err := db.Unscoped().Model(&productdomain.ProductSKU{}).
		Select("id, product_id, sku_code").
		Order("id asc").
		Find(&rows).Error; err != nil {
		return nil, err
	}

	result := make(map[uint]uint, len(rows))
	for _, row := range rows {
		if row.ProductID == 0 || row.ID == 0 {
			continue
		}
		current, exists := result[row.ProductID]
		if !exists {
			result[row.ProductID] = row.ID
			continue
		}
		if strings.EqualFold(strings.TrimSpace(row.SKUCode), productdomain.DefaultSKUCode) {
			result[row.ProductID] = row.ID
			continue
		}
		if current == 0 {
			result[row.ProductID] = row.ID
		}
	}
	return result, nil
}

// backfillLegacySKUID 回填历史 order/cart/card_secret 数据的 sku_id。
func backfillLegacySKUID(db *gorm.DB, productToSKU map[uint]uint) error {
	if len(productToSKU) == 0 {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		for productID, skuID := range productToSKU {
			if productID == 0 || skuID == 0 {
				continue
			}

			if err := tx.Unscoped().Model(&orderdomain.OrderItem{}).
				Where("product_id = ? AND sku_id = 0", productID).
				Update("sku_id", skuID).Error; err != nil {
				return err
			}
			if err := tx.Model(&cartdomain.Item{}).
				Where("product_id = ? AND sku_id = 0", productID).
				Update("sku_id", skuID).Error; err != nil {
				return err
			}
			if err := tx.Unscoped().Model(&cardsecretdomain.Secret{}).
				Where("product_id = ? AND sku_id = 0", productID).
				Update("sku_id", skuID).Error; err != nil {
				return err
			}
			if err := tx.Unscoped().Model(&cardsecretdomain.Batch{}).
				Where("product_id = ? AND sku_id = 0", productID).
				Update("sku_id", skuID).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// validateSKUMigrationIntegrity 校验迁移完整性，避免半迁移状态继续运行。
func validateSKUMigrationIntegrity(db *gorm.DB) error {
	type pendingCheck struct {
		name  string
		query func() (int64, error)
	}

	checks := []pendingCheck{
		{
			name: "order_items",
			query: func() (int64, error) {
				var count int64
				err := db.Model(&orderdomain.OrderItem{}).Where("sku_id = 0").Count(&count).Error
				return count, err
			},
		},
		{
			name: "cart_items",
			query: func() (int64, error) {
				var count int64
				err := db.Model(&cartdomain.Item{}).Where("sku_id = 0").Count(&count).Error
				return count, err
			},
		},
		{
			name: "card_secrets",
			query: func() (int64, error) {
				var count int64
				err := db.Model(&cardsecretdomain.Secret{}).Where("sku_id = 0 AND deleted_at IS NULL").Count(&count).Error
				return count, err
			},
		},
		{
			name: "card_secret_batches",
			query: func() (int64, error) {
				var count int64
				err := db.Model(&cardsecretdomain.Batch{}).Where("sku_id = 0 AND deleted_at IS NULL").Count(&count).Error
				return count, err
			},
		},
	}

	for _, check := range checks {
		count, err := check.query()
		if err != nil {
			return err
		}
		if count > 0 {
			return fmt.Errorf("sku migration incomplete: %s still has %d records with sku_id=0", check.name, count)
		}
	}

	var missingProducts int64
	if err := db.Raw(`
SELECT COUNT(1) FROM (
	SELECT p.id
	FROM products p
	LEFT JOIN product_skus s ON s.product_id = p.id AND s.deleted_at IS NULL
	WHERE p.deleted_at IS NULL
	GROUP BY p.id
	HAVING COUNT(s.id) = 0
) t
`).Scan(&missingProducts).Error; err != nil {
		return err
	}
	if missingProducts > 0 {
		return fmt.Errorf("sku migration incomplete: %d products still have no sku", missingProducts)
	}

	return nil
}

// ensurePaymentProviderBepusdtRenameMigration 把 payment_channels 表里历史
// provider_type='epusdt'（实际是 BEpusdt 适配器）改名为 'bepusdt'，仅执行一次。
// 通过 settings 表写 marker 保证幂等：一旦标记 done，后续启动跳过；
// 即使用户后续新建真 epusdt 渠道也不会被误改。
func ensurePaymentProviderBepusdtRenameMigration(databases ...*gorm.DB) error {
	db := migrationDB(databases)
	if db == nil {
		return errors.New("database is not initialized")
	}

	var marker settingsstore.SettingRecord
	if err := db.First(&marker, db.Statement.Quote("key")+" = ?", paymentProviderBepusdtRenameMigrationSettingKey).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	} else if migrationDone(marker.ValueJSON) {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			"UPDATE payment_channels SET provider_type = ? WHERE provider_type = ?",
			"bepusdt", "epusdt",
		).Error; err != nil {
			return err
		}

		marker := settingsstore.SettingRecord{
			Key: paymentProviderBepusdtRenameMigrationSettingKey,
			ValueJSON: jsonmap.JSON{
				"done":        true,
				"migrated_at": time.Now().UTC().Format(time.RFC3339),
			},
		}
		return tx.Save(&marker).Error
	})
}

// ensurePaymentChannelBepusdtConfigMigration 把旧 BEpusdt channel_type 规范化为新配置结构。
// 缺少显式 trade_type 时保持旧版实际行为，统一使用 usdt.trc20；未知 channel 类型保持原样，
// 并把跳过的渠道 ID 写入 migration marker，避免静默改成错误的支付类型。
func ensurePaymentChannelBepusdtConfigMigration(databases ...*gorm.DB) error {
	db := migrationDB(databases)
	if db == nil {
		return errors.New("database is not initialized")
	}

	var marker settingsstore.SettingRecord
	if err := db.First(&marker, db.Statement.Quote("key")+" = ?", paymentChannelBepusdtConfigMigrationSettingKey).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	} else if migrationDone(marker.ValueJSON) {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		var channels []paymentdomain.PaymentChannel
		if err := tx.Where("provider_type = ?", "bepusdt").Find(&channels).Error; err != nil {
			return err
		}

		legacyChannelTypes := map[string]struct{}{
			"usdt":       {},
			"usdt-trc20": {},
			"usdc-trc20": {},
			"trx":        {},
		}
		migratedCount := 0
		skippedChannelIDs := make([]uint, 0)
		for index := range channels {
			channel := &channels[index]
			config := channel.ConfigJSON
			if config == nil {
				config = jsonmap.JSON{}
			}
			channelType := strings.ToLower(strings.TrimSpace(channel.ChannelType))
			tradeType, _ := config["trade_type"].(string)
			tradeType = strings.TrimSpace(tradeType)
			if tradeType == "" {
				_, knownLegacyType := legacyChannelTypes[channelType]
				if !knownLegacyType {
					if channelType != "bepusdt" {
						skippedChannelIDs = append(skippedChannelIDs, channel.ID)
					}
					continue
				}
				tradeType = "usdt.trc20"
			}
			if channelType == "bepusdt" {
				continue
			}

			config["trade_type"] = tradeType
			if orderMode, _ := config["order_mode"].(string); strings.TrimSpace(orderMode) == "" {
				config["order_mode"] = "transaction"
			}
			if err := tx.Model(channel).Updates(map[string]interface{}{
				"channel_type": "bepusdt",
				"config_json":  config,
			}).Error; err != nil {
				return fmt.Errorf("migrate bepusdt channel %d: %w", channel.ID, err)
			}
			migratedCount++
		}

		marker := settingsstore.SettingRecord{
			Key: paymentChannelBepusdtConfigMigrationSettingKey,
			ValueJSON: jsonmap.JSON{
				"done":                true,
				"migrated_at":         time.Now().UTC().Format(time.RFC3339),
				"migrated_count":      migratedCount,
				"skipped_channel_ids": skippedChannelIDs,
			},
		}
		return tx.Save(&marker).Error
	})
}

// ensureCategoryParentMigration 兼容历史单层分类数据，统一将空 parent_id 视为 0。
func ensureCategoryParentMigration(databases ...*gorm.DB) error {
	db := migrationDB(databases)
	if db == nil {
		return errors.New("database is not initialized")
	}

	var marker settingsstore.SettingRecord
	if err := db.First(&marker, db.Statement.Quote("key")+" = ?", categoryParentMigrationSettingKey).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	} else if migrationDone(marker.ValueJSON) {
		return nil
	}

	if !db.Migrator().HasColumn(&categorydomain.Category{}, "parent_id") {
		return nil
	}
	if err := db.Model(&categorydomain.Category{}).Where("parent_id IS NULL").Update("parent_id", 0).Error; err != nil {
		return err
	}

	doneMarker := settingsstore.SettingRecord{
		Key: categoryParentMigrationSettingKey,
		ValueJSON: jsonmap.JSON{
			"done":        true,
			"migrated_at": time.Now().UTC().Format(time.RFC3339),
		},
	}
	return db.Save(&doneMarker).Error
}
