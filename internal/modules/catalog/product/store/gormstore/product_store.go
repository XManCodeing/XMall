package gormstore

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	productcontract "github.com/XManCodeing/XMall/internal/modules/catalog/product/contract"
	productdomain "github.com/XManCodeing/XMall/internal/modules/catalog/product/domain"
	membershipdomain "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"

	"github.com/XManCodeing/XMall/internal/persistence/gormutil"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ProductStore 是 Catalog Product 端口的 GORM 实现。
type ProductStore struct {
	db *gorm.DB
}

var _ productcontract.Repository = (*ProductStore)(nil)
var _ productcontract.PublicSaleChecker = (*ProductStore)(nil)

// LockProductForWrite 在当前事务锁定 productID，防止 SKU 写先于配置聚合锁造成锁顺序倒置。
func (r *ProductStore) LockProductForWrite(productID uint) error {
	var product productdomain.Product
	return r.db.Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND deleted_at IS NULL", productID).First(&product).Error
}

// CheckPublicSale 检查商品交易状态；已启用会员仍须通过订单/付款身份及快照复核。
// productIDs 为交易商品集合；禁用配置、失效商品或分类均关闭会员销售。
func (r *ProductStore) CheckPublicSale(productIDs []uint) error {
	return r.checkMembershipPolicy(productIDs, false)
}

// CheckOrdinaryWrite 拒绝全部会员配置，避免通用更新/删除绕过会员 CAS。
// productIDs 是通用写入目标，查询失败时返回错误且不得继续写入。
func (r *ProductStore) CheckOrdinaryWrite(productIDs []uint) error {
	return r.checkMembershipPolicy(productIDs, true)
}

// checkMembershipPolicy 在当前连接按商品、配置加锁；writing 区分通用写入与交易。
// 返回门控错误或数据库错误；无会员配置不影响普通商品行为。
func (r *ProductStore) checkMembershipPolicy(productIDs []uint, writing bool) error {
	if len(productIDs) == 0 {
		return nil
	}
	// 与会员配置写入共用商品行锁，避免检查通过后在订单/余额事务内被转换。
	// 按主键固定顺序加锁，降低多商品订单之间死锁概率。
	var products []productdomain.Product
	if err := r.db.Select("id, is_active, category_id").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id IN ? AND deleted_at IS NULL", productIDs).Order("id").Find(&products).Error; err != nil {
		return fmt.Errorf("lock products for sales: %w", err)
	}
	var configs []membershipdomain.ProductConfig
	if err := r.db.Select("product_id, enabled").Clauses(clause.Locking{Strength: "UPDATE"}).Where("product_id IN ?", productIDs).Order("product_id").Find(&configs).Error; err != nil {
		return fmt.Errorf("check membership sales: %w", err)
	}
	for _, config := range configs {
		if writing || !config.Enabled {
			return productcontract.ErrMembershipSaleClosed
		}
		var categoryID uint
		for _, row := range products {
			if row.ID == config.ProductID && row.IsActive {
				categoryID = row.CategoryID
			}
		}
		var count int64
		if err := r.db.Table("categories").Where("id = ? AND is_active = ? AND deleted_at IS NULL", categoryID, true).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return productcontract.ErrMembershipSaleClosed
		}
	}
	return nil
}

func NewProductStore(db *gorm.DB) *ProductStore {
	return &ProductStore{db: db}
}

// BindTx 将 Store 绑定到调用方事务，并仅暴露 Product 端口。
func (r *ProductStore) BindTx(tx *gorm.DB) productcontract.Repository {
	if tx == nil {
		return r
	}
	return NewProductStore(tx)
}

func (r *ProductStore) Transaction(fn func(tx *gorm.DB) error) error {
	if fn == nil {
		return nil
	}
	return r.db.Transaction(fn)
}

