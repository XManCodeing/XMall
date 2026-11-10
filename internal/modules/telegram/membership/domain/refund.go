package domain

import "time"

// RefundNotice is a durable action-needed event, committed with the local refund fact.
// It deliberately contains no item attribution or entitlement decision.
type RefundNotice struct {
	Version   uint64    `gorm:"not null;default:1" json:"version,string"`
	RefundID  uint      `gorm:"primaryKey;autoIncrement:false" json:"refund_id,string"`
	OrderID   uint      `gorm:"not null;index" json:"order_id,string"`
	CreatedAt time.Time `gorm:"not null" json:"created_at"`
}

// TableName returns the refund action queue table.
func (RefundNotice) TableName() string { return "telegram_membership_refund_notices" }

// RefundDisposition preserves the authenticated decision and exact historical source selection.
// A repeated refund/item command cannot revoke another purchase or replay platform effects.
type RefundDisposition struct {
	ID                    uint   `gorm:"primaryKey" json:"id,string"`
	RefundID              uint   `gorm:"not null;uniqueIndex:uidx_membership_refund_disposition,priority:1" json:"refund_id,string"`
	OrderItemID           uint   `gorm:"not null;uniqueIndex:uidx_membership_refund_disposition,priority:2" json:"order_item_id,string"`
	ActorAdminID          uint   `gorm:"not null" json:"actor_admin_id,string"`
	AccountID             uint   `gorm:"not null" json:"account_id,string"`
	BeneficiaryTelegramID int64  `gorm:"not null" json:"beneficiary_telegram_id,string"`
	SnapshotHash          string `gorm:"size:64;not null" json:"snapshot_hash"`
	SourceIDs             []uint `gorm:"serializer:json;type:text;not null" json:"source_ids"`
	ExpectedVersion       uint64 `gorm:"not null" json:"expected_version,string"`
	// ExecutionVersion distinguishes durable bindings from historical audits without them.
	ExecutionVersion uint              `gorm:"not null;default:0" json:"-"`
	Executions       []RefundExecution `gorm:"serializer:json;type:text" json:"-"`
	CreatedAt        time.Time         `gorm:"not null" json:"created_at"`
}

// RefundExecution anchors one refund item's actual cleanup cycle and member tasks.
// TelegramID is the historical physical identity (including explicitly mapped identities).
// Covered records the transaction-time decision, never an assumed removal success.
type RefundExecution struct {
	TargetID           uint
	TelegramID         int64
	EntitlementVersion uint64
	CleanupCycleID     uint
	Covered            bool
	Members            []RefundMemberExecution
}

// RefundMemberExecution preserves the exact scheduled route and task, not invite provenance.
// OperationID zero means no task was proven; readers must keep that outcome unconfirmed.
type RefundMemberExecution struct {
	BotID        uint
	ChatID       int64
	RouteVersion uint64
	OperationID  uint
}

// TableName returns the immutable refund disposition audit table.
func (RefundDisposition) TableName() string { return "telegram_membership_refund_dispositions" }
