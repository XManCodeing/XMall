package contract

import (
	"context"
	"errors"
	"time"

	affiliatecontract "github.com/XManCodeing/XMall/internal/modules/affiliate/contract"
	cardsecretcontract "github.com/XManCodeing/XMall/internal/modules/cardsecret/contract"
	productcontract "github.com/XManCodeing/XMall/internal/modules/catalog/product/contract"
	couponcontract "github.com/XManCodeing/XMall/internal/modules/coupon/contract"
	fulfillmentcontract "github.com/XManCodeing/XMall/internal/modules/fulfillment/contract"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	resellercontract "github.com/XManCodeing/XMall/internal/modules/reseller/contract"
	resellerdomain "github.com/XManCodeing/XMall/internal/modules/reseller/domain"
	walletcontract "github.com/XManCodeing/XMall/internal/modules/wallet/contract"
	"github.com/XManCodeing/XMall/internal/shared/money"
)

var ErrProcurementRefundBlocked = errors.New("active upstream procurement blocks local refund")

var (
	// ErrRefundRequestConflict rejects reuse of an idempotency key with a different intent.
	ErrRefundRequestConflict = errors.New("refund request conflicts with persisted intent")
	// ErrRefundRequestReplay indicates that the original committed request must be resumed.
	ErrRefundRequestReplay = errors.New("refund request already committed")
	// ErrRefundRequestInvalid indicates invalid amount, order, or explicit purchase selection.
	ErrRefundRequestInvalid = errors.New("invalid membership refund request")
	// ErrRefundRequestDenied indicates missing backend refund/disposition permissions.
	ErrRefundRequestDenied = errors.New("membership refund permission denied")
)

// MembershipRefundRequest is the durable order-owned command shared with orchestration.
type MembershipRefundRequest orderdomain.MembershipRefundRequest

// TableName keeps the cross-module projection on the order-owned command table.
func (MembershipRefundRequest) TableName() string { return "membership_refund_requests" }

// MembershipRefundInput accepts one explicit intent; existing means no new money movement.
type MembershipRefundInput struct {
	RequestKey       string `json:"request_key"`
	OrderID          uint   `json:"order_id,string"`
	ExistingRefundID uint   `json:"existing_refund_id,string"`
	ItemIDs          []uint `json:"item_ids"`
	Method           string `json:"method"`
	Amount           string `json:"amount"`
	Remark           string `json:"remark"`
	ActorAdminID     uint   `json:"-"`
}

// MembershipRefundCommands must be implemented on the same transaction-bound order store.
type MembershipRefundCommands interface {
	BeginMembershipRefund(*MembershipRefundRequest) error
	BindMembershipRefund(string, uint) error
	GetMembershipRefund(string) (*MembershipRefundRequest, error)
	ListMembershipRefunds(uint, uint) ([]MembershipRefundRequest, error)
	CompleteMembershipRefund(string) error
}

// MembershipRefundFunds persists request, actual refund and termination intent atomically.
type MembershipRefundFunds interface {
	CommitMembershipRefund(context.Context, MembershipRefundInput) (*MembershipRefundRequest, error)
	ListMembershipRefunds(uint, uint) ([]MembershipRefundRequest, error)
	CompleteMembershipRefund(string) error
}

// MembershipRefundRetry persists fenced rights-only attempts; manual bypasses backoff, never an active lease.
// Claim returns nil when another worker owns the request or the automatic budget is exhausted.
// Finish accepts only the claimed version, so an expired worker cannot overwrite a newer attempt.
type MembershipRefundRetry interface {
	ClaimMembershipRefund(context.Context, MembershipRefundRequest, time.Time, bool) (*MembershipRefundRequest, error)
	FinishMembershipRefund(context.Context, MembershipRefundRequest, time.Time, bool) error
}

// MembershipRefundRetryLimit bounds automatic attempts, including crash/write-failure attempts.
const MembershipRefundRetryLimit uint = 5

// MembershipRefundRetryLease bounds ownership of local, idempotent rights disposal.
const MembershipRefundRetryLease = 2 * time.Minute

// MembershipRefundRetryDelay returns capped exponential backoff for a one-based attempt number.
func MembershipRefundRetryDelay(attempt uint) time.Duration {
	if attempt == 0 {
		attempt = 1
	}
	if attempt > 7 {
		attempt = 7
	}
	return time.Minute * time.Duration(1<<(attempt-1))
}

// MembershipRefundAdmin is the authenticated combined use case consumed by admin HTTP.
type MembershipRefundAdmin interface {
	Execute(context.Context, MembershipRefundInput) (*MembershipRefundRequest, error)
	Requests(context.Context, uint, uint) ([]MembershipRefundRequest, error)
}

// RefundGuard must run on the transaction-bound store before any refund write.
type RefundGuard interface{ LockRefundScope(uint) error }

