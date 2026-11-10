package contract

import (
	"context"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
)

// RefundNotice is the cross-module durable event written in the existing refund transaction.
type RefundNotice d.RefundNotice

// TableName returns the shared event table without exposing membership internals to order.
func (RefundNotice) TableName() string { return "telegram_membership_refund_notices" }

// RefundSelection is an administrator's explicit item choice, never an inferred monetary allocation.
type RefundSelection struct {
	RefundID        uint   `json:"refund_id,string"`
	OrderItemID     uint   `json:"order_item_id,string"`
	AccountID       uint   `json:"account_id,string"`
	ExpectedVersion uint64 `json:"expected_version,string"`
	SnapshotHash    string `json:"snapshot_hash"`
	SourceIDs       []uint `json:"source_ids"`
	ActorAdminID    uint   `json:"-"`
}

// RefundCandidate contains verified local evidence plus an immutable item/source selection.
type RefundCandidate struct {
	RefundSelection
	OrderID               uint  `json:"order_id,string"`
	BeneficiaryTelegramID int64 `json:"beneficiary_telegram_id,string"`
	Disposed              bool  `json:"disposed"`
}

// RefundAdmin exposes only authorized local evidence and explicit disposition; it never sends money.
type RefundAdmin interface {
	RefundCandidates(context.Context, uint) ([]RefundCandidate, error)
	DisposeRefund(context.Context, RefundSelection) error
}

// LifecycleAdmin offers existing lifecycle execution diagnostics and guarded recovery commands.
type LifecycleAdmin interface {
	Tasks(context.Context, uint, uint, int) ([]LifecycleTask, error)
	Recover(context.Context, LifecycleRecovery) error
}