// List 商品列表
func (r *ProductStore) List(filter productcontract.ListFilter) ([]productdomain.Product, int64, error) {
	var products []productdomain.Product

	query := r.db.Model(&productdomain.Product{}).Where("products.deleted_at IS NULL")
	if filter.OnlyMembership {
		query = query.Where("EXISTS (SELECT 1 FROM telegram_membership_product_configs mc WHERE mc.product_id = products.id)")
	}
	// 在 Count 和分页之前划分后台商品类型，禁用配置也仍属于会员群商品。
	if filter.ExcludeMembership {
		query = query.Where("NOT EXISTS (SELECT 1 FROM telegram_membership_product_configs mc WHERE mc.product_id = products.id)")
	}
	if filter.WithCategory {
		query = query.Preload("Category", "deleted_at IS NULL")
	}
	if filter.OnlyActive {
		query = query.Where("products.is_active = ?", true)
		query = query.Where("EXISTS (SELECT 1 FROM categories c WHERE c.id = products.category_id AND c.is_active = ? AND c.deleted_at IS NULL)", true)
		query = query.Preload("SKUs", func(db *gorm.DB) *gorm.DB {
			return db.Where("deleted_at IS NULL AND is_active = ?", true).Order("sort_order DESC, id ASC")
		})
	} else {
		query = query.Preload("SKUs", func(db *gorm.DB) *gorm.DB {
			return db.Where("deleted_at IS NULL").Order("sort_order DESC, id ASC")
		})
	}
	if len(filter.CategoryIDs) > 0 {
		query = query.Where("category_id IN ?", filter.CategoryIDs)
	} else if filter.CategoryID != "" {
		query = query.Where("category_id = ?", filter.CategoryID)
	}
	// 三态上架筛选：OnlyActive 已强制 is_active = true，此时不再叠加
	if filter.IsActive != nil && !filter.OnlyActive {
		query = query.Where("products.is_active = ?", *filter.IsActive)
	}
	if len(filter.ExcludeProductIDs) > 0 {
		query = query.Where("products.id NOT IN ?", filter.ExcludeProductIDs)
	}
	if fulfillmentType := strings.TrimSpace(filter.FulfillmentType); fulfillmentType != "" {
		query = query.Where("fulfillment_type = ?", fulfillmentType)
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		like := "%" + search + "%"
		condition, argCount := gormutil.BuildLocalizedLikeCondition(r.db, []string{"slug"}, []string{"title_json", "description_json"})
		searchQuery := r.db.Where(condition, gormutil.RepeatLikeArgs(like, argCount)...)

		skuCondition, skuArgCount := gormutil.BuildLocalizedLikeCondition(r.db, []string{"ps.sku_code"}, nil)
		searchQuery = searchQuery.Or(
			"EXISTS (SELECT 1 FROM product_skus ps WHERE ps.product_id = products.id AND ps.deleted_at IS NULL AND ("+skuCondition+"))",
			gormutil.RepeatLikeArgs(like, skuArgCount)...,
		)

		if numericID, err := strconv.ParseUint(search, 10, 64); err == nil && numericID > 0 {
			searchQuery = searchQuery.Or("id = ?", uint(numericID))
		}
		query = query.Where(searchQuery)
	}

	if filter.UpdatedAfter != nil {
		// 库存变化落在 SKU（人工库存预占/售出）与卡密行上，不会改动商品行本身；
		// 下游增量同步依赖此过滤，需一并纳入，否则库存变化要等每日全量同步才可见。
		after := *filter.UpdatedAfter
		query = query.Where("(products.updated_at > ? OR "+
			"EXISTS (SELECT 1 FROM product_skus ps WHERE ps.product_id = products.id AND ps.updated_at > ?) OR "+
			"EXISTS (SELECT 1 FROM card_secrets cs WHERE cs.product_id = products.id AND cs.updated_at > ?))",
			after, after, after)
	}

	stockStatus := strings.ToLower(strings.TrimSpace(filter.StockStatus))
	query = applyStockStatusFilter(query, stockStatus, filter.LowStockThreshold)
	if filter.HasWholesalePrices != nil {
		expr := gormutil.JSONArrayLengthExpr(r.db, "wholesale_prices")
		if *filter.HasWholesalePrices {
			query = query.Where(expr + " > 0")
		} else {
			query = query.Where(expr + " = 0")
		}
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	query = gormutil.ApplyPagination(query, filter.Page, filter.PageSize)

	if err := query.Order("sort_order DESC, created_at DESC").Find(&products).Error; err != nil {
		return nil, 0, err
	}

	return products, total, nil
}

func applyStockStatusFilter(query *gorm.DB, status string, lowStockThreshold int) *gorm.DB {
	if query == nil || status == "" {
		return query
	}
	if lowStockThreshold < 0 {
		lowStockThreshold = 0
	}

	// manual 库存子查询
	const manualActiveSKUExists = "EXISTS (SELECT 1 FROM product_skus ps WHERE ps.product_id = products.id AND ps.is_active = true AND ps.deleted_at IS NULL)"
	const manualUnlimitedSKUExists = "EXISTS (SELECT 1 FROM product_skus ps WHERE ps.product_id = products.id AND ps.is_active = true AND ps.deleted_at IS NULL AND ps.manual_stock_total = -1)"
	const manualSKURemaining = "COALESCE((SELECT SUM(CASE WHEN ps.manual_stock_total > 0 THEN ps.manual_stock_total ELSE 0 END) FROM product_skus ps WHERE ps.product_id = products.id AND ps.is_active = true AND ps.deleted_at IS NULL), 0)"

	// auto 库存子查询（可用卡密数）
	const autoStockCount = "COALESCE((SELECT COUNT(*) FROM card_secrets cs WHERE cs.product_id = products.id AND cs.status = 'available' AND cs.deleted_at IS NULL), 0)"

	// upstream 库存子查询（通过 product_mappings + sku_mappings）
	const upstreamUnlimitedExists = "EXISTS (SELECT 1 FROM product_mappings pm JOIN sku_mappings sm ON sm.product_mapping_id = pm.id AND sm.deleted_at IS NULL WHERE pm.local_product_id = products.id AND pm.deleted_at IS NULL AND sm.upstream_stock = -1)"
	const upstreamStockSum = "COALESCE((SELECT SUM(CASE WHEN sm.upstream_stock > 0 THEN sm.upstream_stock ELSE 0 END) FROM product_mappings pm JOIN sku_mappings sm ON sm.product_mapping_id = pm.id AND sm.deleted_at IS NULL WHERE pm.local_product_id = products.id AND pm.deleted_at IS NULL), 0)"

	switch status {
	case "low":
		// manual: 非无限且剩余 <= 0 | auto: 可用卡密位于 [0, 低库存阈值] | upstream: 非无限且库存和 = 0
		condition := fmt.Sprintf("("+
			"(fulfillment_type = 'manual' AND (((%s) AND NOT (%s) AND (%s) <= 0) OR (NOT (%s) AND manual_stock_total = 0)))"+
			" OR (fulfillment_type = 'auto' AND (%s) >= 0 AND (%s) <= ?)"+
			" OR (fulfillment_type = 'upstream' AND NOT (%s) AND (%s) = 0)"+
			")",
			manualActiveSKUExists, manualUnlimitedSKUExists, manualSKURemaining, manualActiveSKUExists,
			autoStockCount, autoStockCount,
			upstreamUnlimitedExists, upstreamStockSum,
		)
		return query.Where(condition, lowStockThreshold)
	case "normal":
		// manual: 非无限且剩余 > 0 | auto: 可用卡密 > 低库存阈值 | upstream: 非无限且库存和 > 0
		condition := fmt.Sprintf("("+
			"(fulfillment_type = 'manual' AND (((%s) AND NOT (%s) AND (%s) > 0) OR (NOT (%s) AND manual_stock_total > 0)))"+
			" OR (fulfillment_type = 'auto' AND (%s) > ?)"+
			" OR (fulfillment_type = 'upstream' AND NOT (%s) AND (%s) > 0)"+
			")",
			manualActiveSKUExists, manualUnlimitedSKUExists, manualSKURemaining, manualActiveSKUExists,
			autoStockCount,
			upstreamUnlimitedExists, upstreamStockSum,
		)
		return query.Where(condition, lowStockThreshold)
	case "unlimited":
		// manual: 有无限 SKU | upstream: 有无限库存的映射
		condition := fmt.Sprintf("("+
			"(fulfillment_type = 'manual' AND ((%s) OR (NOT (%s) AND manual_stock_total = -1)))"+
			" OR (fulfillment_type = 'upstream' AND (%s))"+
			")",
			manualUnlimitedSKUExists, manualActiveSKUExists,
			upstreamUnlimitedExists,
		)
		return query.Where(condition)
	default:
		return query
	}
}

// GetBySlug 根据 slug 获取商品
func (r *ProductStore) GetBySlug(slug string, onlyActive bool) (*productdomain.Product, error) {
	query := r.db.Preload("Category", "deleted_at IS NULL").Where("products.deleted_at IS NULL AND products.slug = ?", slug)
	if onlyActive {
		query = query.Where("products.is_active = ?", true)
		query = query.Where("EXISTS (SELECT 1 FROM categories c WHERE c.id = products.category_id AND c.is_active = ? AND c.deleted_at IS NULL)", true)
		query = query.Preload("SKUs", func(db *gorm.DB) *gorm.DB {
			return db.Where("deleted_at IS NULL AND is_active = ?", true).Order("sort_order DESC, id ASC")
		})
	} else {
		query = query.Preload("SKUs", func(db *gorm.DB) *gorm.DB {
			return db.Where("deleted_at IS NULL").Order("sort_order DESC, id ASC")
		})
	}

	var product productdomain.Product
	if err := query.First(&product).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &product, nil
}

// GetByID 根据 ID 获取商品
func (r *ProductStore) GetByID(id string) (*productdomain.Product, error) {
	pk, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return nil, nil
	}
	var product productdomain.Product
	if err := r.db.Preload("Category", "deleted_at IS NULL").
		Preload("SKUs", func(db *gorm.DB) *gorm.DB {
			return db.Where("deleted_at IS NULL AND is_active = ?", true).Order("sort_order DESC, id ASC")
		}).
		Where("products.deleted_at IS NULL").
		First(&product, pk).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &product, nil
}