// Store 是订单应用层所需的完整持久化端口。
// 事务通过 WithinTransaction 暴露模块端口，不向应用层泄漏 ORM 连接。
type Store interface {
	Create(order *orderdomain.Order, items []orderdomain.OrderItem) error
	GetByID(id uint) (*orderdomain.Order, error)
	GetByIDs(ids []uint) ([]orderdomain.Order, error)
	ResolveReceiverEmailByOrderID(orderID uint) (string, error)
	GetByIDAndUser(id uint, userID uint) (*orderdomain.Order, error)
	GetByOrderNoAndUser(orderNo string, userID uint) (*orderdomain.Order, error)
	GetAnyByOrderNoAndUser(orderNo string, userID uint) (*orderdomain.Order, error)
	GetByIDAndGuest(id uint, email, password string) (*orderdomain.Order, error)
	GetByOrderNoAndGuest(orderNo, email, password string) (*orderdomain.Order, error)
	GetAnyByOrderNoAndGuest(orderNo, email, password string) (*orderdomain.Order, error)
	GetByIDAndUserScoped(id uint, userID uint, scope TenantScope) (*orderdomain.Order, error)
	GetByOrderNoAndUserScoped(orderNo string, userID uint, scope TenantScope) (*orderdomain.Order, error)
	GetAnyByOrderNoAndUserScoped(orderNo string, userID uint, scope TenantScope) (*orderdomain.Order, error)
	GetByIDAndGuestScoped(id uint, email, password string, scope TenantScope) (*orderdomain.Order, error)
	GetByOrderNoAndGuestScoped(orderNo, email, password string, scope TenantScope) (*orderdomain.Order, error)
	GetAnyByOrderNoAndGuestScoped(orderNo, email, password string, scope TenantScope) (*orderdomain.Order, error)
	ListChildren(parentID uint) ([]orderdomain.Order, error)
	ListByUser(filter ListFilter) ([]orderdomain.Order, int64, error)
	StatsByUser(filter ListFilter) (map[string]int64, error)
	ListByUserScoped(filter ListFilter, scope TenantScope) ([]orderdomain.Order, int64, error)
	StatsByUserScoped(filter ListFilter, scope TenantScope) (map[string]int64, error)
	ListByGuest(email, password string, page, pageSize int) ([]orderdomain.Order, int64, error)
	ListByGuestScoped(email, password string, page, pageSize int, scope TenantScope) ([]orderdomain.Order, int64, error)
	ListAdmin(filter ListFilter) ([]orderdomain.Order, int64, error)
	UpdateStatus(id uint, status string, updates map[string]interface{}) error
	CountOrderItemsByProduct(productID uint) (int64, error)
	CountPendingByUserID(userID uint) (int64, error)
	LockRiskKeys(keys []string) error
	CountPendingGuestByRiskIP(riskIP string) (int64, error)
	CountPendingMemberByRiskIP(riskIP string) (int64, error)
	SumPendingGuestQuantityByRiskIP(riskIP string, productIDs []uint) (map[uint]int64, error)
	UpdateFields(id uint, updates map[string]interface{}) error
	UpdateChildrenStatus(parentID uint, targetStatus string, now time.Time) (int64, error)
	UpdateFieldsWhereWalletPaid(id uint, updates map[string]interface{}) (int64, error)
	GetByIDForUpdate(id uint) (*orderdomain.Order, error)
	GetByIDForUpdateWithChildren(id uint) (*orderdomain.Order, error)

	CreateRefundRecord(record *orderdomain.OrderRefundRecord) error
	GetRefundRecordByID(id uint) (*orderdomain.OrderRefundRecord, error)
	GetRefundRecordByIDForUpdate(id uint) (*orderdomain.OrderRefundRecord, error)
	UpdateRefundRecordPaymentFee(id uint, refunded bool, amount money.Amount, updatedAt time.Time) error
	ListRefundRecordsByOrderIDs(orderIDs []uint) ([]orderdomain.OrderRefundRecord, error)
	ListRefundRecordsAdmin(filter RefundRecordListFilter) ([]orderdomain.OrderRefundRecord, int64, error)

	WithinTransaction(fn func(Transaction) error) error
}

// ResellerOrderStore 是创建订单时写入分销快照所需的最小端口。
type ResellerOrderStore interface {
	CreateOrderSnapshot(snapshot *resellerdomain.OrderSnapshot) error
}

// Transaction 是已打开事务的订单工作单元。
// 每个方法只暴露相应领域端口，保证事务原子性同时隔离 GORM。
type Transaction interface {
	Orders() Store
	Products() productcontract.Repository
	ProductSKUs() productcontract.SKURepository
	CardSecrets() cardsecretcontract.Repository
	Coupons() couponcontract.Repository
	CouponUsages() couponcontract.UsageRepository
	Fulfillments() fulfillmentcontract.Store
	Wallets() walletcontract.Transaction
	Affiliates() affiliatecontract.Store
	ResellerOrders() ResellerOrderStore
	ResellerAccounting() resellercontract.AccountingLedgerStore
	ExpirePendingPaymentsByOrderIDs(orderIDs []uint, expiredAt time.Time) (int64, error)
}
