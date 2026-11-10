package contract

import (
	"errors"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	paymentdomain "github.com/XManCodeing/XMall/internal/modules/payment/domain"
)

// paymentAttemptRetryable authorizes a successor only for local non-submission or a ready terminal attempt.
// payment is a persisted snapshot and now is the expiry clock; unknown creation never qualifies.
func paymentAttemptRetryable(payment *paymentdomain.Payment, now time.Time) bool {
	return payment.CreationState == "not_submitted" || (payment.CreationState == "ready" &&
		(payment.SupersededAt != nil || payment.Status == constants.PaymentStatusFailed || payment.Status == constants.PaymentStatusExpired ||
			(payment.ExpiredAt != nil && !payment.ExpiredAt.After(now))))
}

// ResolvePaymentReplay follows persisted deterministic successors without creating attempts.
// order/previous/payments must share a locked snapshot; now is the validation clock.
// It returns the latest replayable attempt or one explicit retry key, even after session loss.
func ResolvePaymentReplay(order *orderdomain.Order, previous *paymentdomain.Payment, payments []paymentdomain.Payment, now time.Time) (*paymentdomain.Payment, error) {
	byKey := make(map[string]*paymentdomain.Payment, len(payments))
	for index := range payments {
		attempt := &payments[index]
		if attempt.OperationKey != nil {
			byKey[*attempt.OperationKey] = attempt
		}
	}
	for depth := 0; depth <= len(payments); depth++ {
		// Locate already-persisted successors before validating order state so a
		// paid successor remains an authoritative receipt after the session expires.
		if previous != nil && previous.Status != constants.PaymentStatusSuccess && paymentAttemptRetryable(previous, now) {
			var retry *PaymentAttemptRetryError
			if errors.As(PaymentAttemptRetry(previous), &retry) {
				if next := byKey[retry.OperationKey]; next != nil {
					if next.OrderID != previous.OrderID || next.OperationHash != previous.OperationHash || next.ID <= previous.ID {
						return nil, ErrPaymentCreationUncertain
					}
					previous = next
					continue
				}
			}
		}
		return previous, ValidatePaymentReplay(order, previous, payments, now)
	}
	return nil, ErrPaymentCreationUncertain
}
