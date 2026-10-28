package common

import (
	"context"
	"sync/atomic"

	paymentcontract "github.com/XManCodeing/XMall/internal/modules/payment/contract"
)

type creationAttemptKey struct{}
type creationAttempt struct{ dispatched atomic.Bool }

// TrackCreation is opt-in: only adapters whose complete execution path marks
// the dispatch boundary may prove that an error happened before submission.
// Never infer this fact from an error category (config/request/auth, etc.).
func TrackCreation(ctx context.Context) (context.Context, func(error) error) {
	if ctx == nil {
		ctx = context.Background()
	}
	attempt := &creationAttempt{}
	return context.WithValue(ctx, creationAttemptKey{}, attempt), func(err error) error {
		if err != nil && !attempt.dispatched.Load() {
			return paymentcontract.GatewayNotSubmitted(err)
		}
		return err
	}
}

// MarkCreationDispatched must run BEFORE handing a payment request to an HTTP
// client or SDK. Even transport errors are uncertain after this boundary.
// Authentication/certificate downloads alone do not create a payment.
func MarkCreationDispatched(ctx context.Context) {
	if ctx != nil {
		if attempt, ok := ctx.Value(creationAttemptKey{}).(*creationAttempt); ok {
			attempt.dispatched.Store(true)
		}
	}
}
