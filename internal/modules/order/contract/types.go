package contract

import (
	d "github.com/XManCodeing/XMall/internal/modules/order/domain"
	"time"
)

// FulfillmentOrder 是跨模块只读订单事实，沿用订单持久字段但不是类型别名。
type FulfillmentOrder d.Order

// TableName 返回真实订单表名，无参数。
func (FulfillmentOrder) TableName() string { return "orders" }

// FulfillmentItem 是跨模块真实订单项事实，不接受调用方构造快照。
type FulfillmentItem d.OrderItem

// TableName 返回真实订单项表名，无参数。
func (FulfillmentItem) TableName() string { return "order_items" }

// RefundFact exposes an immutable local refund record to authorized cross-module consumers.
type RefundFact d.OrderRefundRecord

// TableName returns the existing local refund fact table.
func (RefundFact) TableName() string { return "order_refund_records" }

// ListFilter 查询订单列表的过滤条件。
type ListFilter struct {
	// MembershipItemIDs constrains root orders before COUNT/paging when MembershipStateFiltered is true.
	MembershipItemIDs       []uint
	MembershipStateFiltered bool
	Page                    int
	PageSize                int
	UserID                  uint
	UserKeyword             string
	Status                  string
	// EffectivePendingAt selects only pending, unexpired parent orders at this instant.
	// Used by read-only pending lists and summaries before count and pagination.
	EffectivePendingAt *time.Time
	OrderNo            string
	GuestEmail         string
	ProductKeyword     string
	CreatedFrom        *time.Time
	CreatedTo          *time.Time
	SortBy             string
	SortOrder          string
}

// TenantScope 表示前台订单查询的分销租户范围。
//
// ResellerID == nil 明确表示主站范围: orders.reseller_id IS NULL。
// 后台列表不要使用该结构，后台 nil 语义是“不按分销商过滤”。
type TenantScope struct {
	ResellerID *uint
}

// RefundRecordListFilter 查询订单退款记录的过滤条件。
type RefundRecordListFilter struct {
	Page           int
	PageSize       int
	UserID         uint
	UserKeyword    string
	OrderNo        string
	GuestEmail     string
	ProductKeyword string
	CreatedFrom    *time.Time
	CreatedTo      *time.Time
}
