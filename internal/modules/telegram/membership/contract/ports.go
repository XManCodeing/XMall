package contract

import (
	"context"
	"errors"
	"math"
	"time"

	ei "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/contract"
	od "github.com/XManCodeing/XMall/internal/modules/order/domain"

	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
)

var (
	// ErrInvalid 表示请求字段或现有商品/SKU 关联无效。
	ErrInvalid = errors.New("invalid membership configuration")
	// ErrNotFound 表示不存在所请求的会员配置或快照。
	ErrNotFound = errors.New("membership configuration or snapshot not found")
	// ErrConflict 表示 CAS 版本过期或不可变快照已经存在。
	ErrConflict = errors.New("membership version conflict")
	// ErrCapability 表示群目标、路由或新鲜权限不满足要求。
	ErrCapability = errors.New("membership target capability unavailable")
	// ErrSalesClosed 表示商品当前未启用销售，或不允许通过配置编辑直接切换销售状态。
	ErrSalesClosed = errors.New("membership sales unavailable: product is not enabled")
	// ErrUntrustedIdentity 表示未提供可信身份，不允许用 HTTP 字段补齐。
	ErrUntrustedIdentity = errors.New("trusted membership purchase identity required")
	// ErrOverlapUnavailable 表示永久资格账本端口缺失，不能将其视为无永久覆盖。
	ErrOverlapUnavailable = errors.New("membership permanent overlap ledger unavailable")
	// ErrTransactionRequired 表示外部编排没有传入实际事务句柄。
	ErrTransactionRequired = errors.New("membership requires an active database transaction")
)

// AdminTerm 按 SKU ID 或稳定编码绑定期限；新建和 DEFAULT 使用 SKUCode。
type AdminTerm struct {
	SKUID   uint   `json:"sku_id"`
	SKUCode string `json:"sku_code"`
	Kind    string `json:"kind"`
	Months  int64  `json:"months"`
}

// AdminConfigInput 是会员商品一次原子保存的配置输入。
type AdminConfigInput struct {
	ExpectedVersion uint64      `json:"expected_version"`
	Enabled         bool        `json:"enabled"`
	Quantity        int         `json:"quantity"`
	TargetIDs       []uint      `json:"target_ids"`
	Terms           []AdminTerm `json:"terms"`
}

// AdminWriter 将配置与 SKU 编码绑定到调用者现有事务；失败必须回滚整次商品保存。
type AdminWriter interface {
	SaveProductConfig(context.Context, uint, AdminConfigInput) error
}

// AdminOperations 仅在原商品事务内处理会员停销/CAS及删除保护，不接触历史权益。
type AdminOperations interface {
	PrepareProductOperation(context.Context, uint, uint64, bool, bool) error
}

// ConfigInput 是整个商品会员扩展的替换输入。ExpectedVersion=0 只允许首次创建。
// Targets/Terms 必须非空且唯一，Quantity 固定为 1；Enabled 当前只能为 false。
type ConfigInput struct {
	ProductID       uint        `json:"product_id"`
	ExpectedVersion uint64      `json:"expected_version"`
	Enabled         bool        `json:"enabled"`
	Quantity        int         `json:"quantity"`
	TargetIDs       []uint      `json:"target_ids"`
	Terms           []d.SKUTerm `json:"terms"`
}

// TrustedIdentity 无可导出字段，HTTP 解码不能直接赋值；零值永远不可用。
// 它是内部可信编排参数，不代替身份适配器在同一订单事务中的绑定版本复核。
type TrustedIdentity struct {
	accountID  uint
	telegramID int64
	version    uint64
}

// IdentityFromVerifiedBinding 仅供已校验登录/Telegram 绑定的内部适配器调用。
// account、telegram、version 必须来自同一事务内受锁/CAS 保护的身份记录，不能来自 HTTP body。
// 返回不可直接反序列化的身份值；无效字段返回 ErrUntrustedIdentity。
func IdentityFromVerifiedBinding(account uint, telegram int64, version uint64) (TrustedIdentity, error) {
	if account == 0 || telegram <= 0 || version == 0 || version > math.MaxInt64 {
		return TrustedIdentity{}, ErrUntrustedIdentity
	}
	return TrustedIdentity{accountID: account, telegramID: telegram, version: version}, nil
}

// Values 返回可信账号、Telegram ID、身份版本；零值或损坏参数返回错误。
func (identity TrustedIdentity) Values() (uint, int64, uint64, error) {
	if identity.accountID == 0 || identity.telegramID <= 0 || identity.version == 0 {
		return 0, 0, 0, ErrUntrustedIdentity
	}
	return identity.accountID, identity.telegramID, identity.version, nil
}

// SnapshotInput 仅承载可信订单事务内的记录引用，不接收期限、目标或受益身份。
// 调用方须验证订单项商品/SKU/数量/账号归属，并提供同一事务内的 TrustedIdentity。
type SnapshotInput struct {
	OrderID       uint
	OrderItemID   uint
	ProductID     uint
	SKUID         uint
	ConfigVersion uint64
	Quantity      int
}