// GetAdminByID 根据 ID 获取后台商品详情，包含全部 SKU
func (r *ProductStore) GetAdminByID(id string) (*productdomain.Product, error) {
	pk, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return nil, nil
	}
	var product productdomain.Product
	if err := r.db.Preload("Category", "deleted_at IS NULL").
		Preload("SKUs", func(db *gorm.DB) *gorm.DB {
			return db.Where("deleted_at IS NULL").Order("sort_order DESC, id ASC")
		}).
		Where("products.deleted_at IS NULL").
		First(&product, pk).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &product, nil
}

// ListByIDs 批量获取商品
func (r *ProductStore) ListByIDs(ids []uint) ([]productdomain.Product, error) {
	if len(ids) == 0 {
		return []productdomain.Product{}, nil
	}
	var products []productdomain.Product
	if err := r.db.Where("deleted_at IS NULL AND id IN ?", ids).Find(&products).Error; err != nil {
		return nil, err
	}
	return products, nil
}

// Create 创建商品
func (r *ProductStore) Create(product *productdomain.Product) error {
	return r.db.Create(product).Error
}

// Update 更新商品自身字段。
//
// 不级联保存关联：调用方拿到的 product 往往带着读取时预加载的 Category/SKUs 快照，
// 而 SKU 删除是硬删除，级联 upsert 会把已删除的规格按原 ID 重新插回（issue #344）。
// SKU 一律由 SKURepository 独占管理。
func (r *ProductStore) Update(product *productdomain.Product) error {
	return r.db.Omit(clause.Associations).Save(product).Error
}

