package contract

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	paymentdomain "github.com/XManCodeing/XMall/internal/modules/payment/domain"
)

var (
	ErrPaymentCreateFailed      = errors.New("payment create failed")
	ErrPaymentCreationUncertain = errors.New("payment creation in progress or outcome unknown; query existing payment before retry")
	ErrReplayOrderInvalid       = errors.New("payment replay requires a payable root order")
)

// PaymentAttemptRetryError describes a terminal attempt and the deterministic key
// for an explicitly requested successor; replaying the original never dispatches.
type PaymentAttemptRetryError struct {
	OperationKey string
}

// Error returns a diagnostic without exposing operation keys.
func (e *PaymentAttemptRetryError) Error() string {
	return "payment attempt unavailable; request a new attempt"
}

// Unwrap preserves the creation-failed classification for callers.
func (e *PaymentAttemptRetryError) Unwrap() error { return ErrPaymentCreateFailed }

// PaymentAttemptRetry derives a successor from a persisted attempt's immutable key
// and ID. Missing attempts or keys retain the legacy creation-failed classification.
func PaymentAttemptRetry(payment *paymentdomain.Payment) error {
	if payment == nil || payment.OperationKey == nil || *payment.OperationKey == "" {
		return ErrPaymentCreateFailed
	}
	key := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", *payment.OperationKey, payment.ID)))
	return &PaymentAttemptRetryError{OperationKey: fmt.Sprintf("payment-retry:%x", key)}
}

// ValidatePaymentReplay evaluates authoritative order and attempt snapshots at now.
// It performs no I/O: nil means receipt/checkout replay is allowed; a retry error
// authorizes only an explicit successor, while uncertain outcomes remain blocked.
func ValidatePaymentReplay(order *orderdomain.Order, previous *paymentdomain.Payment, payments []paymentdomain.Payment, now time.Time) error {
	if order == nil || previous == nil {
		return ErrReplayOrderInvalid
	}
	if previous.Status == constants.PaymentStatusSuccess {
		// Preserve authoritative receipts, including anomalies. Presentation must
		// not expose completed attempts as payable checkouts.
		return nil
	}
	for _, attempt := range payments {
		if (attempt.CreationState == "processing" || attempt.CreationState == "unknown") && attempt.Status != constants.PaymentStatusSuccess {
			return ErrPaymentCreationUncertain
		}
	}
	if order.ParentID != nil || order.Status != constants.OrderStatusPendingPayment || (order.ExpiresAt != nil && !order.ExpiresAt.After(now)) {
		return ErrReplayOrderInvalid
	}
	if paymentAttemptRetryable(previous, now) {
		// Session-write failure or re-entry yields the same successor; the old
		// key remains immutable and cannot create a duplicate attempt.
		return PaymentAttemptRetry(previous)
	}
	if previous.CreationState != "ready" {
		return ErrPaymentCreationUncertain
	}
	if previous.Status != constants.PaymentStatusPending && previous.Status != constants.PaymentStatusInitiated {
		return ErrPaymentCreateFailed
	}
	return nil
}
