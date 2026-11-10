package domain

import "time"

const (
	// ReceiptEntitlementRecorded 仅表示权益账本已记录，绝不等价于 Telegram 入群。
	ReceiptEntitlementRecorded = "entitlement_recorded"
	// ReceiptDelivered 表示普通人工或卡密项真实交付完成。
	ReceiptDelivered = "delivered"
	// SystemMembershipType 只用于交付记录，不是商品 fulfillment_type 枚举。
	SystemMembershipType = "system_membership"
)

// ItemReceipt 是叶订单项唯一的不可变履约回执；会员回执与权益账本同事务写入。
// 不存邀请 URL、交付伪文本或管理员身份；普通项通过 FulfillmentID 追溯真实交付记录。
type ItemReceipt struct {
	OrderItemID   uint      `gorm:"primaryKey;autoIncrement:false" json:"order_item_id"`
	OrderID       uint      `gorm:"not null;index" json:"order_id"`
	Kind          string    `gorm:"size:32;not null" json:"kind"`
	Status        string    `gorm:"size:32;not null" json:"status"`
	FulfillmentID *uint     `gorm:"index" json:"fulfillment_id,omitempty"`
	PaidAt        time.Time `gorm:"not null" json:"paid_at"`
	RecordedAt    time.Time `gorm:"not null" json:"recorded_at"`
}

// TableName 返回 item 级可靠回执表名，无参数。
func (ItemReceipt) TableName() string { return "fulfillment_item_receipts" }
