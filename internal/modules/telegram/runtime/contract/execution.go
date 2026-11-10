package contract

import "context"

// ExecutionFence is scoped to a single leased runtime generation. Business
// consumers receive no token or lease handle; they can only revalidate the
// current execution immediately before an external side effect.
type ExecutionFence interface {
	Check(context.Context, uint, uint64) error
}

type executionFenceKey struct{}

// InboxExecutionStore persists entry into a claimed update's business handler.
// An expired marked claim has an unknown result and must not be auto-replayed.
// FinishInbox clears the marker when a normal result has been observed.
type InboxExecutionStore interface {
	MarkInboxExecution(context.Context, uint, string) error
}

// DeliveryExecutionStore records entry into transport for an owned claim.
// An expired marked mutable operation is unknown, not an automatic retry.
// FinishDelivery or FinishPanel replaces the marker with an observed result.
type DeliveryExecutionStore interface {
	MarkDeliveryExecution(context.Context, uint, string) error
}

func WithExecutionFence(ctx context.Context, fence ExecutionFence) context.Context {
	return context.WithValue(ctx, executionFenceKey{}, fence)
}

// CheckExecutionFence fails closed outside a supervised bot worker. A database
// row saying "running" is not sufficient evidence of Redis lease ownership.
func CheckExecutionFence(ctx context.Context, bot uint, generation uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fence, ok := ctx.Value(executionFenceKey{}).(ExecutionFence)
	if !ok || fence == nil || bot == 0 || generation == 0 {
		return ErrStale
	}
	return fence.Check(ctx, bot, generation)
}
