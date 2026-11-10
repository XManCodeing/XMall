package domain

import (
	"time"

	"github.com/XManCodeing/XMall/internal/shared/money"
	"github.com/shopspring/decimal"
)

// MembershipRefundRequest durably binds one idempotent money operation to explicit purchase termination.
// ItemIDs are administrator selections, never inferred from the monetary amount.
type MembershipRefundRequest struct {
	ID               uint       `gorm:"primaryKey" json:"id,string"`
	RequestKey       string     `gorm:"size:96;uniqueIndex;not null" json:"request_key"`
	Fingerprint      string     `gorm:"size:64;not null" json:"-"`
	ActorAdminID     uint       `json:"-"`
	Method           string     `gorm:"size:16;not null" json:"method"`
	Amount           string     `gorm:"size:64" json:"amount"`
	Remark           string     `gorm:"type:text" json:"remark"`
	ExistingRefundID uint       `json:"existing_refund_id,string"`
	OrderID          uint       `gorm:"index;not null" json:"order_id,string"`
	RefundID         uint       `gorm:"index;not null" json:"refund_id,string"`
	ItemIDs          []uint     `gorm:"serializer:json;type:text;not null" json:"item_ids"`
	FundsState       string     `gorm:"size:32;not null" json:"funds_state"`
	RightsState      string     `gorm:"size:32;index;not null" json:"rights_state"`
	RetryAttempts    uint       `gorm:"not null;default:0" json:"retry_attempts"`
	RetryVersion     uint64     `gorm:"not null;default:0" json:"-"`
	RetryAfter       *time.Time `gorm:"index" json:"retry_after,omitempty"`
	RetryLeaseUntil  *time.Time `json:"-"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// OrderRefundRecord 退款记录
type OrderRefundRecord struct {
	ID                       uint         `gorm:"primarykey" json:"id"`
	UserID                   uint         `gorm:"index;not null;default:0" json:"user_id"`
	GuestEmail               string       `gorm:"index;type:varchar(255)" json:"guest_email,omitempty"`
	OrderID                  uint         `gorm:"index;not null" json:"order_id"`
	Type                     string       `gorm:"index;type:varchar(32);not null" json:"type"`
	Amount                   money.Amount `gorm:"type:decimal(20,2);not null;default:0" json:"amount"`
	PaymentFeeRefunded       bool         `gorm:"not null;default:false" json:"payment_fee_refunded"`
	PaymentFeeRefundedAmount money.Amount `gorm:"type:decimal(20,2);not null;default:0" json:"payment_fee_refunded_amount"`
	Currency                 string       `gorm:"type:varchar(16);not null;default:''" json:"currency"`
	Remark                   string       `gorm:"type:text" json:"remark,omitempty"`
	CreatedAt                time.Time    `gorm:"index" json:"created_at"`
	UpdatedAt                time.Time    `gorm:"index" json:"updated_at"`
	DeletedAt                *time.Time   `gorm:"index" json:"-"`
}

// TableName 指定表名
func (OrderRefundRecord) TableName() string {
	return "order_refund_records"
}

// CalculatePaymentFeeRefundAmount allocates the original payment fee to one
// refund. It uses cumulative values so the final refund absorbs rounding
// differences without ever returning more than the original payment fee.
func CalculatePaymentFeeRefundAmount(
	paymentAmount decimal.Decimal,
	paymentFee decimal.Decimal,
	refundedPrincipalBefore decimal.Decimal,
	refundedFeeBefore decimal.Decimal,
	refundAmount decimal.Decimal,
) decimal.Decimal {
	paymentAmount = paymentAmount.Round(2)
	paymentFee = paymentFee.Round(2)
	refundedPrincipalBefore = refundedPrincipalBefore.Round(2)
	refundedFeeBefore = refundedFeeBefore.Round(2)
	refundAmount = refundAmount.Round(2)
	if paymentAmount.LessThanOrEqual(decimal.Zero) ||
		paymentFee.LessThanOrEqual(decimal.Zero) ||
		refundAmount.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero
	}

	if refundedPrincipalBefore.IsNegative() {
		refundedPrincipalBefore = decimal.Zero
	}
	if refundedFeeBefore.IsNegative() {
		refundedFeeBefore = decimal.Zero
	}
	if refundedPrincipalBefore.GreaterThan(paymentAmount) {
		refundedPrincipalBefore = paymentAmount
	}
	if refundedFeeBefore.GreaterThan(paymentFee) {
		refundedFeeBefore = paymentFee
	}

	cumulativePrincipal := refundedPrincipalBefore.Add(refundAmount)
	if cumulativePrincipal.GreaterThan(paymentAmount) {
		cumulativePrincipal = paymentAmount
	}
	targetRefundedFee := paymentFee.Mul(cumulativePrincipal).Div(paymentAmount).Round(2)
	if cumulativePrincipal.GreaterThanOrEqual(paymentAmount) {
		targetRefundedFee = paymentFee
	}

	current := targetRefundedFee.Sub(refundedFeeBefore).Round(2)
	if current.IsNegative() {
		return decimal.Zero
	}
	remaining := paymentFee.Sub(refundedFeeBefore).Round(2)
	if current.GreaterThan(remaining) {
		return remaining
	}
	return current
}