// QuickUpdatePublicSale 在同事务锁定商品与会员配置后开售，关闭检查与更新之间的竞态。
func (r *ProductStore) QuickUpdatePublicSale(id string, fields map[string]interface{}) error {
	parsed, err := strconv.ParseUint(id, 10, 64)
	if err != nil || parsed == 0 {
		return productcontract.ErrNotFound
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		scoped := NewProductStore(tx)
		if err := scoped.CheckOrdinaryWrite([]uint{uint(parsed)}); err != nil {
			return err
		}
		return scoped.QuickUpdate(id, fields)
	})
}

// QuickUpdate 快速更新商品指定字段
func (r *ProductStore) QuickUpdate(id string, fields map[string]interface{}) error {
	return r.db.Model(&productdomain.Product{}).Where("id = ? AND deleted_at IS NULL", id).Updates(fields).Error
}

// Delete 删除商品
func (r *ProductStore) Delete(id string) error {
	return r.db.Model(&productdomain.Product{}).Where("id = ? AND deleted_at IS NULL", id).Update("deleted_at", time.Now()).Error
}

// CountBySlug 统计 slug 数量（含软删除记录：products.slug 唯一索引同样约束已删除商品）
func (r *ProductStore) CountBySlug(slug string, excludeID *string) (int64, error) {
	var count int64
	query := r.db.Model(&productdomain.Product{}).Where("slug = ?", slug)
	if excludeID != nil {
		query = query.Where("id != ?", *excludeID)
	}
	if err := query.Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// ReserveManualStock 预占手动库存
func (r *ProductStore) ReserveManualStock(productID uint, quantity int) (int64, error) {
	return gormutil.ReserveManualStock(r.db, &productdomain.Product{}, productID, quantity)
}

// ReleaseManualStock 释放手动库存占用
func (r *ProductStore) ReleaseManualStock(productID uint, quantity int) (int64, error) {
	return gormutil.ReleaseManualStock(r.db, &productdomain.Product{}, productID, quantity)
}

// ConsumeManualStock 消耗手动库存（支付成功后占用转已售）
func (r *ProductStore) ConsumeManualStock(productID uint, quantity int) (int64, error) {
	return gormutil.ConsumeManualStock(r.db, &productdomain.Product{}, productID, quantity)
}
