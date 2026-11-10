package contract

import (
	"context"
	"errors"
	"time"

	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
)

// AdminLedgerReader reads ledger facts only for order-item IDs already authorized by the caller.
// OrderLedger accepts no product/current-configuration input and returns safe, historical DTOs.
type AdminLedgerReader interface {
	OrderLedger(context.Context, []uint) ([]AdminItemLedger, error)
}

// AdminItemLedger is the administrator-only ledger enrichment for one authorized order item.
// State is pending, failed or recorded; empty sources never imply delivery or group admission.
type AdminItemLedger struct {
	ItemID    uint                   `json:"item_id"`
	State     string                 `json:"state"`
	Sources   []AdminLedgerSource    `json:"sources"`
	Effective []AdminLedgerEffective `json:"effective"`
}

// AdminLedgerSource exposes historical entitlement facts, never operational errors or credentials.
type AdminLedgerSource struct {
	TargetID              uint      `json:"target_id"`
	BeneficiaryTelegramID string    `json:"beneficiary_telegram_id"`
	Kind                  string    `json:"kind"`
	Months                int64     `json:"months"`
	PaidAt                time.Time `json:"paid_at"`
	Status                string    `json:"status"`
}

// AdminLedgerEffective describes the aggregate entitlement for the source's beneficiary and target.
// Permanent entitlements have no expiration; PendingRestore is not Telegram group membership.
type AdminLedgerEffective struct {
	TargetID              uint       `json:"target_id"`
	BeneficiaryTelegramID string     `json:"beneficiary_telegram_id"`
	Permanent             bool       `json:"permanent"`
	ExpiresAt             *time.Time `json:"expires_at,omitempty"`
	PendingRestore        bool       `json:"pending_restore"`
}

// AdminOrderStateReader reads independent rights/member/invitation evidence for authorized item IDs.
// It never mutates orders or contacts Telegram; missing evidence is unconfirmed.
type AdminOrderStateReader interface {
	OrderStates(context.Context, []uint, time.Time) ([]AdminOrderGroupState, error)
}

// AdminOrderStateMatcher resolves matching purchase IDs before the order store counts/pages.
// Implementations must require every non-empty dimension to match the same group row.
type AdminOrderStateMatcher interface {
	MatchingOrderItems(context.Context, AdminOrderStateFilter, time.Time) ([]uint, error)
}

// AdminOrderGroupState keeps purchased targets separate, including mixed multi-group outcomes.
type AdminOrderGroupState struct {
	ItemID                uint       `json:"item_id"`
	TargetID              uint       `json:"target_id"`
	BeneficiaryTelegramID string     `json:"beneficiary_telegram_id"`
	Rights                string     `json:"rights"`
	Permanent             bool       `json:"permanent"`
	ExpiresAt             *time.Time `json:"expires_at,omitempty"`
	MemberRemoved         bool       `json:"member_removed"`
	MemberRemoval         string     `json:"member_removal"`
	InviteCleanup         string     `json:"invite_cleanup"`
}

// AdminOrderStateFilter requires every non-empty dimension to match one item/group.
type AdminOrderStateFilter struct {
	Rights        string
	MemberRemoval string
	InviteCleanup string
}

// Empty reports whether no membership evidence constraint was requested.
func (filter AdminOrderStateFilter) Empty() bool {
	return filter.Rights == "" && filter.MemberRemoval == "" && filter.InviteCleanup == ""
}

// Validate returns ErrInvalid for an unsupported enum, otherwise nil.
func (filter AdminOrderStateFilter) Validate() error {
	if !orderStateAllowed(filter.Rights, "none", "pending", "active", "expired", "revoked", "unconfirmed") ||
		!orderStateAllowed(filter.MemberRemoval, "none", "pending", "removed", "failed", "present", "not_required", "unknown", "unconfirmed", "mixed") ||
		!orderStateAllowed(filter.InviteCleanup, "none", "pending", "succeeded", "failed", "not_required", "unknown", "unconfirmed", "mixed") {
		return ErrInvalid
	}
	return nil
}

// Matches evaluates one immutable purchase target without combining different groups.
func (filter AdminOrderStateFilter) Matches(row AdminOrderGroupState) bool {
	return (filter.Rights == "" || filter.Rights == row.Rights) &&
		(filter.MemberRemoval == "" || filter.MemberRemoval == row.MemberRemoval) &&
		(filter.InviteCleanup == "" || filter.InviteCleanup == row.InviteCleanup)
}

// orderStateAllowed checks value against the dimension's explicit allowed enums.
func orderStateAllowed(value string, allowed ...string) bool {
	if value == "" {
		return true
	}
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

// PaidGrant 引用可信付款事实；PaidAt 是持久化成功时点，PaymentID=0 仅允许全额钱包付款。
type PaidGrant struct {
	OrderID   uint
	PaymentID uint
	PaidAt    time.Time
}

// PaidGranter 原子提交来源、投影、恢复任务和系统履约，失败不允许部分成功。
type PaidGranter interface {
	GrantPaid(context.Context, PaidGrant) error
}

var (
	// ErrPaymentEvidence 表示订单或付款证据不匹配，禁止授予。
	ErrPaymentEvidence = errors.New("membership payment evidence mismatch")
	// ErrSystemFulfillmentUnavailable 表示可靠履约端口未装配，必须失败关闭。
	ErrSystemFulfillmentUnavailable = errors.New("membership system fulfillment unavailable")
	// ErrPermanentFullyCovered 表示全部套餐目标已有永久资格。
	ErrPermanentFullyCovered = errors.New("membership targets fully covered by permanent entitlement")
	// ErrPermanentPartiallyCovered 表示部分目标已有永久资格，须人工确认。
	ErrPermanentPartiallyCovered = errors.New("membership targets partially covered by permanent entitlement")
)

// Ledger 管理内部已授权撤权及按观察时刻重算；不根据退款金额推断撤权。
type Ledger interface {
	RevokeSource(context.Context, d.SourceKey, time.Time) error
	Recalculate(context.Context, d.EntitlementScope, time.Time) (*d.EntitlementProjection, error)
}
