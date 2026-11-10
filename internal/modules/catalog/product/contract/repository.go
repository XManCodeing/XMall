package productcontract

import (
	"errors"
	"time"

	productdomain "github.com/XManCodeing/XMall/internal/modules/catalog/product/domain"
)

// ErrMembershipSaleClosed 表示会员扩展商品尚未开放通用销售。
var ErrMembershipSaleClosed = errors.New("membership product sales are closed")

// ErrSalePolicyUnavailable 表示生产销售保护未正确装配。
var ErrSalePolicyUnavailable = errors.New("product sale policy unavailable")

// ProductWriteLocker 保证会员写入先锁商品，再修改 SKU 和配置，统一锁顺序。
type ProductWriteLocker interface {
	LockProductForWrite(productID uint) error
}

// AtomicPublicSaleUpdater 将普通后台开售检查与字段写入放在同一事务中。
type AtomicPublicSaleUpdater interface {
	QuickUpdatePublicSale(id string, fields map[string]interface{}) error
}

// PublicSaleChecker 是事务绑定商品仓储提供的可选销售保护端口。
// CheckPublicSale 接收商品 ID 集合；会员配置须启用，交易仍需身份/权益复核。
type PublicSaleChecker interface {
	CheckPublicSale(productIDs []uint) error
}

// RequirePublicSale 使用当前事务的仓储检查商品；缺少端口时关闭销售。
// repository 为当前商品仓储，productIDs 为全部交易商品，返回拒绝或查询错误。
func RequirePublicSale(repository interface{}, productIDs []uint) error {
	checker, ok := repository.(PublicSaleChecker)
	if !ok || checker == nil {
		return ErrSalePolicyUnavailable
	}
	return checker.CheckPublicSale(productIDs)
}

// RequireOrdinaryWrite 拒绝通用入口对任何会员配置的写删，防止绕过专用 CAS。
// repository 是当前事务仓储，productIDs 是目标集合；缺少隔离端口时关闭写入。
func RequireOrdinaryWrite(repository interface{}, productIDs []uint) error {
	checker, ok := repository.(interface{ CheckOrdinaryWrite([]uint) error })
	if !ok || checker == nil {
		return ErrSalePolicyUnavailable
	}
	return checker.CheckOrdinaryWrite(productIDs)
}

// ListFilter 描述 Catalog 商品列表的持久化筛选条件。
//
// IsActive 与 OnlyActive 都是上架状态条件，分工不同：OnlyActive 是公开侧强制的
// 「必须上架」开关；IsActive 是后台可选的三态筛选，nil 不限、true 仅已上架、false 仅已下架。
type ListFilter struct {
	OnlyMembership bool
	// ExcludeMembership 仅用于普通后台列表；按扩展配置存在性排除，不按购买身份判断。
	ExcludeMembership  bool
	Page               int
	PageSize           int
	CategoryID         string
	CategoryIDs        []uint
	ExcludeProductIDs  []uint
	Search             string
	FulfillmentType    string
	StockStatus        string
	HasWholesalePrices *bool
	LowStockThreshold  int
	OnlyActive         bool
	IsActive           *bool
	WithCategory       bool
	UpdatedAfter       *time.Time
}

// Repository 定义商品领域需要的持久化能力；事务适配保留在 GORM Store。
type Repository interface {
	List(filter ListFilter) ([]productdomain.Product, int64, error)
	GetBySlug(slug string, onlyActive bool) (*productdomain.Product, error)
	GetByID(id string) (*productdomain.Product, error)
	GetAdminByID(id string) (*productdomain.Product, error)
	ListByIDs(ids []uint) ([]productdomain.Product, error)
	Create(item *productdomain.Product) error
	Update(item *productdomain.Product) error
	Delete(id string) error
	CountBySlug(slug string, excludeID *string) (int64, error)
	ReserveManualStock(productID uint, quantity int) (int64, error)
	ReleaseManualStock(productID uint, quantity int) (int64, error)
	ConsumeManualStock(productID uint, quantity int) (int64, error)
	QuickUpdate(id string, fields map[string]interface{}) error
}

// SKURepository 定义商品 SKU 领域需要的持久化能力。
type SKURepository interface {
	ListByProduct(productID uint, onlyActive bool) ([]productdomain.ProductSKU, error)
	GetByID(id uint) (*productdomain.ProductSKU, error)
	GetByProductAndCode(productID uint, skuCode string) (*productdomain.ProductSKU, error)
	ListByIDs(ids []uint) ([]productdomain.ProductSKU, error)
	Create(item *productdomain.ProductSKU) error
	CreateBatch(items []productdomain.ProductSKU) error
	Update(item *productdomain.ProductSKU) error
	Delete(id uint) error
	DeleteByProduct(productID uint) error
	PurgeSoftDeletedByProductAndCode(productID uint, skuCode string) error
	ReserveManualStock(skuID uint, quantity int) (int64, error)
	ReleaseManualStock(skuID uint, quantity int) (int64, error)
	ConsumeManualStock(skuID uint, quantity int) (int64, error)
}