// ConfigReader 供网站/Bot/后台读取同一份扩展，不进行网络探测。
type ConfigReader interface {
	Config(context.Context, uint) (*d.ProductConfig, error)
}

// Share unavailable reasons are stable API codes; an available link has an empty reason.
const (
	ShareUnavailable    = "unavailable"
	ShareMissingBinding = "missing_binding"
	ShareMixedBots      = "mixed_bots"
	ShareBotUnavailable = "bot_unavailable"
)

// ProductShare is a read-only permanent deep link, not purchase or access authorization.
type ProductShare struct {
	URL               string `json:"share_url"`
	UnavailableReason string `json:"share_unavailable_reason"`
}

// TargetNameReader returns current registered names for the requested logical target IDs.
// Missing/deleted targets are omitted; database errors must propagate.
type TargetNameReader interface {
	TargetNames(context.Context, []uint) (map[uint]string, error)
}

// ShareReader reads an existing code and live eligibility without creating any data.
type ShareReader interface {
	ProductShare(context.Context, *d.ProductConfig, bool) (ProductShare, error)
}

// ShareWriter creates or reuses a code only after validating current persisted sale/bot bindings.
type ShareWriter interface {
	EnsureProductShare(context.Context, uint) (ProductShare, error)
}

// ShareResolver resolves only exact short codes; callers must authorize current bot relations.
type ShareResolver interface {
	ResolveProductShare(context.Context, string) (uint, error)
}

// DisplayConfig 是网站与 Bot 共用的只读白名单，不包含目标 ID、路由或邀请信息。
type DisplayConfig struct {
	SalesOpen  bool
	ProductID  uint
	Version    uint64
	Quantity   int
	Terms      []d.SKUTerm
	GroupNames []string
}

// DisplayReader 按商品读取当前配置和管理员登记群名；仅无配置返回 ErrNotFound。
// 目标缺失、用途错误或读取失败必须返回其他错误，禁止降级为普通商品。
type DisplayReader interface {
	Display(context.Context, uint) (*DisplayConfig, error)
}

// OrderItemSnapshot 复用不可变快照值，供跨模块只读 DTO 使用。
type OrderItemSnapshot d.OrderItemSnapshot

// SnapshotReader 按已授权订单项读取历史快照，不访问当前配置。
type SnapshotReader interface {
	Snapshot(context.Context, uint) (*d.OrderItemSnapshot, error)
}

// Store 只执行数据库操作；所有目标检查均读取当前持久观测，不进行网络请求。
// Snapshot persistence 仅为 B4 集成原语，不构成完整结算授权或 B5 权益授予。
type Store interface {
	ConfigReader
	SaveConfig(context.Context, ConfigInput, time.Time) (*d.ProductConfig, error)
	Snapshot(context.Context, uint) (*d.OrderItemSnapshot, error)
}

// SnapshotWriter 必须绑定外部实际订单事务，不能由普通连接构造后自动提交。
// 本接口复核绑定和真实订单项归属；不授权永久资格重叠或支付，销售始终关闭。
type SnapshotWriter interface {
	InsertSnapshot(context.Context, SnapshotInput, TrustedIdentity, time.Time) (*d.OrderItemSnapshot, error)
}

// CheckoutLine 是共享订单引擎已解析 DEFAULT SKU 后的商品数量引用。
type CheckoutLine struct {
	ProductID uint
	SKUID     uint
	Quantity  int
}

// CheckoutPlan 由数据库适配器生成；不是 HTTP 输入 DTO，订单 API 不接收此值。
// 创建后仍须在订单事务中重验绑定、配置版本和目标路由。
type CheckoutPlan struct {
	AccountID uint
	Binding   ei.TelegramBinding
	Lines     []SnapshotInput
	Targets   map[uint][]d.SnapshotTarget
}

// Checkout 仅绑定实际订单事务，不持有全局 DB 或网络依赖。
// Prepare 用于预览与下单共用的可信预检，Revalidate 在写订单前保持锁直到提交。
type Checkout interface {
	Prepare(context.Context, uint, []CheckoutLine, time.Time) (*CheckoutPlan, error)
	Revalidate(context.Context, *CheckoutPlan, time.Time) error
	SaveItems(context.Context, *CheckoutPlan, []od.OrderItem, time.Time) error
}

// CheckoutProvider 是订单工作单元的可选扩展；纯普通商品不会查询身份或群表。
type CheckoutProvider interface{ MembershipCheckout() (Checkout, error) }

// PermanentOverlapGuard 由未来 B5 同事务账本实现；nil 必须 fail-closed。
// 返回 nil 必须代表全部目标不存在永久覆盖；全覆盖和部分覆盖都返回明确错误。
type PermanentOverlapGuard interface {
	CheckPermanentOverlap(context.Context, uint, int64, []d.SnapshotTarget) error
}
